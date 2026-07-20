# 设计：CustomFuse 在 Sandbox 场景下的 Agent Identity 鉴权

## 1. 背景

### 1.1 业务背景

使用 ACK Sandbox（RunD）环境的客户需要挂载 JuiceFS、JindoFS、s3fs 等外部 FUSE 存储。当前 Sandbox 场景使用 **agent-identity** 鉴权：外部控制器（`ack-agent-identity`）为每个 sandbox 签发 token，FUSE 客户端使用该 token 换取受权限约束的短时 STS 凭证。

### 1.2 问题陈述

Agent-identity 协议**非开源**，与阿里云内部基础设施紧耦合。要求每个第三方 FUSE 客户端都像 ossfs 一样原生实现 agent-identity 支持是不现实的。我们需要一个通用方案：

1. 在 `mount-proxy`（我们可控的基础设施层）内完成 agent-identity token 交换
2. 通过文件或环境变量将 STS 凭证传递给任意 FUSE 客户端
3. 支持周期性凭证刷新，无需重启 FUSE 进程

### 1.3 现状

#### ossfs 如何处理 agent-identity（理想路径）

ossfs 在其 C++ 凭证刷新循环中内置了 agent-identity 支持：

```
ossfs 进程（内存中）
  → 读取 /var/opt/sandbox/agent-token/<sandboxId>.token
  → POST 到 credential-provider 端点（携带 Bearer token + CA 证书）
  → 收到 STS {accessKeyId, accessKeySecret, securityToken, expiration}
  → 存储在内存中，过期前自动刷新
  → 使用 V4 签名发起 OSS 请求
```

关键特性：**STS 凭证从不落盘**，仅存在于 ossfs 进程内存中。

#### CustomFuse 当前鉴权方式

CustomFuse 在挂载时将 Kubernetes Secret 条目作为环境变量传递给入口脚本。无凭证刷新机制——凭证在 FUSE 进程的整个生命周期中是静态的。

```
CSI NodePublish → mount-proxy → entrypoint.sh（环境变量：$accessKeyId, $accessKeySecret, ...）
```

## 2. 调研：FUSE 客户端凭证接受方式

### 2.1 JuiceFS 社区版（CE）

| 方式 | 机制 |
|------|------|
| 格式化时 | `--access-key`、`--secret-key`、`--session-token` 参数 |
| 环境变量 | `ALICLOUD_ACCESS_KEY_ID`、`ALICLOUD_ACCESS_KEY_SECRET`、`SECURITY_TOKEN`（OSS 后端） |
| 运行时刷新 | **元数据驱动热加载**：`baseMeta.refresh()` 协程每 12 秒从元数据引擎读取 Format 结构。凭证变更时热替换存储客户端 |
| 运行时轮转方式 | `juicefs config <META-URL> --access-key NEW --secret-key NEW --session-token NEW` |
| 文件方式 | 不支持——不监控文件中的凭证变化 |

**JuiceFS CE 核心要点**：规范的轮转机制是通过 `juicefs config` 更新元数据引擎（Redis/MySQL/TiKV）。入口脚本中的后台进程可以周期性执行此命令注入新 STS token。

### 2.2 JuiceFS 企业版（EE）

JuiceFS EE 使用**双层认证模型**：

| 层级 | 用途 | 机制 |
|------|------|------|
| 控制台 Token（`--token`） | 控制面认证（获取 FS 配置、元数据地址） | 从 JuiceFS Web Console 签发 |
| 对象存储 AK/SK | 数据面认证（直接访问 OSS/S3） | 与 CE 相同——客户端直接访问存储 |

| 方式 | 机制 | 已确认？ |
|------|------|:---:|
| Auth 命令 | `juicefs auth <name> --token T --access-key AK --secret-key SK --session-token TOKEN` | ⚠️ 需确认 `--session-token` 参数是否存在 |
| 配置缓存 | `~/.juicefs/<name>.conf`（或 `--conf-dir`） | 是 |
| 挂载命令 | `juicefs mount --foreground <name> <path>`（使用 auth 生成的缓存配置） | 是（生产使用中） |
| STS 支持 | 未知——当前生产仅使用永久 AK/SK | ⚠️ **必须确认**（见 11.1 Q3） |
| 运行时刷新 | 未知——EE 闭源。可能路径：Console 介入 re-auth、配置文件重读、或无刷新 | ⚠️ **必须确认**（见 11.1 Q1/Q2） |

**当前生产入口脚本**（来自 sandbox 操作指南，使用永久 AK/SK）：
```bash
juicefs auth --token=${token} --accesskey=${ak} --secretkey=${sk} --bucket=${url} ${source}
export JFS_FOREGROUND="1"
exec juicefs mount ${otherOpts} ${source} ${mountpoint}
```

**JuiceFS EE 核心要点**：与 CE（有明确文档的 `juicefs config` → 元数据引擎热加载）不同，EE 的凭证刷新行为**尚未确认**。设计假设可以工作（通过后台重新执行 `juicefs auth`），但受阻于第 11.1 节 Q1-Q5 的确认结果。如果 EE 无法接受 STS token 或无法运行时刷新，CustomFuse 对 JuiceFS EE 的 agent-identity 支持可能需要不同方案或不可行。

### 2.3 JindoFS / JindoFuse

| 方式 | 机制 |
|------|------|
| 配置文件 | `fs.oss.accessKeyId`、`fs.oss.accessKeySecret`、`fs.oss.securityToken` |
| 环境变量 | `OSS_ACCESS_KEY_ID`、`OSS_ACCESS_KEY_SECRET`、`OSS_SECURITY_TOKEN` |
| CustomCredentialsProvider（HTTP） | `aliyun.oss.provider.url=http://...` — 周期性从端点获取 JSON |
| CustomCredentialsProvider（secrets） | `aliyun.oss.provider.url=secrets:///path/prefix` — 周期性从磁盘读取凭证文件 |
| secrets 协议文件格式 | `{prefix}AccessKeyId`、`{prefix}AccessKeySecret`、`{prefix}SecurityToken` |

**JindoFS 核心要点**：`secrets:///` 协议完美适配——mount-proxy 将凭证文件写入已知目录，JindoFS 周期性读取。

### 2.4 s3fs-fuse

| 方式 | 机制 |
|------|------|
| passwd_file | `AKID:SECRET`（此格式不支持 session token） |
| AWS credentials 文件 | `~/.aws/credentials`，含 `aws_session_token` 字段 |
| 环境变量 | `AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`、`AWS_SESSION_TOKEN` |
| credlib 插件 | `-o credlib=libname.so` — 调用 `UpdateS3fsCredential()` 进行刷新 |
| IAM role | `-o iam_role=auto` — 从 IMDS 自动刷新 |
| 运行时刷新 | 静态凭证：**无刷新**。IAM/credlib：自动刷新 |

**s3fs 核心要点**：静态凭证无法轮转。对于 STS 场景，需要 credlib 插件或 IAM 角色模拟（自定义 IMDS 端点）。

### 2.5 GeeseFS

| 方式 | 机制 |
|------|------|
| AWS credentials 文件 | 标准格式，含 `aws_session_token` |
| 环境变量 | 标准 AWS 环境变量 |
| 自定义 IAM 端点 | `--iam --iam-url URL --iam-flavor imdsv1` — 从自定义 HTTP 端点获取 |
| 刷新 | 后台定时器 + 懒刷新；过期前 5 分钟重新获取 |

**GeeseFS 核心要点**：`--iam-url` 指向本地 HTTP 端点（由 mount-proxy 提供），即可实现自动刷新。

### 2.6 总结：通用凭证传递方式

| 传递方式 | 支持的客户端 | 支持轮转 |
|----------|-------------|----------|
| 启动时环境变量 | 全部 | 否（一次性） |
| 凭证文件（周期性重读） | JindoFS（`secrets:///`）、s3fs（credlib） | 是 |
| 元数据引擎更新 | JuiceFS CE（`juicefs config`） | 是 |
| 本地 HTTP 凭证端点 | GeeseFS（`--iam-url`）、JindoFS（HTTP provider） | 是 |
| 自定义凭证进程/库 | s3fs（`credlib`）、goofys/GeeseFS（AWS `credential_process`） | 是 |

## 3. 客户端准入要求

本节定义 FUSE 客户端使用 agent-identity 鉴权**必须具备的能力**。不满足条件的客户端必须回退到 Kubernetes Secret 中存储的固定凭证（`authType=""`，现有默认路径）。

### 3.1 强制要求

| # | 要求 | 原因 |
|---|------|------|
| R1 | **支持 STS Token** — 客户端必须能接受临时凭证（AccessKeyId + AccessKeySecret + SecurityToken），而非仅支持永久 AK/SK | Agent-identity 签发的是有时限的 STS 凭证。只接受永久 AK/SK 的客户端无法消费。 |
| R2 | **运行时凭证刷新** — 客户端必须有不重启进程即可加载新凭证的机制（文件重读、config 命令、HTTP 端点或凭证库） | STS token 会过期（通常 1 小时）。没有刷新机制，token 过期后挂载即失败。 |
| R3 | **前台模式** — 客户端必须支持前台运行（`-f`、`--foreground` 或等价参数） | mount-proxy 管理 FUSE 进程生命周期；守护进程化会破坏生命周期管理和信号传递。 |

### 3.2 决策矩阵

| STS 支持 | 运行时刷新 | 结论 |
|:---:|:---:|:---|
| 是 | 是 | **完全兼容** — agent-identity 可轮转使用 |
| 是 | 否 | **部分兼容** — 短生命周期挂载（< token 有效期）可用。长时间运行的挂载在 token 过期后将失败。若客户接受定期 Pod 回收则可行。 |
| 否 | N/A | **不兼容** — 必须使用 Secret 中的固定 AK/SK |

### 3.3 客户端评估结果

| 客户端 | STS | 运行时刷新 | 刷新机制 | 结论 |
|--------|:---:|:---:|----------|------|
| JuiceFS EE | ⚠️ 未确认 | ⚠️ 未确认 | 可能：Console re-auth、配置文件重读、或 `juicefs auth` 重执行 | **⚠️ 待确认** — 受阻于 Q1-Q5（第 11.1 节） |
| JuiceFS CE | 是 | 是 | `juicefs config` → 元数据引擎热加载（12 秒） | 完全兼容 |
| JindoFS | 是 | 是 | `secrets:///` 文件 provider（原生周期性重读） | 完全兼容 |
| GeeseFS | 是 | 是 | `--iam-url` HTTP 端点（后台 + 懒刷新） | 完全兼容 |
| s3fs | 是 | 部分 | `use_session_token` + passwd_file 重写；重读时机不确定 | 部分兼容 |
| CubeFS | 是 | 否 | 仅挂载时静态配置 | 部分兼容（短生命周期挂载） |

> **注意**：JuiceFS EE 是最高优先级客户端（生产客户）。其兼容性当前**待确认**。整个设计方案的前提是 EE 能接受 STS token 并在运行时刷新。详见第 11.1 节的决策树。

### 3.4 回退方案：Secret 中的固定凭证

对于不满足 R1 或 R2 的客户端，可使用现有 `authType=""` 路径：

```yaml
# PV spec（不设置 authType 字段 → 默认为 secret 透传）
volumeAttributes:
  source: "my-bucket:/path"
  fuseType: "cubefs"
# Secret 中包含永久 AK/SK 键值对
# 入口脚本通过环境变量接收：$accessKeyId, $accessKeySecret
```

安全性较低（长期凭证），但兼容任何启动时接受凭证的客户端。

## 4. 设计方案

### 4.1 架构概览

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│  Sandbox Pod（mount-proxy sidecar，由 sandbox-controller 注入）                    │
│                                                                                  │
│  ┌─────────────────────────────────────────────────────────────────────────────┐ │
│  │ csi-mount-proxy-server（PID 1）                                              │ │
│  │                                                                             │ │
│  │  ┌───────────────────────┐    ┌──────────────────────────────────────────┐  │ │
│  │  │ Credential Refresher  │    │ CustomFuse Driver                        │  │ │
│  │  │（新组件）               │    │                                          │  │ │
│  │  │                       │    │  接收挂载请求                             │  │ │
│  │  │ • 读取 agent token    │    │  → 从 options/secrets 设置环境变量        │  │ │
│  │  │ • 调用 cred-provider  │    │  → 执行 /entrypoint.sh                  │  │ │
│  │  │ • 将 STS 写入文件     │    │                                          │  │ │
│  │  │ • 定时刷新            │    │  entrypoint.sh:                          │  │ │
│  │  └───────────┬───────────┘    │    从文件读取 STS（或 env/cmd）           │  │ │
│  │              │                 │    挂载 FUSE 客户端到 $mountpoint         │  │ │
│  │              │ 写入            └──────────────────────────────────────────┘  │ │
│  │              ▼                                                               │ │
│  │  /var/run/customfuse/credentials/                                           │ │
│  │    ├── AccessKeyId                                                          │ │
│  │    ├── AccessKeySecret                                                      │ │
│  │    ├── SecurityToken                                                        │ │
│  │    └── Expiration                                                           │ │
│  └─────────────────────────────────────────────────────────────────────────────┘ │
│                                                                                  │
│  外部文件（由 ack-agent-identity 控制器放置）：                                    │
│    /var/opt/sandbox/agent-token/<sandboxId>.token                                │
│    /etc/ssl/certs/agent-identity/ca.crt                                          │
└──────────────────────────────────────────────────────────────────────────────────┘
```

### 4.2 核心设计：mount-proxy 中的 Credential Refresher

新的 `CredentialRefresher` 组件运行在 mount-proxy 中（customfuse driver 内），负责：

1. **Token 交换**：读取 agent-identity token 文件 → POST 到 credential-provider → 获取 STS 凭证
2. **文件传递**：将 STS 凭证以独立文件形式写入已知目录
3. **周期刷新**：在过期前重新获取凭证（可配置提前量）
4. **入口通知**：将凭证目录路径以 `$credentialDir` 环境变量暴露给入口脚本

```go
// pkg/mounter/proxy/server/customfuse/credential_refresher.go

type CredentialRefresher struct {
    tokenFile     string // /var/opt/sandbox/agent-token/<sandboxId>.token
    endpoint      string // credential-provider URL
    credProvider  string // credential provider 名称
    caFile        string // CA 证书路径（可选）
    outputDir     string // /var/run/customfuse/credentials/
    refreshMargin time.Duration // 过期前多久刷新（默认 5 分钟）

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

### 4.3 凭证文件格式

凭证以独立文件写入（兼容 JindoFS `secrets:///` 协议和 Kubernetes projected volume 约定）：

```
/var/run/customfuse/credentials/
├── AccessKeyId         # 纯文本，无尾换行
├── AccessKeySecret     # 纯文本，无尾换行
├── SecurityToken       # 纯文本，无尾换行
└── Expiration          # ISO 8601 格式：2026-04-09T04:27:09Z
```

**原子写入**：每个文件先写入临时文件，再通过 `rename(2)` 移入最终位置，避免读到不完整内容。

**为什么用独立文件而非组合格式**：我们的凭证是阿里云 STS token，用于签名 OSS 请求。我们范围内没有任何 FUSE 客户端会读 AWS credentials 文件（`~/.aws/credentials`）来访问 OSS——它们各自有不同的凭证接入机制。独立文件是通用的最小公约数：JindoFS 通过 `secrets:///` 原生读取，其他客户端的入口脚本直接 `cat` 需要的文件即可。

### 4.4 入口脚本接口

当 agent-identity 鉴权激活时，入口脚本接收以下**额外环境变量**：

| 环境变量 | 值 | 用途 |
|----------|---|------|
| `credentialDir` | `/var/run/customfuse/credentials` | 凭证文件所在目录（AccessKeyId、AccessKeySecret、SecurityToken、Expiration） |
| `authType` | `agent-identity` | 允许入口脚本按鉴权类型分支 |

入口脚本决定如何将凭证传递给对应的 FUSE 客户端。示例：

**JuiceFS EE 入口脚本**（后台循环执行 `juicefs auth`）：
```bash
#!/bin/bash
set -e

# 从文件读取 STS 凭证完成初始 auth
AK=$(cat "$credentialDir/AccessKeyId")
SK=$(cat "$credentialDir/AccessKeySecret")
TOKEN=$(cat "$credentialDir/SecurityToken")

juicefs auth "$source" --token="$consoleToken" \
    --access-key="$AK" --secret-key="$SK" --session-token="$TOKEN"

# 后台凭证刷新：凭证变化时重新执行 juicefs auth
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

exec mount.juicefs "$source" "$mountpoint" -o "foreground,no-update${otherOpts:+,$otherOpts}"
```

**JuiceFS CE 入口脚本**（元数据驱动的后台循环刷新）：
```bash
#!/bin/bash
set -e

AK=$(cat "$credentialDir/AccessKeyId")
SK=$(cat "$credentialDir/AccessKeySecret")
TOKEN=$(cat "$credentialDir/SecurityToken")

juicefs format --storage=oss \
    --bucket="http://$bucket.$url" \
    --access-key="$AK" --secret-key="$SK" --session-token="$TOKEN" \
    "$source" myjfs

# 后台凭证刷新：凭证变化时更新 JuiceFS 元数据
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

**JindoFS 入口脚本**（原生 `secrets:///` provider）：
```bash
#!/bin/bash
set -e
JINDO_OPTS="-ouri=$source -oendpoint=$url"
JINDO_OPTS="$JINDO_OPTS -ofs.oss.credentials.provider=com.aliyun.jindodata.oss.auth.CustomCredentialsProvider"
JINDO_OPTS="$JINDO_OPTS -oaliyun.oss.provider.url=secrets://$credentialDir/"
exec jindo-fuse -f $JINDO_OPTS "$mountpoint"
```

### 4.5 CSI 代码变更

#### 原则：对现有框架最小影响

Agent-identity 逻辑**完全封装在 mount-proxy 的 customfuse driver 内**。不修改：
- `proxy.MountRequest` / `proxy.Response` 协议结构体
- `mounter.MountOperation` 结构体
- 其他 driver（ossfs、ossfs2、alinas）
- fuse_pod_manager 框架

#### 4.5.1 新增：Credential Refresher（`pkg/mounter/proxy/server/customfuse/credential_refresher.go`）

```go
package customfuse

// CredentialRefresher 处理 agent-identity token 交换和 STS 凭证刷新。
// 当 mount options 中检测到 authType=agent-identity 时，每次挂载实例化一个。
type CredentialRefresher struct { ... }

// Start 启动凭证刷新循环。先执行初始获取（阻塞），
// 然后在后台持续刷新。初始获取失败时返回 error。
func (r *CredentialRefresher) Start(ctx context.Context) error { ... }

// Stop 终止后台刷新协程。
func (r *CredentialRefresher) Stop() { ... }

// Dir 返回凭证文件写入的目录路径。
func (r *CredentialRefresher) Dir() string { ... }
```

#### 4.5.2 修改：CustomFuse driver（`pkg/mounter/proxy/server/customfuse/driver.go`）

`extendedMounter.ExtendedMount` 方法增加 agent-identity 感知：

```go
func (m *extendedMounter) ExtendedMount(ctx context.Context, op *mounter.MountOperation) error {
    // ... 现有代码 ...

    // 新增：从 mount options 中检测 agent-identity auth
    authType, agentOpts := extractAgentIdentityOptions(op.Options)
    
    var refresher *CredentialRefresher
    if authType == "agent-identity" {
        refresher = NewCredentialRefresher(agentOpts)
        if err := refresher.Start(ctx); err != nil {
            return fmt.Errorf("credential refresher start failed: %w", err)
        }
        // 注入凭证环境变量给入口脚本
        env = append(env,
            "authType=agent-identity",
            "credentialDir="+refresher.Dir(),
        )
        // 从 env 中移除原始的 agent_identity_* 选项（仅限基础设施使用）
    }

    // ... 现有的入口脚本执行代码 ...

    if refresher != nil {
        // 监控：入口脚本退出时停止 refresher
        go func() {
            <-exited
            refresher.Stop()
        }()
    }

    // ... 现有代码 ...
}
```

#### 4.5.3 修改：CSI controllerserver/nodeserver（`pkg/customfuse/`）

`options.go` — 向 `fuseOptions` 添加 agent-identity 字段：

```go
type fuseOptions struct {
    // ... 现有字段 ...
    
    // Agent Identity（仅 sandbox）
    AuthType                string // "agent-identity" 或 ""
    SandboxId               string
    SandboxCredProviderName string
}
```

`options.go` — 解析新的 volumeAttributes：

```go
case "sandboxid":
    opts.SandboxId = value
case "sandboxcredprovidername":
    opts.SandboxCredProviderName = value
```

`options.go` — 将 agent-identity 参数作为 mount options 传递（仅作为传输）：

```go
func (o *fuseOptions) makeMountOptions() []string {
    // ... 现有代码 ...
    if o.AuthType == "agent-identity" {
        opts = append(opts, "authType=agent-identity")
        // endpoint、token file、CA file 在 mount-proxy 侧解析
        // （与 ossfs 同模式 — ApplyOptionDefaults 添加它们）
    }
    return opts
}
```

`controllerserver.go` — 阻止非 sandbox 路径使用 agent-identity：

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

#### 4.5.4 修改：mount-proxy `ApplyOptionDefaults`（customfuse）

```go
// pkg/mounter/proxy/server/customfuse/driver.go

func (h *Driver) ApplyOptionDefaults(options []string) []string {
    tm := mounterutils.IndexMountOptions(options)
    
    // 注入 agent-identity 基础设施选项（与 ossfs driver 同模式）
    if _, ok := tm["authType"]; ok {
        if ep := ossfpm.GetAgentIdentityEndpoint(); ep != "" {
            options = mounterutils.MergeMountOptions(options,
                []string{"agent_identity_endpoint=" + ep})
        }
        // token 文件路径从 sandboxId 推导（由 sandbox-controller 注入）
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

### 4.6 凭证刷新生命周期

```
时间线：
    t=0    mount-proxy 收到挂载请求
    t=0    CredentialRefresher 启动，执行初始 token 交换
    t=0    将 STS 凭证写入 /var/run/customfuse/credentials/
    t=0    入口脚本启动，读取凭证，挂载 FUSE
    t=N    凭证接近过期（过期时间 - 5 分钟）
    t=N    CredentialRefresher 重新读取 token 文件，交换新 STS
    t=N    原子覆写凭证文件
    t=N+Δ  FUSE 客户端检测到变化（轮询/心跳/重读）
    ...
    t=X    入口脚本退出（FUSE 卸载或崩溃）
    t=X    CredentialRefresher 停止
```

## 5. 安全分析

### 5.1 凭证暴露对比

| 方面 | ossfs（原生 agent-identity） | CustomFuse（本设计） |
|------|------------------------------|---------------------|
| STS 凭证存储 | 仅在内存中 | 磁盘文件（sandbox VM 内） |
| 凭证有效期 | 短时 STS（通常 1 小时） | 相同——同样的 STS token |
| 崩溃时暴露 | 进程内存释放 | 文件保留至 pod 清理 |
| 访问范围 | 受 CredentialProvider 策略约束 | 相同——同样的策略模板 |
| 泄露影响范围 | 限时内对特定 bucket 路径的读写 | 相同 |

### 5.2 风险评估

**风险：STS 凭证写入 sandbox VM 磁盘**

- **缓解 1 — 范围**：凭证受 CredentialProvider CR 策略模板约束（例如：对特定 bucket 路径的只读权限）。泄露的凭证无法超出声明的权限范围。
- **缓解 2 — 有效期**：STS token 会过期（通常 1 小时）。即使被窃取，过期后即失效。
- **缓解 3 — 文件权限**：凭证文件以 `0600` 权限写入，属主为 root。只有以 root 运行的进程（FUSE 客户端）可以读取。
- **缓解 4 — tmpfs 选项**（未来）：凭证目录可使用 `tmpfs`（纯内存文件系统）支撑，避免持久化到磁盘。节点故障时暴露风险几乎为零。
- **缓解 5 — 清理**：`CredentialRefresher.Stop()` 在关闭时删除凭证文件。

**风险：FUSE 进程可读取 agent-identity token 文件**

Agent-identity token 文件（`/var/opt/sandbox/agent-token/<sandboxId>.token`）已由外部控制器放置在 sandbox 文件系统中。FUSE 进程理论上可以直接读取。然而：
- 该 token 仅对特定 credential-provider 端点有效（内部 K8s 服务）
- 端点要求匹配 `sandboxClientId`——token 绑定到此 sandbox

**结论**：内存方式（ossfs）与文件方式（CustomFuse）的安全差异是**可接受的**，原因如下：
1. STS 凭证已受权限约束且有时间限制
2. Sandbox VM 是一个隔离环境，单一租户
3. 这是支持任意 FUSE 客户端（无需修改它们）的标准权衡

### 5.3 与现有模式的对比

此文件方式的凭证传递与业界实践一致：
- Kubernetes projected service account tokens：以文件形式写入
- AWS IRSA（IAM Roles for Service Accounts）：已知路径的 token 文件
- JindoFS `secrets:///` provider：为此模式专门设计
- GeeseFS `--iam-url`：本地 HTTP 端点提供凭证

## 6. 端到端使用指南（客户视角）

本节从**客户视角**描述完整使用流程：如何在 sandbox 环境中使用 agent-identity 鉴权挂载外部存储。这是产品/售前团队回答"客户具体怎么使用"所需的信息。

### 6.1 前置条件（平台侧 — 我方责任）

客户使用此功能前，以下需由云平台（我方）就绪：

1. ACK 集群配置了 sandbox（RunD）节点
2. 部署 `ack-agent-identity` 控制器（签发 token，提供 credential-provider API）
3. 部署 `ack-agent-sandbox-controller`（注入 mount-proxy sidecar，管理 SandboxClaim 生命周期）
4. 部署支持 CustomFuse + agent-identity 的 CSI 驱动（csi-plugin/csi-agent）
5. 创建 CredentialProvider CR（定义 RAM 角色 + 目标 bucket 的权限范围）
6. 配置 AgentIdentity + AgentRole + AgentRoleBinding CR

### 6.2 客户需要提供的内容

| 项目 | 说明 | 示例 |
|------|------|------|
| **FUSE 客户端镜像** | 包含 FUSE 二进制的 Docker 镜像（如 JuiceFS EE 客户端） | `registry.example.com/juicefs-ee:5.1` |
| **入口脚本** | 从文件读取凭证并驱动 FUSE 客户端的 Shell 脚本 | 见 6.3 |
| **PV/PVC 清单** | 含 `authType=agent-identity` 的 Kubernetes 卷定义 | 见 6.4 |
| **存储后端** | 实际的存储服务（OSS bucket、JuiceFS 元数据服务等） | `redis://meta.internal:6379/1` |

### 6.3 步骤一：构建镜像

客户构建的 Docker 镜像需包含：
1. 其 FUSE 客户端二进制
2. 将我方凭证文件桥接到客户端机制的入口脚本

**Dockerfile 示例（JuiceFS EE）**：
```dockerfile
FROM juicedata/mount:ee-5.1

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENTRYPOINT ["/entrypoint.sh"]
```

**entrypoint.sh**（我方凭证传递与 JuiceFS EE 之间的桥接）：
```bash
#!/bin/bash
set -e

# --- 凭证读取（平台自动提供以下环境变量）---
# $credentialDir = /var/run/customfuse/credentials（由 mount-proxy 写入）
# $authType = "agent-identity"
# $source = JuiceFS 卷名（来自 PV volumeAttributes）
# $mountpoint = 目标挂载路径（由 mount-proxy 管理）

AK=$(cat "$credentialDir/AccessKeyId")
SK=$(cat "$credentialDir/AccessKeySecret")
TOKEN=$(cat "$credentialDir/SecurityToken")

# --- JuiceFS EE auth（用我方 STS 换取 JuiceFS 配置）---
juicefs auth "$source" --token="$consoleToken" \
    --access-key="$AK" --secret-key="$SK" --session-token="$TOKEN"

# --- 后台凭证刷新 ---
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

# --- 挂载（前台模式，必须）---
exec mount.juicefs "$source" "$mountpoint" -o "foreground,no-update${otherOpts:+,$otherOpts}"
```

**客户须知关键点**：
- 入口脚本**必须**以前台模式运行（`exec` FUSE 进程，不要守护进程化）
- `$credentialDir`、`$authType`、`$source`、`$mountpoint` 由平台**自动设置**
- 凭证桥接逻辑（`juicefs auth` + 后台循环）是客户的责任
- PV `mountOptions` 或 `volumeAttributes` 中的其他环境变量同样可用（如 `$otherOpts`、`$bucket`、`$url`）

### 6.4 步骤二：创建 PV/PVC

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
    volumeHandle: juicefs-ee-sandbox   # 唯一 ID
    volumeAttributes:
      source: "myjfs"                  # JuiceFS 卷名
      fuseType: "juicefs-ee"           # 用于指标标记
      authType: "agent-identity"       # 触发动态凭证传递
      # sandboxId 和 sandboxCredProviderName 由 sandbox-controller 自动注入
      # — 客户不需要手动设置
    nodePublishSecretRef:
      name: juicefs-ee-secret          # 包含 consoleToken（非凭证的配置项）
      namespace: default
  mountOptions:
    - cache-size=1024                  # 作为 $cache-size 传给入口脚本
---
apiVersion: v1
kind: Secret
metadata:
  name: juicefs-ee-secret
  namespace: default
type: Opaque
stringData:
  consoleToken: "eyJ..."              # JuiceFS 控制台 token（非 OSS 凭证）
```

### 6.5 运行时发生了什么（对客户透明）

```
1. 客户创建引用 PVC 的 SandboxClaim
2. sandbox-controller：
   - 向 sandbox pod 注入 mount-proxy sidecar
   - 将 sandboxId + sandboxCredProviderName patch 到 volumeAttributes
3. ack-agent-identity 控制器：
   - 在 sandbox pod 内签发 token 文件
4. csi-agent（sandbox 内）：
   - 向 mount-proxy 发送挂载请求
5. mount-proxy CredentialRefresher：
   - 读取 agent token → 交换 STS → 写入 /var/run/customfuse/credentials/
6. mount-proxy 启动 entrypoint.sh：
   - 入口脚本读取 STS 文件 → 执行 juicefs auth → 挂载
7. 持续运行：
   - CredentialRefresher 每 ~50 分钟轮转 STS（1 小时过期前）
   - 入口脚本后台循环检测文件变化 → 重新执行 juicefs auth
```

### 6.6 客户不需要了解/操作的内容

- 不需要理解 agent-identity 协议细节
- 不需要处理 token 交换的 HTTP 调用
- 不需要管理 CA 证书
- 不需要配置 `sandboxId` 或 `sandboxCredProviderName`（自动注入）
- 不需要修改 FUSE 客户端二进制本身

## 7. 责任边界与故障排查

### 7.1 责任矩阵

| 层级 | 责任方 | 范围 |
|------|--------|------|
| **Agent-identity 基础设施** | 云平台（我方） | Token 签发、credential-provider API、CA 证书、sandbox-controller 注入 |
| **CSI 驱动 + mount-proxy** | 云平台（我方） | CredentialRefresher、STS 文件传递、挂载生命周期、环境变量注入 |
| **入口脚本** | 客户 | 凭证桥接逻辑（读取文件 → 喂给 FUSE 客户端）、后台刷新循环 |
| **FUSE 客户端二进制** | 外部存储厂商 | 实际的 FUSE 实现、其凭证接受机制、存储协议 |
| **存储后端** | 客户 / 外部厂商 | 元数据服务、对象存储 bucket、网络连通性 |

### 7.2 支持边界：我方保证 vs. 不保证

**我方保证（CSI 平台）**：
- 入口脚本启动前，STS 凭证文件已存在于 `$credentialDir`
- 文件在过期前被原子更新（提前 5 分钟）
- 文件格式已文档化且稳定：`AccessKeyId`、`AccessKeySecret`、`SecurityToken`、`Expiration`
- 环境变量（`$credentialDir`、`$authType`、`$source`、`$mountpoint` 等）正确设置
- mount-proxy 正确管理 FUSE 进程生命周期（信号、清理）

**我方不保证**：
- FUSE 客户端能否正确消费凭证（入口脚本的责任）
- 入口脚本的刷新逻辑是否正确（客户的代码）
- 存储后端从 sandbox 网络是否可达
- FUSE 客户端是否优雅处理凭证轮转（客户端/厂商属性）
- FUSE 客户端的 I/O 性能或缓存行为

### 7.3 故障排查指南：该找谁

| 现象 | 可能原因 | 检查方法 | 责任方 |
|------|----------|----------|--------|
| 挂载立即失败，报 "credential refresher start failed" | Token 文件不可用、credential-provider 不可达或 CA 不匹配 | 检查 mount-proxy 日志中的 HTTP 错误详情；验证 agent-identity 控制器是否健康 | **云平台** |
| 挂载失败，存储 API 报 "permission denied" | STS 范围过窄或 CredentialProvider CR 配置错误 | 检查 `Expiration` 文件是否在未来时间；验证 CredentialProvider 策略是否匹配 bucket/path | **云平台**（策略配置） |
| 挂载初始成功，约 1 小时后失败 | 入口脚本后台刷新循环异常或 FUSE 客户端未拾取新凭证 | 检查 `AccessKeyId` 文件内容是否有变化（refresher 工作正常？）；检查入口脚本日志中的刷新错误 | 文件在刷新→**客户**（入口脚本）；文件未刷新→**云平台** |
| 挂载成功但 I/O 慢或报错 | FUSE 客户端问题、网络问题或存储后端问题 | 检查 FUSE 客户端自身日志；直接测试存储连通性 | **客户 / 外部厂商** |
| "unsupported authType" 错误 | 在非 sandbox 节点（RunC 路径）使用了 `authType=agent-identity` | agent-identity 仅在 sandbox 中工作；检查节点类型 | **客户**（配置错误） |
| 入口脚本退出报 "command not found" | FUSE 二进制缺失或路径错误 | 检查 Dockerfile，验证二进制可执行 | **客户**（镜像构建） |
| 凭证文件存在但 FUSE 客户端拒绝 | 客户端不支持 STS，或入口脚本格式转换有误 | 手动 `cat` 凭证文件；验证入口脚本正确传递 | **客户**（入口脚本）或 **外部厂商**（客户端 STS 支持） |

### 7.4 诊断命令

平台支持工程师用于验证凭证传递是否正常：

```bash
# 1. 检查凭证文件存在且未过期
kubectl exec -n <ns> <pod> -c mount-proxy -- ls -la /var/run/customfuse/credentials/
kubectl exec -n <ns> <pod> -c mount-proxy -- cat /var/run/customfuse/credentials/Expiration

# 2. 检查 mount-proxy 日志中的刷新活动
kubectl logs -n <ns> <pod> -c mount-proxy | grep -i "credential\|refresh\|token"

# 3. 检查入口脚本是否在运行
kubectl exec -n <ns> <pod> -c mount-proxy -- ps aux | grep entrypoint

# 4. 检查 agent-identity token 是否存在
kubectl exec -n <ns> <pod> -c mount-proxy -- ls /var/opt/sandbox/agent-token/

# 5. 验证 credential-provider 端点可达
kubectl exec -n <ns> <pod> -c mount-proxy -- \
    curl -sk https://credential-provider.ack-agent-identity.svc:8443/healthz
```

### 7.5 售前常见问答

**Q：哪些外部存储系统可以跟客户说"我们支持 agent-identity"？**

A：任何满足准入要求（第 3 节）的 FUSE 存储：支持 STS token 且具备运行时凭证刷新机制。当前已确认：
- **JuiceFS EE** — 完全兼容，最高优先级（生产客户）
- **JuiceFS CE** — 完全兼容（社区/示例）
- **JindoFS** — 完全兼容（原生文件刷新）
- **GeeseFS** — 完全兼容（HTTP 端点刷新）
- **s3fs** — 部分兼容（刷新支持有限，短生命周期挂载可用）

**Q：我们具体提供什么 vs. 客户需要做什么？**

A：我方提供完整的凭证生命周期管理（token 交换 → STS 传递 → 轮转）。客户提供：(1) 包含 FUSE 客户端的 Docker 镜像；(2) 一个入口脚本（约 20-50 行 bash），从我方凭证文件读取并喂给客户端。我方提供支持客户端的参考入口脚本。

**Q：客户的 FUSE 客户端不支持 STS token 怎么办？**

A：必须回退到 Kubernetes Secret 中存储的固定 AK/SK（现有默认鉴权路径）。安全性较低但通用兼容。建议客户向存储厂商提出 STS 支持需求。

**Q：与 ossfs 相比安全水位差异是什么？**

A：ossfs 仅将 STS 保存在内存中。CustomFuse 将其写入 sandbox VM 内的文件。风险极小，因为：(1) 凭证受权限约束且约 1 小时过期；(2) sandbox 是单租户隔离环境；(3) 文件权限为 0600。这与 AWS IRSA 和 Kubernetes service account token 的模式相同。

## 8. 客户端兼容性矩阵

| FUSE 客户端 | 凭证传递方式 | 轮转策略 | 入口脚本复杂度 |
|-------------|-------------|----------|---------------|
| JuiceFS EE | 文件 → `juicefs auth`（后台循环） | 入口脚本轮询文件，重新执行 `juicefs auth`；使用 `--no-update` 运行的挂载在下次重挂载时生效 | 中——与 CE 相同模式，但对长时间运行的挂载是尽力而为 |
| JuiceFS CE | 文件 → `juicefs config`（后台循环） | 入口脚本轮询文件，变化时执行 `juicefs config`；JuiceFS 在 12 秒内热替换存储客户端 | 中——需要后台进程 |
| JindoFS | 文件 → `secrets:///` provider（原生） | 原生——JindoFS 根据 Expiration 自动重读文件 | 低——单一参数 |
| s3fs | 文件 → 周期性重写 passwd_file | 入口脚本后台循环重写 passwd_file；s3fs 下次请求时重读 | 中——s3fs 刷新支持有限 |
| GeeseFS | 本地 HTTP 凭证端点（未来） | 原生——GeeseFS 从端点获取，后台 + 懒刷新 | 低——单一参数 |
| 通用 | 文件 → 入口脚本启动时读取 | 不轮转（短生命周期挂载） | 低——一次性读取 |

## 9. 实施计划

### 阶段 1：核心（JuiceFS EE MVP）

1. **CredentialRefresher** — 实现 token 交换和文件传递
2. **CustomFuse driver 集成** — 检测 `authType=agent-identity`，启动 refresher，注入环境变量
3. **CSI options 解析** — 向 customfuse options 添加 `sandboxId`、`sandboxCredProviderName`
4. **ApplyOptionDefaults** — 注入基础设施参数（endpoint、token file、CA file）
5. **JuiceFS EE 参考入口脚本** — 含后台 `juicefs auth` 循环
6. **单元测试** — credential refresher、options 解析

### 阶段 2：健壮性

7. **优雅降级** — 若初始 token 交换失败，记录错误并让入口脚本以空凭证启动（入口脚本可自行重试）
8. **监控指标** — 刷新次数、刷新失败数、凭证年龄
9. **tmpfs 凭证目录** — fuse pod manager 为凭证目录添加 medium=Memory 的 emptyDir
10. **停止时凭证文件清理**

### 阶段 3：扩展客户端支持

11. **本地 HTTP 凭证端点** — mount-proxy 提供 `http://localhost:<port>/credentials`（ECS 元数据格式），供支持 `--iam-url` 或 `ram_role` 的客户端使用
12. **JuiceFS CE 社区 sample** — 使用 `juicefs config` 的参考入口脚本
13. **兼容性验证** — JindoFS、GeeseFS（确认兼容，不做产品化）

## 10. 备选方案

### 10.1 修改 `MountOperation` 以承载鉴权上下文

否决。这需要修改代理协议并影响所有 driver。Agent-identity 仅用于 sandbox 场景，不应污染通用挂载接口。

### 10.2 独立 sidecar 处理凭证刷新

否决。增加额外容器会使 pod 拓扑复杂化。mount-proxy 已经作为 PID 1 运行在 fuse pod 中，是管理凭证的天然位置——它已知挂载生命周期。

### 10.3 通过 mount-proxy 协议传递 STS 凭证（MountRequest.Secrets）

部分采纳用于**初始**凭证（首次挂载），但不足以支持刷新。挂载请求是一次性消息；挂载完成后没有机制通过 socket 推送更新后的凭证。刷新周期需要文件传递。

### 10.4 完全由入口脚本处理 token 交换

否决。这将要求每个入口脚本作者实现 agent-identity 协议（HTTP POST、JSON 解析、CA 验证、刷新循环）。将此逻辑集中在 mount-proxy 中提供一致的、经过测试的实现，入口脚本作者只需简单读取文件。

### 10.5 将凭证刷新嵌入 FUSE 客户端镜像

否决。我们无法修改第三方 FUSE 客户端镜像。CustomFuse 的理念是"自带镜像"——不应要求超出添加 mount-proxy 和入口脚本之外的镜像变更。

## 11. 待定问题

### 11.1 [阻塞项] JuiceFS EE 运行时凭证刷新能力

**背景**：当前 JuiceFS EE 在 sandbox 中使用的是 Kubernetes Secret 中的永久 AK/SK。切换到 agent-identity 后，凭证变为约 1 小时过期的 STS token。运行中的 `juicefs mount` 进程必须能够获取新凭证。

**当前入口脚本（生产环境使用中）**：
```bash
juicefs auth --token=${token} --accesskey=${ak} --secretkey=${sk} --bucket=${url} ${source}
exec juicefs mount --foreground ${source} ${mountpoint}
```
没有后台刷新，因为永久凭证不需要刷新。

**需要与 JuiceFS 团队确认的问题**：

| # | 问题 | 对设计的影响 |
|---|------|-------------|
| Q1 | 运行中的 `juicefs mount` 进程是否会通过 console token 周期性重新联系 JuiceFS Console 自动刷新对象存储凭证？ | 如果是 → 最简路径。只需确保 Console 端配置了 STS 角色。无需修改入口脚本。 |
| Q2 | 如果在 mount 运行中重新执行 `juicefs auth`（使用新的 AK/SK/Token），mount 进程是否会检测并加载更新后的配置文件（`~/.juicefs/<name>.conf`）？多久生效？ | 如果是 → 后台 `juicefs auth` 循环方案可行。 |
| Q3 | JuiceFS EE 的 `juicefs auth` 是否支持 `--session-token`？（即能否接受 STS token，而非仅永久 AK/SK？） | 如果否 → agent-identity 与 JuiceFS EE 完全不兼容，为阻塞性问题。 |
| Q4 | 如果运行中的 mount 不刷新凭证，JuiceFS 推荐的不重新挂载就轮转对象存储凭证的方案是什么？ | 决定我们的兜底策略。 |
| Q5 | EE 是否有类似 CE `juicefs config` 的命令（等价于 CE 的元数据引擎凭证更新），可以触发运行中的 mount 使用新凭证？ | 如果有 → 与 CE 同模式，最简入口脚本。 |

**基于回答的决策树**：

```
Q3=否 → 阻塞：JuiceFS EE 完全无法使用 agent-identity
Q3=是, Q1=是 → 最简：Console 处理刷新，入口脚本无需变化
Q3=是, Q1=否, Q2=是 → 按文档设计：后台 juicefs auth 循环
Q3=是, Q1=否, Q2=否, Q5=是 → 使用 EE 版本的 juicefs config 等价命令
Q3=是, Q1=否, Q2=否, Q5=否 → 唯一选项：短 STS 有效期 + Pod 回收（降级方案）
```

### 11.2 SandboxId 注入

sandbox-controller 如何将 `sandboxId` 注入 CustomFuse volumeAttributes？需确认控制器的注入机制兼容（与 OSS 相同：控制器将 `sandboxId` 和 `sandboxCredProviderName` patch 到 volumeAttributes）。

### 11.3 单 Sandbox 多 Volume

如果一个 sandbox 有多个 CustomFuse PV，每个 PV 有自己的 mount-proxy 挂载请求。凭证刷新应该共享（每 sandbox 一个 refresher）还是每挂载独立（更简单，略有冗余）？建议：**每挂载独立**——更简单，每 volume 每小时多一次 HTTP 调用的开销可忽略。

### 11.4 Token 文件可用时序

mount-proxy 启动时 agent-identity token 文件可能尚未就绪。CredentialRefresher 初始化需有退避重试逻辑。

### 11.5 凭证目录路径冲突

如果多个 volume 挂载在同一 fuse pod 中（当前架构不支持，但前瞻考虑），凭证目录应按 volume 隔离：`/var/run/customfuse/credentials/<volumeId>/`。
