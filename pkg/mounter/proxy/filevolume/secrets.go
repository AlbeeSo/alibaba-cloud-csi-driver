package filevolume

import (
	"fmt"
	"path"

	core "k8s.io/api/core/v1"
)

const SecretRoot = "/volume-secrets"

// SecretDirectory validates only the Pod's explicit projection. CSI neither
// GETs the Secret nor serializes node-publish credentials into desired.
func SecretDirectory(pod *core.Pod, name string, requiredKeys []string) (string, error) {
	if !ValidComponent(name) {
		return "", fmt.Errorf("missing or invalid secretRef")
	}
	consumer, _, err := Consumer(pod)
	if err != nil {
		return "", err
	}
	if consumer == nil {
		return "", fmt.Errorf("missing secret consumer")
	}
	directory := path.Join(SecretRoot, name)
	for _, vm := range consumer.VolumeMounts {
		if vm.MountPath != directory {
			continue
		}
		if !vm.ReadOnly || vm.SubPath != "" || vm.SubPathExpr != "" {
			return "", fmt.Errorf("secretRef must use a read-only directory projection without subPath")
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.Name != vm.Name || volume.Secret == nil || volume.Secret.SecretName != name {
				continue
			}
			if volume.Secret.Optional != nil && *volume.Secret.Optional {
				return "", fmt.Errorf("credential Secret must not be optional")
			}
			if len(volume.Secret.Items) > 0 {
				items := map[string]string{}
				for _, item := range volume.Secret.Items {
					items[item.Key] = item.Path
				}
				for _, key := range requiredKeys {
					if items[key] != key {
						return "", fmt.Errorf("secretRef must project %s as %s", key, key)
					}
				}
			}
			return directory, nil
		}
	}
	return "", fmt.Errorf("secretRef %s must be explicitly mounted at %s in the sidecar", name, directory)
}
