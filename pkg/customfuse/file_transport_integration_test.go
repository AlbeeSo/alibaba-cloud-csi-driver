//go:build !windows

package customfuse

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/features"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/require"
	gatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/mount-utils"
)

// Only the kernel/FUSE boundary is simulated. Publishing, JSON serialization,
// dispatcher routing, reconciliation, readiness and CSI unpublish are real.
type integrationFileDriver struct {
	raw      *mount.FakeMounter
	mu       sync.Mutex
	requests []proxy.MountRequest
	exits    map[string]chan error
}

func (*integrationFileDriver) Name() string                            { return "file-integration" }
func (*integrationFileDriver) Fstypes() []string                       { return []string{"customfuse"} }
func (*integrationFileDriver) Init()                                   {}
func (*integrationFileDriver) Terminate()                              {}
func (*integrationFileDriver) ApplyOptionDefaults(o []string) []string { return o }
func (*integrationFileDriver) Mount(context.Context, *proxy.MountRequest, int) error {
	return fmt.Errorf("socket transport must not be called")
}
func (d *integrationFileDriver) MountFile(ctx context.Context, r *proxy.MountRequest) (<-chan error, error) {
	if r.Source == "blocked" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r.Source == "invalid-endpoint" {
		return nil, fmt.Errorf("client rejected secret-value")
	}
	if err := d.raw.Mount(r.Source, r.Target, "fuse.demo", nil); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, *r)
	exit := make(chan error, 1)
	d.exits[r.Target] = exit
	return exit, nil
}

type integrationFileBackend struct {
	server.FileBackend
	raw *mount.FakeMounter
}

// Production FileBackend uses a direct Linux unmount syscall. Replace that one
// syscall here while retaining its real driver dispatch and mount-table checks.
func (b integrationFileBackend) Unmount(target string) error { return b.raw.Unmount(target) }

type fileFixture struct {
	t              *testing.T
	state, kubelet string
	node           *nodeServer
	driver         *integrationFileDriver
	backend        integrationFileBackend
}

func newFileFixture(t *testing.T) *fileFixture {
	t.Helper()
	gatetesting.SetFeatureGateDuringTest(t, features.FunctionalMutableFeatureGate, features.RundCSIProtocol3, true)
	raw := mount.NewFakeMounter(nil)
	d := &integrationFileDriver{raw: raw, exits: map[string]chan error{}}
	server.RegisterDriver(d)
	server.Init([]string{d.Name()})
	f := &fileFixture{t: t, state: t.TempDir(), kubelet: t.TempDir(), driver: d, node: &nodeServer{locks: utils.NewVolumeLocks(), rawMounter: raw, clientset: fileTestClient()}, backend: integrationFileBackend{FileBackend: server.FileBackend{Mounter: raw}, raw: raw}}
	t.Setenv("CSI_FUSE_FILE_ROOT", f.state)
	return f
}

func (f *fileFixture) publish(uid, id, source string) *csi.NodePublishVolumeRequest {
	f.t.Helper()
	req := &csi.NodePublishVolumeRequest{VolumeId: id, TargetPath: filepath.Join(f.kubelet, "pods", uid, "volumes", "kubernetes.io~csi", id, "mount"),
		VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": uid, "csi.storage.k8s.io/pod.namespace": "test", "csi.storage.k8s.io/pod.uid": uid, "source": source}, Readonly: true, Secrets: map[string]string{"password": "secret-value"},
		VolumeCapability: &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY}}}
	_, err := f.node.NodePublishVolume(context.Background(), req)
	require.NoError(f.t, err)
	return req
}

func (f *fileFixture) run(uid string, ids ...string) func() {
	f.t.Helper()
	r := &filevolume.Runner{Root: filepath.Join(f.state, uid), PodUID: uid, TargetRoot: filepath.Join(f.kubelet, "pods", uid, "volumes", "kubernetes.io~csi"), Expected: ids, Timeout: 150 * time.Millisecond, Backend: f.backend}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				require.NoError(f.t, err)
			case <-time.After(5 * time.Second):
				f.t.Error("file runner did not stop")
			}
		})
	}
	f.t.Cleanup(stop)
	return stop
}

func (f *fileFixture) ready(uid string, ids ...string) bool {
	return filevolume.CheckReady(filepath.Join(f.state, uid), uid, ids, time.Now()) == nil
}
func (f *fileFixture) status(uid, id, phase string) bool {
	var s filevolume.Status
	_ = filevolume.ReadJSON(filevolume.StatusPath(filepath.Join(f.state, uid), id), &s)
	return s.Phase == phase
}

func TestFileTransportIntegrationMultiPodLifecycle(t *testing.T) {
	f := newFileFixture(t)
	first := f.publish("pod-a", "shared", "first-source")
	stopA := f.run("pod-a", "shared", "second")
	require.Eventually(t, func() bool { return f.status("pod-a", "shared", "Mounted") }, 3*time.Second, 10*time.Millisecond)
	require.False(t, f.ready("pod-a", "shared", "second"), "a missing second publish must keep the application blocked")
	f.publish("pod-a", "second", "second-source")
	require.Eventually(t, func() bool { return f.ready("pod-a", "shared", "second") }, 3*time.Second, 10*time.Millisecond)
	other := f.publish("pod-b", "shared", "other-pod-source")
	stopB := f.run("pod-b", "shared")
	require.Eventually(t, func() bool { return f.ready("pod-b", "shared") }, 3*time.Second, 10*time.Millisecond)
	// Repeated CSI publish must not rewrite generation or trigger another mount.
	_, err := f.node.NodePublishVolume(context.Background(), first)
	require.NoError(t, err)
	f.driver.mu.Lock()
	requests := append([]proxy.MountRequest(nil), f.driver.requests...)
	f.driver.mu.Unlock()
	require.Len(t, requests, 3)
	for _, r := range requests {
		require.Contains(t, r.Options, "readOnly=true")
		require.Equal(t, "secret-value", r.Secrets["password"])
		require.True(t, filepath.IsAbs(r.Target))
	}
	_, err = f.node.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: first.VolumeId, TargetPath: first.TargetPath})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return f.status("pod-a", "shared", "Unmounted") }, 3*time.Second, 10*time.Millisecond)
	require.True(t, f.ready("pod-b", "shared"), "unpublishing one Pod must not affect another Pod sharing the PV")
	require.FileExists(t, filevolume.RequestPath(filepath.Join(f.state, "pod-b"), "shared"))
	stopA()
	stopB()
	mounted, err := f.backend.Mounted(other.TargetPath)
	require.NoError(t, err)
	require.False(t, mounted)
}

func TestFileTransportIntegrationFailuresAndRestart(t *testing.T) {
	f := newFileFixture(t)
	f.publish("pod-a", "good", "valid")
	f.publish("pod-a", "bad", "invalid-endpoint")
	f.publish("pod-a", "slow", "blocked")
	stop := f.run("pod-a", "good", "bad", "slow")
	require.Eventually(t, func() bool {
		return f.status("pod-a", "good", "Mounted") && f.status("pod-a", "bad", "Failed") && f.status("pod-a", "slow", "Failed")
	}, 3*time.Second, 10*time.Millisecond)
	require.False(t, f.ready("pod-a", "good", "bad", "slow"))
	var bad, slow filevolume.Status
	require.NoError(t, filevolume.ReadJSON(filevolume.StatusPath(filepath.Join(f.state, "pod-a"), "bad"), &bad))
	require.Contains(t, bad.Error, "client rejected")
	require.NotNil(t, bad.Result)
	require.Equal(t, bad.Error, bad.Result.Error)
	require.NotContains(t, bad.Error, "secret-value")
	require.NoError(t, filevolume.ReadJSON(filevolume.StatusPath(filepath.Join(f.state, "pod-a"), "slow"), &slow))
	require.Contains(t, slow.Error, "deadline exceeded")
	stop()
	// Seed a fresh-looking success from a previous process. A restarted runner
	// must create its own session and await a new driver result.
	var d filevolume.Desired
	require.NoError(t, filevolume.ReadJSON(filevolume.RequestPath(filepath.Join(f.state, "pod-a"), "slow"), &d))
	old := filevolume.Status{APIVersion: filevolume.Version, PodUID: "pod-a", VolumeID: "slow", Session: slow.Session, ObservedGeneration: d.Generation, Phase: "Mounted", UpdatedAt: time.Now()}
	require.NoError(t, filevolume.WriteJSON(filevolume.StatusPath(filepath.Join(f.state, "pod-a"), "slow"), old))
	stopAgain := f.run("pod-a", "slow")
	require.Eventually(t, func() bool {
		var session string
		_ = filevolume.ReadJSON(filepath.Join(f.state, "pod-a", "session.json"), &session)
		return session != "" && session != old.Session
	}, 3*time.Second, 10*time.Millisecond)
	require.False(t, f.ready("pod-a", "slow"))
	stopAgain()
}

func TestFileTransportIntegrationProcessExit(t *testing.T) {
	f := newFileFixture(t)
	req := f.publish("pod-a", "one", "valid")
	stop := f.run("pod-a", "one")
	require.Eventually(t, func() bool { return f.ready("pod-a", "one") }, 3*time.Second, 10*time.Millisecond)
	f.driver.mu.Lock()
	exit := f.driver.exits[req.TargetPath]
	f.driver.mu.Unlock()
	exit <- fmt.Errorf("daemon exited: secret-value")
	require.Eventually(t, func() bool { return f.status("pod-a", "one", "Failed") }, 3*time.Second, 10*time.Millisecond)
	require.False(t, f.ready("pod-a", "one"))
	stop()
	var s filevolume.Status
	require.NoError(t, filevolume.ReadJSON(filevolume.StatusPath(filepath.Join(f.state, "pod-a"), "one"), &s))
	require.Contains(t, s.Error, "daemon exited")
	require.NotContains(t, s.Error, "secret-value")
	require.NotNil(t, s.Result)
	require.NoError(t, s.Result.ToError(), "process exit must not rewrite the original successful mount response")
	require.NotNil(t, s.UnmountResult)
	require.NoError(t, s.UnmountResult.ToError())
}
