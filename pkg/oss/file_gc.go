//go:build !windows

package oss

import (
	"context"
	"path/filepath"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"k8s.io/klog/v2"
)

func RunFileStateGC(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if err := filevolume.CollectOrphans(filevolume.Root(ossFileRoot), filepath.Join(utils.KubeletRootDir, "pods")); err != nil {
			klog.ErrorS(err, "File control state GC failed; retaining unverified entries")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
