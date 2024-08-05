package strmvol

import (
	"context"
	"reflect"
	"testing"

	svutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/assert"

	csi "github.com/container-storage-interface/spec/lib/go/csi"
)

func newMockControllerServer() *controllerServer {
	return &controllerServer{
		locks: utils.NewVolumeLocks(),
	}
}

func Test_CreateVolume(t *testing.T) {
	cs := newMockControllerServer()

	createVolumeReq := &csi.CreateVolumeRequest{
		Name: "test-volume",
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1024 * 1024 * 1024, // 1 GiB
		},
		Parameters: map[string]string{
			svutils.KeySecretName:      "test-secret",
			svutils.KeySecretNamespace: "test-namespace",
			svutils.KeyTargetRef:       "test-target-ref",
		},
	}
	wantPv := &csi.Volume{
		VolumeId:      "test-volume-" + svutils.TargetTypeOSS,
		CapacityBytes: 1024 * 1024 * 1024,
		VolumeContext: map[string]string{
			svutils.KeyVolumeType:          svutils.VolumeTypeFastImage,
			svutils.KeyFsType:              svutils.FsTypeEXT4,
			svutils.KeyTargetType:          svutils.TargetTypeOSS,
			svutils.KeyTargetRef:           "test-target-ref",
			svutils.KeyReadOnly:            "true",
			svutils.ProvSecretNameKey:      "test-secret",
			svutils.ProvSecretNamespaceKey: "test-namespace",
		},
	}
	gotPv, err := cs.CreateVolume(context.Background(), createVolumeReq)
	assert.Nil(t, err)
	assert.True(t, reflect.DeepEqual(gotPv, wantPv))
}

func Test_ControllerGetCapabilities(t *testing.T) {
	cs := newMockControllerServer()

	resp, err := cs.ControllerGetCapabilities(context.Background(), &csi.ControllerGetCapabilitiesRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, len(controllerCaps), len(resp.Capabilities))
	for i, cap := range controllerCaps {
		assert.Equal(t, cap, (resp.Capabilities[i].Type).(*csi.ControllerServiceCapability_Rpc).Rpc.Type)
	}
}

func Test_ValidateVolumeCapabilities(t *testing.T) {
	cs := newMockControllerServer()
	assert.NotNil(t, cs)

	confirmedReq := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
			},
		},
	}
	confirmedResp, err := cs.ValidateVolumeCapabilities(context.Background(), confirmedReq)
	assert.NoError(t, err)
	assert.NotNil(t, confirmedResp)
	assert.Equal(t, confirmedReq.VolumeCapabilities, confirmedResp.Confirmed.VolumeCapabilities)

	notConfirmedReq := &csi.ValidateVolumeCapabilitiesRequest{
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
				},
			},
		},
	}
	notConfirmedResp, err := cs.ValidateVolumeCapabilities(context.Background(), notConfirmedReq)
	assert.NoError(t, err)
	assert.NotNil(t, notConfirmedResp)
	assert.Equal(t, csi.ValidateVolumeCapabilitiesResponse{}, *notConfirmedResp)
}
