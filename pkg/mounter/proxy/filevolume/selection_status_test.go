package filevolume

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"testing"
)

func TestTerminatingPublishPreservesKubeletTeardownMetadata(t *testing.T) {
	now := metav1.Now()
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "business", Namespace: "test", UID: types.UID("pod-a"), DeletionTimestamp: &now}, Spec: core.PodSpec{NodeName: "node-a"}}
	req := &csi.NodePublishVolumeRequest{VolumeContext: map[string]string{utils.PodNameKey: "business", utils.PodNamespaceKey: "test", "csi.storage.k8s.io/pod.uid": "pod-a"}}
	// Kubelet classifies Aborted as uncertain progress and retains vol_data.json.
	// No mount may be started for this terminating identity.
	require.Equal(t, codes.Aborted, status.Code(ValidatePublishPod(pod, req, "node-a")))
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "another-pod"
	require.Equal(t, codes.FailedPrecondition, status.Code(ValidatePublishPod(pod, req, "node-a")))
}
