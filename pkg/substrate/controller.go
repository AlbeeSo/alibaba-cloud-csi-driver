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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Controller struct {
	csi.UnimplementedControllerServer
}

func (*Controller) CreateVolume(_ context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	parts := volumeIdentity.FindStringSubmatch(req.GetName())
	if len(parts) != 3 || req.GetVolumeContentSource() != nil || len(req.GetSecrets()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "expected an existing-volume reference without content source or secrets")
	}
	if !filesystemCapabilities(req.VolumeCapabilities) {
		return nil, status.Error(codes.InvalidArgument, "filesystem volume capabilities are required")
	}
	capacity := req.GetCapacityRange().GetRequiredBytes()
	limit := req.GetCapacityRange().GetLimitBytes()
	if capacity < 0 || limit < 0 || (limit > 0 && capacity > limit) {
		return nil, status.Error(codes.InvalidArgument, "invalid capacity range")
	}
	if err := agentidentity.ValidateActorIdentity(req.Parameters); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if req.Parameters[agentidentity.ActorUIDKey] != parts[1] || req.Parameters[agentidentity.ActorNameKey] == "" || req.Parameters[agentidentity.ActorNamespaceKey] == "" {
		return nil, status.Error(codes.InvalidArgument, "actor UID, name and namespace must identify the volume owner")
	}
	attributes := map[string]string{SubstrateModeKey: "true"}
	for _, key := range []string{agentidentity.ActorUIDKey, agentidentity.ActorNameKey, agentidentity.ActorNamespaceKey} {
		attributes[key] = req.Parameters[key]
	}
	return &csi.CreateVolumeResponse{Volume: &csi.Volume{VolumeId: req.Name, CapacityBytes: capacity, VolumeContext: attributes}}, nil
}

func (*Controller) DeleteVolume(_ context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	return &csi.DeleteVolumeResponse{}, nil
}

func (*Controller) ControllerPublishVolume(_ context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	if !volumeIdentity.MatchString(req.GetVolumeId()) || req.GetNodeId() == "" || !filesystemCapabilities([]*csi.VolumeCapability{req.GetVolumeCapability()}) {
		return nil, status.Error(codes.InvalidArgument, "volume identity, node ID and filesystem capability are required")
	}
	return &csi.ControllerPublishVolumeResponse{}, nil
}

func (*Controller) ControllerUnpublishVolume(_ context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	if req.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

func (*Controller) ValidateVolumeCapabilities(_ context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	if !volumeIdentity.MatchString(req.GetVolumeId()) {
		return nil, status.Error(codes.InvalidArgument, "expected a Substrate volume identity")
	}
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
