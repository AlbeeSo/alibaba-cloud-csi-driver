// Package fusesidecar injects optional native sidecars for the RunD 3 file protocol.
package fusesidecar

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const InjectAnnotation = "fuse.csi.alibabacloud.com/inject"
const ProfileAttribute = "fuse.csi.alibabacloud.com/sidecar-profile"
const OSSDriver = "ossplugin.csi.alibabacloud.com"
const CustomDriver = "customfuseplugin.csi.alibabacloud.com"

// Profiles are operator-owned; a PVC cannot cause an arbitrary privileged image
// to run. One profile per CSI driver may be selected in a Pod. OSS profiles must
// include both ossfs clients; customfuse images provide their own /entrypoint.sh.
type Profile struct {
	Driver    string                    `json:"driver"`
	Image     string                    `json:"image"`
	Resources core.ResourceRequirements `json:"resources,omitempty"`
}
type Config struct {
	OSSStateRoot     string                      `json:"ossStateRoot"`
	CustomStateRoot  string                      `json:"customStateRoot"`
	Profiles         map[string]Profile          `json:"profiles"`
	ImagePullSecrets []core.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

func (c *Config) Defaults() {
	if c.OSSStateRoot == "" {
		c.OSSStateRoot = "/var/run/ossfs/file-volumes"
	}
	if c.CustomStateRoot == "" {
		c.CustomStateRoot = "/var/run/customfuse/file-volumes"
	}
}
func (c Config) Validate() error {
	for _, p := range []string{c.OSSStateRoot, c.CustomStateRoot} {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return fmt.Errorf("sidecar roots must be absolute non-root paths")
		}
	}
	if c.OSSStateRoot == c.CustomStateRoot {
		return fmt.Errorf("CSI drivers require separate state roots")
	}
	for name, p := range c.Profiles {
		if name == "" || p.Image == "" || (p.Driver != OSSDriver && p.Driver != CustomDriver) {
			return fmt.Errorf("invalid sidecar profile %q", name)
		}
	}
	return nil
}

type group struct {
	profile string
	handles map[string]bool
	names   map[string]bool
	targets map[string]string // PV directory -> existing Pod PVC volume name
}

// Mutate does not need the admission-time Pod UID. subPathExpr is expanded by
// kubelet from the Downward API only after the real UID has been assigned.
func Mutate(ctx context.Context, client kubernetes.Interface, cfg Config, pod *core.Pod) (*core.Pod, error) {
	if !filevolume.Enabled() {
		return pod.DeepCopy(), nil
	}
	if v := pod.Annotations[InjectAnnotation]; v == "false" {
		return pod.DeepCopy(), nil
	} else if v != "" && v != "true" {
		return nil, fmt.Errorf("%s must be true or false", InjectAnnotation)
	}
	supported, err := filevolume.SupportsSidecar(ctx, client, pod)
	if err != nil {
		return nil, err
	}
	if !supported {
		return pod.DeepCopy(), nil
	}
	cfg.Defaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, fmt.Errorf("missing Kubernetes client")
	}
	groups := map[string]*group{}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		claim, err := client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, v.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("cannot resolve PVC %s", v.PersistentVolumeClaim.ClaimName)
		}
		if claim.Spec.VolumeName == "" {
			if claim.Spec.StorageClassName != nil && *claim.Spec.StorageClassName != "" {
				sc, err := client.StorageV1().StorageClasses().Get(ctx, *claim.Spec.StorageClassName, metav1.GetOptions{})
				if err != nil {
					return nil, fmt.Errorf("cannot resolve StorageClass for PVC %s", claim.Name)
				}
				if sc.Provisioner != OSSDriver && sc.Provisioner != CustomDriver {
					continue
				}
			}
			return nil, fmt.Errorf("PVC %s must be bound before RunD sidecar admission (WaitForFirstConsumer is not supported)", claim.Name)
		}
		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, claim.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("cannot resolve PV for PVC %s", claim.Name)
		}
		if pv.Spec.CSI == nil || (pv.Spec.CSI.Driver != OSSDriver && pv.Spec.CSI.Driver != CustomDriver) {
			continue
		}
		if ref := pv.Spec.ClaimRef; ref == nil || ref.Namespace != pod.Namespace || ref.Name != claim.Name || ref.UID != claim.UID {
			return nil, fmt.Errorf("PV %s binding does not match PVC", pv.Name)
		}
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == core.PersistentVolumeBlock {
			return nil, fmt.Errorf("FUSE sidecars require filesystem volumes")
		}
		driver := pv.Spec.CSI.Driver
		profile := pv.Spec.CSI.VolumeAttributes[ProfileAttribute]
		if profile == "" {
			if driver == OSSDriver {
				profile = "oss"
			} else {
				profile = "customfuse"
			}
		}
		p, ok := cfg.Profiles[profile]
		if !ok || p.Driver != driver {
			return nil, fmt.Errorf("no matching operator sidecar profile %q for PV %s", profile, pv.Name)
		}
		g := groups[driver]
		if g == nil {
			g = &group{profile: profile, handles: map[string]bool{}, names: map[string]bool{}, targets: map[string]string{}}
			groups[driver] = g
		}
		if g.profile != profile {
			return nil, fmt.Errorf("one Pod cannot use different sidecar profiles for CSI driver %s; use a dispatching image or manual sidecars", driver)
		}
		id := pv.Spec.CSI.VolumeHandle
		if id == "" || strings.ContainsAny(id, ",\r\n") {
			return nil, fmt.Errorf("PV %s has an invalid file-protocol volume handle", pv.Name)
		}
		g.handles[id] = true
		g.names[v.Name] = true
		if _, exists := g.targets[pv.Name]; !exists {
			g.targets[pv.Name] = v.Name
		}
	}
	out := pod.DeepCopy()
	var injected []core.Container
	for _, driver := range []string{OSSDriver, CustomDriver} {
		g := groups[driver]
		if g == nil {
			continue
		}
		name, root, drivers := "oss", cfg.OSSStateRoot, "ossfs,ossfs2"
		if driver == CustomDriver {
			name, root, drivers = "customfuse", cfg.CustomStateRoot, "customfuse"
		}
		var ids []string
		for id := range g.handles {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		stateName := "fuse-" + name + "-state"
		state := core.Volume{Name: stateName, VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: root, Type: ptr.To(core.HostPathDirectoryOrCreate)}}}
		for _, v := range []core.Volume{state} {
			found := false
			for _, existing := range out.Spec.Volumes {
				if existing.Name == v.Name {
					if !reflect.DeepEqual(existing, v) {
						return nil, fmt.Errorf("reserved volume %s conflicts", v.Name)
					}
					found = true
				}
			}
			if !found {
				out.Spec.Volumes = append(out.Spec.Volumes, v)
			}
		}
		args := []string{"--file-state-dir=/volume-state", "--file-target-dir=/pod-volumes", "--file-expected-volumes=" + strings.Join(ids, ","), "--timeout=30s"}
		probe := append([]string{"csi-mount-proxy-server", "--file-check-ready"}, args[:3]...)
		side := core.Container{Name: "fuse-" + name, Image: cfg.Profiles[g.profile].Image, ImagePullPolicy: core.PullIfNotPresent, Command: []string{"csi-mount-proxy-server"}, Args: append([]string{"--driver=" + drivers}, args...), RestartPolicy: ptr.To(core.ContainerRestartPolicyAlways), Resources: cfg.Profiles[g.profile].Resources,
			SecurityContext: &core.SecurityContext{Privileged: ptr.To(true), RunAsUser: ptr.To(int64(0))},
			Env:             []core.EnvVar{{Name: "POD_UID", ValueFrom: &core.EnvVarSource{FieldRef: &core.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.uid"}}}},
			StartupProbe:    &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: probe}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 60},
			ReadinessProbe:  &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: probe}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 3},
			VolumeMounts:    []core.VolumeMount{{Name: stateName, MountPath: "/volume-state", SubPathExpr: "$(POD_UID)"}},
		}
		// Share the exact PVC source used by the application. A hostPath of its
		// kubelet parent is exported as a different virtio-fs path by RunD, and
		// guest mounts on that alias are invisible to the application's PVC.
		var pvs []string
		for pv := range g.targets {
			pvs = append(pvs, pv)
		}
		sort.Strings(pvs)
		for _, pv := range pvs {
			side.VolumeMounts = append(side.VolumeMounts, core.VolumeMount{Name: g.targets[pv], MountPath: filepath.Join("/pod-volumes", pv, "mount"), MountPropagation: ptr.To(core.MountPropagationBidirectional)})
		}
		found := false
		for _, c := range out.Spec.Containers {
			if c.Name == side.Name {
				return nil, fmt.Errorf("reserved sidecar name %s conflicts", side.Name)
			}
		}
		for _, c := range out.Spec.InitContainers {
			if c.Name == side.Name {
				if !reflect.DeepEqual(c, side) {
					return nil, fmt.Errorf("reserved sidecar name %s conflicts", side.Name)
				}
				found = true
			}
		}
		if !found {
			injected = append(injected, side)
		}
		for _, containers := range [][]core.Container{out.Spec.Containers, out.Spec.InitContainers} {
			for i := range containers {
				if containers[i].Name == side.Name {
					continue
				}
				for j := range containers[i].VolumeMounts {
					m := &containers[i].VolumeMounts[j]
					if g.names[m.Name] {
						if m.SubPath != "" || m.SubPathExpr != "" {
							return nil, fmt.Errorf("FUSE application subPath mounts cannot precede sidecar mounting")
						}
						if m.MountPropagation != nil && *m.MountPropagation != core.MountPropagationHostToContainer {
							return nil, fmt.Errorf("application FUSE mounts require HostToContainer propagation")
						}
						m.MountPropagation = ptr.To(core.MountPropagationHostToContainer)
					}
				}
			}
		}
	}
	out.Spec.InitContainers = append(injected, out.Spec.InitContainers...)
	if len(groups) > 0 {
		for _, s := range cfg.ImagePullSecrets {
			found := false
			for _, e := range out.Spec.ImagePullSecrets {
				if e.Name == s.Name {
					found = true
				}
			}
			if !found {
				out.Spec.ImagePullSecrets = append(out.Spec.ImagePullSecrets, s)
			}
		}
	}
	return out, nil
}
