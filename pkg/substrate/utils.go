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
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils/agentidentity"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	mount "k8s.io/mount-utils"
)

type resolvedMount struct {
	Actor      ActorInfo
	ActorUID   string
	VolumeName string
	Request    *csi.NodePublishVolumeRequest
}

func resolveMount(ctx context.Context, lookup ActorLookup, id string, attributes map[string]string) (resolvedMount, error) {
	parts := volumeIdentity.FindStringSubmatch(id)
	if len(parts) != 3 {
		return resolvedMount{}, status.Error(codes.InvalidArgument, "expected a Substrate actor volume identity")
	}
	if lookup == nil {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "actor lookup must be configured")
	}
	ref := ActorReference{UID: parts[1], Name: attributes[agentidentity.ActorNameKey], Atespace: attributes[agentidentity.ActorNamespaceKey]}
	if attributes[agentidentity.ActorUIDKey] != ref.UID || ref.Name == "" || ref.Atespace == "" {
		return resolvedMount{}, status.Error(codes.InvalidArgument, "actor UID, name and namespace must identify the volume owner")
	}
	actor, err := lookup(ctx, ref)
	if err != nil {
		return resolvedMount{}, err
	}
	if actor.UID != parts[1] {
		return resolvedMount{}, status.Error(codes.PermissionDenied, "actor lookup returned a different identity")
	}
	resolved := resolvedMount{Actor: actor, ActorUID: parts[1], VolumeName: parts[2]}
	if actor.Golden {
		if actor.Atespace != "ate-golden" || actor.TemplateUID == "" || actor.Name != actor.TemplateUID {
			return resolvedMount{}, status.Error(codes.FailedPrecondition, "golden actor association is invalid")
		}
		return resolved, nil
	}
	if actor.Atespace == "ate-golden" {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "unverified golden actor")
	}
	if len(actor.Annotation) == 0 || len(actor.Annotation) > 256*1024 {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "missing or oversized actor publish requests")
	}
	var entries []publishEntry
	if err := json.Unmarshal([]byte(actor.Annotation), &entries); err != nil {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "invalid actor publish requests JSON")
	}
	var matched *publishEntry
	for i := range entries {
		if entries[i].VolumeName != resolved.VolumeName {
			continue
		}
		if matched != nil {
			return resolvedMount{}, status.Error(codes.FailedPrecondition, "duplicate actor volume publish request")
		}
		matched = &entries[i]
	}
	if matched == nil || matched.Driver != NASDriverName {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "a NAS publish request is required for the template volume")
	}
	req := new(csi.NodePublishVolumeRequest)
	if err := protojson.Unmarshal(matched.Request, req); err != nil {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "invalid CSI publish request")
	}
	if req.VolumeId == "" || req.GetVolumeCapability().GetMount() == nil || req.GetVolumeCapability().GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_UNKNOWN || len(req.Secrets) != 0 {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "a filesystem request without secrets is required")
	}
	if !filepath.IsAbs(req.TargetPath) || filepath.Clean(req.TargetPath) != req.TargetPath || req.StagingTargetPath != "" {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "publish request requires a clean guest target and no staging path")
	}
	if !strings.EqualFold(req.VolumeContext["authType"], "agent-identity") || req.VolumeContext["mountProtocol"] != "alinas" {
		return resolvedMount{}, status.Error(codes.FailedPrecondition, "only AgenticFS with Agent Identity is supported")
	}
	for key, value := range req.VolumeContext {
		switch strings.ToLower(key) {
		case "useclient", "containernetworkfilesystem":
			if value != "" {
				return resolvedMount{}, status.Error(codes.FailedPrecondition, "NAS client selection and CNFS routing are not supported through the Substrate bridge")
			}
		}
		if (strings.EqualFold(key, "sandboxId") || key == PodUIDKey) && value != "" && value != actor.UID {
			return resolvedMount{}, status.Error(codes.PermissionDenied, "publish request carries a different actor identity")
		}
	}
	if err := validateActorMetadata(req.VolumeContext, actor); err != nil {
		return resolvedMount{}, err
	}
	resolved.Request = req
	return resolved, nil
}

func validateActorMetadata(values map[string]string, actor ActorInfo) error {
	if !agentidentity.HasActorIdentity(values) {
		return nil
	}
	if values[agentidentity.ActorUIDKey] != actor.UID || values[agentidentity.ActorNameKey] != actor.Name || values[agentidentity.ActorNamespaceKey] != actor.Atespace {
		return status.Error(codes.PermissionDenied, "actor metadata does not match the volume owner")
	}
	return nil
}

func setActorMetadata(values map[string]string, actor ActorInfo) {
	values[agentidentity.ActorUIDKey] = actor.UID
	values[agentidentity.ActorNameKey] = actor.Name
	values[agentidentity.ActorNamespaceKey] = actor.Atespace
}

func requestReadOnly(req *csi.NodePublishVolumeRequest) bool {
	return explicitReadOnly(req) || flagsContain(req.GetVolumeCapability().GetMount().GetMountFlags(), "ro")
}

func explicitReadOnly(req *csi.NodePublishVolumeRequest) bool {
	return mounterutils.ReadOnlyRequested(req.GetReadonly(), req.GetVolumeCapability().GetAccessMode().GetMode())
}

func flagsContain(flags []string, wanted string) bool {
	for _, flag := range flags {
		for _, option := range mounterutils.SplitMountOptions(flag) {
			if strings.TrimSpace(option) == wanted {
				return true
			}
		}
	}
	return false
}

func (n *Node) checkMountedReadOnly(target string, readOnly bool) error {
	if !readOnly {
		return nil
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target && !flagsContain(entry.Opts, "ro") {
			return status.Error(codes.AlreadyExists, "target access mode differs; unpublish before changing readonly")
		}
	}
	return nil
}

func (n *Node) mountAt(target string) (*mount.MountPoint, error) {
	if n.opts.Mounter == nil {
		return nil, status.Error(codes.FailedPrecondition, "mount inspector is required")
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target {
			return &entry, nil
		}
	}
	return nil, nil
}

func nasMount(entry *mount.MountPoint) bool {
	return entry.Type == "nfs" || entry.Type == "nfs4" || entry.Type == "alinas"
}

func (n *Node) placeholderSource(volumeID, target string) (string, error) {
	if !filepath.IsAbs(n.opts.StateDir) || filepath.Clean(n.opts.StateDir) == "/" {
		return "", status.Error(codes.FailedPrecondition, "an absolute dedicated placeholder directory is required")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(volumeID+"\x00"+target)))
	return filepath.Join(n.opts.StateDir, "placeholders", key), nil
}

func (n *Node) placeholderMounted(source, target string) (bool, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, status.Error(codes.FailedPrecondition, "placeholder source is not an owned directory")
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Path == target && entry.Device == source {
			return true, nil
		}
	}
	refs, err := n.opts.Mounter.GetMountRefs(source)
	if err != nil {
		return false, err
	}
	for _, ref := range refs {
		if ref == target {
			return true, nil
		}
	}
	return false, nil
}

func (n *Node) publishPlaceholder(volumeID, target string) error {
	live, err := n.mountAt(target)
	if err != nil {
		return err
	}
	source, err := n.placeholderSource(volumeID, target)
	if err != nil {
		return err
	}
	if live != nil {
		owned, err := n.placeholderMounted(source, target)
		if err != nil {
			return err
		}
		if !owned {
			return status.Error(codes.FailedPrecondition, "Golden target does not reference its expected source")
		}
		return nil
	}
	if info, err := os.Lstat(source); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return status.Error(codes.FailedPrecondition, "placeholder source is not an owned directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(source, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	return n.opts.Mounter.Mount(source, target, "", []string{"bind"})
}

func (n *Node) unpublishPlaceholder(source, target string) error {
	if err := n.opts.Mounter.Unmount(target); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Device == source || entry.Path == source || strings.HasPrefix(entry.Path, source+string(os.PathSeparator)) {
			return fmt.Errorf("refusing to remove mounted placeholder storage")
		}
	}
	refs, err := n.opts.Mounter.GetMountRefs(source)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("placeholder source still has mount references")
	}
	return os.RemoveAll(source)
}
