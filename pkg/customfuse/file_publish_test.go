//go:build !windows

package customfuse

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	node "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
)

func TestFilePublishNoSocketAndPodIsolation(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	ns := &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: mount.NewFakeMounter(nil), clientset: fileTestClient()}
	var requests []*csi.NodePublishVolumeRequest
	for _, uid := range []string{"pod-a", "pod-b"} {
		req := &csi.NodePublishVolumeRequest{VolumeId: "shared-volume", TargetPath: filepath.Join(t.TempDir(), "pods", uid, "volumes", "kubernetes.io~csi", "pv", "mount"),
			VolumeContext:    map[string]string{"csi.storage.k8s.io/pod.name": uid, "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": uid},
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
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	// A nil manager would panic if file mode accidentally attempted Pod creation.
	_, err := (&controllerServer{clientset: fileTestClient()}).ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{
		VolumeId: "one", NodeId: "node", VolumeContext: map[string]string{},
	})
	require.NoError(t, err)
}

func fileTestClient() *fake.Clientset {
	return fake.NewSimpleClientset(
		&core.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{filevolume.RuntimeLabel: filevolume.ACKSandboxRuntime}}},
		&node.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "business-guest"}, Handler: "runv", Scheduling: &node.Scheduling{NodeSelector: map[string]string{filevolume.RuntimeLabel: filevolume.ACKSandboxRuntime}}},
		&core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "test", UID: "pod-a"}, Spec: core.PodSpec{NodeName: "node", RuntimeClassName: ptr.To("business-guest")}},
		&core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-b", Namespace: "test", UID: "pod-b"}, Spec: core.PodSpec{NodeName: "node", RuntimeClassName: ptr.To("business-guest")}},
	)
}
