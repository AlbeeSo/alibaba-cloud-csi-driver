package filevolume

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func TestClientMetricsProjectionUsesSameClaimWithoutLookup(t *testing.T) {
	pod := &core.Pod{Spec: core.PodSpec{Volumes: []core.Volume{{Name: "data", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "claim"}}}}, InitContainers: []core.Container{{Name: SidecarName, VolumeMounts: []core.VolumeMount{
		{Name: "data", MountPath: "/pod-volumes/pv/mount", MountPropagation: ptr.To(core.MountPropagationHostToContainer)},
		{Name: "data", MountPath: ClientTarget("pv"), MountPropagation: ptr.To(core.MountPropagationHostToContainer)},
	}}}}}
	req := &csi.NodePublishVolumeRequest{TargetPath: "/pods/uid/volumes/kubernetes.io~csi/pv/mount"}
	require.NoError(t, ValidateClientMetricsProjection(pod, req, true))
	pod.Spec.InitContainers[0].VolumeMounts[1].ReadOnly = true
	require.ErrorContains(t, ValidateClientMetricsProjection(pod, req, true), "same PVC")
	pod.Spec.InitContainers[0].VolumeMounts[1].ReadOnly = false
	pod.Spec.InitContainers[0].VolumeMounts[1].Name = "missing"
	require.Error(t, ValidateClientMetricsProjection(pod, req, true))
	require.True(t, ClientMetricsEnabled([]string{"use_metrics"}))
	require.True(t, ClientMetricsEnabled([]string{"use_metrics=true"}))
	require.False(t, ClientMetricsEnabled([]string{"use_metrics=true", "use_metrics=false"}))
	require.False(t, ClientMetricsEnabled(nil))
}
