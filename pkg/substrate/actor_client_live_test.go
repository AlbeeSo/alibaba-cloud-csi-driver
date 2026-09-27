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
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActorLookupLive(t *testing.T) {
	endpoint := os.Getenv("SUBSTRATE_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("set SUBSTRATE_LIVE_* to run the read-only cluster probe")
	}
	client, err := NewActorClient(ActorClientOptions{
		Endpoint: endpoint, CAFile: os.Getenv("SUBSTRATE_LIVE_CA_FILE"),
		TokenFile: os.Getenv("SUBSTRATE_LIVE_TOKEN_FILE"), ServerName: os.Getenv("SUBSTRATE_LIVE_SERVER_NAME"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	ref := ActorReference{UID: os.Getenv("SUBSTRATE_LIVE_ACTOR_UID"), Name: os.Getenv("SUBSTRATE_LIVE_ACTOR_NAME"), Atespace: os.Getenv("SUBSTRATE_LIVE_ATESPACE")}
	actor, err := client.Lookup(t.Context(), ref)
	require.NoError(t, err)
	require.Equal(t, ref.UID, actor.UID)
	require.Equal(t, ref.Name, actor.Name)
	require.Equal(t, ref.Atespace, actor.Atespace)
	if ref.Atespace == "ate-golden" {
		require.True(t, actor.Golden)
	} else {
		require.NotEmpty(t, actor.Annotation)
	}
}
