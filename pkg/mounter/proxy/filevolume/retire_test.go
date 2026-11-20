package filevolume

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
)

func TestConcurrentVolumeRetirementIsIdempotent(t *testing.T) {
	for range 25 {
		root := t.TempDir()
		var targets []string
		for _, key := range []string{"first", "second", "third", "fourth"} {
			targets = append(targets, publishControl(t, root, "pod-a", key))
		}
		var wg sync.WaitGroup
		errs := make(chan error, len(targets))
		for _, target := range targets {
			wg.Go(func() { errs <- Retire(root, target) })
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			require.NoError(t, err)
		}
		require.NoDirExists(t, filepath.Join(root, "pod-a"))
		for _, target := range targets {
			require.DirExists(t, target)
		}
	}
}

func publishControl(t *testing.T, root, uid, key string) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "pods", uid, "volumes", "kubernetes.io~csi", key, "mount")
	require.NoError(t, Publish(root, uid, "handle-"+key, target, proxy.MountRequest{Fstype: "customfuse"}))
	return target
}

func TestRetireWithoutACKPreservesDataAndOtherVolumes(t *testing.T) {
	root := t.TempDir()
	first := publishControl(t, root, "pod-a", "first")
	second := publishControl(t, root, "pod-a", "second")
	other := publishControl(t, root, "pod-b", "first")
	data := filepath.Join(first, "persistent-data")
	require.NoError(t, os.WriteFile(data, []byte("keep"), 0600))
	require.NoError(t, WriteJSON(filepath.Join(root, "pod-a", "manifest.json"), Manifest{APIVersion: Version, PodUID: "pod-a", VolumeKeys: []string{"first", "second", "never-submitted"}}))
	require.NoError(t, Retire(root, first))
	require.NoFileExists(t, RequestPath(filepath.Join(root, "pod-a"), "first"))
	require.FileExists(t, RequestPath(filepath.Join(root, "pod-a"), "second"))
	require.NoError(t, Retire(root, second))
	require.NoDirExists(t, filepath.Join(root, "pod-a"))
	require.FileExists(t, RequestPath(filepath.Join(root, "pod-b"), "first"))
	require.FileExists(t, data)
	require.DirExists(t, other)
	require.NoError(t, Retire(root, second))
}

func TestRetireMissingDesiredKeepsOtherVolumeResult(t *testing.T) {
	root := t.TempDir()
	first := publishControl(t, root, "pod-a", "first")
	second := publishControl(t, root, "pod-a", "second")
	require.NoError(t, os.Remove(RequestPath(filepath.Join(root, "pod-a"), "second")))
	statusPath := StatusPath(filepath.Join(root, "pod-a"), "second")
	require.NoError(t, WriteJSON(statusPath, Status{APIVersion: Version, PodUID: "pod-a", VolumeID: "handle-second", ObservedGeneration: "execution", Phase: "Failed"}))
	require.NoError(t, Retire(root, first))
	require.FileExists(t, statusPath, "one volume must not erase another volume's result after accidental desired deletion")
	require.NoError(t, Retire(root, second))
	require.NoDirExists(t, filepath.Join(root, "pod-a"))
}

func TestOrphanCollectionUsesKubeletContentAndNeverTraversesData(t *testing.T) {
	root, pods := t.TempDir(), t.TempDir()
	live := publishControl(t, root, "live", "first")
	dead := publishControl(t, root, "dead", "first")
	require.NoError(t, os.Mkdir(filepath.Join(pods, "live"), 0700))
	data := filepath.Join(dead, "persistent-data")
	require.NoError(t, os.WriteFile(data, []byte("keep"), 0600))
	require.Error(t, CollectOrphans(root, filepath.Join(pods, "missing-root")))
	require.DirExists(t, filepath.Join(root, "dead"))
	require.NoError(t, CollectOrphans(root, pods))
	require.NoDirExists(t, filepath.Join(root, "dead"))
	require.DirExists(t, filepath.Join(root, "live"))
	require.DirExists(t, live)
	require.FileExists(t, data)
}

func TestRetireRejectsSymlinkEscapeAndPreservesUnknownContent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	target := publishControl(t, root, "pod-a", "first")
	path := RequestPath(filepath.Join(root, "pod-a"), "first")
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Remove(filepath.Dir(path)))
	sentinel := filepath.Join(outside, filepath.Base(path))
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Dir(path)))
	require.Error(t, Retire(root, target))
	require.FileExists(t, sentinel)
	require.NoError(t, os.Remove(filepath.Dir(path)))
	unknown := filepath.Join(root, "pod-a", "unknown")
	require.NoError(t, os.Mkdir(unknown, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(unknown, "keep"), []byte("keep"), 0600))
	require.NoError(t, Retire(root, target))
	require.FileExists(t, filepath.Join(unknown, "keep"))
}
