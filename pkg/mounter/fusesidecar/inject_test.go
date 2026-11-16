package fusesidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	storage "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/utils/ptr"
)

func fixture(t *testing.T) (*fake.Clientset, Config, *core.Pod) {
	t.Helper()
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	client := fake.NewSimpleClientset()
	cfg := Config{Profiles: map[string]Profile{"oss": {Driver: OSSDriver, Image: "example.invalid/oss-aio:test"}, "customfuse": {Driver: CustomDriver, Image: "example.invalid/custom:test"}}}
	cfg.Defaults()
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test"}, Spec: core.PodSpec{RuntimeClassName: ptr.To("rund"), Containers: []core.Container{{Name: "app", Image: "example.invalid/app:test"}}, InitContainers: []core.Container{{Name: "init", Image: "example.invalid/init:test"}}}}
	for _, name := range []string{"oss-a", "oss-b", "custom"} {
		driver := OSSDriver
		if name == "custom" {
			driver = CustomDriver
		}
		claim := &core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: pod.Namespace, UID: types.UID("claim-" + name)}, Spec: core.PersistentVolumeClaimSpec{VolumeName: name}}
		pv := &core.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: core.PersistentVolumeSpec{ClaimRef: &core.ObjectReference{Name: name, Namespace: pod.Namespace, UID: claim.UID}, PersistentVolumeSource: core.PersistentVolumeSource{CSI: &core.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: name, VolumeAttributes: map[string]string{}}}}}
		_, err := client.CoreV1().PersistentVolumeClaims(pod.Namespace).Create(context.Background(), claim, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = client.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{})
		require.NoError(t, err)
		pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: name, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name}}})
		pod.Spec.Containers[0].VolumeMounts = append(pod.Spec.Containers[0].VolumeMounts, core.VolumeMount{Name: name, MountPath: "/" + name, ReadOnly: name == "oss-b"})
	}
	return client, cfg, pod
}

func TestMixedDriversAndAdmissionRoundTrip(t *testing.T) {
	client, cfg, pod := fixture(t)
	out, err := Mutate(context.Background(), client, cfg, pod)
	require.NoError(t, err)
	require.Empty(t, pod.Spec.Containers[0].VolumeMounts[0].MountPropagation)
	require.Len(t, out.Spec.InitContainers, 3)
	require.Equal(t, "fuse-oss", out.Spec.InitContainers[0].Name)
	require.Equal(t, "fuse-customfuse", out.Spec.InitContainers[1].Name)
	require.Equal(t, "init", out.Spec.InitContainers[2].Name)
	require.Contains(t, out.Spec.InitContainers[0].Args, "--driver=ossfs,ossfs2")
	require.Contains(t, out.Spec.InitContainers[0].Args, "--file-expected-volumes=oss-a,oss-b")
	require.Equal(t, "$(POD_UID)", out.Spec.InitContainers[0].VolumeMounts[0].SubPathExpr)
	require.Equal(t, core.MountPropagationBidirectional, *out.Spec.InitContainers[0].VolumeMounts[1].MountPropagation)
	require.Equal(t, core.MountPropagationHostToContainer, *out.Spec.Containers[0].VolumeMounts[0].MountPropagation)
	require.True(t, out.Spec.Containers[0].VolumeMounts[1].ReadOnly)
	again, err := Mutate(context.Background(), client, cfg, out)
	require.NoError(t, err)
	require.Equal(t, out, again)
	// Exercise the actual HTTPS AdmissionReview handler, including dry-run.
	raw, err := json.Marshal(pod)
	require.NoError(t, err)
	review := admission.AdmissionReview{Request: &admission.AdmissionRequest{UID: "review-uid", Namespace: pod.Namespace, Operation: admission.Create, Resource: metav1.GroupVersionResource{Version: "v1", Resource: "pods"}, Object: runtime.RawExtension{Raw: raw}, DryRun: ptr.To(true)}}
	body, err := json.Marshal(review)
	require.NoError(t, err)
	server := httptest.NewTLSServer(Handler{Client: client, Config: cfg})
	defer server.Close()
	response, err := server.Client().Post(server.URL, "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer response.Body.Close()
	var got admission.AdmissionReview
	require.NoError(t, json.NewDecoder(response.Body).Decode(&got))
	require.Equal(t, review.Request.UID, got.Response.UID)
	require.True(t, got.Response.Allowed)
	require.NotEmpty(t, got.Response.Patch)
	var patch []struct {
		Op, Path string
		Value    core.PodSpec
	}
	require.NoError(t, json.Unmarshal(got.Response.Patch, &patch))
	require.Equal(t, "/spec", patch[0].Path)
	require.Equal(t, out.Spec, patch[0].Value)
}

func TestSelectionAndManualSidecars(t *testing.T) {
	for _, mode := range []string{"gate-off", "runc", "manual"} {
		t.Run(mode, func(t *testing.T) {
			client, cfg, pod := fixture(t)
			switch mode {
			case "gate-off":
				gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
			case "runc":
				pod.Spec.RuntimeClassName = ptr.To("runc")
			case "manual":
				pod.Annotations = map[string]string{InjectAnnotation: "false"}
			}
			out, err := Mutate(context.Background(), client, cfg, pod)
			require.NoError(t, err)
			require.Equal(t, pod, out)
			require.Empty(t, client.Actions()[6:])
		})
	}
}

func TestRejectUnsafeOrUnresolvableAdmission(t *testing.T) {
	for _, mode := range []string{"missing-pvc", "unbound", "binding", "profile", "conflict", "subpath", "propagation", "block"} {
		t.Run(mode, func(t *testing.T) {
			client, cfg, pod := fixture(t)
			ctx := context.Background()
			switch mode {
			case "missing-pvc":
				require.NoError(t, client.CoreV1().PersistentVolumeClaims("test").Delete(ctx, "oss-a", metav1.DeleteOptions{}))
			case "unbound":
				p, _ := client.CoreV1().PersistentVolumeClaims("test").Get(ctx, "oss-a", metav1.GetOptions{})
				p.Spec.VolumeName = ""
				_, err := client.CoreV1().PersistentVolumeClaims("test").Update(ctx, p, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "binding", "block":
				p, _ := client.CoreV1().PersistentVolumes().Get(ctx, "oss-a", metav1.GetOptions{})
				if mode == "block" {
					p.Spec.VolumeMode = ptr.To(core.PersistentVolumeBlock)
				} else {
					p.Spec.ClaimRef.UID = "wrong"
				}
				_, err := client.CoreV1().PersistentVolumes().Update(ctx, p, metav1.UpdateOptions{})
				require.NoError(t, err)
			case "profile":
				delete(cfg.Profiles, "oss")
			case "conflict":
				pod.Spec.Containers = append(pod.Spec.Containers, core.Container{Name: "fuse-oss"})
			case "subpath":
				pod.Spec.Containers[0].VolumeMounts[0].SubPath = "dir"
			case "propagation":
				pod.Spec.Containers[0].VolumeMounts[0].MountPropagation = ptr.To(core.MountPropagationBidirectional)
			}
			_, err := Mutate(ctx, client, cfg, pod)
			require.Error(t, err)
		})
	}
}

func TestUnrelatedWaitForFirstConsumer(t *testing.T) {
	client, cfg, pod := fixture(t)
	_, err := client.StorageV1().StorageClasses().Create(context.Background(), &storage.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Provisioner: "diskplugin.csi.alibabacloud.com"}, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = client.CoreV1().PersistentVolumeClaims("test").Create(context.Background(), &core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "disk"}, Spec: core.PersistentVolumeClaimSpec{StorageClassName: ptr.To("disk")}}, metav1.CreateOptions{})
	require.NoError(t, err)
	pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: "disk", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "disk"}}})
	_, err = Mutate(context.Background(), client, cfg, pod)
	require.NoError(t, err)
}

func TestMalformedReview(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/mutate", bytes.NewBufferString("{}"))
	w := httptest.NewRecorder()
	Handler{}.ServeHTTP(w, r)
	require.Equal(t, http.StatusBadRequest, w.Code)
}
