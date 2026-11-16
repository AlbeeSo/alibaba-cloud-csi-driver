package customfuse

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/interceptors"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
)

// Use the real customfuse process lifecycle and interceptor chain, replacing only
// the mount-table probe and executable. No FUSE device or cluster is required.
func TestMountFileSupervisesRealProcess(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "env")
	t.Setenv("FILE_TEST_CAPTURE", capture)
	m := newTestMounter(t, false, func(string) *exec.Cmd {
		return exec.Command("sh", "-c", `printf '%s|%s|%s' "$source" "$readOnly" "$password" > "$FILE_TEST_CAPTURE"; exec sleep 30`)
	})
	m.driver.Mounter = mounter.NewForMounter(m, interceptors.CustomFuseJWTAuthInterceptor, interceptors.FuseMonitorInterceptor)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	checks := 0
	exit, err := m.driver.MountFile(ctx, &proxy.MountRequest{Source: "first", Target: t.TempDir(), Options: []string{"readOnly=true"}, Secrets: map[string]string{"password": "test-value"}}, func(string) (bool, error) {
		checks++
		return checks >= 3, nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, checks, 3)
	require.NotNil(t, exit)
	require.Eventually(t, func() bool { b, _ := os.ReadFile(capture); return string(b) == "first|true|test-value" }, 3*time.Second, 10*time.Millisecond)
	m.driver.pids.Range(func(pid, _ any) bool { require.NoError(t, signalGroup(pid.(int), syscall.SIGTERM)); return true })
	select {
	case err := <-exit:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("file transport never received process exit")
	}
	require.True(t, waitGroupTimeout(&m.driver.wg, time.Second), "process must be reaped")
}

func TestMountFilePropagatesEntrypointFailure(t *testing.T) {
	m := newTestMounter(t, true, func(string) *exec.Cmd {
		return exec.Command("sh", "-c", `echo 'FUSE endpoint unavailable' >&2; exit 42`)
	})
	m.driver.Mounter = mounter.NewForMounter(m, interceptors.FuseMonitorInterceptor)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	exit, err := m.driver.MountFile(ctx, &proxy.MountRequest{Source: "first", Target: t.TempDir()}, nil)
	require.ErrorContains(t, err, "FUSE endpoint unavailable")
	require.Nil(t, exit)
	require.True(t, waitGroupTimeout(&m.driver.wg, time.Second), "failed child must be reaped")
}
