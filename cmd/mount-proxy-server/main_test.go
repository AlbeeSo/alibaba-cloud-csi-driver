package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"github.com/stretchr/testify/require"
)

func TestFileLifecycleShutdownBudget(t *testing.T) {
	for _, blocked := range []string{"drain", "terminate", "neither"} {
		t.Run(blocked, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			defer close(release)
			run := func(ctx context.Context) error {
				<-ctx.Done()
				if blocked == "drain" {
					<-release
				}
				return nil
			}
			terminate := func() {
				if blocked == "terminate" {
					<-release
				}
			}
			cancel()
			err := fileLifecycle(ctx, run, terminate, 30*time.Millisecond)
			if blocked == "neither" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "cleanup unconfirmed")
			}
		})
	}
}

func TestFileLifecyclePreservesRunError(t *testing.T) {
	want := errors.New("invalid inventory")
	err := fileLifecycle(context.Background(), func(context.Context) error { return want }, func() {}, time.Second)
	require.ErrorIs(t, err, want)
}

// Exercise the real binary's linked driver registrations. File-mode startup
// must not depend on socket-mode flags or initialize the NAS driver.
func TestLinkedFileDrivers(t *testing.T) {
	fileDrivers := server.InitFileDrivers()
	defer server.Terminate(fileDrivers)
	for _, name := range []string{"ossfs", "ossfs2", "customfuse"} {
		require.Contains(t, fileDrivers, name)
	}
	require.NotContains(t, fileDrivers, "alinas")
}
