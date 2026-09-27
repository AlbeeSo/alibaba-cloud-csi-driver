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
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/volumecontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func validateActorMetadata(values map[string]string, actor ActorInfo) error {
	if !volumecontext.HasActorIdentity(values) {
		return nil
	}
	if err := volumecontext.ValidateActorIdentity(values); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if values[volumecontext.ActorUIDKey] != actor.UID || values[volumecontext.ActorNameKey] != actor.Name || values[volumecontext.ActorNamespaceKey] != actor.Atespace {
		return status.Error(codes.PermissionDenied, "actor metadata does not match the volume owner")
	}
	return nil
}

func setActorMetadata(values map[string]string, actor ActorInfo) {
	values[volumecontext.ActorUIDKey] = actor.UID
	values[volumecontext.ActorNameKey] = actor.Name
	values[volumecontext.ActorNamespaceKey] = actor.Atespace
}
