package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"golang.org/x/sys/unix"
	"k8s.io/mount-utils"
)

// FileMounter returns the supervised process exit channel to the file transport.
// Socket mounts retain their existing monitoring path.
type FileMounter interface {
	MountFile(context.Context, *proxy.MountRequest, func(string) (bool, error)) (<-chan error, error)
}

type FileFDMounter interface {
	MountFileFD(context.Context, *proxy.MountRequest, int, func(string, error)) (<-chan error, error)
	ReleaseFileFD(context.Context, string) error
}

// InitFileDrivers enables all linked drivers that implement the file transport.
// Request Fstype selects the driver; socket-mode allowlists do not apply here.
// Return the initialized names so shutdown terminates exactly these drivers.
func InitFileDrivers() []string {
	var names []string
	for name, driver := range nameToDriver {
		if _, ok := driver.(FileMounter); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	Init(names)
	return names
}

// MountFile uses the same interceptor entry point for every FUSE driver. Keep
// request-to-operation conversion here, rather than duplicating it per client.
func MountFile(ctx context.Context, m mounter.Mounter, req *proxy.MountRequest, ready func(string) (bool, error)) (<-chan error, error) {
	return MountFileFD(ctx, m, req, ready, 0)
}

func MountFileFD(ctx context.Context, m mounter.Mounter, req *proxy.MountRequest, ready func(string) (bool, error), fd int) (<-chan error, error) {
	return MountFileObserved(ctx, m, req, ready, fd, nil)
}

func MountFileObserved(ctx context.Context, m mounter.Mounter, req *proxy.MountRequest, ready func(string) (bool, error), fd int, observe func(string, error)) (<-chan error, error) {
	op := &mounter.MountOperation{Source: req.Source, Target: req.Target, FsType: req.Fstype, Options: req.Options, Args: req.MountFlags, Secrets: req.Secrets, VolumeID: req.VolumeID}
	op.FuseFd, op.Recovery, op.MetricsPath = fd, req.Recovery, req.MetricsPath
	if fd > 0 {
		op.Args = nil
	}
	op.MountReady = ready
	if observe != nil {
		op.OnProcessExit = func(err error) { observe("Recovering", err) }
		op.OnRecoverySuccess = func(_ int, _ error, _ int) { observe("Mounted", nil) }
		op.OnRecoveryFailed = func(_ error, err error, _ int) { observe("Failed", err) }
	}
	if err := m.ExtendedMount(ctx, op); err != nil {
		return nil, err
	}
	result, ok := op.MountResult.(FuseMountResult)
	if !ok {
		return nil, fmt.Errorf("missing FUSE process supervision")
	}
	return result.ExitChan, nil
}

type FileBackend struct {
	Mounter               mount.Interface
	StateRoot, TargetRoot string
	HealthStates          *sync.Map
	FDTargets             *sync.Map
}

type fileHealth struct {
	phase string
	err   error
}

func (b FileBackend) Health(target string) (string, error) {
	if b.HealthStates != nil {
		if value, ok := b.HealthStates.Load(target); ok {
			health := value.(fileHealth)
			return health.phase, health.err
		}
	}
	return "Mounted", nil
}

func (b FileBackend) Mount(ctx context.Context, req *proxy.MountRequest) (<-chan error, error) {
	copy := *req
	req = &copy
	if req.MetricsPath != "" {
		if b.StateRoot == "" {
			return nil, fmt.Errorf("missing metrics state root")
		}
		root, err := os.OpenRoot(b.StateRoot)
		if err != nil {
			return nil, err
		}
		dir, err := root.Open(req.MetricsPath)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		_ = dir.Close()
		req.MetricsPath = filepath.Join(b.StateRoot, filepath.FromSlash(req.MetricsPath))
	}
	d, ok := fstypeToDriver[req.Fstype].(FileMounter)
	if !ok {
		return nil, fmt.Errorf("fstype %q does not support file transport", req.Fstype)
	}
	if req.FdPassing {
		fdDriver, ok := d.(FileFDMounter)
		if !ok {
			return nil, fmt.Errorf("fstype %s cannot receive file-mode FUSE fd", req.Fstype)
		}
		if b.FDTargets == nil {
			return nil, fmt.Errorf("fd consumer registry is missing")
		}
		b.FDTargets.Store(req.Target, fdDriver)
		relative, err := filepath.Rel(b.TargetRoot, req.Target)
		if err != nil || strings.HasPrefix(relative, "..") {
			return nil, fmt.Errorf("invalid fd target")
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) != 2 || parts[1] != "mount" {
			return nil, fmt.Errorf("invalid fd target")
		}
		var desired filevolume.Desired
		if err := filevolume.ReadJSON(filevolume.RequestPath(b.StateRoot, parts[0]), &desired); err != nil {
			return nil, err
		}
		if err := desired.Validate(desired.PodUID); err != nil {
			return nil, err
		}
		if desired.Mount.VolumeID != req.VolumeID || desired.Mount.Target != relative {
			return nil, fmt.Errorf("fd request identity mismatch")
		}
		fd, err := filevolume.ReceiveFD(b.StateRoot, parts[0], desired.Generation, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("receive CSI FUSE fd: %w", err)
		}
		exit, err := fdDriver.MountFileFD(ctx, req, fd, func(phase string, err error) {
			if b.HealthStates != nil {
				b.HealthStates.Store(req.Target, fileHealth{phase: phase, err: err})
			}
		})
		if err != nil {
			_ = unix.Close(fd)
		}
		return exit, err
	}
	if err := os.MkdirAll(req.Target, 0755); err != nil {
		return nil, err
	}
	return d.MountFile(ctx, req, b.Mounted)
}

func (b FileBackend) Mounted(target string) (bool, error) {
	points, err := b.Mounter.List()
	if err != nil {
		return false, err
	}
	for _, p := range points {
		if p.Path == target && strings.HasPrefix(p.Type, "fuse") && p.Type != "fuse.virtiofs" {
			return true, nil
		}
	}
	return false, nil
}

func (b FileBackend) Unmount(target string) error {
	if b.FDTargets != nil {
		if driver, ok := b.FDTargets.Load(target); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return driver.(FileFDMounter).ReleaseFileFD(ctx, target)
		}
	}
	mounted, err := b.Mounted(target)
	if err != nil || !mounted {
		return err
	}
	return unmountFileTarget(target)
}
