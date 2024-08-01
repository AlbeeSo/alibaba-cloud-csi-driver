package strmvol

import (
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
	log "github.com/sirupsen/logrus"
)

// PluginFoles defines the location of strmvolplugin
const (
	driverName = "strmvolplugin.csi.alibabacloud.com"
)

type STRMVOL struct {
	endpoint         string
	identityServer   *identityServer
	controllerServer *controllerServer
	nodeServer       *nodeServer
}

func NewDriver(meta *metadata.Metadata, endpoint, serviceType string) *STRMVOL {
	log.Infof("Driver: %v version: %v", driverName, version.VERSION)

	var s STRMVOL
	s.endpoint = endpoint
	s.identityServer = newIdentityServer(driverName, version.VERSION)

	if serviceType == utils.ProvisionerService {
		cs, err := newControllerServer()
		if err != nil {
			log.Fatalf("Failed to initialize streaming-volume controller service: %v", err)
		}
		s.controllerServer = cs
	} else {
		ns, err := newNodeServer()
		if err != nil {
			log.Fatalf("Failed to initialize streaming-volume node service: %v", err)
		}
		s.nodeServer = ns
	}
	return &s
}

func (s *STRMVOL) Run() {
	log.Infof("Starting strmvol driver service, endpoint: %s", s.endpoint)
	common.RunCSIServer(s.endpoint, s.identityServer, s.controllerServer, s.nodeServer)
}
