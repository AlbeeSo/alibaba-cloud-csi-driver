package filevolume

import (
	"fmt"
	"path/filepath"
)

// Missing files are not commands. Keep an explicit tombstone until the Pod
// inventory is retired, so retries/restarts cannot resurrect a revoked mount.
type UnmountRequest struct {
	APIVersion  string `json:"apiVersion"`
	PodUID      string `json:"podUID"`
	VolumeID    string `json:"volumeID"`
	Target      string `json:"target"`
	Generation  string `json:"generation"`
	OperationID string `json:"operationID"`
}

func UnmountPath(root, key string) string { return filepath.Join(root, "unmount", ID(key)+".json") }

func (u UnmountRequest) Validate(d Desired) error {
	if u.APIVersion != Version || u.PodUID != d.PodUID || u.VolumeID != d.Mount.VolumeID || u.Target != d.Mount.Target || u.Generation != d.Generation || u.OperationID != ID(d.Generation+":unmount") {
		return fmt.Errorf("unmount command does not match current request generation")
	}
	return nil
}

func newUnmount(d Desired) UnmountRequest {
	return UnmountRequest{APIVersion: Version, PodUID: d.PodUID, VolumeID: d.Mount.VolumeID, Target: d.Mount.Target, Generation: d.Generation, OperationID: ID(d.Generation + ":unmount")}
}
