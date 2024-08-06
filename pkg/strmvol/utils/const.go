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
	KeyTargetRef       = "targetRef"
	KeyFsType          = "fsType"
)

const (
	SecretUsername        = "username"
	SecretPassword        = "password"
	SecretAccessKeyId     = "accessKeyId"
	SecretAccessKeySecret = "accessKeySecret"
)

const (
	DockerAuth = "dockerAuth"
	AliyunAK   = "aliyunAK"
)

const (
	VALUEMUSTHAVE = "MUSTHAVE"
	VALUEOPTIONAL = "OPTIONAL"
)

const (
	VolumeTypeFastImage = "alibaba.fastimage"
	VolumeTypeoverlaybd = "oci.overlaybd"
)

const (
	FsTypeEXT4 = "ext4"
	FsTypeXFS  = "xfs"
)
