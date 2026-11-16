package filevolume

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func TestValidateRundIdentity(t *testing.T) {
	p := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "test", UID: "uid"}, Spec: core.PodSpec{RuntimeClassName: ptr.To("rund")}}
	client := fake.NewSimpleClientset(p)
	req := &csi.NodePublishVolumeRequest{VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": "pod", "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": "uid"}}
	require.NoError(t, ValidateRundPod(context.Background(), client, req))
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "old-uid"
	require.Error(t, ValidateRundPod(context.Background(), client, req))
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "uid"
	p.Spec.RuntimeClassName = ptr.To("runc")
	_, err := client.CoreV1().Pods("test").Update(context.Background(), p, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Error(t, ValidateRundPod(context.Background(), client, req))
	require.Error(t, ValidateRundPod(context.Background(), nil, req))
}
