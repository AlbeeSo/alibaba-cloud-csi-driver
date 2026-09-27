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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func bindingFixture() binding {
	return binding{Version: 1, LogicalID: testID, Target: testTarget, ActorUID: testUID, VolumeName: "data", Kind: bindingNAS, Driver: NASDriverName, RealID: "customer-pv", Digest: strings.Repeat("a", 64)}
}

func TestBindingStoreSurvivesRestartAndRejectsDrift(t *testing.T) {
	root := t.TempDir()
	store := bindingStore{root: root}
	want := bindingFixture()
	require.NoError(t, store.put(want))
	restarted := bindingStore{root: root}
	got, err := restarted.load(want.LogicalID, want.Target)
	require.NoError(t, err)
	require.Equal(t, &want, got)
	require.NoError(t, restarted.put(want))
	changed := want
	changed.RealID = "different-pv"
	require.Error(t, restarted.put(changed))
	require.NoError(t, restarted.remove(want.LogicalID, want.Target))
	_, err = restarted.load(want.LogicalID, want.Target)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestBindingStoreRejectsCorruption(t *testing.T) {
	store := bindingStore{root: t.TempDir()}
	want := bindingFixture()
	require.NoError(t, store.put(want))
	require.NoError(t, os.WriteFile(store.file(want.LogicalID, want.Target), []byte(`{"version":1,"kind":"unknown"}`), 0600))
	_, err := store.load(want.LogicalID, want.Target)
	require.Error(t, err)
}

func TestBindingStoreIsTargetScoped(t *testing.T) {
	store := bindingStore{root: t.TempDir()}
	one := bindingFixture()
	two := one
	two.Target = "/another/actor/volumes/data"
	require.NoError(t, store.put(one))
	require.NoError(t, store.put(two))
	require.NotEqual(t, store.file(one.LogicalID, one.Target), store.file(two.LogicalID, two.Target))
}
