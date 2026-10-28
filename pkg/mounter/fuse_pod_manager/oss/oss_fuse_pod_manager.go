package oss

import (
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	fpm "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fuse_pod_manager"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type OSSFuseMounterType interface {
	fpm.FuseMounterType
	PrecheckAuthConfig(o *Options, onNode bool) error
	MakeAuthConfig(o *Options, m metadata.MetadataProvider) (*fpm.AuthConfig, error)
	MakeMountOptions(o *Options, m metadata.MetadataProvider) ([]string, error)
}

type OSSFusePodManager struct {
	fpm.FusePodManager
	OSSFuseMounterType
}

func NewOSSFusePodManager(fuseType OSSFuseMounterType, client kubernetes.Interface, constrainResourceVersion bool) *OSSFusePodManager {
	manager := fpm.NewFusePodManager(fuseType, client, constrainResourceVersion)
	return &OSSFusePodManager{
		*manager,
		fuseType,
	}
}

// SigVersion is the version of signature for ossfs
type SigVersion string

const (
	// default version for ossfs
	SigV1 SigVersion = "v1"
	// need set region
	SigV4 SigVersion = "v4"
)

const (
	EncryptedTypeKms    = "kms"
	EncryptedTypeAes256 = "aes256"
)

const (
	AuthTypeSTS           = "sts"
	AuthTypeRRSA          = "rrsa"
	AuthTypeCSS           = "csi-secret-store"
	AuthTypePublic        = "public"
	AuthTypeAgentIdentity = "agent-identity"
)

type AccessKey struct {
	AkID     string `json:"akId"`
	AkSecret string `json:"akSecret"`
}
type TokenSecret struct {
	AccessKeyId     string `json:"accessKeyId"`
	AccessKeySecret string `json:"accessKeySecret"`
	Expiration      string `json:"expiration"`
	SecurityToken   string `json:"securityToken"`
}

// Options contains options for target oss
type Options struct {
	// DirectAssigned indicates the volume should be directly assigned to the pod.
	// When DirectAssigned is True, it means the runtime is either rund or coco:
	// - Controller server should skip the controller publish phase
	// - Node server should check the skipGlobalMount field to determine if it's rund (skipGlobalMount=true) or coco (skipGlobalMount=false)
	// Otherwise, the runtime is runc
	DirectAssigned bool
	CNFSName       string

	// oss options
	// normal bucket
	Bucket string `json:"bucket"`
	// agentic bucket
	AgenticBucket     string `json:"agenticBucket"`
	BucketSpace       string `json:"bucketSpace"`
	BucketSpacePrefix string `json:"bucketSpacePrefix"`

	URL  string `json:"url"`
	Path string `json:"path"`

	// authorization options
	// accesskey
	AccessKey   `json:",inline"`
	TokenSecret `json:",inline"`
	SecretRef   string `json:"secretRef"`
	// RRSA
	RoleName           string `json:"roleName"` // also for STS
	RoleArn            string `json:"roleArn"`
	OidcProviderArn    string `json:"oidcProviderArn"`
	ServiceAccountName string `json:"serviceAccountName"`
	// assume role
	AssumeRoleArn string `json:"assumeRoleArn"`
	ExternalId    string `json:"externalId"`
	// csi secret store
	SecretProviderClass string `json:"secretProviderClass"`
	// agent identity
	SandboxId               string `json:"sandboxId"`
	SandboxCredProviderName string `json:"sandboxCredProviderName"`

	// ossfs options
	OtherOpts  string     `json:"otherOpts"`
	MetricsTop string     `json:"metricsTop"`
	Encrypted  string     `json:"encrypted"`
	KmsKeyId   string     `json:"kmsKeyId"`
	Region     string     `json:"region"`
	SigVersion SigVersion `json:"sigVersion"`

	// mount options
	UseSharedPath bool   `json:"useSharedPath"`
	AuthType      string `json:"authType"`
	FuseType      string `json:"fuseType"`

	// ReadOnly indicates the volume is read-only at the *volume* level, driven
	// solely by the CSI access mode (SINGLE_NODE_READER_ONLY /
	// MULTI_NODE_READER_ONLY). It MUST be honored by the shared attachPath in
	// RunC because every pod on the node binds the same ossfs mount — an
	// access-mode-ro volume cannot be made writable by any pod.
	ReadOnly bool `json:"readOnly"`

	// PodReadOnly indicates the *current pod* requested read-only access via
	// volumeMounts.readOnly (NodePublishVolumeRequest.Readonly). This is a
	// per-pod, per-mount semantic that MUST NOT leak into the shared RunC
	// attachPath: doing so would poison the ossfs daemon options for all
	// subsequent pods on the node (a ro pod followed by an rw pod would leave
	// the rw pod silently read-only).
	//
	// kubelet/CRI already enforces per-pod ro at the *container* bind mount
	// (CRI Mount.Readonly, set from csiMountMgr.GetAttributes().ReadOnly), so
	// the driver does not need to remount the targetPath ro itself. The driver
	// only consumes PodReadOnly where the ossfs daemon itself must be ro:
	//   - RunD/MicroVM per-pod ossfs (isolated, no sharing, safe to honor)
	//   - overlay lower-dir requires a ro ossfs (ValidateOverlayRequirements)
	//
	// In the RunC shared-attachPath path, NodePublishVolume clears this field
	// before building attachPath mount options (see nodeserver.go).
	PodReadOnly bool `json:"podReadOnly"`

	// overlay options
	Overlay bool `json:"overlay"`

	// pod template
	DnsPolicy corev1.DNSPolicy `json:"dnsPolicy"`

	// recovery
	FdPassing        bool `json:"fdPassing"`
	Recovery         bool `json:"recovery"`
	RecoveryDegraded bool `json:"-"`
}

// MountBucket returns the effective bucket name for mounting.
// BucketSpace and Bucket are mutually exclusive (enforced by validation).
// In AgenticBucket mode, BucketSpace is used; otherwise Bucket.
// If BucketSpacePrefix was specified, it has already been resolved into BucketSpace
// by resolveBucketSpacePrefix before this method is called.
func (o *Options) MountBucket() string {
	if o.BucketSpace != "" {
		return o.BucketSpace
	}
	return o.Bucket
}
