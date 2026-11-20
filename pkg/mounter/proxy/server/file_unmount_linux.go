//go:build linux

package server

import "golang.org/x/sys/unix"

// Remove only the FUSE layer. The underlying PVC projection belongs to kubelet
// and must survive sidecar restarts; do not remove its directory or unmount it.
func unmountFileTarget(target string) error {
	err := unix.Unmount(target, 0)
	// Do not turn EBUSY into an unverified success with MNT_DETACH. The caller
	// must retain CleanupPending and let the lifecycle owner retry/take over.
	if err == unix.EINVAL || err == unix.ENOENT {
		return nil
	}
	return err
}
