//go:build !windows

package oss

import (
	"context"
	"fmt"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
)

func setFileBindingMetadata(req *csi.NodePublishVolumeRequest, namespace, pv, claim string) {
	if req.VolumeContext == nil {
		req.VolumeContext = map[string]string{}
	}
	req.VolumeContext[common.PVNameKey] = pv
	req.VolumeContext[common.PVCNamespaceKey] = namespace
	req.VolumeContext[common.PVCNameKey] = claim
}

func TestDirectPublishMetadataSkipsPVCAPI(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	for _, count := range []int{1, 10, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			pod := manualFilePod("rund", count)
			client := fileTestClient(pod)
			client.PrependReactor("get", "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) {
				t.Error("metadata fast path must not GET PVCs")
				return true, nil, fmt.Errorf("PVC access intentionally unavailable")
			})
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			ns.clientset = client
			server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
			for i := range count {
				req := fileRequest(t, i, "ossfs2")
				setFileBindingMetadata(req, pod.Namespace, fmt.Sprintf("pv-%d", i), fmt.Sprintf("claim-%d", i))
				for range 2 {
					_, err := server.NodePublishVolume(context.Background(), req)
					require.NoError(t, err)
				}
			}
			pods, claims := fileQueryCounts(t, client)
			require.Equal(t, 2*count, pods, "reuse the existing Pod lookup per RPC")
			require.Zero(t, claims, "cold and repeated publishes must both avoid PVC GET")
		})
	}
}

func TestDirectPublishMetadataRequiresNoKubernetesClient(t *testing.T) {
	pod := manualFilePod("rund", 1)
	req := fileRequest(t, 0, "ossfs2")
	setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
	m, err := filevolume.ManifestForPod(pod, req)
	require.NoError(t, err)
	ns := &nodeServer{}
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, m))
	require.Nil(t, ns.fileBindings.values, "metadata must not initialize or populate the fallback cache")
}

func TestDirectPublishIncompleteMetadataUsesFallback(t *testing.T) {
	fields := []string{common.PVNameKey, common.PVCNamespaceKey, common.PVCNameKey}
	for mask := range 7 { // Every incomplete subset of the three fields.
		t.Run(fmt.Sprint(mask), func(t *testing.T) {
			pod := manualFilePod("rund", 1)
			client := fileTestClient(pod)
			ns := &nodeServer{clientset: client}
			req := fileRequest(t, 0, "ossfs2")
			setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
			for i, name := range fields {
				if mask&(1<<i) == 0 {
					delete(req.VolumeContext, name)
				}
			}
			m, err := filevolume.ManifestForPod(pod, req)
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, m))
			}
			require.Len(t, client.Actions(), 1, "incomplete metadata falls back once, then uses the binding cache")
			require.Equal(t, "persistentvolumeclaims", client.Actions()[0].GetResource().Resource)
		})
	}
}

func TestDirectPublishConflictingMetadataDoesNotFallback(t *testing.T) {
	for _, field := range []string{common.PVNameKey, common.PVCNamespaceKey, common.PVCNameKey} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", field, partial), func(t *testing.T) {
				pod := manualFilePod("rund", 1)
				client := fileTestClient(pod)
				ns := &nodeServer{clientset: client}
				req := fileRequest(t, 0, "ossfs2")
				m, err := filevolume.ManifestForPod(pod, req)
				require.NoError(t, err)
				require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, m)) // warm authoritative fallback
				client.ClearActions()
				if !partial {
					setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
				}
				req.VolumeContext[field] = "wrong-value"
				err = ns.validateFileBinding(context.Background(), pod, req, m)
				require.Equal(t, codes.FailedPrecondition, status.Code(err))
				require.ErrorContains(t, err, field)
				require.ErrorContains(t, err, "conflicts")
				require.Empty(t, client.Actions(), "neither a warm cache nor a new GET may hide conflicting metadata")
			})
		}
	}
}

func TestDirectPublishMetadataDoesNotSeedFallbackCache(t *testing.T) {
	pod := manualFilePod("rund", 1)
	client := fileTestClient(pod)
	ns := &nodeServer{clientset: client}
	req := fileRequest(t, 0, "ossfs2")
	m, err := filevolume.ManifestForPod(pod, req)
	require.NoError(t, err)
	setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, m))
	delete(req.VolumeContext, common.PVCNameKey)
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, m))
	require.Len(t, client.Actions(), 1, "declarative metadata must not masquerade as a live API binding when a later request lacks metadata")
}
