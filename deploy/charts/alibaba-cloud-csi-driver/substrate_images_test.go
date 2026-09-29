// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package chart_test

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func TestSubstrateEnvoyUsesStandardImageSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"Beijing VPC", []string{"--set", "deploy.regionID=cn-beijing", "--set", "deploy.network=vpc"}, "registry-cn-beijing-vpc.ack.aliyuncs.com/acs/envoy:v1.39-latest"},
		{"Beijing public", []string{"--set", "deploy.regionID=cn-beijing", "--set", "deploy.network=internet"}, "registry-cn-beijing.ack.aliyuncs.com/acs/envoy:v1.39-latest"},
		{"custom image", []string{"--set", "deploy.network=vpc", "--set", "images.registryVPC=registry.example.com", "--set", "images.envoy.repo=team/envoy", "--set", "images.envoy.tag=reviewed"}, "registry.example.com/team/envoy:reviewed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--set", "enableSubstrate=true"}, tc.args...)
			resources := renderChart(t, args...)
			var deployment appsv1.DeploymentSpec
			if err := json.Unmarshal(findResource(t, resources, "Deployment", "csi-provisioner").Spec, &deployment); err != nil {
				t.Fatal(err)
			}
			if got := container(t, deployment.Template.Spec, "substrate-controller-proxy").Image; got != tc.want {
				t.Fatalf("Envoy image %q, want %q", got, tc.want)
			}
		})
	}
}
