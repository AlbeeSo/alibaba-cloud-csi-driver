package strmvol

import (
	"context"
	"os"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/internal"
	svutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	mountutils "k8s.io/mount-utils"
)

const (
	requestTimeout = 10 * time.Second
)

var (
	nodeCaps = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
	}
)

type nodeServer struct {
	nodeId  string
	mounter mountutils.Interface
	locks   *utils.VolumeLocks
}

func newNodeServer(nodeId string) (*nodeServer, error) {
	return &nodeServer{
		nodeId:  nodeId,
		mounter: mountutils.New(""),
		locks:   utils.NewVolumeLocks(),
	}, nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	log.WithField("request", req).Info("NodePublishVolume: starting")
	vc := req.GetVolumeContext()
	targetPath := req.GetTargetPath()
	globalPath := req.GetStagingTargetPath()
	err := svutils.ValidateRequest(vc, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)

	err = svutils.ValidateNodePulishVolumeParams(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	// ensure taget path is not mounted
	notMounted, err := ns.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("NodeStageVolume: targetPath %s does not exist, creating...", targetPath)
			if err = svutils.CreateDir(targetPath); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to create targetPath %s: %v", targetPath, err)
			}
			notMounted = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMounted {
		return nil, status.Errorf(codes.Internal, "targetPath %s is already mounted", targetPath)
	}

	// ensure taget path is mounted
	notMounted, err = ns.mounter.IsLikelyNotMountPoint(globalPath)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if notMounted {
		return nil, status.Errorf(codes.Internal, "globalPath %s is not a mountpoint", globalPath)
	}

	// do mount
	fsType, mountOptions := svutils.GetFsTypeAndOptions(req)
	if err = ns.mounter.Mount(globalPath, targetPath, fsType, mountOptions); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &csi.NodePublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	log.WithField("request", req).Info("NodeUnpublishVolume: starting")
	targetPath := req.GetTargetPath()
	err := svutils.ValidateRequest(nil, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)

	if err := cleanupMountpoint(ns.mounter, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount %s: %v", targetPath, err)
	}
	log.Infof("NodeUnpublishVolume: unmount volume on %s successfully", targetPath)
	return &csi.NodeUnpublishVolumeResponse{}, nil
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	log.WithField("request", req).Info("NodeStageVolume: starting")
	vc := req.GetVolumeContext()
	targetPath := req.GetStagingTargetPath()
	err := svutils.ValidateRequest(vc, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)

	modified, err := svutils.ValidateNodeStageVolumeParams(req, vc)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	if modified {
		log.Infof("NodeStageVolume: parameters have modified to: %v")
	}

	secret := req.GetSecrets()
	secretType, secretData := svutils.ValidateNodeStageVolumeSecrets(secret)
	if secretType == "" || secretData == "" {
		return nil, status.Errorf(codes.InvalidArgument, "failed to parse secret type and data, want (%s, %s) or (%s, %s)", svutils.SecretAccessKeyId, svutils.SecretAccessKeySecret, svutils.SecretUsername, svutils.SecretPassword)
	}

	vc[internal.SecretType] = secretType
	vc[internal.SecretData] = secretData

	notMounted, err := ns.mounter.IsLikelyNotMountPoint(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Infof("NodeStageVolume: targetPath %s does not exist, creating...", targetPath)
			if err = svutils.CreateDir(targetPath); err != nil {
				return nil, status.Errorf(codes.Internal, "failed to create targetPath %s: %v", targetPath, err)
			}
			notMounted = true
		} else {
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if !notMounted {
		return nil, status.Errorf(codes.Internal, "targetPath %s is already mounted", targetPath)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(requestTimeout))
	defer cancel()
	client, err := internal.NewStrmvolClient(ctx, vc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create strmvol client failed, error: %s", err)
	}
	resp, err := client.AttachVolume()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "attach volume failed, error: %s")
	}
	// todo: strmvold should return error if has already attached but with different params
	if resp.Status != internal.StatusOK {
		return nil, status.Errorf(codes.Internal, "attach volume failed, status: %d, message: %s", resp.Status, resp.Message)
	}

	// bind mount to targrtPath
	mountInfo := resp.Mount
	if mountInfo == nil {
		return nil, status.Errorf(codes.Internal, "attach volume succeed but got nil mount info")
	}
	mountInfo.Target = targetPath
	log.Infof("NodeStageVolume: attach volume succeed, mount info: %v")
	// todo: assumed that options like ro, discard ... are supported by strmvold
	err = ns.mounter.Mount(mountInfo.Source, mountInfo.Target, mountInfo.Type, mountInfo.Options)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to mount %s: %v", targetPath, err)
	}

	return &csi.NodeStageVolumeResponse{}, nil
}
func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	log.WithField("request", req).Info("NodeUnpublishVolume: starting")
	targetPath := req.GetStagingTargetPath()
	err := svutils.ValidateRequest(nil, targetPath)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	if !ns.locks.TryAcquire(req.VolumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for %s", req.VolumeId)
	}
	defer ns.locks.Release(req.VolumeId)

	if err := cleanupMountpoint(ns.mounter, targetPath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount %s: %v", targetPath, err)
	}

	id, volumeType := svutils.SplitVolumeHandle(req.GetVolumeId())
	if id == "" || volumeType == "" {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volumeHandle %s, please use <pvName-targetType> instead", req.GetVolumeId())
	}
	vc := map[string]string{
		internal.VolumeID:     id,
		svutils.KeyVolumeType: volumeType,
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(requestTimeout))
	defer cancel()
	client, err := internal.NewStrmvolClient(ctx, vc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create strmvol client failed, error: %s", err)
	}
	dResp, err := client.DetachVolume()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "detach volume failed, error: %s")
	}
	// todo: strmvold should return notfound error help CSI retry if detach succeeded but remove failed
	if dResp.Status != internal.StatusOK && dResp.Status != internal.StatusArtifactNotFound {
		return nil, status.Errorf(codes.Internal, "detach volume failed, status: %d, message: %s", dResp.Status, dResp.Message)
	}
	rResp, err := client.RemoveVolume()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "remove volume failed, error: %s", err)
	}
	if rResp.Status != internal.StatusOK && rResp.Status != internal.StatusArtifactNotFound {
		return nil, status.Errorf(codes.Internal, "remove volume failed, status: %d, message: %s", rResp.Status, rResp.Message)
	}
	log.Infof("NodeUnstageVolume: unmount volume on %s successfully", req.GetStagingTargetPath())
	return &csi.NodeUnstageVolumeResponse{}, nil
}
func (ns *nodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	return &csi.NodeGetInfoResponse{
		NodeId: ns.nodeId,
		// todo: should limit the maxVolumesPerNode through env as disk driver?
		MaxVolumesPerNode: 65535,
	}, nil
}
func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}
func (ns *nodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (ns *nodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func cleanupMountpoint(mounter mountutils.Interface, mountPath string) (err error) {
	forceUnmounter, ok := mounter.(mountutils.MounterForceUnmounter)
	if ok {
		err = mountutils.CleanupMountWithForce(mountPath, forceUnmounter, false, time.Second*30)
	} else {
		err = mountutils.CleanupMountPoint(mountPath, mounter, false)
	}
	return
}
