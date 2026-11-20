//go:build linux

package oss

import (
	"context"
	"os"
	"path/filepath"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	proxyclient "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/client"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var prepareSidecarFD = proxyclient.PrepareFuseFD

func (ns *nodeServer) publishFDVolume(ctx context.Context, req *csi.NodePublishVolumeRequest, request proxy.MountRequest, uid string) (*csi.NodePublishVolumeResponse, error) {
	kernelRequest := request
	kernelRequest.Target = req.TargetPath
	request.Options = proxyclient.DaemonOptions(&request)
	desired, err := filevolume.DesiredFor(uid, req.VolumeId, req.TargetPath, request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	key := filepath.Dir(desired.Mount.Target)
	root := filevolume.Root(ossFileRoot)
	podRoot := filepath.Join(root, uid)
	var old filevolume.Desired
	if err := filevolume.ReadJSON(filevolume.RequestPath(podRoot, key), &old); err == nil {
		if old.Generation != desired.Generation || old.Validate(uid) != nil {
			return nil, status.Error(codes.AlreadyExists, "sidecar configuration changed; recreate Pod")
		}
	} else if !os.IsNotExist(err) {
		return nil, status.Errorf(codes.Internal, "read fd request: %v", err)
	}
	if filevolume.NativeFDs.Has(req.TargetPath, desired.Generation) {
		return &csi.NodePublishVolumeResponse{}, nil
	}
	// A restarted CSI must not remount beneath a possibly live consumer. A
	// matching Mounted result proves previous delivery; ambiguous states fail
	// closed instead of inventing recovery of a lost descriptor.
	notMounted, err := mounterutils.SafeIsNotMountPoint(ns.rawMounter, req.TargetPath, true)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "inspect fd target: %v", err)
	}
	if !notMounted {
		var observed filevolume.Status
		if filevolume.ReadJSON(filevolume.StatusPath(podRoot, key), &observed) == nil && observed.PodUID == uid && observed.ObservedGeneration == desired.Generation && observed.Phase == "Mounted" {
			return &csi.NodePublishVolumeResponse{}, nil
		}
		return nil, status.Error(codes.FailedPrecondition, "CSI lost the pending FUSE fd ownership; recreate Pod rather than remounting a possibly active connection")
	}
	if err := os.MkdirAll(req.TargetPath, 0755); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := filevolume.NativeFDs.OfferLazy(podRoot, key, req.TargetPath, desired.Generation, func() (int, error) {
		// The original publish RPC has already returned before the receiver can
		// start. Do not attach delayed preparation to its cancelled context.
		return prepareSidecarFD(context.Background(), &kernelRequest, ns.rawMounter)
	}); err != nil {
		return nil, status.Errorf(codes.Internal, "offer FUSE fd: %v", err)
	}
	if err := filevolume.Publish(root, uid, req.VolumeId, req.TargetPath, request); err != nil {
		filevolume.NativeFDs.Close(req.TargetPath)
		_ = mounterutils.SafeCleanupFuseMount(req.TargetPath, ns.rawMounter, true)
		return nil, status.Errorf(codes.Internal, "publish fd request: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
