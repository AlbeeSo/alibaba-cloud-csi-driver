package filevolume

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"
)

type Backend interface {
	Mount(context.Context, *proxy.MountRequest) (<-chan error, error)
	Mounted(string) (bool, error)
	Unmount(string) error
}

type Runner struct {
	Root, PodUID, TargetRoot string
	Expected                 []string
	Timeout                  time.Duration
	Backend                  Backend
	controlMu                sync.RWMutex
	controls                 map[string]bool
	controlErr               error
}

// Run owns one worker per expected volume. Mounts proceed independently so one
// blocked client cannot hide another volume's result. Failed mounts stay failed
// until a sidecar restart; no unbounded background remount loop is introduced.
func (r *Runner) Run(ctx context.Context) error {
	if !ValidComponent(r.PodUID) || !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.TargetRoot) || r.Timeout <= 0 || r.Backend == nil {
		return fmt.Errorf("file mode requires Pod UID, absolute roots, backend and positive timeout")
	}
	lock, err := os.OpenFile(filepath.Join(r.Root, ".runner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }() // releases flock; no buffered writes
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fmt.Errorf("another runner owns this Pod state: %w", err)
	}
	if len(r.Expected) == 0 {
		ids, err := waitExpected(ctx, r.Root, r.PodUID)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		r.Expected = ids
	}
	if err := r.refreshControls(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range r.Expected {
		if id == "" || seen[id] {
			return fmt.Errorf("empty or duplicate expected volume")
		}
		seen[id] = true
	}
	session := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	ready, err := wasReady(r.Root, r.PodUID)
	if err != nil {
		return err
	}
	if ready {
		for _, key := range r.Expected {
			var s Status
			_ = ReadJSON(StatusPath(r.Root, key), &s)
			// A rejected restart has no new execution session. Preserve completed
			// and pending cleanup evidence so it cannot invalidate a valid ACK
			// just before kubelet calls NodeUnpublish.
			if s.PodUID == r.PodUID && (s.Phase == "Unmounted" || s.Phase == "CleanupPending" || s.Phase == "Draining") {
				continue
			}
			s.APIVersion, s.PodUID, s.UpdatedAt = Version, r.PodUID, time.Now().UTC()
			s.Phase, s.Error = "PodRecreationRequired", "mountproxy restarted after startup readiness; recreate the Pod because application bind mounts may retain the old FUSE instance"
			if err := WriteJSON(StatusPath(r.Root, key), s); err != nil {
				return err
			}
		}
		return fmt.Errorf("mountproxy restarted after startup readiness; Pod recreation required")
	}
	if err := WriteJSON(filepath.Join(r.Root, "session.json"), session); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, len(r.Expected))
	for _, id := range r.Expected {
		wg.Go(func() {
			if err := r.volume(ctx, id, session); err != nil {
				errCh <- err
			}
		})
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ctx.Done():
			break wait
		case <-done:
			break wait
		case err := <-errCh:
			klog.ErrorS(err, "File volume worker stopped; other volumes remain supervised")
		case <-ticker.C:
			if err := r.refreshControls(); err != nil {
				klog.ErrorS(err, "Cannot refresh control metadata")
			}
		}
	}
	cancel()
	<-done
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func (r *Runner) volume(ctx context.Context, id, session string) (result error) {
	s := Status{APIVersion: Version, PodUID: r.PodUID, VolumeID: id, Session: session, Phase: "Pending"}
	var current *Desired
	write := func() error {
		s.UpdatedAt = time.Now().UTC()
		err := WriteJSON(StatusPath(r.Root, id), s)
		if err != nil && current != nil {
			klog.ErrorS(err, "Cannot persist status; preserving mount until explicit cleanup", "volumeKey", id)
			return nil
		}
		return err
	}
	if err := write(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var target string
	var exited <-chan error
	attempted := false
	// Always try explicit unmount before driver.Terminate kills the client.
	defer func() {
		if target == "" {
			return
		}
		s.Phase, s.Error = "Draining", ""
		_ = write()
		if err := r.Backend.Unmount(target); err != nil {
			s.Phase, s.Error = "CleanupPending", "cleanup failed: "+redact(err.Error(), current)
			s.UnmountResult = &proxy.Response{Error: s.Error}
			result = fmt.Errorf("volume %s cleanup failed", id)
		} else {
			s.Phase = "Unmounted"
			s.UnmountResult = &proxy.Response{}
		}
		if err := write(); err != nil {
			result = err
		}
	}()
	for {
		var revoke UnmountRequest
		revokeErr := ReadJSON(UnmountPath(r.Root, id), &revoke)
		if revokeErr == nil && current != nil {
			if err := revoke.Validate(*current); err != nil {
				s.Phase, s.Error = "Failed", err.Error()
				if err := write(); err != nil {
					return err
				}
			} else {
				s.Phase, s.Error = "Draining", ""
				if err := write(); err != nil {
					return err
				}
				if err := r.Backend.Unmount(target); err != nil {
					s.Phase, s.Error = "CleanupPending", "cleanup failed: "+redact(err.Error(), current)
					s.UnmountResult = &proxy.Response{Error: s.Error}
					if err := write(); err != nil {
						return err
					}
					// A busy revoked volume must not cancel workers for other volumes.
					select {
					case <-ctx.Done():
						return nil
					case <-ticker.C:
						continue
					}
				}
				s.Phase, s.Error, s.UnmountResult = "Unmounted", "", &proxy.Response{}
				if err := write(); err != nil {
					return err
				}
				target = "" // explicit cleanup already completed
				return nil
			}
		} else if revokeErr == nil && current == nil {
			// A restarted sidecar must not execute a previously revoked mount.
			var revoked Desired
			if err := ReadJSON(RequestPath(r.Root, id), &revoked); err != nil {
				return fmt.Errorf("cannot verify revoked request for %s", id)
			}
			if err := revoked.Validate(r.PodUID); err != nil {
				return err
			}
			if revoked.Mount.Target != id+"/mount" {
				return fmt.Errorf("revoked target mismatch")
			}
			if err := revoke.Validate(revoked); err != nil {
				return err
			}
			current = &revoked
			target = filepath.Join(r.TargetRoot, filepath.FromSlash(revoked.Mount.Target))
			s.VolumeID, s.ObservedGeneration = revoked.Mount.VolumeID, revoked.Generation
			return nil
		} else if revokeErr != nil && !os.IsNotExist(revokeErr) {
			s.Phase, s.Error = "ControlError", "cannot decode explicit unmount command"
			if err := write(); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				continue
			}
		}
		d, err := r.readDesired(id)
		switch {
		case os.IsNotExist(err):
			if current != nil {
				s.Phase, s.Error = "RequestMissing", "desired file disappeared without an explicit unmount command"
			}
		case err != nil:
			s.Phase, s.Error = "Failed", "cannot decode desired file"
		default:
			if err = d.Validate(r.PodUID); err == nil && d.Mount.Target != id+"/mount" {
				err = fmt.Errorf("volume target mismatch")
			}
			if err != nil {
				s.Phase, s.Error = "Failed", err.Error()
			} else if current != nil && current.Generation != d.Generation {
				s.Phase, s.Error = "Failed", "configuration changed; recreate Pod to apply it"
			} else if current != nil && s.Phase == "RequestMissing" && s.Result != nil && s.Result.Error == "" && exited != nil {
				s.Phase, s.Error = "Mounted", ""
			} else if !attempted {
				original := d
				current = &original
				s.VolumeID = d.Mount.VolumeID
				s.ObservedGeneration, s.Phase, s.Error = d.Generation, "Mounting", ""
				s.UpdatedAt = time.Now().UTC()
				// The execution intent must be durable before starting a daemon.
				if err := WriteJSON(StatusPath(r.Root, id), s); err != nil {
					return err
				}
				// Check parents before inspecting a potentially disconnected FUSE
				// mount: lstat on the mounted leaf may block in the dead daemon.
				if err := rejectSymlinks(r.TargetRoot, filepath.Dir(d.Mount.Target)); err != nil {
					return err
				}
				target = filepath.Join(r.TargetRoot, filepath.FromSlash(d.Mount.Target))
				attempted = true
				// A previous process may have left a disconnected FUSE mount.
				// Never adopt a mount solely because its pathname still exists.
				mounted, mountErr := r.Backend.Mounted(target)
				if mountErr == nil && mounted {
					mountErr = r.Backend.Unmount(target)
				}
				if mountErr == nil {
					mountErr = os.MkdirAll(target, 0755)
				}
				if mountErr == nil {
					mountErr = rejectSymlinks(r.TargetRoot, d.Mount.Target)
				}
				if mountErr == nil {
					d.Mount.Target = target
					mountCtx, cancel := context.WithTimeout(ctx, r.Timeout)
					exited, mountErr = r.Backend.Mount(mountCtx, &d.Mount)
					cancel()
				}
				if mountErr != nil {
					s.Phase, s.Error = "Failed", redact(mountErr.Error(), current)
				} else {
					s.Phase = "Mounted"
				}
				s.Result = &proxy.Response{Error: s.Error}
			}
		}
		if attempted && (s.Phase == "Mounted" || s.Phase == "RequestMissing") {
			select {
			case err := <-exited:
				s.Phase, s.Error = "Failed", "FUSE process exited"
				if err != nil {
					s.Error += ": " + redact(err.Error(), current)
				}
				exited = nil
			default:
				mounted, err := r.Backend.Mounted(target)
				if err != nil || !mounted {
					s.Phase, s.Error = "Failed", "mount point is no longer present"
				}
			}
		}
		if err := write(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// The directory is writable only by trusted node/sidecar processes. Reject
// existing symlink components as an additional guard against escaping TargetRoot.
func rejectSymlinks(root, relative string) error {
	p := root
	for _, component := range strings.Split(relative, "/") {
		p = filepath.Join(p, component)
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in mount target")
		}
	}
	return nil
}

func redact(message string, d *Desired) string {
	if d != nil {
		if d.Mount.Source != "" {
			message = strings.ReplaceAll(message, d.Mount.Source, "[source]")
		}
		for _, value := range d.Mount.Secrets {
			if value != "" {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return message
}
