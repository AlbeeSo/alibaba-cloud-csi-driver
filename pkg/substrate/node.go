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
	"encoding/json"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

const (
	DriverName                = "substrate.csi.alibabacloud.com"
	DriverShortName           = "substrate"
	NASDriverName             = "nasplugin.csi.alibabacloud.com"
	PublishRequestsAnnotation = "ate.dev/csi-volume-publish-requests"
	PodUIDKey                 = agentidentity.PodUIDKey
	SubstrateModeKey          = agentidentity.SubstrateModeKey
	DefaultActorRoot          = "/var/lib/ateom-gvisor/actors"
)

var volumeIdentity = regexp.MustCompile(`^substrate-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})-([a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?)$`)

type publishEntry struct {
	VolumeName string          `json:"volumeName"`
	Driver     string          `json:"driver"`
	Request    json.RawMessage `json:"request"`
}

type ActorLookup func(context.Context, ActorReference) (ActorInfo, error)

type NodeOptions struct {
	NodeID    string
	ActorRoot string
	Lookup    ActorLookup
	NAS       csi.NodeServer
	StateDir  string
	Mounter   mount.Interface
}

type Node struct {
	csi.UnimplementedNodeServer
	opts   NodeOptions
	mu     sync.Mutex
	active map[string]bool
}

func NewNode(opts NodeOptions) *Node { return &Node{opts: opts, active: map[string]bool{}} }

func (n *Node) acquire(target string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.active[target] {
		return false
	}
	n.active[target] = true
	return true
}

func (n *Node) release(target string) { n.mu.Lock(); delete(n.active, target); n.mu.Unlock() }

func (n *Node) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	uid, _, err := n.identity(req.GetVolumeId(), req.GetTargetPath())
	if err != nil {
		return nil, err
	}
	if agentidentity.ActorUID(req.GetVolumeContext()) != uid {
		return nil, status.Error(codes.InvalidArgument, "actor UID must match the volume identity")
	}
	if req.GetVolumeContext()[PodUIDKey] == "" {
		return nil, status.Error(codes.InvalidArgument, "worker Pod UID is required")
	}
	if requestReadOnly(req) {
		return nil, status.Error(codes.FailedPrecondition, "outer readonly constraints are unsupported; configure readonly in the Actor publish request")
	}
	if !n.acquire(req.TargetPath) {
		return nil, status.Error(codes.Aborted, "target operation already in progress")
	}
	defer n.release(req.TargetPath)
	resolved, err := resolveMount(ctx, n.opts.Lookup, req.VolumeId, req.VolumeContext)
	if err != nil {
		return nil, err
	}
	if err := validateActorMetadata(req.VolumeContext, resolved.Actor); err != nil {
		return nil, err
	}
	// req describes the virtual bridge volume; resolved.Request owns the backend configuration.
	if resolved.Actor.Golden {
		if err := n.publishPlaceholder(req.VolumeId, req.TargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "publish golden placeholder: %v", err)
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if n.opts.NAS == nil {
		return nil, status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
	}
	live, err := n.mountAt(req.TargetPath)
	if err != nil {
		return nil, err
	}
	if live != nil && !nasMount(live) {
		return nil, status.Error(codes.FailedPrecondition, "target is mounted with an unsupported filesystem")
	}
	if err := n.checkMountedReadOnly(req.TargetPath, explicitReadOnly(resolved.Request)); err != nil {
		return nil, err
	}
	real := resolved.Request
	real.TargetPath = req.TargetPath
	real.VolumeContext[SubstrateModeKey] = "true"
	setActorMetadata(real.VolumeContext, resolved.Actor)
	for _, key := range []string{agentidentity.PodUIDKey, agentidentity.PodNameKey, agentidentity.PodNamespaceKey} {
		if value := req.VolumeContext[key]; value != "" {
			real.VolumeContext[key] = value
		} else {
			delete(real.VolumeContext, key)
		}
	}
	return n.opts.NAS.NodePublishVolume(ctx, real)
}

func (n *Node) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	_, _, err := n.identity(req.GetVolumeId(), req.GetTargetPath())
	if err != nil {
		return nil, err
	}
	if !n.acquire(req.TargetPath) {
		return nil, status.Error(codes.Aborted, "target operation already in progress")
	}
	defer n.release(req.TargetPath)
	live, err := n.mountAt(req.TargetPath)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	if !nasMount(live) && filepath.IsAbs(n.opts.StateDir) && filepath.Clean(n.opts.StateDir) != "/" {
		source, sourceErr := n.placeholderSource(req.VolumeId, req.TargetPath)
		if sourceErr != nil {
			return nil, sourceErr
		}
		owned, sourceErr := n.placeholderMounted(source, req.TargetPath)
		if sourceErr != nil {
			return nil, sourceErr
		}
		if owned {
			if err := n.unpublishPlaceholder(source, req.TargetPath); err != nil {
				return nil, status.Errorf(codes.Internal, "unpublish golden placeholder: %v", err)
			}
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
	}
	if !nasMount(live) {
		return nil, status.Error(codes.FailedPrecondition, "refusing to unmount an unsupported live mount")
	}
	if n.opts.NAS == nil {
		return nil, status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
	}
	if _, err := n.opts.NAS.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); err != nil {
		return nil, err
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (n *Node) identity(volumeID, target string) (string, string, error) {
	parts := volumeIdentity.FindStringSubmatch(volumeID)
	if len(parts) != 3 {
		return "", "", status.Error(codes.InvalidArgument, "expected a Substrate actor volume identity")
	}
	if !filepath.IsAbs(n.opts.ActorRoot) || filepath.Clean(n.opts.ActorRoot) == "/" {
		return "", "", status.Error(codes.FailedPrecondition, "an absolute actor root is required")
	}
	if target != filepath.Join(n.opts.ActorRoot, parts[1], "volumes", parts[2]) {
		return "", "", status.Error(codes.InvalidArgument, "target must be the actor volume path")
	}
	return parts[1], parts[2], nil
}

func (*Node) NodeStageVolume(context.Context, *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	return &csi.NodeStageVolumeResponse{}, nil
}

func (*Node) NodeUnstageVolume(context.Context, *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (n *Node) NodeGetInfo(context.Context, *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	if n.opts.NodeID == "" {
		return nil, status.Error(codes.FailedPrecondition, "bridge node identity is not configured")
	}
	return &csi.NodeGetInfoResponse{NodeId: n.opts.NodeID}, nil
}

func (*Node) NodeGetCapabilities(context.Context, *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{Capabilities: []*csi.NodeServiceCapability{{Type: &csi.NodeServiceCapability_Rpc{Rpc: &csi.NodeServiceCapability_RPC{Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME}}}}}, nil
}
