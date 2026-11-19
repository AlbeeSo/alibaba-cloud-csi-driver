package interceptors

import (
	"context"
	"errors"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var _ mounter.MountInterceptor = FuseMonitorInterceptor

var (
	raw            = mount.NewWithoutSystemd("")
	monitorManager = server.NewMountMonitorManager()
)

func FuseMonitorInterceptor(ctx context.Context, op *mounter.MountOperation, handler mounter.MountHandler) error {
	if op == nil || op.MetricsPath == "" {
		return handler(ctx, op)
	}

	// Get or create monitor for this target
	monitor, found := monitorManager.GetMountMonitor(op.Target, op.MetricsPath, raw, true)
	if monitor == nil {
		klog.ErrorS(errors.New("failed to get mount monitor"), "stop monitoring mountpoint status", "mountpoint", op.Target)
		return handler(ctx, op)
	}
	if found {
		monitor.IncreaseMountRetryCount()
	}

	// Register callbacks for async metrics updates before mount starts.
	// This ensures callbacks are ready before any post-attach failure occurs.
	previousExit, previousSuccess, previousFailure := op.OnProcessExit, op.OnRecoverySuccess, op.OnRecoveryFailed
	op.OnProcessExit = func(exitErr error) {
		monitor.HandleProcessExitForRecovery(exitErr)
		if previousExit != nil {
			previousExit(exitErr)
		}
	}
	op.OnRecoverySuccess = func(pid int, exitErr error, attempts int) {
		monitor.HandleRecoverySuccess(pid, exitErr, attempts)
		if previousSuccess != nil {
			previousSuccess(pid, exitErr, attempts)
		}
	}
	op.OnRecoveryFailed = func(exitErr error, recoveryErr error, attempts int) {
		monitor.HandleRecoveryFailed(exitErr, recoveryErr, attempts)
		if previousFailure != nil {
			previousFailure(exitErr, recoveryErr, attempts)
		}
	}

	err := handler(ctx, op)

	// Synchronous metrics path: mount failure (including pre-attach crash for fd-passing)
	// is reported here because the error returns through the normal call stack.
	if err != nil {
		// This method should only be called when err != nil.
		// Invoking it with a nil error will trigger a warning log.
		monitor.HandleMountFailureOrExit(err)
	}

	if op.MountResult == nil {
		return err
	}

	res, ok := op.MountResult.(server.FuseMountResult)
	if !ok {
		klog.ErrorS(errors.New("failed to assert fuse mount result type"), "skipping monitoring of mountpoint", "mountpoint", op.Target)
		return err
	}

	// Relay the terminal outcome to the file worker after observing it. Two
	// independent readers on the original channel could lose the exit error.
	originalExit := res.ExitChan
	workerExit := make(chan error, 1)
	res.ExitChan = workerExit
	op.MountResult = res
	go func() {
		exitErr, received := <-originalExit
		if received {
			workerExit <- exitErr
		}
		close(workerExit)
		// Assume the process exits with no error upon receiving SIGTERM,
		// and exits with an error in case of unexpected failures.
		if err == nil && exitErr != nil {
			monitor.HandleMountFailureOrExit(exitErr)
		}
	}()

	if err != nil {
		return err
	}

	monitor.HandleMountSuccess(res.PID)
	// Start monitoring goroutine (ticker based only)
	monitorManager.StartMonitoring(op.Target)
	return nil
}
