package internal

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/proto"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type StrmvolClient struct {
	ctx    context.Context
	client proto.StreamingVolumeSerivceClient
	id     string
	ref    string
	parms  map[string]string
}

type BootConfig struct {
	Address  string
	LogLevel string
	Driver   string // 'overlaybd' or 'fastimage' or else
	//RwMode: "overlayfs"
	//LogReportCaller: false
	//WritableLayerType: "append"
}

func getBootConfig(driver string) *BootConfig {
	// get driver from req.parameters
	return &BootConfig{
		Address:  "/run/csi-tool/strmvold.sock",
		LogLevel: "info",
		Driver:   driver,
	}
}

func NewStrmvolClient(ctx context.Context, vc map[string]string) (*StrmvolClient, error) {

	params := convertParamValue(vc)
	id, ref, driver := params[VolumeID], params[ArtifactRef], params[VolumeType]
	// todo: how client define driver
	config := getBootConfig(driver)

	socketFile := filepath.Join(config.Address, id)
	if !utils.IsFileExisting(socketFile) {
		return nil, fmt.Errorf("failed to new a strmvol client, socket file %s not exist", config.Address)
	}

	conn, err := grpc.DialContext(ctx, "unix:"+config.Address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("failed to new a strmvol client, dial socket file %s failed, err: %v", config.Address, err)
	}
	defer conn.Close()
	client := proto.NewStreamingVolumeSerivceClient(conn)
	if client == nil {
		return nil, fmt.Errorf("failed to new a strmvol client, create client failed")
	}
	return &StrmvolClient{
		ctx:    ctx,
		client: client,
		id:     id,
		ref:    ref,
		parms:  vc,
	}, nil
}

func (sc *StrmvolClient) AttachVolume() (*proto.AttachVolumeResponse, error) {

	return sc.client.Attach(sc.ctx, &proto.AttachVolumeRequest{
		Id:       sc.id,
		ImageRef: sc.ref,
		Params:   sc.parms,
	})
}

func (sc *StrmvolClient) DetachVolume() (*proto.DetachVolumeResponse, error) {

	return sc.client.Detach(sc.ctx, &proto.DetachVolumeRequest{
		Id:     sc.id,
		Params: sc.parms,
	})
}

func (sc *StrmvolClient) RemoveVolume() (*proto.RemoveResponse, error) {

	return sc.client.Remove(sc.ctx, &proto.RemoveRequest{
		Id:       sc.id,
		VolumeID: sc.id,
		// Artifact: "",
	})
}
