//go:build !windows

package oss

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/mount-utils"
)

func TestFileNodeUnpublishDoesNotWaitForStoppedGuestACK(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty-target", true: "nonempty-target"}[busy], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			req := fileRequest(t, 0, "ossfs2")
			require.NoError(t, filevolume.Publish(root, "pod-a", req.VolumeId, req.TargetPath, proxy.MountRequest{Fstype: "ossfs2"}))
			if busy {
				require.NoError(t, os.WriteFile(filepath.Join(req.TargetPath, "persistent-data"), []byte("keep"), 0600))
			}
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			_, err := ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
			if busy {
				require.Equal(t, codes.Internal, status.Code(err))
				require.FileExists(t, filepath.Join(req.TargetPath, "persistent-data"))
				require.FileExists(t, filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"))
			} else {
				require.NoError(t, err)
				require.NoDirExists(t, filepath.Join(root, "pod-a"))
				_, err = ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
				require.NoError(t, err)
			}
		})
	}
}

func TestFileNodeUnpublishCorruptMetadataCannotRedirectOrBlockCleanup(t *testing.T) {
	for _, content := range []string{"{broken", "{}", `{"podUID":"other","MountProxy":{"volumeID":"other","target":"/outside"}}`} {
		t.Run(content, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CSI_FUSE_FILE_ROOT", root)
			req := fileRequest(t, 0, "ossfs2")
			require.NoError(t, filevolume.Publish(root, "pod-a", req.VolumeId, req.TargetPath, proxy.MountRequest{Fstype: "ossfs2"}))
			other := filepath.Join(root, "other", "desired")
			require.NoError(t, os.MkdirAll(other, 0700))
			data := filepath.Join(other, "keep")
			require.NoError(t, os.WriteFile(data, []byte("keep"), 0600))
			require.NoError(t, os.WriteFile(filevolume.RequestPath(filepath.Join(root, "pod-a"), "pv-0"), []byte(content), 0600))
			ns := setupTestNodeServer(t, mount.NewFakeMounter(nil), false)
			_, err := ns.NodeUnpublishVolume(context.Background(), &csi.NodeUnpublishVolumeRequest{VolumeId: req.VolumeId, TargetPath: req.TargetPath})
			require.NoError(t, err)
			require.NoDirExists(t, filepath.Join(root, "pod-a"))
			require.FileExists(t, data)
		})
	}
}
