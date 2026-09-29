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
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
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
	DefaultActorRoot          = "/var/lib/ateom-gvisor/actors"
)

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

// Node serializes operations per mount target, not per VolumeId: the VolumeId is
// opaque to the bridge, and two logical volumes reaching one target must still
// serialize. The lock is process-local, like the rest of the bridge's state.
type Node struct {
	csi.UnimplementedNodeServer
	opts  NodeOptions
	locks *utils.VolumeLocks
}

func NewNode(opts NodeOptions) *Node { return &Node{opts: opts, locks: utils.NewVolumeLocks()} }

func (n *Node) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	// 1. The mount target, not the VolumeId, says which Actor volume this is.
	id, err := n.identity(req.GetTargetPath())
	if err != nil {
		return nil, err
	}
	// 2. What the caller claims about itself has to line up with that directory.
	if err := checkCallerMetadata(req, id); err != nil {
		return nil, err
	}
	// 3. One operation per target at a time; the lock is process-local.
	if !n.locks.TryAcquire(req.TargetPath) {
		return nil, status.Error(codes.Aborted, "target operation already in progress")
	}
	defer n.locks.Release(req.TargetPath)
	// 4. The Actor API decides what this volume really is, and which request describes it.
	resolved, err := resolveMount(ctx, n.opts.Lookup, id, req.VolumeContext)
	if err != nil {
		return nil, err
	}
	// 5. Actor metadata the caller brought along must describe the same Actor the API named.
	if err := validateActorMetadata(req.VolumeContext, resolved.Actor); err != nil {
		return nil, err
	}
	// 6. A Golden Actor is building its image, so it gets a local placeholder rather than
	// backend storage that would be shared with the running Actor. By design the placeholder is
	// writable whatever the request asked for: it exists to receive the image being built.
	if resolved.Actor.Golden {
		if err := n.publishPlaceholder(req.VolumeId, req.TargetPath); err != nil {
			return nil, status.Errorf(codes.Internal, "publish golden placeholder: %v", err)
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	// 7. The target must be able to host the mount the Actor asks for.
	if err := n.checkBackendTarget(req.TargetPath); err != nil {
		return nil, err
	}
	// 8. NAS does the mount, with the Actor's own request.
	return n.opts.NAS.NodePublishVolume(ctx, backendRequest(req, resolved))
}

// checkCallerMetadata validates the part of the request the caller controls: it has to name
// the Actor that owns the target directory. Pod identity is only injected by the ACK-adapted
// atelet as the actor UID itself (volumeContextForActor in Substrate's cmd/atelet/volumes.go),
// so when it is present it must agree with that directory.
func checkCallerMetadata(req *csi.NodePublishVolumeRequest, id volumeIdentity) error {
	if agentidentity.ActorUID(req.GetVolumeContext()) != id.ActorUID {
		return status.Error(codes.InvalidArgument, "actor UID must match the target actor directory")
	}
	if uid := req.GetVolumeContext()[PodUIDKey]; uid != "" && uid != id.ActorUID {
		return status.Error(codes.InvalidArgument, "pod UID must be the actor UID of the target directory")
	}
	return nil
}

// checkBackendTarget rejects a target that cannot carry this Actor's mount: no NAS server to
// forward to, or a live mount on a filesystem the bridge does not own. By design nothing else is
// compared. A live mount the NAS driver recognises is left to NAS, which answers an
// already-mounted target with success, so a retry stays retryable and matching a live mount
// against a new request stays the caller's unpublish-first obligation instead of a refusal the
// bridge invents.
func (n *Node) checkBackendTarget(target string) error {
	if n.opts.NAS == nil {
		return status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
	}
	live, err := n.mountAt(target)
	if err != nil {
		return err
	}
	if live != nil && !nasMount(live) {
		return status.Error(codes.FailedPrecondition, "target is mounted with an unsupported filesystem")
	}
	return nil
}

// backendRequest turns the Actor's stored publish request into the one handed to NAS: the target
// becomes the host directory the caller gave us, and the substrate-mode marker tells NAS to skip
// kubelet semantics. Everything else — volume ID, capability, read-only state, mount options,
// every other volume context entry — passes through untouched, which is what keeps a Substrate
// mount equivalent to the ACS Sandbox mount of the same volume.
//
// The actor identity is the one addition, and it is not optional: the stored request is generated
// without knowing which Actor it will be mounted for, and NAS refuses a request without a pod UID
// (pkg/nas/nodeserver.go:526) and exchanges the agent-identity credential under the actor UID as
// its resource ID. It is filled in only where the stored request is silent about identity.
//
// By design the caller's own read-only request is not carried over, which is a deliberate
// deviation from the CSI wording that the SP MUST honour readonly on publish: the Actor's request
// decides access, not a Pod object that does not exist here, and a bridge that "corrected" the
// request would make the Substrate and ACS Sandbox forms differ where neither can see it.
func backendRequest(req *csi.NodePublishVolumeRequest, resolved resolvedMount) *csi.NodePublishVolumeRequest {
	real := resolved.Request
	real.TargetPath = req.TargetPath
	real.VolumeContext[utils.SubstrateModeKey] = "true"
	setActorMetadata(real.VolumeContext, resolved.Actor)
	if real.VolumeContext[agentidentity.PodUIDKey] == "" {
		real.VolumeContext[agentidentity.PodUIDKey] = resolved.Actor.UID
	}
	return real
}

func (n *Node) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	// 1. The target still has to be one of the bridge's actor volume directories. No Actor
	// is looked up: unpublish decides from the mount table alone, which is what keeps it
	// working after the Actor is gone.
	if _, err := n.identity(req.GetTargetPath()); err != nil {
		return nil, err
	}
	// 2. One operation per target at a time.
	if !n.locks.TryAcquire(req.TargetPath) {
		return nil, status.Error(codes.Aborted, "target operation already in progress")
	}
	defer n.locks.Release(req.TargetPath)
	// 3. An absent mount is already unpublished.
	live, err := n.mountAt(req.TargetPath)
	if err != nil {
		return nil, err
	}
	if live == nil {
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	// 4. A non-NAS mount can only be the bridge's own Golden placeholder.
	if !nasMount(live) {
		return n.unpublishBackendPlaceholder(req)
	}
	// 5. NAS owns its mounts. The volume ID here is the caller's, not the one publish forwarded:
	// NAS uses it as a lock and log key only and the bridge keeps no per-ID state, which is by
	// design so unpublish still works once the Actor and its annotation are gone.
	if n.opts.NAS == nil {
		return nil, status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
	}
	if _, err := n.opts.NAS.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath}); err != nil {
		return nil, err
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// unpublishBackendPlaceholder removes a Golden placeholder and the directory behind it.
// Anything else mounted there is refused: the bridge never unmounts storage it did not
// create.
func (n *Node) unpublishBackendPlaceholder(req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	const unsupported = "refusing to unmount an unsupported live mount"
	if !filepath.IsAbs(n.opts.StateDir) || filepath.Clean(n.opts.StateDir) == "/" {
		return nil, status.Error(codes.FailedPrecondition, unsupported)
	}
	source, err := n.placeholderSource(req.VolumeId, req.TargetPath)
	if err != nil {
		return nil, err
	}
	owned, err := n.placeholderMounted(source, req.TargetPath)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, status.Error(codes.FailedPrecondition, unsupported)
	}
	if err := n.unpublishPlaceholder(source, req.TargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "unpublish golden placeholder: %v", err)
	}
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// volumeIdentity is what a mount target says about the volume it holds.
type volumeIdentity struct {
	ActorUID   string
	VolumeName string
}

// identity reads the Actor UID and the publish-request key out of the mount
// target instead of the VolumeId: Substrate has not stabilized its logical
// volume naming, so only its host directory layout is treated as a contract.
func (n *Node) identity(target string) (volumeIdentity, error) {
	root := filepath.Clean(n.opts.ActorRoot)
	if !filepath.IsAbs(n.opts.ActorRoot) || root == "/" {
		return volumeIdentity{}, status.Error(codes.FailedPrecondition, "an absolute actor root is required")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return volumeIdentity{}, status.Error(codes.InvalidArgument, "target must be a clean absolute path")
	}
	rel, ok := strings.CutPrefix(target, root+"/")
	if !ok {
		return volumeIdentity{}, status.Error(codes.InvalidArgument, "target must be inside the actor root")
	}
	segments := strings.Split(rel, "/")
	if len(segments) < 2 {
		return volumeIdentity{}, status.Error(codes.InvalidArgument, "target must be an actor volume directory")
	}
	return volumeIdentity{ActorUID: segments[0], VolumeName: segments[len(segments)-1]}, nil
}

// NodeStage and NodeUnstage answer successfully and do nothing: the bridge mounts at publish and
// has no stage of its own. By design the answer is a success rather than Unimplemented, because
// atelet only skips staging when the driver reports Unimplemented (internal/volume/csi/plugin.go)
// and would otherwise fail the mount; the consequence is that its publish carries a staging path
// the NAS driver never reads, so that path is forwarded and not validated.
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
