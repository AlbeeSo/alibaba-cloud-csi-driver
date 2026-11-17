package server

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"k8s.io/mount-utils"
)

// FileMounter returns the supervised process exit channel to the file transport.
// Socket mounts retain their existing monitoring path.
type FileMounter interface {
	MountFile(context.Context, *proxy.MountRequest, func(string) (bool, error)) (<-chan error, error)
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
	op := &mounter.MountOperation{Source: req.Source, Target: req.Target, FsType: req.Fstype, Options: req.Options, Args: req.MountFlags, Secrets: req.Secrets, VolumeID: req.VolumeID}
	op.MountReady = ready
	if err := m.ExtendedMount(ctx, op); err != nil {
		return nil, err
	}
	result, ok := op.MountResult.(FuseMountResult)
	if !ok {
		return nil, fmt.Errorf("missing FUSE process supervision")
	}
	return result.ExitChan, nil
}

type FileBackend struct{ Mounter mount.Interface }

func (b FileBackend) Mount(ctx context.Context, req *proxy.MountRequest) (<-chan error, error) {
	d, ok := fstypeToDriver[req.Fstype].(FileMounter)
	if !ok {
		return nil, fmt.Errorf("fstype %q does not support file transport", req.Fstype)
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
	mounted, err := b.Mounted(target)
	if err != nil || !mounted {
		return err
	}
	return unmountFileTarget(target)
}
