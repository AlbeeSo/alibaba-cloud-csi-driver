//go:build !windows

package oss

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
)

// Exercise RPC routing -> metadata validation -> real control files -> consumer
// workers/probes/drain. Only the final FUSE syscall/process is simulated here;
// subprocess and syscall behavior have separate server integration tests.
func TestDirectPublishMetadataFileConsumerIntegration(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixedFallback=%t", mixed), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod := manualFilePod("rund", 2)
			if mixed {
				prependUnrelatedPVC(pod)
			}
			client := fileTestClient(pod)
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			for i, fuse := range []string{"ossfs", "ossfs2"} {
				req := fileRequest(t, i, fuse)
				if !mixed || i == 0 {
					setFileBindingMetadata(req, pod.Namespace, fmt.Sprintf("pv-%d", i), fmt.Sprintf("claim-%d", i))
				}
				req.Readonly = i == 1
				// Model the provisioner -> CreateVolume -> PV VolumeContext
				// propagation, not just manually injected NodePublish fields.
				created, err := (&controllerServer{}).CreateVolume(context.Background(), &csi.CreateVolumeRequest{Name: req.VolumeId, Parameters: req.VolumeContext})
				require.NoError(t, err)
				req.VolumeContext = created.Volume.VolumeContext
				_, err = server.NodePublishVolume(context.Background(), req)
				require.NoError(t, err)
			}
			backend := &metadataIntegrationBackend{requests: map[string]proxy.MountRequest{}}
			state := filepath.Join(root, "pod-a")
			target := t.TempDir()
			for _, key := range []string{"pv-0", "pv-1"} {
				// Kubelet/runtime supplies each PVC projection before the
				// consumer starts. The fake backend does not create it.
				require.NoError(t, os.MkdirAll(filepath.Join(target, key, "mount"), 0755))
			}
			runner := &filevolume.Runner{Root: state, PodUID: "pod-a", TargetRoot: target, Timeout: time.Second, Backend: backend}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx) }()
			require.Eventually(t, func() bool { return filevolume.CheckReady(state, "pod-a", nil, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
			backend.mu.Lock()
			first := backend.requests[filepath.Join(target, "pv-0", "mount")]
			second := backend.requests[filepath.Join(target, "pv-1", "mount")]
			backend.mu.Unlock()
			require.Equal(t, "handle-0", first.VolumeID)
			require.Equal(t, "ossfs", first.Fstype)
			require.Equal(t, "handle-1", second.VolumeID)
			require.Equal(t, "ossfs2", second.Fstype)
			require.Empty(t, first.Secrets)
			require.Empty(t, second.Secrets)
			require.Contains(t, second.Options, "ro=true")
			cancel()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(3 * time.Second):
				t.Fatal("file consumer did not drain")
			}
			for _, key := range []string{"pv-0", "pv-1"} {
				var result filevolume.Status
				require.NoError(t, filevolume.ReadJSON(filevolume.StatusPath(state, key), &result))
				require.Equal(t, "Unmounted", result.Phase)
			}
			_, claims := fileQueryCounts(t, client)
			if mixed {
				require.Equal(t, 1, claims)
			} else {
				require.Zero(t, claims)
			}
		})
	}
}

type metadataIntegrationBackend struct {
	mu       sync.Mutex
	requests map[string]proxy.MountRequest
}

func (b *metadataIntegrationBackend) Mount(_ context.Context, req *proxy.MountRequest) (<-chan error, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.requests[req.Target] = *req
	return make(chan error), nil
}

func (b *metadataIntegrationBackend) Mounted(target string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.requests[target]
	return ok, nil
}

func (b *metadataIntegrationBackend) Unmount(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.requests, target)
	return nil
}
