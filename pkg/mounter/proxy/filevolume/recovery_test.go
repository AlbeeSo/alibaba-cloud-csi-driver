package filevolume

import (
	"testing"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
)

func TestRecoveryRequiresExplicitWritableFusectlProjection(t *testing.T) {
	pod := &core.Pod{Spec: core.PodSpec{InitContainers: []core.Container{{Name: SidecarName}}}}
	require.ErrorContains(t, ValidateRecoveryProjection(pod), "explicit writable HostPath")
	pod.Spec.Volumes = []core.Volume{{Name: "fusectl", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: FuseConnectionsDir}}}}
	pod.Spec.InitContainers[0].VolumeMounts = []core.VolumeMount{{Name: "fusectl", MountPath: FuseConnectionsDir, ReadOnly: true}}
	require.Error(t, ValidateRecoveryProjection(pod))
	pod.Spec.InitContainers[0].VolumeMounts[0].ReadOnly = false
	require.NoError(t, ValidateRecoveryProjection(pod))
	pod.Spec.InitContainers[0].VolumeMounts[0].SubPath = "one-connection"
	require.Error(t, ValidateRecoveryProjection(pod))
}
