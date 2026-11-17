//go:build linux

package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Opt in inside a privileged disposable Linux test container. The child has a
// private mount namespace, so a failed assertion cannot leave a host mount.
func TestFileUnmountSyscall(t *testing.T) {
	if os.Getenv("CSI_FILE_MOUNT_TEST_CHILD") != "1" {
		if os.Getenv("CSI_FILE_MOUNT_TEST") != "1" {
			t.Skip("requires a privileged disposable Linux container")
		}
		cmd := exec.Command("unshare", "--mount", "--propagation", "private", os.Args[0], "-test.run=^TestFileUnmountSyscall$", "-test.v")
		cmd.Env = append(os.Environ(), "CSI_FILE_MOUNT_TEST_CHILD=1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	target := filepath.Join(t.TempDir(), "mount")
	require.NoError(t, os.Mkdir(target, 0700))
	require.NoError(t, unix.Mount("tmpfs", target, "tmpfs", 0, "size=1m"))
	defer func() {
		if err := unix.Unmount(target, 0); err != nil && err != unix.EINVAL {
			t.Errorf("clean up test mount: %v", err)
		}
	}()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(target))
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	require.ErrorIs(t, unmountFileTarget(target), unix.EBUSY)
	// EBUSY must leave the mount attached, without a lazy-detach fallback.
	var st unix.Statfs_t
	require.NoError(t, unix.Statfs(target, &st))
	require.EqualValues(t, unix.TMPFS_MAGIC, st.Type)
	require.NoError(t, os.Chdir(cwd))
	require.NoError(t, unmountFileTarget(target))
	require.NoError(t, unmountFileTarget(target), "retry is idempotent")
	require.DirExists(t, target, "unmount never removes the underlying directory")
}
