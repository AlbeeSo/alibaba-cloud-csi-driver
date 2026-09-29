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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
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
