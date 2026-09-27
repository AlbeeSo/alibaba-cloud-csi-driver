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

package volumecontext

import "fmt"

const (
	ActorUIDKey       = "csi.alibabacloud.com/actor.uid"
	ActorNameKey      = "csi.alibabacloud.com/actor.name"
	ActorNamespaceKey = "csi.alibabacloud.com/actor.namespace"
	PodUIDKey         = "csi.storage.k8s.io/pod.uid"
	PodNameKey        = "csi.storage.k8s.io/pod.name"
	PodNamespaceKey   = "csi.storage.k8s.io/pod.namespace"
	SubstrateModeKey  = "csi.alibabacloud.com/substrate-mode"
)

func HasActorIdentity(values map[string]string) bool {
	for _, key := range []string{ActorUIDKey, ActorNameKey, ActorNamespaceKey} {
		if _, present := values[key]; present {
			return true
		}
	}
	return false
}

func ValidateActorIdentity(values map[string]string) error {
	if !HasActorIdentity(values) {
		return nil
	}
	uid := values[ActorUIDKey]
	if uid == "" || values[ActorNameKey] == "" || values[ActorNamespaceKey] == "" {
		return fmt.Errorf("actor UID, name and namespace must be supplied together")
	}
	if uid == "." || uid == ".." {
		return fmt.Errorf("actor UID must be a safe identifier")
	}
	for _, r := range uid {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("actor UID must be a safe identifier")
	}
	return nil
}

// ActorUID reads the legacy pod.uid only when no explicit Actor identity is present.
func ActorUID(values map[string]string) string {
	if HasActorIdentity(values) {
		return values[ActorUIDKey]
	}
	return values[PodUIDKey]
}

func MountOwnerUID(values map[string]string) string {
	if values[SubstrateModeKey] == "true" {
		return ActorUID(values)
	}
	return values[PodUIDKey]
}
