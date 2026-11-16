//go:build !windows

package customfuse

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/stretchr/testify/require"
	gatetesting "k8s.io/component-base/featuregate/testing"
)

func TestAgentNeverPublishesHostFiles(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	agent := NewCSIAgent("")
	require.True(t, agent.ns.isCSIAgent)
	_, err := agent.NodePublishVolume(context.Background(), &csi.NodePublishVolumeRequest{VolumeId: "volume", TargetPath: filepath.Join(t.TempDir(), "mount"), VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}})
	require.ErrorContains(t, err, "mount proxy socket path is empty")
	require.NoDirExists(t, filepath.Join(root, "desired"))
}
