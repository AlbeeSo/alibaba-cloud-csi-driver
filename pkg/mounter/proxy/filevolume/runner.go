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
}

// Run owns one worker per expected volume. Mounts proceed independently so one
// blocked client cannot hide another volume's result. Failed mounts stay failed
// until a sidecar restart; no unbounded background remount loop is introduced.
func (r *Runner) Run(ctx context.Context) error {
	if !ValidComponent(r.PodUID) || !filepath.IsAbs(r.Root) || !filepath.IsAbs(r.TargetRoot) || r.Timeout <= 0 || r.Backend == nil {
		return fmt.Errorf("file mode requires Pod UID, absolute roots, backend and positive timeout")
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
	seen := map[string]bool{}
	for _, id := range r.Expected {
		if id == "" || seen[id] {
			return fmt.Errorf("empty or duplicate expected volume")
		}
		seen[id] = true
	}
	session := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
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
	select {
	case <-ctx.Done():
	case err := <-errCh:
		cancel()
		<-done
		return err
	}
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
	write := func() error { s.UpdatedAt = time.Now().UTC(); return WriteJSON(StatusPath(r.Root, id), s) }
	if err := write(); err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var current *Desired
	var target string
	var exited <-chan error
	attempted := false
	// Always try explicit unmount before driver.Terminate kills the client.
	defer func() {
		if target == "" {
			return
		}
		if err := r.Backend.Unmount(target); err != nil {
			s.Phase, s.Error = "Failed", "cleanup failed: "+redact(err.Error(), current)
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
		var d Desired
		err := ReadJSON(RequestPath(r.Root, id), &d)
		switch {
		case os.IsNotExist(err):
			if current != nil {
				return nil
			}
		case err != nil:
			s.Phase, s.Error = "Failed", "cannot decode desired file"
		default:
			if err = d.Validate(r.PodUID); err == nil && d.Mount.VolumeID != id {
				err = fmt.Errorf("volume ID mismatch")
			}
			if err != nil {
				s.Phase, s.Error = "Failed", err.Error()
			} else if current != nil && current.Generation != d.Generation {
				s.Phase, s.Error = "Failed", "configuration changed; recreate Pod to apply it"
			} else if !attempted {
				original := d
				current = &original
				s.ObservedGeneration, s.Phase, s.Error = d.Generation, "Mounting", ""
				if err := write(); err != nil {
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
		if attempted && s.Phase == "Mounted" {
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
