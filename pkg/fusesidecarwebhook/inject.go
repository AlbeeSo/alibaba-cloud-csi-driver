// Package fusesidecarwebhook generates the same Pod contract used by manual
// consumers. Admission never executes mounts or reads credential Secrets.
package fusesidecarwebhook

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const InjectAnnotation = "csi.alibabacloud.com/inject-mountproxy"

type Injector struct {
	Client                kubernetes.Interface
	OSSImage, CustomImage string
	AllowPrivileged       bool
}

type dataVolume struct{ name, pv string }

// Mutate returns a copy. No annotation means no API calls. Bound PVCs are
// required: waiting here for WFFC binding would block the scheduling it needs.
func (in Injector) Mutate(ctx context.Context, pod *core.Pod) (*core.Pod, error) {
	if pod.Annotations[InjectAnnotation] != "true" {
		return pod, nil
	}
	consumer, _, err := filevolume.Consumer(pod)
	if err != nil {
		return nil, err
	}
	if in.Client == nil {
		return nil, fmt.Errorf("webhook Kubernetes client is unavailable")
	}
	var data []dataVolume
	seenClaims := map[string]bool{}
	family, kind, fdMode := "", "", false
	secrets := map[string]bool{}
	rrsa := false
	recovery := false
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			continue
		}
		claim := volume.PersistentVolumeClaim.ClaimName
		if seenClaims[claim] {
			return nil, fmt.Errorf("PVC %s is declared more than once", claim)
		}
		seenClaims[claim] = true
		pvc, err := in.Client.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, claim, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("resolve PVC %s: %w", claim, err)
		}
		if pvc.Spec.VolumeName == "" {
			return nil, fmt.Errorf("PVC %s must be bound before mountproxy injection; unbound/WaitForFirstConsumer admission is unsupported", claim)
		}
		pv, err := in.Client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("resolve PV for PVC %s: %w", claim, err)
		}
		if pv.Spec.CSI == nil {
			continue
		}
		driver := pv.Spec.CSI.Driver
		if driver != "ossplugin.csi.alibabacloud.com" && driver != "customfuseplugin.csi.alibabacloud.com" {
			continue
		}
		if pv.Spec.ClaimRef == nil || pv.Spec.ClaimRef.Namespace != pod.Namespace || pv.Spec.ClaimRef.Name != claim || pv.Spec.ClaimRef.UID != pvc.UID {
			return nil, fmt.Errorf("PV %s does not match the PVC binding identity", pv.Name)
		}
		attrs := map[string]string{}
		for _, key := range []string{"runtimeclass", "fuseType", "authType", "secretRef", "recovery"} {
			value, err := filevolume.Attribute(pv.Spec.CSI.VolumeAttributes, key)
			if err != nil {
				return nil, fmt.Errorf("PV %s: %w", pv.Name, err)
			}
			attrs[key] = value
		}
		runtime := strings.ToLower(attrs["runtimeclass"])
		if runtime != "runc" && runtime != "rund" {
			return nil, fmt.Errorf("PV %s needs explicit runtimeclass runc or rund; Pod defaults do not prove runtime capabilities", pv.Name)
		}
		if pod.Spec.RuntimeClassName != nil && *pod.Spec.RuntimeClassName == "kata-remote" {
			return nil, fmt.Errorf("kata-remote uses the existing COCO path, not mountproxy injection")
		}
		if runtime == "rund" && (pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == "" || *pod.Spec.RuntimeClassName == "runc") {
			return nil, fmt.Errorf("PV %s requires an explicit guest RuntimeClass on the Pod", pv.Name)
		}
		nativeFD := runtime == "runc" && driver == "ossplugin.csi.alibabacloud.com" && attrs["fuseType"] == "ossfs2"
		if attrs["recovery"] != "" {
			enabled, err := strconv.ParseBool(attrs["recovery"])
			if err != nil {
				return nil, fmt.Errorf("PV %s has invalid recovery boolean", pv.Name)
			}
			if enabled && !nativeFD {
				return nil, fmt.Errorf("PV %s recovery requires native OSS2 fd consumer", pv.Name)
			}
			recovery = recovery || enabled
		}
		if len(data) > 0 && (family != runtime || kind != driver || fdMode != nativeFD) {
			return nil, fmt.Errorf("one mountproxy cannot mix runtime families, OSS/customfuse, or fd/direct mount capabilities")
		}
		family, kind, fdMode = runtime, driver, nativeFD
		if attrs["authType"] == "rrsa" {
			rrsa = true
		} else if runtime == "runc" && attrs["authType"] == "" && attrs["secretRef"] != "" {
			secrets[attrs["secretRef"]] = true
		} else {
			return nil, fmt.Errorf("PV %s requires rrsa, or native runc SecretRef", pv.Name)
		}
		data = append(data, dataVolume{volume.Name, pv.Name})
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("injection requested but Pod has no bound OSS/customfuse volumes")
	}
	name, image := filevolume.SidecarName, in.OSSImage
	if kind == "customfuseplugin.csi.alibabacloud.com" {
		name, image = filevolume.CustomSidecarName, in.CustomImage
	}
	if consumer != nil {
		if consumer.Name != name {
			return nil, fmt.Errorf("existing consumer name does not match volume driver")
		}
		if err := validate(pod, data, fdMode, rrsa, secrets); err != nil {
			return nil, fmt.Errorf("existing manual consumer: %w", err)
		}
		if recovery {
			if err := filevolume.ValidateRecoveryProjection(pod); err != nil {
				return nil, err
			}
		}
		return pod, nil
	}
	if image == "" {
		return nil, fmt.Errorf("webhook has no configured compatible sidecar image for %s", kind)
	}
	if !fdMode && !in.AllowPrivileged {
		return nil, fmt.Errorf("direct mount requires privileged sidecar; administrator must explicitly allow privileged injection")
	}
	out := pod.DeepCopy()
	// Fixed generated names must not overwrite application volumes.
	addVolume := func(v core.Volume) error {
		for _, old := range out.Spec.Volumes {
			if old.Name == v.Name {
				return fmt.Errorf("generated volume name %s conflicts with an existing volume", v.Name)
			}
		}
		out.Spec.Volumes = append(out.Spec.Volumes, v)
		return nil
	}
	if err := addVolume(core.Volume{Name: "csi-mountproxy-state", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: filevolume.HostStateDir, Type: ptr.To(core.HostPathDirectoryOrCreate)}}}); err != nil {
		return nil, err
	}
	probe := func() *core.Probe {
		return &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: []string{"csi-mount-proxy-server", "--file-check-ready"}}}, PeriodSeconds: 2, TimeoutSeconds: 2, FailureThreshold: 150}
	}
	sidecar := core.Container{Name: name, Image: image, Command: []string{"csi-mount-proxy-server"}, Args: []string{"--file-mode"}, RestartPolicy: ptr.To(core.ContainerRestartPolicyAlways),
		Env:             []core.EnvVar{{Name: "POD_UID", ValueFrom: &core.EnvVarSource{FieldRef: &core.ObjectFieldSelector{FieldPath: "metadata.uid"}}}},
		SecurityContext: &core.SecurityContext{Privileged: ptr.To(!fdMode)}, StartupProbe: probe(), ReadinessProbe: probe(),
		VolumeMounts: []core.VolumeMount{{Name: "csi-mountproxy-state", MountPath: filevolume.DefaultStateDir, SubPathExpr: "$(POD_UID)"}},
	}
	sidecar.ReadinessProbe.FailureThreshold = 1
	propagation := core.MountPropagationBidirectional
	if fdMode {
		propagation = core.MountPropagationHostToContainer
	}
	for _, d := range data {
		sidecar.VolumeMounts = append(sidecar.VolumeMounts, core.VolumeMount{Name: d.name, MountPath: path.Join(filevolume.DefaultTargetDir, d.pv, "mount"), MountPropagation: ptr.To(propagation)})
	}
	if recovery {
		if err := addVolume(core.Volume{Name: "csi-mountproxy-fusectl", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: filevolume.FuseConnectionsDir, Type: ptr.To(core.HostPathDirectory)}}}); err != nil {
			return nil, err
		}
		sidecar.VolumeMounts = append(sidecar.VolumeMounts, core.VolumeMount{Name: "csi-mountproxy-fusectl", MountPath: filevolume.FuseConnectionsDir})
	}
	if rrsa {
		if err := addVolume(core.Volume{Name: "csi-mountproxy-rrsa", VolumeSource: core.VolumeSource{Projected: &core.ProjectedVolumeSource{Sources: []core.VolumeProjection{{ServiceAccountToken: &core.ServiceAccountTokenProjection{Audience: "sts.aliyuncs.com", Path: "token", ExpirationSeconds: ptr.To(int64(3600))}}}}}}); err != nil {
			return nil, err
		}
		sidecar.VolumeMounts = append(sidecar.VolumeMounts, core.VolumeMount{Name: "csi-mountproxy-rrsa", MountPath: filevolume.RRSATokenDir, ReadOnly: true})
	}
	var secretNames []string
	for secret := range secrets {
		secretNames = append(secretNames, secret)
	}
	sort.Strings(secretNames)
	for _, secret := range secretNames {
		if !filevolume.ValidComponent(secret) {
			return nil, fmt.Errorf("invalid SecretRef name")
		}
		volumeName := "csi-secret-" + filevolume.ID(secret)[:16]
		if err := addVolume(core.Volume{Name: volumeName, VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: secret}}}); err != nil {
			return nil, err
		}
		sidecar.VolumeMounts = append(sidecar.VolumeMounts, core.VolumeMount{Name: volumeName, MountPath: path.Join(filevolume.SecretRoot, secret), ReadOnly: true})
	}
	out.Spec.InitContainers = append([]core.Container{sidecar}, out.Spec.InitContainers...)
	if err := validate(out, data, fdMode, rrsa, secrets); err != nil {
		return nil, err
	}
	return out, nil
}

func validate(pod *core.Pod, data []dataVolume, fd, rrsa bool, secrets map[string]bool) error {
	copy := pod.DeepCopy()
	// Admission runs before the apiserver assigns UID; only structural validation
	// uses a synthetic value. Generated env always resolves the real Pod UID.
	copy.UID = types.UID("admission-validation")
	for _, d := range data {
		req := &csi.NodePublishVolumeRequest{TargetPath: "/pods/admission-validation/volumes/kubernetes.io~csi/" + d.pv + "/mount", VolumeContext: map[string]string{utils.PodUIDKey: string(copy.UID)}}
		if _, err := filevolume.ManifestForPodWithFD(copy, req, fd); err != nil {
			return err
		}
	}
	if rrsa {
		if err := filevolume.ValidateRRSAToken(copy); err != nil {
			return err
		}
	}
	for name := range secrets {
		if _, err := filevolume.SecretDirectory(copy, name, nil); err != nil {
			return err
		}
	}
	return nil
}
