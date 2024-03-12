/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nas

import (
	csicommon "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/agent/csi-common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/common"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/nas/internal"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/version"
	log "github.com/sirupsen/logrus"
)

const (
	driverName = "nasplugin.csi.alibabacloud.com"
)

// NAS the NAS object
type NAS struct {
	endpoint         string
	identityServer   *identityServer
	controllerServer *controllerServer
	nodeServer       *nodeServer
}

func NewDriver(meta *metadata.Metadata, endpoint, serviceType string) *NAS {
	log.Infof("Driver: %v version: %v", driverName, version.VERSION)

	var d NAS
	d.endpoint = endpoint
	d.identityServer = newIdentityServer(driverName, version.VERSION)

	if serviceType == utils.ProvisionerService {
		config, err := internal.GetControllerConfig(meta)
		if err != nil {
			log.Fatalf("Get nas controller config: %v", err)
		}
		cs, err := newControllerServer(config)
		if err != nil {
			log.Fatalf("Failed to init nas controller server: %v", err)
		}
		d.controllerServer = cs
	} else {
		config, err := internal.GetNodeConfig()
		if err != nil {
			log.Fatalf("Get nas node config: %v", err)
		}
		d.nodeServer = newNodeServer(config)
	}

	return &d
}

func (d *NAS) Run() {
	log.Infof("Starting csi-plugin Driver: %v version: %v", driverName, version.VERSION)
	servers := csicommon.Servers{
		Ids: d.identityServer,
		Cs:  d.controllerServer,
		Ns:  d.nodeServer,
	}
	common.RunCSIServer(d.endpoint, servers)
}
