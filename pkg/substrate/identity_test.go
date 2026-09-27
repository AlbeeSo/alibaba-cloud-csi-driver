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
)

func actorMetadataFixture() map[string]string {
	return map[string]string{
		"csi.alibabacloud.com/actor.uid":       testUID,
		"csi.alibabacloud.com/actor.name":      "actor",
		"csi.alibabacloud.com/actor.namespace": "storage-test",
	}
}

func goldenMetadataFixture() map[string]string {
	attributes := actorMetadataFixture()
	attributes["csi.alibabacloud.com/actor.name"] = "template-uid"
	attributes["csi.alibabacloud.com/actor.namespace"] = "ate-golden"
	return attributes
}

func nodeContextFixture(digest string) map[string]string {
	attributes := actorMetadataFixture()
	attributes[PodUIDKey] = "worker-uid"
	attributes["csi.storage.k8s.io/pod.name"] = "worker-name"
	attributes["csi.storage.k8s.io/pod.namespace"] = "worker-space"
	attributes[BindingDigestKey] = digest
	return attributes
}

func identifiedLookup(t *testing.T) ActorLookup {
	t.Helper()
	annotation := annotationFixture(t, writablePublishFixture())
	return func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
	}
}

func TestCreateReturnsActorMetadataWithoutBindingWorker(t *testing.T) {
	controller := &Controller{Lookup: identifiedLookup(t)}
	parameters := actorMetadataFixture()
	parameters[PodUIDKey] = "worker-one"
	request := &csi.CreateVolumeRequest{Name: testID, Parameters: parameters, VolumeCapabilities: []*csi.VolumeCapability{writablePublishFixture().VolumeCapability}}
	first, err := controller.CreateVolume(t.Context(), request)
	require.NoError(t, err)
	for key, value := range actorMetadataFixture() {
		require.Equal(t, value, first.Volume.VolumeContext[key])
	}
	require.NotContains(t, first.Volume.VolumeContext, PodUIDKey)
	parameters[PodUIDKey] = "worker-two"
	second, err := controller.CreateVolume(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, first.Volume.VolumeContext[BindingDigestKey], second.Volume.VolumeContext[BindingDigestKey])
	parameters["csi.alibabacloud.com/actor.name"] = "wrong-actor"
	_, err = controller.CreateVolume(t.Context(), request)
	require.Error(t, err)
}

func TestPublishKeepsActorIdentityWhileWorkerChanges(t *testing.T) {
	real := writablePublishFixture()
	node, downstream, request := readonlyNodeFixture(t, real)
	node.opts.Lookup = identifiedLookup(t)
	for key, value := range actorMetadataFixture() {
		request.VolumeContext[key] = value
	}
	for _, worker := range []string{"worker-one", "worker-two"} {
		request.VolumeContext[PodUIDKey] = worker
		request.VolumeContext["csi.storage.k8s.io/pod.name"] = worker + "-name"
		request.VolumeContext["csi.storage.k8s.io/pod.namespace"] = worker + "-pool"
		_, err := node.NodePublishVolume(t.Context(), request)
		require.NoError(t, err)
		require.Equal(t, worker, downstream.published.VolumeContext[PodUIDKey])
		require.Equal(t, worker+"-name", downstream.published.VolumeContext["csi.storage.k8s.io/pod.name"])
		require.Equal(t, worker+"-pool", downstream.published.VolumeContext["csi.storage.k8s.io/pod.namespace"])
		for key, value := range actorMetadataFixture() {
			require.Equal(t, value, downstream.published.VolumeContext[key])
		}
		require.Equal(t, testTarget, downstream.published.TargetPath)
	}
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
	require.Equal(t, real.VolumeId, downstream.unpublished.VolumeId)
}

func TestPublishRejectsIncompleteOrMismatchedActorMetadata(t *testing.T) {
	for _, key := range []string{"csi.alibabacloud.com/actor.uid", "csi.alibabacloud.com/actor.name", "csi.alibabacloud.com/actor.namespace"} {
		t.Run(key, func(t *testing.T) {
			for _, value := range []string{"", "different"} {
				node, downstream, request := readonlyNodeFixture(t, writablePublishFixture())
				node.opts.Lookup = identifiedLookup(t)
				for name, actual := range actorMetadataFixture() {
					request.VolumeContext[name] = actual
				}
				request.VolumeContext[key] = value
				_, err := node.NodePublishVolume(t.Context(), request)
				require.Error(t, err)
				require.Nil(t, downstream.published)
			}
		})
	}
}
