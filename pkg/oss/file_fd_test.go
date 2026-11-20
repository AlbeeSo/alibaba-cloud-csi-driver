//go:build linux

package oss

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
	core "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
	"k8s.io/utils/ptr"
)

// Actual NodePublish -> real files -> runner/probe -> SCM_RIGHTS -> cleanup.
// Only the privileged kernel mount is replaced by a pipe-backed connection.
func TestNativeSidecarFileFDIntegrationOnDegradedNode(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod := manualFilePod("runc", 2)
	pod.Spec.RuntimeClassName = nil
	pod.Spec.InitContainers[0].SecurityContext.Privileged = ptr.To(false)
	for i := range pod.Spec.InitContainers[0].VolumeMounts {
		vm := &pod.Spec.InitContainers[0].VolumeMounts[i]
		if vm.Name == "pv-0" || vm.Name == "pv-1" {
			vm.MountPropagation = ptr.To(core.MountPropagationHostToContainer)
		}
	}
	previous := prepareSidecarFD
	t.Cleanup(func() { prepareSidecarFD = previous })
	var prepared atomic.Int32
	prepareSidecarFD = func(_ context.Context, request *proxy.MountRequest, _ mount.Interface) (int, error) {
		prepared.Add(1)
		reader, writer, err := os.Pipe()
		if err != nil {
			return -1, err
		}
		defer func() { _ = reader.Close(); _ = writer.Close() }()
		if _, err := writer.Write([]byte("existing-fd")); err != nil {
			return -1, err
		}
		return unix.Dup(int(reader.Fd()))
	}
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	ns.clientset = fileTestClient(pod)
	ns.kernelSupportsRecovery = false
	server := common.WrapNodeServerWithMetricRecorder(ns, "oss", ns.clientset)
	targetRoot := filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi")
	for i := range 2 {
		req := fileRequest(t, i, "ossfs2")
		req.TargetPath = filepath.Join(targetRoot, fmt.Sprintf("pv-%d", i), "mount")
		req.VolumeContext["runtimeclass"] = "runc"
		req.VolumeContext[filevolume.MountModeKey] = "sidecar"
		req.VolumeContext["recovery"] = "true"
		t.Cleanup(func() { filevolume.NativeFDs.Close(req.TargetPath) })
		for range 2 {
			_, err := server.NodePublishVolume(context.Background(), req)
			require.NoError(t, err)
		}
		var desired filevolume.Desired
		require.NoError(t, filevolume.ReadJSON(filevolume.RequestPath(filepath.Join(root, "pod-a"), fmt.Sprintf("pv-%d", i)), &desired))
		require.True(t, desired.Mount.FdPassing)
		require.False(t, desired.Mount.Recovery, "kernel downgrade must not disable fd-passing")
		require.Empty(t, desired.Mount.Secrets)
		degraded, err := os.ReadFile(filepath.Join(root, "pod-a", "metrics", fmt.Sprintf("pv-%d", i), utils.MetricsRecoveryDegraded))
		require.NoError(t, err)
		require.Equal(t, "1", string(degraded))
	}
	require.Zero(t, prepared.Load(), "kernel mounts must wait until the consumer has started")
	backend := &handoffBackend{root: filepath.Join(root, "pod-a"), fds: map[string]int{}}
	runner := &filevolume.Runner{Root: backend.root, PodUID: "pod-a", TargetRoot: targetRoot, Timeout: time.Second, Backend: backend}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	require.Eventually(t, func() bool { return filevolume.CheckReady(backend.root, "pod-a", nil, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	require.Equal(t, int32(2), prepared.Load(), "republish must not create another connection")
	for i := range 2 {
		var state filevolume.Status
		require.NoError(t, filevolume.ReadJSON(filevolume.StatusPath(backend.root, fmt.Sprintf("pv-%d", i)), &state))
		require.Equal(t, "Released", state.Phase)
	}
}

func TestNativeSidecarSecretProjectionNeverSerializesCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod := manualFilePod("runc", 1)
	pod.Spec.InitContainers[0].SecurityContext.Privileged = ptr.To(false)
	for i := range pod.Spec.InitContainers[0].VolumeMounts {
		vm := &pod.Spec.InitContainers[0].VolumeMounts[i]
		if vm.Name == "pv-0" {
			vm.MountPropagation = ptr.To(core.MountPropagationHostToContainer)
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: "credentials", VolumeSource: core.VolumeSource{Secret: &core.SecretVolumeSource{SecretName: "credentials"}}})
	pod.Spec.InitContainers[0].VolumeMounts = append(pod.Spec.InitContainers[0].VolumeMounts, core.VolumeMount{Name: "credentials", MountPath: filevolume.SecretRoot + "/credentials", ReadOnly: true})
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	ns.clientset = fileTestClient(pod)
	req := fileRequest(t, 0, "ossfs2")
	req.TargetPath = filepath.Join(t.TempDir(), "pods", "pod-a", "volumes", "kubernetes.io~csi", "pv-0", "mount")
	req.VolumeContext["runtimeclass"] = "runc"
	req.VolumeContext["authType"] = ""
	req.VolumeContext["secretRef"] = "credentials"
	req.Secrets = nil // SecretRef projects credentials directly; CSI carries none.
	previous := prepareSidecarFD
	prepareSidecarFD = func(context.Context, *proxy.MountRequest, mount.Interface) (int, error) {
		return unix.Open("/dev/null", unix.O_RDONLY, 0)
	}
	t.Cleanup(func() { prepareSidecarFD = previous; filevolume.NativeFDs.Close(req.TargetPath) })
	_, err := common.WrapNodeServerWithMetricRecorder(ns, "oss", ns.clientset).NodePublishVolume(context.Background(), req)
	require.NoError(t, err)
	raw, err := os.ReadFile(filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"))
	require.NoError(t, err)
	var desired filevolume.Desired
	require.NoError(t, filevolume.ReadJSON(filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"), &desired))
	require.Empty(t, desired.Mount.Secrets)
	require.Contains(t, string(raw), "oss_sts_multi_conf_ak_file=/volume-secrets/credentials/AccessKeyId")
	require.NotContains(t, string(raw), "/etc/ossfs2/passwd-ossfs2")
	for _, action := range ns.clientset.(*fake.Clientset).Actions() {
		require.NotEqual(t, "secrets", action.GetResource().Resource)
		require.NotEqual(t, "persistentvolumeclaims", action.GetResource().Resource)
	}
}

type handoffBackend struct {
	mu   sync.Mutex
	root string
	fds  map[string]int
}

func (b *handoffBackend) Mounted(string) (bool, error) { return true, nil }
func (b *handoffBackend) Mount(_ context.Context, req *proxy.MountRequest) (<-chan error, error) {
	key := filepath.Base(filepath.Dir(req.Target))
	var desired filevolume.Desired
	if err := filevolume.ReadJSON(filevolume.RequestPath(b.root, key), &desired); err != nil {
		return nil, err
	}
	fd, err := filevolume.ReceiveFD(b.root, key, desired.Generation, time.Second)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 32)
	n, err := unix.Read(fd, buf)
	if err != nil || string(buf[:n]) != "existing-fd" {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrong connection payload")
	}
	b.mu.Lock()
	b.fds[req.Target] = fd
	b.mu.Unlock()
	return make(chan error), nil
}
func (b *handoffBackend) Unmount(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if fd, ok := b.fds[target]; ok {
		_ = unix.Close(fd)
		delete(b.fds, target)
	}
	return nil
}
