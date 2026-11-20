//go:build linux

package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFuseDeviceRefusesExistingNonDevice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fuse")
	require.NoError(t, os.WriteFile(path, []byte("keep"), 0600))
	never := func(string, uint32, int) error { t.Fatal("must not replace an existing path"); return nil }
	require.ErrorContains(t, ensureFuseDevice(path, never), "not the FUSE")
	link := filepath.Join(t.TempDir(), "fuse")
	require.NoError(t, os.Symlink(path, link))
	require.ErrorContains(t, ensureFuseDevice(link, never), "not the FUSE")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
}

func TestFuseDeviceMissingRequiresGuestMknodPermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fuse")
	called := false
	err := ensureFuseDevice(path, func(p string, mode uint32, dev int) error {
		called = true
		require.Equal(t, path, p)
		require.Equal(t, uint32(unix.S_IFCHR|0600), mode)
		require.Equal(t, unix.Mkdev(10, 229), uint64(dev))
		return unix.EPERM
	})
	require.True(t, called)
	require.ErrorIs(t, err, unix.EPERM)
}
