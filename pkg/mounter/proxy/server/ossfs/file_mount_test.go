package ossfs

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
)

func TestMountFileReturnsSupervisedProcessExit(t *testing.T) {
	m := newTestMounter(t, false, func(...string) *exec.Cmd { return exec.Command("sleep", "30") })
	m.driver.Mounter = mounter.NewForMounter(m)
	checks := 0
	exit, err := m.driver.MountFile(context.Background(), &proxy.MountRequest{Target: t.TempDir(), Fstype: "ossfs"}, func(string) (bool, error) {
		checks++
		return checks >= 3, nil
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, checks, 3, "must wait for FUSE above an existing PVC projection")
	require.NotNil(t, exit)
	m.driver.pids.Range(func(_, value any) bool { require.NoError(t, value.(*exec.Cmd).Process.Kill()); return true })
	select {
	case err := <-exit:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("file transport did not receive OSSFS process exit")
	}
}
