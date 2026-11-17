package filevolume

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils/rund/directvolume"
	"github.com/stretchr/testify/require"
)

func TestRunDEnvelopeAndFutureMountFields(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv", "mount")
	var mount proxy.MountRequest
	require.NoError(t, json.Unmarshal([]byte(`{"source":"bucket:/prefix","fstype":"future-fuse","mountFlags":["ro"],"futureOption":{"enabled":true}}`), &mount))
	require.NoError(t, Publish(root, "pod-a", "volume", target, mount))
	descriptor := filepath.Join(filepath.Dir(target), directvolume.RunD3MountInfoFileName)
	data, err := os.ReadFile(descriptor)
	require.NoError(t, err)
	var common directvolume.MountInfo
	require.NoError(t, json.Unmarshal(data, &common))
	require.Equal(t, directvolume.DeviceTypeFUSE, common.DeviceType)
	require.Equal(t, mount.Source, common.Source)
	require.Equal(t, mount.Fstype, common.FSType)
	var desired Desired
	require.NoError(t, json.Unmarshal(data, &desired))
	require.NoError(t, desired.Validate("pod-a"))
	require.Equal(t, []string{"ro"}, desired.Mount.MountFlags)
	require.JSONEq(t, `{"enabled":true}`, string(desired.Mount.Extensions["futureOption"]))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Contains(t, fields, "MountProxy")
	require.NoError(t, ReadJSON(RequestPath(filepath.Join(root, "pod-a"), "volume"), &desired))
	require.NoError(t, desired.Validate("pod-a"))
	desired.RequiredFeatures = []string{"unknown-required-behavior"}
	desired.Generation = desired.Digest()
	require.ErrorContains(t, desired.Validate("pod-a"), "unsupported required")
	require.NoError(t, Unpublish(root, "volume", target))
	require.NoFileExists(t, descriptor)
}

func TestUnpublishRetriesDescriptorCleanup(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv", "mount")
	require.NoError(t, Publish(root, "pod-a", "volume", target, proxy.MountRequest{Fstype: "customfuse"}))
	require.NoError(t, os.Remove(RequestPath(filepath.Join(root, "pod-a"), "volume")))
	require.NoError(t, Unpublish(root, "volume", target))
	require.NoFileExists(t, filepath.Join(filepath.Dir(target), directvolume.RunD3MountInfoFileName))
}
