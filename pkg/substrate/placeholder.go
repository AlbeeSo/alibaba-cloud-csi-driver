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
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (n *Node) mounted(target string) (bool, error) {
	if n.opts.Mounter == nil {
		return false, status.Error(codes.FailedPrecondition, "mount inspector is required")
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return false, status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target {
			return true, nil
		}
	}
	return false, nil
}

func (n *Node) publishPlaceholder(store bindingStore, b binding) error {
	mounted, err := n.mounted(b.Target)
	if err != nil || mounted {
		return err
	}
	source := store.placeholder(b.LogicalID, b.Target)
	if err := os.MkdirAll(source, 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(b.Target, 0755); err != nil {
		return err
	}
	return n.opts.Mounter.Mount(source, b.Target, "", []string{"bind"})
}

func (n *Node) unpublishPlaceholder(store bindingStore, b binding) error {
	mounted, err := n.mounted(b.Target)
	if err != nil {
		return err
	}
	if mounted {
		if err := n.opts.Mounter.Unmount(b.Target); err != nil {
			return err
		}
	}
	if err := os.Remove(b.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source := store.placeholder(b.LogicalID, b.Target)
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Path == source || strings.HasPrefix(entry.Path, source+string(os.PathSeparator)) {
			return fmt.Errorf("refusing to remove mounted placeholder storage")
		}
	}
	return os.RemoveAll(source)
}
