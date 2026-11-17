//go:build !windows

package oss

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/stretchr/testify/require"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
)

func TestOSSAgentKeepsSocketExecutionWithRunDGate(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	fixture := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	agent := NewCSIAgent(fixture.metadata, filepath.Join(t.TempDir(), "missing.sock"))
	require.True(t, agent.ns.isCSIAgent)
	_, err := agent.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{VolumeId: "volume", TargetPath: filepath.Join(t.TempDir(), "mount"), VolumeContext: map[string]string{"fuseType": "ossfs2", "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com", "path": "/data"}, Secrets: map[string]string{"AccessKeyId": "fake-ak", "AccessKeySecret": "fake-secret"}, VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}})
	require.ErrorContains(t, err, "dial unix")
	require.NoDirExists(t, filepath.Join(root, "desired"))
}
