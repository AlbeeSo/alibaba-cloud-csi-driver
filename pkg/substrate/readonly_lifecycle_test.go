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
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func TestExplicitReadonlyStillChecksLiveModeAfterRestart(t *testing.T) {
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
			mode := "rw"
			if firstRO {
				mode = "ro"
			}
			fake := node.opts.Mounter.(*mount.FakeMounter)
			fake.MountPoints = []mount.MountPoint{{Device: "server:/", Path: testTarget, Type: "nfs", Opts: []string{mode}}}
			node = NewNode(node.opts)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			downstream.published = nil
			real.Readonly = !firstRO
			annotation = annotationFixture(t, real)
			_, err = node.NodePublishVolume(t.Context(), in)
			if firstRO {
				require.NoError(t, err)
				require.False(t, downstream.published.Readonly)
			} else {
				require.Equal(t, codes.AlreadyExists, status.Code(err))
				require.Nil(t, downstream.published)
			}
			_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
			require.NoError(t, err)
			fake.MountPoints = nil
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			require.Equal(t, !firstRO, downstream.published.Readonly)
		})
	}
}

func TestReadonlyPublishNeedsNoStateDirectory(t *testing.T) {
	node, downstream, in := readonlyNodeFixture(t, publishFixture())
	node.opts.StateDir = ""
	for range 2 {
		node = NewNode(node.opts)
		_, err := node.NodePublishVolume(t.Context(), in)
		require.NoError(t, err)
		require.True(t, downstream.published.Readonly)
	}
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
			node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Type: "nfs", Opts: []string{tc.mountMode}}})
			_, err := node.NodePublishVolume(t.Context(), in)
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

func TestObsoleteMetadataCannotBlockStatelessPublish(t *testing.T) {
	real := writablePublishFixture()
	node, downstream, in := readonlyNodeFixture(t, real)
	path := filepath.Join(node.opts.StateDir, "legacy.json")
	require.NoError(t, os.WriteFile(path, []byte("corrupt or obsolete metadata"), 0600))
	_, err := node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.False(t, downstream.published.Readonly)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "corrupt or obsolete metadata", string(data))
}
