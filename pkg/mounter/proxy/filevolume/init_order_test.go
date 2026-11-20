package filevolume

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

func TestPrecedingInitPVCLogs(t *testing.T) {
	for _, scenario := range []string{"first", "emptydir", "unrelated-pvc", "shared-pvcs"} {
		t.Run(scenario, func(t *testing.T) {
			state := klog.CaptureState()
			t.Cleanup(state.Restore)
			var info, warning bytes.Buffer
			klog.LogToStderr(false)
			klog.SetOutputBySeverity("INFO", &info)
			klog.SetOutputBySeverity("WARNING", &warning)
			volume := func(name, claim string) core.Volume {
				return core.Volume{Name: name, VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: claim}}}
			}
			volumes := map[string]core.Volume{"data-a": volume("data-a", "claim-a"), "alias-a": volume("alias-a", "claim-a"), "data-b": volume("data-b", "claim-b"), "other": volume("other", "claim-other"), "scratch": {Name: "scratch", VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{}}}}
			init := func(name string, mounts ...string) core.Container {
				c := core.Container{Name: name, Env: []core.EnvVar{{Name: "PRIVATE_VALUE", Value: "must-not-appear-in-order-logs"}}}
				for _, name := range mounts {
					c.VolumeMounts = append(c.VolumeMounts, core.VolumeMount{Name: name})
				}
				return c
			}
			pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "pod", Namespace: "test"}}
			sidecar := core.Container{Name: "arbitrary-sidecar-name"}
			index := 1
			switch scenario {
			case "first":
				index = 0
				pod.Spec.InitContainers = []core.Container{sidecar, init("later", "data-a")}
			case "emptydir":
				pod.Spec.InitContainers = []core.Container{init("prepare", "scratch"), sidecar}
			case "unrelated-pvc":
				pod.Spec.InitContainers = []core.Container{init("prepare", "other", "other"), sidecar}
			case "shared-pvcs":
				index = 2
				pod.Spec.InitContainers = []core.Container{init("prepare-a", "data-a", "alias-a", "other"), init("prepare-b", "data-b", "other"), sidecar}
			}
			err := checkPrecedingInitPVCs(pod, index, volumes, map[string]bool{"claim-a": true, "claim-b": true})
			klog.Flush()
			if scenario == "shared-pvcs" {
				require.ErrorContains(t, err, "must precede init container prepare-a")
				require.Contains(t, warning.String(), "pvcCount=3 pvcs=[claim-a claim-b claim-other]")
				require.Contains(t, warning.String(), "initContainers=[prepare-a prepare-b] managedPVCCount=2 managedPVCs=[claim-a claim-b]")
			} else {
				require.NoError(t, err)
			}
			if index > 0 {
				require.Contains(t, info.String(), "not the first init container; checking preceding PVC references")
				if scenario == "unrelated-pvc" {
					require.Contains(t, warning.String(), "pvcCount=1 pvcs=[claim-other]")
					require.Contains(t, warning.String(), "managedPVCCount=0 managedPVCs=[]")
				}
			} else {
				require.Empty(t, info.String())
			}
			if scenario == "first" || scenario == "emptydir" {
				require.Empty(t, warning.String())
			}
			require.NotContains(t, info.String()+warning.String(), "must-not-appear-in-order-logs")
			if warning.Len() > 0 {
				require.True(t, strings.HasPrefix(warning.String(), "W"), "must be warning severity")
			}
		})
	}
}
