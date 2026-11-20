package server

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"k8s.io/mount-utils"
)

// OSS client versions in the existing images derive metric paths from
// /run/fuse.ossfs/<key>/globalmount. Keep that client-side convention without
// changing MountRequest.Target, PVC projections, or host-side mount identity.
// The data path is a second standard PVC projection, not a symlink (the client
// canonicalizes paths). Only its metrics directory uses a container-local link.
func prepareFileClientMetrics(op *mounter.MountOperation, attachRoot, metricsRoot string, mounts mount.Interface) error {
	key := filepath.Base(filepath.Dir(op.Target))
	alias := filepath.Join(attachRoot, key, "globalmount")
	entries, err := mounts.List()
	if err != nil {
		return err
	}
	projected := false
	for _, entry := range entries {
		if entry.Path == alias {
			projected = true
		}
	}
	if !projected {
		return fmt.Errorf("OSS client metrics require a real PVC projection at %s; a symlink is insufficient", alias)
	}
	if err := exactSymlink(op.MetricsPath, filepath.Join(metricsRoot, key)); err != nil {
		return err
	}
	op.ClientTarget = alias
	return nil
}

func exactSymlink(target, name string) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	err := os.Symlink(target, name)
	if err == nil {
		return nil
	}
	if !os.IsExist(err) {
		return err
	}
	existing, readErr := os.Readlink(name)
	if readErr != nil || existing != target {
		return fmt.Errorf("client metrics alias conflicts at %s; recreate sidecar", name)
	}
	return nil
}
