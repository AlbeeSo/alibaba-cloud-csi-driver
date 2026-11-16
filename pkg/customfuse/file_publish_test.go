//go:build !windows

package customfuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	"k8s.io/mount-utils"
)

func TestFilePublishNoSocketAndPodIsolation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	ns := &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: mount.NewFakeMounter(nil)}
	var requests []*csi.NodePublishVolumeRequest
	for _, uid := range []string{"pod-a", "pod-b"} {
		req := &csi.NodePublishVolumeRequest{VolumeId: "shared-volume", TargetPath: filepath.Join(t.TempDir(), "pods", uid, "volumes", "kubernetes.io~csi", "pv", "mount"),
			VolumeContext:    map[string]string{fileTransportKey: "file", "csi.storage.k8s.io/pod.uid": uid},
			VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}},
			Secrets:          map[string]string{"password": "test-secret"}}
		_, err := ns.NodePublishVolume(context.Background(), req)
		require.NoError(t, err)
		requests = append(requests, req)
		path := filevolume.RequestPath(filepath.Join(root, uid), req.VolumeId)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		_, err = ns.NodePublishVolume(context.Background(), req)
		require.NoError(t, err)
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, before, after)
		var d filevolume.Desired
		require.NoError(t, filevolume.ReadJSON(path, &d))
		require.NoError(t, d.Validate(uid))
		require.Equal(t, "pv/mount", d.Mount.Target)
		req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "other"
		_, err = ns.NodePublishVolume(context.Background(), req)
		require.Error(t, err)
	}
	require.FileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "shared-volume"))
	require.FileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-b"), "shared-volume"))
	_, err := ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: requests[0].VolumeId, TargetPath: requests[0].TargetPath})
	require.NoError(t, err)
	require.NoFileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "shared-volume"))
	require.FileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-b"), "shared-volume"))
}

func TestControllerFilePublishDoesNotCreateFusePod(t *testing.T) {
	// A nil manager would panic if file mode accidentally attempted Pod creation.
	_, err := (&controllerServer{}).ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "one", NodeId: "node", VolumeContext: map[string]string{fileTransportKey: "file"},
	})
	require.NoError(t, err)
}
