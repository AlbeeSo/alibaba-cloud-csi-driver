package filevolume

import (
	"fmt"
	"path"

	core "k8s.io/api/core/v1"
)

const RRSATokenDir = "/var/run/secrets/ack.alibabacloud.com/rrsa-tokens"
const RRSATokenFile = RRSATokenDir + "/token"

// A directory projection (no subPath) lets kubelet rotate the token atomically.
// The client performs OIDC/STS validation; CSI never reads or serializes JWTs.
func ValidateRRSAToken(pod *core.Pod) error {
	for _, c := range pod.Spec.InitContainers {
		if !fileContainer(c) {
			continue
		}
		for _, vm := range c.VolumeMounts {
			if vm.MountPath != RRSATokenDir {
				continue
			}
			if !vm.ReadOnly || vm.SubPath != "" || vm.SubPathExpr != "" {
				return fmt.Errorf("RRSA token must be a read-only directory projection without subPath")
			}
			for _, v := range pod.Spec.Volumes {
				if v.Name != vm.Name || v.Projected == nil {
					continue
				}
				for _, source := range v.Projected.Sources {
					if token := source.ServiceAccountToken; token != nil && path.Clean(token.Path) == "token" && token.Audience == "sts.aliyuncs.com" {
						return nil
					}
				}
			}
		}
	}
	return fmt.Errorf("mountproxy requires a projected ServiceAccount token with audience sts.aliyuncs.com at %s/token", RRSATokenDir)
}
