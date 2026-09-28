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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	mount "k8s.io/mount-utils"
)

const testUID = "1c3d0394-02df-4aa9-b9ec-27604d5b7601"
const testID = "substrate-" + testUID + "-data"
const testTarget = "/var/lib/ateom-gvisor/actors/" + testUID + "/volumes/data"

type recordingNAS struct {
	csi.UnimplementedNodeServer
	published   *csi.NodePublishVolumeRequest
	unpublished *csi.NodeUnpublishVolumeRequest
	err         error
}

func (n *recordingNAS) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	n.published = req
	return &csi.NodePublishVolumeResponse{}, n.err
}

func (n *recordingNAS) NodeUnpublishVolume(_ context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	n.unpublished = req
	return &csi.NodeUnpublishVolumeResponse{}, n.err
}

func publishFixture() *csi.NodePublishVolumeRequest {
	return &csi.NodePublishVolumeRequest{
		VolumeId: "customer-pv-a1b2c3", TargetPath: "/data", Readonly: true,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{FsType: "nfs", MountFlags: []string{"tls", "vers=3"}}},
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY},
		},
		VolumeContext:  map[string]string{"authType": "agent-identity", "mountProtocol": "alinas", "server": "ap-example.fs.cn-beijing.agenticfs.aliyuncs.com", "path": "/tenant-a", "sandboxCredProviderName": "read-only"},
		PublishContext: map[string]string{"preserved": "value"},
	}
}

func annotationFixture(t *testing.T, request *csi.NodePublishVolumeRequest) string {
	t.Helper()
	raw, err := protojson.Marshal(request)
	require.NoError(t, err)
	encoded, err := json.Marshal([]publishEntry{{VolumeName: "data", Driver: NASDriverName, Request: raw}})
	require.NoError(t, err)
	return string(encoded)
}

func TestPublishPreservesRequestAndUsesActorIdentity(t *testing.T) {
	request := publishFixture()
	annotation := annotationFixture(t, request)
	downstream := &recordingNAS{}
	node := NewNode(NodeOptions{NodeID: "node", ActorRoot: "/var/lib/ateom-gvisor/actors", NAS: downstream, StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil),
		Lookup: func(_ context.Context, ref ActorReference) (ActorInfo, error) {
			require.Equal(t, testUID, ref.UID)
			return ActorInfo{UID: ref.UID, Atespace: "storage-test", Name: "actor", Annotation: annotation}, nil
		},
	})
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, actorMetadataFixture())
	require.NoError(t, err)
	_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: nodeContextFixture(resolved.Digest)})
	require.NoError(t, err)
	want := proto.Clone(request).(*csi.NodePublishVolumeRequest)
	want.TargetPath = testTarget
	want.VolumeContext["csi.alibabacloud.com/substrate-mode"] = "true"
	for key, value := range nodeContextFixture("") {
		if key != BindingDigestKey {
			want.VolumeContext[key] = value
		}
	}
	require.True(t, proto.Equal(want, downstream.published))
	require.Equal(t, "/data", request.TargetPath)
	require.NotContains(t, request.VolumeContext, "csi.storage.k8s.io/pod.uid")
}

func TestPublishRejectsInvalidBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*csi.NodePublishVolumeRequest, *csi.NodePublishVolumeRequest)
	}{
		{"missing actor UID", func(in, _ *csi.NodePublishVolumeRequest) { delete(in.VolumeContext, "csi.alibabacloud.com/actor.uid") }},
		{"foreign target", func(in, _ *csi.NodePublishVolumeRequest) { in.TargetPath = "/etc/data" }},
		{"wrong volume ID", func(in, _ *csi.NodePublishVolumeRequest) { in.VolumeId = "unrelated-volume" }},
		{"secrets", func(_, real *csi.NodePublishVolumeRequest) {
			real.Secrets = map[string]string{"key": "must-not-persist"}
		}},
		{"foreign identity", func(_, real *csi.NodePublishVolumeRequest) { real.VolumeContext["sandboxId"] = "another-actor" }},
		{"non agent identity", func(_, real *csi.NodePublishVolumeRequest) { real.VolumeContext["authType"] = "access-key" }},
		{"non AgenticFS", func(_, real *csi.NodePublishVolumeRequest) { real.VolumeContext["mountProtocol"] = "nfs" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := publishFixture()
			in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: nodeContextFixture("")}
			tc.mutate(in, real)
			annotation := annotationFixture(t, real)
			downstream := &recordingNAS{}
			node := NewNode(NodeOptions{ActorRoot: "/var/lib/ateom-gvisor/actors", NAS: downstream, Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
			}})
			_, err := node.NodePublishVolume(t.Context(), in)
			require.Error(t, err)
			require.Nil(t, downstream.published)
		})
	}
}

func TestUnpublishResolvesIdentityWithoutVolumeContext(t *testing.T) {
	downstream := &recordingNAS{}
	annotation := annotationFixture(t, publishFixture())
	state := t.TempDir()
	node := NewNode(NodeOptions{ActorRoot: "/var/lib/ateom-gvisor/actors", NAS: downstream, StateDir: state, Mounter: mount.NewFakeMounter(nil), Lookup: func(_ context.Context, ref ActorReference) (ActorInfo, error) {
		require.Equal(t, testUID, ref.UID)
		return ActorInfo{UID: ref.UID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}})
	resolved, err := resolveMount(t.Context(), node.opts.Lookup, testID, actorMetadataFixture())
	require.NoError(t, err)
	_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: nodeContextFixture(resolved.Digest)})
	require.NoError(t, err)
	restarted := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: state, Mounter: mount.NewFakeMounter(nil), NAS: downstream})
	_, err = restarted.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	require.Equal(t, "customer-pv-a1b2c3", downstream.unpublished.VolumeId)
	require.Equal(t, testTarget, downstream.unpublished.TargetPath)
}

func TestUnpublishDoesNotNeedActorLookup(t *testing.T) {
	downstream := &recordingNAS{}
	state := t.TempDir()
	require.NoError(t, (bindingStore{root: state}).put(bindingFixture()))
	node := NewNode(NodeOptions{ActorRoot: "/var/lib/ateom-gvisor/actors", NAS: downstream, StateDir: state, Mounter: mount.NewFakeMounter(nil), Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
		t.Fatal("unpublish must not call the Actor API")
		return ActorInfo{}, status.Error(codes.Unavailable, "control plane unavailable")
	}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	require.Equal(t, "customer-pv", downstream.unpublished.VolumeId)
}

func TestNodeGetInfoRequiresNodeIdentity(t *testing.T) {
	_, err := NewNode(NodeOptions{}).NodeGetInfo(t.Context(), &csi.NodeGetInfoRequest{})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	info, err := NewNode(NodeOptions{NodeID: "worker-node"}).NodeGetInfo(t.Context(), &csi.NodeGetInfoRequest{})
	require.NoError(t, err)
	require.Equal(t, "worker-node", info.NodeId)
}
