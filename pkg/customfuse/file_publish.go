//go:build !windows

package customfuse

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
)

const fileTransportKey = "mountTransport"

var errFileConfigurationChanged = errors.New("file transport configuration changed; unpublish before changing it")

func fileTransport(ctx map[string]string) bool { return ctx[fileTransportKey] == "file" }

func fileRoot() string {
	if root := os.Getenv("CSI_FUSE_FILE_ROOT"); root != "" {
		return root
	}
	// Already host-mounted by the customfuse node DaemonSet.
	return "/host/var/run/customfuse/file-volumes"
}

// The kubelet's publish path is also the identity needed by Unpublish, which
// carries neither PodInfo nor volumeContext. Only filesystem CSI paths qualify.
func fileTarget(target string) (uid, relative string, err error) {
	parts := strings.Split(filepath.Clean(target), string(filepath.Separator))
	for i := 0; i+5 < len(parts); i++ {
		if parts[i] == "pods" && parts[i+2] == "volumes" && parts[i+3] == "kubernetes.io~csi" && i+6 == len(parts) && parts[i+5] == "mount" {
			if !filevolume.ValidComponent(parts[i+1]) || !filevolume.ValidComponent(parts[i+4]) {
				break
			}
			return parts[i+1], parts[i+4] + "/mount", nil
		}
	}
	return "", "", fmt.Errorf("file transport requires a kubelet Pod CSI filesystem target")
}

func publishFile(req *csi.NodePublishVolumeRequest, opts *fuseOptions) error {
	uid, target, err := fileTarget(req.TargetPath)
	if err != nil {
		return err
	}
	if req.VolumeContext["csi.storage.k8s.io/pod.uid"] != uid {
		return fmt.Errorf("PodInfo UID missing or does not match target")
	}
	if opts.AuthType != "" {
		return fmt.Errorf("file demo currently supports Secret passthrough authentication only")
	}
	if req.GetVolumeCapability().GetMount() == nil {
		return fmt.Errorf("file transport requires a filesystem volume")
	}
	d := filevolume.Desired{APIVersion: filevolume.Version, PodUID: uid, Mount: proxy.MountRequest{
		Source: opts.Source, Target: target, Fstype: "customfuse", VolumeID: req.VolumeId,
		Options: opts.makeMountOptions(), Secrets: opts.Secrets,
	}}
	d.Generation = d.Digest()
	if err := d.Validate(uid); err != nil {
		return err
	}
	root := filepath.Join(fileRoot(), uid)
	var old filevolume.Desired
	path := filevolume.RequestPath(root, req.VolumeId)
	if err := filevolume.ReadJSON(path, &old); err == nil {
		if old.Generation != d.Generation {
			return errFileConfigurationChanged
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(req.TargetPath, 0755); err != nil {
		return err
	}
	return filevolume.WriteJSON(path, d)
}
