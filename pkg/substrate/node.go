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
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"os"
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
	uid, name, err := n.identity(req.GetVolumeId(), req.GetTargetPath())
	if err != nil {
		return nil, err
	}
	if agentidentity.ActorUID(req.GetVolumeContext()) != uid {
		return nil, status.Error(codes.InvalidArgument, "actor UID must match the volume identity")
	}
	if req.GetVolumeContext()[PodUIDKey] == "" {
		return nil, status.Error(codes.InvalidArgument, "worker Pod UID is required")
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
	if req.VolumeContext[BindingDigestKey] != resolved.Digest {
		return nil, status.Error(codes.FailedPrecondition, "publish request differs from the controller binding")
	}
	// req describes the virtual bridge volume; resolved.Request owns the backend configuration.
	// Only read-only restrictions affect backend access; do not merge virtual capabilities.
	boundReadOnly := nasReadOnly(resolved.Request)
	readOnly := boundReadOnly || requestReadOnly(req)
	if readOnly && !boundReadOnly {
		resolved.Digest = fmt.Sprintf("%x", sha256.Sum256([]byte(resolved.Digest+"\x00readonly")))
	}
	if readOnly && resolved.Request != nil {
		enforceReadOnly(resolved.Request)
	}
	store := bindingStore{root: n.opts.StateDir}
	if n.opts.Mounter == nil {
		return nil, status.Error(codes.FailedPrecondition, "mount inspector is required")
	}
	if _, err := store.load(req.VolumeId, req.TargetPath); errors.Is(err, os.ErrNotExist) {
		mounted, err := n.mounted(req.TargetPath)
		if err != nil {
			return nil, err
		}
		if mounted {
			return nil, status.Error(codes.FailedPrecondition, "refusing to claim an untracked mount")
		}
	} else if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot read binding: %v", err)
	} else if err := n.checkMountedReadOnly(req.TargetPath, readOnly); err != nil {
		return nil, err
	}
	b := binding{Version: 1, LogicalID: req.VolumeId, Target: req.TargetPath, ActorUID: uid, VolumeName: name, Kind: resolved.Kind, Digest: resolved.Digest}
	if resolved.Kind == bindingNAS {
		b.Driver = NASDriverName
		b.RealID = resolved.Request.VolumeId
	}
	if err := store.put(b); err != nil {
		if errors.Is(err, errBindingConflict) {
			return nil, status.Error(codes.AlreadyExists, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "persist mount binding: %v", err)
	}
	if resolved.Kind == bindingGolden {
		if err := n.publishPlaceholder(store, b, readOnly); err != nil {
			return nil, status.Errorf(codes.Internal, "publish golden placeholder: %v", err)
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	if n.opts.NAS == nil {
		return nil, status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
	}
	real := resolved.Request
	real.TargetPath = req.TargetPath
	real.VolumeContext[SubstrateModeKey] = "true"
	if agentidentity.HasActorIdentity(req.VolumeContext) {
		setActorMetadata(real.VolumeContext, resolved.Actor)
		for _, key := range []string{agentidentity.PodUIDKey, agentidentity.PodNameKey, agentidentity.PodNamespaceKey} {
			if value := req.VolumeContext[key]; value != "" {
				real.VolumeContext[key] = value
			} else {
				delete(real.VolumeContext, key)
			}
		}
	} else {
		real.VolumeContext[PodUIDKey] = uid
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
	store := bindingStore{root: n.opts.StateDir}
	b, err := store.load(req.VolumeId, req.TargetPath)
	if errors.Is(err, os.ErrNotExist) {
		mounted, checkErr := n.mounted(req.TargetPath)
		if checkErr != nil {
			return nil, checkErr
		}
		if mounted {
			return nil, status.Error(codes.FailedPrecondition, "refusing to unmount an untracked target")
		}
		if err := os.Remove(req.TargetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, status.Errorf(codes.FailedPrecondition, "untracked target is not removable: %v", err)
		}
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot read mount binding: %v", err)
	}
	switch b.Kind {
	case bindingGolden:
		if err := n.unpublishPlaceholder(store, *b); err != nil {
			return nil, status.Errorf(codes.Internal, "unpublish golden placeholder: %v", err)
		}
	case bindingNAS:
		if n.opts.NAS == nil {
			return nil, status.Error(codes.FailedPrecondition, "NAS forwarding is not configured")
		}
		if _, err := n.opts.NAS.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: b.RealID, TargetPath: b.Target}); err != nil {
			return nil, err
		}
	default:
		return nil, status.Error(codes.FailedPrecondition, "unknown mount binding kind")
	}
	if err := store.remove(req.VolumeId, req.TargetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "remove mount binding: %v", err)
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
