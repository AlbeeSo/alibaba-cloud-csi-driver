//go:build !windows

package customfuse

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
)

var errFileConfigurationChanged = filevolume.ErrConfigurationChanged

func fileRoot() string { return filevolume.Root("/host/var/run/customfuse/file-volumes") }

func publishFile(req *csi.NodePublishVolumeRequest, opts *fuseOptions) error {
	if opts.AuthType != "" {
		return fmt.Errorf("file transport currently requires Secret credential passthrough")
	}
	if req.GetVolumeCapability().GetMount() == nil {
		return fmt.Errorf("file transport requires a filesystem volume")
	}
	return filevolume.Publish(fileRoot(), req.VolumeContext["csi.storage.k8s.io/pod.uid"], req.VolumeId, req.TargetPath, proxy.MountRequest{
		Source: opts.Source, Fstype: "customfuse", Options: opts.makeMountOptions(), Secrets: opts.Secrets,
	})
}
