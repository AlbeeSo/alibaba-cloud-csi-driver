package filevolume

import (
	"testing"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
)

func TestConsumerExactNamesAndAmbiguity(t *testing.T) {
	for _, name := range []string{SidecarName, CustomSidecarName} {
		pod := &core.Pod{Spec: core.PodSpec{InitContainers: []core.Container{{Name: "prepare"}, {Name: name, Command: []string{"custom-wrapper"}}}}}
		c, i, err := Consumer(pod)
		require.NoError(t, err)
		require.Equal(t, 1, i)
		require.Equal(t, name, c.Name)
	}
	for _, name := range []string{"example-csi-customfuse-storage", "mountproxy", CustomSidecarName + "-extra"} {
		c, _, err := Consumer(&core.Pod{Spec: core.PodSpec{InitContainers: []core.Container{{Name: name}}}})
		require.NoError(t, err)
		require.Nil(t, c)
	}
	_, _, err := Consumer(&core.Pod{Spec: core.PodSpec{InitContainers: []core.Container{{Name: SidecarName}, {Name: CustomSidecarName}}}})
	require.ErrorContains(t, err, "multiple")
	_, _, err = Consumer(&core.Pod{Spec: core.PodSpec{Containers: []core.Container{{Name: CustomSidecarName}}}})
	require.ErrorContains(t, err, "restartable init")
}

func TestModeAttributesRejectConflictingSpellings(t *testing.T) {
	value, err := Attribute(map[string]string{"runtimeClass": "rund"}, "runtimeclass")
	require.NoError(t, err)
	require.Equal(t, "rund", value)
	_, err = RequireSidecar(map[string]string{"runtimeclass": "runc", "runtimeClass": "rund"})
	require.ErrorContains(t, err, "conflicting")
	required, err := RequireSidecar(map[string]string{"MountMode": "sidecar"})
	require.NoError(t, err)
	require.True(t, required)
}
