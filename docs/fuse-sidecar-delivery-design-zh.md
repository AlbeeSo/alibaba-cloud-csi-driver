# RunD 与 RunC 业务 sidecar 文件交付方案

更新：2026-11-19。最终选择是业务 sidecar 统一使用 file，现有独立 FUSE Pod 保留 socket RPC。不提供让用户选择 sidecar file/socket 的开关。本文记录分流原则和后续设计边界，不代表两种运行时均已支持。RunD 现有实现继续在当前 PR review；RunC file sidecar 后续使用独立分支/MR，不把两套入口合并重构。

最终能力边界：**RunD sidecar 不支持 fd-passing 和 recovery**，保持 guest 内直接挂载、RRSA 和当前 privileged 要求，不规划借 HostPath 传 host fd。**RunC sidecar 后续单独分支/MR 开发**，复用已有 fd-passing 和对应 driver 的 daemon recovery，实现非特权 sidecar；配置与状态走 file，fd 经同内核 Unix socket 交付。该 RunC 能力尚未实现，不影响当前 RunD PR，也不改变既有独立 FUSE Pod。

## 选择依据

两种业务 sidecar 都必须在 NodePublish 返回后才能启动。文件负责配置、完整卷集合和执行状态，不承担 fd 传递；若使用 fd-passing，CSI 可以先建立内核挂载再返回，sidecar 随后接管 fd。数据面真正可用仍须等用户态 daemon 初始化完成。使用 file 可复用已有 inventory、generation、worker、状态和 probe，不需要再开发一套以 socket 承载完整配置和结果的异步执行协议。

现有独立 FUSE Pod 的共享挂载、同步调用与业务 sidecar 的 Pod 独占挂载、异步执行本来就不同，不能因为都用 socket 而假装无需分叉。代码按消费者和挂载范围组织，避免把传输选择继续塞进 runtimeType 判断。统一的是 sidecar 交付和状态模型，不是隐藏 runc/rund 的目标准备、传播和异常回收差异。

权限要求优先于减少改动：不能为了使用 file 就一律要求业务 Pod 内的 mountproxy 以 privileged 运行。RunC 对 fd-capable driver 复用已有 fd-passing，目标是 CSI 负责内核挂载、非特权 sidecar 接管 fd；customfuse 直接挂载的权限独立说明，不要求支持 fd-passing/recovery。普通业务容器不需要 privileged。file + fd socket 是互补通道，不提供用户任选 file/socket 配置交付的开关。RunD 当前 privileged 要求仍是限制，不能借 RunC 同内核方案宣称已解决。

## 实现状态

| 项目 | 状态 |
|---|---|
| RunD 手动 native sidecar、file 请求、多卷 inventory、probe、回收 | 已实现；详见[现有设计](fuse-manual-sidecar-design-zh.md)和[验收记录](fuse-manual-sidecar-validation-zh.md) |
| guest file 仅 RRSA、固定 sidecar 名称 | 已实现，提交 a441f4830；Linux OSS/filevolume 测试、集测与 CI 通过；本次命名/鉴权更新未重跑云 E2E |
| RunC 业务 sidecar、file 异步交付 | 待实现，本文是设计约定；不开发第二套 sidecar socket 投递 |
| 新 SC 参数、controller 跳过建 FUSE Pod | 待实现；参数名称尚未确定 |
| 首位 sidecar 正常路径免额外配置、免 PVC GET | 已达成要求，尚未实现；当前代码缺少完整绑定元数据时仍可能 GET PVC |
| webhook | 后续可选能力，当前未实现 |
| RunC sidecar 非特权 fd-passing | 必须纳入开发设计；已有底层实现可复用，file 异步交接尚未适配 |
| RunD sidecar fd-passing/recovery | 明确不支持；请求必须报错，不静默降级或忽略 |
| RunC sidecar recovery | 复用已有 ossfs2 daemon recovery；file worker 和状态模型尚未接入，不宣称覆盖 mountproxy/Pod 重启 |
| customfuse sidecar | 必须纳入两个环境的交付；独立命名、file 直接挂载，不支持 fd-passing/recovery；命名和真实挂载 E2E 尚未落实 |

这里 RunC/RunD 是讨论两条执行路径的称呼，不要求集群存在名为 runc 的 RuntimeClass。Pod runtimeClassName 为 nil 只表示默认 handler，不能据此断言共享宿主机内核。

## 共同契约

一个 Pod 由一个 native sidecar 管理多个 FUSE 卷。默认 OSS sidecar 名称固定为 `alibabacloud-csi-mountproxy`；customfuse 使用独立的 `<vendor>-csi-customfuse-<name>` 命名，例如 `example-csi-customfuse-storage`，前后段非空并遵守 Kubernetes 容器名称约束。按保留名称或严格命名规则识别，不解析启动 command/args 或镜像名称；候选存在但结构不合法时明确报错，不当成“没有 sidecar”回退。默认名称不用于 customfuse，名称本身也不授权读取凭据或决定实际请求的 fstype。

本阶段一个 Pod 只允许一个挂载消费者候选。多个 customfuse 候选或默认与 customfuse 同时存在均明确报歧义，不按数组顺序随便选择。若以后支持多消费者，必须先设计逐卷归属，不能隐式放开。上文四种分流中的“有 sidecar”包括合法的 customfuse 命名候选。当前代码仍只识别默认精确名称；customfuse 命名规则是待实现契约，不宣称当前已生效。

结构包括 restartPolicy=Always 的 init container、所需 probe、UID 隔离和数据/控制挂载。推荐放 initContainers 首位，startupProbe 等全部期望卷成功才允许后续 init 和业务启动；不能只看“目前已收到的卷”就放行。增加卷修改 volumes/volumeMounts，不要求用户修改 expected-volumes 或启动参数。webhook 和用户手工配置遵守同一契约。

名称和结构校验用于识别与配置诊断，不证明容器可信。RunD guest file 仅支持 RRSA，使用业务 Pod projected token 在 sidecar 内换证，CSI 不交付 JWT、STS AK 或 Secret。RunC file 允许 RRSA；Secret 引用必须通过业务 Pod 显式声明并挂载的 Secret volume 交付，不因为保留名称而向用户容器发送 CSI 持有的 Secret。控制 JSON 不携带凭据内容；具体 Secret 路径引用、文件格式和刷新契约留待 RunC 实现设计，不能直接复用目前“guest 仅 RRSA”的校验后宣称已支持。

共同复用 mountproxy 的挂载请求字段、错误含义和 FUSE driver 执行能力，保持卷标识与路径语义，不用重新定义一套简化挂载参数。当前独立 FUSE Pod 的 mountproxy 是 Unix socket 上的自定义 JSON RPC，不是 gRPC；其协议和执行流程保持不变。

## RunD：保留当前 file 路径

当前入口继续保留，不依据本文改动历史分流：

```text
原 directAssigned=true / socket为空 / skipGlobalMount=false 候选分支
  Pod runtimeClassName=kata-remote → 既有 COCO
  其他 → 校验当前 guest file sidecar 契约
         发布 PodUID 隔离的 manifest/desired
         返回交付成功
```

当前代码仍要求显式 non-runc RuntimeClass；RundCSIProtocol3 的历史提前 skip 仍在，实验配置为 false。没有给旧 csi-agent、COCO 或 Serverless 重新定义行为。runv 仅作 RunD 家族测试变体，不能用它区分产品模式。

控制目录为宿主机 `/var/run/ossfs/file-volumes/<PodUID>`，通过 Downward API 的 POD_UID 和 subPathExpr 映射到 sidecar `/volume-state`。数据卷仍由 Pod 声明；挂载路径沿用 `/pod-volumes/<PV-name>/mount`，不会改成 PVC 名。sidecar 读请求、逐卷执行并写结果，probe 负责启动屏障。

不要求运行时放开宿主机 socket。公开调研未找到可确认适用于 rund 的 UDS 白名单/代理开关，不将社区 Kata 的 vsock 能力推导为 rund 已具备。RunD 本轮选用 file，不继续把网络/vsock 代理研究作为实现前提。

## RunC：controller 决定是否创建 FUSE Pod，node 选择消费者

新增一个 SC parameters key，经 CreateVolume 保留在 PV volumeAttributes，成为 ControllerPublish/NodePublish 都能读取的 VolumeContext。本文暂称 requireSidecar，仅为语义占位，不能直接作为当前可用参数使用。

开启表示：controller 不创建独立 FUSE Pod，node 必须找到合法业务 sidecar。未开启保持 controller 原有创建行为，node 仍允许从 Pod 识别业务 sidecar。

| key 开启 | Pod 中有合法 sidecar | ControllerPublish | NodePublish |
|---|---|---|---|
| 是 | 是 | 跳过创建独立 FUSE Pod | 使用 PodUID 隔离文件，异步交付 |
| 是 | 否 | 跳过创建独立 FUSE Pod | 明确 FailedMount，不回退 |
| 否 | 是 | 按原逻辑创建独立 FUSE Pod | 使用 PodUID 隔离文件，异步交付 |
| 否 | 否 | 按原逻辑创建独立 FUSE Pod | 使用独立 FUSE Pod 的 socket 与现有执行流程 |

保留名称存在但结构不合法时，无论 key 是否开启都报错。查 Pod 失败不能伪装成“没有 sidecar”。首期不通过 controller 查询业务 Pod、扫描消费者或新增 informer 来猜分流。

第三行的代价是独立 FUSE Pod 可能多余，且其创建失败仍会阻塞进入 NodePublish。这是接受自动发现分支后的已知行为；使用 sidecar 的新 SC 建议显式开启 key。node 不擅自删除多余 FUSE Pod，因为同节点其他 Pod 可能仍在使用它。

key 作用于整个卷；开启后该卷的所有消费者都必须带 sidecar。新 SC 参数不会自动补写存量 PV，CSI volume source 不可变，不能承诺热补；静态 PV 可在创建时声明同一参数。

```text
ControllerPublish(req):
    保留现有其他模式的分流
    if requireSidecar(req.VolumeContext):
        return success                    # 不创建独立 FUSE Pod
    return existingCreateFusePod(req)

NodePublish(req):                          # 本段只描述新的 RunC 范围
    pod = 复用公共包装层已有 Pod 信息
    consumer = 按保留名称查找并校验
    if consumer 声明错误:
        return error
    if consumer 存在:
        准备 Pod 独占目标与控制目录
        复用 file 协议写入完整 inventory 和不可变 desired
        return deliveryAccepted
    if requireSidecar(req.VolumeContext):
        return error("required mountproxy sidecar is missing")
    socket = 现有 controller PublishContext 的 socket
    return existingSharedGlobalMountFlow(req, socket)
```

原 RunC 独立 FUSE Pod 使用 `directAssigned=false / socket非空 / skipGlobalMount=false`。新的 file sidecar 不能继续依赖 hasSocket=true：controller 跳过创建时没有 socket，node 必须在旧分支强制检查 socket 之前，依据 key 和已有 Pod 的 sidecar 声明完成分流，不能伪造 socket 或要求全局 socket flag。这里不额外查询 RuntimeClass，也不依赖 nil 判断实际运行时。RunD 现有入口保持独立。

仍需区分挂载范围：独立 FUSE Pod 使用现有共享 globalmount；业务 sidecar 使用 Pod 独占目标与生命周期。skipGlobalMount 不新增为要求用户配置的全局 env；新 key 的 controller 行为和 node 的消费者选择也不能合并成“开 key 才识别 sidecar”。

两条 sidecar 路径都使用文件，交付不再要求确认 UDS 是否跨内核可达。但不得据此声明所有默认 handler 已受支持：控制文件一致性、数据投影和 mount propagation 仍须在目标运行时验证。RunD 和 RunC 各自入口与鉴权能力保持清晰，不在本轮自动扩大支持范围。

## 首次发布与异步完成

标准 kubelet 在启动 Pod 容器之前等待卷发布。因此两条新 sidecar 路径都不能让首次 NodePublish 同步等待同 Pod 的 mountproxy 完成 mount：

```text
NodePublish 校验、准备投影、接受任务
  → 返回成功（交付成功，不是远端已挂载）
  → kubelet 启动首位 native sidecar
  → sidecar 消费 file 请求并执行 mount
  → 全部期望卷成功，startupProbe 通过
  → 后续 init 和业务启动
```

参考 [Kubernetes v1.33.3 SyncPod](https://github.com/kubernetes/kubernetes/blob/v1.33.3/pkg/kubelet/kubelet.go#L1892)。逐卷结果文件供 probe、日志和后续诊断使用。重复交付必须幂等，状态缺失/超时不证明 mount 没发生，不能据此盲目另起 daemon。

CSI 同步校验失败会使 NodePublish 返回错误，用户看到 FailedMount；已接受任务后的 daemon 挂载失败则由 startupProbe 阻塞后续容器，暴露 probe 错误、sidecar 日志与逐卷结果。不能把后一种情况一概称为首次 FailedMount。业务放行后的失败还需 readiness/状态反映，不能把一次 Ready 永久缓存。

GCS FUSE 采用 CSI 先建内核 FUSE 挂载、再向 sidecar 传 fd 的方案，不是同步等待 sidecar；其机制见[官方设计说明](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/main/docs/known-issues.md#the-sidecar-container-mode-design)。RunC fd-passing 适配采用同样的阶段划分，复用本仓库已有实现。RunD 当前由 privileged sidecar 直接执行 mount，不能天然获得“daemon 尚未接管时访问只阻塞、不落空目录”的属性；两者都需要 startupProbe 屏障。

## 生命周期和清理边界

| 场景 | RunD file 现有原则 | RunC file 必须落实的要求 |
|---|---|---|
| 多卷/部分失败 | 独立 worker 与结果，全部成功才放行 | 复用屏障和逐卷隔离，不用一个文件成功代表全部成功 |
| CSI 重启 | 请求文件保留，恢复与幂等消费 | 待投递任务与结果可恢复，不能只保存内存队列 |
| 文件不可见/状态超时 | 心跳/状态超时不等于挂载已撤销 | 复用请求身份、generation 与状态语义，不能盲目另起 daemon |
| 正常退出 | native sidecar 接收 SIGTERM drain，EBUSY 明确保留待清理 | 保持明确卸载与进程回收，验证 host 可见挂载清理 |
| Pod 强删/sidecar SIGKILL | 不等已停止 sidecar ACK；kubelet/CRI 与 NodeUnpublish 兜底 | 同样不能依赖结果文件 ACK 完成回收；旧任务不能复活 |
| 已放行业务后 daemon 失败 | 不透明重挂冒充修复既有业务 bind | 按 driver 能力接入已有 fd recovery，耗尽后明确失败；不以重挂冒充恢复 |

控制文件的删除不等价于卸载，缺失 desired 不能视为可信卸载指令。路径要按 PodUID 隔离；删除一个 Pod 不得卸载其他消费者的挂载。只清理自己明确拥有的挂载/控制对象，不对可能承载远端数据的目标执行递归删除。

RunD 已有测试结果不能替代 RunC 验收：runc 的 mount namespace、传播与宿主机残留不同，必须单独验证。RunD 现有回收细节和局限继续以[根因文档](fuse-manual-sidecar-rootcause-zh.md)为准，不因统一 file 就宣称两者已经等价或清理验收完成。

## 开销要求与尚待落地的细节

正常首位 sidecar 应无需额外绑定元数据、无新增 PVC GET。非首位但前序 init 不挂 PVC，也不因顺序检查查询；只有前序存在 PVC 且需要确定交集时，才考虑已有元数据优先、查询兜底。这个顺序检查目标与当前独立绑定校验的实现不同，尚待调整；跳过绑定校验也意味着失去当前针对手工交换 PV/PVC 映射的额外保护，不能同时宣称两者均保留。

RunC 开发前需补齐具体契约，但不重新讨论已定的 file/socket 选择：

1. 新 key 的正式名称、布尔解析、错误提示与参数保留测试。
2. 复用 PodUID 控制目录、manifest/desired/result 和默认 file-mode/probe 契约，确认 runc 的挂载映射。
3. 复用 UID/卷/generation 身份与幂等机制，验证结果缺失和 CSI 重启，不另建一套异步任务队列。
4. 复用完整卷集合与 probe 屏障，覆盖多卷；不要求用户按卷修改参数。
5. CSI 先建内核挂载、非特权 sidecar 接管 fd 的目标映射，以及 RRSA/Secret volume 的具体接入；不能退回“默认 privileged”绕过。
6. 与现有卷级操作锁、独立 FUSE Pod controller 回收和共享 globalmount 的隔离。

首期 RunC 验收至少覆盖上表四种分流、同名错误配置、nil RuntimeClass 不引入额外请求、双卷与不同 driver、Secret/RRSA、控制文件新增/原子替换/删除/损坏/旧 generation、状态过期、CSI/sidecar 重启、部分失败、正常删除/强删/挂载中删除、两个 Pod 共用 PV 的隔离、远端哨兵保留和无残留挂载。同时回归独立 FUSE Pod 的 socket/共享挂载路径。不能以“能读文件”代替生命周期验收。

## 非特权 sidecar 与已有 fd-passing 的复用

仓库已经支持 fd-passing，不需要重写底层能力：

- `pkg/mounter/proxy/client/client.go` 已有 prepareFuseFd、内核 mount 和 SCM_RIGHTS 发送。
- `pkg/mounter/proxy/server/handler.go` 已有 fd 接收和错误路径关闭。
- `pkg/mounter/proxy/server/ossfs2/driver.go`、mount.go、supervise.go 已有 FuseFd/Recovery 执行、fd 持有和 daemon 恢复逻辑。
- 独立 FUSE Pod 模板已经根据 FdPassing 降低权限。不能把现有能力误报成待从零设计。

当前差异是 client.Mount 同步“建挂载→连接并发送→等执行结果”，失败时会清理新挂载；同 Pod sidecar 首次尚未启动，不能直接调用它后返回成功。需要拆出或复用底层步骤，增加 Pod/卷/generation 绑定的异步 fd 交接。文件继续交付配置与状态，socket 仅交付对应连接的 fd，不把 fd 数字写入 JSON 冒充可恢复句柄。

```text
CSI：校验 → 创建 Pod 独占内核 FUSE 挂载并暂持 fd
   → 持久发布 file 配置、准备 fd 交接端点 → NodePublish 返回
sidecar：启动 → 读 file → 接收匹配的 fd
        → 复用支持该能力的 driver 执行链路 → 写结果 → probe 放行
```

最小新增边界是异步交接、请求与 fd 的关联、重复/过期接收、取消和未接收清理，以及 FileMounter 到已有 FuseFd/Recovery 执行的适配。CSI 重启会丢失其进程持有的 fd，文件不能恢复这个句柄，需明确接管前后的重试/重建边界，不能声称持久配置等于持久 fd。实际实现应复用已有函数和测试，避免复制一套 fd 管理器。

RunC 复用的已有 recovery 针对 mountproxy 存活时的 daemon 故障，不应重新发明；但 file worker 目前将 daemon 退出视为失败，需要适配“恢复中/恢复成功/耗尽”的结果和 probe 语义。mountproxy 本身退出、整个 Pod 重建的恢复范围须单独说明，不能扩大现有保证。driver 能力要逐个确认，不能仅因 ossfs2 支持就对 ossfs/customfuse 全部开启。此适配不向 RunD 开放 recovery。

RunC fd-capable driver 验收需增加：sidecar 和业务容器均非 privileged、无多余 capability/设备授权、CSI 建挂载后 sidecar 延迟启动、重复 fd 交接/失败清理、daemon recovery 复用、CSI 在接管前后重启，以及 inode/既有业务挂载保持有效。启动期内核未服务挂载可能阻塞访问，需验证 kubelet/CRI 启动与挂载检查不会因此死锁。customfuse 的权限按下一节独立验收。

RunD 的 host 与 guest 不共享内核，不能通过 HostPath 或 SCM_RIGHTS 直接交付宿主机 FUSE connection。最终决定不支持 RunD sidecar fd-passing/recovery，也不将新增 guest 内有权限挂载者或运行时代理作为本方案的开发项。现有 guest file 保持 privileged，这是当前权限边界，不能解释为所有 file 模式都必须特权。两条路径统一控制协议，不意味着底层 fd 通道或权限能力相同。

## customfuse：必须支持的独立能力分支

RunD 和后续 RunC 均必须支持 customfuse，不能仅验通 ossfs2 就视为交付完成。customfuse 不考虑 fd-passing/recovery，也不要求任意 FUSE 客户端支持接管 fd；显式请求这些能力应报错。它复用 file 配置、inventory、逐卷 worker、probe 和清理框架，由自定义入口直接执行挂载。现有子进程集测不等于真实 customfuse 文件系统 E2E 已通过。

用户镜像带兼容的 mountproxy 和自己的客户端/入口，sidecar 名称使用 `<vendor>-csi-customfuse-<name>`。复用既有 customfuse 入口、参数及子进程组管理契约，所需 ConfigMap/Secret 由 Pod 显式挂载，不将 CSI Secret 写进控制 JSON。RunD 继续仅 RRSA；自定义客户端需要适配业务 Pod 身份，不能仅因收到 rrsa 参数就宣称任何文件系统已完成鉴权。RunC 的 Secret volume/身份适配在后续 PR 实现。

权限必须按 driver 能力说明：RunC 的非特权目标适用于支持 fd 接管的 driver；customfuse 直接 mount 的客户端所需设备、capability、传播和 privileged 要求需独立验证，不能保证任意客户端均非特权，也不能为减少实现而一律给所有 sidecar privileged。业务应用容器不需要因此开启 privileged。若一个挂载消费者管理的混合卷要求不同权限，容器权限按整体生效，不能按卷隔离或继续将其标称非特权；webhook 不得静默提权，须明确允许所需权限或拒绝不兼容配置。

验收必须包括真实 customfuse 双卷挂载、自定义入口失败/退出、子进程回收、部分失败隔离、取消、正常删除/强删/CSI 重启、无残留挂载和远端哨兵保留；同时覆盖独立命名的识别、同名非法配置、多候选拒绝、卷目标映射和 probe 屏障。混合 driver 是否支持由显式能力和单消费者契约决定，不因名字自动允许。优先用单测/子进程集测覆盖参数和异常，真实 E2E 验关键数据面与生命周期；RunD、RunC 分别记录结果。

PR 1 纳入 RunD customfuse 的命名支持、契约与验收；PR 2 纳入 RunC customfuse 的直接挂载分支及权限验收，与 fd-capable 非特权路线区分；PR 3 支持对应命名和自定义镜像的可选注入。仍按三个功能 PR 交付，不把 customfuse 当作无限期后续增强。

## 可选 webhook：只生成配置，不参与挂载执行

使用 Pod annotation 显式请求注入，例如 `csi.alibabacloud.com/inject-mountproxy: "true"`；该 key 是设计候选，正式名称待实现确定，不是当前可用配置。Deployment 应写在 spec.template.metadata.annotations。未声明时 webhook 不注入，不根据 RuntimeClass 或“发现 FUSE 卷”自动开启。

三个选择分别独立：

| 声明 | 职责 |
|---|---|
| Pod 注入 annotation | 请求 webhook 生成 sidecar、控制挂载、probe 等配置 |
| SC/PV 的 requireSidecar 候选 key | controller 跳过独立 FUSE Pod；node 强制要求 sidecar |
| 最终 Pod 中保留名称的 sidecar | node 识别、校验并选择消费者，不关心手工还是 webhook 生成 |

关闭或未安装 webhook 时，用户仍可手动配置。annotation 本身不会让 CSI 自动生成容器；RunC requireSidecar 开启但最终没有 sidecar，node 明确 FailedMount。未开启该 key 时仍遵循前述四种分流，不能承诺仅有 annotation 就阻止 controller 创建独立 FUSE Pod。RunD 保持其现有缺 consumer 报错入口。

webhook 负责生成符合公共契约的 Pod：按 OSS 默认名称或 customfuse 独立命名的 Always init sidecar 放在首位、完整卷集合、默认 file-mode/probe、PodUID 控制目录隔离、数据卷映射、RRSA projection 或支持的 Secret volume、资源及安全上下文。RunD 只允许 RRSA 并保持当前权限；RunC 按 driver 能力区分非特权 fd 接管与 customfuse 直接挂载，不能统一注入 privileged 或静默提权。需要明确的运行环境/能力配置，不能把 runtimeClassName=nil 当作 runc 的证明。

已存在保留名称时不再插入第二份。合法手动配置保持，不能覆盖自定义镜像/启动入口；明确请求注入但现有配置不兼容、卷类型或 driver 能力不支持时返回可操作的 admission 错误，不静默跳过。重复 admission 应幂等。注入结果仍由 node 校验，不以 annotation 当作可信认证。其他 mutating webhook 可能再次调整 init 顺序，必须通过组合测试验证；不能因自己 prepend 就保证最终顺序永远成立。

对明确请求注入的 Pod，推荐 webhook 不可用时 admission 失败，避免静默遗漏；未请求注入的 Pod 不应受其故障影响。具体 webhook 匹配范围、failurePolicy、超时、证书和升级通过部署设计落实。webhook 不执行 mount、不处理 NodeUnpublish，不持有 FUSE fd，也不复制 CSI Secret 到 Pod env/annotation。

### 卷绑定信息与 admission 成本

webhook 可能需要读取 PVC、PV 和 SC，才能识别目标 driver、参数并生成当前 `/pod-volumes/<PV-name>/mount` 映射。这是 admission 阶段成本，不是 node 每次发布的新增请求；应按 Pod 内唯一卷去重查询，不能简单宣称全过程零额外 API。

必须处理 Pod 创建时 PVC 尚未 Bound 的情况。特别是 WaitForFirstConsumer：如果 webhook 因拿不到 PV 就等待绑定，而绑定需要该 Pod 先进入调度，会形成等待循环；不能把延长 admission 超时当作方案。当前路径协议需要 PV 名，因此 webhook 不能仅靠一个 annotation 就承诺支持所有动态供给场景。

PR 开发前需确认支持范围和解决方式，保持既有 mountproxy 路径协议。可以先限定已绑定卷并明确拒绝未支持场景，但必须标明不支持该 WaitForFirstConsumer 创建流程，不能声称会自动重试完成；如要覆盖未绑定动态卷，需有经过验证的注入/映射方案，不能悄悄改路径、要求用户手填卷列表或阻塞 admission 等 PV。此问题是 webhook 的独立验收项，不反向成为手动 sidecar 核心能力的前置依赖。

webhook 测试还需覆盖：不开 annotation、开 annotation、合法手动 sidecar、同名非法容器、重复 admission、多卷/混合 driver、未绑定或已删除 PVC、API 失败、RRSA/Secret、不同运行环境、与其他 webhook 的顺序交互，以及与 SC key 的四种组合。至少实际验通一条 webhook 注入后的完整挂载和正常/强删清理链路。

## PR 拆分与依赖

建议总计 **3 个功能 PR**，包含当前 RunD PR；设计文档随相关 PR 更新，不再拆一个纯文档前置 PR。每个 PR 自带必要测试、使用说明和验收记录，不把可用性依赖的清理/错误处理留给后续补丁。

| PR | 内容和完成边界 | 依赖 |
|---|---|---|
| 1：当前 RunD file 基础 | 当前 PR #13：file 契约、固定名称、RRSA、多卷、probe、失败与回收；明确不支持 fd-passing/recovery；收尾首位正常路径免额外配置/免 PVC GET 等已承诺项 | 不依赖 RunC 或 webhook |
| 2：RunC 非特权 file sidecar | 独立分支/MR：SC key、controller/node 四种分流、Pod 独占挂载、复用 file 协议与已有 fd-passing/driver recovery、Secret volume、非特权及完整生命周期验收；回归原独立 FUSE Pod | 依赖 PR 1 的公共协议和 worker/probe；不依赖 webhook，手动 Pod 完成验收 |
| 3：可选 webhook | annotation 驱动的幂等注入、两种环境模板、卷映射与绑定时序处理、部署/证书/失败策略、注入 E2E；不重复实现挂载执行 | 完整覆盖 RunD/RunC 时依赖 PR 1 和 PR 2 的稳定配置契约 |

推荐合并顺序是 PR 1 → PR 2 → PR 3。当前 PR 不加入 RunC 实现；PR 2 从 PR 1 合并后的基线建立，若提前开发则显式以其分支为 base，review 只包含增量。不要因业务需求解耦就宣称代码无依赖。

webhook 在功能上不是两种手动 sidecar 的依赖。PR 3 的调研和测试可并行，但不应先发布猜测 RunC fd 配置的模板。若必须更早交付 RunD-only webhook，可只依赖 PR 1 并明确不支持 RunC，后续再补一个模板扩展 PR；这属于交付计划改变，默认仍按总计三个 PR、不额外拆分。
