//go:build !windows

package oss

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
)

func TestOSSFilePublishWithoutSocket(t *testing.T) {
	for _, fuseType := range []string{"ossfs", "ossfs2"} {
		t.Run(fuseType, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = fake.NewSimpleClientset(&core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "test", UID: "pod-a"}, Spec: core.PodSpec{RuntimeClassName: ptr.To("rund")}})
			req := &csi.NodePublishVolumeRequest{VolumeId: "oss-file", TargetPath: filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv", "mount"), Readonly: true,
				VolumeContext:    map[string]string{"csi.storage.k8s.io/pod.name": "pod-a", "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": "pod-a", "fuseType": fuseType, "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com", "path": "/first"},
				Secrets:          map[string]string{"AccessKeyId": "temporary-ak", "AccessKeySecret": "temporary-sk", "SecurityToken": "temporary-token", "Expiration": "2099-01-01T00:00:00Z"},
				VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}}
			_, err := ns.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			var desired filevolume.Desired
			path := filevolume.RequestPath(filepath.Join(root, "pod-a"), req.VolumeId)
			require.NoError(t, filevolume.ReadJSON(path, &desired))
			require.NoError(t, desired.Validate("pod-a"))
			require.Equal(t, fuseType, desired.Mount.Fstype)
			require.Contains(t, strings.Join(desired.Mount.Options, ","), "ro")
			if fuseType == "ossfs2" {
				require.Contains(t, desired.Mount.Options, "oss_bucket=test-bucket")
			}
			require.Equal(t, "temporary-token", desired.Mount.Secrets["SecurityToken"])
			_, err = ns.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
			_, err = ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
			require.NoError(t, err)
			require.NoFileExists(t, path)
		})
	}
}

func TestOSSFileControllerSkipsFusePod(t *testing.T) {
	for _, fuseType := range []string{"ossfs", "ossfs2"} {
		t.Run(fuseType, func(t *testing.T) {
			gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			cs := &controllerServer{metadata: ns.metadata}
			_, err := cs.ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeId: "oss-file", NodeId: "node",
				VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}},
				VolumeContext:    map[string]string{"fuseType": fuseType, "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com"}})
			require.NoError(t, err)
		})
	}
}
