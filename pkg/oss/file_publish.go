//go:build !windows

package oss

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/cloud/metadata"
	ossfpm "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/fuse_pod_manager/oss"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
)

const ossFileRoot = "/host/var/run/ossfs/file-volumes"

func validateFileOptions(opts *ossfpm.Options) error {
	if opts.AuthType != ossfpm.AuthTypeRRSA {
		return fmt.Errorf("guest sidecar requires authType=rrsa; CSI Secret credentials are not delivered to business sidecars (got authType %q)", opts.AuthType)
	}
	return filevolume.ValidateMount(proxy.MountRequest{VolumeID: "validation", Fstype: opts.FuseType, Overlay: opts.Overlay, FdPassing: opts.FdPassing, Recovery: opts.Recovery})
}

// Reuse the Pod fetched by the common wrapper; never query RuntimeClasses.
func (ns *nodeServer) podForDirectPublish(ctx context.Context, req *csi.NodePublishVolumeRequest) (*core.Pod, error) {
	pod, err := utils.GetPodFromContextOrK8s(ctx, ns.clientset, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "resolve Pod for direct mount: %v", err)
	}
	if err := filevolume.ValidatePublishPod(pod, req, ns.nodeName); err != nil {
		return nil, err
	}
	return pod, nil
}

func (ns *nodeServer) publishFileVolume(ctx context.Context, req *csi.NodePublishVolumeRequest, opts *ossfpm.Options, pod *core.Pod) (*csi.NodePublishVolumeResponse, error) {
	if opts.DirectAssigned && (pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == "" || *pod.Spec.RuntimeClassName == utils.RuncRunTimeTag) {
		return nil, status.Error(codes.FailedPrecondition, "file-mode sidecar requires an explicit non-runc RuntimeClass; the PV runtimeclass declaration does not select the Pod runtime")
	}
	fdMode := !opts.DirectAssigned && opts.FuseType == "ossfs2"
	validation := *opts
	secretDirectory := ""
	if !opts.DirectAssigned && opts.AuthType == "" && opts.SecretRef != "" {
		var err error
		keys := []string{mounterutils.KeyAccessKeyId, mounterutils.KeyAccessKeySecret, mounterutils.KeySecurityToken}
		if opts.FuseType == mounterutils.OssFsType {
			keys = append(keys, mounterutils.KeyExpiration)
		}
		secretDirectory, err = filevolume.SecretDirectory(pod, opts.SecretRef, keys)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		validation.AuthType = ossfpm.AuthTypeRRSA
	}
	if fdMode {
		if opts.Recovery {
			if err := filevolume.ValidateRecoveryProjection(pod); err != nil {
				return nil, status.Error(codes.FailedPrecondition, err.Error())
			}
		}
		validation.FdPassing, validation.Recovery = false, false
		opts.FdPassing = true
	}
	if err := validateFileOptions(&validation); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	manifest, err := filevolume.ManifestForPodWithFD(pod, req, fdMode)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "manual mountproxy consumer: %v", err)
	}
	if manifest.ConsumerName != filevolume.SidecarName {
		return nil, status.Error(codes.FailedPrecondition, "OSS volumes require the alibabacloud-csi-mountproxy consumer, not customfuse")
	}
	if err := ns.validateFileBinding(ctx, pod, req, manifest); err != nil {
		return nil, err
	}
	if opts.AuthType == ossfpm.AuthTypeRRSA {
		if err := filevolume.ValidateRRSAToken(pod); err != nil {
			return nil, status.Errorf(codes.FailedPrecondition, "sidecar RRSA: %v", err)
		}
		sa := pod.Spec.ServiceAccountName
		if sa == "" {
			sa = "default"
		}
		if opts.ServiceAccountName != "" && opts.ServiceAccountName != sa {
			return nil, status.Error(codes.FailedPrecondition, "RRSA serviceAccountName must match the business Pod ServiceAccount")
		}
		opts.ServiceAccountName = sa
	}
	manager := ns.fusePodManagers[opts.FuseType]
	if manager == nil {
		return nil, status.Errorf(codes.Internal, "missing %s manager", opts.FuseType)
	}
	if err := checkOssOptions(opts, manager); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	opts.ReadOnly = opts.ReadOnly || req.GetReadonly()
	auth, err := makeAuthConfig(opts, manager, ns.metadata, true)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	options, flags, err := makeMountOptionsAndFlags(opts, manager, ns.metadata, req.VolumeCapability)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	options = manager.AddDefaultMountOptions(options, flags)
	if filevolume.ClientMetricsEnabled(options) {
		if err := filevolume.ValidateClientMetricsProjection(pod, req, fdMode); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	if secretDirectory != "" {
		prefix := filepath.Join(mounterutils.GetConfigDir(opts.FuseType), mounterutils.GetPasswdFileName(opts.FuseType))
		for i, option := range options {
			options[i] = strings.ReplaceAll(option, prefix, secretDirectory)
		}
	}
	// Point the client at the rotating token in its own Pod, never a host token
	// or credentials obtained by the CSI node identity.
	options, err = ossfpm.AppendRRSAAuthOptions(sidecarTokenMetadata{ns.metadata}, options, req.VolumeId, req.TargetPath, auth)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "sidecar RRSA options: %v", err)
	}
	// Reuse the validated inventory instead of scanning/logging the Pod twice.
	if err := filevolume.WriteJSON(filepath.Join(filevolume.Root(ossFileRoot), manifest.PodUID, "manifest.json"), manifest); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "publish Pod volume manifest: %v", err)
	}
	request := proxy.MountRequest{
		// Guest consumers use their own projected identity, never CSI credentials.
		Fstype: opts.FuseType, Source: fmt.Sprintf("%s:%s", opts.MountBucket(), opts.Path), Options: options, MountFlags: flags, FdPassing: fdMode, Recovery: opts.Recovery,
	}
	request.MetricsPath, err = filevolume.WriteMetricsInventory(filevolume.Root(ossFileRoot), req, opts.FuseType, "oss", opts.MountBucket(), opts.RecoveryDegraded)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "prepare sidecar metrics: %v", err)
	}
	if fdMode {
		return ns.publishFDVolume(ctx, req, request, manifest.PodUID)
	}
	err = filevolume.Publish(filevolume.Root(ossFileRoot), req.VolumeContext["csi.storage.k8s.io/pod.uid"], req.VolumeId, req.TargetPath, request)
	if err != nil {
		code := codes.InvalidArgument
		var pathError *os.PathError
		if errors.Is(err, filevolume.ErrConfigurationChanged) {
			code = codes.AlreadyExists
		}
		if errors.As(err, &pathError) {
			code = codes.Internal
		}
		return nil, status.Errorf(code, "publish file configuration: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}

type sidecarTokenMetadata struct{ metadata.MetadataProvider }

func (m sidecarTokenMetadata) Get(key metadata.MetadataKey) (string, error) {
	if key == metadata.RRSATokenFile {
		return filevolume.RRSATokenFile, nil
	}
	return m.MetadataProvider.Get(key)
}
