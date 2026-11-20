//go:build linux

package filevolume

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestLazyFDDoesNotMountBeforeConsumerAndReportsPreparationFailure(t *testing.T) {
	root := t.TempDir()
	var offers FDRegistry
	var calls atomic.Int32
	require.NoError(t, offers.OfferLazy(root, "pv", "target", "generation", func() (int, error) { calls.Add(1); return -1, fmt.Errorf("kernel rejected mount option") }))
	t.Cleanup(func() { offers.Close("target") })
	require.Zero(t, calls.Load())
	_, err := ReceiveFD(root, "pv", "stale", time.Second)
	require.Error(t, err)
	require.Zero(t, calls.Load(), "a stale generation cannot trigger a kernel mount")
	_, err = ReceiveFD(root, "pv", "generation", time.Second)
	require.ErrorContains(t, err, "kernel rejected mount option")
	_, err = ReceiveFD(root, "pv", "generation", time.Second)
	require.Error(t, err)
	require.Equal(t, int32(1), calls.Load(), "failed preparation must not be replayed against a partially mounted target")
}

func TestFileFDHandoffUsesRealSCMRightsAndRejectsReplay(t *testing.T) {
	// The visible directory is deliberately longer than sockaddr_un.sun_path.
	root := filepath.Join(t.TempDir(), strings.Repeat("long-control-directory-", 8))
	require.NoError(t, os.MkdirAll(root, 0700))
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	fd, err := unix.Dup(int(reader.Fd()))
	require.NoError(t, err)
	var offers FDRegistry
	require.NoError(t, offers.Offer(root, "pv", "host-target", "generation", fd))
	t.Cleanup(func() { offers.Close("host-target") })
	_, err = ReceiveFD(root, "pv", "old-generation", time.Second)
	require.Error(t, err)
	received, err := ReceiveFD(root, "pv", "generation", time.Second)
	require.NoError(t, err)
	defer func() { _ = unix.Close(received) }()
	_, err = writer.Write([]byte("fuse-connection"))
	require.NoError(t, err)
	buf := make([]byte, 64)
	n, err := unix.Read(received, buf)
	require.NoError(t, err)
	require.Equal(t, "fuse-connection", string(buf[:n]))
	_, err = ReceiveFD(root, "pv", "generation", time.Second)
	require.Error(t, err, "the same connection must not be assigned to a second daemon")
	require.True(t, offers.Close("host-target"))
	require.False(t, offers.Close("host-target"))
}

func TestFileFDSocketCannotEscapeControlRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "fds")))
	_, _, err := shortSocket(root, "pv", true)
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestOrphanCollectionClosesPendingFDAndPreservesLivePod(t *testing.T) {
	root, pods := t.TempDir(), t.TempDir()
	for _, uid := range []string{"live", "dead"} {
		target := publishControl(t, root, uid, "pv")
		fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
		require.NoError(t, err)
		require.NoError(t, NativeFDs.Offer(filepath.Join(root, uid), "pv", target, "generation", fd))
		t.Cleanup(func() { NativeFDs.Close(target) })
		if uid == "live" {
			require.NoError(t, os.Mkdir(filepath.Join(pods, uid), 0700))
		}
	}
	require.NoError(t, CollectOrphans(root, pods))
	require.NoDirExists(t, filepath.Join(root, "dead"))
	require.DirExists(t, filepath.Join(root, "live"))
	NativeFDs.mu.Lock()
	require.Len(t, NativeFDs.offers, 1)
	NativeFDs.mu.Unlock()
}
