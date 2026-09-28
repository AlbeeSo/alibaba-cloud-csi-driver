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
	"sync/atomic"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

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
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Empty(t, nas.published)
	_, err = client.NodeUnpublishVolume(ctx, &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	_, err = client.NodePublishVolume(ctx, in)
	require.NoError(t, err)
	require.Len(t, nas.published, 1)
	require.False(t, <-nas.published)
}
