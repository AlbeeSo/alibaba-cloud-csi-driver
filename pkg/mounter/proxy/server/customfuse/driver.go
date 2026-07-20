package customfuse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/interceptors"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	mounterutils "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/utils"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

const (
	configMapEntrypoint = "/etc/fuse-config/entrypoint.sh"
	defaultEntrypoint   = "/entrypoint.sh"

	authTypeAgentIdentity = "agent-identity"

	optAuthType                  = "authType"
	optSandboxId                 = "sandboxId"
	optSandboxCredProviderName   = "sandboxCredProviderName"
	optAgentIdentityEndpoint     = "agent_identity_endpoint"
	optAgentIdentityTokenFile    = "agent_identity_token_file"
	optAgentIdentityCredProvider = "agent_identity_cred_provider"
	optAgentIdentityCAFile       = "agent_identity_ca_file"
)

func init() {
	server.RegisterDriver(NewDriver())
}

type Driver struct {
	mounter.Mounter
	pids           sync.Map
	monitorManager *server.MountMonitorManager
	wg             sync.WaitGroup
}

func NewDriver() *Driver {
	driver := &Driver{
		monitorManager: server.NewMountMonitorManager(),
	}
	m := &extendedMounter{
		driver:    driver,
		Interface: mount.New(""),
	}
	driver.Mounter = mounter.NewForMounter(
		m,
		interceptors.OssfsMonitorInterceptor,
	)
	return driver
}

func (h *Driver) Name() string {
	return "customfuse"
}

func (h *Driver) Fstypes() []string {
	return []string{"customfuse"}
}

func (h *Driver) Init() {}

func (h *Driver) ApplyOptionDefaults(options []string) []string {
	idx := mounterutils.IndexMountOptions(options)
	if idx[optAuthType] != authTypeAgentIdentity {
		return options
	}

	var appends []string
	if _, ok := idx[optAgentIdentityEndpoint]; !ok {
		appends = append(appends, optAgentIdentityEndpoint+"="+server.GetAgentIdentityEndpoint())
	}
	if _, ok := idx[optAgentIdentityTokenFile]; !ok {
		if sandboxId := idx[optSandboxId]; sandboxId != "" {
			appends = append(appends, optAgentIdentityTokenFile+"="+server.GetAgentIdentityTokenFilePath(sandboxId))
		}
	}
	if credProv := idx[optSandboxCredProviderName]; credProv != "" {
		if _, ok := idx[optAgentIdentityCredProvider]; !ok {
			appends = append(appends, optAgentIdentityCredProvider+"="+credProv)
		}
	}
	caPath := server.GetAgentIdentityCAFilePath()
	if unix.Access(caPath, unix.R_OK) == nil {
		appends = append(appends, optAgentIdentityCAFile+"="+caPath)
	}

	if len(appends) > 0 {
		options = mounterutils.MergeMountOptions(options, appends)
	}
	return options
}

func (h *Driver) Terminate() {
	h.monitorManager.StopAllMonitoring()

	h.pids.Range(func(key, value any) bool {
		if err := value.(*exec.Cmd).Process.Signal(syscall.SIGTERM); err != nil {
			klog.ErrorS(err, "Failed to terminate customfuse process", "pid", key)
		}
		klog.V(4).InfoS("Sent sigterm to customfuse process", "pid", key)
		return true
	})

	h.monitorManager.WaitForAllMonitoring()
	h.wg.Wait()
	klog.InfoS("All customfuse processes and monitoring goroutines exited")
}

func (h *Driver) Mount(ctx context.Context, req *proxy.MountRequest) error {
	return h.ExtendedMount(ctx, &mounter.MountOperation{
		Source:      req.Source,
		Target:      req.Target,
		Options:     req.Options,
		Secrets:     req.Secrets,
		MetricsPath: req.MetricsPath,
	})
}

type extendedMounter struct {
	driver *Driver
	mount.Interface
}

var _ mounter.Mounter = &extendedMounter{}

func (m *extendedMounter) ExtendedMount(ctx context.Context, op *mounter.MountOperation) error {
	logger := klog.FromContext(ctx)
	target := op.Target

	options := m.driver.ApplyOptionDefaults(op.Options)
	env := buildEnvVars(op.Source, op.Target, options, op.Secrets)

	var refresher *CredentialRefresher
	idx := mounterutils.IndexMountOptions(options)
	if idx[optAuthType] == authTypeAgentIdentity {
		opts := AgentIdentityOpts{
			TokenFile:    idx[optAgentIdentityTokenFile],
			Endpoint:     idx[optAgentIdentityEndpoint],
			CredProvider: idx[optAgentIdentityCredProvider],
			CAFile:       idx[optAgentIdentityCAFile],
		}
		volumeID := idx[optSandboxId]
		if volumeID == "" {
			volumeID = "default"
		}
		refresher = NewCredentialRefresher(opts, volumeID)
		if err := refresher.Start(ctx); err != nil {
			return fmt.Errorf("credential refresher start: %w", err)
		}
		env = filterAgentIdentityEnv(env)
		env = append(env, "credentialDir="+refresher.Dir(), optAuthType+"="+authTypeAgentIdentity)
	}

	entrypoint := defaultEntrypoint
	if fi, err := os.Stat(configMapEntrypoint); err == nil {
		if fi.Mode()&0111 == 0 {
			return fmt.Errorf("configmap entrypoint %s is not executable (mode %s)", configMapEntrypoint, fi.Mode())
		}
		entrypoint = configMapEntrypoint
	}
	logger.Info("Using entrypoint", "path", entrypoint)

	sw := server.NewSwitchableWriter(os.Stderr)
	cmd := exec.Command(entrypoint)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, sw)
	defer func() {
		sw.SwitchTarget(os.Stderr)
	}()

	if err := cmd.Start(); err != nil {
		if refresher != nil {
			refresher.Stop()
		}
		return fmt.Errorf("start entrypoint failed: %w", err)
	}

	pid := cmd.Process.Pid
	logger.Info("Started customfuse entrypoint", "pid", pid, "target", target)

	exited := make(chan error, 1)
	m.driver.wg.Add(1)
	m.driver.pids.Store(pid, cmd)
	go func() {
		defer m.driver.wg.Done()
		defer m.driver.pids.Delete(pid)

		err := cmd.Wait()
		if refresher != nil {
			refresher.Stop()
		}
		if err != nil {
			logger.Error(err, "customfuse entrypoint exited with error", "mountpoint", target, "pid", pid)
		} else {
			logger.Info("customfuse entrypoint exited", "mountpoint", target, "pid", pid)
		}
		exited <- err
		close(exited)
	}()

	err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (done bool, err error) {
		select {
		case err := <-exited:
			if err != nil {
				return false, fmt.Errorf("entrypoint exited: %w", err)
			}
			return false, fmt.Errorf("entrypoint exited unexpectedly")
		default:
			notMnt, err := m.IsLikelyNotMountPoint(target)
			if err != nil {
				logger.Error(err, "check mountpoint", "mountpoint", target)
				return false, nil
			}
			if !notMnt {
				logger.Info("Successfully mounted", "mountpoint", target)
				return true, nil
			}
			return false, nil
		}
	})

	if err == nil {
		logger.Info("Customfuse mount succeeded, handing off to customer", "mountpoint", target, "pid", pid)
		op.MountResult = server.FuseMountResult{
			PID:      pid,
			ExitChan: exited,
		}
		return nil
	}

	if wait.Interrupted(err) {
		if terr := cmd.Process.Signal(syscall.SIGTERM); terr != nil {
			logger.Error(terr, "Failed to terminate entrypoint", "pid", pid)
		}
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
				logger.Error(kerr, "Failed to kill entrypoint", "pid", pid)
			}
		}
	}
	return err
}

// buildEnvVars converts mount parameters into environment variables for the
// FUSE entrypoint. All options are passed through as-is:
//
//	key=value → env var "key=value"
//	key       → env var "key=" (empty value; entrypoints detect presence via ${key+set})
//
// Driver-set env vars:
//
//	source     — opaque to the driver
//	mountpoint — target path where the entrypoint must mount
//
// From options (carried as key=value pairs, including expanded mountOptions):
//
//	bucket       — object storage bucket name
//	url          — storage endpoint
//	path         — sub-path within the volume (e.g., JuiceFS subdir)
//	readOnly     — "true" when PV is read-only
//	otherOpts    — legacy mount options from volumeAttributes (backward compat)
//	<any key>    — arbitrary mount option from pv.Spec.MountOptions
//
// Secrets are passed as env vars with the key as the variable name
// (no prefix, no transformation).
//
// AK/SK compatibility: if secrets contain "akId"/"akSecret" (OSS convention)
// but not "accessKeyId"/"accessKeySecret", the latter are added automatically
// so entrypoints can standardize on "accessKeyId"/"accessKeySecret".
func buildEnvVars(source, target string, options []string, secrets map[string]string) []string {
	env := []string{
		"mountpoint=" + target,
	}
	if source != "" {
		env = append(env, "source="+source)
	}

	for _, opt := range options {
		key, value, hasValue := strings.Cut(opt, "=")
		if !hasValue {
			// Bare flag (e.g., "writeback") — set as key= with empty value.
			// Entrypoints can use ${key+set} to detect presence.
			env = append(env, key+"=")
			continue
		}
		env = append(env, key+"="+value)
	}

	for key, value := range secrets {
		env = append(env, key+"="+value)
	}

	if _, ok := secrets["accessKeyId"]; !ok {
		if v, ok := secrets["akId"]; ok {
			env = append(env, "accessKeyId="+v)
		}
	}
	if _, ok := secrets["accessKeySecret"]; !ok {
		if v, ok := secrets["akSecret"]; ok {
			env = append(env, "accessKeySecret="+v)
		}
	}

	return env
}

// filterAgentIdentityEnv removes infrastructure-only keys from env that should
// not be exposed to the entrypoint. These keys are consumed by the driver itself
// (CredentialRefresher) and replaced by credentialDir + authType.
func filterAgentIdentityEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "agent_identity_") {
			continue
		}
		switch key {
		case optSandboxId, optSandboxCredProviderName, optAuthType:
			continue
		}
		result = append(result, e)
	}
	return result
}
