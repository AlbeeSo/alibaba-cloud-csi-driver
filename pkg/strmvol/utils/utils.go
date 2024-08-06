package utils

import (
	"fmt"
	"os"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
)

func CreateDir(path string) error {
	err := os.MkdirAll(path, os.FileMode(0755))
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func GetVolumeHandle(volumeId, targetType string) string {
	return fmt.Sprintf("%s-%s", volumeId, targetType)
}

func SplitVolumeHandle(volumeHandle string) (volumeId, targetType string) {
	lastIdx := strings.LastIndex(volumeHandle, "-")
	if lastIdx == -1 {
		return "", ""
	}
	return volumeHandle[:lastIdx], volumeHandle[lastIdx+1:]
}

func GetFsTypeAndOptions(req *csi.NodePublishVolumeRequest) (fsType string, mountOptions []string) {
	vc := req.GetVolumeContext()
	fs := req.GetVolumeCapability().GetMount().GetFsType()
	mountOptions = req.GetVolumeCapability().GetMount().GetMountFlags()

	fsType = FsTypeEXT4
	if vc != nil {
		val, ok := vc[KeyFsType]
		if ok && val != "" {
			fsType = val
		}
	}
	if fs != "" {
		fsType = fs
	}
	mountOptions = append(mountOptions, "bind")
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}
	mountOptions = utils.CollectMountOptions(fsType, mountOptions)
	return
}
