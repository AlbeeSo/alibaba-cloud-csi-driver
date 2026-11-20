package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/stretchr/testify/require"
	"k8s.io/mount-utils"
)

func TestFileClientMetricsAliasesPreserveProtocolTarget(t *testing.T) {
	attach, metrics := t.TempDir(), t.TempDir()
	var aliases []string
	for _, name := range []string{"first", "second"} {
		state := filepath.Join(t.TempDir(), "metrics", name)
		require.NoError(t, os.MkdirAll(state, 0700))
		op := &mounter.MountOperation{Target: "/pod-volumes/" + name + "/mount", MetricsPath: state}
		clientPath := filepath.Join(attach, name, "globalmount")
		require.NoError(t, os.MkdirAll(clientPath, 0700))
		mounts := mount.NewFakeMounter([]mount.MountPoint{{Path: clientPath, Type: "ext4"}})
		require.NoError(t, prepareFileClientMetrics(op, attach, metrics, mounts))
		require.Equal(t, "/pod-volumes/"+name+"/mount", op.Target)
		_, err := os.Readlink(op.ClientTarget)
		require.Error(t, err, "client data path is a real PVC projection, never a symlink")
		key := filepath.Base(filepath.Dir(op.ClientTarget))
		target, err := os.Readlink(filepath.Join(metrics, key))
		require.NoError(t, err)
		require.Equal(t, state, target)
		require.NoError(t, prepareFileClientMetrics(op, attach, metrics, mounts), "idempotent without replacing anything")
		aliases = append(aliases, op.ClientTarget)
		require.NoError(t, os.Remove(filepath.Join(metrics, key)))
		require.NoError(t, os.Symlink("/different", filepath.Join(metrics, key)))
		require.ErrorContains(t, prepareFileClientMetrics(op, attach, metrics, mounts), "conflicts")
		require.ErrorContains(t, prepareFileClientMetrics(op, attach, metrics, mount.NewFakeMounter(nil)), "real PVC projection")
	}
	require.NotEqual(t, aliases[0], aliases[1], "client counters must not collide between volumes")
}
