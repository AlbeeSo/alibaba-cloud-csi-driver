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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	mount "k8s.io/mount-utils"
)

func writablePublishFixture() *csi.NodePublishVolumeRequest {
	req := publishFixture()
	req.Readonly = false
	req.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER
	return req
}

func readonlyNodeFixture(t *testing.T, real *csi.NodePublishVolumeRequest) (*Node, *recordingNAS, *csi.NodePublishVolumeRequest) {
	t.Helper()
	annotation := annotationFixture(t, real)
	downstream := &recordingNAS{}
	node := NewNode(NodeOptions{
		ActorRoot: DefaultActorRoot, StateDir: t.TempDir(), NAS: downstream,
		Mounter: mount.NewFakeMounter(nil),
		Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
			return ActorInfo{UID: testUID, Atespace: "storage-test", Name: "actor", Annotation: annotation}, nil
		},
	})
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, actorMetadataFixture())
	require.NoError(t, err)
	return node, downstream, &csi.NodePublishVolumeRequest{
		VolumeId: testID, TargetPath: testTarget,
		VolumeCapability: writablePublishFixture().VolumeCapability,
		VolumeContext:    nodeContextFixture(resolved.Digest),
	}
}

func TestPublishHonorsReadonlySourcesWithoutMergingOtherFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*csi.NodePublishVolumeRequest, *csi.NodePublishVolumeRequest)
		wantRO bool
	}{
		{"writable", func(_, _ *csi.NodePublishVolumeRequest) {}, false},
		{"caller boolean", func(in, _ *csi.NodePublishVolumeRequest) { in.Readonly = true }, true},
		{"annotation boolean", func(_, real *csi.NodePublishVolumeRequest) { real.Readonly = true }, true},
		{"caller single reader", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		}, true},
		{"caller multi reader", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}, true},
		{"annotation reader", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}, true},
		{"caller ro flags", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.GetMount().MountFlags = []string{"vers=4", "rw,ro"}
		}, true},
		{"annotation ro flags", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.GetMount().MountFlags = []string{"tls", "vers=3", "rw,ro"}
		}, true},
		{"annotation options", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeContext["options"] = "tls,ram,rw,ro"
		}, true},
		{"quoted option is not ro", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.GetMount().MountFlags = []string{`context="value,ro,value"`}
		}, false},
		{"unrecognized context is not merged", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeContext["options"] = "ro"
			in.VolumeContext["ro"] = "true"
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := writablePublishFixture()
			in := &csi.NodePublishVolumeRequest{VolumeCapability: writablePublishFixture().VolumeCapability, VolumeContext: map[string]string{}}
			tc.mutate(in, real)
			node, downstream, request := readonlyNodeFixture(t, real)
			request.Readonly = in.Readonly
			request.VolumeCapability = in.VolumeCapability
			for key, value := range in.VolumeContext {
				request.VolumeContext[key] = value
			}
			request.VolumeContext["server"] = "must-not-replace-the-bound-server"
			request.VolumeContext["path"] = "/must-not-replace-the-bound-path"
			_, err := node.NodePublishVolume(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, tc.wantRO, downstream.published.Readonly)
			require.Equal(t, real.VolumeId, downstream.published.VolumeId)
			require.Equal(t, real.VolumeContext["server"], downstream.published.VolumeContext["server"])
			require.Equal(t, real.VolumeContext["path"], downstream.published.VolumeContext["path"])
			require.NotContains(t, downstream.published.VolumeCapability.GetMount().MountFlags, "vers=4")
			require.NotContains(t, downstream.published.VolumeContext, BindingDigestKey)
		})
	}
}

func TestReadonlyPublishRemovesOnlyConflictingWritableOptions(t *testing.T) {
	real := writablePublishFixture()
	real.VolumeCapability.GetMount().MountFlags = []string{"tls,rw,vers=3", `context="value,rw,value"`, "rw"}
	real.VolumeContext["options"] = "tls,ram,rw,nolock"
	node, downstream, in := readonlyNodeFixture(t, real)
	in.Readonly = true
	_, err := node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.True(t, downstream.published.Readonly)
	require.Equal(t, []string{"tls,vers=3", `context="value,rw,value"`}, downstream.published.VolumeCapability.GetMount().MountFlags)
	require.Equal(t, "tls,ram,nolock", downstream.published.VolumeContext["options"])
	require.Equal(t, []string{"tls,rw,vers=3", `context="value,rw,value"`, "rw"}, real.VolumeCapability.GetMount().MountFlags)
}
