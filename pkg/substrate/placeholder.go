/*
Copyright 2026 The Kubernetes Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package substrate

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mount "k8s.io/mount-utils"
)

func (n *Node) mountAt(target string) (*mount.MountPoint, error) {
	if n.opts.Mounter == nil {
		return nil, status.Error(codes.FailedPrecondition, "mount inspector is required")
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target {
			return &entry, nil
		}
	}
	return nil, nil
}

func nasMount(entry *mount.MountPoint) bool {
	return entry.Type == "nfs" || entry.Type == "nfs4" || entry.Type == "alinas"
}

func (n *Node) placeholderSource(volumeID, target string) (string, error) {
	if !filepath.IsAbs(n.opts.StateDir) || filepath.Clean(n.opts.StateDir) == "/" {
		return "", status.Error(codes.FailedPrecondition, "an absolute dedicated placeholder directory is required")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(volumeID+"\x00"+target)))
	return filepath.Join(n.opts.StateDir, "placeholders", key), nil
}

func (n *Node) placeholderMounted(source, target string) (bool, error) {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, status.Error(codes.FailedPrecondition, "placeholder source is not an owned directory")
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Path == target && entry.Device == source {
			return true, nil
		}
	}
	refs, err := n.opts.Mounter.GetMountRefs(source)
	if err != nil {
		return false, err
	}
	for _, ref := range refs {
		if ref == target {
			return true, nil
		}
	}
	return false, nil
}

func (n *Node) publishPlaceholder(volumeID, target string) error {
	live, err := n.mountAt(target)
	if err != nil {
		return err
	}
	source, err := n.placeholderSource(volumeID, target)
	if err != nil {
		return err
	}
	if live != nil {
		owned, err := n.placeholderMounted(source, target)
		if err != nil {
			return err
		}
		if !owned {
			return status.Error(codes.FailedPrecondition, "Golden target does not reference its expected source")
		}
		return nil
	}
	if info, err := os.Lstat(source); err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return status.Error(codes.FailedPrecondition, "placeholder source is not an owned directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(source, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0755); err != nil {
		return err
	}
	return n.opts.Mounter.Mount(source, target, "", []string{"bind"})
}

func (n *Node) unpublishPlaceholder(source, target string) error {
	if err := n.opts.Mounter.Unmount(target); err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Device == source || entry.Path == source || strings.HasPrefix(entry.Path, source+string(os.PathSeparator)) {
			return fmt.Errorf("refusing to remove mounted placeholder storage")
		}
	}
	refs, err := n.opts.Mounter.GetMountRefs(source)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return fmt.Errorf("placeholder source still has mount references")
	}
	return os.RemoveAll(source)
}
