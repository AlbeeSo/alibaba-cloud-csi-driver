//go:build !windows

package oss

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
)

// This measures the cache hit path only, not API/network or full NodePublish.
// The request counter prevents a future refactor silently adding warm GETs.
func BenchmarkFileBindingWarm(b *testing.B) {
	pod := manualFilePod("rund", 1)
	client := fileTestClient(pod)
	ns := &nodeServer{clientset: client}
	req := &csi.NodePublishVolumeRequest{TargetPath: "/var/lib/kubelet/pods/pod-a/volumes/kubernetes.io~csi/pv-0/mount", VolumeContext: map[string]string{"csi.storage.k8s.io/pod.uid": "pod-a"}}
	manifest, err := filevolume.ManifestForPod(pod, req)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := ns.validateFileBinding(ctx, pod, req, manifest); err != nil {
		b.Fatal(err)
	}
	client.ClearActions()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := ns.validateFileBinding(ctx, pod, req, manifest); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if len(client.Actions()) != 0 {
		b.Fatal("warm validation unexpectedly contacted the API")
	}
}

// Complete metadata works without a client or cache, including the cold path.
func BenchmarkFileBindingMetadata(b *testing.B) {
	pod := manualFilePod("rund", 1)
	ns := &nodeServer{}
	req := &csi.NodePublishVolumeRequest{TargetPath: "/var/lib/kubelet/pods/pod-a/volumes/kubernetes.io~csi/pv-0/mount", VolumeContext: map[string]string{"csi.storage.k8s.io/pod.uid": "pod-a"}}
	setFileBindingMetadata(req, pod.Namespace, "pv-0", "claim-0")
	manifest, err := filevolume.ManifestForPod(pod, req)
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := ns.validateFileBinding(ctx, pod, req, manifest); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if ns.fileBindings.values != nil {
		b.Fatal("metadata validation unexpectedly initialized the fallback cache")
	}
}
