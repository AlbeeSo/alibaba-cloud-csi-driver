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
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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
	downstream.mounts = func() mount.Interface { return node.opts.Mounter }
	return node, downstream, &csi.NodePublishVolumeRequest{
		VolumeId: testID, TargetPath: testTarget,
		VolumeCapability: writablePublishFixture().VolumeCapability,
		VolumeContext:    nodeContextFixture(),
	}
}

func TestPublishPreservesInnerReadonlyConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*csi.NodePublishVolumeRequest, *csi.NodePublishVolumeRequest)
	}{
		{"writable", func(_, _ *csi.NodePublishVolumeRequest) {}},
		{"annotation boolean", func(_, real *csi.NodePublishVolumeRequest) { real.Readonly = true }},
		{"annotation single reader", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		}},
		{"annotation multi reader", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}},
		{"annotation ro flags", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.GetMount().MountFlags = []string{"tls", "vers=3", "rw,ro"}
		}},
		{"annotation options", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeContext["options"] = "tls,ram,rw,ro"
		}},
		{"mount flags override readonly options", func(_, real *csi.NodePublishVolumeRequest) {
			real.VolumeContext["options"] = "tls,ram,ro"
			real.VolumeCapability.GetMount().MountFlags = []string{"tls", "ram", "rw"}
		}},
		{"explicit readonly does not normalize flags", func(_, real *csi.NodePublishVolumeRequest) {
			real.Readonly = true
			real.VolumeCapability.GetMount().MountFlags = []string{"tls,rw,vers=3", `context="value,rw,value"`, "rw"}
			real.VolumeContext["options"] = "tls,ram,rw,nolock"
		}},
		{"quoted option is not ro", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.GetMount().MountFlags = []string{`context="value,ro,value"`}
		}},
		{"unrecognized context is not merged", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeContext["options"] = "ro"
			in.VolumeContext["ro"] = "true"
		}},
		{"obsolete controller digest is ignored", func(in, _ *csi.NodePublishVolumeRequest) {
			in.VolumeContext["csi.alibabacloud.com/substrate-binding-digest"] = "obsolete"
		}},
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
			want := proto.Clone(real).(*csi.NodePublishVolumeRequest)
			want.TargetPath = testTarget
			want.VolumeContext[SubstrateModeKey] = "true"
			for key, value := range nodeContextFixture() {
				want.VolumeContext[key] = value
			}
			_, err := node.NodePublishVolume(t.Context(), request)
			require.NoError(t, err)
			require.True(t, proto.Equal(want, downstream.published), "forwarded request differs from the annotation")
		})
	}
}

func TestPublishRejectsOuterReadonlyBeforeLookupOrMount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*csi.NodePublishVolumeRequest)
	}{
		{"boolean", func(in *csi.NodePublishVolumeRequest) { in.Readonly = true }},
		{"single reader", func(in *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		}},
		{"multi reader", func(in *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}},
		{"mount flag", func(in *csi.NodePublishVolumeRequest) { in.VolumeCapability.GetMount().MountFlags = []string{"ro"} }},
		{"combined mount flags", func(in *csi.NodePublishVolumeRequest) {
			in.VolumeCapability.GetMount().MountFlags = []string{"tls, ro ,rw"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, downstream, in := readonlyNodeFixture(t, writablePublishFixture())
			lookup := node.opts.Lookup
			calls := 0
			node.opts.Lookup = func(ctx context.Context, ref ActorReference) (ActorInfo, error) {
				calls++
				return lookup(ctx, ref)
			}
			tc.mutate(in)
			_, err := node.NodePublishVolume(t.Context(), in)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.Zero(t, calls)
			require.Nil(t, downstream.published)
			entries, err := os.ReadDir(node.opts.StateDir)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

type readonlyObservedNAS struct {
	csi.UnimplementedNodeServer
	published chan bool
}

func (n *readonlyObservedNAS) NodePublishVolume(_ context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	n.published <- req.Readonly
	return &csi.NodePublishVolumeResponse{}, nil
}

func (*readonlyObservedNAS) NodeUnpublishVolume(context.Context, *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func TestReadonlyPublishLifecycleThroughGRPC(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	node, _, in := readonlyNodeFixture(t, writablePublishFixture())
	readonlyAnnotation := annotationFixture(t, publishFixture())
	writableAnnotation := annotationFixture(t, writablePublishFixture())
	var innerReadonly atomic.Bool
	innerReadonly.Store(true)
	var lookups atomic.Int32
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		lookups.Add(1)
		annotation := writableAnnotation
		if innerReadonly.Load() {
			annotation = readonlyAnnotation
		}
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	nas := &readonlyObservedNAS{published: make(chan bool, 1)}
	node.opts.NAS = nas
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	csi.RegisterControllerServer(server, &Controller{})
	csi.RegisterNodeServer(server, node)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); require.NoError(t, <-done) })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	created, err := csi.NewControllerClient(conn).CreateVolume(ctx, &csi.CreateVolumeRequest{
		Name: testID, Parameters: actorMetadataFixture(), VolumeCapabilities: []*csi.VolumeCapability{in.VolumeCapability},
	})
	require.NoError(t, err)
	in.VolumeContext = created.Volume.VolumeContext
	require.Zero(t, lookups.Load())
	in.VolumeContext[PodUIDKey] = "worker-uid"
	in.Readonly = true
	client := csi.NewNodeClient(conn)
	_, err = client.NodePublishVolume(ctx, in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, lookups.Load())
	require.Empty(t, nas.published)
	in.Readonly = false
	_, err = client.NodePublishVolume(ctx, in)
	require.NoError(t, err)
	require.Equal(t, int32(1), lookups.Load())
	require.Len(t, nas.published, 1)
	require.True(t, <-nas.published)
	in.VolumeCapability.GetMount().MountFlags = []string{"ro"}
	_, err = client.NodePublishVolume(ctx, in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Equal(t, int32(1), lookups.Load())
	require.Empty(t, nas.published)
	in.VolumeCapability.GetMount().MountFlags = nil
	_, err = client.NodePublishVolume(ctx, in)
	require.NoError(t, err)
	require.Len(t, nas.published, 1)
	require.True(t, <-nas.published)
	innerReadonly.Store(false)
	_, err = client.NodePublishVolume(ctx, in)
	require.NoError(t, err)
	require.False(t, <-nas.published)
	_, err = client.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	_, err = client.NodePublishVolume(ctx, in)
	require.NoError(t, err)
	require.Len(t, nas.published, 1)
	require.False(t, <-nas.published)
}
