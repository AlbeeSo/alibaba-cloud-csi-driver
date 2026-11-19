//go:build !windows

package customfuse

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
)

const fileRoot = "/host/var/run/ossfs/file-volumes"

// selectFileConsumer only applies to cluster node service. csi-agent has its
// own explicit socket and direct target lifecycle and does not auto-discover.
func (ns *nodeServer) selectFileConsumer(ctx context.Context, req *csi.NodePublishVolumeRequest) (*core.Pod, error) {
	required, err := filevolume.RequireSidecar(req.VolumeContext)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	runtimeClass, _ := filevolume.Attribute(req.VolumeContext, "runtimeclass") // RequireSidecar checked conflicts.
	guest := strings.EqualFold(runtimeClass, "rund")
	if ns.skipGlobalMount || (!required && !guest && req.VolumeContext[utils.PodNameKey] == "") {
		return nil, nil
	}
	// The common CSI wrapper already fetched this Pod, including cached errors.
	pod, err := utils.GetPodFromContextOrK8s(ctx, nil, req)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "resolve customfuse consumer Pod: %v", err)
	}
	consumer, _, err := filevolume.Consumer(pod)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	if consumer == nil {
		if required || guest {
			return nil, status.Errorf(codes.FailedPrecondition, "customfuse requires sidecar %s", filevolume.CustomSidecarName)
		}
		return nil, nil
	}
	if consumer.Name != filevolume.CustomSidecarName {
		return nil, status.Errorf(codes.FailedPrecondition, "customfuse requires sidecar %s, not the OSS consumer", filevolume.CustomSidecarName)
	}
	if err := filevolume.ValidatePublishPod(pod, req, ns.NodeID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return pod, nil
}

func (ns *nodeServer) publishFileVolume(req *csi.NodePublishVolumeRequest, opts *fuseOptions, pod *core.Pod) (*csi.NodePublishVolumeResponse, error) {
	// RRSA is performed by the customer's entrypoint using this Pod's identity.
	// Never serialize req.Secrets or exchange using the CSI node's credentials.
	secretDirectory := ""
	runtimeClass, err := filevolume.Attribute(req.VolumeContext, "runtimeclass")
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	guest := strings.EqualFold(runtimeClass, "rund")
	if !guest && opts.AuthType == "" && opts.SecretRef != "" {
		var err error
		secretDirectory, err = filevolume.SecretDirectory(pod, opts.SecretRef, nil)
		if err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	if opts.AuthType != "rrsa" && secretDirectory == "" {
		return nil, status.Error(codes.InvalidArgument, "customfuse guest sidecar requires authType=rrsa; Secret credentials are not delivered by CSI")
	}
	for key, value := range req.VolumeContext {
		switch strings.ToLower(key) {
		case "fdpassing", "recovery", "overlay":
			v := strings.TrimSpace(value)
			if v != "" && !strings.EqualFold(v, "false") {
				return nil, status.Errorf(codes.InvalidArgument, "customfuse sidecar does not support %s", key)
			}
		}
	}
	manifest, err := filevolume.ManifestForPod(pod, req)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "customfuse consumer: %v", err)
	}
	if opts.AuthType == "rrsa" {
		if err := filevolume.ValidateRRSAToken(pod); err != nil {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
	}
	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	if opts.ServiceAccountName != "" && opts.ServiceAccountName != sa {
		return nil, status.Error(codes.FailedPrecondition, "customfuse serviceAccountName must match the business Pod")
	}
	options := opts.makeMountOptions()
	if opts.AuthType == "rrsa" {
		options = append(options, "rrsa_token_file="+filevolume.RRSATokenFile)
	}
	if secretDirectory != "" {
		options = append(options, "credential_dir="+secretDirectory)
	}
	// Keep the client's labels/source format independent of the dispatch type.
	options = append(options, "fuseType="+opts.FuseType)
	root := filevolume.Root(fileRoot)
	if err := filevolume.WriteJSON(filepath.Join(root, manifest.PodUID, "manifest.json"), manifest); err != nil {
		return nil, status.Errorf(codes.Internal, "publish customfuse inventory: %v", err)
	}
	metrics, err := filevolume.WriteMetricsInventory(root, req, opts.FuseType, "customfuse", opts.metricsBucketName(), false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "customfuse metrics: %v", err)
	}
	if err := filevolume.Publish(root, string(pod.UID), req.VolumeId, req.TargetPath, proxy.MountRequest{Fstype: "customfuse", Source: opts.Source, Options: options, MetricsPath: metrics}); err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "publish customfuse configuration: %v", err)
	}
	return &csi.NodePublishVolumeResponse{}, nil
}
