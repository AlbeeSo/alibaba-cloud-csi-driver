# 手动 FUSE sidecar：配置与排障

本说明对应 `codex/fuse-coco-runtime-split` 分支，使用集群 CSI DaemonSet、RunD、手动原生 sidecar、双卷 OSSFS2 与 RRSA。绑定验证、标准卸载和控制 GC 已补齐；自建旧运行时的共享文件问题没有在实际 rund 原生文件小实验中复现，但完整 FUSE/RRSA 链路仍待实际环境验收。先阅读 [设计](fuse-manual-sidecar-design-zh.md)、[根因记录](fuse-manual-sidecar-rootcause-zh.md) 和 [验证记录](fuse-manual-sidecar-validation-zh.md)，不能直接当作任意 RunD 的生产安装方案。

## 前提

Kubernetes v1.33 及以上，目标节点与 RuntimeClass 能创建实际 RunD guest；该 guest 支持特权容器、FUSE 设备及同 Pod 的挂载传播。`runtimeClassName: rund` 是本实验使用的名字。runv handler 是测试版本差异，不据此区分代码模式。

CSI 使用本分支构建的非托管组件。启用 oss driver，保留集群 DS 的正常配置，`SKIP_GLOBAL_MOUNT` 使用默认 false；不能配全局 mountproxy socket 把本例导向已有 socket 分支。`RundCSIProtocol3=false`，否则旧逻辑可能提前 skip，不会落这套文件。

guest sidecar 仅接受 `authType: rrsa`，不接受默认 Secret 鉴权或其他 authType，也不向业务 sidecar 交付 CSI Secret。init container 名称必须为 `alibabacloud-csi-mountproxy`；按名称识别，不解析其启动 command/args。仍校验 Always、probe、权限和挂载结构；名称不是身份认证。自定义入口必须实际启动兼容的 file-mode 服务。

runc sidecar 尚未实现：后续允许 Secret 引用时，凭据须由 Pod 显式声明 Secret volume 并挂给 sidecar，不从 CSI 请求交付。当前 runc 独立 FUSE Pod 的 Secret 鉴权保持原有行为。

sidecar 镜像包含本分支的 `csi-mount-proxy-server`、ossfs2 及所需动态库。需要混合 ossfs1 时镜像还需包含 ossfs；file-mode 自动注册链接的 driver 不等于镜像自动包含所有外部可执行文件。customfuse 的可执行入口及环境仍由其镜像提供，本轮未做真实 customfuse 集群验收。

集群已启用 RRSA。业务 ServiceAccount 的 OIDC 身份拥有指定 bucket/prefix 的最小权限，角色信任限制 issuer、audience 与 `system:serviceaccount:<namespace>:<serviceaccount>`。这里不提供账号相关角色 ARN、OIDC provider 或真实凭据。

## 配置示例

完整 YAML 见 [双卷 RRSA Deployment](../examples/oss/file-sidecar/demo.yaml)。修改以下占位符后使用：

- `OSS_BUCKET`、endpoint/region、两个 path。
- `ACCOUNT_ID`、`RRSA_ROLE`、`CLUSTER_OIDC_PROVIDER`，角色匹配示例 namespace/SA。
- `OSSFS2_FILE_IMAGE`：本分支的 mountproxy + ossfs2 镜像，私有镜像按环境加 imagePullSecrets。

预先在两个 prefix 分别写入 `hello.txt`，内容为 `first-ossfs2-volume` 和 `second-ossfs2-volume`。示例应用读取两卷、验证第一卷写回和第二卷拒绝写入，再保持运行；不是通用业务容器模板。

PV `volumeAttributes.runtimeClass: rund` 进入 directAssigned 分流；Pod `runtimeClassName: rund` 决定实际运行时。两个配置不能互相替代。文件 sidecar 会拒绝实际 runc 或没有显式 RuntimeClass 的 Pod。`authType: rrsa`、roleArn/oidcProviderArn 对应 guest 内 STS 换证，serviceAccountName 必须匹配业务 Pod。

sidecar 的固定部分是：

```yaml
command: [csi-mount-proxy-server]
args: [--file-mode]
restartPolicy: Always
env:
  - name: POD_UID
    valueFrom: {fieldRef: {fieldPath: metadata.uid}}
startupProbe:
  exec:
    command: [csi-mount-proxy-server, --file-check-ready]
readinessProbe:
  exec:
    command: [csi-mount-proxy-server, --file-check-ready]
```

实际周期、超时、grace、资源配额以完整 YAML 为准。不要将该片段直接替换完整探针配置。

默认 state=/volume-state、target=/pod-volumes，不必传目录参数，不用 `--driver` 或 `--file-expected-volumes`。PodUID 既供命令默认读取，也供 state 的 subPathExpr 展开。

增加卷时只增减 Pod 的 volumes/volumeMounts 及对应 PV/PVC，不改 args/probe。sidecar 数据 mountPath 必须是 `/pod-volumes/<PV-name>/mount`，其中是 PV 名，不是 PVC 名或任意 volumeHandle。挂载路径和 MountRequest.Target 语义不变；CSI 写完整 manifest。

为避免冷启动的 PVC GET，可在静态 PV 的 `spec.csi.volumeAttributes` 提供三个一致字段（完整示例已包含）：

```yaml
csi.storage.k8s.io/pv/name: ossfs2-file-a
csi.storage.k8s.io/pvc/namespace: fuse-file-demo
csi.storage.k8s.io/pvc/name: oss-first
```

三者分别匹配当前请求的 PV 目标、Pod namespace 和 sidecar 对应挂载的 PVC 名时，不发起 PVC API 请求，也不依赖兜底缓存。动态供给可以在 external-oss-provisioner 增加 `--extra-create-metadata=true`，这些参数会由 CreateVolume 保留并随 PV 回传到 NodePublish；仓库 chart 目前未默认启用。静态声明及 Retain 后重绑定要同步更新，不是实时绑定证明。

上述字段应在 PV 创建时提供；开启 provisioner 参数不补写存量 PV。`spec.csi.volumeAttributes` 属于 PV 的不可变 volume source，不能把补字段或重绑定后的更正当成普通热更新。存量卷继续使用兜底；如需迁移，应遵循保留数据和 PV source 不可变的规则规划新的 PV 声明，本文不提供自动重建动作。依据见 [Kubernetes 校验](https://github.com/kubernetes/kubernetes/blob/v1.33.3/pkg/apis/core/validation/validation.go#L1995)。

任何已提供字段冲突都明确失败；一致但缺少字段才走兼容兜底：缓存未命中时 GET 一次 PVC 验证 Bound 关系，之后复用成功缓存。查询失败/未绑定不缓存，CSI 重启或淘汰后可能重新查询。不额外 GET RuntimeClass/PV/Node，不增加 list/watch。

mountproxy 必须放在使用这些 PVC 的其他 init container 之前。它的 startupProbe 只会阻塞后续 init 和应用；把数据准备 init 放在它前面，会读到空投影或写本地回退数据，因此当前明确拒绝这种配置。与数据卷无关的配置准备 init 可放前面。

不在首位会记录 INFO；前置 init 只要挂载 PVC，还会记录 WARNING，包含去重 `pvcCount/pvcs`、容器名及 `managedPVCCount/managedPVCs`。交集为零允许继续，有交集仍明确失败。告警来自已有 Pod 数据的本地扫描，与绑定查询是两件事，不会仅因非首位而 GET PVC。

state 是 host 控制目录 `/var/run/ossfs/file-volumes` 的 UID 子目录，不是远端数据卷。RRSA 是只读 projected 目录，不用 subPath；不把该 token 挂给业务容器也不影响业务身份用于挂载。

示例 `expirationSeconds: 86400` 是本轮绕过 guest 投射更新问题的测试窗口。应核对实际 token 的 exp−iat，但不要打印 JWT。到期前成功不代表轮转已通过，不建议把延长有效期当作生产修复。

## 启动和错误表现

```sh
kubectl -n fuse-file-demo get pods
kubectl -n fuse-file-demo describe pod <pod>
kubectl -n fuse-file-demo logs <pod> -c alibabacloud-csi-mountproxy
kubectl -n fuse-file-demo exec <pod> -c alibabacloud-csi-mountproxy -- csi-mount-proxy-server --file-check-ready
```

最后一条在全部期望卷成功且状态新鲜时返回 0，首次成功会写放行屏障；失败输出卷 key、phase 和脱敏原因。它不挂载、不发 Kubernetes 请求，也不读远端数据。startupProbe 成功前业务等待；挂载失败时 NodePublish 可能已经成功交付文件，不能据此判断卷已挂好。

不要直接输出完整 desired 或 projected token。当前 guest 路径仅允许 RRSA；需要状态时只读取 `/volume-state/status/*.json` 的 phase、error、updatedAt 和 generation/session；对外分享前仍应检查客户端 stderr 是否包含敏感内容。

| 现象 | 处理 |
|---|---|
| FailedMount: native mountproxy sidecar | 保留名称的 init sidecar 缺失，先修 Pod 模板 |
| FailedMount: startup/readiness exec probes | 补齐固定探针；不能用恒成功命令放行业务 |
| FailedMount: state/target contract | 核对 UID env、subPathExpr、PV 路径及传播模式 |
| FailedMount: PVC bound to PV / fix mapping | sidecar 的 volumeMount.name 与 mountPath 中 PV 对不上，修 Pod 模板；不要仅交换路径名称 |
| FailedMount: binding metadata ... conflicts | PV/PVC/namespace 声明与请求或 sidecar 挂载不一致；检查静态配置或 Retain 后的旧元数据；不会悄悄退回查询 |
| binding metadata incomplete; falling back to PVC lookup | 至少一个绑定字段缺失且缓存未命中，触发真实兜底 GET；若要零查询，补全一致字段 |
| not the first init container / Preceding init containers mount PVCs | 核对前置 init 的 PVC 数量及 managed 交集；不是所有 PVC 都属于本 sidecar；与本 sidecar 重叠时仍拒绝 |
| FailedMount: explicit non-runc RuntimeClass | PV 声明不代替实际 Pod runtime，补齐正确的 guest RuntimeClass |
| FailedMount: must precede init container | 把 mountproxy 放到消费这些 PVC 的 init container 前面 |
| RRSA projection / serviceAccountName 错误 | 核对只读 projected volume、audience、SA 一致性 |
| Failed + OSS/STS 原因 | 核对 prefix 权限、信任与 endpoint；修配置后重建 Pod |
| RequestMissing | 控制文件异常；保留已有挂载，排查 producer/共享路径，不将删文件当作卸载 |
| PodRecreationRequired | 已放行业务后消费者重启，重建整个 Pod |
| StartError: Socket not connected | 此测试版本在 sidecar 重建前遇到旧 FUSE；重建整个 Pod |
| CleanupPending | 活消费者的正常 umount 失败；检查引用，不能 lazy detach 后伪报成功 |
| 节点记录 guest ACK unavailable | 标准 kubelet 已停止消费者后不应再等回复；检查其后 host target 卸载和控制文件退休是否成功 |

只有 readiness 失败不会主动停止已经运行的业务。业务应处理 I/O 错误；需要退出/重试策略由应用明确实现。重建整个 Pod 前按业务要求确认写回，不能把 sidecar 透明重挂当作可用性保障。

## 删除与异常处理

正常通过 Deployment 缩容、删除 Pod 或任务完成退出。没有额外 preStop/postStart hook；mountproxy 接收 SIGTERM 执行 drain。不要以 `rm desired`、`rm -rf state` 或手写 shell umount 代替协议。

SIGKILL、grace 耗尽和强删可能没有 guest ACK。kubelet/CRI 负责先停止 Pod，NodeUnpublish 随后安全清理 host target 和自有控制文件，不因 ACK 缺失或控制 JSON 损坏无限等待。host target 非空、实际卸载失败或控制文件 I/O 失败仍会返回错误重试，不能递归删除来绕过。

CSI 启动和每 5 分钟会回收 kubelet 本地 Pod 目录已不存在的遗留控制文件；活跃目录、检查失败和未知内容保留。它不是 VM 清理器。API Pod NotFound、状态过期或 host 看不到 FUSE 都不单独作为 guest 销毁证明；运行时必须履行标准进程停止/沙箱回收契约。

实验退出后清理云资源与控制记录，需要掌握实际 runtime 已停止及资源所有权的证据。只可移除明确属于实验的控制文件和空目录；不对 kubelet Pod 数据目录、FUSE 目标或未知挂载路径执行递归删除。真实环境存在无法确认的旧 guest 时，交由运行时维护者处理；本说明不提供猜测性的强制清理命令。

## 开发者回归

在 Linux 运行以下检查；依赖缓存可用时无需真实集群：

```sh
go test -count=1 -p 1 ./cmd/mount-proxy-server
go test -count=1 -p 1 -run 'Test(DirectPublish|FileNodeUnpublish|DetermineRuntimeType|NodePublishVolume_RuntimeTypes)' ./pkg/oss
go test -count=1 -p 1 ./pkg/mounter/proxy/filevolume
go test -count=1 -p 1 -run 'Test(File|SocketDriverSelection|FuseDevice|MountFile)' ./pkg/mounter/proxy/server/...
```

`TestMountFile*` 通过真实子进程覆盖退出/错误/参数及 interceptor，无需真实 FUSE。`TestFileUnmountSyscall` 默认跳过；只在一次性特权 Linux 测试容器中设置 `CSI_FILE_MOUNT_TEST=1`，测试会创建私有 mount namespace 验证 EBUSY、无 lazy detach 及释放后的幂等卸载，不能在未知生产节点直接启用。

RunD E2E 仍负责验证 guest/host 视图、RRSA 与 kubelet 生命周期。完整矩阵未通过项在验证记录中逐项保留，不以本地测试通过替代。
