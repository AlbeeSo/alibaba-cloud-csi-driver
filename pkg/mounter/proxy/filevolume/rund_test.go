package filevolume

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	node "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func runtimeFixture() (*fake.Clientset, *core.Pod, *csi.NodePublishVolumeRequest) {
	p := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", UID: "uid"}, Spec: core.PodSpec{NodeName: "node", RuntimeClassName: ptr.To("application-sandbox")}}
	client := fake.NewSimpleClientset(p,
		&core.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", Labels: map[string]string{RuntimeLabel: ACKSandboxRuntime}}},
		&node.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "application-sandbox"}, Handler: "runv", Scheduling: &node.Scheduling{NodeSelector: map[string]string{RuntimeLabel: ACKSandboxRuntime}}})
	req := &csi.NodePublishVolumeRequest{VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": "pod", "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": "uid"}}
	return client, p, req
}

func TestRuntimeAliasAndIdentity(t *testing.T) {
	client, p, req := runtimeFixture()
	require.NoError(t, ValidateRundPod(context.Background(), client, req))
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "old"
	require.Error(t, ValidateRundPod(context.Background(), client, req))
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "uid"
	_, err := ResolvePublish(context.Background(), client, req, "different-node")
	require.ErrorContains(t, err, "not scheduled")
	p.Spec.RuntimeClassName = ptr.To("runc")
	_, err = client.CoreV1().Pods("test").Update(context.Background(), p, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Error(t, ValidateRundPod(context.Background(), client, req))
	require.Error(t, ValidateRundPod(context.Background(), nil, req))
}

func TestDelegatedServerlessDoesNotSelectFileTransport(t *testing.T) {
	client, p, req := runtimeFixture()
	p.Spec.RuntimeClassName = ptr.To("rund")
	_, err := client.CoreV1().Pods("test").Update(context.Background(), p, metav1.UpdateOptions{})
	require.NoError(t, err)
	n, err := client.CoreV1().Nodes().Get(context.Background(), "node", metav1.GetOptions{})
	require.NoError(t, err)
	// Even a node with the same runtime label must retain delegation for an
	// unrecognized classic rund class; label presence alone selects no new path.
	n.Labels = map[string]string{RuntimeLabel: ACKSandboxRuntime}
	_, err = client.CoreV1().Nodes().Update(context.Background(), n, metav1.UpdateOptions{})
	require.NoError(t, err)
	selected, err := ResolvePublish(context.Background(), client, req, "node")
	require.NoError(t, err)
	require.False(t, selected)
	selected, err = SupportsSidecar(context.Background(), client, p)
	require.NoError(t, err)
	require.False(t, selected)
}

func TestRuntimeSelectorAndNodeEvidenceRequired(t *testing.T) {
	for _, mode := range []string{"wrong-handler", "missing-selector", "missing-node-label", "deleted-class", "missing-pod"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client, _, req := runtimeFixture()
			switch mode {
			case "wrong-handler", "missing-selector":
				rc, err := client.NodeV1().RuntimeClasses().Get(ctx, "application-sandbox", metav1.GetOptions{})
				require.NoError(t, err)
				if mode == "wrong-handler" {
					rc.Handler = "runc"
				} else {
					rc.Scheduling = nil
				}
				_, err = client.NodeV1().RuntimeClasses().Update(ctx, rc, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "missing-node-label":
				n, err := client.CoreV1().Nodes().Get(ctx, "node", metav1.GetOptions{})
				require.NoError(t, err)
				n.Labels = nil
				_, err = client.CoreV1().Nodes().Update(ctx, n, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "deleted-class":
				require.NoError(t, client.NodeV1().RuntimeClasses().Delete(ctx, "application-sandbox", metav1.DeleteOptions{}))
			case "missing-pod":
				require.NoError(t, client.CoreV1().Pods("test").Delete(ctx, "pod", metav1.DeleteOptions{}))
			}
			_, err := ResolvePublish(ctx, client, req, "node")
			require.Error(t, err)
		})
	}
}
