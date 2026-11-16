//go:build !windows

package oss

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	ossfpm "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fuse_pod_manager/oss"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const ossFileRoot = "/host/var/run/ossfs/file-volumes"

func validateFileOptions(opts *ossfpm.Options) error {
	if opts.AuthType != "" {
		return fmt.Errorf("file transport currently requires Secret credential passthrough")
	}
	return filevolume.ValidateMount(proxy.MountRequest{VolumeID: "validation", Fstype: opts.FuseType, Overlay: opts.Overlay, FdPassing: opts.FdPassing, Recovery: opts.Recovery})
}

func (ns *nodeServer) publishFileVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	opts, err := parseOptions(ctx, ns.cnfsGetter, req.VolumeContext, req.Secrets, []*csi.VolumeCapability{req.VolumeCapability}, "", true, false, ns.metadata)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := resolveAgenticBucketOptions(opts, ns.metadata); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := validateFileOptions(opts); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	manager := ns.fusePodManagers[opts.FuseType]
	if manager == nil {
		return nil, status.Errorf(codes.Internal, "missing %s manager", opts.FuseType)
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
		Fstype: opts.FuseType, Source: fmt.Sprintf("%s:%s", opts.MountBucket(), opts.Path), Options: options, Secrets: auth.Secrets,
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
