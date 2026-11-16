package filevolume

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
)

const TransportKey = "mountTransport"

var ErrConfigurationChanged = errors.New("file transport configuration changed; unpublish before changing it")

func Target(target string) (uid, relative string, err error) {
	parts := strings.Split(filepath.Clean(target), string(filepath.Separator))
	for i := 0; i+5 < len(parts); i++ {
		if parts[i] == "pods" && parts[i+2] == "volumes" && parts[i+3] == "kubernetes.io~csi" && i+6 == len(parts) && parts[i+5] == "mount" {
			if !ValidComponent(parts[i+1]) || !ValidComponent(parts[i+4]) {
				break
			}
			return parts[i+1], parts[i+4] + "/mount", nil
		}
	}
	return "", "", fmt.Errorf("file transport requires a kubelet Pod CSI filesystem target")
}

func Root(defaultRoot string) string {
	if root := os.Getenv("CSI_FUSE_FILE_ROOT"); root != "" {
		return root
	}
	return defaultRoot
}

func Publish(root, uid, volumeID, hostTarget string, request proxy.MountRequest) error {
	targetUID, relative, err := Target(hostTarget)
	if err != nil {
		return err
	}
	if uid == "" || targetUID != uid {
		return fmt.Errorf("PodInfo UID missing or does not match target")
	}
	request.Target, request.VolumeID = relative, volumeID
	d := Desired{APIVersion: Version, PodUID: uid, Mount: request}
	d.Generation = d.Digest()
	if err := d.Validate(uid); err != nil {
		return err
	}
	path := RequestPath(filepath.Join(root, uid), volumeID)
	var old Desired
	if err := ReadJSON(path, &old); err == nil {
		if old.Generation != d.Generation {
			return ErrConfigurationChanged
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(hostTarget, 0755); err != nil {
		return err
	}
	return WriteJSON(path, d)
}

func Unpublish(root, volumeID, hostTarget string) error {
	uid, relative, err := Target(hostTarget)
	if err != nil {
		return nil
	}
	path := RequestPath(filepath.Join(root, uid), volumeID)
	var d Desired
	if err := ReadJSON(path, &d); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if d.PodUID != uid || d.Mount.VolumeID != volumeID || d.Mount.Target != relative {
		return fmt.Errorf("file request does not match unpublish target")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
