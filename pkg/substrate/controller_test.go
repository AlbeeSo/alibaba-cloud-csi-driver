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
	"context"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestLogicalVolumeLifecycle(t *testing.T) {
	annotation := annotationFixture(t, publishFixture())
	controller := &Controller{Lookup: func(context.Context, string) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "storage-test", Name: "actor", Annotation: annotation}, nil
	}}
	req := &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}, CapacityRange: &csi.CapacityRange{RequiredBytes: 1024}}
	created, err := controller.CreateVolume(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, testID, created.Volume.VolumeId)
	require.Equal(t, int64(1024), created.Volume.CapacityBytes)
	require.Equal(t, "true", created.Volume.VolumeContext["csi.alibabacloud.com/substrate-mode"])
	require.Len(t, created.Volume.VolumeContext[BindingDigestKey], 64)
	_, err = controller.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: testID})
	require.NoError(t, err)
	_, err = controller.DeleteVolume(t.Context(), &csi.DeleteVolumeRequest{VolumeId: testID})
	require.NoError(t, err)
}

func TestControllerRejectsMissingBusinessAnnotation(t *testing.T) {
	controller := &Controller{Lookup: func(context.Context, string) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "storage-test", Name: "actor"}, nil
	}}
	_, err := controller.CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&Controller{}).CreateVolume(t.Context(), tc.req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		})
	}
}
