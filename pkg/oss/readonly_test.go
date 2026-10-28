// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oss

import (
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	ossfpm "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fuse_pod_manager/oss"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadonlyDecisionPreservesOSSCapabilityAggregation verifies that
// opts.ReadOnly is driven solely by the volume access mode (reader-only
// modes), while the per-pod volumeMounts.readOnly flag is captured separately
// in opts.PodReadOnly. This split is what prevents a single ro pod from
// poisoning the shared RunC attachPath for subsequent rw pods.
func TestReadonlyDecisionPreservesOSSCapabilityAggregation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		readonly        bool
		modes           []csi.VolumeCapability_AccessMode_Mode
		wantReadOnly    bool
		wantPodReadOnly bool
	}{
		{"empty capabilities, no pod ro", false, nil, false, false},
		{"pod ro without capabilities", true, nil, false, true},
		{"writer, no pod ro", false, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, false, false},
		{"single reader, no pod ro", false, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY}, true, false},
		{"multi reader, no pod ro", false, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY}, true, false},
		{"reader followed by writer", false, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY, csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, true, false},
		{"writer followed by reader", false, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY}, true, false},
		// Critical regression guard: a per-pod ro request on a writer volume
		// MUST NOT set opts.ReadOnly (which drives the shared attachPath).
		// It only sets opts.PodReadOnly, which RunC clears before building
		// attachPath options. Without this split, a ro pod poisons attachPath
		// and every later rw pod on the node is silently read-only.
		{"pod ro + writer (no attachPath poisoning)", true, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER}, false, true},
		{"pod ro + multi writer (RWX, no poisoning)", true, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER}, false, true},
		{"pod ro + reader (both set)", true, []csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var capabilities []*csi.VolumeCapability
			for _, mode := range tc.modes {
				capabilities = append(capabilities, &csi.VolumeCapability{AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{MountFlags: []string{"ro"}}}, AccessMode: &csi.VolumeCapability_AccessMode{Mode: mode}})
			}
			opts := mustParseOptions(t, map[string]string{"bucket": "bucket", "url": "oss-cn-hangzhou.aliyuncs.com", "otheropts": "-o ro"}, nil, capabilities, tc.readonly, "", true, m)
			require.Equal(t, tc.wantReadOnly, opts.ReadOnly, "opts.ReadOnly must reflect access-mode only, not per-pod readOnly")
			require.Equal(t, tc.wantPodReadOnly, opts.PodReadOnly, "opts.PodReadOnly must reflect the per-pod readOnly flag")
			require.Equal(t, "-o ro", opts.OtherOpts)
		})
	}
}

// TestRunCAttachPathOptionsExcludePodReadOnly verifies the core fix: when a
// per-pod ro request (PodReadOnly=true) arrives for a writable volume
// (ReadOnly=false), the shared RunC attachPath mount options must NOT contain
// "ro". The RunC path in NodePublishVolume clears PodReadOnly before calling
// makeMountOptionsAndFlags; this test simulates that clearing and confirms the
// resulting options are free of "ro", while RunD/MicroVM (PodReadOnly
// preserved) correctly include "ro".
func TestRunCAttachPathOptionsExcludePodReadOnly(t *testing.T) {
	t.Setenv("REGION_ID", "cn-beijing")
	fakeMeta := metadata.NewMetadata()

	ossfs1Fuse, _ := ossfpm.GetFuseMounter(mounterutils.OssFsType, utils.Config{}, fakeMeta)
	fpm := ossfpm.NewOSSFusePodManager(ossfs1Fuse, nil, false)

	baseOpts := func() *ossfpm.Options {
		return &ossfpm.Options{
			URL:      "https://oss-cn-hangzhou.aliyuncs.com",
			Bucket:   "test-bucket",
			Path:     "/",
			FuseType: mounterutils.OssFsType,
			AccessKey: ossfpm.AccessKey{
				AkID:     "ak",
				AkSecret: "sk",
			},
			SigVersion: "v4",
		}
	}

	containsRo := func(opts []string) bool {
		for _, o := range opts {
			if o == "ro" {
				return true
			}
		}
		return false
	}

	t.Run("RunD/MicroVM: PodReadOnly preserved -> ro present", func(t *testing.T) {
		opts := baseOpts()
		opts.PodReadOnly = true
		mountOptions, _, err := makeMountOptionsAndFlags(opts, fpm, fakeMeta, nil)
		require.NoError(t, err)
		assert.True(t, containsRo(mountOptions), "RunD/MicroVM ossfs must be ro when PodReadOnly is set: %v", mountOptions)
	})

	t.Run("RunC: PodReadOnly cleared -> ro absent (no poisoning)", func(t *testing.T) {
		opts := baseOpts()
		opts.PodReadOnly = true
		opts.PodReadOnly = false
		mountOptions, _, err := makeMountOptionsAndFlags(opts, fpm, fakeMeta, nil)
		require.NoError(t, err)
		assert.False(t, containsRo(mountOptions), "RunC shared attachPath must NOT be ro for a per-pod ro request: %v", mountOptions)
	})

	t.Run("access-mode ro -> ro present regardless of PodReadOnly", func(t *testing.T) {
		opts := baseOpts()
		opts.ReadOnly = true
		opts.PodReadOnly = false
		mountOptions, _, err := makeMountOptionsAndFlags(opts, fpm, fakeMeta, nil)
		require.NoError(t, err)
		assert.True(t, containsRo(mountOptions), "access-mode ro volume must mount attachPath ro: %v", mountOptions)
	})

	t.Run("rw volume, no pod ro -> ro absent", func(t *testing.T) {
		opts := baseOpts()
		opts.ReadOnly = false
		opts.PodReadOnly = false
		mountOptions, _, err := makeMountOptionsAndFlags(opts, fpm, fakeMeta, nil)
		require.NoError(t, err)
		assert.False(t, containsRo(mountOptions), "writable volume with no pod ro must not have ro: %v", mountOptions)
	})
}
