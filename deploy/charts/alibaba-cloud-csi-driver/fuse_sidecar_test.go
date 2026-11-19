package chart_test

import (
	"encoding/json"
	"slices"
	"testing"

	apps "k8s.io/api/apps/v1"
)

func TestFuseProvisionersEnableCreateMetadataByDefault(t *testing.T) {
	resources := renderChart(t, "--set", "csi.oss.enabled=true", "--set", "csi.customfuse.enabled=true")
	for _, test := range []struct{ deployment, name string }{{"csi-provisioner", "external-oss-provisioner"}, {"csi-customfuse-provisioner", "external-customfuse-provisioner"}} {
		var spec apps.DeploymentSpec
		if err := json.Unmarshal(findResource(t, resources, "Deployment", test.deployment).Spec, &spec); err != nil {
			t.Fatal(err)
		}
		c := container(t, spec.Template.Spec, test.name)
		count := 0
		for _, arg := range c.Args {
			if arg == "--extra-create-metadata=true" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("%s: metadata flag count=%d", test.name, count)
		}
	}
}

func TestFuseWebhookOptInDeploymentAndPrivilegeDefault(t *testing.T) {
	for _, r := range renderChart(t) {
		if r.Kind == "MutatingWebhookConfiguration" {
			t.Fatal("admission must be disabled by default")
		}
	}
	resources := renderChart(t, "--set", "fuseSidecarWebhook.enabled=true", "--set", "fuseSidecarWebhook.tlsSecretName=test-tls", "--set", "fuseSidecarWebhook.caBundle=dGVzdA==")
	var spec apps.DeploymentSpec
	if err := json.Unmarshal(findResource(t, resources, "Deployment", "csi-fuse-sidecar-webhook").Spec, &spec); err != nil {
		t.Fatal(err)
	}
	c := container(t, spec.Template.Spec, "webhook")
	if !slices.Contains(c.Args, "--fuse-webhook-allow-privileged=false") {
		t.Fatal("privileged injection must require explicit administrator opt-in")
	}
	if spec.Template.Spec.ServiceAccountName != "csi-fuse-sidecar-webhook" {
		t.Fatal("webhook must use its dedicated read-only service account")
	}
	findResource(t, resources, "MutatingWebhookConfiguration", "csi-test-csi-fuse-sidecar")
}
