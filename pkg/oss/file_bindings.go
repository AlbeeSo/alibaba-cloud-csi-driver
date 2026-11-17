//go:build !windows

package oss

import (
	"context"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/lru"
)

// PVC protection and immutable Pod volume sources keep a bound claim stable
// while this Pod uses it. Scope successful entries by Pod UID, never Pod name.
// This bounded cache needs no informer/list/watch and is cold after CSI restart.
type fileBindingCache struct {
	once   sync.Once
	values *lru.Cache
}

func (ns *nodeServer) validateFileBinding(ctx context.Context, pod *core.Pod, req *csi.NodePublishVolumeRequest, manifest filevolume.Manifest) error {
	_, relative, err := filevolume.Target(req.TargetPath)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	key := relative[:len(relative)-len("/mount")]
	claim := manifest.VolumeClaims[key]
	if claim == "" {
		return status.Error(codes.FailedPrecondition, "mountproxy target has no PVC")
	}
	if ns.clientset == nil {
		return status.Error(codes.Unavailable, "PVC binding validation requires the Kubernetes client")
	}
	ns.fileBindings.once.Do(func() { ns.fileBindings.values = lru.New(4096) })
	cacheKey := string(pod.UID) + "/" + pod.Namespace + "/" + claim
	load := func() (string, error) {
		// NodePublish already holds the existing per-volume operation lock.
		if value, ok := ns.fileBindings.values.Get(cacheKey); ok {
			return value.(string), nil
		}
		pvc, err := ns.clientset.CoreV1().PersistentVolumeClaims(pod.Namespace).Get(ctx, claim, metav1.GetOptions{})
		if err != nil {
			code := codes.Unavailable
			if apierrors.IsNotFound(err) {
				code = codes.FailedPrecondition
			}
			return "", status.Errorf(code, "resolve mountproxy PVC %s/%s binding: %v", pod.Namespace, claim, err)
		}
		if pvc.UID == "" || pvc.DeletionTimestamp != nil || pvc.Status.Phase != core.ClaimBound || pvc.Spec.VolumeName == "" {
			return "", status.Errorf(codes.FailedPrecondition, "mountproxy PVC %s/%s must be Bound and not terminating", pod.Namespace, claim)
		}
		ns.fileBindings.values.Add(cacheKey, pvc.Spec.VolumeName)
		return pvc.Spec.VolumeName, nil
	}
	bound, err := load()
	if err != nil {
		return err
	}
	if bound != key {
		return status.Errorf(codes.FailedPrecondition, "mountproxy target %s maps PVC %s/%s, which is bound to PV %s; fix the sidecar volumeMount mapping", key, pod.Namespace, claim, bound)
	}
	return nil
}
