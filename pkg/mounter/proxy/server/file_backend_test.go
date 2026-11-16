//go:build linux

package server

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/mount-utils"
)

func TestFileBackendPreservesPVCProjection(t *testing.T) {
	for _, fs := range []string{"virtio_fs", "virtiofs", "fuse.virtiofs", "ext4"} {
		b := FileBackend{Mounter: mount.NewFakeMounter([]mount.MountPoint{{Path: "/volume", Type: fs}})}
		mounted, err := b.Mounted("/volume")
		require.NoError(t, err)
		require.False(t, mounted)
		require.NoError(t, b.Unmount("/volume"))
		points, err := b.Mounter.List()
		require.NoError(t, err)
		require.Len(t, points, 1)
	}
	b := FileBackend{Mounter: mount.NewFakeMounter([]mount.MountPoint{{Path: "/volume", Type: "virtio_fs"}, {Path: "/volume", Type: "fuse.ossfs2"}})}
	mounted, err := b.Mounted("/volume")
	require.NoError(t, err)
	require.True(t, mounted)
}
