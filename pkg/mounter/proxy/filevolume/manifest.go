package filevolume

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	core "k8s.io/api/core/v1"
)

const (
	DefaultStateDir  = "/volume-state"
	DefaultTargetDir = "/pod-volumes"
	HostStateDir     = "/var/run/ossfs/file-volumes"
)

// Keys are the CSI target directory names declared by the sidecar, not volume
// handles. Inventory comes from the cached Pod; the producer separately verifies
// the PVC binding for each submitted target.
type Manifest struct {
	APIVersion   string            `json:"apiVersion"`
	PodUID       string            `json:"podUID"`
	VolumeKeys   []string          `json:"volumeKeys"`
	VolumeClaims map[string]string `json:"volumeClaims,omitempty"`
}

func ManifestForPod(pod *core.Pod, req *csi.NodePublishVolumeRequest) (Manifest, error) {
	var result Manifest
	err := podManifest(pod, req, func(m Manifest) error { result = m; return nil })
	return result, err
}

func ReadExpected(root, uid string) ([]string, error) {
	var m Manifest
	if err := ReadJSON(filepath.Join(root, "manifest.json"), &m); err != nil {
		return nil, err
	}
	if m.APIVersion != Version || m.PodUID != uid || !ValidComponent(uid) || len(m.VolumeKeys) == 0 {
		return nil, fmt.Errorf("invalid Pod volume manifest")
	}
	seen := map[string]bool{}
	for _, key := range m.VolumeKeys {
		if !ValidComponent(key) || seen[key] {
			return nil, fmt.Errorf("invalid or duplicate manifest volume key")
		}
		seen[key] = true
	}
	return m.VolumeKeys, nil
}

func waitExpected(ctx context.Context, root, uid string) ([]string, error) {
	for {
		keys, err := ReadExpected(root, uid)
		if !os.IsNotExist(err) {
			return keys, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func fileContainer(c core.Container) bool {
	return len(c.Command) == 1 && filepath.Base(c.Command[0]) == "csi-mount-proxy-server" && len(c.Args) == 1 && c.Args[0] == "--file-mode"
}

func fileProbe(p *core.Probe) bool {
	return p != nil && p.Exec != nil && len(p.Exec.Command) == 2 && filepath.Base(p.Exec.Command[0]) == "csi-mount-proxy-server" && p.Exec.Command[1] == "--file-check-ready"
}

// Manual consumers use fixed arguments/probes. The Pod declares each volume
// once; neither the sidecar command nor the startup probe has a volume list.
func PublishPodManifest(pod *core.Pod, req *csi.NodePublishVolumeRequest, root string) error {
	return podManifest(pod, req, func(m Manifest) error {
		return WriteJSON(filepath.Join(root, m.PodUID, "manifest.json"), m)
	})
}

// ValidatePodManifest checks the consumer before auth validation without leaving
// an inventory for a request that was never submitted.
func ValidatePodManifest(pod *core.Pod, req *csi.NodePublishVolumeRequest) error {
	return podManifest(pod, req, func(Manifest) error { return nil })
}

func podManifest(pod *core.Pod, req *csi.NodePublishVolumeRequest, accept func(Manifest) error) error {
	uid, relative, err := Target(req.TargetPath)
	if err != nil {
		return err
	}
	if string(pod.UID) != uid || req.VolumeContext["csi.storage.k8s.io/pod.uid"] != uid {
		return fmt.Errorf("manifest Pod UID mismatch")
	}
	var consumer *core.Container
	consumerIndex := -1
	for i := range pod.Spec.InitContainers {
		c := &pod.Spec.InitContainers[i]
		if fileContainer(*c) {
			if consumer != nil {
				return fmt.Errorf("multiple file-mode sidecars")
			}
			consumer = c
			consumerIndex = i
		}
	}
	if consumer == nil {
		return fmt.Errorf("direct mount requires a native mountproxy sidecar with args [--file-mode]")
	}
	if consumer.RestartPolicy == nil || *consumer.RestartPolicy != core.ContainerRestartPolicyAlways || !fileProbe(consumer.StartupProbe) || !fileProbe(consumer.ReadinessProbe) {
		return fmt.Errorf("mountproxy must be a restartable init sidecar with startup/readiness exec probes [csi-mount-proxy-server, --file-check-ready]")
	}
	if consumer.SecurityContext == nil || consumer.SecurityContext.Privileged == nil || !*consumer.SecurityContext.Privileged {
		return fmt.Errorf("mountproxy sidecar must be privileged")
	}
	volumes := map[string]core.Volume{}
	for _, v := range pod.Spec.Volumes {
		volumes[v.Name] = v
	}
	uidEnv := false
	for _, e := range consumer.Env {
		if e.Name == "POD_UID" && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil && e.ValueFrom.FieldRef.FieldPath == "metadata.uid" {
			uidEnv = true
		}
	}
	state := false
	keys := map[string]bool{}
	claims := map[string]bool{}
	for _, vm := range consumer.VolumeMounts {
		v := volumes[vm.Name]
		if vm.MountPath == DefaultStateDir {
			if v.HostPath == nil || v.HostPath.Path != HostStateDir || vm.SubPathExpr != "$(POD_UID)" || vm.SubPath != "" || vm.ReadOnly || !uidEnv {
				return fmt.Errorf("mountproxy state must mount %s at %s with Pod UID subPathExpr", HostStateDir, DefaultStateDir)
			}
			state = true
			continue
		}
		if !strings.HasPrefix(vm.MountPath, DefaultTargetDir+"/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(vm.MountPath, DefaultTargetDir+"/"), "/")
		if len(parts) != 2 || !ValidComponent(parts[0]) || parts[1] != "mount" || v.PersistentVolumeClaim == nil || vm.ReadOnly || vm.SubPath != "" || vm.SubPathExpr != "" || vm.MountPropagation == nil || *vm.MountPropagation != core.MountPropagationBidirectional {
			return fmt.Errorf("mountproxy data mounts require PVCs at %s/<CSI-volume-directory>/mount with Bidirectional propagation and no subPath", DefaultTargetDir)
		}
		if keys[parts[0]] {
			return fmt.Errorf("duplicate mountproxy volume target")
		}
		if claims[v.PersistentVolumeClaim.ClaimName] {
			return fmt.Errorf("mountproxy must not map the same PVC to multiple volume targets")
		}
		claims[v.PersistentVolumeClaim.ClaimName] = true
		keys[parts[0]] = true
	}
	if !state {
		return fmt.Errorf("mountproxy sidecar has no Pod-scoped state volume")
	}
	if !keys[strings.TrimSuffix(relative, "/mount")] {
		return fmt.Errorf("publish target is absent from mountproxy sidecar volume mounts")
	}
	// A native sidecar gates only subsequent init/app containers. An earlier
	// init consumer would see the underlying empty projection before FUSE and
	// could write local fallback data or block initialization forever.
	for _, init := range pod.Spec.InitContainers[:consumerIndex] {
		for _, vm := range init.VolumeMounts {
			v := volumes[vm.Name]
			if v.PersistentVolumeClaim != nil && claims[v.PersistentVolumeClaim.ClaimName] {
				return fmt.Errorf("mountproxy must precede init container %s which consumes its PVC", init.Name)
			}
		}
	}
	m := Manifest{APIVersion: Version, PodUID: uid, VolumeClaims: map[string]string{}}
	for key := range keys {
		m.VolumeKeys = append(m.VolumeKeys, key)
	}
	for _, vm := range consumer.VolumeMounts {
		key := strings.TrimSuffix(strings.TrimPrefix(vm.MountPath, DefaultTargetDir+"/"), "/mount")
		if keys[key] {
			m.VolumeClaims[key] = volumes[vm.Name].PersistentVolumeClaim.ClaimName
		}
	}
	sort.Strings(m.VolumeKeys)
	return accept(m)
}
