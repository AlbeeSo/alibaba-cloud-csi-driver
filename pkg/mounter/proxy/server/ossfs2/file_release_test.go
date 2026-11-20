package ossfs2

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFileFDReleaseStopsOnlyOwnedProcessAndClosesDescriptor(t *testing.T) {
	driver := &Driver{pids: new(sync.Map)}
	proc, command := startTestProcess(t, "exec sleep 100")
	t.Cleanup(func() { _ = command.Process.Kill() })
	other, otherCommand := startTestProcess(t, "exec sleep 100")
	t.Cleanup(func() { _ = otherCommand.Process.Kill(); <-other.exited })
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	fd, err := unix.Dup(int(reader.Fd()))
	require.NoError(t, err)
	const target = "/pod-volumes/pv/mount"
	stop := &fileFDStop{done: make(chan struct{})}
	driver.fileStops.Store(target, stop)
	driver.targetProcesses.Store(target, command)
	driver.targetProcesses.Store("/other", otherCommand)
	driver.pids.Store(command.Process.Pid, command)
	driver.wg.Add(1)
	exited := make(chan error, 1)
	go (&extendedMounter{driver: driver}).superviseProcess(proc, &mounter.MountOperation{Target: target, FuseFd: fd}, 0, true, exited)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, driver.ReleaseFileFD(ctx, target))
	require.NoError(t, driver.ReleaseFileFD(ctx, target), "release is idempotent")
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.ErrorIs(t, err, unix.EBADF, "release must wait for the supervisor to close its fd")
	require.False(t, driver.shouldStop("/other"), "per-volume release must not disable another volume's recovery")
	select {
	case <-other.exited:
		t.Fatal("unrelated client was terminated")
	default:
	}
}
