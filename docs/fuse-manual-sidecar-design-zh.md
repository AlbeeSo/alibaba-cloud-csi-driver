# RunD 手动 FUSE sidecar 设计

本分支实现集群 CSI DaemonSet 向业务 Pod 内的 mountproxy 交付挂载请求。2026-11-18 已修复并真实复测 PVC 绑定校验、无 ACK 的标准卸载和控制文件 GC；双卷 RRSA、混合 ossfs1/ossfs2、失败隔离与异常退出链路已有证据。自建旧运行时存在共享文件一致性问题；实际 rund 的补充原生文件实验已通过，没有复现该问题，但完整 FUSE/RRSA 链路仍须在实际环境验收。

- [验证记录与未通过项](fuse-manual-sidecar-validation-zh.md)
- [逐项根因、标准依据与修复](fuse-manual-sidecar-rootcause-zh.md)
- [使用方式与双卷 RRSA 示例](fuse-manual-sidecar-usage-zh.md)
- [完整 corner case 验收矩阵及 ACK Fluid internal 调研](fuse-manual-sidecar-test-plan-zh.md)

## 范围与分流

本轮只实现手动配置的原生 init sidecar、RunD、HostPath 控制目录、OSS 文件系统。无 webhook，无 runc sidecar RPC，无 csi-agent 改造。runv 是 RunD 的测试运行时变体，不作为产品模式开关。

```text
NodePublish(req):
    保留现有 RundCSIProtocol3 提前 skip 行为
    opts = 现有 PV / flag 解析
    runtime = DetermineRuntimeType(directAssigned, socket, skipGlobalMount)
    if runtime == 原 COCO 候选分支:
        pod = 复用公共包装层获取的 Pod
        验证 PodInfo UID/name/namespace、调度节点；拒绝 terminating Pod
        if pod.spec.runtimeClassName == "kata-remote":
            继续原 COCO 路径
        else:
            要求显式 non-runc RuntimeClass
            校验手动 sidecar / state / data mounts / probes / init 顺序
            校验当前 PVC 的标准 Bound 关系（PodUID 有界缓存）
            校验文件模式能力与鉴权
            从 Pod 的 sidecar mounts 生成完整 inventory
            发布不可变 desired(完整 MountRequest, generation)
            返回“请求交付成功”
    else:
        继续原 runc / csi-agent / 外部 socket 路径
```

COCO 候选条件为内部 `directAssigned=true, socket为空, skipGlobalMount=false`。实验 PV 使用 `runtimeclass: rund`，Pod 使用 `runtimeClassName: rund`，DS 使用 `RundCSIProtocol3=false`。没有新增全局 sidecar feature gate，也没有把其他旧分支改成这套协议。缺少手动 sidecar 时明确 FailedPrecondition，不默认猜测 virtiofs 回退。

公共 NodePublish 包装层原本就 GET Pod；runtime 分类复用 context，包括失败结果，不 GET RuntimeClass/PV/Node。可信绑定验证会在冷缓存时增加每个 Pod/claim 一次 PVC GET，成功结果进入 4096 项 LRU；重复发布不再 GET PVC，重启/淘汰后重新验证。没有全集群 informer/list/watch。1/10/100 卷分别测量冷/热请求计数；不能再宣称所有新逻辑零 API 成本。探针、卷 worker 和控制 GC 不调用 Kubernetes API。

## 数据面与控制面

HostPath 只存控制文件：host 的 `/var/run/ossfs/file-volumes/<PodUID>` 通过 `subPathExpr: $(POD_UID)` 挂为 sidecar 的 `/volume-state`。UID 从 Downward API 注入环境变量。sidecar 无需 Kubernetes token、Pod GET 权限或 CSI socket。

数据仍使用 Pod 声明的 PVC：sidecar 在 `/pod-volumes/<PV-name>/mount` 挂载，Bidirectional；业务容器使用相同 volume，HostToContainer。guest 的 FUSE 覆盖在底层 PVC/virtiofs 投影上，只卸载自有 FUSE 层，不卸载底层投影、不删除目录内容。

固定命令为 `csi-mount-proxy-server --file-mode`，不配置 driver 和 expected-volumes。所有已链接的 file-capable driver 自动注册，请求的 fstype 选择具体实现。增加卷只增加 volumes/volumeMounts，不改 sidecar args/probe。inventory 从已有 Pod 挂载声明生成，等待全部卷，不以“目前已经到达的文件”作为完整集合。

mountPath 中的 PV 名必须对应该 PVC 的真实绑定。当前逐目标验证标准 PVC.spec.volumeName，真实交换两个不同 PVC 的错误会在发布任何可执行配置前被拒绝；重复 PVC 目标也拒绝。mountproxy 必须位于消费这些 PVC 的其他 init container 之前，防止 init 在 FUSE 挂载前读取空目录或写入本地回退数据。不使用这些卷的配置准备 init 可以在它之前。

## 文件协议

每个 UID 下的布局：

```text
manifest.json                  # 完整 volumeKeys（PV 目标目录名）
desired/<sha256(key)>.json      # MountInfo + 完整 MountProxy.MountRequest
session.json                   # 当前消费者实例
ready-session.json             # 首次放行业务后的重启屏障
status/<sha256(key)>.json       # 挂载结果、生命周期、卸载结果、心跳
unmount/<sha256(key)>.json      # 显式撤销，绑定 generation/operationID
.runner.lock                   # 同一 UID 只允许一个消费者
```

MountInfo 复用已有公共结构；FUSE 参数保留在 MountProxy 中，包括 MountFlags、Options、Secrets 和真实 VolumeID。key 与 volumeHandle 分开。此模式不生成 `vol_attr.json`，避免既有 runtime 消费者再次挂载。

MountRequest JSON 保留未知扩展字段；新增必须支持的语义需通过 RequiredFeatures 协商，旧消费者明确拒绝。保留未知字段不等于旧 driver 已具备新能力。overlay、fd-passing、recovery、host metrics path 目前明确拒绝。

generation 是请求内容摘要。同一 UID/key 的重复交付幂等，参数变化要求重建 Pod。desired 与撤销指令在合法生命周期内不靠修改旧内容更新。写入采用临时文件、fsync、rename，文件 0600、UID 目录 0700。RRSA 不将 JWT 或 STS AK 写入 desired；Secret 鉴权路径仍可能含凭据，不能将完整 JSON 作为诊断日志输出。

**按运行时分别验收一致性：**自建 runv 2.2.4 中，host 替换/删除已有文件会出现 guest 旧内容，长 token 无法修复。后续实际 rund 的无 CSI/FUSE 对照中，替换/删除与 ConfigMap/projected 更新均可见，没有复现旧版本问题，见根因记录第 8 节。原生文件通过仍不能替代本实现数据面与 RRSA 轮换验收；CSI 不重做 runtime 文件同步。

## 启动、失败与探针

每卷一个 worker，独立挂载和监督 daemon。挂载确认检查真正的 FUSE 层，底层 virtiofs 存在不算成功。状态保留原 mountproxy Response 及脱敏错误；异步状态额外表达进程退出、请求丢失和清理结果。

`--file-check-ready` 检查完整 inventory、UID、generation、当前 session、成功的 mountproxy Response、Mounted 与新鲜心跳；无 API 或远端数据 I/O。首次成功时写 ready-session 屏障。startupProbe 以此阻止业务提前启动，readinessProbe 持续反映故障；它不证明远端持续可读写或手动映射一定正确。无 livenessProbe，避免用无限重启掩盖坏连接。

| 事件 | 预期表现 |
|---|---|
| 请求未全到达 | 对应卷 Pending，startupProbe 失败，业务等待 |
| 一卷权限/挂载失败 | 该卷 Failed + 原因，其他卷继续，业务不启动 |
| 运行后单卷 daemon 退出 | Failed / Pod NotReady；不落到本地目录继续写 |
| desired 消失且消费者看见 | RequestMissing / NotReady，保留已有挂载；缺失不是撤销 |
| 相同 generation 恢复 | 可恢复 Mounted；不同内容要求重建 Pod |
| 放行业务后 sidecar 重启 | 禁止透明重挂，PodRecreationRequired；重建整个 Pod |
| 运行时更早阻止容器重建 | StartError / Socket not connected；也是故障，不算屏障逻辑已运行 |

不能在 NodePublish 内等待 guest 挂载结果：kubelet 要等 NodePublish 完成才启动 sidecar，会形成死锁。因此 CSI 返回成功代表交付，真正挂载失败通过 probe、状态及 Pod 事件观察。

## 终止与清理责任

不配置 postStart/preStop shell。mountproxy 是主进程，接收 SIGTERM/SIGINT。正常删除/有限任务完成时，原生 sidecar 在业务停止后收到终止信号。Pod grace 90 秒，startupProbe 的容器终止预算 30 秒；业务可能耗尽总预算，不能保证 sidecar 总有时间清理。

优雅退出：取消挂载 → 各 worker 独立 Draining → 正常 umount 自有 FUSE 层 → 写 Unmounted 或 CleanupPending → driver 终止进程。file-mode 为 drain 与 driver 终止共用 20 秒退出上限；超时返回错误，保留未确认结果，交由 runtime 终止。上限不是“20 秒内保证清理完成”。

活消费者收到显式 UnmountRequest 时校验 UID/handle/target/generation/operationID；EBUSY 保持 CleanupPending 并重试，不 MNT_DETACH 后伪报成功。这是文件消费者的操作/观测语义。

标准 NodeUnpublish 则由 kubelet 在终止 Pod 容器、调用 CRI 停止 sandbox 后触发，不能等待已经停止的 sidecar 回复。节点记录 ACK 是否存在或控制文件是否损坏，随后安全清理 CO 指定的 host target；成功后才退休对应控制文件。CO 请求是清理范围的依据，不沿可变 JSON 中的 target/PodUID 删除数据。它既不把 Pod NotFound 当作完成，也不为 guest VM 增设私有销毁证明。

| 责任 | 当前实现/证据 |
|---|---|
| guest 优雅卸载 | mountproxy；双卷正常删除、部分失败后删除、有限任务完成已验证 |
| busy 卷重试 | 独立 worker；真实持有 cwd 的撤销测试通过 |
| SIGKILL 后停止进程与回收 VM | kubelet 调用标准 CRI，runtime 负责；CSI 不管理 guest 进程/VM |
| 无卸载 ACK 的 NodeUnpublish | 继续标准 host 卸载；真实 SIGKILL + 强删 + CSI 重启后双卷成功、旧 UID 状态删除 |
| UID 控制目录 GC | 已实现每卷退休、并发幂等；启动/每 5 分钟回收 kubelet 本地 Pod 目录已删除的遗留状态 |

SID 被杀且 Pod 尚未停止时，保持故障可见，不能用透明重挂恢复旧业务 bind。整个 Pod 终止时复用标准 kubelet/CRI 生命周期，不新增 RunD 进程扫描、CRI 补偿或自建 agent。若运行时报告停止成功后仍保留存活的 FUSE 进程，是运行时契约问题，不能在 CSI 中猜测修复。标准源码与真实无 ACK 测试见根因记录。

数据路径禁止递归删除；host target 非空时仍失败，不能为清理成功忽略 ENOTEMPTY。控制退休使用 os.Root 限定在 UID 下，只删除已知文件和空目录，不沿 desired.Target、kubelet 数据目录或不明路径执行 RemoveAll；未知内容保留。孤儿控制 GC 依据 kubelet 已移除本地 Pod 目录，不依据 API NotFound/心跳。根目录缺失、检查失败或符号链接越界时保留。该 GC 回收元数据，不代替 runtime 回收 guest 挂载。

## host 清理与 emptyDir

自建版本中，host 看不到 guest 的远端哨兵。限定在测试专用子目录内执行 host 递归删除后，guest 与独立 OSS API 均确认远端哨兵仍在；这支持该版本的 guest 隔离，但不覆盖所有 runtime、残余 namespace 与节点故障。

emptyDir 本身不提供远端数据防删保障。若 host 视图能进入活 FUSE，kubelet 或其他递归清理仍可能触及远端。它也不会让 SIGKILL 执行 preStop，或替代 guest 消亡证据。当前 emptyDir 对照只验证诊断导入配置后的数据面，未替换为 CSI 完整链路。

## RRSA 与测试窗口

业务 Pod 的 ServiceAccount 通过 projected volume 获得 audience=sts.aliyuncs.com 的 OIDC token；guest 内 ossfs2 调用 STS 并消费凭据。host kubelet负责投射 token 文件，鉴权与挂载发生在 guest。CSI 不使用节点身份换证。

目前 guest 读不到 host 已轮换的投射内容。按本轮约定使用实际签发 86400 秒的 token 继续其他用例；不修改 STS 权限，不引入 token mirror。**RRSA 自动轮换未通过，24 小时窗口不是生产解决方案。**

## 与 ACK Fluid internal 的差异

Fluid internal 的 RunD 注入将数据 PVC 改成共享 emptyDir；具体镜像/脚本承担优雅清理，有些路径使用 preStop、trap、auto_unmount。详细源码证据见测试计划第 9 节。没有额外 guest GC 本身并非缺陷：进程停止和 sandbox 回收本应属于 kubelet/runtime。

本实现保留 CSI 发布链路，记录 generation/session、异步挂载与清理结果；Native sidecar 提供启动门槛和正常终止顺序。不把交付或 lazy detach 当成挂载成功，但也不要求已停止的 guest 再 ACK。两者都依赖标准运行时生命周期及隔离契约；emptyDir 不是额外数据安全保证。

## 代码 review 顺序

1. `pkg/oss/nodeserver.go`、`pkg/utils/util.go`：最小分流、缓存失败、原路径与 API 次数。
2. `pkg/oss/file_publish.go`、`file_bindings.go`、`filevolume/manifest.go`：消费者契约、RRSA、完整卷集合、标准绑定校验与明确的冷/热查询成本。
3. `filevolume/protocol.go`、`publication.go`、`unmount.go`：请求复用、不可变性、显式撤销及 ACK 条件。
4. `filevolume/runner.go`、`readiness.go`、`refresh.go`：多卷状态、进程监督、重启屏障、一致性前提。
5. `cmd/mount-proxy-server` 与 `proxy/server/file_*`：默认模式、driver 注册、退出上限、仅卸载 FUSE。
6. 三种 FUSE driver 的 MountFile 和 interceptor：复用已有执行逻辑、Secrets 生命周期、MountReady 与 ExitChan。
7. `filevolume/retire.go`、`pkg/oss/file_gc.go`、`main.go`：控制文件退休、孤儿 GC、进程生命周期；重点检查无递归数据删除、并发/损坏状态及活跃 UID 保留。
8. 对应测试及根因/验证记录：区分我们已修复的实现问题与已独立重现的运行时文件接口限制。

没有 webhook、RBAC 扩权或依赖版本变更。绑定、NodeUnpublish 和 GC 不再作为“外部问题”；当前保留标准文件 API、投射卷和 CRI 生命周期。实际 rund 的原生文件更新已验证，完整 FUSE 链路及真实 RRSA 轮转仍需验收。未扩大到 webhook/runc，也不将单节点测试等同生产规模验收。
