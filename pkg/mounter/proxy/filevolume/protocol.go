// Package filevolume implements the experimental Pod-scoped FUSE file transport.
// The RunD MountInfo envelope is shared with block/NFS; MountProxy is its FUSE extension.
package filevolume

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils/rund/directvolume"
)

const Version = "fuse.csi.alibabacloud.com/v1alpha1"

type Desired struct {
	directvolume.MountInfo
	APIVersion       string             `json:"apiVersion"`
	PodUID           string             `json:"podUID"`
	Generation       string             `json:"generation"`
	RequiredFeatures []string           `json:"requiredFeatures,omitempty"`
	Mount            proxy.MountRequest `json:"MountProxy"`
}

type Status struct {
	// Result is the mount-proxy response for this observed generation. Phase
	// additionally describes asynchronous process/mount lifecycle changes.
	Result             *proxy.Response `json:"result,omitempty"`
	UnmountResult      *proxy.Response `json:"unmountResult,omitempty"`
	APIVersion         string          `json:"apiVersion"`
	PodUID             string          `json:"podUID"`
	VolumeID           string          `json:"volumeID"`
	ObservedGeneration string          `json:"observedGeneration"`
	Session            string          `json:"session"`
	Phase              string          `json:"phase"`
	Error              string          `json:"error,omitempty"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

func ID(volumeID string) string {
	h := sha256.Sum256([]byte(volumeID))
	return hex.EncodeToString(h[:])
}

func ValidComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\")
}

func (d Desired) Validate(uid string) error {
	if len(d.RequiredFeatures) > 0 {
		return fmt.Errorf("unsupported required file-protocol features: %v", d.RequiredFeatures)
	}
	if d.APIVersion != Version || !ValidComponent(uid) || d.PodUID != uid {
		return fmt.Errorf("unsupported version or mismatched Pod UID")
	}
	parts := strings.Split(d.Mount.Target, "/")
	if len(parts) != 2 || !ValidComponent(parts[0]) || parts[1] != "mount" {
		return fmt.Errorf("target must be <CSI-volume-directory>/mount")
	}
	if err := ValidateMount(d.Mount); err != nil {
		return err
	}
	if d.Generation == "" || d.Generation != d.Digest() {
		return fmt.Errorf("invalid generation digest")
	}
	return nil
}

// ValidateMount validates transport capabilities independently of the client.
// Driver availability is checked by the mount-proxy dispatcher in the sidecar.
func ValidateMount(m proxy.MountRequest) error {
	if m.VolumeID == "" || m.Fstype == "" {
		return fmt.Errorf("file transport requires volume ID and filesystem type")
	}
	if m.FdPassing {
		return fmt.Errorf("file transport does not transfer FUSE file descriptors")
	}
	if m.Recovery {
		return fmt.Errorf("file transport does not support transparent recovery")
	}
	if m.Overlay {
		return fmt.Errorf("file transport does not manage overlay mounts")
	}
	if m.MetricsPath != "" {
		return fmt.Errorf("file transport does not expose host metrics paths")
	}
	return nil
}

func (d Desired) Digest() string {
	d.Generation = ""
	b, _ := json.Marshal(d)
	return ID(string(b))
}

// WriteJSON replaces a file atomically. Readers never observe partially written JSON.
func WriteJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pending-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func ReadJSON(path string, value any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}

func RequestPath(root, volumeID string) string {
	return filepath.Join(root, "desired", ID(volumeID)+".json")
}
func StatusPath(root, volumeID string) string {
	return filepath.Join(root, "status", ID(volumeID)+".json")
}

// CheckReady requires an explicit nonempty expected set, current generations and
// fresh heartbeats. A stale status from a previous sidecar cannot open the gate.
func CheckReady(root, uid string, expected []string, now time.Time) error {
	if len(expected) == 0 {
		return fmt.Errorf("expected volumes must not be empty")
	}
	var session string
	if err := ReadJSON(filepath.Join(root, "session.json"), &session); err != nil || session == "" {
		return fmt.Errorf("no active file runner session")
	}
	for _, volumeID := range expected {
		var d Desired
		if err := ReadJSON(RequestPath(root, volumeID), &d); err != nil {
			return fmt.Errorf("missing request for %s", volumeID)
		}
		if err := d.Validate(uid); err != nil {
			return err
		}
		if d.Mount.VolumeID != volumeID {
			return fmt.Errorf("volume ID mismatch")
		}
		var s Status
		if err := ReadJSON(StatusPath(root, volumeID), &s); err != nil {
			return fmt.Errorf("missing status for %s", volumeID)
		}
		age := now.Sub(s.UpdatedAt)
		if s.APIVersion != Version || s.PodUID != uid || s.VolumeID != volumeID || s.Session != session || s.ObservedGeneration != d.Generation || s.Phase != "Mounted" || age < 0 || age > 10*time.Second {
			return fmt.Errorf("volume %s is not ready (phase %s)", volumeID, s.Phase)
		}
	}
	return nil
}
