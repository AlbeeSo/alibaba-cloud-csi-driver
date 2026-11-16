//go:build !windows

package oss

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ossFileRoot = "/host/var/run/ossfs/file-volumes"

func (ns *nodeServer) publishFileVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	opts, err := parseOptions(ctx, ns.cnfsGetter, req.VolumeContext, req.Secrets, []*csi.VolumeCapability{req.VolumeCapability}, "", true, false, ns.metadata)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if opts.FuseType != "ossfs2" || opts.AuthType != "" || opts.Overlay || opts.FdPassing || opts.Recovery {
		return nil, status.Error(codes.InvalidArgument, "file mode requires ossfs2 with Secret credentials, without overlay, fd-passing or recovery")
	}
	manager := ns.fusePodManagers[opts.FuseType]
	if manager == nil {
		return nil, status.Error(codes.Internal, "missing ossfs2 manager")
	}
	if err := checkOssOptions(opts, manager); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	opts.ReadOnly = opts.ReadOnly || req.GetReadonly()
	auth, err := makeAuthConfig(opts, manager, ns.metadata, true)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	options, flags, err := makeMountOptionsAndFlags(opts, manager, ns.metadata, req.VolumeCapability)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	options = manager.AddDefaultMountOptions(options, flags)
	err = filevolume.Publish(filevolume.Root(ossFileRoot), req.VolumeContext["csi.storage.k8s.io/pod.uid"], req.VolumeId, req.TargetPath, proxy.MountRequest{
		Fstype: "ossfs2", Source: fmt.Sprintf("%s:%s", opts.MountBucket(), opts.Path), Options: options, Secrets: auth.Secrets,
	})
	if err != nil {
		code := codes.InvalidArgument
		var pathError *os.PathError
		if errors.Is(err, filevolume.ErrConfigurationChanged) {
			code = codes.AlreadyExists
		}
		if errors.As(err, &pathError) {
			code = codes.Internal
		}
		return nil, status.Errorf(code, "publish file configuration: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
