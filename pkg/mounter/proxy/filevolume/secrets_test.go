package filevolume

import (
	"testing"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func TestSecretDirectoryExplicitProjection(t *testing.T) {
	fixture := func() *core.Pod {
		return &core.Pod{Spec: core.PodSpec{
			InitContainers: []core.Container{{Name: SidecarName, VolumeMounts: []core.VolumeMount{{Name: "credentials", MountPath: SecretRoot + "/credentials", ReadOnly: true}}}},
			Volumes:        []core.Volume{{Name: "credentials", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "credentials"}}}},
		}}
	}
	for _, test := range []struct {
		name   string
		change func(*core.Pod)
		want   string
	}{
		{"default keys", func(*core.Pod) {}, ""},
		{"explicit keys", func(p *core.Pod) {
			p.Spec.Volumes[0].Secret.Items = []core.KeyToPath{{Key: "AccessKeyId", Path: "AccessKeyId"}}
		}, ""},
		{"renamed key", func(p *core.Pod) {
			p.Spec.Volumes[0].Secret.Items = []core.KeyToPath{{Key: "AccessKeyId", Path: "renamed"}}
		}, "project AccessKeyId"},
		{"wrong secret", func(p *core.Pod) { p.Spec.Volumes[0].Secret.SecretName = "different" }, "explicitly mounted"},
		{"optional", func(p *core.Pod) { p.Spec.Volumes[0].Secret.Optional = ptr.To(true) }, "not be optional"},
		{"writable", func(p *core.Pod) { p.Spec.InitContainers[0].VolumeMounts[0].ReadOnly = false }, "read-only"},
		{"subpath", func(p *core.Pod) { p.Spec.InitContainers[0].VolumeMounts[0].SubPath = "credentials" }, "without subPath"},
		{"subpath expr", func(p *core.Pod) { p.Spec.InitContainers[0].VolumeMounts[0].SubPathExpr = "$(NAME)" }, "without subPath"},
		{"missing mount", func(p *core.Pod) { p.Spec.InitContainers[0].VolumeMounts = nil }, "explicitly mounted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := fixture()
			test.change(pod)
			directory, err := SecretDirectory(pod, "credentials", []string{"AccessKeyId"})
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			require.Equal(t, SecretRoot+"/credentials", directory)
		})
	}
}
