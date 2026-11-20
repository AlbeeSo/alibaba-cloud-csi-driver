package filevolume

import (
	"fmt"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
)

const MountModeKey = "mountMode"

// Attribute follows existing CSI option case-insensitivity, but rejects
// conflicting spellings instead of allowing Go map iteration to pick a mode.
func Attribute(attributes map[string]string, name string) (string, error) {
	value, found := "", false
	for key, candidate := range attributes {
		if !strings.EqualFold(key, name) {
			continue
		}
		if found && candidate != value {
			return "", fmt.Errorf("conflicting values for volume attribute %s", name)
		}
		value, found = candidate, true
	}
	return value, nil
}

// RequireSidecar is the volume-level controller hint. Consumer discovery on the
// node still works without it; it is not the webhook injection annotation.
func RequireSidecar(attributes map[string]string) (bool, error) {
	if _, err := Attribute(attributes, "runtimeclass"); err != nil {
		return false, err
	}
	value, err := Attribute(attributes, MountModeKey)
	if err != nil {
		return false, err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false, nil
	}
	if value != "sidecar" {
		return false, fmt.Errorf("%s must be sidecar or omitted", MountModeKey)
	}
	return true, nil
}

// ValidatePublishPod checks the already fetched Pod against the kubelet request;
// no container name or mutable control file can authorize another Pod's target.
func ValidatePublishPod(pod *core.Pod, req *csi.NodePublishVolumeRequest, node string) error {
	if pod == nil || string(pod.UID) == "" || string(pod.UID) != req.VolumeContext["csi.storage.k8s.io/pod.uid"] || pod.Name != req.VolumeContext[utils.PodNameKey] || pod.Namespace != req.VolumeContext[utils.PodNamespaceKey] {
		return status.Error(codes.FailedPrecondition, "sidecar requires matching PodInfo name, namespace and UID")
	}
	if node != "" && pod.Spec.NodeName != node {
		return status.Error(codes.FailedPrecondition, "sidecar Pod is not scheduled on this CSI node")
	}
	if pod.DeletionTimestamp != nil {
		// Kubelet can reconstruct a previously published volume during teardown.
		// A final error removes vol_data.json and can prevent NewUnmounter from
		// issuing NodeUnpublish. Keep progress uncertain until teardown completes.
		return status.Error(codes.Aborted, "cannot publish a new mount for a terminating Pod; volume teardown is pending")
	}
	return nil
}
