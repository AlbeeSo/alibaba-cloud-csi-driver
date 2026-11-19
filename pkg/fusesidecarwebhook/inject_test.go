package fusesidecarwebhook

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"
)

func fixture() (*core.Pod, *fake.Clientset) {
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test", Annotations: map[string]string{InjectAnnotation: "true"}}, Spec: core.PodSpec{InitContainers: []core.Container{{Name: "prepare"}}, Containers: []core.Container{{Name: "app"}}}}
	var objects []runtime.Object
	for _, name := range []string{"a", "b"} {
		pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: name, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: name}}})
		objects = append(objects, &core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name)}, Spec: core.PersistentVolumeClaimSpec{VolumeName: "pv-" + name}},
			&core.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + name}, Spec: core.PersistentVolumeSpec{ClaimRef: &core.ObjectReference{Name: name, Namespace: "test", UID: types.UID(name)}, PersistentVolumeSource: core.PersistentVolumeSource{CSI: &core.CSIPersistentVolumeSource{Driver: "ossplugin.csi.alibabacloud.com", VolumeHandle: name, VolumeAttributes: map[string]string{"runtimeclass": "runc", "fuseType": "ossfs2", "authType": "rrsa"}}}}})
	}
	return pod, fake.NewClientset(objects...)
}

func TestInjectionBoundMultiVolumeAndIdempotence(t *testing.T) {
	pod, client := fixture()
	in := Injector{Client: client, OSSImage: "example.invalid/mountproxy:test"}
	out, err := in.Mutate(context.Background(), pod)
	require.NoError(t, err)
	require.Len(t, out.Spec.InitContainers, 2)
	c := out.Spec.InitContainers[0]
	require.Equal(t, filevolume.SidecarName, c.Name)
	require.False(t, *c.SecurityContext.Privileged)
	require.Equal(t, core.ContainerRestartPolicyAlways, *c.RestartPolicy)
	require.Len(t, c.Args, 1, "volume count must not enter args")
	require.Len(t, c.VolumeMounts, 6)
	require.NoError(t, filevolume.ValidateRRSAToken(out))
	require.Equal(t, "prepare", out.Spec.InitContainers[1].Name)
	again, err := in.Mutate(context.Background(), out)
	require.NoError(t, err)
	require.Equal(t, out, again)
	require.Len(t, pod.Spec.InitContainers, 1, "input must not be mutated")
	for _, action := range client.Actions() {
		require.NotEqual(t, "secrets", action.GetResource().Resource)
	}
}

func TestInjectionDisabledDoesNotReadAPI(t *testing.T) {
	pod, client := fixture()
	pod.Annotations = nil
	out, err := (Injector{Client: client}).Mutate(context.Background(), pod)
	require.NoError(t, err)
	require.Same(t, pod, out)
	require.Empty(t, client.Actions())
}

func TestNativeInjectionDoesNotInheritBusinessUID(t *testing.T) {
	pod, client := fixture()
	pod.Spec.SecurityContext = &core.PodSecurityContext{RunAsUser: ptr.To(int64(1000)), RunAsNonRoot: ptr.To(true)}
	out, err := (Injector{Client: client, OSSImage: "test"}).Mutate(t.Context(), pod)
	require.NoError(t, err)
	require.Equal(t, int64(1000), *out.Spec.SecurityContext.RunAsUser)
	security := out.Spec.InitContainers[0].SecurityContext
	require.Equal(t, int64(0), *security.RunAsUser)
	require.False(t, *security.RunAsNonRoot)
	require.False(t, *security.Privileged)
	require.False(t, *security.AllowPrivilegeEscalation)
	require.Equal(t, []core.Capability{"ALL"}, security.Capabilities.Drop)
}

func TestInjectionSupportsExistingRuntimeClassSpelling(t *testing.T) {
	pod, client := fixture()
	for _, name := range []string{"pv-a", "pv-b"} {
		pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), name, metav1.GetOptions{})
		require.NoError(t, err)
		delete(pv.Spec.CSI.VolumeAttributes, "runtimeclass")
		pv.Spec.CSI.VolumeAttributes["runtimeClass"] = "runc"
		_, err = client.CoreV1().PersistentVolumes().Update(context.Background(), pv, metav1.UpdateOptions{})
		require.NoError(t, err)
	}
	_, err := (Injector{Client: client, OSSImage: "test"}).Mutate(context.Background(), pod)
	require.NoError(t, err)
}

func TestInjectionConfigurationErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*core.Pod, *fake.Clientset)
		want   string
	}{
		{"unbound", func(p *core.Pod, c *fake.Clientset) {
			pvc, _ := c.CoreV1().PersistentVolumeClaims("test").Get(context.Background(), "a", metav1.GetOptions{})
			pvc.Spec.VolumeName = ""
			_, err := c.CoreV1().PersistentVolumeClaims("test").Update(context.Background(), pvc, metav1.UpdateOptions{})
			require.NoError(t, err)
		}, "WaitForFirstConsumer"},
		{"unknown runtime", func(p *core.Pod, c *fake.Clientset) {
			pv, _ := c.CoreV1().PersistentVolumes().Get(context.Background(), "pv-a", metav1.GetOptions{})
			delete(pv.Spec.CSI.VolumeAttributes, "runtimeclass")
			_, err := c.CoreV1().PersistentVolumes().Update(context.Background(), pv, metav1.UpdateOptions{})
			require.NoError(t, err)
		}, "explicit runtimeclass"},
		{"coco", func(p *core.Pod, c *fake.Clientset) { p.Spec.RuntimeClassName = ptr.To("kata-remote") }, "COCO"},
		{"bad existing consumer", func(p *core.Pod, c *fake.Clientset) {
			p.Spec.InitContainers = append([]core.Container{{Name: filevolume.SidecarName}}, p.Spec.InitContainers...)
		}, "restartable init"},
		{"volume conflict", func(p *core.Pod, c *fake.Clientset) {
			p.Spec.Volumes = append(p.Spec.Volumes, core.Volume{Name: "csi-mountproxy-state"})
		}, "conflicts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod, client := fixture()
			test.change(pod, client)
			_, err := (Injector{Client: client, OSSImage: "test"}).Mutate(context.Background(), pod)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestAdmissionHTTPPatchAndDenial(t *testing.T) {
	pod, client := fixture()
	in := Injector{Client: client, OSSImage: "test"}
	server := httptest.NewServer(in)
	defer server.Close()
	for _, fail := range []bool{false, true} {
		if fail {
			client.ClearActions()
			pod.Spec.InitContainers = append([]core.Container{{Name: filevolume.SidecarName}}, pod.Spec.InitContainers...)
		}
		raw, err := json.Marshal(pod)
		require.NoError(t, err)
		request := admission.AdmissionReview{Request: &admission.AdmissionRequest{UID: "request", Namespace: "test", Operation: admission.Create, Resource: metav1.GroupVersionResource{Resource: "pods", Version: "v1"}, Object: runtime.RawExtension{Raw: raw}}}
		body, err := json.Marshal(request)
		require.NoError(t, err)
		response, err := http.Post(server.URL, "application/json", bytes.NewReader(body))
		require.NoError(t, err)
		var result admission.AdmissionReview
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.NoError(t, response.Body.Close())
		require.Equal(t, types.UID("request"), result.Response.UID)
		require.Equal(t, !fail, result.Response.Allowed)
		if fail {
			require.NotEmpty(t, result.Response.Result.Message)
			require.Empty(t, result.Response.Patch)
		} else {
			require.Equal(t, admission.PatchTypeJSONPatch, *result.Response.PatchType)
			require.Contains(t, string(result.Response.Patch), filevolume.SidecarName)
		}
	}
}

func TestInjectionDriverAuthAndPrivilegeMatrix(t *testing.T) {
	for _, test := range []struct {
		name, runtime, driver, fuse, auth string
		allow                             bool
		want                              string
	}{
		{"guest denied privilege", "rund", "ossplugin.csi.alibabacloud.com", "ossfs2", "rrsa", false, "explicitly allow"},
		{"guest rrsa", "rund", "ossplugin.csi.alibabacloud.com", "ossfs2", "rrsa", true, ""},
		{"guest secret refused", "rund", "ossplugin.csi.alibabacloud.com", "ossfs2", "", true, "requires rrsa"},
		{"native secret fd", "runc", "ossplugin.csi.alibabacloud.com", "ossfs2", "", false, ""},
		{"custom guest", "rund", "customfuseplugin.csi.alibabacloud.com", "custom", "rrsa", true, ""},
		{"custom native secret", "runc", "customfuseplugin.csi.alibabacloud.com", "custom", "", true, ""},
		{"oss1 direct", "runc", "ossplugin.csi.alibabacloud.com", "ossfs", "rrsa", true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod, client := fixture()
			if test.runtime == "rund" {
				pod.Spec.RuntimeClassName = ptr.To("rund")
			}
			for _, name := range []string{"pv-a", "pv-b"} {
				pv, err := client.CoreV1().PersistentVolumes().Get(context.Background(), name, metav1.GetOptions{})
				require.NoError(t, err)
				pv.Spec.CSI.Driver = test.driver
				pv.Spec.CSI.VolumeAttributes = map[string]string{"runtimeclass": test.runtime, "fuseType": test.fuse, "authType": test.auth, "secretRef": "credentials"}
				_, err = client.CoreV1().PersistentVolumes().Update(context.Background(), pv, metav1.UpdateOptions{})
				require.NoError(t, err)
			}
			out, err := (Injector{Client: client, OSSImage: "oss-test", CustomImage: "custom-test", AllowPrivileged: test.allow}).Mutate(context.Background(), pod)
			if test.want != "" {
				require.ErrorContains(t, err, test.want)
				return
			}
			require.NoError(t, err)
			if test.auth == "" {
				_, err = filevolume.SecretDirectory(out, "credentials", []string{"AccessKeyId"})
				require.NoError(t, err)
			}
			if test.driver == "customfuseplugin.csi.alibabacloud.com" {
				require.Equal(t, filevolume.CustomSidecarName, out.Spec.InitContainers[0].Name)
			}
			for _, action := range client.Actions() {
				require.NotEqual(t, "secrets", action.GetResource().Resource)
			}
		})
	}
}
