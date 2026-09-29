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
	mount "k8s.io/mount-utils"
)

func TestGoldenDoesNotClaimItsSourceHiddenByAnotherMount(t *testing.T) {
	for _, operation := range []string{"publish", "unpublish"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, testUID, "volumes", "data")
			mounter := mount.NewFakeMounter(nil)
			node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Mounter: mounter, Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "template-uid", Atespace: "ate-golden", TemplateUID: "template-uid", Golden: true}, nil
			}})
			source, err := node.placeholderSource(testID, target)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(source, 0755))
			require.NoError(t, os.MkdirAll(target, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(source, "retain"), []byte("source data"), 0600))
			mounter.MountPoints = []mount.MountPoint{
				{Device: source, Path: target, Type: "none"},
				{Device: "foreign", Path: target, Type: "tmpfs"},
			}
			before := append([]mount.MountPoint(nil), mounter.MountPoints...)
			if operation == "publish" {
				attributes := goldenMetadataFixture()
				attributes[PodUIDKey] = "worker-pod-uid"
				_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, VolumeContext: attributes})
			} else {
				_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
			}
			require.Error(t, err)
			require.Equal(t, before, mounter.MountPoints)
			data, err := os.ReadFile(filepath.Join(source, "retain"))
			require.NoError(t, err)
			require.Equal(t, "source data", string(data))
		})
	}
}
