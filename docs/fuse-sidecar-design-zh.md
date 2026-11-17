# FUSE Sidecar 挂载模式设计

状态：待评审，本文描述目标设计，不代表当前 PR 已实现。

本文对应 [PR #12](https://github.com/AlbeeSo/alibaba-cloud-csi-driver/pull/12) 的下一轮调整。调研基线为 upstream/master `3cc89c86b`，现有实验实现为 `002fbbb92`。实验实现仍使用 `RundCSIProtocol3` 选择 ACK 沙箱文件路径；本设计拟解除该绑定。本文只新增设计说明，不修改运行代码，也不执行 E2E。

## 1. 目标与边界

新增一个由集群 CSI 管理的 FUSE 挂载模式：业务 Pod 中的 mountproxy sidecar 执行挂载。runc、rund 均可使用，但不接管原来的 guest csi-agent 与 host 委托场景。

设计区分三个维度：

| 维度 | 取值 | 谁负责选择 |
| --- | --- | --- |
| 挂载模式 | Legacy / Sidecar | 新的全局默认与 PV 显式配置 |
| sidecar 提供方式 | 用户手动配置 / webhook 注入 | 独立的 webhook 配置和 Pod opt-out |
| 控制请求传输 | rund 用 HostPath 文件；runc 目标使用 RPC/gRPC | 内部按运行环境选择，不增加用户必须配置的 transport 开关 |

非目标：改变 Legacy 默认路径；默认切换到 virtiofs；接管原 csi-agent；透明恢复业务已有 FUSE 连接；在第一阶段实现 runc sidecar RPC。

## 2. 默认行为基线

### 2.1 不能把 rund 的默认行为写成 virtiofs

当前 OSS 的 `DetermineRuntimeType` 根据 `directAssigned`、是否有 mountproxy socket、`skipGlobalMount` 三项判断，**不是根据 Pod RuntimeClass 自动选择 virtiofs**。

| directAssigned | 有 socket | skipGlobalMount | 现有判断 |
| --- | --- | --- | --- |
| false | false | false | 报错：非 agent 模式没有 socket |
| false | false | true | MicroVM / cmd-mounter 分支，不是 virtiofs fallback |
| false | true | false | RunC 路径，使用 attachPath 再 bind 到业务目标 |
| false | true | true | RunD 路径，直接挂载目标 |
| true | true | false | 报错：rund cannot be used in non csi-agent mode |
| true | true | true | RunD 路径，直接挂载目标 |
| true | false | false | COCO 分支，不是 virtiofs fallback |
| true | false | true | MicroVM 分支，directAssigned 不改变该结果 |

正常构造时，DS nodeServer 的 `skipGlobalMount` 默认 false，csi-agent 默认 true；参数还可能受既有部署配置影响。因此，现有直接 RunD 挂载通常依赖 agent 的执行配置，不能宣称所有名为 rund 的 Pod 都有一种自动 fallback。

原 OSS `RundCSIProtocol3` 分支还会在 host 插件识别到旧 `rund` 路径时直接 skip，将执行交给外部机制。skip 成功本身不能证明后续发生了 virtiofs 共享或成功挂载。customfuse 原默认代码则通过 socket 和 `skipGlobalMount` 选择 attach 或直接挂载，没有完全相同的旧 gate 分支。

源码入口：[运行时判断](../pkg/oss/utils.go)、[DS 构造](../pkg/oss/oss.go)、[agent 构造](../pkg/oss/csi_agent.go)、[OSS NodePublish](../pkg/oss/nodeserver.go)、[customfuse NodePublish](../pkg/customfuse/nodeserver.go)。旧 gate 行为应对照上述 upstream 基线，而不是已改动的实验分支。

### 2.2 本设计如何对待默认行为

`Legacy` 的含义是“调用既有流程”，不是“保证成功”：

- runc 的常规部署继续使用独立 FUSE Pod。
- rund 继续既有判断；原来需要 agent 就仍需要，原来报错就仍可报错。
- 如果某种现有部署实际由 host 挂载再经 virtiofs 共享，本设计保留它，但不新增该 fallback。
- 第一阶段先记录新环境中不启用 Sidecar、不配 sidecar 的实测结果，作为后续比较基线。

virtiofs 描述 host/guest 的文件共享方式，独立 FUSE Pod / 业务 sidecar 描述 FUSE 进程的执行位置，两者不应混成同一个枚举。

## 3. 配置模型

### 3.1 挂载模式配置

以下名称暂定，待评审后确定：

```yaml
# 新 gate，默认 false；不复用 RundCSIProtocol3
FuseSidecar: false

# PV.spec.csi.volumeAttributes，可从 StorageClass 传入
fuse.csi.alibabacloud.com/mount-mode: sidecar  # sidecar / legacy / 未配置
```

这里的新 gate 表示“全局默认选择 Sidecar”，不是“gate 关闭就禁止 PV 单独启用”。PV 显式配置优先，以满足“gate 或 PV 配置均可开启”，并允许全局开启后的逐 PV 回退。

| 新 gate | PV 模式 | 最终模式 |
| --- | --- | --- |
| false | 未配置 | Legacy |
| false | sidecar | Sidecar |
| false | legacy | Legacy |
| true | 未配置 | Sidecar |
| true | sidecar | Sidecar |
| true | legacy | Legacy |
| 任意 | 非法值 | InvalidArgument，不能静默回退 |

全局默认仅适用于集群插件支持的常规 FUSE 数据卷；不自动改造 rootfs 等特殊 publish 路径。PV 同时显式要求 Sidecar 和冲突的旧执行语义时，应明确报配置冲突，不能靠分支先后猜测。

### 3.2 webhook 配置

`webhook.enabled` 是第二类配置，只决定是否帮助用户提供 sidecar，不决定挂载模式。

Pod 的 `inject: "false"` 是提供方式的 opt-out，不是把挂载模式改回 Legacy：全局安装 webhook 的情况下，用户仍可手动提供 sidecar。

| 模式 | webhook | sidecar 提供与预期效果 |
| --- | --- | --- |
| Legacy | 关闭 | 完整执行旧流程 |
| Legacy | 开启 | 不为该卷注入 sidecar，完整执行旧流程 |
| Sidecar | 关闭 | 用户手动提供；DS 验证存在符合契约的消费方，缺失则明确报错 |
| Sidecar | 开启 | webhook 注入；若显式 opt-out，则验证用户手动提供的消费方 |

**选择 Sidecar 与提供消费方是两个条件，不能只满足第一个就报告实际挂载成功。** 不开启 webhook 是受支持的，不提供任何消费方不是一种受支持的 Sidecar 配置。

### 3.3 所有组件必须使用一致的模式选择

ControllerPublish 没有 PodInfo，webhook admission 时 Pod 通常尚未调度，DS 却知道实际 Node。因此不能分别让三处代码自由猜测模式。

拟采用统一的 `ResolveMode(PV, defaultPolicy)`，并让 controller、DS、webhook 使用同源默认策略。ControllerPublish 将解析后的模式及策略版本作为执行计划交给 NodePublish，DS 校验一致性。策略不一致时报告 `ModePolicyMismatch`，不能一边创建独立 FUSE Pod、一边等业务 sidecar。

如果严格要求“仅修改某个 DS 的 gate 就自动生效”，还需要权威的节点策略发布机制。controller 可以按 Node 读取，但尚未调度的 Pod 在 admission 时不能确定落在哪个 DS 策略上。**异构节点默认值、DS-only 开关与自动 webhook 注入的组合，尚不能直接宣称支持。**

评审建议：全局默认由一处安装配置同步给相关组件；逐卷差异通过 PV 配置表达，不增加第三类用户开关。第一阶段优先用显式 PV 配置验证路径。如果必须支持 DS-only 的异构默认，需先补齐调度/admission 的设计。

## 4. 执行角色与优先级

`clientset != nil`、RuntimeClass 字面名称、节点是否为裸金属都不能单独证明执行角色。

角色优先于新的全局默认：

1. csi-agent 执行入口继续原有行为；不继承集群插件的新 Sidecar 默认。
2. 明确属于旧 host→guest agent 委托的请求保留委托。
3. PV 在这些旧角色上显式要求新 Sidecar 时，报 `SidecarRoleConflict`，避免两个执行方争抢同一卷。
4. 只有集群插件拥有执行权的请求，才进入新的 Legacy / Sidecar 选择。

旧 `RundCSIProtocol3` 恢复旧语义，不负责选择新 Sidecar。旧委托的识别应依赖已确认的部署/执行契约，不能将所有 rund Pod 都归为旧委托，否则新 rund sidecar 永远进不去。

## 5. 目标伪代码

目标是把策略判断集中在计划解析器中。具体 CSI handler 和 driver 不应再各自堆积 gate、RuntimeClass、socket、webhook 的判断。

### 5.1 模式及执行计划

```text
ResolveMode(pv, policy):
    choice = ParsePVMode(pv)                  # 非法值直接报错
    if choice explicitly set:
        return choice
    return Sidecar if policy.sidecarDefault else Legacy

ResolveNodePlan(request, role, policy, supportedStage):
    choice = ParsePVMode(request.volumeContext)

    if role is Agent or ConfirmedLegacyDelegate:
        if choice == Sidecar:
            return error(SidecarRoleConflict)
        return ExistingExecutionPlan(role)    # 不让全局默认接管 agent

    mode = ResolveMode(request.volume, policy)
    if mode == Legacy:
        return LegacyPlan                    # 不新增 virtiofs fallback

    pod = GetAndVerifyPod(request.PodInfo)    # API/UID/调度节点不符就报错
    runtime = ResolveActualRuntime(pod)      # 失败不能默认为 runc
    ValidateSidecarCompatibility(request)    # 特殊旧路径、能力冲突在这里拒绝

    if runtime == Rund:
        transport = HostPathFile
    else if runtime == Runc and supportedStage >= 3:
        transport = RPC
    else:
        return error(UnsupportedSidecarRuntime)

    consumer = ValidateConsumerContract(pod, request.volume, transport)
    if consumer missing:
        return error(MissingSidecarConsumer)
    if consumer invalid or ambiguous:
        return error(InvalidSidecarConsumer)

    return SidecarPlan(mode, runtime, transport, consumer, policy.revision)
```

`ValidateConsumerContract` 校验约定的 mountproxy 容器、模式、UID 注入、配置与目标卷挂载、探针和唯一所有者，不只检查容器名称。它验证的是 PodSpec 中有合理消费方，不是在 NodePublish 中等待尚未启动的进程上线。

管理员可以在 admission 时提前发现问题，但 webhook 关闭时，DS 仍必须做这层验证。静态检查不能保证镜像一定含有正确客户端或进程一定成功；实际执行错误仍由结果和探针报告。

### 5.2 CSI handlers

```text
ControllerPublish(volume, node):
    plan = ResolveControllerPlan(volume, sharedPolicy(node))
    if plan.mode == Legacy:
        return ExistingControllerPublish(volume, node)
    ValidateControllerSideCompatibility(plan)
    PersistAttachmentPlan(plan)               # 非仅内存，供重试/解绑使用
    return PublishContext(plan)              # 不创建独立 FUSE Pod

NodePublish(request):
    plan = ResolveNodePlan(request, executionRole, sharedPolicy, stage)
    ValidateControllerNodePlanAgreement(plan, request.PublishContext)
    if plan is ExistingExecutionPlan or LegacyPlan:
        return ExistingNodePublish(request)

    manifest = ResolveAllSelectedSidecarVolumes(request.Pod, plan.consumer)
    # 清单只包含选择 Sidecar 且属于该消费方的卷，不包含 Legacy 卷
    PersistPublicationPlan(request.identity, plan)
    transport = TransportFactory(plan.transport)
    transport.PublishManifest(manifest)
    transport.PublishMountRequest(BuildMountRequest(request))
    return Delivered                         # 不能等这个 Pod 的 sidecar 启动

NodeUnpublish(request):
    plan = LoadRecordedPublicationPlan(request.identity)
    # 按发布时的实际所有者清理，不能根据当前 gate 重新猜
    if plan belongs to Legacy:
        return ExistingNodeUnpublish(request)
    return TransportFactory(plan.transport).Cancel(request.identity)

ControllerUnpublish(volume, node):
    plan = LoadRecordedAttachmentPlan(volume, node)
    if plan belongs to Legacy:
        return ExistingControllerUnpublish(volume, node)
    return CleanupSidecarAttachmentMetadata(plan)
```

首次启用和旧状态迁移时，可能没有新执行记录；需要明确的旧路径识别规则。无法确认所有权时应报告诊断错误，不应同时尝试两种清理。执行记录的具体持久化位置需在第一阶段实现前选定，不能仅保存在 DS/controller 的进程内。

### 5.3 webhook

```text
OnPodCreate(pod):
    if webhook disabled:
        admission is not called

    selected = ResolveSidecarVolumes(pod.PVCs, sharedDefaultPolicy)
    if selected is empty:
        return pod unchanged

    if pod opts out of injection:
        ValidateManualConsumers(pod, selected)
        return pod

    ValidateBindingsProfilesAndRuntimeSupport(pod, selected, stage)
    return InjectOrValidateExistingConsumers(pod, selected)
```

注入失败时 admission 拒绝并说明原因。未注入、不允许注入的 Pod 仍会在 NodePublish 校验；DS 不把“安装了 webhook”等同于“该 Pod 一定被正确注入”。

## 6. 每种分支的用户可见表现

| 条件 | 目标表现 |
| --- | --- |
| Legacy + runc | 原有独立 FUSE Pod 与原 RPC 结果，不创建业务 sidecar 请求 |
| Legacy + rund | 原有委托、挂载或错误；记录实测，不承诺 virtiofs |
| Sidecar + 手动合法消费方 | 不创建独立 FUSE Pod；发布请求，启动探针控制业务启动 |
| Sidecar + 无消费方 | NodePublish `FailedPrecondition`，明确指出卷、Pod 和缺失契约；不写成“挂载成功” |
| Sidecar + 容器存在但 state/UID/PVC 映射错误 | `InvalidSidecarConsumer`，指出缺失/冲突项，不静默回退 |
| Sidecar + 两个消费方声称管理同一卷 | 拒绝，不能让两个 mountproxy 同时执行 |
| Sidecar + rund + HostPath 无法共享 | 共享条件可预检时直接报错；否则缺清单/heartbeat，startupProbe 不通过；不能因请求在 host 写成功就认为 guest 可见 |
| Sidecar + 挂载请求失败 | 保存/回传原 mountproxy 错误，startupProbe 不通过，业务不启动 |
| Sidecar + 挂载成功后 daemon 退出 | readiness 失败，保留最初成功结果并记录生命周期错误；不承诺自动重启业务 |
| Sidecar + 第一/二阶段的 runc | 明确 Unimplemented，不假装回退成另一种 Sidecar 传输 |
| 非法 PV 配置或组件策略不一致 | 明确配置错误，不随机选择一个分支 |
| 未启用 Sidecar 却手工放入等待文件的 mountproxy | 不会据此隐式开启 Sidecar；旧 CSI 流程照常，手工 sidecar 会因无清单无法启动就绪，属于用户配置冲突 |

建议的缺消费方错误信息：

```text
sidecar mode selected for volume <volume>, but Pod <namespace>/<name>
has no matching mountproxy consumer; enable injection or configure a manual sidecar
```

不要把所有错误合并成“mount failed”或“runtime unsupported”。诊断至少区分：模式选择错误、角色冲突、消费方缺失、共享不可见、请求执行失败、运行期进程退出、卸载失败。日志和 Events 不应包含凭据。

## 7. rund HostPath 生命周期

这里有两种不同的卷：配置状态目录通过 HostPath 共享，业务数据卷则必须让 sidecar 与业务引用同一个 PVC。不能把额外 HostPath 的 kubelet 目标目录别名当作等价业务卷，先前 RunD 实测已证明传播可能不通。

### 7.1 目录、身份与所有者

沿用默认 `/volume-state`、`/pod-volumes` 和固定启动/探针命令。CSI 创建 Pod UID 隔离的源目录，消费者通过 Downward API + subPathExpr 挂入。目录里包含完整 manifest、desired、status 和 session。

- CSI 拥有 manifest/desired 的发布、取消与身份验证；sidecar 拥有挂载执行、状态与 heartbeat。
- 同一目录只能有一个 runner 所有者；多个 driver/consumer 的目录必须隔离，不能共享一个 session 文件互相覆盖。
- 每次 runner 启动生成新 session，状态必须匹配 UID、请求 generation、session 和新鲜 heartbeat。
- manifest 来自 Pod/PVC/PV 的完整选中集合，不能通过扫描已经到达的请求推导。
- 保存请求包含 Secret，保留现有目录/文件权限与脱敏要求。

### 7.2 创建、运行与重启

```text
验证消费方与身份
→ 创建 UID 源目录、原子发布完整 manifest 和卷请求
→ NodePublish 返回已交付
→ kubelet 挂入 HostPath/PVC，启动 sidecar
→ sidecar 校验 session/请求并执行挂载
→ 所有预期卷成功后 startupProbe 放行业务
```

在新环境验证源目录创建与 kubelet subPath 的实际顺序、host/guest 原子 rename 的可见性及状态回写。不能只确认 host 文件存在。

Pod 存活期间，**不 RemoveAll 其配置源目录再重新创建**：已有 bind/subPath 可能继续引用旧目录 inode。sidecar 重启也不能通过重建源目录“修复”；应保留目录、更新 session、处理旧 FUSE 层，并对运行中业务连接采取明确恢复策略。当前默认恢复策略仍是必要时重建 Pod，不承诺无损 remount。

### 7.3 取消与优雅删除

业务退出后，kubelet 终止原生 sidecar；mountproxy 先卸载每个 FUSE 层、回写结果，再停止和回收客户端。NodeUnpublish 取消/删除对应 desired 与描述文件，消费者观察到取消也会卸载，不依赖它与 SIGTERM 的固定先后顺序。

不能在 guest 仍要回写状态时先删掉整棵 HostPath 目录。卸载遇 EBUSY 的 lazy detach 只说明挂载从相应命名空间分离，不等于所有 FD 立即释放或远端写入已经持久化。

### 7.4 异常退出、孤儿与清理

SIGKILL、OOM、节点断电、guest 崩溃或宽限期耗尽，均可能来不及卸载和回写。缺失完成状态不能伪造为 Unmounted，也不能单靠 host mountinfo 证明 guest 清理。

GC 至少要求 Pod UID 已不再存活，并处理可能仍在运行的 sandbox/consumer，再决定回收状态目录。无法确认时保留“清理结果未知”的诊断，不按 Pod 名重用旧 UID 目录。状态保留期限和孤儿 GC 的具体实现需另行明确；它们不应阻塞前面的优雅卸载契约。

`/var/run` 在某些 host 上是易失存储。原子写与 fsync 不等于节点重启后仍保留请求；重启后的重建、重发和旧 guest 是否仍存活必须实测，不能承诺依靠该目录跨节点故障恢复。

## 8. runc RPC 的生命周期约束

第三阶段的目标是独立的 RPC/gRPC 通道，能够自然接收结构化执行结果，不要求与 rund 共用文件轮询实现。当前 mountproxy 原协议实际是 JSON over Unix socket；不能把目标 gRPC 通道写成已经实现。

但是换成 RPC 不会消除启动依赖：

```text
NodePublish 等业务 sidecar 的 Mount RPC 完成
    ↕ 相互等待
业务 sidecar 等本 Pod 的 NodePublish 完成后才能启动
```

独立 FUSE Pod 没有这个依赖，因为它能提前启动。业务 sidecar 不能直接照搬同一同步调用时序。

建议第三阶段评审异步模型：DS 先保存请求并返回交付结果，sidecar 启动后与 DS 建立 RPC 会话，取得 manifest/请求、回传执行结果及心跳。请求按 Pod UID、卷、generation 和 session 匹配；重连、重复交付、取消和 DS 重启均需定义。

UID 用于关联请求，不是调用方的身份凭据。RPC 仍需认证并授权到相应 Pod/节点，不能只凭客户端自报 UID 返回包含 Secret 的请求；具体机制属于第三阶段设计。

RPC 可以直接传回客户端失败原因，免去 status 文件读取，但**已经返回成功的初次 NodePublish 不能被事后改成失败**。业务首次启动仍由探针控制。若要求初次 CSI RPC 同步返回实际挂载结果，就需要先解决不同的启动编排模型，不能把它隐含在“runc 支持 gRPC”中。

第三阶段是否接受异步 CSI 交付语义、RPC 谁发起连接、请求如何持久化，以及是否需要把结果转成 Kubernetes Events/诊断接口，均应在实施前确认。它们不阻塞第一阶段 rund HostPath 验证。

## 9. 分阶段实施与验证

### 阶段一：用户手动 sidecar、rund、HostPath

不部署 webhook，不实现 runc sidecar RPC。先集中验证一条新执行路径和默认分支。

| 用例 | 预期 |
| --- | --- |
| 未选 Sidecar、未配 sidecar | 与旧环境基线一致，记录是原有成功路径还是原有错误；不新增 virtiofs fallback |
| 选择 Sidecar、未配 sidecar | 明确 MissingSidecarConsumer；不创建新的挂载消费方，不静默成功 |
| 选择 Sidecar、手动配置正确 | 完整 manifest、请求、响应均经 HostPath，业务真实 I/O 通过 |
| 手动配置目录/UID/目标错误 | 能识别的配置错误提前拒绝，共享不可见时探针明确失败 |
| 多卷，其中一个缺失/失败 | 其他卷结果仍可观察，业务不启动；清单不因只收到部分请求而缩水 |
| sidecar 重启、DS 重启 | 不重建 live 源目录，旧 session 不误放行，所有权可恢复或明确报错 |
| Job、优雅删除、请求取消 | 明确卸载、状态回写、进程回收及目录保留顺序 |
| 强杀、guest/节点故障 | 记录清理未知与孤儿处理边界，不伪造成功；优先确认 HostPath 挂载残留和源目录引用 |

### 阶段二：增加可选 webhook

验证第 3.2 节完整四格矩阵：Legacy/Sidecar × webhook 关闭/开启。另测 opt-out + 合法手动 sidecar、opt-out + 无消费方、webhook 不可用、无匹配 profile、重复注入和错误绑定。

webhook 不可用时：需要自动注入的请求拒绝；符合 admission bypass 的手动配置仍应可运行。不开启自动注入的合法用法不能依赖 webhook 在线。

混合卷必须单独覆盖：同 Pod 一个 Legacy 卷、一个 Sidecar 卷；manifest 和注入只管理后者，不能双重挂载，也不能等待 Legacy 卷的 sidecar 状态。

### 阶段三：runc sidecar RPC

保持相同的模式与 webhook 矩阵，将内部 transport 切到 RPC。验证实际挂载失败结果、启动顺序无死锁、会话与身份隔离、取消、重连、DS/sidecar 重启和结果持久化。

runc Legacy 仍使用原独立 FUSE Pod。rund HostPath 不因新增 RPC 被迁移。旧 csi-agent 路径每阶段均做回归隔离。

每次实现只跑直接相关的简单代码测试；完整 E2E 等新的环境按上述阶段执行，不提前创建集群。

## 10. 评审需确认的决策

1. **缺消费方策略**：建议显式 Sidecar + 无消费方直接报配置错误；未选择 Sidecar 才执行默认流程，不将“默认”和“Sidecar 失败后的回退”混为一谈。
2. **默认策略来源**：建议相关组件同源配置；若必须只改 DS gate，需先设计异构节点策略与 admission 的协调。
3. **持久化执行计划**：确定 controller/node 的记录位置与旧状态迁移，保证解绑按原所有者执行。
4. **runc RPC 结果契约**：确认接受异步交付加独立结果，还是要求改变启动编排以实现初次 NodePublish 同步挂载。
5. **旧基线**：先验证新 rund 环境未启用 Sidecar 时的实际行为；默认报错是可接受基线，virtiofs 不是本次新增默认。

实验实现的历史环境与验证证据见 [既有调研与 Review 说明](rund3-fuse-design-review-zh.md)、[验证记录](rund3-fuse-validation.md)。这些记录不能替代本设计三个阶段的验收。
