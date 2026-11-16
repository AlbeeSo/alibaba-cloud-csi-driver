package filevolume

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const RuntimeLabel = "alibabacloud.com/container-runtime"
const ACKSandboxRuntime = "Sandboxed-Container.runv"

func Enabled() bool { return features.FunctionalMutableFeatureGate.Enabled(features.RundCSIProtocol3) }

// SupportsSidecar uses the operator-owned RuntimeClass contract. A class name
// alone is not evidence of its implementation: ACK exposes RunD as handler runv.
func SupportsSidecar(ctx context.Context, client kubernetes.Interface, pod *core.Pod) (bool, error) {
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == "" || *pod.Spec.RuntimeClassName == "runc" {
		return false, nil
	}
	if client == nil {
		return false, fmt.Errorf("cannot resolve sandbox RuntimeClass without Kubernetes client")
	}
	name := *pod.Spec.RuntimeClassName
	rc, err := client.NodeV1().RuntimeClasses().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && name == "rund" {
		return false, nil
	} // original delegated path
	if err != nil {
		return false, fmt.Errorf("resolve RuntimeClass: %w", err)
	}
	return rc.Handler == "runv" && rc.Scheduling != nil && rc.Scheduling.NodeSelector[RuntimeLabel] == ACKSandboxRuntime, nil
}

// SidecarNode is also used by ControllerPublish, which receives a node ID but
// no PodInfo. This gate-enabled installation serves sandbox FUSE consumers on
// these nodes; a runc consumer there is rejected rather than left without an attach.
func SidecarNode(ctx context.Context, client kubernetes.Interface, name string) (bool, error) {
	if client == nil || name == "" {
		return false, fmt.Errorf("cannot resolve sandbox node without Kubernetes client and node ID")
	}
	node, err := client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("resolve sandbox node: %w", err)
	}
	return node.Labels[RuntimeLabel] == ACKSandboxRuntime, nil
}

// ResolvePublish returns true for a verified ACK sandbox file consumer, false
// for the original rund delegation path. API/identity failures never fall back.
func ResolvePublish(ctx context.Context, client kubernetes.Interface, req *csi.NodePublishVolumeRequest, nodeID string) (bool, error) {
	name, ns, uid := req.VolumeContext["csi.storage.k8s.io/pod.name"], req.VolumeContext["csi.storage.k8s.io/pod.namespace"], req.VolumeContext["csi.storage.k8s.io/pod.uid"]
	if client == nil || name == "" || ns == "" {
		return false, fmt.Errorf("RunD file transport requires a Kubernetes client and PodInfo name, namespace and UID")
	}
	pod, err := client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("verify Pod identity: %w", err)
	}
	if uid != "" && string(pod.UID) != uid {
		return false, fmt.Errorf("Pod UID does not match publish request")
	}
	supported, err := SupportsSidecar(ctx, client, pod)
	if err != nil {
		return false, err
	}
	if !supported {
		if pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == "rund" {
			return false, nil
		}
		return false, fmt.Errorf("unsupported RuntimeClass for RunD file transport")
	}
	if uid == "" {
		return false, fmt.Errorf("RunD file transport requires Pod UID")
	}
	if pod.Spec.NodeName == "" || (nodeID != "" && pod.Spec.NodeName != nodeID) {
		return false, fmt.Errorf("Pod is not scheduled on this CSI node")
	}
	ack, err := SidecarNode(ctx, client, pod.Spec.NodeName)
	if err != nil {
		return false, err
	}
	if ack {
		return true, nil
	}
	return false, fmt.Errorf("unsupported node/runtime for RunD file transport")
}

func ValidateRundPod(ctx context.Context, client kubernetes.Interface, req *csi.NodePublishVolumeRequest) error {
	sidecar, err := ResolvePublish(ctx, client, req, "")
	if err != nil {
		return err
	}
	if !sidecar {
		return fmt.Errorf("Pod uses delegated RunD execution, not file transport")
	}
	return nil
}
