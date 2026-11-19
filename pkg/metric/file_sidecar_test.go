//go:build !windows

package metric

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/interceptors"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/sets"
)

func TestFileMetricsAreScopedToConfiguredDriver(t *testing.T) {
	root := t.TempDir()
	old := fuseMetricDirs
	fuseMetricDirs = map[string][]string{"ossfs": {root}}
	t.Cleanup(func() { fuseMetricDirs = old })
	for _, backend := range []string{"oss", "customfuse"} {
		req := &csi.NodePublishVolumeRequest{VolumeId: backend, TargetPath: "/pods/" + backend + "/volumes/kubernetes.io~csi/pv/mount", VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": "consumer", "csi.storage.k8s.io/pod.namespace": "test"}}
		rel, err := filevolume.WriteMetricsInventory(filepath.Join(root, "file-volumes"), req, "client", backend, "filesystem", false)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "file-volumes", backend, rel, utils.MetricsMountPointStatus), []byte("1"), 0600))
	}
	for _, test := range []struct{ driver, volume string }{{ossDriverName, "oss"}, {customfuseDriverName, "customfuse"}} {
		collector, err := NewFuseStatCollector()
		require.NoError(t, err)
		collector.(*usFsStatCollector).fileDrivers = sets.New(test.driver)
		ch := make(chan prometheus.Metric, 10)
		require.NoError(t, collector.Update(context.Background(), sets.New[string](), ch))
		close(ch)
		count := 0
		for metric := range ch {
			count++
			var value dto.Metric
			require.NoError(t, metric.Write(&value))
			for _, label := range value.Label {
				if label.GetName() == "pv" {
					require.Equal(t, test.volume, label.GetValue())
				}
			}
		}
		require.Equal(t, 1, count, "separate OSS/customfuse DaemonSets must not both export the same file consumer")
	}
}

func TestFileSidecarMonitorThroughExistingCollector(t *testing.T) {
	root := t.TempDir()
	old := fuseMetricDirs
	fuseMetricDirs = map[string][]string{"ossfs": {root}}
	t.Cleanup(func() { fuseMetricDirs = old })
	req := &csi.NodePublishVolumeRequest{VolumeId: "volume-handle", TargetPath: "/var/lib/kubelet/pods/uid/volumes/kubernetes.io~csi/pv/mount", VolumeContext: map[string]string{"csi.storage.k8s.io/pod.name": "consumer", "csi.storage.k8s.io/pod.namespace": "test"}}
	rel, err := filevolume.WriteMetricsInventory(filepath.Join(root, "file-volumes"), req, "ossfs2", "oss", "bucket", true)
	require.NoError(t, err)
	dir := filepath.Join(root, "file-volumes", "uid", rel)
	exit := make(chan error, 1)
	op := &mounter.MountOperation{Target: filepath.Join(t.TempDir(), "target"), MetricsPath: dir}
	err = interceptors.FuseMonitorInterceptor(context.Background(), op, func(context.Context, *mounter.MountOperation) error {
		op.MountResult = server.FuseMountResult{PID: 123, ExitChan: exit}
		return nil
	})
	require.NoError(t, err)
	collector, err := NewFuseStatCollector()
	require.NoError(t, err)
	collect := func() map[string]float64 {
		ch := make(chan prometheus.Metric, 128)
		require.NoError(t, collector.Update(context.Background(), sets.New[string](), ch))
		close(ch)
		values := map[string]float64{}
		for metric := range ch {
			var value dto.Metric
			require.NoError(t, metric.Write(&value))
			labels := map[string]string{}
			for _, label := range value.Label {
				labels[label.GetName()] = label.GetValue()
			}
			require.Equal(t, "consumer", labels["pod"])
			require.Equal(t, "volume-handle", labels["pv"])
			var number float64
			if value.Gauge != nil {
				number = value.Gauge.GetValue()
			} else if value.Counter != nil {
				number = value.Counter.GetValue()
			}
			values[metric.Desc().String()] = number
		}
		return values
	}
	require.NotEmpty(t, collect(), "must emit real existing collector metrics")
	op.OnProcessExit(fmt.Errorf("daemon crash"))
	op.OnRecoverySuccess(124, fmt.Errorf("daemon crash"), 1)
	values := collect()
	foundDegraded, foundFailover := false, false
	for name, value := range values {
		if strings.Contains(name, utils.MetricsRecoveryDegraded) {
			foundDegraded = true
			require.Equal(t, float64(1), value)
		}
		if strings.Contains(name, "failover") {
			foundFailover = true
			require.Equal(t, float64(1), value)
		}
	}
	require.True(t, foundDegraded)
	require.True(t, foundFailover)
	exit <- fmt.Errorf("terminal failure")
	close(exit)
	worker := op.MountResult.(server.FuseMountResult).ExitChan
	select {
	case err := <-worker:
		require.ErrorContains(t, err, "terminal failure")
	case <-time.After(time.Second):
		t.Fatal("monitor consumed the worker's exit outcome")
	}
}
