//go:build !windows

package oss

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	kata "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils/kata/directvolume"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
)

func manualFilePod(class string, count int) *core.Pod {
	p := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-a", Namespace: "test", UID: "pod-a"}, Spec: core.PodSpec{NodeName: "test-node", RuntimeClassName: ptr.To(class)}}
	c := core.Container{Name: filevolume.SidecarName, Command: []string{"csi-mount-proxy-server"}, Args: []string{"--file-mode"}, RestartPolicy: ptr.To(core.ContainerRestartPolicyAlways),
		SecurityContext: &core.SecurityContext{Privileged: ptr.To(true)},
		StartupProbe:    &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: []string{"csi-mount-proxy-server", "--file-check-ready"}}}},
		ReadinessProbe:  &core.Probe{ProbeHandler: core.ProbeHandler{Exec: &core.ExecAction{Command: []string{"csi-mount-proxy-server", "--file-check-ready"}}}},
		Env:             []core.EnvVar{{Name: "POD_UID", ValueFrom: &core.EnvVarSource{FieldRef: &core.ObjectFieldSelector{FieldPath: "metadata.uid"}}}},
		VolumeMounts:    []core.VolumeMount{{Name: "state", MountPath: filevolume.DefaultStateDir, SubPathExpr: "$(POD_UID)"}}}
	p.Spec.Volumes = []core.Volume{{Name: "state", VolumeSource: core.VolumeSource{HostPath: &core.HostPathVolumeSource{Path: filevolume.HostStateDir}}}}
	for i := range count {
		key := fmt.Sprintf("pv-%d", i)
		p.Spec.Volumes = append(p.Spec.Volumes, core.Volume{Name: key, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: fmt.Sprintf("claim-%d", i)}}})
		c.VolumeMounts = append(c.VolumeMounts, core.VolumeMount{Name: key, MountPath: "/pod-volumes/" + key + "/mount", MountPropagation: ptr.To(core.MountPropagationBidirectional)})
	}
	p.Spec.InitContainers = []core.Container{c}
	p.Spec.Volumes = append(p.Spec.Volumes, core.Volume{Name: "rrsa", VolumeSource: core.VolumeSource{Projected: &core.ProjectedVolumeSource{Sources: []core.VolumeProjection{{ServiceAccountToken: &core.ServiceAccountTokenProjection{Audience: "sts.aliyuncs.com", Path: "token"}}}}}})
	p.Spec.InitContainers[0].VolumeMounts = append(p.Spec.InitContainers[0].VolumeMounts, core.VolumeMount{Name: "rrsa", MountPath: filevolume.RRSATokenDir, ReadOnly: true})
	return p
}

func fileRequest(t *testing.T, i int, fuseType string) *csi.NodePublishVolumeRequest {
	t.Helper()
	return &csi.NodePublishVolumeRequest{VolumeId: fmt.Sprintf("handle-%d", i), TargetPath: filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", fmt.Sprintf("pv-%d", i), "mount"),
		VolumeContext:    map[string]string{utils.PodNameKey: "pod-a", utils.PodNamespaceKey: "test", "csi.storage.k8s.io/pod.uid": "pod-a", "runtimeclass": "rund", "fuseType": fuseType, "bucket": "test-bucket", "url": "oss-cn-beijing.aliyuncs.com", "authType": "rrsa", "roleArn": "acs:ram::1234567890123456:role/test", "oidcProviderArn": "acs:ram::1234567890123456:oidc-provider/test"},
		Secrets:          map[string]string{"AccessKeyId": "test-ak", "AccessKeySecret": "test-sk"},
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}}}
}

func TestDirectPublishCachedPodRouting(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, class := range []string{"rund", "runv", "sandbox", "unknown", "kata-remote"} {
		t.Run(class, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			restore := kata.SetRootPathForTesting(t.TempDir())
			defer restore()
			pod := manualFilePod(class, 2)
			if class == "kata-remote" {
				pod.Spec.InitContainers = nil
			}
			client := fileTestClient(pod)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			// Pod routing reuses the wrapper GET; cold PVC validation is separate.
			server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			for i, fuse := range []string{"ossfs", "ossfs2"} {
				req := fileRequest(t, i, fuse)
				_, err := server.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
				path := filevolume.RequestPath(filepath.Join(root, "pod-a"), fmt.Sprintf("pv-%d", i))
				if class == "kata-remote" {
					require.True(t, isDirectVolumePath(req.TargetPath))
					require.NoFileExists(t, path)
				} else {
					var d filevolume.Desired
					require.NoError(t, filevolume.ReadJSON(path, &d))
					require.NoError(t, d.Validate("pod-a"))
					require.Equal(t, req.VolumeId, d.Mount.VolumeID)
					require.Equal(t, fuse, d.Mount.Fstype)
					require.Empty(t, d.Mount.Secrets, "RRSA must not forward CSI credentials")
					keys, err := filevolume.ReadExpected(filepath.Join(root, "pod-a"), "pod-a")
					require.NoError(t, err)
					require.Equal(t, []string{"pv-0", "pv-1"}, keys)
				}
				_, err = server.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
			}
			podGets, pvcGets := fileQueryCounts(t, client)
			require.Equal(t, 4, podGets)
			if class == "kata-remote" {
				require.Zero(t, pvcGets)
			} else {
				require.Equal(t, 2, pvcGets)
			}
		})
	}
}

func TestDirectPublishFailsClosedWithoutAdditionalQueries(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, scenario := range []string{"api-failure", "uid-mismatch", "wrong-node", "missing-sidecar", "missing-state", "missing-target", "fake-probe", "no-readiness", "ordinary-init", "missing-startup", "missing-uid-env", "unprivileged", "wrong-propagation", "duplicate-pvc", "runc", "nil-class", "empty-class", "terminating-pod", "early-data-init"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod := manualFilePod("rund", 1)
			switch scenario {
			case "early-data-init":
				pod.Spec.InitContainers = append([]core.Container{{Name: "prepare", VolumeMounts: []core.VolumeMount{{Name: "pv-0", MountPath: "/data"}}}}, pod.Spec.InitContainers...)
			case "runc":
				pod.Spec.RuntimeClassName = ptr.To("runc")
			case "nil-class":
				pod.Spec.RuntimeClassName = nil
			case "empty-class":
				pod.Spec.RuntimeClassName = ptr.To("")
			case "terminating-pod":
				pod.DeletionTimestamp = ptr.To(metav1.Now())
			case "uid-mismatch":
				pod.UID = "replacement"
			case "wrong-node":
				pod.Spec.NodeName = "other"
			case "missing-sidecar":
				pod.Spec.InitContainers = nil
			case "missing-state":
				pod.Spec.InitContainers[0].VolumeMounts = pod.Spec.InitContainers[0].VolumeMounts[1:]
			case "missing-target":
				pod.Spec.InitContainers[0].VolumeMounts = pod.Spec.InitContainers[0].VolumeMounts[:1]
			case "fake-probe":
				pod.Spec.InitContainers[0].StartupProbe.Exec.Command = []string{"true"}
			case "no-readiness":
				pod.Spec.InitContainers[0].ReadinessProbe = nil
			case "ordinary-init":
				pod.Spec.InitContainers[0].RestartPolicy = nil
			case "missing-startup":
				pod.Spec.InitContainers[0].StartupProbe = nil
			case "missing-uid-env":
				pod.Spec.InitContainers[0].Env = nil
			case "unprivileged":
				pod.Spec.InitContainers[0].SecurityContext.Privileged = ptr.To(false)
			case "wrong-propagation":
				pod.Spec.InitContainers[0].VolumeMounts[1].MountPropagation = ptr.To(core.MountPropagationHostToContainer)
			case "duplicate-pvc":
				mount := pod.Spec.InitContainers[0].VolumeMounts[1]
				mount.MountPath = "/pod-volumes/other/mount"
				pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, mount)
			}
			client := fake.NewSimpleClientset(pod)
			if scenario == "api-failure" {
				client.PrependReactor("get", "pods", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("API unavailable") })
			}
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			_, err := server.NodePublishVolume(context.Background(), fileRequest(t, 0, "ossfs2"))
			want := codes.FailedPrecondition
			if scenario == "api-failure" {
				want = codes.Unavailable
			}
			require.Equal(t, want, status.Code(err), "%v", err)
			require.Len(t, client.Actions(), 1)
			require.NoFileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"))
		})
	}
}

func TestDirectPublishAPICallsScaleWithRequestsOnly(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, count := range []int{1, 10, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			client := fileTestClient(manualFilePod("rund", count))
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			srv := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			for i := range count {
				req := fileRequest(t, i, "ossfs2")
				req.TargetPath = filepath.Join(root, "pods", "pod-a", "volumes", "kubernetes.io~csi", fmt.Sprintf("pv-%d", i), "mount")
				_, err := srv.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
			}
			podGets, pvcGets := fileQueryCounts(t, client)
			require.Equal(t, count, podGets)
			require.Equal(t, count, pvcGets)
			for i := range count {
				req := fileRequest(t, i, "ossfs2")
				req.TargetPath = filepath.Join(root, "pods", "pod-a", "volumes", "kubernetes.io~csi", fmt.Sprintf("pv-%d", i), "mount")
				_, err := srv.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
			}
			podGets, pvcGets = fileQueryCounts(t, client)
			require.Equal(t, 2*count, podGets)
			require.Equal(t, count, pvcGets, "warm republishes must not query claims again")
		})
	}
}

func TestDirectPublishMissingRRSAConsumerReportsConsumerBeforeToken(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod := manualFilePod("rund", 1)
	pod.Spec.InitContainers = nil
	client := fake.NewSimpleClientset(pod)
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	req := fileRequest(t, 0, "ossfs2")
	req.VolumeContext["authType"] = "rrsa"
	_, err := common.WrapNodeServerWithMetricRecorder(ns, "oss", client).NodePublishVolume(context.Background(), req)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "native mountproxy sidecar")
	require.Len(t, client.Actions(), 1)
	require.NoFileExists(t, filepath.Join(root, "pod-a", "manifest.json"))
}

func TestDirectPublishRRSAUsesPodTokenWithoutCredentials(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, scenario := range []string{"valid", "missing-token", "wrong-audience", "subpath", "wrong-sa"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod := manualFilePod("rund", 1)
			pod.Spec.ServiceAccountName = "business"
			req := fileRequest(t, 0, "ossfs2")
			req.Secrets = nil
			req.VolumeContext["authType"] = "rrsa"
			req.VolumeContext["roleArn"] = "acs:ram::1234567890123456:role/test"
			req.VolumeContext["oidcProviderArn"] = "acs:ram::1234567890123456:oidc-provider/test"
			switch scenario {
			case "missing-token":
				pod.Spec.Volumes = pod.Spec.Volumes[:len(pod.Spec.Volumes)-1]
			case "wrong-audience":
				pod.Spec.Volumes[len(pod.Spec.Volumes)-1].Projected.Sources[0].ServiceAccountToken.Audience = "wrong"
			case "subpath":
				pod.Spec.InitContainers[0].VolumeMounts[len(pod.Spec.InitContainers[0].VolumeMounts)-1].SubPath = "token"
			case "wrong-sa":
				req.VolumeContext["serviceAccountName"] = "other"
			}
			client := fileTestClient(pod)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			_, err := server.NodePublishVolume(context.Background(), req)
			if scenario != "valid" {
				require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)
				return
			}
			require.NoError(t, err)
			var d filevolume.Desired
			require.NoError(t, filevolume.ReadJSON(filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"), &d))
			require.Empty(t, d.Mount.Secrets)
			require.Contains(t, d.Mount.Options, "rrsa_token_file="+filevolume.RRSATokenFile)
			require.Contains(t, d.Mount.Options, "rrsa_role_arn="+req.VolumeContext["roleArn"])
			require.Len(t, client.Actions(), 2)
		})
	}
}

func TestGuestSidecarRejectsNonRRSAWithoutPublishingCredentials(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, auth := range []string{"", "sts", "ram", "agent-identity"} {
		t.Run("auth="+auth, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod := manualFilePod("rund", 1)
			client := fileTestClient(pod)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			req := fileRequest(t, 0, "ossfs2")
			req.VolumeContext["authType"] = auth
			_, err := common.WrapNodeServerWithMetricRecorder(ns, "oss", client).NodePublishVolume(context.Background(), req)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "test-sk")
			require.NoFileExists(t, filepath.Join(root, "pod-a", "manifest.json"))
			require.NoFileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"))
		})
	}
}

func TestGuestSidecarIdentifiedByReservedName(t *testing.T) {
	pod := manualFilePod("rund", 1)
	req := fileRequest(t, 0, "ossfs2")
	pod.Spec.InitContainers[0].Name = "mountproxy"
	_, err := filevolume.ManifestForPod(pod, req)
	require.ErrorContains(t, err, filevolume.SidecarName)
	pod.Spec.InitContainers[0].Name = filevolume.SidecarName
	pod.Spec.InitContainers[0].Command = []string{"/custom-entrypoint"}
	pod.Spec.InitContainers[0].Args = []string{"custom", "arguments"}
	_, err = filevolume.ManifestForPod(pod, req)
	require.NoError(t, err, "entrypoint and arguments are not identity checks")
	require.NoError(t, filevolume.ValidateRRSAToken(pod))
	pod.Spec.InitContainers[0].RestartPolicy = nil
	_, err = filevolume.ManifestForPod(pod, req)
	require.ErrorContains(t, err, "restartable init sidecar")
}

func fileTestClient(pod *core.Pod) *fake.Clientset {
	objects := []runtime.Object{pod}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			claim := v.PersistentVolumeClaim.ClaimName
			objects = append(objects, &core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: claim, Namespace: pod.Namespace, UID: types.UID("uid-" + claim)}, Spec: core.PersistentVolumeClaimSpec{VolumeName: v.Name}, Status: core.PersistentVolumeClaimStatus{Phase: core.ClaimBound}})
		}
	}
	return fake.NewSimpleClientset(objects...)
}

func fileQueryCounts(t *testing.T, client *fake.Clientset) (pods, claims int) {
	t.Helper()
	for _, a := range client.Actions() {
		require.Equal(t, "get", a.GetVerb())
		switch a.GetResource().Resource {
		case "pods":
			pods++
		case "persistentvolumeclaims":
			claims++
		default:
			t.Fatalf("unexpected resource query: %s", a.GetResource().Resource)
		}
	}
	return
}
