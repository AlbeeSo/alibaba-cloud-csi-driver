//go:build linux

package server

import "golang.org/x/sys/unix"

// Remove only the FUSE layer. The underlying PVC projection belongs to kubelet
// and must survive sidecar restarts; do not remove its directory or unmount it.
func unmountFileTarget(target string) error {
	err := unix.Unmount(target, 0)
	if err == unix.EBUSY {
		err = unix.Unmount(target, unix.MNT_DETACH)
	}
	if err == unix.EINVAL || err == unix.ENOENT {
		return nil
	}
	return err
}
