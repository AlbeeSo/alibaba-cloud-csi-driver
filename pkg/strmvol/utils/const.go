package utils

const (
	ProvSecretNameKey           = "csi.storage.k8s.io/provisioner-secret-name"
	NodeStageSecretNameKey      = "csi.storage.k8s.io/node-stage-secret-name"
	ProvSecretNamespaceKey      = "csi.storage.k8s.io/provisioner-secret-namespace"
	NodeStageSecretNamespaceKey = "csi.storage.k8s.io/node-stage-secret-namespace"
)

const (
	KeyVolumeType      = "volumeType"
	KeySecretName      = "secretName"
	KeySecretNamespace = "secretNamespace"
	KeyReadOnly        = "readOnly"
	KeyTargetType      = "targetType"
	KeyTargetRef       = "targetRef"
	KeyFsType          = "fsType"
)

const (
	VALUEMUSTHAVE = "MUSTHAVE"
	VALUEOPTIONAL = "OPTIONAL"
)

const (
	VolumeTypeFastImage = "fastimage"
	VolumeTypeOverlaydb = "overlaydb"
)

const (
	TargetTypeSnapshot = "snapshot"
	TargetTypeOSS      = "OSS"
	TargetTypeImage    = "image"
)

const (
	FsTypeEXT4 = "ext4"
	FsTypeXFS  = "xfs"
)
