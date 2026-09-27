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

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActorIdentityDoesNotFallBackToWorkerWhenIncomplete(t *testing.T) {
	legacy := map[string]string{PodUIDKey: "legacy-actor", SubstrateModeKey: "true"}
	require.Equal(t, "legacy-actor", ActorUID(legacy))
	current := map[string]string{ActorUIDKey: "actor-id", ActorNameKey: "actor-name", ActorNamespaceKey: "actor-space", PodUIDKey: "worker-id", SubstrateModeKey: "true"}
	require.Equal(t, "actor-id", ActorUID(current))
	require.Equal(t, "actor-id", MountOwnerUID(current))
	delete(current, ActorUIDKey)
	require.Empty(t, ActorUID(current))
	current[SubstrateModeKey] = "false"
	require.Equal(t, "worker-id", MountOwnerUID(current))
}

func TestActorUIDCannotBecomePathOrMountOption(t *testing.T) {
	for _, uid := range []string{"../other", "..", "/root", "actor,option=value", "actor\x00id"} {
		values := map[string]string{ActorUIDKey: uid, ActorNameKey: "actor", ActorNamespaceKey: "space"}
		require.Error(t, ValidateActorIdentity(values))
	}
	require.NoError(t, ValidateActorIdentity(map[string]string{ActorUIDKey: "actor-id", ActorNameKey: "actor", ActorNamespaceKey: "space"}))
}
