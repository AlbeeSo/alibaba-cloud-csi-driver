package filevolume

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/stretchr/testify/require"
)

type fakeBackend struct {
	mu          sync.Mutex
	mounts      map[string]bool
	exits       map[string]chan error
	fail        string
	blocked     string
	failUnmount string
	calls       int
}

func (b *fakeBackend) Mount(ctx context.Context, req *proxy.MountRequest) (<-chan error, error) {
	if req.VolumeID == b.blocked {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if req.VolumeID == b.fail {
		return nil, errors.New("client failed secret-password")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.mounts[req.Target] = true
	c := make(chan error, 1)
	b.exits[req.VolumeID] = c
	return c, nil
}
func (b *fakeBackend) Mounted(target string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mounts[target], nil
}
func (b *fakeBackend) Unmount(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if filepath.Base(filepath.Dir(target)) == b.failUnmount {
		return errors.New("device busy")
	}
	delete(b.mounts, target)
	return nil
}

func (b *fakeBackend) Health(string) (string, error) { return "Mounted", nil }

func TestRestartAfterStartupRequiresPodRecreation(t *testing.T) {
	r, b := newRunner(t, "one")
	request(t, r.Root, "one")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	var before Status
	require.NoError(t, ReadJSON(StatusPath(r.Root, "one"), &before))
	require.Equal(t, "Unmounted", before.Phase)
	require.ErrorContains(t, r.Run(context.Background()), "Pod recreation required")
	require.Error(t, CheckReady(r.Root, r.PodUID, r.Expected, time.Now()))
	var after Status
	require.NoError(t, ReadJSON(StatusPath(r.Root, "one"), &after))
	require.Equal(t, before, after, "rejected restart must preserve completed cleanup evidence")
	var session string
	require.NoError(t, ReadJSON(filepath.Join(r.Root, "session.json"), &session))
	require.Equal(t, before.Session, session)
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Equal(t, 1, b.calls, "never remount behind an application's stale bind mount")
}

func TestRestartBarrierRejectsOldMountedState(t *testing.T) {
	r, b := newRunner(t, "one")
	d := request(t, r.Root, "one")
	require.NoError(t, WriteJSON(filepath.Join(r.Root, "session.json"), "old"))
	require.NoError(t, markReady(r.Root, r.PodUID, "old"))
	require.NoError(t, WriteJSON(StatusPath(r.Root, "one"), Status{APIVersion: Version, PodUID: r.PodUID, VolumeID: "one", ObservedGeneration: d.Generation, Session: "old", Phase: "Mounted", UpdatedAt: time.Now()}))
	require.ErrorContains(t, r.Run(context.Background()), "Pod recreation required")
	require.ErrorContains(t, CheckReady(r.Root, r.PodUID, r.Expected, time.Now()), "PodRecreationRequired")
	require.Zero(t, b.calls)
}

func TestBusyUnmountDoesNotCancelAnotherVolume(t *testing.T) {
	r, b := newRunner(t, "first", "second")
	d := request(t, r.Root, "first")
	request(t, r.Root, "second")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	b.mu.Lock()
	b.failUnmount = "first"
	b.mu.Unlock()
	require.NoError(t, WriteJSON(UnmountPath(r.Root, "first"), newUnmount(d)))
	require.Eventually(t, func() bool {
		var s Status
		_ = ReadJSON(StatusPath(r.Root, "first"), &s)
		return s.Phase == "CleanupPending"
	}, 3*time.Second, 10*time.Millisecond)
	b.mu.Lock()
	require.True(t, b.mounts[filepath.Join(r.TargetRoot, "second", "mount")])
	b.failUnmount = ""
	b.mu.Unlock()
	require.Eventually(t, func() bool {
		var s Status
		_ = ReadJSON(StatusPath(r.Root, "first"), &s)
		return s.Phase == "Unmounted"
	}, 3*time.Second, 10*time.Millisecond)
	var s Status
	require.NoError(t, ReadJSON(StatusPath(r.Root, "second"), &s))
	require.Equal(t, "Mounted", s.Phase)
	cancel()
	require.NoError(t, <-done)
}

func request(t *testing.T, root, id string) Desired {
	t.Helper()
	d := Desired{APIVersion: Version, PodUID: "pod-a", Mount: proxy.MountRequest{VolumeID: id, Target: id + "/mount", Fstype: "customfuse", Secrets: map[string]string{"password": "secret-password"}}}
	d.Generation = d.Digest()
	require.NoError(t, WriteJSON(RequestPath(root, id), d))
	return d
}

func newRunner(t *testing.T, ids ...string) (*Runner, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{mounts: map[string]bool{}, exits: map[string]chan error{}}
	r := &Runner{Root: t.TempDir(), PodUID: "pod-a", TargetRoot: t.TempDir(), Expected: ids, Timeout: 3 * time.Second, Backend: b}
	for _, id := range ids {
		require.NoError(t, os.MkdirAll(filepath.Join(r.TargetRoot, id, "mount"), 0755))
	}
	return r, b
}

func TestMultiVolumeGateAndProcessExit(t *testing.T) {
	r, b := newRunner(t, "first", "second")
	request(t, r.Root, "first")
	request(t, r.Root, "not-expected") // Never execute unlisted files.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { var s Status; _ = ReadJSON(StatusPath(r.Root, "first"), &s); return s.Phase == "Mounted" }, 3*time.Second, 10*time.Millisecond)
	require.Error(t, CheckReady(r.Root, r.PodUID, r.Expected, time.Now()))
	request(t, r.Root, "second")
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	b.mu.Lock()
	exit := b.exits["first"]
	b.mu.Unlock()
	exit <- errors.New("lost daemon")
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) != nil }, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Empty(t, b.mounts)
	require.Equal(t, 2, b.calls)
}

func TestBlockedVolumeDoesNotHideOtherResult(t *testing.T) {
	r, b := newRunner(t, "blocked", "failed")
	b.blocked, b.fail = "blocked", "failed"
	request(t, r.Root, "blocked")
	request(t, r.Root, "failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { var s Status; _ = ReadJSON(StatusPath(r.Root, "failed"), &s); return s.Phase == "Failed" }, 2*time.Second, 10*time.Millisecond)
	var s Status
	require.NoError(t, ReadJSON(StatusPath(r.Root, "failed"), &s))
	require.NotContains(t, s.Error, "secret-password")
	cancel()
	require.NoError(t, <-done)
}

func TestRejectForeignPodAndTraversal(t *testing.T) {
	root := t.TempDir()
	d := request(t, root, "one")
	require.Error(t, d.Validate("pod-b"))
	d.Mount.Target = "../mount"
	d.Generation = d.Digest()
	require.Error(t, d.Validate("pod-a"))
	d.Mount.Target = "/outside/mount"
	d.Generation = d.Digest()
	require.Error(t, d.Validate("pod-a"))
}

func TestReadinessRejectsStaleGenerationAndSession(t *testing.T) {
	root := t.TempDir()
	d := request(t, root, "one")
	now := time.Now()
	require.NoError(t, WriteJSON(filepath.Join(root, "session.json"), "new"))
	s := Status{APIVersion: Version, PodUID: "pod-a", VolumeID: "one", ObservedGeneration: d.Generation, Session: "old", Phase: "Mounted", UpdatedAt: now}
	require.NoError(t, WriteJSON(StatusPath(root, "one"), s))
	require.Error(t, CheckReady(root, "pod-a", []string{"one"}, now))
	s.Session = "new"
	s.UpdatedAt = now.Add(-11 * time.Second)
	require.NoError(t, WriteJSON(StatusPath(root, "one"), s))
	require.Error(t, CheckReady(root, "pod-a", []string{"one"}, now))
	s.UpdatedAt = now
	s.ObservedGeneration = "old"
	require.NoError(t, WriteJSON(StatusPath(root, "one"), s))
	require.Error(t, CheckReady(root, "pod-a", []string{"one"}, now))
	require.Error(t, CheckReady(root, "pod-a", nil, now))
}

func TestReadinessRequiresSuccessfulMountResponse(t *testing.T) {
	root := t.TempDir()
	d := request(t, root, "one")
	now := time.Now()
	require.NoError(t, WriteJSON(filepath.Join(root, "session.json"), "session"))
	s := Status{APIVersion: Version, PodUID: "pod-a", VolumeID: "one", ObservedGeneration: d.Generation, Session: "session", Phase: "Mounted", UpdatedAt: now}
	for _, result := range []*proxy.Response{nil, {Error: "mount failed"}} {
		s.Result = result
		require.NoError(t, WriteJSON(StatusPath(root, "one"), s))
		require.Error(t, CheckReady(root, "pod-a", []string{"one"}, now))
	}
	s.Result = &proxy.Response{}
	require.NoError(t, WriteJSON(StatusPath(root, "one"), s))
	require.NoError(t, CheckReady(root, "pod-a", []string{"one"}, now))
}

func TestMissingRequestDoesNotUnmountAndExplicitCommandDoes(t *testing.T) {
	r, b := newRunner(t, "one")
	d := request(t, r.Root, "one")
	// A healthy recovery supervisor must not hide missing control state.
	d.Mount.Fstype, d.Mount.FdPassing, d.Mount.Recovery = "ossfs2", true, true
	d.RequiredFeatures = []string{FeatureFuseFD}
	d.Generation = d.Digest()
	require.NoError(t, WriteJSON(RequestPath(r.Root, "one"), d))
	b.mounts[filepath.Join(r.TargetRoot, "one", "mount")] = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, os.Remove(RequestPath(r.Root, "one")))
	require.Eventually(t, func() bool {
		var s Status
		_ = ReadJSON(StatusPath(r.Root, "one"), &s)
		return s.Phase == "RequestMissing"
	}, 3*time.Second, 10*time.Millisecond)
	b.mu.Lock()
	require.True(t, b.mounts[filepath.Join(r.TargetRoot, "one", "mount")])
	b.mu.Unlock()
	require.Error(t, CheckReady(r.Root, r.PodUID, r.Expected, time.Now()))
	require.NoError(t, WriteJSON(RequestPath(r.Root, "one"), d))
	require.Eventually(t, func() bool { return CheckReady(r.Root, r.PodUID, r.Expected, time.Now()) == nil }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, WriteJSON(UnmountPath(r.Root, "one"), newUnmount(d)))
	require.Eventually(t, func() bool { var s Status; _ = ReadJSON(StatusPath(r.Root, "one"), &s); return s.Phase == "Released" }, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
	b.mu.Lock()
	defer b.mu.Unlock()
	require.Empty(t, b.mounts)
	require.Equal(t, 1, b.calls, "restoring desired must not remount")
}
