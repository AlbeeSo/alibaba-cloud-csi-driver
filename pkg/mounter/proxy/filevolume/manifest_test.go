package filevolume

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func manifestFixture(t *testing.T) (*fake.Clientset, *csi.NodePublishVolumeRequest, string) {
	t.Helper()
	pod := &core.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "test", UID: "pod-a"}}
	client := fake.NewSimpleClientset()
	for _, id := range []string{"first", "second", "custom"} {
		driver := OSSDriver
		if id == "custom" {
			driver = CustomDriver
		}
		_, err := client.CoreV1().PersistentVolumeClaims("test").Create(context.Background(), &core.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: id, Namespace: "test", UID: types.UID(id)}, Spec: core.PersistentVolumeClaimSpec{VolumeName: "pv-" + id}}, metav1.CreateOptions{})
		require.NoError(t, err)
		_, err = client.CoreV1().PersistentVolumes().Create(context.Background(), &core.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv-" + id}, Spec: core.PersistentVolumeSpec{ClaimRef: &core.ObjectReference{Namespace: "test", Name: id, UID: types.UID(id)}, PersistentVolumeSource: core.PersistentVolumeSource{CSI: &core.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: id}}}}, metav1.CreateOptions{})
		require.NoError(t, err)
		pod.Spec.Volumes = append(pod.Spec.Volumes, core.Volume{Name: id, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: id}}})
	}
	_, err := client.CoreV1().Pods("test").Create(context.Background(), pod, metav1.CreateOptions{})
	require.NoError(t, err)
	req := &csi.NodePublishVolumeRequest{VolumeId: "first", TargetPath: filepath.Join(t.TempDir(), "pods/pod-a/volumes/kubernetes.io~csi/pv-first/mount"), VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": "app", "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": "pod-a"}}
	return client, req, t.TempDir()
}

func TestManifestInventoryDoesNotDependOnArrivedRequests(t *testing.T) {
	client, req, root := manifestFixture(t)
	require.NoError(t, PublishPodManifest(context.Background(), client, req, OSSDriver, root))
	ids, err := ReadExpected(filepath.Join(root, "pod-a"), "pod-a")
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second"}, ids)
	require.Error(t, CheckReady(filepath.Join(root, "pod-a"), "pod-a", nil, time.Now()), "inventory alone cannot open startup gate")
	require.NoError(t, PublishPodManifest(context.Background(), client, req, OSSDriver, root), "repeated publishes are safe")
	_, err = ReadExpected(filepath.Join(root, "pod-a"), "another-pod")
	require.Error(t, err)
	req.VolumeContext["csi.storage.k8s.io/pod.uid"] = "old-uid"
	require.Error(t, PublishPodManifest(context.Background(), client, req, OSSDriver, root))
}

func TestIncompleteBindingCannotPublishPartialManifest(t *testing.T) {
	client, req, root := manifestFixture(t)
	require.NoError(t, client.CoreV1().PersistentVolumes().Delete(context.Background(), "pv-second", metav1.DeleteOptions{}))
	require.Error(t, PublishPodManifest(context.Background(), client, req, OSSDriver, root))
	require.NoFileExists(t, filepath.Join(root, "pod-a", "manifest.json"))
}

func TestRunnerWaitsForManifestWithoutManualExpectedSet(t *testing.T) {
	r, _ := newRunner(t, "first", "second")
	r.Expected = nil
	request(t, r.Root, "first")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	defer func() { cancel(); require.NoError(t, <-done) }()
	require.Error(t, CheckReady(r.Root, r.PodUID, nil, time.Now()))
	require.NoError(t, WriteJSON(filepath.Join(r.Root, "manifest.json"), Manifest{APIVersion: Version, PodUID: r.PodUID, VolumeIDs: []string{"first", "second"}}))
	require.Eventually(t, func() bool { var s Status; _ = ReadJSON(StatusPath(r.Root, "first"), &s); return s.Phase == "Mounted" }, time.Second, 10*time.Millisecond)
	require.Error(t, CheckReady(r.Root, r.PodUID, nil, time.Now()), "missing second request blocks startup")
	request(t, r.Root, "second")
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, nil, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
}
