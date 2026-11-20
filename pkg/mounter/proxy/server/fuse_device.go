//go:build linux

package server

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// EnsureFuseDevice prepares the guest's own character device. RunD deliberately
// does not forward host devices to privileged containers. Never replace an
// existing path or accept a symlink/regular file in place of the device.
func EnsureFuseDevice(path string) error {
	return ensureFuseDevice(path, unix.Mknod)
}

func ensureFuseDevice(path string, create func(string, uint32, int) error) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err = create(path, unix.S_IFCHR|0600, int(unix.Mkdev(10, 229))); err != nil && !os.IsExist(err) {
			return fmt.Errorf("create FUSE device: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect FUSE device: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if info.Mode()&os.ModeCharDevice == 0 || info.Mode()&os.ModeSymlink != 0 || unix.Major(uint64(st.Rdev)) != 10 || unix.Minor(uint64(st.Rdev)) != 229 {
		return fmt.Errorf("%s is not the FUSE character device", path)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open FUSE device: %w", err)
	}
	return unix.Close(fd)
}
