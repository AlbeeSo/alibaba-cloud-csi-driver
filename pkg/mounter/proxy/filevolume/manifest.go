package filevolume

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	DefaultStateDir  = "/volume-state"
	DefaultTargetDir = "/pod-volumes"
	OSSDriver        = "ossplugin.csi.alibabacloud.com"
	CustomDriver     = "customfuseplugin.csi.alibabacloud.com"
)

// Manifest is the complete Pod/driver inventory, independent of which mount
// requests have arrived. It contains no credentials and is published by CSI.
type Manifest struct {
	APIVersion string   `json:"apiVersion"`
	PodUID     string   `json:"podUID"`
	VolumeIDs  []string `json:"volumeIDs"`
}

func ReadExpected(root, uid string) ([]string, error) {
	var m Manifest
	if err := ReadJSON(filepath.Join(root, "manifest.json"), &m); err != nil {
		return nil, err
	}
	if m.APIVersion != Version || m.PodUID != uid || !ValidComponent(uid) || len(m.VolumeIDs) == 0 {
		return nil, fmt.Errorf("invalid Pod volume manifest")
	}
	seen := map[string]bool{}
	for _, id := range m.VolumeIDs {
		if id == "" || seen[id] {
			return nil, fmt.Errorf("empty or duplicate manifest volume")
		}
		seen[id] = true
	}
	return m.VolumeIDs, nil
}

func waitExpected(ctx context.Context, root, uid string) ([]string, error) {
	for {
		ids, err := ReadExpected(root, uid)
		if !os.IsNotExist(err) {
			return ids, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// PublishPodManifest resolves all bound claims before publishing any inventory.
// Never derive the expected set by scanning desired files: a missing request
// must keep startup blocked, including when webhook injection is disabled.
func PublishPodManifest(ctx context.Context, client kubernetes.Interface, req *csi.NodePublishVolumeRequest, driver, root string) error {
	uid := req.VolumeContext["csi.storage.k8s.io/pod.uid"]
	if client == nil || !ValidComponent(uid) {
		return fmt.Errorf("manifest requires Kubernetes client and Pod UID")
	}
	pod, err := client.CoreV1().Pods(req.VolumeContext["csi.storage.k8s.io/pod.namespace"]).Get(ctx, req.VolumeContext["csi.storage.k8s.io/pod.name"], metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(pod.UID) != uid {
		return fmt.Errorf("manifest Pod UID mismatch")
	}
	ids := map[string]bool{}
	matched := false
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		claim, err := client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, v.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if claim.Spec.VolumeName == "" {
			return fmt.Errorf("manifest requires bound PVC %s", claim.Name)
		}
		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != driver {
			continue
		}
		ref := pv.Spec.ClaimRef
		if ref == nil || ref.Namespace != pod.Namespace || ref.Name != claim.Name || ref.UID != claim.UID {
			return fmt.Errorf("manifest PV binding mismatch")
		}
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == core.PersistentVolumeBlock {
			return fmt.Errorf("file manifest requires filesystem volumes")
		}
		id := pv.Spec.CSI.VolumeHandle
		if id == "" {
			return fmt.Errorf("manifest volume handle is empty")
		}
		ids[id] = true
		if id == req.VolumeId {
			targetUID, relative, err := Target(req.TargetPath)
			if err != nil || targetUID != uid || relative != pv.Name+"/mount" {
				return fmt.Errorf("manifest volume does not match publish target")
			}
			matched = true
		}
	}
	if !matched {
		return fmt.Errorf("published volume is absent from Pod manifest")
	}
	m := Manifest{APIVersion: Version, PodUID: uid}
	for id := range ids {
		m.VolumeIDs = append(m.VolumeIDs, id)
	}
	sort.Strings(m.VolumeIDs)
	return WriteJSON(filepath.Join(root, uid, "manifest.json"), m)
}
