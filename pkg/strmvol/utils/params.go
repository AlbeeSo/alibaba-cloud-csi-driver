package utils

import (
	"fmt"
)

func setDefault(key string, value string, vc map[string]string) (bool, error) {
	_, ok := vc[key]
	if ok {
		return false, nil
	}
	switch value {
	case VALUEMUSTHAVE:
		return false, fmt.Errorf("field %s is unset", key)
	case VALUEOPTIONAL:
		return false, nil
	}
	vc[key] = value
	return true, nil
}

var defaultCreateVolumeParams = map[string]string{
	KeyVolumeType: VolumeTypeFastImage,
	KeyFsType:     FsTypeEXT4,
	KeyTargetType: TargetTypeOSS,
	KeyTargetRef:  VALUEMUSTHAVE,
	KeyReadOnly:   "true",
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
	for key, def := range defaultCreateVolumeParams {
		var set bool
		set, err = setDefault(key, def, vc)
		if err != nil {
			return false, err
		}
		if set {
			modified = true
		}
	}
	if _, ok := vc[KeySecretName]; ok {
		vc[ProvSecretNameKey] = vc[KeySecretName]
	}
	return
}
