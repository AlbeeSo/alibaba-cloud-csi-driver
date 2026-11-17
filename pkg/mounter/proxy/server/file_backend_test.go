//go:build linux

package server

import (
	"context"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
	"k8s.io/mount-utils"
)

type autoFileDriver struct {
	fakeUnmountDriver
	initialized, terminated int
	lastRequest             *proxy.MountRequest
}

func (d *autoFileDriver) Init()      { d.initialized++ }
func (d *autoFileDriver) Terminate() { d.terminated++ }
func (d *autoFileDriver) MountFile(_ context.Context, req *proxy.MountRequest, _ func(string) (bool, error)) (<-chan error, error) {
	d.lastRequest = req
	return make(chan error), nil
}

func TestFileDriversAutoDiscoveryAndMixedDispatch(t *testing.T) {
	oldNames, oldFstypes := nameToDriver, fstypeToDriver
	t.Cleanup(func() { nameToDriver, fstypeToDriver = oldNames, oldFstypes })
	nameToDriver, fstypeToDriver = map[string]Driver{}, map[string]Driver{}
	// A future linked file-capable driver must work without another allowlist.
	for _, name := range []string{"ossfs", "ossfs2", "customfuse", "future-fuse"} {
		RegisterDriver(&autoFileDriver{fakeUnmountDriver: fakeUnmountDriver{name: name}})
	}
	RegisterDriver(&fakeUnmountDriver{name: "socket-only"})
	names := InitFileDrivers()
	require.Equal(t, []string{"customfuse", "future-fuse", "ossfs", "ossfs2"}, names)
	b := FileBackend{Mounter: mount.NewFakeMounter(nil)}
	for _, name := range names {
		driver := nameToDriver[name].(*autoFileDriver)
		require.Equal(t, 1, driver.initialized)
		req := &proxy.MountRequest{Fstype: name, Target: t.TempDir()}
		exit, err := b.Mount(context.Background(), req)
		require.NoError(t, err)
		require.NotNil(t, exit)
		require.Same(t, req, driver.lastRequest)
	}
	for _, name := range []string{"unknown-fuse", "socket-only"} {
		_, err := b.Mount(context.Background(), &proxy.MountRequest{Fstype: name})
		require.ErrorContains(t, err, name)
	}
	Terminate(names)
	for _, name := range names {
		require.Equal(t, 1, nameToDriver[name].(*autoFileDriver).terminated)
	}
}

func TestSocketDriverSelectionRemainsExplicit(t *testing.T) {
	oldNames, oldFstypes := nameToDriver, fstypeToDriver
	t.Cleanup(func() { nameToDriver, fstypeToDriver = oldNames, oldFstypes })
	nameToDriver, fstypeToDriver = map[string]Driver{}, map[string]Driver{}
	selected := &autoFileDriver{fakeUnmountDriver: fakeUnmountDriver{name: "selected"}}
	disabled := &autoFileDriver{fakeUnmountDriver: fakeUnmountDriver{name: "disabled"}}
	RegisterDriver(selected)
	RegisterDriver(disabled)
	Init([]string{"selected"})
	require.Same(t, selected, fstypeToDriver["selected"])
	require.NotContains(t, fstypeToDriver, "disabled")
	require.Equal(t, 0, disabled.initialized)
	Terminate([]string{"selected"})
	require.Equal(t, 1, selected.terminated)
	require.Equal(t, 0, disabled.terminated)
}

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
