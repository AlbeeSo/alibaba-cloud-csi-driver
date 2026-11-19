//go:build linux

package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	mountinfo "github.com/moby/sys/mountinfo"
)

var FuseConnectionsDir = "/sys/fs/fuse/connections"

// GetFuseConnectionID returns the FUSE connection ID (minor device number)
// for the given mount point by parsing /proc/self/mountinfo.
// SingleEntryFilter matches only the exact mount point path, so this
// cannot accidentally return a connection ID for a different mount.
func GetFuseConnectionID(mountpoint string) (uint64, error) {
	infos, err := mountinfo.GetMounts(mountinfo.SingleEntryFilter(mountpoint))
	if err != nil {
		return 0, fmt.Errorf("get mounts for %s: %w", mountpoint, err)
	}
	return fuseConnectionID(infos)
}

func fuseConnectionID(infos []*mountinfo.Info) (uint64, error) {
	var connection *mountinfo.Info
	for _, info := range infos {
		// A propagated FUSE mount can sit on top of the original PVC bind at
		// exactly the same pathname. The bind's minor is not a FUSE connection.
		if !strings.HasPrefix(info.FSType, "fuse") || info.FSType == "fuse.virtiofs" {
			continue
		}
		if connection != nil {
			return 0, fmt.Errorf("ambiguous stacked FUSE mounts; refusing to flush an unverified connection")
		}
		connection = info
	}
	if connection == nil {
		return 0, fmt.Errorf("no FUSE mount info found")
	}
	return uint64(connection.Minor), nil
}

// FlushFuseConnection interrupts all in-flight FUSE requests for a connection,
// keeping the connection alive for recovery restart.
//
// "flush" is a kernel extension from the alinux FUSE recovery patch set.
// Callers are already gated by a /proc/kallsyms check for fuse_flush_pq
// (see detectKernelRecoverySupport), so this file is expected to exist
// when this function is reached.
func FlushFuseConnection(connID uint64) error {
	connDir := filepath.Join(FuseConnectionsDir, strconv.FormatUint(connID, 10))
	flushPath := filepath.Join(connDir, "flush")

	if err := os.WriteFile(flushPath, []byte("1"), 0o644); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("flush FUSE connection %d: %w (kernel may lack alinux FUSE recovery patch, or connection already torn down)", connID, err)
		}
		return fmt.Errorf("flush FUSE connection %d: %w", connID, err)
	}
	return nil
}
