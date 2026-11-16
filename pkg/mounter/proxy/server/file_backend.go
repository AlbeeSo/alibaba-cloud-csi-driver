package server

import (
	"context"
	"fmt"
	"os"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"k8s.io/mount-utils"
)

// FileMounter returns the supervised process exit channel to the file transport.
// Socket mounts retain their existing monitoring path.
type FileMounter interface {
	MountFile(context.Context, *proxy.MountRequest) (<-chan error, error)
}

type FileBackend struct{ Mounter mount.Interface }

func (b FileBackend) Mount(ctx context.Context, req *proxy.MountRequest) (<-chan error, error) {
	d, ok := fstypeToDriver[req.Fstype].(FileMounter)
	if !ok {
		return nil, fmt.Errorf("driver does not support file transport")
	}
	if err := os.MkdirAll(req.Target, 0755); err != nil {
		return nil, err
	}
	return d.MountFile(ctx, req)
}

func (b FileBackend) Mounted(target string) (bool, error) {
	points, err := b.Mounter.List()
	if err != nil {
		return false, err
	}
	for _, p := range points {
		if p.Path == target {
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
	return mounterutils.SafeCleanupFuseMount(target, b.Mounter, true)
}
