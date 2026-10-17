/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package substrate

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	mount "k8s.io/mount-utils"
)

// Backend is one storage driver the bridge can forward a mount to. The bridge
// itself is driver-agnostic: it reads the publish request the shared generator
// stored in the Actor annotation, routes that request to the backend named by
// its driver, and lets that backend own the resulting mount. Supporting another
// driver means registering another Backend, not editing the routing.
type Backend struct {
	// Driver is the CSI driver name the stored publish request is addressed to,
	// and the key this backend is registered under.
	Driver string
	// Node forwards publish and unpublish to the driver's own node server.
	Node csi.NodeServer
	// OwnsMount reports whether a live mount at a target was created by this
	// backend. Unpublish routes on it, because by then the Actor and its
	// annotation may be gone and only the mount table is left: a live mount no
	// backend owns is the bridge's own Golden placeholder.
	OwnsMount func(*mount.MountPoint) bool
	// Validate refuses a stored request this backend must not be handed, before
	// it is forwarded. Invariants common to every driver are checked separately
	// by the bridge; this covers only what is specific to one driver's mount.
	Validate func(*csi.NodePublishVolumeRequest, ActorInfo) error
	// MountProxySocket, when non-empty, is injected into the forwarded
	// request's PublishContext so the downstream node server uses this
	// socket path for mount-proxy instead of its own default. This is the
	// mechanism that routes FUSE mounts through the sidecar container's
	// mount-proxy-server: the bridge sets the socket path, and the
	// downstream OSS/NAS driver picks it up via ResolveMountProxySocket.
	MountProxySocket string
}

// applyMountProxySocket injects the backend's MountProxySocket into the
// request's PublishContext when the backend has one configured. The
// downstream driver reads this key via ResolveMountProxySocket and uses
// the sidecar's socket instead of a local or pod-level socket.
func (b Backend) applyMountProxySocket(req *csi.NodePublishVolumeRequest) {
	if b.MountProxySocket == "" {
		return
	}
	if req.PublishContext == nil {
		req.PublishContext = make(map[string]string)
	}
	req.PublishContext[mounterutils.MountProxySocketKey] = b.MountProxySocket
}

// NASBackend forwards AgenticFS mounts to the in-process NAS node server.
func NASBackend(node csi.NodeServer) Backend {
	return Backend{
		Driver:    NASDriverName,
		Node:      node,
		OwnsMount: nasMount,
		Validate:  validateNASRequest,
	}
}

// OSSBackend forwards object-store mounts to the in-process OSS node server.
// The OSS driver recognises the substrate-mode marker itself and hands the
// substrate_* options to ossfs, which performs the credential exchange; the
// bridge only routes the request, exactly as it does for NAS.
func OSSBackend(node csi.NodeServer) Backend {
	return Backend{
		Driver:    OSSDriverName,
		Node:      node,
		OwnsMount: ossMount,
		Validate:  validateOSSRequest,
	}
}

// backend returns the registered backend a stored request's driver names.
func (n *Node) backend(driver string) (Backend, bool) {
	b, ok := n.opts.Backends[driver]
	if !ok || b.Node == nil {
		return Backend{}, false
	}
	return b, true
}

// backendForMount returns the backend that owns a live mount, decided from the
// mount table alone so unpublish still works once the Actor is gone.
func (n *Node) backendForMount(mp *mount.MountPoint) (Backend, bool) {
	for _, b := range n.opts.Backends {
		if b.Node == nil || b.OwnsMount == nil {
			continue
		}
		if b.OwnsMount(mp) {
			return b, true
		}
	}
	return Backend{}, false
}
