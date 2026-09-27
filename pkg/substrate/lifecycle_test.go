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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func TestGoldenPlaceholderLifecycleWithoutNAS(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	state := t.TempDir()
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter(nil)
	nas := &recordingNAS{err: errors.New("golden must not call NAS")}
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "ate-golden", Name: "template-uid", TemplateUID: "template-uid", Golden: true}, nil
	}
	attributes := goldenMetadataFixture()
	resolved, err := resolveMount(t.Context(), lookup, testID, attributes)
	require.NoError(t, err)
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: state, Mounter: mounter, NAS: nas, Lookup: lookup})
	attributes[PodUIDKey], attributes[BindingDigestKey] = "worker-uid", resolved.Digest
	in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, VolumeContext: attributes}
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Nil(t, nas.published)
	require.DirExists(t, target)
	require.Len(t, mounter.MountPoints, 1)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	placeholder := (bindingStore{root: state}).placeholder(testID, target)
	require.NoError(t, os.WriteFile(filepath.Join(placeholder, "build-output"), []byte("temporary"), 0600))
	restarted := NewNode(NodeOptions{ActorRoot: root, StateDir: state, Mounter: mounter, NAS: nas})
	_, err = restarted.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.NoError(t, err)
	require.Nil(t, nas.unpublished)
	require.Empty(t, mounter.MountPoints)
	require.NoDirExists(t, placeholder)
	_, err = restarted.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.NoError(t, err)
}

func TestUnpublishKeepsBindingWhenNASFails(t *testing.T) {
	state := t.TempDir()
	store := bindingStore{root: state}
	b := bindingFixture()
	require.NoError(t, store.put(b))
	nas := &recordingNAS{err: status.Error(codes.Unavailable, "daemon unavailable")}
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: state, Mounter: mount.NewFakeMounter(nil), NAS: nas, Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
		t.Fatal("unpublish must not query actor")
		return ActorInfo{}, nil
	}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.Equal(t, codes.Unavailable, status.Code(err))
	_, err = store.load(testID, testTarget)
	require.NoError(t, err)
}

func TestUntrackedMountIsNeverClaimedOrRemoved(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "unknown", Path: target, Type: "nfs"}})
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Mounter: mounter, NAS: &recordingNAS{}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Len(t, mounter.MountPoints, 1)
}

func TestControllerBindingRejectsAnnotationDriftOnAnotherNode(t *testing.T) {
	annotation := annotationFixture(t, publishFixture())
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "storage-test", Name: "actor", Annotation: annotation}, nil
	}
	created, err := (&Controller{Lookup: lookup}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, Parameters: actorMetadataFixture(), VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
	require.NoError(t, err)
	changed := publishFixture()
	changed.VolumeContext["path"] = "/another-tenant"
	annotation = annotationFixture(t, changed)
	nas := &recordingNAS{}
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil), Lookup: lookup, NAS: nas})
	context := created.Volume.VolumeContext
	context[PodUIDKey] = testUID
	_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: context})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Nil(t, nas.published)
}
