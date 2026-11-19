//go:build !windows

package oss

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
)

func TestControllerSidecarHintFromStaticPVAndDynamicParameters(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		name := "static-pv-without-storageclass"
		if dynamic {
			name = "dynamic-storageclass-parameters"
		}
		t.Run(name, func(t *testing.T) {
			req := fileRequest(t, 0, "ossfs2")
			attrs := req.VolumeContext
			attrs["runtimeclass"] = "runc"
			attrs[filevolume.MountModeKey] = "sidecar"
			controller := &controllerServer{metadata: &metadata.FakeProvider{}} // no client/manager: creating a FUSE Pod must not occur
			if dynamic {
				created, err := controller.CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: req.VolumeId, Parameters: attrs})
				require.NoError(t, err)
				attrs = created.Volume.VolumeContext
			}
			pv := &core.PersistentVolume{Spec: core.PersistentVolumeSpec{StorageClassName: "", PersistentVolumeSource: core.PersistentVolumeSource{CSI: &core.CSIPersistentVolumeSource{Driver: "ossplugin.csi.alibabacloud.com", VolumeHandle: req.VolumeId, VolumeAttributes: attrs}}}}
			response, err := controller.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeId: pv.Spec.CSI.VolumeHandle, NodeId: "node", VolumeContext: pv.Spec.CSI.VolumeAttributes, VolumeCapability: req.VolumeCapability})
			require.NoError(t, err)
			require.Empty(t, response.PublishContext)
		})
	}
}
