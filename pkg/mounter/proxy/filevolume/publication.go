package filevolume

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils/rund/directvolume"
)

var ErrConfigurationChanged = errors.New("file transport configuration changed; unpublish before changing it")
var ErrCleanupPending = errors.New("guest cleanup not confirmed")
var ErrIdentityMismatch = errors.New("file request does not match unpublish target")

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
	d := Desired{MountInfo: directvolume.MountInfo{Source: request.Source, DeviceType: directvolume.DeviceTypeFUSE, FSType: request.Fstype, MountOpts: request.Options, Extra: map[string]string{}}, APIVersion: Version, PodUID: uid, Mount: request}
	d.Generation = d.Digest()
	if err := d.Validate(uid); err != nil {
		return err
	}
	path := RequestPath(filepath.Join(root, uid), strings.TrimSuffix(relative, "/mount"))
	var revoked UnmountRequest
	if err := ReadJSON(UnmountPath(filepath.Join(root, uid), strings.TrimSuffix(relative, "/mount")), &revoked); err == nil {
		return fmt.Errorf("pod volume was revoked; recreate Pod before publishing again")
	} else if !os.IsNotExist(err) {
		return err
	}
	var old Desired
	if err := ReadJSON(path, &old); err == nil {
		if err := old.Validate(uid); err != nil {
			return fmt.Errorf("existing file request is invalid: %w", err)
		}
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
	path := RequestPath(filepath.Join(root, uid), strings.TrimSuffix(relative, "/mount"))
	var d Desired
	if err := ReadJSON(path, &d); err != nil {
		if os.IsNotExist(err) {
			key := strings.TrimSuffix(relative, "/mount")
			var s Status
			if err := ReadJSON(StatusPath(filepath.Join(root, uid), key), &s); err == nil {
				if s.APIVersion != Version || s.PodUID != uid || s.VolumeID != volumeID || s.ObservedGeneration == "" {
					return ErrCleanupPending
				}
				// Recover the identity from the guest status after accidental desired
				// deletion. The worker still validates against its in-memory request.
				d = Desired{APIVersion: Version, PodUID: uid, Generation: s.ObservedGeneration, Mount: proxy.MountRequest{VolumeID: volumeID, Target: relative}}
			} else {
				keys, inventoryErr := ReadExpected(filepath.Join(root, uid), uid)
				if inventoryErr != nil && !os.IsNotExist(inventoryErr) {
					return inventoryErr
				}
				for _, k := range keys {
					if k == key {
						return ErrCleanupPending
					}
				}
				return nil
			}
		} else {
			return err
		}
	}
	if d.PodUID != uid || d.Mount.VolumeID != volumeID || d.Mount.Target != relative {
		return ErrIdentityMismatch
	}
	key := strings.TrimSuffix(relative, "/mount")
	if err := WriteJSON(UnmountPath(filepath.Join(root, uid), key), newUnmount(d)); err != nil {
		return err
	}
	var s Status
	if err := ReadJSON(StatusPath(filepath.Join(root, uid), key), &s); err != nil {
		return ErrCleanupPending
	}
	var session string
	if err := ReadJSON(filepath.Join(root, uid, "session.json"), &session); err != nil || session == "" {
		return ErrCleanupPending
	}
	if s.APIVersion != Version || s.PodUID != uid || s.VolumeID != volumeID || s.ObservedGeneration != d.Generation || s.Session != session || s.Phase != "Unmounted" || s.UnmountResult == nil || s.UnmountResult.Error != "" {
		return ErrCleanupPending
	}
	// Keep desired and the tombstone until inventory retirement. They contain
	// identity needed for a retry even if the sidecar has already exited.
	return nil
}
