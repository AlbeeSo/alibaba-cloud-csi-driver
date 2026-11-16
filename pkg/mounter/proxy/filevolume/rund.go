package filevolume

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func Enabled() bool { return features.FunctionalMutableFeatureGate.Enabled(features.RundCSIProtocol3) }

// ValidateRundPod fails closed: a failed API lookup must never select the old
// socket path when the installation is configured for the RunD 3 protocol.
func ValidateRundPod(ctx context.Context, client kubernetes.Interface, req *csi.NodePublishVolumeRequest) error {
	name, namespace, uid := req.VolumeContext["csi.storage.k8s.io/pod.name"], req.VolumeContext["csi.storage.k8s.io/pod.namespace"], req.VolumeContext["csi.storage.k8s.io/pod.uid"]
	if client == nil || name == "" || namespace == "" || uid == "" {
		return fmt.Errorf("RunD 3 file transport requires a Kubernetes client and PodInfo name, namespace and UID")
	}
	pod, err := client.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot verify RunD Pod identity: %w", err)
	}
	if string(pod.UID) != uid || pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "rund" {
		return fmt.Errorf("RunD 3 file transport requires matching Pod UID and runtimeClassName rund")
	}
	return nil
}
