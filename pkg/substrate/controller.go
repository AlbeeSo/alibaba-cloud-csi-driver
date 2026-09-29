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
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Controller struct {
	csi.UnimplementedControllerServer
}

// CreateVolume hands back the identity of an existing volume. It provisions nothing: the
// backend volume already exists because the shared generator built its publish request when
// the Actor was created.
func (*Controller) CreateVolume(_ context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// 1. Requests that ask the bridge to create or copy storage are refused.
	if err := checkCreateRequest(req); err != nil {
		return nil, err
	}
	// 2. The logical volume ID is whatever the caller chose; the capacity is echoed back
	// unverified, and the Actor metadata travels to publish through VolumeContext.
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{
		VolumeId:      req.GetName(),
		CapacityBytes: req.GetCapacityRange().GetRequiredBytes(),
		VolumeContext: actorAttributes(req.GetParameters()),
	}}, nil
}

// checkCreateRequest rejects the requests the bridge cannot satisfy. It does not compare the
// declared Actor with the Actor API: that answer is only trustworthy at publish time, when
// the volume is actually mounted.
func checkCreateRequest(req *csi.CreateVolumeRequest) error {
	if req.GetVolumeContentSource() != nil || len(req.GetSecrets()) != 0 {
		return status.Error(codes.InvalidArgument, "expected an existing-volume reference without content source or secrets")
	}
	capacity, limit := req.GetCapacityRange().GetRequiredBytes(), req.GetCapacityRange().GetLimitBytes()
	if capacity < 0 || limit < 0 || (limit > 0 && capacity > limit) {
		return status.Error(codes.InvalidArgument, "invalid capacity range")
	}
	if req.GetParameters()[agentidentity.ActorUIDKey] == "" || req.GetParameters()[agentidentity.ActorNameKey] == "" || req.GetParameters()[agentidentity.ActorNamespaceKey] == "" {
		return status.Error(codes.InvalidArgument, "actor UID, name and namespace parameters are required")
	}
	return nil
}

// actorAttributes is the VolumeContext that travels unchanged to NodePublishVolume, where
// the bridge checks it against the Actor API.
func actorAttributes(parameters map[string]string) map[string]string {
	attributes := map[string]string{utils.SubstrateModeKey: "true"}
	for _, key := range []string{agentidentity.ActorUIDKey, agentidentity.ActorNameKey, agentidentity.ActorNamespaceKey} {
		attributes[key] = parameters[key]
	}
	return attributes
}

func (*Controller) DeleteVolume(_ context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	return &csi.DeleteVolumeResponse{}, nil
}

// Attach is a no-op by design: the bridge holds no per-node state, and the
// chart's CSIDriver object sets attachRequired=false, so nothing may depend on
// this answer. NodePublishVolume is where the Actor API decides reachability.
func (*Controller) ControllerPublishVolume(context.Context, *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (*Controller) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (*Controller) ValidateVolumeCapabilities(_ context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if !filesystemCapabilities(req.GetVolumeCapabilities()) {
		return &csi.ValidateVolumeCapabilitiesResponse{Message: "only filesystem capabilities are supported"}, nil
	}
	return &csi.ValidateVolumeCapabilitiesResponse{Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: req.VolumeCapabilities, VolumeContext: req.VolumeContext, Parameters: req.Parameters}}, nil
}

func (*Controller) ControllerGetCapabilities(context.Context, *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	capabilities := []*csi.ControllerServiceCapability{}
	for _, capability := range []csi.ControllerServiceCapability_RPC_Type{csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME, csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME} {
		capabilities = append(capabilities, &csi.ControllerServiceCapability{Type: &csi.ControllerServiceCapability_Rpc{Rpc: &csi.ControllerServiceCapability_RPC{Type: capability}}})
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: capabilities}, nil
}

func filesystemCapabilities(capabilities []*csi.VolumeCapability) bool {
	if len(capabilities) == 0 {
		return false
	}
	for _, capability := range capabilities {
		if capability.GetMount() == nil || capability.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_UNKNOWN {
			return false
		}
	}
	return true
}
