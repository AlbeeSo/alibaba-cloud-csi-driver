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
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: state, Mounter: mounter, NAS: nas, Lookup: lookup})
	attributes[PodUIDKey] = "worker-uid"
	in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, VolumeContext: attributes}
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Nil(t, nas.published)
	require.DirExists(t, target)
	require.Len(t, mounter.MountPoints, 1)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	placeholder, err := node.placeholderSource(testID, target)
	require.NoError(t, err)
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

func TestUnpublishRetriesLiveMountWhenNASFails(t *testing.T) {
	state := t.TempDir()
	nas := &recordingNAS{err: status.Error(codes.Unavailable, "daemon unavailable")}
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: state, Mounter: mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Device: "server:/", Type: "nfs"}}), NAS: nas, Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
		t.Fatal("unpublish must not query actor")
		return ActorInfo{}, nil
	}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.Equal(t, codes.Unavailable, status.Code(err))
	nas.err = nil
	node = NewNode(node.opts)
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
	require.NoError(t, err)
}

func TestForeignFilesystemIsNotRemoved(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "unknown", Path: target, Type: "tmpfs"}})
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Mounter: mounter, NAS: &recordingNAS{}})
	_, err := node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Len(t, mounter.MountPoints, 1)
}

func TestFirstPublishUsesAnnotationChangedAfterCreate(t *testing.T) {
	annotation := annotationFixture(t, publishFixture())
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		return ActorInfo{UID: testUID, Atespace: "storage-test", Name: "actor", Annotation: annotation}, nil
	}
	created, err := (&Controller{}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, Parameters: actorMetadataFixture(), VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
	require.NoError(t, err)
	changed := publishFixture()
	changed.VolumeContext["path"] = "/another-tenant"
	annotation = annotationFixture(t, changed)
	nas := &recordingNAS{}
	node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil), Lookup: lookup, NAS: nas})
	context := created.Volume.VolumeContext
	context[PodUIDKey] = "worker-uid"
	_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: context})
	require.NoError(t, err)
	require.Equal(t, "/another-tenant", nas.published.VolumeContext["path"])
}

func TestCreateSucceedsBeforePublishConfigurationIsAvailable(t *testing.T) {
	for _, tc := range []struct {
		name       string
		annotation string
		lookupErr  error
		wantCode   codes.Code
	}{
		{"missing annotation", "", nil, codes.FailedPrecondition},
		{"invalid annotation", "not JSON", nil, codes.FailedPrecondition},
		{"API unavailable", "", status.Error(codes.Unavailable, "API unavailable"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created, err := (&Controller{}).CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: testID, Parameters: actorMetadataFixture(), VolumeCapabilities: []*csi.VolumeCapability{publishFixture().VolumeCapability}})
			require.NoError(t, err)
			calls := 0
			nas := &recordingNAS{}
			node := NewNode(NodeOptions{ActorRoot: DefaultActorRoot, StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil), NAS: nas, Lookup: func(context.Context, ActorReference) (ActorInfo, error) {
				calls++
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: tc.annotation}, tc.lookupErr
			}})
			created.Volume.VolumeContext[PodUIDKey] = "worker-uid"
			_, err = node.NodePublishVolume(t.Context(), &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: testTarget, VolumeContext: created.Volume.VolumeContext})
			require.Equal(t, tc.wantCode, status.Code(err))
			require.Equal(t, 1, calls)
			require.Nil(t, nas.published)
		})
	}
}

func TestPublishUsesCurrentConfigurationWithoutBindingConflicts(t *testing.T) {
	for _, failedFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "mounted", true: "failed publish"}[failedFirst], func(t *testing.T) {
			real := writablePublishFixture()
			annotation := annotationFixture(t, real)
			node, nas, request := readonlyNodeFixture(t, real)
			node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
			}
			if failedFirst {
				nas.err = status.Error(codes.Unavailable, "NAS unavailable")
			}
			_, err := node.NodePublishVolume(t.Context(), request)
			if failedFirst {
				require.Equal(t, codes.Unavailable, status.Code(err))
			} else {
				require.NoError(t, err)
				_, err = node.NodePublishVolume(t.Context(), request)
				require.NoError(t, err)
			}
			node = NewNode(node.opts)
			nas.err, nas.published = nil, nil
			real.VolumeContext["path"] = "/changed-after-binding"
			annotation = annotationFixture(t, real)
			_, err = node.NodePublishVolume(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, "/changed-after-binding", nas.published.VolumeContext["path"])
			_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
			require.NoError(t, err)
			_, err = node.NodePublishVolume(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, "/changed-after-binding", nas.published.VolumeContext["path"])
		})
	}
}

func TestPlaceholderPathIsStableAndTargetScoped(t *testing.T) {
	options := NodeOptions{StateDir: t.TempDir()}
	one, err := NewNode(options).placeholderSource(testID, testTarget)
	require.NoError(t, err)
	again, err := NewNode(options).placeholderSource(testID, testTarget)
	require.NoError(t, err)
	require.Equal(t, one, again)
	two, err := NewNode(options).placeholderSource(testID, "/another/target")
	require.NoError(t, err)
	require.NotEqual(t, one, two)
}

func TestMountedPlaceholderDoesNotRecreateMissingSource(t *testing.T) {
	node := NewNode(NodeOptions{StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil)})
	source, err := node.placeholderSource(testID, testTarget)
	require.NoError(t, err)
	node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Device: source, Path: testTarget, Type: "none"}})
	require.Error(t, node.publishPlaceholder(testID, testTarget))
	require.NoDirExists(t, source)
}

func TestPlaceholderRejectsSymlinkSource(t *testing.T) {
	node := NewNode(NodeOptions{StateDir: t.TempDir(), Mounter: mount.NewFakeMounter(nil)})
	source, err := node.placeholderSource(testID, testTarget)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(source), 0700))
	require.NoError(t, os.Symlink(t.TempDir(), source))
	require.Error(t, node.publishPlaceholder(testID, testTarget))
}
