// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package chart_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

type renderedResource struct {
	Kind     string            `json:"kind"`
	Metadata metav1.ObjectMeta `json:"metadata"`
	Spec     json.RawMessage   `json:"spec"`
	Data     map[string]string `json:"data"`
}

func renderChart(t *testing.T, options ...string) []renderedResource {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	args := append([]string{"template", "test", ".", "--namespace", "csi-test"}, options...)
	cmd := exec.CommandContext(t.Context(), "helm", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, stderr.String())
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(output), 4096)
	var resources []renderedResource
	for {
		var resource renderedResource
		if err := decoder.Decode(&resource); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if resource.Kind != "" {
			resources = append(resources, resource)
		}
	}
	return resources
}

func findResource(t *testing.T, resources []renderedResource, kind, name string) renderedResource {
	t.Helper()
	for _, resource := range resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			return resource
		}
	}
	t.Fatalf("missing %s/%s", kind, name)
	return renderedResource{}
}

func container(t *testing.T, pod corev1.PodSpec, name string) corev1.Container {
	t.Helper()
	for _, c := range pod.Containers {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing container %s", name)
	return corev1.Container{}
}

func TestSubstrateDisabledDoesNotAddResources(t *testing.T) {
	for _, resource := range renderChart(t) {
		if strings.Contains(resource.Metadata.Name, "substrate") || resource.Metadata.Name == "ate-storage" {
			t.Fatalf("unexpected default resource: %s/%s", resource.Kind, resource.Metadata.Name)
		}
	}
}

func TestSubstrateEnabledWiresControllerNodeAndTrust(t *testing.T) {
	resources := renderChart(t, "--set", "enableSubstrate=true")
	var deployment appsv1.DeploymentSpec
	if err := json.Unmarshal(findResource(t, resources, "Deployment", "csi-substrate-controller").Spec, &deployment); err != nil {
		t.Fatal(err)
	}
	driver := container(t, deployment.Template.Spec, "bridge")
	args := strings.Join(driver.Args, " ")
	for _, flag := range []string{"--driver=substrate.csi.alibabacloud.com", "--run-controller-service=true", "--run-node-service=false", "--substrate-api-endpoint=api.ate-system.svc:443", "--substrate-api-ca-file=/run/ateapi/trust-bundle.pem", "--substrate-api-token-file=/run/ateapi/token"} {
		if !strings.Contains(args, flag) {
			t.Errorf("missing controller flag %s", flag)
		}
	}
	container(t, deployment.Template.Spec, "envoy")
	if deployment.Template.Spec.ServiceAccountName != "alicloud-csi-substrate" {
		t.Fatal("bridge requires its own service account")
	}
	var daemon appsv1.DaemonSetSpec
	if err := json.Unmarshal(findResource(t, resources, "DaemonSet", "csi-plugin-substrate").Spec, &daemon); err != nil {
		t.Fatal(err)
	}
	node := container(t, daemon.Template.Spec, "bridge")
	if !strings.Contains(strings.Join(node.Args, " "), "--mount-proxy-sock=/run/cnfs/alinas-mounter.sock") {
		t.Fatal("NAS broker socket is required")
	}
	healthPort := ""
	for _, env := range node.Env {
		if env.Name == "SERVICE_PORT" {
			healthPort = env.Value
		}
	}
	if healthPort == "" || healthPort == "11260" {
		t.Fatal("host-network bridge must not reuse the native plugin health port")
	}
	registrar := container(t, daemon.Template.Spec, "registrar")
	if !strings.Contains(strings.Join(registrar.Args, " "), "/var/lib/kubelet/csi-plugins/substrate.csi.alibabacloud.com/csi.sock") {
		t.Fatal("registrar path mismatch")
	}
	propagated, persistent := false, false
	for _, m := range node.VolumeMounts {
		if m.Name == "actors" && m.MountPropagation != nil && *m.MountPropagation == corev1.MountPropagationBidirectional {
			propagated = true
		}
	}
	for _, v := range daemon.Template.Spec.Volumes {
		if v.Name == "csi" && v.HostPath != nil && v.HostPath.Path == "/var/lib/kubelet/csi-plugins/substrate.csi.alibabacloud.com" {
			persistent = true
		}
		if v.Name == "ateapi" {
			if v.Projected == nil {
				t.Fatal("ateapi trust must be projected")
			}
			ca, token := false, false
			for _, s := range v.Projected.Sources {
				if s.ClusterTrustBundle != nil && s.ClusterTrustBundle.SignerName != nil && *s.ClusterTrustBundle.SignerName == "servicedns.podcert.ate.dev/identity" {
					ca = true
				}
				if s.ServiceAccountToken != nil && s.ServiceAccountToken.Audience == "api.ate-system.svc" {
					token = true
				}
			}
			if !ca || !token {
				t.Fatal("missing CA or audience-bound token")
			}
		}
	}
	if !propagated || !persistent {
		t.Fatal("actor propagation and persistent bridge state are required")
	}
	findResource(t, resources, "CSIDriver", "substrate.csi.alibabacloud.com")
	findResource(t, resources, "StorageClass", "ate-storage")
	findResource(t, resources, "CSIDriverConfig", "substrate-csi-bridge")
	findResource(t, resources, "Service", "csi-substrate-controller")
	config := findResource(t, resources, "ConfigMap", "csi-substrate-controller-envoy")
	if !strings.Contains(config.Data["envoy.yaml"], "require_client_certificate: true") {
		t.Fatal("controller must require mTLS")
	}
}

func TestSubstrateCustomPaths(t *testing.T) {
	resources := renderChart(t, "--set", "enableSubstrate=true", "--set", "deploy.kubeletRootDir=/custom/kubelet", "--set", "substrate.actorRoot=/custom/actors")
	var daemon appsv1.DaemonSetSpec
	if err := json.Unmarshal(findResource(t, resources, "DaemonSet", "csi-plugin-substrate").Spec, &daemon); err != nil {
		t.Fatal(err)
	}
	driver := container(t, daemon.Template.Spec, "bridge")
	if !strings.Contains(strings.Join(driver.Args, " "), "--substrate-actor-root=/custom/actors") {
		t.Fatal("actor root override not forwarded")
	}
	config := findResource(t, resources, "CSIDriverConfig", "substrate-csi-bridge")
	if !strings.Contains(string(config.Spec), "unix:///custom/kubelet/csi-plugins/substrate.csi.alibabacloud.com/csi.sock") {
		t.Fatal("node socket override mismatch")
	}
}

func TestSubstrateRejectsUnsafeActorRoot(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	cmd := exec.CommandContext(t.Context(), "helm", "template", "test", ".", "--set", "enableSubstrate=true", "--set", "substrate.actorRoot=/")
	cmd.Env = append(os.Environ(), "KUBECONFIG=/dev/null")
	if err := cmd.Run(); err == nil {
		t.Fatal("root filesystem must not be an actor root")
	}
}

func TestSubstrateComponentSwitches(t *testing.T) {
	for _, tc := range []struct{ option, forbiddenKind, forbiddenName string }{
		{"controller.enabled=false", "Deployment", "csi-substrate-controller"},
		{"plugin.enabled=false", "DaemonSet", "csi-plugin-substrate"},
		{"substrate.storageClass.create=false", "StorageClass", "ate-storage"},
		{"substrate.createDriverConfig=false", "CSIDriverConfig", "substrate-csi-bridge"},
	} {
		t.Run(tc.option, func(t *testing.T) {
			for _, resource := range renderChart(t, "--set", "enableSubstrate=true", "--set", tc.option) {
				if resource.Kind == tc.forbiddenKind && resource.Metadata.Name == tc.forbiddenName {
					t.Fatalf("unexpected %s/%s", resource.Kind, resource.Metadata.Name)
				}
			}
		})
	}
}
