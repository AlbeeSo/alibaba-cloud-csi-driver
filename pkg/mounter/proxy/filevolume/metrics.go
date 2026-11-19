package filevolume

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
)

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
	if degraded {
		if err := pod.WriteFile(filepath.Join(rel, utils.MetricsRecoveryDegraded), []byte("1"), 0644); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(rel), nil
}
