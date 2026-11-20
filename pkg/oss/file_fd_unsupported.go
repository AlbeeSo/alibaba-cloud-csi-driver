//go:build !linux && !windows

package oss

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (*nodeServer) publishFDVolume(context.Context, *csi.NodePublishVolumeRequest, proxy.MountRequest, string) (*csi.NodePublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "native sidecar fd handoff requires Linux")
}
