package server

import (
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/diagnostic"
	"io"
)

type switchableWriter = diagnostic.Writer

func NewSwitchableWriter(initial io.Writer, secrets ...map[string]string) *switchableWriter {
	var values map[string]string
	if len(secrets) > 0 {
		values = secrets[0]
	}
	return diagnostic.NewWriter(initial, values)
}
