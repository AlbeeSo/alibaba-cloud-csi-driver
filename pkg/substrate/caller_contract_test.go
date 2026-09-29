// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package substrate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublishKeepsActorOwnershipWhenWorkerChanges(t *testing.T) {
	real := writablePublishFixture()
	real.VolumeContext[PodUIDKey] = "previous-worker"
	node, downstream, in := readonlyNodeFixture(t, real)
	for _, worker := range []string{"worker-one", "worker-two"} {
		in.VolumeContext[PodUIDKey] = worker
		in.VolumeContext["csi.storage.k8s.io/pod.name"] = worker + "-name"
		in.VolumeContext["csi.storage.k8s.io/pod.namespace"] = worker + "-namespace"
		_, err := node.NodePublishVolume(t.Context(), in)
		require.NoError(t, err)
		require.Equal(t, worker, downstream.published.VolumeContext[PodUIDKey])
		require.Equal(t, worker+"-name", downstream.published.VolumeContext["csi.storage.k8s.io/pod.name"])
		require.Equal(t, worker+"-namespace", downstream.published.VolumeContext["csi.storage.k8s.io/pod.namespace"])
		for key, value := range actorMetadataFixture() {
			require.Equal(t, value, downstream.published.VolumeContext[key])
		}
	}
}

func TestIgnoredStoredPathsDoNotRestrictPublish(t *testing.T) {
	for _, storedTarget := range []string{"", "relative", "/guest/../unused"} {
		t.Run(storedTarget, func(t *testing.T) {
			real := writablePublishFixture()
			real.TargetPath = storedTarget
			real.StagingTargetPath = "stored/staging/is/unused/by/nas"
			node, downstream, in := readonlyNodeFixture(t, real)
			in.StagingTargetPath = "/outer/staging/must-not-replace-inner"
			_, err := node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			require.Equal(t, testTarget, downstream.published.TargetPath)
			require.Equal(t, real.StagingTargetPath, downstream.published.StagingTargetPath)
		})
	}
}

func TestPublishDoesNotReintroduceAnnotationSizeLimit(t *testing.T) {
	real := writablePublishFixture()
	real.VolumeContext["opaque-configuration"] = strings.Repeat("x", 300*1024)
	node, downstream, in := readonlyNodeFixture(t, real)
	_, err := node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Equal(t, real.VolumeContext["opaque-configuration"], downstream.published.VolumeContext["opaque-configuration"])
}
