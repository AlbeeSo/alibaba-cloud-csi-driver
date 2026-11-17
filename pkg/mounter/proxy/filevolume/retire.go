package filevolume

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Retire only removes this transport's control files. The caller must first
// complete kubelet-requested host target cleanup. It neither traverses a data
// target nor claims to destroy a guest/VM. os.Root confines all removals to the
// UID directory, even if a component is concurrently replaced by a symlink.
func Retire(root, hostTarget string) error {
	uid, relative, err := Target(hostTarget)
	if err != nil {
		return nil
	}
	base, pod, err := openPodState(root, uid)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer base.Close()
	defer pod.Close()
	key := strings.TrimSuffix(relative, "/mount")
	for _, dir := range []string{"desired", "status", "unmount"} {
		if err := removeControl(pod, dir+"/"+ID(key)+".json"); err != nil {
			return err
		}
	}
	// A missing desired is not proof that another volume was retired. Keep
	// its status/tombstone until its own Unpublish. Never-submitted pending
	// inventory entries can be collected after kubelet removes the Pod dir.
	for _, dir := range []string{"desired", "status", "unmount"} {
		entries, err := readControlDir(pod, dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if controlJSON(entry.Name()) {
				return nil
			}
		}
	}
	return retirePod(base, pod, uid)
}

func openPodState(root, uid string) (*os.Root, *os.Root, error) {
	base, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
	}
	info, err := base.Lstat(uid)
	if err == nil && !info.IsDir() {
		err = fmt.Errorf("Pod state is not a directory")
	}
	if err != nil {
		base.Close()
		return nil, nil, err
	}
	pod, err := base.OpenRoot(uid)
	if err != nil {
		base.Close()
		return nil, nil, err
	}
	return base, pod, nil
}

func readControlDir(pod *os.Root, name string) ([]os.DirEntry, error) {
	f, err := pod.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func removeControl(pod *os.Root, path string) error {
	err := pod.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func controlJSON(name string) bool {
	if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimSuffix(name, ".json"))
	return err == nil
}

// Never use RemoveAll. Unknown files/directories remain untouched. In
// particular, a file called "mount" or a symlink to a data directory is not a
// cleanup instruction. Only known flat control filenames are removed.
func retirePod(base, pod *os.Root, uid string) error {
	for _, dir := range []string{"desired", "status", "unmount"} {
		info, err := pod.Lstat(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("control directory %s is not a directory", dir)
		}
		entries, err := readControlDir(pod, dir)
		if os.IsNotExist(err) {
			continue
		} // another volume may retire the same UID concurrently
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.IsDir() && (controlJSON(entry.Name()) || strings.HasPrefix(entry.Name(), ".pending-")) {
				if err := removeControl(pod, dir+"/"+entry.Name()); err != nil {
					return err
				}
			}
		}
		if err := pod.Remove(dir); err != nil && !os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) {
			return err
		}
	}
	for _, name := range []string{"manifest.json", "session.json", "ready-session.json", ".runner.lock"} {
		if err := removeControl(pod, name); err != nil {
			return err
		}
	}
	entries, err := readControlDir(pod, ".")
	if os.IsNotExist(err) {
		return nil
	} // the last concurrent Unpublish already removed it
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".pending-") {
			if err := removeControl(pod, entry.Name()); err != nil {
				return err
			}
		}
	}
	err = base.Remove(uid)
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOTEMPTY) {
		return nil
	}
	return err
}

// CollectOrphans retires controls only after kubelet has removed its local Pod
// directory. API Pod NotFound/heartbeat expiry is deliberately not used.
// Kubelet removes that content after its runtime and volume teardown; this is
// cleanup of node-owned metadata, not an independent guest process reaper.
func CollectOrphans(root, kubeletPods string) error {
	info, err := os.Stat(kubeletPods)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("kubelet pods path is not a directory")
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		uid := entry.Name()
		if !entry.IsDir() || !ValidComponent(uid) || strings.HasPrefix(uid, ".") {
			continue
		}
		if _, err := os.Lstat(filepath.Join(kubeletPods, uid)); !os.IsNotExist(err) {
			continue
		}
		base, pod, err := openPodState(root, uid)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		err = retirePod(base, pod, uid)
		pod.Close()
		base.Close()
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
