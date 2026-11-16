package chart

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fusesidecar"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestFuseSidecarChart(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart integration tests")
	}
	args := []string{"template", "test", "../../deploy/charts/alibaba-cloud-csi-driver", "--set", "fuseSidecarWebhook.enabled=true", "--set", "deploy.featureGates=RundCSIProtocol3=true", "--set", "fuseSidecarWebhook.image=example.invalid/webhook:test", "--set", "fuseSidecarWebhook.tlsSecretName=test-tls", "--set", "fuseSidecarWebhook.caBundle=dGVzdA==", "--set", "imagePullSecrets[0]=private-registry", "--set", "fuseSidecarWebhook.profiles.oss.driver=" + fusesidecar.OSSDriver, "--set", "fuseSidecarWebhook.profiles.oss.image=example.invalid/aio:test"}
	out, err := exec.Command("helm", args...).CombinedOutput()
	require.NoError(t, err, string(out))
	decoder := yaml.NewYAMLToJSONDecoder(strings.NewReader(string(out)))
	foundConfig, foundDeployment := false, false
	for {
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if len(raw) == 0 {
			continue
		}
		var obj struct {
			Kind     string
			Metadata struct{ Name string }
		}
		require.NoError(t, json.Unmarshal(raw, &obj))
		if obj.Metadata.Name != "test-fuse-sidecar" {
			continue
		}
		switch obj.Kind {
		case "ConfigMap":
			var cm core.ConfigMap
			require.NoError(t, json.Unmarshal(raw, &cm))
			var cfg fusesidecar.Config
			require.NoError(t, json.Unmarshal([]byte(cm.Data["config.json"]), &cfg))
			require.NoError(t, cfg.Validate())
			require.Equal(t, []core.LocalObjectReference{{Name: "private-registry"}}, cfg.ImagePullSecrets)
			foundConfig = true
		case "Deployment":
			var deployment apps.Deployment
			require.NoError(t, json.Unmarshal(raw, &deployment))
			require.Equal(t, []core.LocalObjectReference{{Name: "private-registry"}}, deployment.Spec.Template.Spec.ImagePullSecrets)
			foundDeployment = true
		}
	}
	require.True(t, foundConfig)
	require.True(t, foundDeployment)
	for i, a := range args {
		if a == "deploy.featureGates=RundCSIProtocol3=true" {
			args[i] = "deploy.featureGates=RundCSIProtocol3=false"
		}
	}
	out, err = exec.Command("helm", args...).CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(out), "requires deploy.featureGates")
}
