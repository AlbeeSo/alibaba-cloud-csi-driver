package filevolume

import (
	"fmt"

	core "k8s.io/api/core/v1"
)

const FuseConnectionsDir = "/sys/fs/fuse/connections"

// ValidateRecoveryProjection reuses the existing standalone OSS2 recovery
// contract: flushing the alinux connection requires its writable fusectl tree.
// It never silently elevates the sidecar or claims fd handoff alone suffices.
func ValidateRecoveryProjection(pod *core.Pod) error {
	consumer, _, err := Consumer(pod)
	if err != nil {
		return err
	}
	if consumer != nil {
		for _, vm := range consumer.VolumeMounts {
			if vm.MountPath != FuseConnectionsDir || vm.ReadOnly || vm.SubPath != "" || vm.SubPathExpr != "" {
				continue
			}
			for _, volume := range pod.Spec.Volumes {
				if volume.Name == vm.Name && volume.HostPath != nil && volume.HostPath.Path == FuseConnectionsDir {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("OSS2 recovery requires an explicit writable HostPath projection of %s into the sidecar; fd handoff alone does not provide recovery control", FuseConnectionsDir)
}
