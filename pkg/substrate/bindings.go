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
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type bindingKind string

const (
	bindingNAS    bindingKind = "nas"
	bindingGolden bindingKind = "golden"
)

var errBindingConflict = errors.New("target is already bound to a different mount configuration")

type binding struct {
	Version    int         `json:"version"`
	LogicalID  string      `json:"logicalID"`
	Target     string      `json:"target"`
	ActorUID   string      `json:"actorUID"`
	VolumeName string      `json:"volumeName"`
	Kind       bindingKind `json:"kind"`
	Driver     string      `json:"driver,omitempty"`
	RealID     string      `json:"realID,omitempty"`
	Digest     string      `json:"digest"`
}

func (b binding) validate() error {
	if b.Version != 1 || b.LogicalID == "" || b.ActorUID == "" || b.VolumeName == "" || !filepath.IsAbs(b.Target) || filepath.Clean(b.Target) != b.Target {
		return fmt.Errorf("invalid mount binding identity")
	}
	digest, err := hex.DecodeString(b.Digest)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("invalid mount binding digest")
	}
	switch b.Kind {
	case bindingNAS:
		if b.Driver != NASDriverName || b.RealID == "" {
			return fmt.Errorf("invalid NAS binding")
		}
	case bindingGolden:
		if b.Driver != "" || b.RealID != "" {
			return fmt.Errorf("golden placeholder cannot reference storage")
		}
	default:
		return fmt.Errorf("unknown mount binding kind")
	}
	return nil
}

type bindingStore struct{ root string }

func (s bindingStore) check() error {
	if !filepath.IsAbs(s.root) || filepath.Clean(s.root) == "/" {
		return fmt.Errorf("an absolute dedicated binding directory is required")
	}
	return nil
}

func bindingKey(id, target string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(id+"\x00"+target)))
}

func (s bindingStore) file(id, target string) string {
	return filepath.Join(s.root, bindingKey(id, target)+".json")
}

func (s bindingStore) placeholder(id, target string) string {
	return filepath.Join(s.root, "placeholders", bindingKey(id, target))
}

func (s bindingStore) load(id, target string) (*binding, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.file(id, target))
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, 8193))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return nil, err
	}
	if len(raw) > 8192 {
		return nil, fmt.Errorf("mount binding is oversized")
	}
	var b binding
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("invalid mount binding JSON")
	}
	if err := b.validate(); err != nil {
		return nil, err
	}
	if b.LogicalID != id || b.Target != target {
		return nil, fmt.Errorf("mount binding key does not match its contents")
	}
	return &b, nil
}

func (s bindingStore) put(b binding) (retErr error) {
	if err := s.check(); err != nil {
		return err
	}
	if err := b.validate(); err != nil {
		return err
	}
	if existing, err := s.load(b.LogicalID, b.Target); err == nil {
		if *existing != b {
			return errBindingConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(s.root, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.root, ".binding-*")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.Remove(f.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
			retErr = errors.Join(retErr, err)
		}
	}()
	if err := json.NewEncoder(f).Encode(b); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), s.file(b.LogicalID, b.Target)); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, loadErr := s.load(b.LogicalID, b.Target)
		if loadErr != nil {
			return loadErr
		}
		if *existing != b {
			return errBindingConflict
		}
	}
	return s.sync()
}

func (s bindingStore) remove(id, target string) error {
	if err := s.check(); err != nil {
		return err
	}
	if err := os.Remove(s.file(id, target)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return s.sync()
}

func (s bindingStore) sync() error {
	dir, err := os.Open(s.root)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
