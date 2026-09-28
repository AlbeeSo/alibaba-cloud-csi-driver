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
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func TestReadonlyModeCannotChangeAcrossRetriesOrRestart(t *testing.T) {
	for _, firstRO := range []bool{false, true} {
		t.Run(map[bool]string{false: "rw to ro", true: "ro to rw"}[firstRO], func(t *testing.T) {
			node, downstream, in := readonlyNodeFixture(t, writablePublishFixture())
			in.Readonly = firstRO
			_, err := node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			node = NewNode(node.opts)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			downstream.published = nil
			in.Readonly = !firstRO
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
	for _, callerRO := range []bool{false, true} {
		in.Readonly = callerRO
		_, err := node.NodePublishVolume(t.Context(), in)
		require.NoError(t, err)
		require.True(t, downstream.published.Readonly)
	}
	stored, err := (bindingStore{root: node.opts.StateDir}).load(testID, testTarget)
	require.NoError(t, err)
	require.Equal(t, legacy, *stored)
}

func TestReadonlyPublishDoesNotClaimExistingWritableMount(t *testing.T) {
	real := writablePublishFixture()
	real.VolumeContext["options"] = "ro"
	real.VolumeCapability.GetMount().MountFlags = []string{"rw"}
	node, downstream, in := readonlyNodeFixture(t, real)
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, in.VolumeContext)
	require.NoError(t, err)
	legacy := binding{Version: 1, LogicalID: testID, Target: testTarget, ActorUID: testUID, VolumeName: "data", Kind: bindingNAS, Driver: NASDriverName, RealID: real.VolumeId, Digest: resolved.Digest}
	require.NoError(t, (bindingStore{root: node.opts.StateDir}).put(legacy))
	node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Type: "nfs", Opts: []string{"rw"}}})
	_, err = node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Nil(t, downstream.published)
}

func TestGoldenReadonlyPlaceholderHonorsCallerAndRejectsModeChange(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter(nil)
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "ate-golden", Name: "template-uid", TemplateUID: "template-uid", Golden: true}, nil
	}
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Lookup: lookup, Mounter: mounter})
	attributes := goldenMetadataFixture()
	attributes[PodUIDKey] = "worker-uid"
	in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, Readonly: true, VolumeContext: attributes}
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	require.Contains(t, mounter.MountPoints[0].Opts, "ro")
	node = NewNode(node.opts)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	in.Readonly = false
	_, err = node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.NoError(t, err)
	require.Empty(t, mounter.MountPoints)
}
