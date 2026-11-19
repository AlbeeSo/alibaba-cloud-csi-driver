package filevolume

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	core "k8s.io/api/core/v1"
)

const ClientTargetDir = "/run/fuse.ossfs"

func ClientTarget(key string) string { return filepath.Join(ClientTargetDir, key, "globalmount") }

// ClientMetricsEnabled follows the clients' existing use_metrics option. The
// lifecycle monitor is independent and remains enabled when IO metrics are off.
func ClientMetricsEnabled(options []string) bool {
	enabled := false
	for _, option := range options {
		key, value, hasValue := strings.Cut(option, "=")
		if key == "use_metrics" {
			enabled = !hasValue || !strings.EqualFold(value, "false")
		}
	}
	return enabled
}

func ValidateClientMetricsProjection(pod *core.Pod, req *csi.NodePublishVolumeRequest, fd bool) error {
	_, relative, err := Target(req.TargetPath)
	if err != nil {
		return err
	}
	key := filepath.Dir(relative)
	consumer, _, err := Consumer(pod)
	if err != nil {
		return err
	}
	if consumer == nil {
		return fmt.Errorf("missing OSS client consumer")
	}
	volumes := map[string]core.Volume{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = v
	}
	claim := ""
	for _, vm := range consumer.VolumeMounts {
		if vm.MountPath == filepath.Join(DefaultTargetDir, relative) {
			if pvc := volumes[vm.Name].PersistentVolumeClaim; pvc != nil {
				claim = pvc.ClaimName
			}
		}
	}
	propagation := core.MountPropagationBidirectional
	if fd {
		propagation = core.MountPropagationHostToContainer
	}
	for _, vm := range consumer.VolumeMounts {
		if vm.MountPath != ClientTarget(key) {
			continue
		}
		pvc := volumes[vm.Name].PersistentVolumeClaim
		if pvc != nil && claim != "" && pvc.ClaimName == claim && !vm.ReadOnly && vm.SubPath == "" && vm.SubPathExpr == "" && vm.MountPropagation != nil && *vm.MountPropagation == propagation {
			return nil
		}
	}
	return fmt.Errorf("OSS IO metrics require the same PVC projected at %s with matching propagation; add that volumeMount or explicitly disable client IO metrics with use_metrics=false", ClientTarget(key))
}

// WriteMetricsInventory uses the existing collector's records inside this Pod's
// control directory. No extra arbitrary hostPath is given to the sidecar.
func WriteMetricsInventory(root string, req *csi.NodePublishVolumeRequest, client, backend, filesystem string, degraded bool) (string, error) {
	uid, relative, err := Target(req.TargetPath)
	if err != nil {
		return "", err
	}
	key := filepath.Dir(relative)
	if err := os.MkdirAll(filepath.Join(root, uid), 0700); err != nil {
		return "", err
	}
	pod, err := os.OpenRoot(filepath.Join(root, uid))
	if err != nil {
		return "", err
	}
	defer func() { _ = pod.Close() }()
	rel := filepath.Join("metrics", key)
	if err := pod.MkdirAll(rel, 0700); err != nil {
		return "", err
	}
	// Record only public labels, never the opaque customfuse source or secrets.
	fields := []string{client, backend, filesystem, req.VolumeId, req.TargetPath}
	for _, value := range fields {
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			return "", fmt.Errorf("invalid metrics label")
		}
	}
	info := strings.Join(fields, " ")
	if err := pod.WriteFile(filepath.Join(rel, utils.MountPointInfoFile), []byte(info), 0644); err != nil {
		return "", err
	}
	info = req.VolumeContext[utils.PodNamespaceKey] + " " + req.VolumeContext[utils.PodNameKey] + " " + uid + " 0"
	if err := pod.WriteFile(utils.PodInfoFile, []byte(info), 0644); err != nil {
		return "", err
	}
	if err := pod.WriteFile(filepath.Join(rel, utils.PodInfoFile), []byte(info), 0644); err != nil {
		return "", err
	}
	if degraded {
		if err := pod.WriteFile(filepath.Join(rel, utils.MetricsRecoveryDegraded), []byte("1"), 0644); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(rel), nil
}
