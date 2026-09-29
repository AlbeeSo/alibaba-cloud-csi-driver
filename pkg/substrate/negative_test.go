// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package substrate

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func TestPublishRejectsUseclientAndCNFS(t *testing.T) {
	for _, key := range []string{"useclient", "UseClient", "containernetworkfilesystem", "ContainerNetworkFileSystem"} {
		t.Run(key, func(t *testing.T) {
			real := writablePublishFixture()
			real.VolumeContext[key] = "efc"
			annotation := annotationFixture(t, real)
			node, _, in := readonlyNodeFixture(t, real)
			node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
			}
			_, err := node.NodePublishVolume(t.Context(), in)
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "not supported through the Substrate bridge")
		})
	}
}

func TestPublishRejectsInvalidVolumeID(t *testing.T) {
	node, _, _ := readonlyNodeFixture(t, writablePublishFixture())
	_, err := node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{
		VolumeId:      "not-a-substrate-id",
		TargetPath:    testTarget,
		VolumeContext: nodeContextFixture(),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestPublishRejectsMissingWorkerPodUID(t *testing.T) {
	real := writablePublishFixture()
	annotation := annotationFixture(t, real)
	node, _, in := readonlyNodeFixture(t, real)
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	delete(in.VolumeContext, PodUIDKey)
	_, err := node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.ErrorContains(t, err, "worker Pod UID is required")
}

func TestPublishRejectsOuterReadonly(t *testing.T) {
	real := writablePublishFixture()
	annotation := annotationFixture(t, real)
	node, _, in := readonlyNodeFixture(t, real)
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	in.Readonly = true
	_, err := node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "outer readonly")
}

func TestPublishRejectsWhenNASNotConfigured(t *testing.T) {
	real := writablePublishFixture()
	annotation := annotationFixture(t, real)
	node, _, in := readonlyNodeFixture(t, real)
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	node.opts.NAS = nil
	_, err := node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "NAS forwarding is not configured")
}

func TestPublishRejectsUnsupportedFilesystemAtTarget(t *testing.T) {
	real := writablePublishFixture()
	annotation := annotationFixture(t, real)
	node, _, in := readonlyNodeFixture(t, real)
	node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
	node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Device: "/dev/sda1", Type: "ext4"}})
	_, err := node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "unsupported filesystem")
}

func TestUnpublishRejectsUnsupportedLiveMount(t *testing.T) {
	node := NewNode(NodeOptions{
		ActorRoot: DefaultActorRoot,
		StateDir:  t.TempDir(),
		Mounter:   mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Device: "/dev/sda1", Type: "ext4"}}),
		NAS:       &recordingNAS{},
	})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "refusing to unmount an unsupported live mount")
}

func TestUnpublishSucceedsWhenNothingMounted(t *testing.T) {
	node := NewNode(NodeOptions{
		ActorRoot: DefaultActorRoot,
		StateDir:  t.TempDir(),
		Mounter:   mount.NewFakeMounter(nil),
		NAS:       &recordingNAS{},
	})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
}
