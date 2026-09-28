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
	return explicitReadOnly(req) || flagsContain(req.GetVolumeCapability().GetMount().GetMountFlags(), "ro")
}

func explicitReadOnly(req *csi.NodePublishVolumeRequest) bool {
	if req.GetReadonly() {
		return true
	}
	switch req.GetVolumeCapability().GetAccessMode().GetMode() {
	case csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY, csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY:
		return true
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

func (n *Node) checkMountedReadOnly(target string, readOnly bool) error {
	if !readOnly {
		return nil
	}
	entries, err := n.opts.Mounter.List()
	if err != nil {
		return status.Errorf(codes.Internal, "read mount table: %v", err)
	}
	for _, entry := range entries {
		if entry.Path == target && !flagsContain(entry.Opts, "ro") {
			return status.Error(codes.AlreadyExists, "target access mode differs; unpublish before changing readonly")
		}
	}
	return nil
}
