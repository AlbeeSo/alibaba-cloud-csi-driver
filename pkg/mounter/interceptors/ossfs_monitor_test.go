package interceptors

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOssfsMonitorInterceptor(t *testing.T) {
	metricsDir := t.TempDir()
	tests := []struct {
		name      string
		handler   mounter.MountHandler
		op        *mounter.MountOperation
		expectErr bool
	}{
		{
			name:    "nil operation",
			handler: successMountHandler,
		},
		{
			name:    "nil metrics path",
			handler: successMountHandler,
			op:      &mounter.MountOperation{},
		},
		{
			name:    "mount error reservation",
			handler: failureMountHandler,
			op: &mounter.MountOperation{
				MetricsPath: metricsDir,
			},
			expectErr: true,
		},
		{
			name:    "nil mount result",
			handler: successMountHandler,
			op: &mounter.MountOperation{
				MetricsPath: metricsDir,
				Target:      "target1",
			},
		},
		{
			name:    "invalid mount result",
			handler: successMountHandler,
			op: &mounter.MountOperation{
				MetricsPath: metricsDir,
				MountResult: "invalid",
				Target:      "target2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := OssfsMonitorInterceptor(context.Background(), tt.op, tt.handler)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			if tt.op == nil || tt.op.MetricsPath == "" {
				return
			}

			monitor, found := monitorManager.GetMountMonitor(tt.op.Target, tt.op.MetricsPath, raw, false)
			assert.True(t, found)
			assert.NotNil(t, monitor)

		})
	}

	monitorManager.StopAllMonitoring()
	monitorManager.WaitForAllMonitoring()
	monitorManager = server.NewMountMonitorManager()
	defer func() {
		monitorManager.StopAllMonitoring()
		monitorManager.WaitForAllMonitoring()
	}()

	op := &mounter.MountOperation{
		Target:      "volume1",
		MetricsPath: metricsDir,
		MountResult: server.OssfsMountResult{
			PID:      123,
			ExitChan: make(chan error),
		},
	}
	err := OssfsMonitorInterceptor(context.Background(), op, successMountHandler)
	assert.NoError(t, err)
	monitor, found := monitorManager.GetMountMonitor(op.Target, op.MetricsPath, raw, false)
	assert.True(t, found)
	assert.NotNil(t, monitor)
	assertMountMetricValue(t, op.MetricsPath, utils.MetricsMountPointStatus, "0")
	assertMountMetricValue(t, op.MetricsPath, utils.MetricsMountRetryCount, "0")

	err = OssfsMonitorInterceptor(context.Background(), op, failureMountHandler)
	assert.Error(t, err)
	assertMountMetricValue(t, op.MetricsPath, utils.MetricsMountPointStatus, "1")
	assertMountMetricValue(t, op.MetricsPath, utils.MetricsMountRetryCount, "1")
}

func assertMountMetricValue(t *testing.T, metricsDir, metricsFile string, expected string) {
	actual, err := os.ReadFile(filepath.Join(metricsDir, metricsFile))
	assert.NoError(t, err)
	assert.Equal(t, expected, string(actual))
}

// TestOssfsMonitorInterceptor_MountFailureWithProcessExit tests the scenario where mount fails but process has started
func TestOssfsMonitorInterceptor_MountFailureWithProcessExit(t *testing.T) {
	// Use a fresh monitor manager for this test to avoid conflicts
	originalManager := monitorManager
	monitorManager = server.NewMountMonitorManager()
	defer func() {
		monitorManager.StopAllMonitoring()
		monitorManager.WaitForAllMonitoring()
		monitorManager = originalManager
	}()

	metricsDir := t.TempDir()
	target := "test-target-failure"

	// Create ExitChan to simulate process exit
	exitChan := make(chan error, 1)

	// Simulate mount failure but process has started
	mountErr := errors.New("mount timeout")
	handler := func(ctx context.Context, op *mounter.MountOperation) error {
		// Set MountResult to simulate process has started
		op.MountResult = server.OssfsMountResult{
			PID:      12345,
			ExitChan: exitChan,
		}
		return mountErr // Return mount error
	}

	op := &mounter.MountOperation{
		Target:      target,
		MetricsPath: metricsDir,
	}

	// Execute interceptor
	err := OssfsMonitorInterceptor(context.Background(), op, handler)
	assert.Error(t, err)
	assert.Equal(t, mountErr, err)

	// Verify first call to HandleMountFailureOrExit (on mount failure)
	monitor, found := monitorManager.GetMountMonitor(target, metricsDir, raw, false)
	require.True(t, found)
	require.NotNil(t, monitor)

	// Read retryCount
	// retryCount represents "retry count", not "failure count".
	// The first mount attempt is not a retry, so retryCount = 0.
	// HandleMountFailureOrExit only writes the current retryCount value, it doesn't increment it.
	retryCountFile := filepath.Join(metricsDir, utils.MetricsMountRetryCount)
	retryCountContent, err := os.ReadFile(retryCountFile)
	require.NoError(t, err)
	assert.Equal(t, "0", string(retryCountContent), "retryCount should be 0 after first mount failure (first attempt is not a retry)")

	// Verify status is unhealthy
	statusFile := filepath.Join(metricsDir, utils.MetricsMountPointStatus)
	statusContent, err := os.ReadFile(statusFile)
	require.NoError(t, err)
	assert.Equal(t, "1", string(statusContent), "status should be unhealthy (1)")

	// Simulate process exit (with error)
	processExitErr := errors.New("process exited with error")
	exitChan <- processExitErr
	close(exitChan)

	// Wait for goroutine to process
	time.Sleep(100 * time.Millisecond)

	// Verify HandleMountFailureOrExit is NOT called again (on process exit)
	// When mount fails (err != nil), we don't call HandleMountFailureOrExit again on process exit
	// The condition "err == nil && exitErr != nil" is false because err != nil
	retryCountContent2, err := os.ReadFile(retryCountFile)
	require.NoError(t, err)
	// retryCount = 0 because this is the first mount attempt (not a retry).
	// HandleMountFailureOrExit only writes the current retryCount value, it doesn't increment it.
	// When the process exits, we don't call HandleMountFailureOrExit again (because err != nil),
	// so retryCount remains 0.
	assert.Equal(t, "0", string(retryCountContent2), "retryCount should remain 0 after process exit")
}

// TestOssfsMonitorInterceptor_ProcessExitAfterSuccess tests the scenario where process exits after successful mount
func TestOssfsMonitorInterceptor_ProcessExitAfterSuccess(t *testing.T) {
	// Use a fresh monitor manager for this test to avoid conflicts
	originalManager := monitorManager
	monitorManager = server.NewMountMonitorManager()
	defer func() {
		monitorManager.StopAllMonitoring()
		monitorManager.WaitForAllMonitoring()
		monitorManager = originalManager
	}()

	metricsDir := t.TempDir()
	target := "test-target-exit-after-success"

	exitChan := make(chan error, 1)

	handler := func(ctx context.Context, op *mounter.MountOperation) error {
		op.MountResult = server.OssfsMountResult{
			PID:      12345,
			ExitChan: exitChan,
		}
		return nil // Mount succeeded
	}

	op := &mounter.MountOperation{
		Target:      target,
		MetricsPath: metricsDir,
	}

	err := OssfsMonitorInterceptor(context.Background(), op, handler)
	assert.NoError(t, err)

	// Simulate abnormal process exit
	processExitErr := errors.New("process crashed")
	exitChan <- processExitErr
	close(exitChan)

	// Wait for goroutine to process
	time.Sleep(100 * time.Millisecond)

	// Verify HandleMountFailureOrExit is called (on process exit)
	statusFile := filepath.Join(metricsDir, utils.MetricsMountPointStatus)
	statusContent, err := os.ReadFile(statusFile)
	require.NoError(t, err)
	assert.Equal(t, "1", string(statusContent), "status should be unhealthy (1) after process exit")
}

// TestOssfsMonitorInterceptor_RetryCount tests retry count logic
func TestOssfsMonitorInterceptor_RetryCount(t *testing.T) {
	// Use a fresh monitor manager for this test to avoid conflicts
	originalManager := monitorManager
	monitorManager = server.NewMountMonitorManager()
	defer func() {
		monitorManager.StopAllMonitoring()
		monitorManager.WaitForAllMonitoring()
		monitorManager = originalManager
	}()

	metricsDir := t.TempDir()
	target := "test-target-retry"

	// First mount failure
	handler1 := func(ctx context.Context, op *mounter.MountOperation) error {
		return errors.New("mount failed")
	}

	op1 := &mounter.MountOperation{
		Target:      target,
		MetricsPath: metricsDir,
	}

	err1 := OssfsMonitorInterceptor(context.Background(), op1, handler1)
	assert.Error(t, err1)

	// Verify retryCount is 0 after first failure
	// retryCount represents "retry count", not "failure count".
	// The first mount attempt is not a retry, so retryCount = 0.
	// IncreaseMountRetryCount() is only called when found == true (retry attempt).
	retryCountFile := filepath.Join(metricsDir, utils.MetricsMountRetryCount)
	retryCountContent, err := os.ReadFile(retryCountFile)
	require.NoError(t, err)
	assert.Equal(t, "0", string(retryCountContent), "retryCount should be 0 after first failure (first attempt is not a retry)")

	// Second mount failure (retry)
	// found == true, so IncreaseMountRetryCount() is called before mount attempt (0 -> 1).
	// HandleMountFailureOrExit only writes the current retryCount value, it doesn't increment it.
	err2 := OssfsMonitorInterceptor(context.Background(), op1, handler1)
	assert.Error(t, err2)

	// Verify retryCount is 1 (incremented from 0 to 1 when retry started)
	retryCountContent2, err := os.ReadFile(retryCountFile)
	require.NoError(t, err)
	assert.Equal(t, "1", string(retryCountContent2), "retryCount should be 1 after second failure (incremented when retry started)")
}
