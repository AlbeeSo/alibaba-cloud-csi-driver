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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	mount "k8s.io/mount-utils"
)

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
