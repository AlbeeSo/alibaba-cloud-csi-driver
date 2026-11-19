package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/stretchr/testify/require"
)

func TestFileClientMetricsAliasesPreserveProtocolTarget(t *testing.T) {
	attach, metrics := t.TempDir(), t.TempDir()
	var aliases []string
	for _, name := range []string{"first", "second"} {
		state := filepath.Join(t.TempDir(), "metrics", name)
		require.NoError(t, os.MkdirAll(state, 0700))
		op := &mounter.MountOperation{Target: "/pod-volumes/" + name + "/mount", MetricsPath: state}
		require.NoError(t, prepareFileClientMetrics(op, attach, metrics))
		require.Equal(t, "/pod-volumes/"+name+"/mount", op.Target)
		target, err := os.Readlink(op.ClientTarget)
		require.NoError(t, err)
		require.Equal(t, op.Target, target)
		key := filepath.Base(filepath.Dir(op.ClientTarget))
		target, err = os.Readlink(filepath.Join(metrics, key))
		require.NoError(t, err)
		require.Equal(t, state, target)
		require.NoError(t, prepareFileClientMetrics(op, attach, metrics), "idempotent without replacing anything")
		aliases = append(aliases, op.ClientTarget)
		require.NoError(t, os.Remove(op.ClientTarget))
		require.NoError(t, os.Symlink("/different", op.ClientTarget))
		require.ErrorContains(t, prepareFileClientMetrics(op, attach, metrics), "conflicts")
	}
	require.NotEqual(t, aliases[0], aliases[1], "client counters must not collide between volumes")
}
