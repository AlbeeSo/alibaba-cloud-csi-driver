//go:build !windows

package oss

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ktesting "k8s.io/client-go/testing"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
)

func TestDirectPublishRejectsSwappedPVCMappingBeforeSubmission(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	root := t.TempDir()
	t.Setenv("CSI_FUSE_FILE_ROOT", root)
	pod := manualFilePod("rund", 2)
	client := fileTestClient(pod)
	pod.Spec.InitContainers[0].VolumeMounts[1].Name = "pv-1"
	pod.Spec.InitContainers[0].VolumeMounts[2].Name = "pv-0"
	require.NoError(t, client.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, pod, pod.Namespace))
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	ns.clientset = client
	server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
	req := fileRequest(t, 0, "ossfs2")
	setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
	_, err := server.NodePublishVolume(context.Background(), req)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.ErrorContains(t, err, "conflicts")
	require.NoFileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"))
	require.NoFileExists(t, filepath.Join(root, "pod-a", "manifest.json"))
}

func TestDirectPublishBindingFailureIsNotCached(t *testing.T) {
	pod := manualFilePod("rund", 1)
	prependUnrelatedPVC(pod)
	client := fileTestClient(pod)
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	ns.clientset = client
	req := fileRequest(t, 0, "ossfs2")
	manifest, err := filevolume.ManifestForPod(pod, req)
	require.NoError(t, err)
	fail := true
	client.PrependReactor("get", "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) {
		if fail {
			return true, nil, errors.New("temporary API outage")
		}
		return false, nil, nil
	})
	require.Equal(t, codes.Unavailable, status.Code(ns.validateFileBinding(context.Background(), pod, req, manifest)))
	fail = false
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, manifest))
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, manifest))
	require.Len(t, client.Actions(), 2)
	pod.UID = "replacement"
	manifest.PodUID = "replacement"
	require.NoError(t, ns.validateFileBinding(context.Background(), pod, req, manifest))
	require.Len(t, client.Actions(), 3, "new Pod UID must not reuse the old binding cache")
}

func TestDirectPublishConcurrentBindingLookupIsCoalesced(t *testing.T) {
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, false)
	t.Setenv("CSI_FUSE_FILE_ROOT", t.TempDir())
	pod := manualFilePod("rund", 1)
	prependUnrelatedPVC(pod)
	client := fileTestClient(pod)
	ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
	ns.clientset = client
	server := common.WrapNodeServerWithMetricRecorder(ns, "oss", client)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	req := fileRequest(t, 0, "ossfs2")
	for range 32 {
		wg.Go(func() {
			_, err := server.NodePublishVolume(context.Background(), req)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.Contains(t, []codes.Code{codes.OK, codes.Aborted}, status.Code(err))
	}
	_, claims := fileQueryCounts(t, client)
	require.Equal(t, 1, claims)
}

// Exercise the exceptional binding fallback without putting a managed PVC
// before the consumer (which would be rejected by the startup-order guard).
func prependUnrelatedPVC(pod *core.Pod) {
	pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: "prep", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: "unrelated"}}})
	pod.Spec.InitContainers = append([]core.Container{{Name: "prepare", VolumeMounts: []core.VolumeMount{{Name: "prep", MountPath: "/prepare"}}}}, pod.Spec.InitContainers...)
}
