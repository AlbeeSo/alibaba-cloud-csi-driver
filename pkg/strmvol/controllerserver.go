package strmvol

import (
	"context"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	svutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	log "github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	controllerCaps = []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
	}
	volumeCaps = []csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
		},
	}
)

type controllerServer struct {
	locks *utils.VolumeLocks
}

func newControllerServer() (*controllerServer, error) {
	c := &controllerServer{
		locks: utils.NewVolumeLocks(),
	}
	return c, nil
}

func (cs *controllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	// todo: only create a PV object now, if create and attach can be devided into 2 operations
	// create a device here if affinity or selector have set
	log.WithField("request", req).Info("CreateVolume: starting")
	vc := req.GetParameters()
	valid, err := utils.CheckRequestArgs(vc)
	if !valid {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	modified, err := svutils.ValidateCreateVolumeParams(vc)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	if modified {
		log.Infof("CreateVolume: parameters have modified to: %v")
	}

	targetType := vc[svutils.KeyTargetType]
	volumeId := svutils.GetVolumeHandle(req.GetName(), targetType)

	if !cs.locks.TryAcquire(volumeId) {
		return nil, status.Errorf(codes.Aborted, "There is already an operation for volume %s", req.Name)
	}
	defer cs.locks.Release(volumeId)

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeId,
			CapacityBytes: req.GetCapacityRange().RequiredBytes,
			VolumeContext: vc,
		},
	}, nil
}
func (cs *controllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	// todo: check if volume is in use when InUse interface has been offered
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	// todo: capacity for new overlaybd device is specified, is it possible to expand it?
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	var caps []*csi.ControllerServiceCapability
	for _, cap := range controllerCaps {
		c := &csi.ControllerServiceCapability{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: cap,
				},
			},
		}
		caps = append(caps, c)
	}
	return &csi.ControllerGetCapabilitiesResponse{Capabilities: caps}, nil
}
func (cs *controllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	// todo: different modes validation for volume types?
	volCaps := req.GetVolumeCapabilities()
	if !utils.IsValidVolumeCapabilities(volCaps, volumeCaps) {
		modes := utils.GetAccessModes(volCaps)
		stringModes := strings.Join(*modes, ", ")
		errString := "Volume capabilities " + stringModes + " not supported."
		return nil, status.Error(codes.InvalidArgument, errString)
	}
	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{VolumeCapabilities: volCaps},
	}, nil
}
func (cs *controllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
func (cs *controllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (cs *controllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}
