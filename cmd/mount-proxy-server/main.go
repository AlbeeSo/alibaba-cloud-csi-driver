package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	flag "github.com/spf13/pflag"
	"golang.org/x/sys/unix"

	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/filevolume"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server"
	_ "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server/alinas"
	_ "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server/customfuse"
	_ "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server/ossfs"
	_ "github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/mounter/proxy/server/ossfs2"
	"github.com/kubernetes-sigs/alibaba-cloud-csi-driver/pkg/utils"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
)

var (
	socketPath                              string
	drivers                                 []string
	handleTimeout                           time.Duration
	enableNftables                          bool
	cleanupNASMounts                        bool
	fileStateDir, fileTargetDir, filePodUID string
	fileCheckReady                          bool
	fileMode                                bool
)

func main() {
	flag.BoolVar(&fileMode, "file-mode", false, "run Pod-scoped file transport instead of socket mode")
	flag.StringVar(&fileStateDir, "file-state-dir", filevolume.DefaultStateDir, "Pod-scoped desired/status directory; explicitly setting this also enables file mode")
	flag.StringVar(&fileTargetDir, "file-target-dir", filevolume.DefaultTargetDir, "this Pod's CSI volume directory inside the sidecar")
	flag.StringVar(&filePodUID, "file-pod-uid", os.Getenv("POD_UID"), "Pod UID from Downward API")
	flag.BoolVar(&fileCheckReady, "file-check-ready", false, "check file statuses and exit (startup/readiness probe)")
	flag.StringVar(&socketPath, "socket", "/var/run/csi/mounter.sock", "socket path")
	flag.StringSliceVar(&drivers, "driver", nil, "socket-mode drivers to enable (e.g. 'ossfs,alinas'); file mode automatically enables all file-capable drivers")
	flag.DurationVar(&handleTimeout, "timeout", proxy.DefaultConnectionTimeout,
		"how long a connection has to deliver a request and receive its answer; also caps a mount when shorter than what the client asks for")
	flag.BoolVar(&enableNftables, "enable-nftables", false, "enable nftables rules to restrict mount proxy port access (default: false)")
	flag.BoolVar(&cleanupNASMounts, "cleanup-nas-mounts-on-exit", false, "unmount all NAS mount points inside the pod on SIGTERM")
	flag.BoolVar(&server.InitNASRSAPEM, "init-nas-rsa-pem", false, "generate the NAS RSA private key (/etc/aliyun/alinas/privateKey.pem) on startup")
	utils.AddKlogFlags(flag.CommandLine)
	utils.AddGoFlags(flag.CommandLine)
	flag.Parse()
	if fileCheckReady {
		if err := filevolume.CheckReady(fileStateDir, filePodUID, nil, time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if fileMode || flag.CommandLine.Changed("file-state-dir") {
		if err := runFileMode(); err != nil {
			klog.ErrorS(err, "File mode stopped")
			os.Exit(1)
		}
		return
	}

	_ = os.Remove(socketPath)
	server.SetCleanupNASMountsOnExit(cleanupNASMounts)
	server.Init(drivers)

	listener, err := listen(socketPath)
	if err != nil {
		klog.ErrorS(err, "Failed to listen", "socket", socketPath)
		os.Exit(1)
	}
	defer os.Remove(socketPath)

	klog.InfoS("Listening on socket", "socket", socketPath)

	if enableNftables {
		klog.InfoS("Init nftables", "enabled", true)
		if err := setupNftables(); err != nil {
			klog.ErrorS(err, "Failed to setup nftables")
			os.Exit(1)
		}
	} else {
		klog.InfoS("Skipping nftables setup", "enabled", false)
	}

	srv := server.NewServer(listener, handleTimeout)

	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go srv.Serve()

	sig := <-c
	klog.InfoS("exiting", "signal", sig)
	if err := srv.Close(); err != nil {
		klog.ErrorS(err, "Failed to close server")
	}
	server.Terminate(drivers)
}

func runFileMode() error {
	if err := server.EnsureFuseDevice("/dev/fuse"); err != nil {
		return err
	}
	fileDrivers := server.InitFileDrivers()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	r := filevolume.Runner{Root: fileStateDir, PodUID: filePodUID, TargetRoot: fileTargetDir,
		Timeout: handleTimeout, Backend: server.FileBackend{Mounter: mount.NewWithoutSystemd("")}}
	return fileLifecycle(ctx, r.Run, func() { server.Terminate(fileDrivers) }, 20*time.Second)
}

// The grace budget covers both volume drain and driver process termination.
// A hung syscall/client must not turn shutdown into an unbounded wait. On
// timeout the process exits with an error; only the runtime can finish teardown,
// and no Unmounted acknowledgment is invented for an unfinished operation.
func fileLifecycle(ctx context.Context, run func(context.Context) error, terminate func(), grace time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	var result error
	select {
	case result = <-done:
	case <-ctx.Done():
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case result = <-done:
		case <-timer.C:
			return fmt.Errorf("file cleanup unconfirmed: volume drain exceeded %s; runtime teardown required", grace)
		}
		terminated := make(chan struct{})
		go func() { terminate(); close(terminated) }()
		select {
		case <-terminated:
			return result
		case <-timer.C:
			return fmt.Errorf("file cleanup unconfirmed: driver termination exceeded shutdown budget %s; runtime teardown required", grace)
		}
	}
	terminated := make(chan struct{})
	go func() { terminate(); close(terminated) }()
	select {
	case <-terminated:
		return result
	case <-time.After(grace):
		return fmt.Errorf("file cleanup unconfirmed: driver termination exceeded %s", grace)
	}
}

func listen(socketPath string) (*net.UnixListener, error) {
	addr := net.UnixAddr{Name: socketPath, Net: "unix"}
	if len(socketPath) < 100 {
		return net.ListenUnix("unix", &addr)
	}

	// Need to change the current working directory to the temp volume base path,
	// because the socket absolute path is longer than 108(104 on OSX) characters,
	// which will cause "bind: invalid argument" errors.
	exPwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get the current directory: %w", err)
	}

	socketDir := filepath.Dir(socketPath)

	if err = os.Chdir(socketDir); err != nil {
		return nil, fmt.Errorf("failed to change directory to %q: %w", socketDir, err)
	}

	defer func() {
		if err := os.Chdir(exPwd); err != nil {
			klog.ErrorS(err, "Failed to change directory back")
		}
	}()

	addr.Name = filepath.Base(socketPath)
	return net.ListenUnix("unix", &addr)
}

const (
	nftablesTableName  = "csi_mount_proxy"
	nftablesChainName  = "early_filter"
	nftablesTargetPort = 12049
)

// setupNftables configures nftables to block local access to the mount proxy port
// from processes outside the target cgroup. This prevents unauthorized access.
func setupNftables() error {
	conn, err := nftables.New()
	if err != nil {
		return fmt.Errorf("failed to create nftables connection: %w", err)
	}

	// Check if table already exists — if so, log existing chains/rules and skip
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return fmt.Errorf("failed to list nftables tables: %w", err)
	}
	for _, t := range tables {
		if t.Name == nftablesTableName {
			chains, _ := conn.ListChainsOfTableFamily(nftables.TableFamilyINet)
			for _, c := range chains {
				if c.Table.Name == nftablesTableName {
					rules, _ := conn.GetRules(t, c)
					klog.InfoS("nftables table already exists, skipping setup",
						"table", t.Name, "chain", c.Name, "ruleCount", len(rules))
				}
			}
			return nil
		}
	}

	// Create table
	table := conn.AddTable(&nftables.Table{
		Family: nftables.TableFamilyINet,
		Name:   nftablesTableName,
	})

	// Create chain: type filter, hook output, priority -150, policy accept
	policy := nftables.ChainPolicyAccept
	chain := conn.AddChain(&nftables.Chain{
		Name:     nftablesChainName,
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityRef(-150),
		Policy:   &policy,
	})

	// Build port bytes (big-endian)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, nftablesTargetPort)

	// Add rule: oif lo tcp dport 12049 meta cgroup != 0 drop
	conn.AddRule(&nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			// Match output interface "lo"
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte("lo\x00")},
			// Match TCP protocol
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
			// Match destination port 12049
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: portBytes},
			// Match cgroup != 0
			&expr.Meta{Key: expr.MetaKeyCGROUP, Register: 1},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
			// Verdict: drop
			&expr.Verdict{Kind: expr.VerdictDrop},
		},
	})

	if err := conn.Flush(); err != nil {
		return fmt.Errorf("failed to apply nftables rules: %w", err)
	}
	return nil
}
