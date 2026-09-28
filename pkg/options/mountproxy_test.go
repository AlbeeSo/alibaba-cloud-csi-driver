// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package options

import "testing"

func TestResolveNASMountProxySocket(t *testing.T) {
	for _, tc := range []struct {
		nas, shared string
		gate        bool
		want        string
	}{
		{"", "", false, ""},
		{"", "", true, "/run/cnfs/alinas-mounter.sock"},
		{"", "/shared.sock", true, "/shared.sock"},
		{"/nas.sock", "/shared.sock", true, "/nas.sock"},
		{"/nas.sock", "", false, "/nas.sock"},
	} {
		if got := ResolveNASMountProxySocket(tc.nas, tc.shared, tc.gate); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
