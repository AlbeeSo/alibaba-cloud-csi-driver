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
	"google.golang.org/protobuf/proto"
	mount "k8s.io/mount-utils"
)

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
			want.VolumeContext["csi.alibabacloud.com/substrate-mode"] = "true"
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

func TestExplicitReadonlyStillChecksLiveModeAfterRestart(t *testing.T) {
	for _, firstRO := range []bool{false, true} {
		t.Run(map[bool]string{false: "rw to ro", true: "ro to rw"}[firstRO], func(t *testing.T) {
			real := writablePublishFixture()
			real.Readonly = firstRO
			annotation := annotationFixture(t, real)
			node, downstream, in := readonlyNodeFixture(t, real)
			node.opts.Lookup = func(context.Context, ActorReference) (ActorInfo, error) {
				return ActorInfo{UID: testUID, Name: "actor", Atespace: "storage-test", Annotation: annotation}, nil
			}
			_, err := node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			mode := "rw"
			if firstRO {
				mode = "ro"
			}
			fake := node.opts.Mounter.(*mount.FakeMounter)
			fake.MountPoints = []mount.MountPoint{{Device: "server:/", Path: testTarget, Type: "nfs", Opts: []string{mode}}}
			node = NewNode(node.opts)
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			downstream.published = nil
			real.Readonly = !firstRO
			annotation = annotationFixture(t, real)
			_, err = node.NodePublishVolume(t.Context(), in)
			if firstRO {
				require.NoError(t, err)
				require.False(t, downstream.published.Readonly)
			} else {
				require.Equal(t, codes.AlreadyExists, status.Code(err))
				require.Nil(t, downstream.published)
			}
			_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: testTarget})
			require.NoError(t, err)
			fake.MountPoints = nil
			_, err = node.NodePublishVolume(t.Context(), in)
			require.NoError(t, err)
			require.Equal(t, !firstRO, downstream.published.Readonly)
		})
	}
}

func TestReadonlyPublishNeedsNoStateDirectory(t *testing.T) {
	node, downstream, in := readonlyNodeFixture(t, publishFixture())
	node.opts.StateDir = ""
	for range 2 {
		node = NewNode(node.opts)
		_, err := node.NodePublishVolume(t.Context(), in)
		require.NoError(t, err)
		require.True(t, downstream.published.Readonly)
	}
}

func TestMountedReadonlyCheckUsesOnlyExplicitInnerRequirements(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*csi.NodePublishVolumeRequest)
		mountMode  string
		wantReject bool
	}{
		{"boolean requires ro", func(real *csi.NodePublishVolumeRequest) { real.Readonly = true }, "rw", true},
		{"single reader requires ro", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY
		}, "rw", true},
		{"multi reader requires ro", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeCapability.AccessMode.Mode = csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY
		}, "rw", true},
		{"matching readonly", func(real *csi.NodePublishVolumeRequest) { real.Readonly = true }, "ro", false},
		{"readonly options overridden by flags", func(real *csi.NodePublishVolumeRequest) {
			real.VolumeContext["options"] = "tls,ram,ro"
			real.VolumeCapability.GetMount().MountFlags = []string{"tls", "ram", "rw"}
		}, "rw", false},
		{"flags are delegated to NAS", func(real *csi.NodePublishVolumeRequest) { real.VolumeCapability.GetMount().MountFlags = []string{"ro"} }, "rw", false},
		{"no explicit writable requirement", func(*csi.NodePublishVolumeRequest) {}, "ro", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			real := writablePublishFixture()
			tc.mutate(real)
			node, downstream, in := readonlyNodeFixture(t, real)
			node.opts.Mounter = mount.NewFakeMounter([]mount.MountPoint{{Path: testTarget, Type: "nfs", Opts: []string{tc.mountMode}}})
			_, err := node.NodePublishVolume(t.Context(), in)
			if tc.wantReject {
				require.Equal(t, codes.AlreadyExists, status.Code(err))
				require.Nil(t, downstream.published)
			} else {
				require.NoError(t, err)
				require.Equal(t, real.Readonly, downstream.published.Readonly)
			}
		})
	}
}

func TestGoldenPlaceholderRejectsOuterReadonlyAndRemainsWritable(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(root, testUID, "volumes", "data")
	mounter := mount.NewFakeMounter(nil)
	lookups := 0
	lookup := func(context.Context, ActorReference) (ActorInfo, error) {
		lookups++
		return ActorInfo{UID: testUID, Atespace: "ate-golden", Name: "template-uid", TemplateUID: "template-uid", Golden: true}, nil
	}
	node := NewNode(NodeOptions{ActorRoot: root, StateDir: t.TempDir(), Lookup: lookup, Mounter: mounter})
	attributes := goldenMetadataFixture()
	attributes[PodUIDKey] = "worker-uid"
	in := &csi.NodePublishVolumeRequest{VolumeId: testID, TargetPath: target, Readonly: true, VolumeContext: attributes}
	_, err = node.NodePublishVolume(t.Context(), in)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Zero(t, lookups)
	require.Empty(t, mounter.MountPoints)
	in.Readonly = false
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.Len(t, mounter.MountPoints, 1)
	require.Equal(t, []string{"bind"}, mounter.MountPoints[0].Opts)
	node = NewNode(node.opts)
	_, err = node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	_, err = node.NodeUnpublishVolume(t.Context(), &csi.NodeUnpublishVolumeRequest{VolumeId: testID, TargetPath: target})
	require.NoError(t, err)
	require.Empty(t, mounter.MountPoints)
}

func TestObsoleteMetadataCannotBlockStatelessPublish(t *testing.T) {
	real := writablePublishFixture()
	node, downstream, in := readonlyNodeFixture(t, real)
	path := filepath.Join(node.opts.StateDir, "legacy.json")
	require.NoError(t, os.WriteFile(path, []byte("corrupt or obsolete metadata"), 0600))
	_, err := node.NodePublishVolume(t.Context(), in)
	require.NoError(t, err)
	require.False(t, downstream.published.Readonly)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "corrupt or obsolete metadata", string(data))
}

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
