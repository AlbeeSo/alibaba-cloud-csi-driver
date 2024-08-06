package utils

import (
	"encoding/base64"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
)

func setDefault(vc map[string]string) (modified bool, err error) {
	for key, def := range defaultParams {
		var set bool
		_, ok := vc[key]
		if ok {
			continue
		}
		switch def {
		case VALUEMUSTHAVE:
			return false, fmt.Errorf("field %s is unset", key)
		case VALUEOPTIONAL:
			continue
		}
		if set {
			modified = true
		}
		vc[key] = def
	}
	return modified, nil
}

var defaultParams = map[string]string{
	KeyVolumeType: VolumeTypeFastImage,
	KeyFsType:     FsTypeEXT4,
	KeyTargetRef:  VALUEMUSTHAVE,
	KeyReadOnly:   "true",
	// KeyNewDevice:  "false",
	// KeyNewDeviceCapacity: "20", // set one of KeyNewDeviceCapacity or KeyTargetRef
}

func ValidateRequest(vc map[string]string, targetPath string) error {
	valid, err := utils.CheckRequestArgs(vc)
	if !valid {
		return err
	}
	valid, err = utils.ValidatePath(targetPath)
	if !valid {
		return err
	}
	return nil
}

func ValidateCreateVolumeParams(vc map[string]string) (modified bool, err error) {
	_, ok1 := vc[KeySecretName]
	_, ok2 := vc[KeySecretNamespace]
	if ok1 && ok2 {
		vc[ProvSecretNameKey] = vc[KeySecretName]
		vc[ProvSecretNamespaceKey] = vc[KeySecretNamespace]
		delete(vc, KeySecretName)
		delete(vc, KeySecretNamespace)
	}
	if ok1 || ok2 {
		return false, fmt.Errorf("%s or %s is empty", KeySecretName, KeySecretNamespace)
	}
	return setDefault(vc)
}

func isValueConsistent(key, actual string, vc map[string]string) error {

	if len(actual) == 0 {
		return nil
	}
	val, ok := vc[key]
	if ok && val != actual {
		return fmt.Errorf("%s is not match with volumeAttributes[%s]: %s", actual, key, val)
	}
	vc[key] = actual
	return nil
}

func ValidateNodeStageVolumeParams(req *csi.NodeStageVolumeRequest, vc map[string]string) (modified bool, err error) {
	// recheck volumeType
	id, volumeType := SplitVolumeHandle(req.GetVolumeId())
	if id == "" || volumeType == "" {
		return false, fmt.Errorf("invalid volumeHandle %s, please use <pvName-targetType> instead", req.GetVolumeId())
	}
	if err := isValueConsistent(KeyVolumeType, volumeType, vc); err != nil {
		return false, err
	}

	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return false, fmt.Errorf("volumeCapability is not provided")
	}
	// recheck access mode
	if volCap.GetBlock() != nil {
		// todo: do we support block mode?
		return false, fmt.Errorf("block mode is not supported yet")
	}
	// recheck fs type
	mntVol := volCap.GetMount()
	if mntVol == nil {
		return false, fmt.Errorf("mount is nil within volume capability")
	}
	if err := isValueConsistent(KeyFsType, mntVol.GetFsType(), vc); err != nil {
		return false, err
	}

	return setDefault(vc)
}

func ValidateNodeStageVolumeSecrets(secret map[string]string) (string, string) {
	ak, ok1 := secret[SecretAccessKeyId]
	sk, ok2 := secret[SecretAccessKeySecret]
	if ok1 && ok2 {
		return AliyunAK, base64.StdEncoding.EncodeToString([]byte(ak + ":" + sk))
	}

	usr, ok1 := secret[SecretUsername]
	pwd, ok2 := secret[SecretPassword]
	if ok1 && ok2 {
		return DockerAuth, base64.StdEncoding.EncodeToString([]byte(usr + ":" + pwd))
	}
	return "", ""
}

func ValidateNodePulishVolumeParams(req *csi.NodePublishVolumeRequest) error {
	volCap := req.GetVolumeCapability()
	if volCap == nil {
		return fmt.Errorf("volumeCapability is not provided")
	}
	// recheck access mode
	if volCap.GetBlock() != nil {
		// todo: do we support block mode?
		return fmt.Errorf("block mode is not supported yet")
	}
	// recheck fs type
	mntVol := volCap.GetMount()
	if mntVol == nil {
		return fmt.Errorf("mount is nil within volume capability")
	}
	// no need to modify volume context
	return nil
}
