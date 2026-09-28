/*
Copyright 2026 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package substrate

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestLogicalVolumeLifecycle(t *testing.T) {
	controller := &Controller{}
	req := &csi.CreateVolumeRequest{Name: testID, Parameters: actorMetadataFixture(), VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, CapacityRange: &csi.CapacityRange{RequiredBytes: 1024}}
	created, err := controller.CreateVolume(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, testID, created.Volume.VolumeId)
	require.Equal(t, int64(1024), created.Volume.CapacityBytes)
	require.Equal(t, "true", created.Volume.VolumeContext["csi.alibabacloud.com/substrate-mode"])
	wantContext := actorMetadataFixture()
	wantContext[SubstrateModeKey] = "true"
	require.Equal(t, wantContext, created.Volume.VolumeContext)
	retried, err := controller.CreateVolume(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, created.Volume, retried.Volume)
	_, err = controller.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: testID})
	require.NoError(t, err)
	_, err = controller.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: testID})
	require.NoError(t, err)
}

func TestControllerDoesNotResolveBackendConfiguration(t *testing.T) {
	parameters := actorMetadataFixture()
	parameters[PublishRequestsAnnotation] = "not JSON"
	parameters["server"] = "not-a-storage-server"
	created, err := (&Controller{}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, Parameters: parameters, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
	require.NoError(t, err)
	wantContext := actorMetadataFixture()
	wantContext[SubstrateModeKey] = "true"
	require.Equal(t, wantContext, created.Volume.VolumeContext)
}

func TestControllerRejectsInvalidVolumeRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *csi.CreateVolumeRequest
	}{
		{"empty", &csi.CreateVolumeRequest{}},
		{"block", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{{AccessType: &csi.VolumeCapability_Block{Block: &csi.VolumeCapability_BlockVolume{}}}}}},
		{"snapshot", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, VolumeContentSource: &csi.VolumeContentSource{}}},
		{"invalid capacity", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, CapacityRange: &csi.CapacityRange{RequiredBytes: 2048, LimitBytes: 1024}}},
		{"negative capacity", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, CapacityRange: &csi.CapacityRange{RequiredBytes: -1}}},
		{"negative limit", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, CapacityRange: &csi.CapacityRange{LimitBytes: -1}}},
		{"secrets", &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, Secrets: map[string]string{"key": "not-allowed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Controller{}).CreateVolume(t.Context(), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}

func TestControllerRejectsMissingOrInconsistentActorParameters(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"csi.alibabacloud.com/actor.uid", ""},
		{"csi.alibabacloud.com/actor.name", ""},
		{"csi.alibabacloud.com/actor.namespace", ""},
		{"csi.alibabacloud.com/actor.uid", "00000000-0000-0000-0000-000000000000"},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			parameters := actorMetadataFixture()
			parameters[tc.key] = tc.value
			_, err := (&Controller{}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, Parameters: parameters, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
	_, err := (&Controller{}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
