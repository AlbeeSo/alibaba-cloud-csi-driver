package filevolume

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
)

func TestPublicationPreservesRequestAndOnlyRemovesState(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv-a", "mount")
	var mount proxy.MountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"Fstype":"customfuse","MountFlags":["ro"],"futureOption":{"enabled":true}}`), &mount))
	require.NoError(t, Publish(root, "pod-a", "handle-not-pv-name", target, mount))
	path := RequestPath(filepath.Join(root, "pod-a"), "pv-a")
	var desired Desired
	require.NoError(t, ReadJSON(path, &desired))
	require.NoError(t, desired.Validate("pod-a"))
	require.Equal(t, "handle-not-pv-name", desired.Mount.VolumeID)
	require.Equal(t, []string{"ro"}, desired.Mount.MountFlags)
	require.JSONEq(t, `{"enabled":true}`, string(desired.Mount.Extensions["futureOption"]))
	require.NoError(t, Publish(root, "pod-a", "handle-not-pv-name", target, mount))
	mount.Source = "changed"
	require.ErrorIs(t, Publish(root, "pod-a", "handle-not-pv-name", target, mount), ErrConfigurationChanged)
	// A remote-data stand-in must survive unpublish. Never walk/remove the tree.
	data := filepath.Join(target, "persistent-data")
	require.NoError(t, os.WriteFile(data, []byte("keep"), 0600))
	require.ErrorContains(t, Unpublish(root, "wrong-handle", target), "does not match")
	require.ErrorIs(t, Unpublish(root, "handle-not-pv-name", target), ErrCleanupPending)
	var command UnmountRequest
	require.NoError(t, ReadJSON(UnmountPath(filepath.Join(root, "pod-a"), "pv-a"), &command))
	require.NoError(t, command.Validate(desired))
	require.NoError(t, WriteJSON(filepath.Join(root, "pod-a", "session.json"), "current-session"))
	require.NoError(t, WriteJSON(StatusPath(filepath.Join(root, "pod-a"), "pv-a"), Status{APIVersion: Version, PodUID: "pod-a", VolumeID: "handle-not-pv-name", ObservedGeneration: desired.Generation, Session: "stale-session", Phase: "Unmounted", UnmountResult: &proxy.Response{}}))
	require.ErrorIs(t, Unpublish(root, "handle-not-pv-name", target), ErrCleanupPending)
	require.NoError(t, WriteJSON(StatusPath(filepath.Join(root, "pod-a"), "pv-a"), Status{APIVersion: Version, PodUID: "pod-a", VolumeID: "handle-not-pv-name", ObservedGeneration: desired.Generation, Session: "current-session", Phase: "Unmounted", UnmountResult: &proxy.Response{}}))
	require.NoError(t, Unpublish(root, "handle-not-pv-name", target))
	require.FileExists(t, path)
	require.FileExists(t, data)
	require.NoError(t, Unpublish(root, "handle-not-pv-name", target))
	require.ErrorContains(t, Publish(root, "pod-a", "handle-not-pv-name", target, mount), "revoked")
	// This transport must not emit RunD's vol_attr.json and trigger a second consumer.
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), "vol_attr.json"))
}
