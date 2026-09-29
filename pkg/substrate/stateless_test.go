// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

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

func TestStatelessPublishAcceptsCorrectionAfterFailureAndRestart(t *testing.T) {
	real := writablePublishFixture()
	annotation := annotationFixture(t, real)
	node, downstream, in := readonlyNodeFixture(t, real)
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	downstream.err = status.Error(codes.Unavailable, "temporary failure")
	_, err := node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.Unavailable, status.Code(err))
	real.VolumeContext["path"] = "/corrected"
	annotation = annotationFixture(t, real)
	downstream.err = nil
	node = NewNode(node.opts)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, "/corrected", downstream.published.VolumeContext["path"])
}

func TestStatelessPublishDoesNotWriteBindingFiles(t *testing.T) {
	node, _, in := readonlyNodeFixture(t, writablePublishFixture())
	_, err := node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	entries, err := os.ReadDir(node.opts.StateDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStatelessUnpublishUsesLiveNASAndLogicalLockKey(t *testing.T) {
	nas := &recordingNAS{}
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, NAS: nas, Mounter: mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Device: "server:/", Type: "nfs"}}), Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
		t.Fatal("unpublish must not query actor")
		return ActorInfo{}, nil
	}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	require.Equal(t, testID, nas.unpublished.VolumeId)
	require.Equal(t, testTarget, nas.unpublished.TargetPath)
	stateFile := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(stateFile, []byte("unused for NAS"), 0600))
	node.opts.StateDir = stateFile
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
}

func TestStatelessUnpublishWithoutMountNeedsNoBackendOrState(t *testing.T) {
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, Mounter: mount.NewFakeMounter(nil)})
	for range 2 {
		_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
		require.NoError(t, err)
	}
}

type waitingNAS struct {
	csi.UnimplementedNodeServer
	entered, release chan struct{}
}

func (n *waitingNAS) NodePublishVolume(context.Context, *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	close(n.entered)
	<-n.release
	return &csi.NodePublishVolumeResponse{}, nil
}

func TestStatelessTargetOperationsStaySerialized(t *testing.T) {
	node, _, in := readonlyNodeFixture(t, writablePublishFixture())
	nas := &waitingNAS{entered: make(chan struct{}), release: make(chan struct{})}
	node.opts.NAS = nas
	done := make(chan error, 1)
	go func() { _, err := node.NodePublishVolume(t.Context(), in); done <- err }()
	<-nas.entered
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.Equal(t, codes.Aborted, status.Code(err))
	close(nas.release)
	require.NoError(t, <-done)
}
