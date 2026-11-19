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
	"k8s.io/klog/v2"
)

const (
	SidecarName       = "alibabacloud-csi-mountproxy"
	CustomSidecarName = "alibabacloud-csi-customfuse-mountproxy"
	DefaultStateDir   = "/volume-state"
	DefaultTargetDir  = "/pod-volumes"
	HostStateDir      = "/var/run/ossfs/file-volumes"
)

// Keys are the CSI target directory names declared by the sidecar, not volume
// handles. Inventory comes from the cached Pod; the producer separately verifies
// the PVC binding for each submitted target.
type Manifest struct {
	APIVersion   string            `json:"apiVersion"`
	PodUID       string            `json:"podUID"`
	VolumeKeys   []string          `json:"volumeKeys"`
	VolumeClaims map[string]string `json:"volumeClaims,omitempty"`
	// BindingLookupRequired is local validation state, never a wire capability.
	// The normal first-sidecar path must not need PVC metadata or API access.
	BindingLookupRequired bool   `json:"-"`
	ConsumerName          string `json:"-"`
	FdPassing             bool   `json:"fdPassing,omitempty"`
}

func ManifestForPod(pod *core.Pod, req *csi.NodePublishVolumeRequest) (Manifest, error) {
	return ManifestForPodWithFD(pod, req, false)
}

func ManifestForPodWithFD(pod *core.Pod, req *csi.NodePublishVolumeRequest, fd bool) (Manifest, error) {
	var result Manifest
	err := podManifestMode(pod, req, fd, func(m Manifest) error { result = m; return nil })
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
	return c.Name == SidecarName || c.Name == CustomSidecarName
}

// Consumer identifies a single native consumer by exact reserved name. Names
// select a contract, not a trusted image or authorization to deliver secrets.
func Consumer(pod *core.Pod) (*core.Container, int, error) {
	if pod == nil {
		return nil, -1, fmt.Errorf("missing Pod")
	}
	for _, c := range pod.Spec.Containers {
		if fileContainer(c) {
			return nil, -1, fmt.Errorf("mountproxy %s must be a restartable init container", c.Name)
		}
	}
	var result *core.Container
	index := -1
	for i := range pod.Spec.InitContainers {
		if fileContainer(pod.Spec.InitContainers[i]) {
			if result != nil {
				return nil, -1, fmt.Errorf("multiple file-mode sidecars; each Pod requires one mount consumer")
			}
			result, index = &pod.Spec.InitContainers[i], i
		}
	}
	return result, index, nil
}

func fileProbe(p *core.Probe) bool {
	return p != nil && p.Exec != nil && len(p.Exec.Command) == 2 && filepath.Base(p.Exec.Command[0]) == "csi-mount-proxy-server" && p.Exec.Command[1] == "--file-check-ready"
}

// Manual consumers use a reserved name and fixed probes. The Pod declares each
// volume once; neither the sidecar command nor the startup probe needs a list.
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
	return podManifestMode(pod, req, false, accept)
}

func podManifestMode(pod *core.Pod, req *csi.NodePublishVolumeRequest, fd bool, accept func(Manifest) error) error {
	uid, relative, err := Target(req.TargetPath)
	if err != nil {
		return err
	}
	if string(pod.UID) != uid || req.VolumeContext["csi.storage.k8s.io/pod.uid"] != uid {
		return fmt.Errorf("manifest Pod UID mismatch")
	}
	consumer, consumerIndex, err := Consumer(pod)
	if err != nil {
		return err
	}
	if consumer == nil {
		return fmt.Errorf("direct mount requires a native mountproxy sidecar named %s", SidecarName)
	}
	if consumer.RestartPolicy == nil || *consumer.RestartPolicy != core.ContainerRestartPolicyAlways || !fileProbe(consumer.StartupProbe) || !fileProbe(consumer.ReadinessProbe) {
		return fmt.Errorf("mountproxy must be a restartable init sidecar with startup/readiness exec probes [csi-mount-proxy-server, --file-check-ready]")
	}
	privileged := consumer.SecurityContext != nil && consumer.SecurityContext.Privileged != nil && *consumer.SecurityContext.Privileged
	if !fd && !privileged {
		return fmt.Errorf("mountproxy sidecar must be privileged")
	}
	if fd && (consumer.Name != SidecarName || privileged) {
		return fmt.Errorf("fd-passing requires the non-privileged OSS sidecar")
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
			if vm.MountPropagation != nil && *vm.MountPropagation != core.MountPropagationNone {
				return fmt.Errorf("mountproxy control directory must not propagate data mounts to the host")
			}
			state = true
			continue
		}
		if !strings.HasPrefix(vm.MountPath, DefaultTargetDir+"/") {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(vm.MountPath, DefaultTargetDir+"/"), "/")
		propagation := core.MountPropagationBidirectional
		if fd {
			propagation = core.MountPropagationHostToContainer
		}
		if len(parts) != 2 || !ValidComponent(parts[0]) || parts[1] != "mount" || v.PersistentVolumeClaim == nil || vm.ReadOnly || vm.SubPath != "" || vm.SubPathExpr != "" || vm.MountPropagation == nil || *vm.MountPropagation != propagation {
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
	if err := checkPrecedingInitPVCs(pod, consumerIndex, volumes, claims); err != nil {
		return err
	}
	m := Manifest{APIVersion: Version, PodUID: uid, VolumeClaims: map[string]string{}, ConsumerName: consumer.Name, FdPassing: fd}
	for _, init := range pod.Spec.InitContainers[:consumerIndex] {
		for _, vm := range init.VolumeMounts {
			if volumes[vm.Name].PersistentVolumeClaim != nil {
				m.BindingLookupRequired = true
			}
		}
	}
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

func checkPrecedingInitPVCs(pod *core.Pod, index int, volumes map[string]core.Volume, managed map[string]bool) error {
	if index == 0 {
		return nil
	}
	consumer := pod.Spec.InitContainers[index].Name
	klog.InfoS("Mountproxy sidecar is not the first init container; checking preceding PVC references", "pod", klog.KObj(pod), "container", consumer, "initIndex", index)
	claims, conflicts := map[string]bool{}, map[string]bool{}
	var containers []string
	firstConflict := ""
	for _, init := range pod.Spec.InitContainers[:index] {
		hasPVC := false
		for _, vm := range init.VolumeMounts {
			v := volumes[vm.Name]
			if v.PersistentVolumeClaim == nil {
				continue
			}
			hasPVC = true
			claim := v.PersistentVolumeClaim.ClaimName
			claims[claim] = true
			if managed[claim] {
				conflicts[claim] = true
				if firstConflict == "" {
					firstConflict = init.Name
				}
			}
		}
		if hasPVC {
			containers = append(containers, init.Name)
		}
	}
	if len(claims) > 0 {
		names := func(set map[string]bool) []string {
			result := make([]string, 0, len(set))
			for name := range set {
				result = append(result, name)
			}
			sort.Strings(result)
			return result
		}
		klog.Warningf("Preceding init containers mount PVCs; review mountproxy startup order: pod=%s/%s container=%s pvcCount=%d pvcs=%v initContainers=%v managedPVCCount=%d managedPVCs=%v", pod.Namespace, pod.Name, consumer, len(claims), names(claims), containers, len(conflicts), names(conflicts))
	}
	// A warning is not a startup-order guarantee. Keep the existing rejection
	// when an earlier init consumes our PVC; it could otherwise succeed against
	// the empty projection before FUSE and leave the Pod falsely Ready.
	if firstConflict != "" {
		return fmt.Errorf("mountproxy must precede init container %s which consumes its PVC", firstConflict)
	}
	return nil
}
