//go:build !windows

package oss

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	"k8s.io/mount-utils"
)

func TestOSSFS2FilePublishWithoutSocket(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	req := &csi.NodePublishVolumeRequest{VolumeId: "oss-file", TargetPath: filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv", "mount"), Readonly: true,
		VolumeContext:    map[string]string{filevolume.TransportKey: "file", "csi.storage.k8s.io/pod.uid": "pod-a", "fuseType": "ossfs2", "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com", "path": "/first"},
		Secrets:          map[string]string{"AccessKeyId": "temporary-ak", "AccessKeySecret": "temporary-sk", "SecurityToken": "temporary-token", "Expiration": "2099-01-01T00:00:00Z"},
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}}
	_, err := ns.NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	var desired filevolume.Desired
	path := filevolume.RequestPath(filepath.Join(root, "pod-a"), req.VolumeId)
	require.NoError(t, filevolume.ReadJSON(path, &desired))
	require.NoError(t, desired.Validate("pod-a"))
	require.Equal(t, "ossfs2", desired.Mount.Fstype)
	require.Contains(t, desired.Mount.Options, "ro=true")
	require.Contains(t, desired.Mount.Options, "oss_bucket=test-bucket")
	require.Equal(t, "temporary-token", desired.Mount.Secrets["SecurityToken"])
	_, err = ns.NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	_, err = ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
	require.NoError(t, err)
	require.NoFileExists(t, path)
}

func TestOSSFS2FileControllerSkipsFusePod(t *testing.T) {
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	cs := &controllerServer{metadata: ns.metadata}
	_, err := cs.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeId: "oss-file", NodeId: "node",
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}},
		VolumeContext:    map[string]string{filevolume.TransportKey: "file", "fuseType": "ossfs2", "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com"}})
	require.NoError(t, err)
}
