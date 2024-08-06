package internal

import (
	"fmt"

	svutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/strmvol/utils"
)

// labels defined in strmvold
const (
	// PodName      = "io.kubernetes.pod.name"
	// PodNamespace = "io.kubernetes.pod.namespace"
	ReadOnly = "io.csi.storage/snapvol.readonly"
	// ParentSpec = "io.csi.storage/snapvol.spec.parent"
	VolumeID = "io.csi.storage/snapvol.id" // set by req.VolumeId
	//ActivePath = "io.csi.storage/snapvol.path.active"
	//CommitPath   = "io.csi.storage/snapvol.path.commit"
	NewDevice   = "io.csi.storage/snapvol.device.create"
	VirtualSize = "io.csi.storage/snapvol.device.size"
	FsType      = "io.csi.storage/snapvol.device.fstype"
	ArtifactRef = "io.csi.storage/snapvol.artifact.ref"
	//CommitRef    = "io.csi.storage/snapvol.commit.ref"
	SecretType = "io.csi.storage/snapvol.secret.type"
	SecretData = "io.csi.storage/snapvol.secret.data"
	VolumeType = "io.csi.storage/snapvol.volume.type"
)

func convertParamValue(vc map[string]string) (params map[string]string) {
	for key, val := range vc {
		switch key {
		case svutils.KeyReadOnly:
			add2Map(ReadOnly, val, params)
		//case svutils.KeyNewDeviceCapacity:
		//   add2Map(NewDevice, "true", params)
		//   add2Map(ArtifactRef, "snapshot.volume.base:"+val, params))
		case svutils.KeyFsType:
			add2Map(FsType, val, params)
		case svutils.KeyTargetRef:
			add2Map(ArtifactRef, val, params)
		default:
			add2Map(key, val, params)
		}
	}
	return params
}

func add2Map(key, val string, m map[string]string) error {
	old, ok := m[key]
	if ok {
		return fmt.Errorf("key %s already exists, value %s", key, old)
	}
	m[key] = val
	return nil
}
