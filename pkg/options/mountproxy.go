// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package options

const DefaultNASMountProxySocket = "/run/cnfs/alinas-mounter.sock"

func ResolveNASMountProxySocket(nasSocket, sharedSocket string, featureEnabled bool) string {
	if nasSocket != "" {
		return nasSocket
	}
	if sharedSocket != "" {
		return sharedSocket
	}
	if featureEnabled {
		return DefaultNASMountProxySocket
	}
	return ""
}
