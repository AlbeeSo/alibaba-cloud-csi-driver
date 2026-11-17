package main

import (
	"testing"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"github.com/stretchr/testify/require"
)

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
