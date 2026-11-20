//go:build linux

package filevolume

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// FDRegistry holds only not-yet-handed-off kernel connections. Configuration
// remains in desired JSON; descriptors never become serialized integers.
type FDRegistry struct {
	mu     sync.Mutex
	offers map[string]*fdOffer
}

type fdOffer struct {
	mu         sync.Mutex
	generation string
	fd         int
	delivered  bool
	closed     bool
	listener   *net.UnixListener
	dir        *os.File
	prepare    func() (int, error)
	prepareErr error
}

var NativeFDs FDRegistry

// shortSocket uses an anchored directory fd instead of chdir or a long sockaddr
// path. CSI and sidecar may see the same directory at different absolute paths.
func shortSocket(root, key string, create bool) (*os.File, string, error) {
	if !ValidComponent(key) {
		return nil, "", fmt.Errorf("invalid fd volume key")
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = base.Close() }()
	rel := filepath.Join("fds", ID(key))
	if create {
		if err := base.MkdirAll(rel, 0700); err != nil {
			return nil, "", err
		}
	}
	dir, err := base.Open(rel)
	if err != nil {
		return nil, "", err
	}
	return dir, fmt.Sprintf("/proc/self/fd/%d/socket", dir.Fd()), nil
}

func (r *FDRegistry) Has(target, generation string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := r.offers[target]
	return o != nil && o.generation == generation
}

// Offer takes ownership on success. The listener stays until kubelet teardown,
// so duplicate receive attempts are rejected rather than starting two daemons.
func (r *FDRegistry) Offer(root, key, target, generation string, fd int) error {
	if fd < 0 || generation == "" {
		return fmt.Errorf("missing fd or generation")
	}
	return r.offer(root, key, target, generation, fd, nil)
}

// OfferLazy delays the kernel mount until the consumer is running and asks for
// its fd. Mounting before returning NodePublish can block the container runtime
// on FUSE_INIT while constructing that same consumer's volume mounts.
func (r *FDRegistry) OfferLazy(root, key, target, generation string, prepare func() (int, error)) error {
	if prepare == nil || generation == "" {
		return fmt.Errorf("missing fd preparation or generation")
	}
	return r.offer(root, key, target, generation, -1, prepare)
}

func (r *FDRegistry) offer(root, key, target, generation string, fd int, prepare func() (int, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.offers == nil {
		r.offers = map[string]*fdOffer{}
	}
	if r.offers[target] != nil {
		return fmt.Errorf("fd offer already exists for target")
	}
	dir, path, err := shortSocket(root, key, true)
	if err != nil {
		return err
	}
	// Only replace this volume's fixed socket, under the confined directory.
	_ = os.Remove(path)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		_ = dir.Close()
		return err
	}
	listener.SetUnlinkOnClose(false)
	o := &fdOffer{generation: generation, fd: fd, listener: listener, dir: dir, prepare: prepare}
	r.offers[target] = o
	go o.serve()
	return nil
}

func (o *fdOffer) serve() {
	for {
		conn, err := o.listener.AcceptUnix()
		if err != nil {
			return
		}
		func() {
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			var generation string
			if json.NewDecoder(io.LimitReader(conn, 1024)).Decode(&generation) != nil {
				return
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			if generation != o.generation || o.delivered || o.closed {
				_, _ = conn.Write([]byte("invalid or already transferred fd offer; recreate Pod"))
				return
			}
			if o.prepare != nil {
				o.fd, o.prepareErr = o.prepare()
				o.prepare = nil
			}
			if o.fd < 0 || o.prepareErr != nil {
				_, _ = fmt.Fprintf(conn, "FUSE preparation failed: %v", o.prepareErr)
				return
			}
			if _, _, err := conn.WriteMsgUnix([]byte{1}, unix.UnixRights(o.fd), nil); err != nil {
				return
			}
			// Once sendmsg succeeds the receiver owns a duplicate. Never offer it
			// again after an ambiguous disconnect: that could create two daemons.
			o.delivered = true
			_ = unix.Close(o.fd)
			o.fd = -1
		}()
	}
}

func (r *FDRegistry) Close(target string) bool {
	r.mu.Lock()
	o := r.offers[target]
	delete(r.offers, target)
	r.mu.Unlock()
	if o == nil {
		return false
	}
	_ = o.listener.Close()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	o.prepare = nil
	if o.fd >= 0 {
		_ = unix.Close(o.fd)
		o.fd = -1
	}
	_ = os.Remove(fmt.Sprintf("/proc/self/fd/%d/socket", o.dir.Fd()))
	_ = o.dir.Close()
	return true
}

// ClosePod is used only after the kubelet Pod directory has disappeared. It
// releases pending descriptors even when kubelet never called NodeUnpublish.
func (r *FDRegistry) ClosePod(uid string) {
	r.mu.Lock()
	var targets []string
	for target := range r.offers {
		if targetUID, _, err := Target(target); err == nil && targetUID == uid {
			targets = append(targets, target)
		}
	}
	r.mu.Unlock()
	for _, target := range targets {
		r.Close(target)
	}
}

// ReceiveFD validates the generation before receiving one CLOEXEC descriptor.
// The caller owns it on success and passes ownership to the existing driver.
func ReceiveFD(root, key, generation string, timeout time.Duration) (int, error) {
	dir, path, err := shortSocket(root, key, false)
	if err != nil {
		return -1, err
	}
	defer func() { _ = dir.Close() }()
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return -1, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(generation); err != nil {
		return -1, err
	}
	data, control := make([]byte, 512), make([]byte, unix.CmsgSpace(4*8))
	n, oobn, flags, _, err := conn.ReadMsgUnix(data, control)
	if err != nil {
		return -1, err
	}
	msgs, err := unix.ParseSocketControlMessage(control[:oobn])
	if err != nil {
		return -1, err
	}
	var fds []int
	for _, msg := range msgs {
		rights, e := unix.ParseUnixRights(&msg)
		if e != nil {
			err = e
			break
		}
		fds = append(fds, rights...)
	}
	if err != nil || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(fds) != 1 || n != 1 || data[0] != 1 {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		if len(fds) == 0 && n > 0 {
			return -1, fmt.Errorf("fd handoff rejected: %s", string(data[:n]))
		}
		return -1, fmt.Errorf("fd handoff rejected or malformed; recreate Pod if already transferred")
	}
	unix.CloseOnExec(fds[0])
	return fds[0], nil
}
