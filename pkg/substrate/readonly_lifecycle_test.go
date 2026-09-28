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
	"fmt"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func TestInnerReadonlyModeCannotChangeAcrossRetriesOrRestart(t *testing.T) {
	for _, firstRO := range []bool{false, true} {
		t.Run(map[bool]string{false: "rw to ro", true: "ro to rw"}[firstRO], func(t *testing.T) {
			real := writablePublishFixture()
			real.Readonly = firstRO
			annotation := annotationFixture(t, real)
			node, downstream, in := readonlyNodeFixture(t, real)
			node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
			}
			_, err := node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			node = NewNode(node.opts)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			downstream.published = nil
			real.Readonly = !firstRO
			annotation = annotationFixture(t, real)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.Equal(t, codes.AlreadyExists, status.Code(err))
			require.Nil(t, downstream.published)
			_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
			require.NoError(t, err)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			require.Equal(t, !firstRO, downstream.published.Readonly)
		})
	}
}

func TestLegacyReadonlyBindingRetainsItsDigest(t *testing.T) {
	node, downstream, in := readonlyNodeFixture(t, publishFixture())
	real := publishFixture()
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, in.VolumeContext)
	require.NoError(t, err)
	legacy := binding{Version: 1, LogicalID: testID, Target: testTarget, ActorUID: testUID, VolumeName: "data", Kind: bindingNAS, Driver: NASDriverName, RealID: real.VolumeId, Digest: resolved.Digest}
	require.NoError(t, (bindingStore{root: node.opts.StateDir}).put(legacy))
	for range 2 {
		node = NewNode(node.opts)
		_, err := node.NodePublishVolume(t.Context(), in)
		require.NoError(t, err)
		require.True(t, downstream.published.Readonly)
	}
	stored, err := (bindingStore{root: node.opts.StateDir}).load(testID, testTarget)
	require.NoError(t, err)
	require.Equal(t, legacy, *stored)
}

func TestMountedReadonlyCheckUsesOnlyExplicitInnerRequirements(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*csi.NodePublishVolumeRequest)
		mountMode  string
		wantReject bool
	}{
		{"boolean requires ro", func(real *csi.NodePublishVolumeRequest) { real.Readonly = true }, "rw", true},
		{"single reader requires ro", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		}, "rw", true},
		{"multi reader requires ro", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}, "rw", true},
		{"matching readonly", func(real *csi.NodePublishVolumeRequest) { real.Readonly = true }, "ro", false},
		{"readonly options overridden by flags", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeContext["options"] = "tls,ram,ro"
			real.VolumeCapability.GetMount().MountFlags = []string{"tls", "ram", "rw"}
		}, "rw", false},
		{"flags are delegated to NAS", func(real *csi.NodePublishVolumeRequest) { real.VolumeCapability.GetMount().MountFlags = []string{"ro"} }, "rw", false},
		{"no explicit writable requirement", func(*csi.NodePublishVolumeRequest) {}, "ro", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := writablePublishFixture()
			tc.mutate(real)
			node, downstream, in := readonlyNodeFixture(t, real)
			resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, in.VolumeContext)
			require.NoError(t, err)
			stored := binding{Version: 1, LogicalID: testID, Target: testTarget, ActorUID: testUID, VolumeName: "data", Kind: bindingNAS, Driver: NASDriverName, RealID: real.VolumeId, Digest: resolved.Digest}
			require.NoError(t, (bindingStore{root: node.opts.StateDir}).put(stored))
			node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Type: "nfs", Opts: []string{tc.mountMode}}})
			_, err = node.NodePublishVolume(t.Context(), in)
			if tc.wantReject {
				require.Equal(t, codes.AlreadyExists, status.Code(err))
				require.Nil(t, downstream.published)
			} else {
				require.NoError(t, err)
				require.Equal(t, real.Readonly, downstream.published.Readonly)
			}
		})
	}
}

func TestGoldenPlaceholderRejectsOuterReadonlyAndRemainsWritable(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter(nil)
	lookups := 0
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		lookups++
		return ActorInfo{UID: testUID, Atespace: "ate-golden", Name: "template-uid", TemplateUID: "template-uid", Golden: true}, nil
	}
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Lookup: lookup, Mounter: mounter})
	attributes := goldenMetadataFixture()
	attributes[PodUIDKey] = "worker-uid"
	in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, Readonly: true, VolumeContext: attributes}
	_, err = node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, lookups)
	require.Empty(t, mounter.MountPoints)
	in.Readonly = false
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	require.Equal(t, []string{"bind"}, mounter.MountPoints[0].Opts)
	node = NewNode(node.opts)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.NoError(t, err)
	require.Empty(t, mounter.MountPoints)
}

func TestSaltedBindingRequiresUnpublishBeforeReuse(t *testing.T) {
	real := writablePublishFixture()
	node, downstream, in := readonlyNodeFixture(t, real)
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, in.VolumeContext)
	require.NoError(t, err)
	legacy := binding{Version: 1, LogicalID: testID, Target: testTarget, ActorUID: testUID, VolumeName: "data", Kind: bindingNAS, Driver: NASDriverName, RealID: real.VolumeId,
		Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(resolved.Digest+"\x00readonly")))}
	store := bindingStore{root: node.opts.StateDir}
	require.NoError(t, store.put(legacy))
	_, err = node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Nil(t, downstream.published)
	stored, err := store.load(testID, testTarget)
	require.NoError(t, err)
	require.Equal(t, legacy, *stored)
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.False(t, downstream.published.Readonly)
	stored, err = store.load(testID, testTarget)
	require.NoError(t, err)
	require.Equal(t, resolved.Digest, stored.Digest)
}
