package customfuse

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/stretchr/testify/require"
)

func TestGeneratedCredentialsDoNotReachDiagnostics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "client.log")
	output, err := os.Create(logPath)
	require.NoError(t, err)
	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = output, output
	t.Cleanup(func() { os.Stdout, os.Stderr = originalOut, originalErr; _ = output.Close() })
	id := "STS." + strings.Repeat("x", 24)
	token := "eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 16) + "." + strings.Repeat("c", 16)
	m := newTestMounter(t, true, func(string) *exec.Cmd {
		return exec.Command("sh", "-c", fmt.Sprintf(`printf 'SecurityToken=%s'; printf 'NoSuchBucket with ak %s OIDCToken=%s' >&2; exit 7`, "unit-private-token", id, token))
	})
	err = m.ExtendedMount(context.Background(), &mounter.MountOperation{Target: t.TempDir()})
	require.ErrorContains(t, err, "NoSuchBucket")
	require.ErrorContains(t, err, "exit status 7")
	require.NoError(t, output.Sync())
	body, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	for _, secret := range []string{id, token, "unit-private-token"} {
		require.NotContains(t, string(body)+err.Error(), secret)
	}
}
