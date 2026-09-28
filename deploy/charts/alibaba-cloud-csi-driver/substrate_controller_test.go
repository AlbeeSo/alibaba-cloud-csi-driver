// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package chart_test

import (
	"encoding/json"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func TestSubstrateControllerOnlyDoesNotRequireActorAPI(t *testing.T) {
	resources := renderChart(t, "--set", "enableSubstrate=true", "--set", "plugin.enabled=false", "--set", "substrate.apiEndpoint=", "--set", "substrate.apiAudience=")
	var deployment appsv1.DeploymentSpec
	if err := json.Unmarshal(findResource(t, resources, "Deployment", "csi-provisioner").Spec, &deployment); err != nil {
		t.Fatal(err)
	}
	container(t, deployment.Template.Spec, "substrate-controller-proxy")
	certificate, clientTrust := false, false
	for _, volume := range deployment.Template.Spec.Volumes {
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.ServiceAccountToken != nil {
				t.Fatal("controller must not project an Actor API token")
			}
			if source.ClusterTrustBundle != nil {
				if source.ClusterTrustBundle.Path == "trust-bundle.pem" {
					t.Fatal("controller must not require the Actor API server trust bundle")
				}
				clientTrust = clientTrust || source.ClusterTrustBundle.Path == "client-trust-bundle.pem"
			}
			certificate = certificate || source.PodCertificate != nil
		}
	}
	if !certificate || !clientTrust {
		t.Fatal("controller must retain certificate and client trust for inbound mTLS")
	}
}
