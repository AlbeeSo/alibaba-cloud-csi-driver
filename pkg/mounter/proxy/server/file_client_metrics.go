package server

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
)

// OSS client versions in the existing images derive metric paths from
// /run/fuse.ossfs/<key>/globalmount. Keep that client-side convention without
// changing MountRequest.Target, PVC projections, or host-side mount identity.
// Both aliases live in the container filesystem; neither is an extra hostPath.
func prepareFileClientMetrics(op *mounter.MountOperation, attachRoot, metricsRoot string) error {
	key := "file-" + filevolume.ID(op.MetricsPath)
	alias := filepath.Join(attachRoot, key, "globalmount")
	if err := exactSymlink(op.Target, alias); err != nil {
		return err
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
