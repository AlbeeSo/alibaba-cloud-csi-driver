//go:build !windows

package customfuse

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
)

func TestControllerSidecarHintFromStaticPVAndDynamicParameters(t *testing.T) {
	for _, dynamic := range []bool{false, true} {
		name := "static-pv-without-storageclass"
		if dynamic {
			name = "dynamic-storageclass-parameters"
		}
		t.Run(name, func(t *testing.T) {
			attrs := map[string]string{filevolume.MountModeKey: "sidecar", "runtimeClass": "runc", "fuseType": "custom-client", "source": "opaque"}
			controller := &controllerServer{}
			if dynamic {
				created, err := controller.CreateVolume(t.Context(), &csi.CreateVolumeRequest{Name: "volume", Parameters: attrs})
				require.NoError(t, err)
				attrs = created.Volume.VolumeContext
			}
			pv := &core.PersistentVolume{Spec: core.PersistentVolumeSpec{StorageClassName: "", PersistentVolumeSource: core.PersistentVolumeSource{CSI: &core.CSIPersistentVolumeSource{Driver: "customfuseplugin.csi.alibabacloud.com", VolumeHandle: "volume", VolumeAttributes: attrs}}}}
			response, err := controller.ControllerPublishVolume(t.Context(), &csi.ControllerPublishVolumeRequest{VolumeId: pv.Spec.CSI.VolumeHandle, NodeId: "node", VolumeContext: pv.Spec.CSI.VolumeAttributes})
			require.NoError(t, err)
			require.Empty(t, response.PublishContext)
		})
	}
}

func customFileFixture(t *testing.T) (*core.Pod, *csi.NodePublishVolumeRequest) {
	t.Helper()
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "consumer", Namespace: "test", UID: "uid"}, Spec: core.PodSpec{NodeName: "node", ServiceAccountName: "business", RuntimeClassName: ptr.To("rund")}}
	pod.Spec.Volumes = []core.Volume{
		{Name: "state", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: filevolume.HostStateDir}}},
		{Name: "rrsa", VolumeSource: core.VolumeSource{Projected: &core.ProjectedVolumeSource{Sources: []core.VolumeProjection{{ServiceAccountToken: &core.ServiceAccountTokenProjection{Audience: "sts.aliyuncs.com", Path: "token"}}}}}},
	}
	probe := func() *core.Probe {
		return &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: []string{"csi-mount-proxy-server", "--file-check-ready"}}}}
	}
	consumer := core.Container{Name: filevolume.CustomSidecarName, Command: []string{"custom-wrapper"}, RestartPolicy: ptr.To(core.ContainerRestartPolicyAlways), SecurityContext: &core.SecurityContext{Privileged: ptr.To(true)}, StartupProbe: probe(), ReadinessProbe: probe(), Env: []core.EnvVar{{Name: "POD_UID", ValueFrom: &core.EnvVarSource{FieldRef: &core.ObjectFieldSelector{FieldPath: "metadata.uid"}}}}, VolumeMounts: []core.VolumeMount{{Name: "state", MountPath: filevolume.DefaultStateDir, SubPathExpr: "$(POD_UID)"}, {Name: "rrsa", MountPath: filevolume.RRSATokenDir, ReadOnly: true}}}
	for i := range 2 {
		key := fmt.Sprintf("pv-%d", i)
		pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: key, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: fmt.Sprintf("claim-%d", i)}}})
		consumer.VolumeMounts = append(consumer.VolumeMounts, core.VolumeMount{Name: key, MountPath: filevolume.DefaultTargetDir + "/" + key + "/mount", MountPropagation: ptr.To(core.MountPropagationBidirectional)})
	}
	pod.Spec.InitContainers = []core.Container{consumer}
	req := &csi.NodePublishVolumeRequest{VolumeId: "custom-handle", TargetPath: filepath.Join(t.TempDir(), "pods", "uid", "volumes", "kubernetes.io~csi", "pv-0", "mount"), VolumeContext: map[string]string{utils.PodNameKey: pod.Name, utils.PodNamespaceKey: pod.Namespace, "csi.storage.k8s.io/pod.uid": "uid", "runtimeclass": "rund", "authType": "rrsa", "source": "custom-source", "capacity": "1Gi"}, Secrets: map[string]string{"password": "must-not-be-delivered"}, VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}}}
	return pod, req
}

func TestCustomFilePublishUsesSharedProtocolWithoutSecretOrPVCGET(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod, req := customFileFixture(t)
	client := fake.NewSimpleClientset(pod)
	client.PrependReactor("get", "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("normal path must not GET PVC")
		return false, nil, nil
	})
	ns := &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: mount.NewFakeMounter(nil), GenericNodeServer: common.GenericNodeServer{NodeID: "node"}}
	server := common.WrapNodeServerWithMetricRecorder(ns, "customfuse", client)
	for i := range 2 {
		req.VolumeId = fmt.Sprintf("custom-handle-%d", i)
		req.TargetPath = filepath.Join(filepath.Dir(filepath.Dir(req.TargetPath)), fmt.Sprintf("pv-%d", i), "mount")
		req.Readonly = i == 1
		_, err := server.NodePublishVolume(context.Background(), req)
		require.NoError(t, err)
		var desired filevolume.Desired
		require.NoError(t, filevolume.ReadJSON(filevolume.RequestPath(filepath.Join(root, "uid"), fmt.Sprintf("pv-%d", i)), &desired))
		require.Equal(t, "customfuse", desired.Mount.Fstype)
		require.Equal(t, req.VolumeId, desired.Mount.VolumeID)
		require.Empty(t, desired.Mount.Secrets)
		require.Contains(t, desired.Mount.Options, "capacity=1Gi")
		if i == 1 {
			require.Contains(t, desired.Mount.Options, "readOnly=true")
		}
	}
	keys, err := filevolume.ReadExpected(filepath.Join(root, "uid"), "uid")
	require.NoError(t, err)
	require.Equal(t, []string{"pv-0", "pv-1"}, keys)
	require.Len(t, client.Actions(), 2, "reuse one Pod GET per publish")
}

func TestCustomFileConsumerValidationAndControllerSkip(t *testing.T) {
	for _, scenario := range []string{"missing", "oss-name", "two-consumers", "secret", "recovery"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod, req := customFileFixture(t)
			switch scenario {
			case "missing":
				pod.Spec.InitContainers = nil
			case "oss-name":
				pod.Spec.InitContainers[0].Name = filevolume.SidecarName
			case "two-consumers":
				pod.Spec.InitContainers = append(pod.Spec.InitContainers, core.Container{Name: filevolume.SidecarName})
			case "secret":
				req.VolumeContext["authType"] = ""
			case "recovery":
				req.VolumeContext["recovery"] = "true"
			}
			ns := &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: mount.NewFakeMounter(nil)}
			_, err := common.WrapNodeServerWithMetricRecorder(ns, "customfuse", fake.NewSimpleClientset(pod)).NodePublishVolume(context.Background(), req)
			require.Error(t, err)
			require.NoFileExists(t, filepath.Join(root, "uid", "manifest.json"))
		})
	}
	_, err := (&controllerServer{}).ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeId: "volume", NodeId: "node", VolumeContext: map[string]string{filevolume.MountModeKey: "sidecar"}})
	require.NoError(t, err, "must skip without dereferencing a fuse pod manager")
	_, err = (&controllerServer{}).ControllerPublishVolume(context.Background(), &csi.ControllerPublishVolumeRequest{VolumeContext: map[string]string{filevolume.MountModeKey: "invalid"}})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestTerminatingCustomSidecarPublishIsRetryableWithoutNewState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod, req := customFileFixture(t)
	pod.DeletionTimestamp = ptr.To(meta.Now())
	client := fake.NewSimpleClientset(pod)
	ns := &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: mount.NewFakeMounter(nil), GenericNodeServer: common.GenericNodeServer{NodeID: "node"}}
	_, err := common.WrapNodeServerWithMetricRecorder(ns, "customfuse", client).NodePublishVolume(t.Context(), req)
	require.Equal(t, codes.Aborted, status.Code(err))
	require.NoDirExists(t, filepath.Join(root, "uid"))
	require.Len(t, client.Actions(), 1)
}
