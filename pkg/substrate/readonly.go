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
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requestReadOnly(req *csi.NodePublishVolumeRequest) bool {
	if req.GetReadonly() {
		return true
	}
	switch req.GetVolumeCapability().GetAccessMode().GetMode() {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY, csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY:
		return true
	}
	return flagsContain(req.GetVolumeCapability().GetMount().GetMountFlags(), "ro")
}

func nasReadOnly(req *csi.NodePublishVolumeRequest) bool {
	if requestReadOnly(req) {
		return true
	}
	for key, value := range req.GetVolumeContext() {
		if strings.EqualFold(key, "options") && flagsContain([]string{value}, "ro") {
			return true
		}
	}
	return false
}

func splitMountFlags(value string) []string {
	inQuotes := false
	return strings.FieldsFunc(value, func(r rune) bool {
		if r == '"' {
			inQuotes = !inQuotes
		}
		return r == ',' && !inQuotes
	})
}

func flagsContain(flags []string, wanted string) bool {
	for _, flag := range flags {
		for _, option := range splitMountFlags(flag) {
			if strings.TrimSpace(option) == wanted {
				return true
			}
		}
	}
	return false
}

func withoutWritableOption(value string) string {
	options := splitMountFlags(value)
	kept := make([]string, 0, len(options))
	for _, option := range options {
		if strings.TrimSpace(option) != "rw" {
			kept = append(kept, option)
		}
	}
	if len(kept) == len(options) {
		return value
	}
	return strings.Join(kept, ",")
}

func enforceReadOnly(req *csi.NodePublishVolumeRequest) {
	req.Readonly = true
	mount := req.GetVolumeCapability().GetMount()
	if len(mount.MountFlags) > 0 {
		flags := make([]string, 0, len(mount.MountFlags))
		for _, flag := range mount.MountFlags {
			filtered := withoutWritableOption(flag)
			if filtered != "" || flag == "" {
				flags = append(flags, filtered)
			}
		}
		mount.MountFlags = flags
	}
	for key, value := range req.VolumeContext {
		if strings.EqualFold(key, "options") {
			req.VolumeContext[key] = withoutWritableOption(value)
		}
	}
}

func (n *Node) checkMountedReadOnly(target string, readOnly bool) error {
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target && flagsContain(entry.Opts, "ro") != readOnly {
			return status.Error(codes.AlreadyExists, "target access mode differs; unpublish before changing readonly")
		}
	}
	return nil
}
