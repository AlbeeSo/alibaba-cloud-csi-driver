# Design: Agent Identity Authentication for CustomFuse in Sandbox Scenarios

## 1. Background

### 1.1 Business Context

Customers using ACK sandbox (RunD) environments need to mount external FUSE storage such as JuiceFS, JindoFS, s3fs, etc. Currently, the sandbox scenario uses **agent-identity** authentication: an external controller (`ack-agent-identity`) issues per-sandbox tokens, and FUSE clients exchange these tokens for short-lived STS credentials scoped to the sandbox's declared permissions.

### 1.2 Problem Statement

The agent-identity protocol is **not open-source** and tightly coupled to Alibaba Cloud's internal infrastructure. It is impractical to require every third-party FUSE client to implement native agent-identity support (as ossfs does). We need a generic solution that:

1. Handles the agent-identity token exchange within `mount-proxy` (the infrastructure layer we control)
2. Delivers the resulting STS credentials to arbitrary FUSE clients via file-based or environment-based mechanisms
3. Supports periodic credential refresh without restarting the FUSE process

### 1.3 Current State

#### How ossfs handles agent-identity (the ideal path)

ossfs has native agent-identity support built into its C++ credential refresh loop:

```
ossfs process (in-memory)
  → reads /var/opt/sandbox/agent-token/<sandboxId>.token
  → POST to credential-provider endpoint (with Bearer token + CA cert)
  → receives STS {accessKeyId, accessKeySecret, securityToken, expiration}
  → stores in memory, auto-refreshes before expiration
  → signs OSS requests with V4 signature
```

Key property: **STS credentials never touch disk**. They exist only in ossfs's process memory.

#### How CustomFuse currently handles auth

CustomFuse passes Kubernetes Secret entries as environment variables to the entrypoint script at mount time. There is no credential refresh mechanism — credentials are static for the lifetime of the FUSE process.

```
CSI NodePublish → mount-proxy → entrypoint.sh (env vars: $accessKeyId, $accessKeySecret, ...)
```

## 2. Research: How FUSE Clients Accept Credentials

### 2.1 JuiceFS Community Edition (CE)

| Method | Mechanism |
|--------|-----------|
| Format-time | `--access-key`, `--secret-key`, `--session-token` flags |
| Env vars | `ALICLOUD_ACCESS_KEY_ID`, `ALICLOUD_ACCESS_KEY_SECRET`, `SECURITY_TOKEN` (for OSS backend) |
| Runtime refresh | **Metadata-driven hot reload**: `baseMeta.refresh()` goroutine reads Format struct from metadata engine every 12s (heartbeat). If credentials changed, it hot-swaps the storage client |
| How to rotate at runtime | `juicefs config <META-URL> --access-key NEW --secret-key NEW --session-token NEW` |
| File-based | Not supported — no file-watching for credentials |

**Key insight for JuiceFS CE**: The canonical rotation mechanism is `juicefs config` updating the metadata engine (Redis/MySQL/TiKV). A background process in the entrypoint can periodically run this command to inject fresh STS tokens.

### 2.2 JuiceFS Enterprise Edition (EE)

JuiceFS EE uses a **two-layer authentication model**:

| Layer | Purpose | Mechanism |
|-------|---------|-----------|
| Console Token (`--token`) | Control plane auth (fetch FS config, metadata addresses) | Issued from JuiceFS Web Console |
| Object Storage AK/SK | Data plane auth (direct access to OSS/S3) | Same as CE — client accesses storage directly |

| Method | Mechanism | Confirmed? |
|--------|-----------|:---:|
| Auth command | `juicefs auth <name> --token T --access-key AK --secret-key SK --session-token TOKEN` | ⚠️ Need to confirm `--session-token` flag existence |
| Config caching | `~/.juicefs/<name>.conf` (or `--conf-dir`) | Yes |
| Mount command | `juicefs mount --foreground <name> <path>` (uses cached config from auth) | Yes (production usage) |
| STS support | Unknown — current production uses permanent AK/SK only | ⚠️ **MUST CONFIRM** (see 11.1 Q3) |
| Runtime refresh | Unknown — EE is closed-source. Possible paths: console-mediated re-auth, config file re-read, or none | ⚠️ **MUST CONFIRM** (see 11.1 Q1/Q2) |

**Current production entrypoint** (from sandbox operations guide, uses permanent AK/SK):
```bash
juicefs auth --token=${token} --accesskey=${ak} --secretkey=${sk} --bucket=${url} ${source}
export JFS_FOREGROUND="1"
exec juicefs mount ${otherOpts} ${source} ${mountpoint}
```

**Key insight for JuiceFS EE**: Unlike CE (which has well-documented `juicefs config` → metadata engine hot-reload), EE's credential refresh behavior is **unconfirmed**. The design assumes it can work (via background `juicefs auth` re-run), but this is blocked on confirming Q1-Q5 in Section 11.1. If EE cannot accept STS tokens or cannot refresh them at runtime, agent-identity support for JuiceFS EE may require a different approach or is infeasible.

### 2.3 JindoFS / JindoFuse

| Method | Mechanism |
|--------|-----------|
| Config file | `fs.oss.accessKeyId`, `fs.oss.accessKeySecret`, `fs.oss.securityToken` |
| Env vars | `OSS_ACCESS_KEY_ID`, `OSS_ACCESS_KEY_SECRET`, `OSS_SECURITY_TOKEN` |
| CustomCredentialsProvider (HTTP) | `aliyun.oss.provider.url=http://...` — periodically fetches JSON from endpoint |
| CustomCredentialsProvider (secrets) | `aliyun.oss.provider.url=secrets:///path/prefix` — periodically reads credential files from disk |
| File format for secrets protocol | `{prefix}AccessKeyId`, `{prefix}AccessKeySecret`, `{prefix}SecurityToken` |

**Key insight for JindoFS**: The `secrets:///` protocol is a perfect fit — mount-proxy writes credential files to a known directory, JindoFS reads them periodically.

### 2.4 s3fs-fuse

| Method | Mechanism |
|--------|-----------|
| passwd_file | `AKID:SECRET` (no session token support in this format) |
| AWS credentials file | `~/.aws/credentials` with `aws_session_token` field |
| Env vars | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN` |
| credlib plugin | `-o credlib=libname.so` — calls `UpdateS3fsCredential()` for refresh |
| IAM role | `-o iam_role=auto` — auto-refresh from IMDS |
| Runtime refresh | Static credentials: **no refresh**. IAM/credlib: auto-refresh |

**Key insight for s3fs**: Static credentials cannot be rotated. For STS scenarios, the credlib plugin mechanism or IAM role emulation (custom IMDS endpoint) would be needed.

### 2.5 GeeseFS

| Method | Mechanism |
|--------|-----------|
| AWS credentials file | Standard format with `aws_session_token` |
| Env vars | Standard AWS env vars |
| Custom IAM endpoint | `--iam --iam-url URL --iam-flavor imdsv1` — fetches from custom HTTP endpoint |
| Refresh | Background timer + lazy refresh; re-fetches 5 min before expiry |

**Key insight for GeeseFS**: `--iam-url` pointing to a local HTTP endpoint (served by mount-proxy) would enable auto-refresh.

### 2.6 Summary: Universal Credential Delivery Methods

| Delivery Method | Clients Supporting It | Supports Rotation |
|-----------------|----------------------|-------------------|
| Environment variables at launch | All | No (one-shot) |
| Credential files (periodic re-read) | JindoFS (`secrets:///`), s3fs (credlib) | Yes |
| Metadata-engine update | JuiceFS (`juicefs config`) | Yes |
| Local HTTP credential endpoint | GeeseFS (`--iam-url`), JindoFS (HTTP provider) | Yes |
| Custom credential process/library | s3fs (`credlib`), goofys/GeeseFS (AWS `credential_process`) | Yes |

## 3. Client Admission Requirements

This section defines **what a FUSE client must support** to use agent-identity authentication. Clients that do not meet these requirements must fall back to static credentials stored in a Kubernetes Secret (`authType=""`, the existing default path).

### 3.1 Mandatory Requirements

| # | Requirement | Reason |
|---|-------------|--------|
| R1 | **STS Token support** — the client MUST accept temporary credentials (AccessKeyId + AccessKeySecret + SecurityToken) rather than only permanent AK/SK | Agent-identity issues time-limited STS credentials. A client that only accepts permanent AK/SK cannot consume them. |
| R2 | **Runtime credential refresh** — the client MUST have a mechanism to pick up new credentials without process restart (file re-read, config command, HTTP endpoint, or credential library) | STS tokens expire (typically 1 hour). Without refresh, the mount fails when the token expires. |
| R3 | **Foreground mode** — the client MUST support running in the foreground (`-f`, `--foreground`, or equivalent) | mount-proxy manages the FUSE process lifecycle; daemonized processes break lifecycle management and signal delivery. |

### 3.2 Decision Matrix

| STS Support | Runtime Refresh | Verdict |
|:-:|:-:|:--|
| Yes | Yes | **Fully compatible** — agent-identity works with rotation |
| Yes | No | **Partially compatible** — works for short-lived mounts (< token lifetime). For long-running mounts, the mount will fail after token expiration. Acceptable if customer accepts periodic pod recycling. |
| No | N/A | **Incompatible** — must use static AK/SK via Secret |

### 3.3 Client Assessment Results

| Client | STS | Runtime Refresh | Refresh Mechanism | Verdict |
|--------|:---:|:---:|---|---|
| JuiceFS EE | ⚠️ Unconfirmed | ⚠️ Unconfirmed | Possibly: console re-auth, config file re-read, or `juicefs auth` re-run | **⚠️ PENDING** — blocked on Q1-Q5 (Section 11.1) |
| JuiceFS CE | Yes | Yes | `juicefs config` → metadata engine hot-reload (12s) | Fully compatible |
| JindoFS | Yes | Yes | `secrets:///` file provider (native periodic re-read) | Fully compatible |
| GeeseFS | Yes | Yes | `--iam-url` HTTP endpoint (background + lazy refresh) | Fully compatible |
| s3fs | Yes | Partial | `use_session_token` + passwd_file rewrite; no guaranteed re-read timing | Partially compatible |
| CubeFS | Yes | No | Static config at mount time only | Partially compatible (short-lived mounts) |

> **Note**: JuiceFS EE is the highest-priority client (production customers). Its compatibility is currently PENDING confirmation. The entire design is predicated on EE being able to accept STS tokens and refresh them at runtime. See Section 11.1 for the decision tree.

### 3.4 Fallback: Static Credentials via Secret

For clients that do not meet R1 or R2, the existing `authType=""` path is available:

```yaml
# PV spec (no authType field → defaults to secret passthrough)
volumeAttributes:
  source: "my-bucket:/path"
  fuseType: "cubefs"
# Secret contains permanent AK/SK as key-value pairs
# Entrypoint receives them as env vars: $accessKeyId, $accessKeySecret
```

This is less secure (long-lived credentials) but compatible with any client that accepts credentials at startup.

## 4. Design

### 4.1 Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│  Sandbox Pod (mount-proxy sidecar, injected by sandbox-controller)               │
│                                                                                  │
│  ┌─────────────────────────────────────────────────────────────────────────────┐ │
│  │ csi-mount-proxy-server (PID 1)                                              │ │
│  │                                                                             │ │
│  │  ┌───────────────────────┐    ┌──────────────────────────────────────────┐  │ │
│  │  │ Credential Refresher  │    │ CustomFuse Driver                        │  │ │
│  │  │ (new component)       │    │                                          │  │ │
│  │  │                       │    │  receives mount request                  │  │ │
│  │  │ • reads agent token   │    │  → sets env vars from options/secrets    │  │ │
│  │  │ • calls cred-provider │    │  → exec /entrypoint.sh                  │  │ │
│  │  │ • writes STS to files │    │                                          │  │ │
│  │  │ • refreshes on timer  │    │  entrypoint.sh:                          │  │ │
│  │  └───────────┬───────────┘    │    reads STS from files (or env/cmd)     │  │ │
│  │              │                 │    mounts FUSE client at $mountpoint     │  │ │
│  │              │ writes          │                                          │  │ │
│  │              ▼                 └──────────────────────────────────────────┘  │ │
│  │  /var/run/customfuse/credentials/                                           │ │
│  │    ├── AccessKeyId                                                          │ │
│  │    ├── AccessKeySecret                                                      │ │
│  │    ├── SecurityToken                                                        │ │
│  │    └── Expiration                                                           │ │
│  └─────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                  │
│  External files (placed by ack-agent-identity controller):                       │
│    /var/opt/sandbox/agent-token/<sandboxId>.token                                │
│    /etc/ssl/certs/agent-identity/ca.crt                                          │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 Core Design: Credential Refresher in mount-proxy

A new `CredentialRefresher` component runs inside mount-proxy (within the customfuse driver) and handles:

1. **Token exchange**: Reads agent-identity token file → POST to credential-provider → receives STS credentials
2. **File-based delivery**: Writes STS credentials to a well-known directory as individual files
3. **Periodic refresh**: Re-fetches credentials before expiration (with configurable margin)
4. **Entrypoint notification**: Exposes credential directory path as `$credentialDir` env var to entrypoint

```go
// pkg/mounter/proxy/server/customfuse/credential_refresher.go

type CredentialRefresher struct {
    tokenFile     string // /var/opt/sandbox/agent-token/<sandboxId>.token
    endpoint      string // credential-provider URL
    credProvider  string // credential provider name
    caFile        string // CA cert path (optional)
    outputDir     string // /var/run/customfuse/credentials/
    refreshMargin time.Duration // refresh this long before expiry (default 5min)

    mu         sync.RWMutex
    current    *STSCredential
    stopCh     chan struct{}
}

type STSCredential struct {
    AccessKeyId     string
    AccessKeySecret string
    SecurityToken   string
    Expiration      time.Time
}
```

### 4.3 Credential File Format

Credentials are written as individual files (compatible with JindoFS `secrets:///` protocol and Kubernetes projected volume conventions):

```
/var/run/customfuse/credentials/
├── AccessKeyId         # plain text, no trailing newline
├── AccessKeySecret     # plain text, no trailing newline
├── SecurityToken       # plain text, no trailing newline
└── Expiration          # ISO 8601 format: 2026-04-09T04:27:09Z
```

**Atomic write**: Each file is written to a temp file and `rename(2)`'d into place to avoid partial reads.

**Why individual files and not a combined format**: Our credentials are Alibaba Cloud STS tokens used to sign OSS requests. No FUSE client in our scope reads AWS credentials files (`~/.aws/credentials`) to access OSS — they each have their own credential intake mechanism. Individual files are the universal lowest-common-denominator: JindoFS reads them natively via `secrets:///`, and all other entrypoints simply `cat` the files they need.

### 4.4 Entrypoint Interface

The entrypoint receives these **additional env vars** when agent-identity auth is active:

| Env Var | Value | Purpose |
|---------|-------|---------|
| `credentialDir` | `/var/run/customfuse/credentials` | Directory containing credential files (AccessKeyId, AccessKeySecret, SecurityToken, Expiration) |
| `authType` | `agent-identity` | Allows entrypoint to branch on auth type |

The entrypoint decides how to pass credentials to its FUSE client. Examples:

**JuiceFS entrypoint** (metadata-driven refresh via background loop):
```bash
#!/bin/bash
set -e

# Initial format with STS credentials from files
AK=$(cat "$credentialDir/AccessKeyId")
SK=$(cat "$credentialDir/AccessKeySecret")
TOKEN=$(cat "$credentialDir/SecurityToken")

juicefs format --storage=oss \
    --bucket="http://$bucket.$url" \
    --access-key="$AK" --secret-key="$SK" --session-token="$TOKEN" \
    "$source" myjfs

# Background credential refresher: update JuiceFS metadata when creds change
(
    LAST_AK="$AK"
    while true; do
        sleep 30
        NEW_AK=$(cat "$credentialDir/AccessKeyId" 2>/dev/null) || continue
        [ "$NEW_AK" = "$LAST_AK" ] && continue
        NEW_SK=$(cat "$credentialDir/AccessKeySecret")
        NEW_TOKEN=$(cat "$credentialDir/SecurityToken")
        juicefs config "$source" \
            --access-key="$NEW_AK" --secret-key="$NEW_SK" --session-token="$NEW_TOKEN" \
            2>/dev/null && LAST_AK="$NEW_AK"
    done
) &

exec mount.juicefs "$source" "$mountpoint" -o "foreground,no-update${mountOptions:+,$mountOptions}"
```

**JindoFS entrypoint** (native `secrets:///` provider):
```bash
#!/bin/bash
set -e
JINDO_OPTS="-ouri=$source -oendpoint=$url"
JINDO_OPTS="$JINDO_OPTS -ofs.oss.credentials.provider=com.aliyun.jindodata.oss.auth.CustomCredentialsProvider"
JINDO_OPTS="$JINDO_OPTS -oaliyun.oss.provider.url=secrets://$credentialDir/"
exec jindo-fuse -f $JINDO_OPTS "$mountpoint"
```

**s3fs entrypoint** (periodic file re-read via wrapper):
```bash
#!/bin/bash
set -e
update_passwd() {
    AK=$(cat "$credentialDir/AccessKeyId")
    SK=$(cat "$credentialDir/AccessKeySecret")
    TOKEN=$(cat "$credentialDir/SecurityToken")
    echo "$AK:$SK:$TOKEN" > /tmp/.passwd-ossfs
    chmod 600 /tmp/.passwd-ossfs
}
update_passwd

# Background updater
( while true; do sleep 60; update_passwd; done ) &

# s3fs doesn't support runtime passwd_file refresh, but we use use_session_token
# which re-reads on each request when combined with credential caching disabled
exec s3fs "$source" "$mountpoint" -f \
    -o passwd_file=/tmp/.passwd-ossfs \
    -o url="http://$url" \
    -o use_session_token
```

### 4.5 CSI Code Changes

#### Principle: Minimal impact on existing framework

The agent-identity logic is **fully encapsulated within the customfuse driver** in mount-proxy. No changes to:
- `proxy.MountRequest` / `proxy.Response` protocol structs
- `mounter.MountOperation` struct
- Other drivers (ossfs, ossfs2, alinas)
- The fuse_pod_manager framework

#### 4.5.1 New: Credential Refresher (`pkg/mounter/proxy/server/customfuse/credential_refresher.go`)

```go
package customfuse

// CredentialRefresher handles agent-identity token exchange and STS credential refresh.
// It is instantiated per-mount when authType=agent-identity is detected in mount options.
type CredentialRefresher struct { ... }

// Start begins the credential refresh loop. It performs an initial fetch (blocking),
// then refreshes in the background. Returns error if initial fetch fails.
func (r *CredentialRefresher) Start(ctx context.Context) error { ... }

// Stop terminates the background refresh goroutine.
func (r *CredentialRefresher) Stop() { ... }

// Dir returns the directory path where credentials are written.
func (r *CredentialRefresher) Dir() string { ... }
```

#### 4.5.2 Modified: CustomFuse driver (`pkg/mounter/proxy/server/customfuse/driver.go`)

The `extendedMounter.ExtendedMount` method gains agent-identity awareness:

```go
func (m *extendedMounter) ExtendedMount(ctx context.Context, op *mounter.MountOperation) error {
    // ... existing code ...

    // NEW: detect agent-identity auth from mount options
    authType, agentOpts := extractAgentIdentityOptions(op.Options)
    
    var refresher *CredentialRefresher
    if authType == "agent-identity" {
        refresher = NewCredentialRefresher(agentOpts)
        if err := refresher.Start(ctx); err != nil {
            return fmt.Errorf("credential refresher start failed: %w", err)
        }
        // Inject credential env vars for entrypoint
        env = append(env,
            "authType=agent-identity",
            "credentialDir="+refresher.Dir(),
        )
        // Remove raw agent_identity_* options from env (they are infra-only)
    }

    // ... existing entrypoint exec code ...

    if refresher != nil {
        // Monitor: stop refresher when entrypoint exits
        go func() {
            <-exited
            refresher.Stop()
        }()
    }

    // ... existing code ...
}
```

#### 4.5.3 Modified: CSI controllerserver/nodeserver (`pkg/customfuse/`)

`options.go` — add agent-identity fields to `fuseOptions`:

```go
type fuseOptions struct {
    // ... existing fields ...
    
    // Agent Identity (sandbox only)
    AuthType                string // "agent-identity" or ""
    SandboxId               string
    SandboxCredProviderName string
}
```

`options.go` — parse new volumeAttributes:

```go
case "sandboxid":
    opts.SandboxId = value
case "sandboxcredprovidername":
    opts.SandboxCredProviderName = value
```

`options.go` — pass agent-identity params as mount options (transport only):

```go
func (o *fuseOptions) makeMountOptions() []string {
    // ... existing code ...
    if o.AuthType == "agent-identity" {
        opts = append(opts, "authType=agent-identity")
        // Endpoint, token file, CA file are resolved at mount-proxy side
        // (same pattern as ossfs — ApplyOptionDefaults adds them)
    }
    return opts
}
```

`controllerserver.go` — guard against agent-identity on non-sandbox paths:

```go
func (cs *controllerServer) ControllerPublishVolume(...) {
    // ...
    if opts.AuthType == "agent-identity" {
        return nil, status.Error(codes.InvalidArgument,
            "agent-identity auth requires a sandbox environment (not supported with fuse pods)")
    }
    // ...
}
```

#### 4.5.4 Modified: mount-proxy `ApplyOptionDefaults` for customfuse

```go
// pkg/mounter/proxy/server/customfuse/driver.go

func (h *Driver) ApplyOptionDefaults(options []string) []string {
    tm := mounterutils.IndexMountOptions(options)
    
    // Inject agent-identity infra options (same pattern as ossfs driver)
    if _, ok := tm["authType"]; ok {
        if ep := ossfpm.GetAgentIdentityEndpoint(); ep != "" {
            options = mounterutils.MergeMountOptions(options,
                []string{"agent_identity_endpoint=" + ep})
        }
        // Token file path is derived from sandboxId (injected by sandbox-controller)
        if sandboxId := os.Getenv("SANDBOX_ID"); sandboxId != "" {
            options = mounterutils.MergeMountOptions(options,
                []string{"agent_identity_token_file=" + ossfpm.GetAgentIdentityTokenFilePath(sandboxId)})
        }
        caPath := server.GetAgentIdentityCAFilePath()
        if unix.Access(caPath, unix.R_OK) == nil {
            options = mounterutils.MergeMountOptions(options,
                []string{"agent_identity_ca_file=" + caPath})
        }
    }
    return options
}
```

### 4.6 Credential Refresh Lifecycle

```
Timeline:
    t=0    Mount request arrives at mount-proxy
    t=0    CredentialRefresher starts, performs initial token exchange
    t=0    Writes STS credentials to /var/run/customfuse/credentials/
    t=0    Entrypoint starts, reads credentials, mounts FUSE
    t=N    Credentials approaching expiration (expiry - 5min)
    t=N    CredentialRefresher re-reads token file, exchanges for new STS
    t=N    Atomically overwrites credential files
    t=N+Δ  FUSE client detects change (polling/heartbeat/re-read)
    ...
    t=X    Entrypoint exits (FUSE unmount or crash)
    t=X    CredentialRefresher stops
```

## 5. Security Analysis

### 5.1 Credential Exposure Comparison

| Aspect | ossfs (native agent-identity) | CustomFuse (this design) |
|--------|-------------------------------|--------------------------|
| STS credential storage | In-memory only | Files on disk (within sandbox VM) |
| Credential lifetime | Short-lived STS (typically 1h) | Same — same STS tokens |
| Exposure on crash | Process memory freed | Files remain until pod cleanup |
| Access scope | Scoped by CredentialProvider policy | Same — same policy template |
| Blast radius if leaked | Read/write to specific bucket path for limited time | Same |

### 5.2 Risk Assessment

**Risk: STS credentials written to disk within the sandbox VM**

- **Mitigation 1 — Scope**: Credentials are scoped by the CredentialProvider CR's policy template (e.g., read-only to a specific bucket path). Leaked credentials cannot escalate beyond the declared permissions.
- **Mitigation 2 — Lifetime**: STS tokens expire (typically 1 hour). Even if exfiltrated, they become useless after expiration.
- **Mitigation 3 — File permissions**: Credential files are written with `0600` mode, owned by root. Only processes running as root (the FUSE client) can read them.
- **Mitigation 4 — tmpfs option** (future): Credential directory can be backed by `tmpfs` (memory-only filesystem) to avoid persistence on disk. This reduces exposure on node failure to nearly zero.
- **Mitigation 5 — Cleanup**: `CredentialRefresher.Stop()` deletes credential files on shutdown.

**Risk: Agent-identity token file readable by FUSE process**

The agent-identity token file (`/var/opt/sandbox/agent-token/<sandboxId>.token`) is already placed on the sandbox filesystem by the external controller. The FUSE process could theoretically read it directly. However:
- The token is only useful with the specific credential-provider endpoint (internal K8s service)
- The endpoint requires matching `sandboxClientId` — the token is bound to this sandbox

**Conclusion**: The security delta between in-memory (ossfs) and file-based (CustomFuse) is **acceptable** given that:
1. STS credentials are already permission-scoped and time-limited
2. The sandbox VM is an isolated environment with a single tenant
3. This is the standard trade-off for supporting arbitrary FUSE clients without modifying them

### 5.3 Comparison with Existing Patterns

This file-based credential delivery is consistent with industry practices:
- Kubernetes projected service account tokens: written as files
- AWS IRSA (IAM Roles for Service Accounts): token file at a well-known path
- JindoFS `secrets:///` provider: designed for exactly this pattern
- GeeseFS `--iam-url`: local HTTP endpoint serving credentials

## 6. End-to-End Usage Guide (Customer Perspective)

This section describes the full workflow from the **customer's perspective**: how to use agent-identity authentication to mount external storage in a sandbox environment. This is the information product/pre-sales teams need to answer "how does the customer actually use this?"

### 6.1 Prerequisites (Platform Side — Our Responsibility)

Before the customer can use this feature, the following must be in place (managed by the cloud platform, not the customer):

1. ACK cluster with sandbox (RunD) nodes
2. `ack-agent-identity` controller deployed (issues tokens, serves credential-provider API)
3. `ack-agent-sandbox-controller` deployed (injects mount-proxy sidecar, manages SandboxClaim lifecycle)
4. CSI driver (csi-plugin/csi-agent) with CustomFuse + agent-identity support deployed
5. CredentialProvider CR created (defines RAM role + permission scope for the target bucket)
6. AgentIdentity + AgentRole + AgentRoleBinding CRs configured

### 6.2 What the Customer Provides

| Item | Description | Example |
|------|-------------|---------|
| **FUSE client image** | Docker image containing the FUSE binary (e.g., JuiceFS EE client) | `registry.example.com/juicefs-ee:5.1` |
| **Entrypoint script** | Shell script that reads credentials from files and drives the FUSE client | See 6.3 below |
| **PV/PVC manifests** | Kubernetes volume definitions with `authType=agent-identity` | See 6.4 below |
| **Storage backend** | The actual storage service (OSS bucket, JuiceFS metadata service, etc.) | `redis://meta.internal:6379/1` |

### 6.3 Step-by-Step: Building the Image

The customer builds a Docker image containing:
1. Their FUSE client binary
2. An entrypoint script that bridges our credential files to the client's mechanism

**Dockerfile example (JuiceFS EE)**:
```dockerfile
FROM juicedata/mount:ee-5.1

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
```

**entrypoint.sh** (the bridge between our credential delivery and JuiceFS EE):
```bash
#!/bin/bash
set -e

# --- Credential reading (platform provides these env vars automatically) ---
# $credentialDir = /var/run/customfuse/credentials (written by mount-proxy)
# $authType = "agent-identity"
# $source = JuiceFS volume name (from PV volumeAttributes)
# $mountpoint = target mount path (managed by mount-proxy)

AK=$(cat "$credentialDir/AccessKeyId")
SK=$(cat "$credentialDir/AccessKeySecret")
TOKEN=$(cat "$credentialDir/SecurityToken")

# --- JuiceFS EE auth (exchanges our STS for JuiceFS config) ---
juicefs auth "$source" --token="$consoleToken" \
    --access-key="$AK" --secret-key="$SK" --session-token="$TOKEN"

# --- Background credential refresh ---
(
    LAST_AK="$AK"
    while true; do
        sleep 30
        NEW_AK=$(cat "$credentialDir/AccessKeyId" 2>/dev/null) || continue
        [ "$NEW_AK" = "$LAST_AK" ] && continue
        NEW_SK=$(cat "$credentialDir/AccessKeySecret")
        NEW_TOKEN=$(cat "$credentialDir/SecurityToken")
        juicefs auth "$source" --token="$consoleToken" \
            --access-key="$NEW_AK" --secret-key="$NEW_SK" --session-token="$NEW_TOKEN" \
            2>/dev/null && LAST_AK="$NEW_AK"
    done
) &

# --- Mount (foreground, required) ---
exec mount.juicefs "$source" "$mountpoint" -o "foreground,no-update${otherOpts:+,$otherOpts}"
```

**Key points for the customer**:
- The entrypoint MUST run in **foreground** mode (`exec` the FUSE process, not daemonize)
- `$credentialDir`, `$authType`, `$source`, `$mountpoint` are **automatically set** by the platform
- The customer is responsible for the credential-bridging logic (the `juicefs auth` + background loop)
- Other env vars from PV `mountOptions` or `volumeAttributes` are available (e.g., `$otherOpts`, `$bucket`, `$url`)

### 6.4 Step-by-Step: Creating PV/PVC

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: juicefs-ee-sandbox
spec:
  capacity:
    storage: 100Gi
  accessModes:
    - ReadWriteMany
  persistentVolumeReclaimPolicy: Retain
  csi:
    driver: customfuseplugin.csi.alibabacloud.com
    volumeHandle: juicefs-ee-sandbox   # unique ID
    volumeAttributes:
      source: "myjfs"                  # JuiceFS volume name
      fuseType: "juicefs-ee"           # for metrics labeling
      authType: "agent-identity"       # triggers dynamic credential delivery
      # sandboxId and sandboxCredProviderName are injected by sandbox-controller
      # — customer does NOT set these manually
    nodePublishSecretRef:
      name: juicefs-ee-secret          # contains consoleToken (non-credential config)
      namespace: default
  mountOptions:
    - cache-size=1024                  # passed as $cache-size to entrypoint
---
apiVersion: v1
kind: Secret
metadata:
  name: juicefs-ee-secret
  namespace: default
type: Opaque
stringData:
  consoleToken: "eyJ..."              # JuiceFS console token (NOT an OSS credential)
```

### 6.5 What Happens at Runtime (Transparent to Customer)

```
1. Customer creates SandboxClaim referencing the PVC
2. sandbox-controller:
   - Injects mount-proxy sidecar into sandbox pod
   - Patches volumeAttributes with sandboxId + sandboxCredProviderName
3. ack-agent-identity controller:
   - Issues token file inside the sandbox pod
4. csi-agent (inside sandbox):
   - Sends mount request to mount-proxy
5. mount-proxy CredentialRefresher:
   - Reads agent token → exchanges for STS → writes to /var/run/customfuse/credentials/
6. mount-proxy starts entrypoint.sh:
   - Entrypoint reads STS from files → runs juicefs auth → mounts
7. Ongoing:
   - CredentialRefresher rotates STS every ~50 min (before 1h expiry)
   - Entrypoint's background loop detects file change → re-runs juicefs auth
```

### 6.6 What the Customer Does NOT Need to Know/Do

- No need to understand agent-identity protocol details
- No need to handle token exchange HTTP calls
- No need to manage CA certificates
- No need to configure `sandboxId` or `sandboxCredProviderName` (auto-injected)
- No need to modify the FUSE client binary itself

## 7. Responsibility Boundaries and Troubleshooting

### 7.1 Responsibility Matrix

| Layer | Owner | Scope |
|-------|-------|-------|
| **Agent-identity infrastructure** | Cloud platform (us) | Token issuance, credential-provider API, CA certs, sandbox-controller injection |
| **CSI driver + mount-proxy** | Cloud platform (us) | CredentialRefresher, STS file delivery, mount lifecycle, env var injection |
| **Entrypoint script** | Customer | Credential-bridging logic (reading files → feeding to FUSE client), background refresh loop |
| **FUSE client binary** | External storage vendor | The actual FUSE implementation, its credential acceptance mechanism, storage protocol |
| **Storage backend** | Customer / External vendor | Metadata service, object storage bucket, network connectivity |

### 7.2 Support Boundary: What We Guarantee vs. What We Don't

**We guarantee (CSI platform)**:
- STS credential files are present at `$credentialDir` before entrypoint starts
- Files are atomically updated before expiration (with 5-min margin)
- File format is documented and stable: `AccessKeyId`, `AccessKeySecret`, `SecurityToken`, `Expiration`
- Environment variables (`$credentialDir`, `$authType`, `$source`, `$mountpoint`, etc.) are correctly set
- mount-proxy correctly manages FUSE process lifecycle (signals, cleanup)

**We do NOT guarantee**:
- That the FUSE client can consume the credentials correctly (entrypoint responsibility)
- That the entrypoint's refresh logic works (customer's code)
- That the storage backend is accessible from the sandbox network
- That the FUSE client handles credential rotation gracefully (client/vendor property)
- I/O performance or cache behavior of the FUSE client

### 7.3 Troubleshooting Guide: Who to Contact

| Symptom | Likely Cause | Check | Owner |
|---------|-------------|-------|-------|
| Mount fails immediately with "credential refresher start failed" | Token file not available, credential-provider unreachable, or CA mismatch | Check mount-proxy logs for HTTP error details; verify agent-identity controller is healthy | **Cloud platform** |
| Mount fails with "permission denied" from storage API | STS scope too narrow, or CredentialProvider CR misconfigured | Check `Expiration` file is in future; verify CredentialProvider policy matches bucket/path | **Cloud platform** (policy config) |
| Mount succeeds initially but fails after ~1 hour | Entrypoint's background refresh loop is broken or FUSE client doesn't pick up new creds | Check if `AccessKeyId` file content has changed (refresher working?); check entrypoint logs for refresh errors | **Customer** (entrypoint) if files are refreshing; **Cloud platform** if files are stale |
| Mount succeeds but I/O is slow or errors | FUSE client issue, network, or storage backend | Check FUSE client's own logs; test storage connectivity directly | **Customer / External vendor** |
| "unsupported authType" error | `authType=agent-identity` used on non-sandbox node (RunC path) | agent-identity only works in sandbox; check node type | **Customer** (misconfiguration) |
| Entrypoint exits with "command not found" | FUSE binary missing from image or wrong path | Check Dockerfile, verify binary is executable | **Customer** (image build) |
| Credential files exist but FUSE client rejects them | Client doesn't support STS, or entrypoint format conversion is wrong | Manually `cat` credential files; verify entrypoint passes them correctly | **Customer** (entrypoint) or **External vendor** (client STS support) |

### 7.4 Diagnostic Commands

For platform support engineers to verify the credential delivery is working:

```bash
# 1. Check credential files exist and are fresh
kubectl exec -n <ns> <pod> -c mount-proxy -- ls -la /var/run/customfuse/credentials/
kubectl exec -n <ns> <pod> -c mount-proxy -- cat /var/run/customfuse/credentials/Expiration

# 2. Check mount-proxy logs for refresh activity
kubectl logs -n <ns> <pod> -c mount-proxy | grep -i "credential\|refresh\|token"

# 3. Check if entrypoint is running
kubectl exec -n <ns> <pod> -c mount-proxy -- ps aux | grep entrypoint

# 4. Check agent-identity token is present
kubectl exec -n <ns> <pod> -c mount-proxy -- ls /var/opt/sandbox/agent-token/

# 5. Verify the credential-provider endpoint is reachable
kubectl exec -n <ns> <pod> -c mount-proxy -- \
    curl -sk https://credential-provider.ack-agent-identity.svc:8443/healthz
```

### 7.5 Answering Pre-Sales Questions

**Q: Which external storage systems can we say "we support" with agent-identity?**

A: Any FUSE-based storage that meets the admission requirements (Section 3): supports STS tokens AND has a runtime credential refresh mechanism. Currently confirmed:
- **JuiceFS EE** — fully compatible, highest priority (production customers)
- **JuiceFS CE** — fully compatible (community/sample)
- **JindoFS** — fully compatible (native file-based refresh)
- **GeeseFS** — fully compatible (HTTP endpoint refresh)
- **s3fs** — partially compatible (limited refresh, works for short-lived mounts)

**Q: What exactly do we provide vs. what does the customer need to do?**

A: We provide the entire credential lifecycle (token exchange → STS delivery → rotation). The customer provides: (1) a Docker image with their FUSE client, and (2) an entrypoint script (~20-50 lines of bash) that reads our credential files and feeds them to the client. We provide reference entrypoint scripts for supported clients.

**Q: What if the customer's FUSE client doesn't support STS tokens?**

A: They must fall back to static AK/SK stored in a Kubernetes Secret (the existing default auth path). This is less secure but universally compatible. We should advise them to request STS support from their storage vendor.

**Q: What's the security trade-off vs. ossfs?**

A: ossfs keeps STS in memory only. CustomFuse writes them to files within the sandbox VM. The risk is minimal because: (1) credentials are permission-scoped and expire in ~1 hour, (2) the sandbox is single-tenant isolated, (3) files have 0600 permissions. This is the same pattern as AWS IRSA and Kubernetes service account tokens.

## 8. Client Compatibility Matrix

| FUSE Client | Credential Delivery Method | Rotation Strategy | Entrypoint Complexity |
|-------------|--------------------------|-------------------|----------------------|
| JuiceFS EE | Files → `juicefs auth` (background loop) | Entrypoint polls files, re-runs `juicefs auth`; running mount uses `--no-update` so credentials take effect on next remount | Medium — same pattern as CE but refresh is best-effort for long-running mounts |
| JuiceFS CE | Files → `juicefs config` (background loop) | Entrypoint polls files, runs `juicefs config` on change; JuiceFS hot-swaps storage client within 12s | Medium — requires background process |
| JindoFS | Files → `secrets:///` provider (native) | Native — JindoFS re-reads files automatically based on Expiration | Low — single flag |
| s3fs | Files → periodic passwd_file rewrite | Entrypoint background loop rewrites passwd_file; s3fs re-reads on next request | Medium — s3fs has limited refresh support |
| GeeseFS | Local HTTP credential endpoint (future) | Native — GeeseFS fetches from endpoint, background + lazy refresh | Low — single flag |
| Generic | Files → entrypoint reads on start | No rotation (short-lived mounts) | Low — one-shot read |

## 9. Implementation Plan

### Phase 1: Core (MVP for JuiceFS EE)

1. **CredentialRefresher** — implement token exchange and file-based delivery
2. **CustomFuse driver integration** — detect `authType=agent-identity`, start refresher, inject env vars
3. **CSI options parsing** — add `sandboxId`, `sandboxCredProviderName` to customfuse options
4. **ApplyOptionDefaults** — inject infra params (endpoint, token file, CA file)
5. **JuiceFS EE reference entrypoint** — with background `juicefs auth` loop
6. **Unit tests** — credential refresher, options parsing

### Phase 2: Robustness

7. **Graceful degradation** — if initial token exchange fails, log error and let entrypoint start with empty credentials (entrypoint can retry)
8. **Metrics** — refresh count, refresh failures, credential age
9. **tmpfs credential directory** — fuse pod manager adds emptyDir with medium=Memory for credential dir
10. **Credential file cleanup** on stop

### Phase 3: Extended Client Support

11. **Local HTTP credential endpoint** — mount-proxy serves `http://localhost:<port>/credentials` (ECS metadata format) for clients supporting `--iam-url` or `ram_role`
12. **JuiceFS CE community sample** — reference entrypoint using `juicefs config`
13. **Compatibility validation** for JindoFS, GeeseFS (confirm, not productize)

## 10. Alternatives Considered

### 10.1 Modify `MountOperation` to carry auth context

Rejected. This would require changes to the proxy protocol and affect all drivers. Agent-identity is specific to the sandbox scenario and should not pollute the generic mount interface.

### 10.2 Separate sidecar for credential refresh

Rejected. Adding another container complicates the pod topology. mount-proxy already runs as PID 1 in the fuse pod and is the natural place to manage credentials — it already knows the mount lifecycle.

### 10.3 Pass STS credentials via mount-proxy protocol (in MountRequest.Secrets)

Partially adopted for the **initial** credentials (first mount), but insufficient for refresh. The mount request is a one-shot message; there is no mechanism to push updated credentials over the socket after mount completes. File-based delivery is necessary for the refresh cycle.

### 10.4 Rely entirely on entrypoint for token exchange

Rejected. This would require every entrypoint author to implement the agent-identity protocol (HTTP POST, JSON parsing, CA validation, refresh loop). Centralizing this in mount-proxy provides a consistent, tested implementation that entrypoint authors can consume via simple file reads.

### 10.5 Embed credential refresh into the FUSE client images

Rejected. We cannot modify third-party FUSE client images. The CustomFuse philosophy is "bring your own image" — we should not require image changes beyond adding mount-proxy and an entrypoint script.

## 11. Open Questions

### 11.1 [BLOCKING] JuiceFS EE Runtime Credential Refresh Capability

**Context**: The current JuiceFS EE sandbox integration uses permanent AK/SK from Kubernetes Secret. With agent-identity, credentials become STS tokens that expire (~1 hour). The running `juicefs mount` process must be able to pick up new credentials.

**Current entrypoint (from production usage)**:
```bash
juicefs auth --token=${token} --accesskey=${ak} --secretkey=${sk} --bucket=${url} ${source}
exec juicefs mount --foreground ${source} ${mountpoint}
```
No background refresh exists because permanent credentials don't expire.

**Questions to confirm with JuiceFS team**:

| # | Question | Impact on Design |
|---|----------|-----------------|
| Q1 | Does a running `juicefs mount` process automatically refresh object storage credentials by periodically re-contacting the JuiceFS Console (using the console token)? | If yes → simplest path. We only need to ensure Console-side has STS role configured. No entrypoint change needed. |
| Q2 | If `juicefs auth` is re-run with new AK/SK/Token while mount is already running, does the mount process detect and pick up the updated config file (`~/.juicefs/<name>.conf`)? How quickly? | If yes → our background `juicefs auth` loop works as designed. |
| Q3 | Does JuiceFS EE support `--session-token` in `juicefs auth`? (i.e., can it accept STS tokens, not just permanent AK/SK?) | If no → agent-identity is completely incompatible with JuiceFS EE, which is a showstopper. |
| Q4 | If the running mount does NOT refresh credentials, what is JuiceFS's recommended approach for rotating object storage credentials without remounting? | Determines our fallback strategy. |
| Q5 | Is there a `juicefs config`-like command in EE (equivalent to CE's metadata-engine credential update) that can trigger a running mount to use new credentials? | If yes → same pattern as CE, simplest entrypoint. |

**Decision tree based on answers**:

```
Q3=No → SHOWSTOPPER: JuiceFS EE cannot use agent-identity at all
Q3=Yes, Q1=Yes → Simplest: Console handles refresh, entrypoint unchanged
Q3=Yes, Q1=No, Q2=Yes → Design as documented: background juicefs auth loop
Q3=Yes, Q1=No, Q2=No, Q5=Yes → Use the EE equivalent of juicefs config
Q3=Yes, Q1=No, Q2=No, Q5=No → Only option: short STS lifetime + pod recycling (degraded)
```

### 11.2 SandboxId Injection

How does the sandbox-controller inject `sandboxId` into CustomFuse volumeAttributes? Need to confirm the controller's injection mechanism is compatible (same as OSS: controller patches volumeAttributes with `sandboxId` and `sandboxCredProviderName`).

### 11.3 Multiple Volumes per Sandbox

If a sandbox has multiple CustomFuse PVs, each gets its own mount-proxy mount request. Should credential refresh be shared (one refresher per sandbox) or per-mount (simpler, slightly redundant)? Recommendation: **per-mount** — simpler, and the overhead of one extra HTTP call per volume per hour is negligible.

### 11.4 Token File Availability Timing

The agent-identity token file may not be available immediately when mount-proxy starts. Need retry logic with backoff in CredentialRefresher initialization.

### 11.5 Credential Directory Path Conflicts

If multiple volumes mount in the same fuse pod (not current architecture, but future-proofing), the credential directory should be scoped per-volume: `/var/run/customfuse/credentials/<volumeId>/`.
