# Deployment Architecture

This document describes how CSI volumes are mounted across different runtime types and storage drivers (OSS / NAS). Understanding this architecture is critical for implementing features like overlayfs that interact with the mount lifecycle.

## Key Concepts

### Mount Proxy

A **mount proxy** is a sidecar or standalone pod that runs the actual FUSE/NFS client (ossfs, ossfs2, alinas/EFC). The CSI plugin communicates with it over a Unix socket (`mounter.sock`) using the proxy protocol (`pkg/mounter/proxy/`). The mount-proxy-server binary (`cmd/mount-proxy-server/`) accepts `--driver` to enable specific drivers (ossfs, ossfs2, alinas).

### csi-agent

The **csi-agent** (`cmd/csi-agent/`) runs inside a sandbox/RunD guest and handles CSI NodePublish/NodeUnpublish requests forwarded from the host. It reads a single request from stdin, delegates to the appropriate driver's CSIAgent, and writes the response to stdout. It also connects to a local mount-proxy-server via `--mount-proxy-sock`.

### Runtime Type Determination (OSS)

OSS uses `DetermineRuntimeType()` (`pkg/oss/utils.go:656`) to classify the runtime based on three booleans:

| directAssigned | hasSocketPath | skipGlobalMount | Runtime Type |
|:-:|:-:|:-:|:--|
| false | false | true  | **MicroVM** (ECI) |
| false | true  | false | **RunC** |
| false | true  | true  | **RunD** |
| true  | false | false | **COCO** |
| true  | true  | true  | **RunD** |

- `directAssigned`: PV attribute, originally for COCO, now also used to distinguish rund in mixed clusters
- `socketPath`: from `publishContext["mountPorxySocket"]`, set by ControllerPublish (RunC) or csi-agent (RunD/sandbox)
- `skipGlobalMount`: nodeserver config, true only in csi-agent binary

---

## OSS Mount Architecture

### OSS RunC

**Topology**: One fuse pod (mount proxy) per PV per node. Multiple business pods can share the same ossfs process via bind mount.

**Flow**:
1. `ControllerPublishVolume` (csi-provisioner) creates a fuse pod in `ack-csi-fuse` namespace. The pod runs `mount-proxy-server --driver=ossfs` (or ossfs2). Returns the socket path in `publishContext`.
2. `NodePublishVolume` (csi-plugin) connects to the fuse pod via the socket. The first pod triggers `ExtendedMount` which mounts ossfs to `attachPath` (globalmount at `/run/fuse.ossfs/<hash>/globalmount`). Subsequent pods only bind-mount `attachPath` → `targetPath`.
3. `NodeUnpublishVolume` unmounts the bind mount at `targetPath`.
4. `NodeUnstageVolume` unmounts the globalmount at `attachPath`.
5. `ControllerUnpublishVolume` deletes the fuse pod.

**Key characteristic**: Fuse pods are immutable once created — they are never updated. To change the image, you must delete and recreate the consumer pod (which triggers ControllerUnpublish → ControllerPublish cycle).

**Code path**: `pkg/oss/nodeserver.go:215-315` (RunC branch), `pkg/oss/controllerserver.go:168-225`

### OSS Sandbox

**Topology**: Similar to RunC — uses mount proxy. But instead of ControllerPublish creating the fuse pod, an **external controller injects a sidecar** (mount-proxy container) into the business pod. Each mount proxy serves only **one business pod** but runs **all PVs** for that pod (potentially multiple ossfs processes).

**Flow**:
1. No ControllerPublish phase — `directAssigned=true`, so `ControllerPublishVolume` returns immediately (`pkg/oss/controllerserver.go:177-179`).
2. The external controller injects the mount-proxy sidecar and sets up the socket path.
3. `NodePublishVolume` in csi-agent receives the request, injects `socketPath` from its own `--mount-proxy-sock` flag into `publishContext` (`pkg/oss/csi_agent.go:55`), then the same code path as RunD handles the mount — directly to `targetPath`, no globalmount.

**Key difference from RunC**: RunC = 1 mount proxy : 1 ossfs : N business pods. Sandbox = 1 mount proxy : N ossfs : 1 business pod.

**Image**: Uses the **AIO (all-in-one)** image (`--driver=alinas,ossfs,ossfs2`) since we don't know which driver will be needed.

### OSS RunD

**Topology**: Identical to Sandbox — sidecar mount proxy injected into the pod. The difference is only in **who triggers the mount**: sandbox is triggered by csi nodeserver receiving the request, while RunD is triggered by **csi-agent** inside the guest.

**Flow**:
1. Same as sandbox: no ControllerPublish, mount proxy is a sidecar.
2. csi-agent receives the request, connects to mount-proxy-server, and mounts directly to `targetPath` (no globalmount).

**Code path**: `pkg/oss/nodeserver.go:246-270` (RunD/MicroVM branch), `pkg/oss/csi_agent.go`

### OSS COCO & MicroVM

- **COCO**: Uses `directAssigned=true`, no socketPath. Mounts via `publishDirectVolume()` — a completely separate code path. Not actively iterated.
- **MicroVM** (ECI): Uses cmd mounter (`OssCmdMounter`) instead of proxy mounter. Not actively iterated.

---

## NAS Mount Architecture

### NAS RunC (Direct Mount)

**Topology**: NFS is mounted directly by the csi-plugin process on the node using kernel `mount` syscall. No mount proxy involved.

**Flow**:
1. `NodePublishVolume` calls `doMount()` which does `mount -t nfs` (or `mount -t alinas` for EFC) directly to `targetPath`.
2. Each business pod gets its own NFS mount — no globalmount sharing.

**Code path**: `pkg/nas/nodeserver.go:481`, `pkg/nas/utils.go:77` (doMount), `pkg/nas/mounter.go:32` (default fstype path)

### NAS RunC (Mount Proxy / cnfs-nas-daemon)

**Topology**: When `AlinasMountProxy` feature gate is enabled, NAS uses a **DaemonSet** (`cnfs-nas-daemon` in `cnfs-system` namespace) as the mount proxy. All business pods on the node share this single DaemonSet pod. Each alinas/EFC client inside the DaemonSet serves a **single business pod** (not shared across pods).

**Flow**:
1. csi-plugin detects `AlinasMountProxy` feature gate → sets `MountProxySocket` to `/run/cnfs/alinas-mounter.sock` (`pkg/nas/internal/config.go:136-138`).
2. `newNasMounter()` creates a `ProxyMounter` when socketPath is non-empty (`pkg/nas/mounter.go:48-49`).
3. `NodePublishVolume` → `doMount()` → `ExtendedMount()` → ProxyMounter forwards to cnfs-nas-daemon.

**Key difference from OSS**: The DaemonSet **can be upgraded** (rolling update). This means it must handle state carefully — in-memory state and emptyDir data can be lost during upgrades. OSS fuse pods are never updated after creation.

**Socket**: `/run/cnfs/alinas-mounter.sock` (shared by all volumes on the node)

**Code path**: `pkg/nas/mounter.go:42-56`, `pkg/nas/internal/config.go:136`, `pkg/mounter/proxy_mounter.go`

### NAS Sandbox & RunD

**Topology**: Share the **same mount proxy sidecar** as OSS sandbox/RunD — the AIO image (`--driver=alinas,ossfs,ossfs2`). The mount-proxy-server handles both OSS and NAS mount requests in the same process.

**Flow**:
1. csi-agent receives NAS NodePublish request, creates `NasMounter` with the mount proxy socket path (`pkg/nas/csi_agent.go:21`).
2. For alinas/EFC fstypes, `NasMounter.ExtendedMount()` delegates to `ProxyMounter` which forwards to the sidecar mount-proxy-server.

**Code path**: `pkg/nas/csi_agent.go`, `pkg/nas/mounter.go:48-49`

---

## Mount Proxy Image Variants

All built from `build/mount-proxy/Dockerfile`:

| Target | Drivers | Use Case |
|--------|---------|----------|
| `ossfs-1.91` (default) | ossfs | OSS RunC fuse pods (ossfs v1.91) |
| `ossfs-1.88` | ossfs | OSS RunC fuse pods (ossfs v1.88, legacy) |
| `ossfs2` | ossfs2 | OSS RunC fuse pods (ossfs v2) |
| `alinas` | alinas | NAS DaemonSet (cnfs-nas-daemon) |
| `aio` | alinas,ossfs,ossfs2 | Sandbox & RunD sidecar (shared by OSS and NAS) |

The `aio` image is built by default: `build/build-all-multi.sh` line 59 builds with `--opt target=aio`.

---

## Summary Table

| Scenario | CSI Component | Mount Proxy | Proxy Lifecycle | Sharing Model | globalmount |
|----------|--------------|-------------|-----------------|---------------|-------------|
| OSS RunC | csi-plugin + csi-provisioner | Per-PV fuse pod | Created by ControllerPublish, immutable | 1 proxy : 1 ossfs : N pods | Yes (`attachPath`) |
| OSS Sandbox | csi-agent | Sidecar (AIO) | Injected by external controller | 1 proxy : N ossfs : 1 pod | No |
| OSS RunD | csi-agent | Sidecar (AIO) | Injected by external controller | 1 proxy : N ossfs : 1 pod | No |
| OSS COCO | csi-plugin | None (direct) | N/A | N/A | No |
| OSS MicroVM | csi-plugin | None (cmd) | N/A | N/A | No |
| NAS RunC (direct) | csi-plugin | None | N/A | 1 mount : 1 pod | No |
| NAS RunC (proxy) | csi-plugin | DaemonSet | Managed by k8s, upgradable | 1 DaemonSet : N clients (1 per pod) | No |
| NAS Sandbox | csi-agent | Sidecar (AIO) | Injected by external controller | Shared with OSS | No |
| NAS RunD | csi-agent | Sidecar (AIO) | Injected by external controller | Shared with OSS | No |

