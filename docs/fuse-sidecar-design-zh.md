# FUSE 请求交付与挂载生命周期重构设计

状态：待评审，本文描述目标设计，不代表当前 PR 已实现。

本文对应 [PR #12](https://github.com/AlbeeSo/alibaba-cloud-csi-driver/pull/12) 的下一轮调整。调研基线为 upstream/master `3cc89c86b`，现有实验实现为 `002fbbb92`。实验实现仍使用 `RundCSIProtocol3` 选择 ACK 沙箱文件路径；本设计拟解除该绑定。本文只新增设计说明，不修改运行代码，也不执行 E2E。

## 1. 目标与边界

补齐“集群 DS 将挂载请求直接交给 Pod 内 mountproxy”的执行组合，支持 rund 文件交付和后续 runc RPC。原 guest csi-agent 本来也可以调用 guest mountproxy sidecar，因此不能用“Legacy / Sidecar”把新旧流程分成互斥两类。

设计区分四个维度：

| 维度 | 取值 | 谁负责选择 |
| --- | --- | --- |
| 接收入口与所有权 | 集群 DS、guest agent、既有委托 | 执行入口、部署契约和持久化执行记录 |
| 挂载作用域 | 共享 attachPath / Pod 级目标 | 已确认的执行计划，按请求决定 |
| 请求交付 | 既有 socket RPC / 文件 / 后续 Pod RPC | 已有契约或目标环境能力，不按 runv/rund 名称区分 |
| 消费方提供方式 | 独立 FUSE Pod、runtime/外部 controller、手动、webhook | 原提供者或独立的注入配置 |

非目标：改变未显式迁移的既有路径；默认切换到 virtiofs；接管原 csi-agent；透明恢复业务已有 FUSE 连接；在第一阶段实现 runc Pod RPC。runv 仅作为 RunD family 的测试实现，最终部署目标是实际 rund。

### 1.1 阻塞项一：固定的是命令，还是完整 sidecar spec

当前实验实现只能确认启动参数和探针不随卷集合变化，不能把它描述为完整 sidecar 配置不变：

| 配置部分 | 当前效果 |
| --- | --- |
| 启动命令和探针 | 固定，预期卷由 CSI manifest 提供 |
| PV 中的 source、挂载选项、凭据 | 请求自动携带，无需复制到 sidecar args；配置变更仍可能要求重建 Pod，不是热更新承诺 |
| sidecar.volumeMounts | 每个 PVC 仍需一项，目标路径含 PV 名；手动用户增加卷时仍须调整 |
| 镜像与资源 | 镜像必须已有对应客户端；不能承诺固定资源配置支持任意多卷 |

开启 webhook 后，用户无需维护 sidecar 段，但最终生成的 volumeMounts 仍随 PVC 集合变化。关闭 webhook 后，当前方案还不满足“整个 sidecar container 段完全固定”。

要满足严格的固定 spec，需要一个固定挂载入口，使该 Pod 所有选中目标在其下可见，并与业务 PVC 的视图一致。RunD 只提供配置目录 bind mount 还不够，还需目标卷视图及传播契约。原实验中“额外挂 kubelet CSI 父目录 HostPath”的方案已出现 guest alias 不一致，不能重新当成已验证方案。

因此该项按两级验收：固定 args/probe 已有代码；无 webhook 的固定完整 sidecar spec 仍是待解决的设计问题，不能因自动 manifest 已实现而关闭。

### 1.2 阻塞项二：清理完成与数据安全不是同一个保证

**当前不能保证挂载成功、失败、force delete、进程强杀、guest/节点故障等所有情况下均无遗留。Kubernetes 1.33 不提供这样的保证。** 原生 sidecar 改进停止顺序，但主容器可能耗尽宽限期，使 sidecar 来不及优雅清理；force delete 也不等待节点确认进程终止。见 [1.33 sidecar 终止说明](https://v1-33.docs.kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/)、[kubectl 强删语义](https://kubernetes.io/docs/reference/kubectl/generated/kubectl_delete/)。

需要先分开两个目录和两种风险：

- 配置 HostPath 只放 manifest、请求和状态，不能承载业务 FUSE 挂载或业务数据。
- 业务目标路径若仍能到达一个存活、可写的远端文件系统，外部递归删除其父目录就可能变成远端 unlink；Pod 对象消失、探针失败或某个 namespace 中已看不到挂载都不是充分保护。

runc 的 Bidirectional 传播会涉及 host 可见挂载；rund 是否把 FUSE 限制在 guest 内，需验证实际 namespace/export 图。此前 E2E 的检查是 node-plugin 可见的挂载表、guest 状态与优雅终止，不是覆盖所有 namespace 的强删清理证明。若要让 host 上任意特权递归删除都无法触及远端，必须通过实际隔离保证 host 无此访问路径；一旦远端挂载对 host 可达，CSI 无法约束所有外部 root 操作。

1.33 文档要求容器自行卸载其创建的传播挂载，并提示 mount propagation 并非对所有卷类型都一致，建议使用 hostPath 或内存 emptyDir。当前使用 CSI PVC 传播的方案必须按具体 runtime 验证，不能仅凭集群版本给出通用保证。见 [1.33 mount propagation](https://v1-33.docs.kubernetes.io/docs/concepts/storage/volumes/#mount-propagation)。

当前实现还存在以下缺口：

1. runner 的显式卸载依赖进程能执行收尾；SIGKILL 不会运行 Go defer。
2. EBUSY 时使用 MNT_DETACH，随后写 Unmounted。该结果只能说明相应卸载操作已处理，不能证明全部别名、namespace 或已有引用都消失；应区分“已分离”和“已验证完成”。见 [Linux umount 语义](https://man7.org/linux/man-pages/man2/umount.2.html)。
3. NodeUnpublish 删除请求并清理自身可见目标，没有等待和验证 guest 卸载完成，也没有独立的异常挂载回收器。
4. 挂载失败原因主要保存在状态文件与日志中，探针通常只显示 phase；强杀后依靠 heartbeat 失效，没有稳定的 Pod 外部失败/清理记录及完整 Events/诊断闭环。

这里不是说现有代码已经执行了危险递归删除：当前文件取消只删除指定请求/描述文件，mount-utils 目标清理使用单路径删除。问题在于，尚未证明遗留挂载不会被其他清理流程遇到，更不能把一次成功的状态回写当成后续目录 GC 的授权。

### 1.3 第一阶段必须补齐的安全契约

以下是设计要求，尚未实现，不能用普通 E2E 通过替代：

```text
请求取消 / Pod 结束
→ 记录 CleanupRequested，阻止旧会话继续执行或重新挂载
→ 活跃 consumer 显式卸载并报告结果
→ consumer 不可达时，由独立生命周期管理方接管
→ 确认对应 runtime/guest/进程状态，并验证自身拥有的挂载及别名
→ 已验证清理完成：才允许删除本地空目标与配置元数据
→ 不能确认：CleanupPending / CleanupFailed / CleanupUnknown，保留隔离记录并报警
```

- 外部管理方可由 DS 与 runtime 能力协作，但不能假定 DS 能进入任意 RunD guest。guest 不可达时需要 runtime 确认销毁/隔离，不能只依据 API Pod NotFound；没有这种能力就必须保留 Unknown，而不是伪造 Cleaned。
- 持久化所有权需关联 Pod UID、卷、session、runtime/sandbox 标识及挂载身份，不能只凭 PID、路径前缀或当前 feature gate 判断。
- 清理不得递归遍历可能到达远端文件系统的目标树；不确定时保留本地目录，不能通过递归删除“回收”未知挂载。仅清理明确属于自己的本地元数据，以及验证后的空目录。
- 状态必须区别挂载结果、卸载请求、lazy detach、清理失败和清理未知。Pod 消失后仍应能按 UID/卷诊断，不只依赖已消失 Pod 的探针输出。
- DS/runtime 恢复后重试异常清理；不能承诺在节点永久不可达时立刻回收。目标是可观测的最终收敛与安全保留，不是任意故障下即时无残留。

新环境验收需覆盖：成功后删除、挂载中取消、失败后部分挂载、busy mount、sidecar 强杀、DS 重启、Pod 强删、guest/节点失联及恢复。数据安全验证使用隔离测试数据、远端删除审计和 sentinel；既看自身注册范围内的挂载，也检查真实 host/guest/runtime 视图。不能只测目录不存在或 Unmounted 字符串。

在固定目标视图、异常清理所有者和上述安全契约明确前，第一阶段应保持实验状态，不能宣称已解决生产环境的挂载泄漏与误删风险。runc 换成 gRPC 也不会自动解决这些数据面生命周期问题。

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

正常构造时，DS nodeServer 的 `skipGlobalMount` 默认 false，csi-agent 默认 true；普通 csi-plugin 也能通过环境变量将其改为 true。因此，这张表里的 RunD 执行分支并不要求进程一定是 csi-agent binary；外部 controller 提供 socket、普通 CSI 进程以直接挂载配置运行的 sandbox 是另一条既有路径。不能宣称所有名为 rund 的 Pod 都有一种自动 fallback。

原 OSS `RundCSIProtocol3` 分支还会在 host 插件识别到旧 `rund` 路径时直接 skip，将执行交给外部机制。skip 成功本身不能证明后续发生了 virtiofs 共享或成功挂载。customfuse 原默认代码则通过 socket 和 `skipGlobalMount` 选择 attach 或直接挂载，没有完全相同的旧 gate 分支。

源码入口：[运行时判断](../pkg/oss/utils.go)、[DS 构造](../pkg/oss/oss.go)、[agent 构造](../pkg/oss/csi_agent.go)、[OSS NodePublish](../pkg/oss/nodeserver.go)、[customfuse NodePublish](../pkg/customfuse/nodeserver.go)。旧 gate 行为应对照上述 upstream 基线，而不是已改动的实验分支。

### 2.2 本设计如何对待默认行为

保持既有部署契约意味着“调用既有流程”，不是“保证成功”，也不是“禁止 sidecar”：

- runc 的常规部署继续使用独立 FUSE Pod。
- rund 继续既有判断；原来需要 agent 就仍需要，原来报错就仍可报错。
- 如果某种现有部署实际由 host 挂载再经 virtiofs 共享，本设计保留它，但不新增该 fallback。
- 第一阶段先记录新环境中未要求 DS→Pod 交付、不配新消费方的实测结果，作为后续比较基线。

virtiofs 描述 host/guest 的文件共享方式，独立 FUSE Pod / 业务 sidecar 描述 FUSE 进程的执行位置，两者不应混成同一个枚举。

### 2.3 三个参数实际从哪里来

以下三参数组合表是 OSS 的逻辑；customfuse 复用 socket 和 skipGlobalMount 的来源，但不使用 OSS 的 DirectAssigned/运行时组合表。

| 参数 | 实际输入与优先级 | 不代表什么 |
| --- | --- | --- |
| DirectAssigned | `VolumeContext["direct"]` 的合法布尔值 > `VolumeContext["runtimeclass"]` 推导值 > CSI 进程环境变量 `DEFAULT_RUNTIME_CLASS` 推导值 > false | 不是实际 Pod RuntimeClass 自动检测，也不是是否使用新 sidecar |
| socket | 非空进程 flag/构造传入值 > `NodePublishVolumeRequest.PublishContext["mountPorxySocket"]` > 空 | 非空不表示 socket 已存在或可达，更不表示一定由独立 FUSE Pod、csi-agent 或业务 sidecar 提供 |
| skipGlobalMount | `SKIP_GLOBAL_MOUNT` > `OSS_SKIP_GLOBAL_MOUNT` > 构造默认值；普通插件默认 false，csi-agent 默认 true | 不是根据 binary 名或 RuntimeClass 自动检测出来的事实 |

#### DirectAssigned

PV 典型配置为 `spec.csi.volumeAttributes.direct: "true"`。请求解析按字段名转小写处理；显式合法的 `direct` 优先级最高。

没有合法显式值时，读取 PV/请求参数 `runtimeclass`，为空才读取进程环境变量 `DEFAULT_RUNTIME_CLASS`。值为 `rund` 得到 true，`runc` 或空得到 false；其他值警告并得到 false。非法 `direct` 值警告后保留前面推导结果。**这里不是读取 Pod.spec.runtimeClassName。**

DirectAssigned 也参与 ControllerPublish：OSS 的旧 controller 在其为 true 时跳过创建独立 FUSE Pod，所以 controller 与 node 的环境默认不一致同样可能造成接线错误。

#### socket

普通 csi-plugin 的 `--mount-proxy-sock` 用于 OSS，`--customfuse-mount-proxy-sock` 用于 customfuse，默认均为空。非空 flag 会覆盖请求中注入的 socket。

csi-agent 使用同名 flags，默认分别为 `/run/kube-agent/csi-agent.sock` 与 `/run/kube-agent/customfuse-agent.sock`，经构造函数写入 nodeServer 的覆盖值。

请求字段是 **PublishContext** 的 `mountPorxySocket`，不是 VolumeContext；`Porxy` 的拼写错误属于现有 wire format，不能直接改正。普通 controller 创建独立 FUSE Pod 后会返回该字段，kubelet 后续带给 NodePublish；外部 sandbox controller 也可以把它写入请求。接收端只解析值，不根据这个字段识别生产者。

因此，用户指出的“外部 controller 注入 socket”的场景确实与“csi-agent 由 flag 获得 socket”不同。注入本身也不会自动把 skipGlobalMount 改为 true；需要另看运行进程的环境配置。

#### skipGlobalMount

它表示是否绕过共享 attach/global mount，直接挂到本次 TargetPath。变量是通过环境配置和构造默认计算的，不是一个同名 PV key 或 CLI flag。

若高优先级的非空环境变量无法解析，当前代码会警告并返回构造默认，不再尝试低优先级环境变量。例如非法 SKIP_GLOBAL_MOUNT 不会继续使用 OSS_SKIP_GLOBAL_MOUNT。

#### 同样有 socket，也可能是不同部署

| 部署/输入 | direct | socket 来源 | skipGlobalMount | OSS 旧执行效果 |
| --- | --- | --- | --- | --- |
| 常规 host CSI + 独立 FUSE Pod | 默认 false | 本仓库 ControllerPublish 返回的 PublishContext | 默认 false | RunC：共享 attachPath 后 bind 到 Pod target |
| guest csi-agent + mountproxy | false 或 true | agent flag 的默认值或显式值 | 默认 true | RunD：直接挂 TargetPath |
| 外部 controller 管理的 sandbox，普通 CSI 进程执行 | false 或 true | 外部注入 PublishContext，且无非空 flag 覆盖 | 环境配置 true | 同样可进入 RunD 直接挂载分支，但不是 csi-agent binary |
| 外部只注入 socket，未改变普通插件默认 | false | 外部 PublishContext | false | 仍走 RunC 的 attach/global mount 逻辑，不会仅因 Pod 是 sandbox 自动直接挂载 |
| 普通插件配置 direct=true、有 socket，但未跳过 global mount | true | 任意有效来源 | false | 组合表报错，不 fallback 到 virtiofs |

这些是接收端代码对给定输入的确定结果，不是对所有外部 sandbox 部署配置的推测。外部 controller 的具体注入流程与目标进程位置，需要结合该部署的请求/环境配置确认。

源码入口：[direct 推导](../pkg/oss/utils.go)、[socket 优先级](../pkg/mounter/utils/utils.go)、[skipGlobalMount 环境解析](../pkg/utils/util.go)、[插件 flags](../main.go)、[agent flags](../cmd/csi-agent/main.go)。

## 3. 重构模型：不使用 Legacy / Sidecar 二分

sidecar 是 mountproxy 的部署形态，不是互斥的执行模式。旧 csi-agent、本来就由外部 controller 管理的 sandbox，以及新增 DS 文件交付，都可能使用 guest 内的 mountproxy sidecar。

因此，重构不应建立一个包含所有新逻辑的大 Sidecar 分支，再把其余代码全部留在 Legacy 分支。应明确哪些执行组合已经存在、哪个组合缺失，以及哪些机制可复用。

### 3.1 四个独立维度

| 维度 | 要表达的事实 | 不能用什么替代 |
| --- | --- | --- |
| CSI 接收入口与执行所有权 | 集群 DS 接收、guest csi-agent 接收，或 host 只负责委托；谁有权提交挂载 | binary 名、clientset 非空、RuntimeClass 名均不能独立证明全部所有权 |
| 挂载作用域 | 共享 attachPath，再 bind；或者直接挂该 Pod 的目标 | 不能认为只有 rund 才需要直接挂目标 |
| 请求交付方式 | 现有 socket RPC、HostPath 文件、新的 Pod RPC/gRPC | socket 字符串非空不能证明消费方、可达性和执行权 |
| mountproxy 提供方式 | 独立 FUSE Pod、原 runtime/外部 controller、用户手动配置、webhook 注入 | “有 sidecar”不等于使用了本次新增交付路径 |

runtime family、具体能力、目录映射、完成响应语义及 cleanup owner 是执行计划的约束，不再通过上述布尔值反推。

### 3.2 已有组合与新增组合

| 组合 | CSI 接收/提交方 | 挂载作用域 | 交付方式 | mountproxy 提供者 | 定位 |
| --- | --- | --- | --- | --- | --- |
| 普通 host FUSE | 集群 DS | SharedAttach | 现有 socket RPC | 独立 FUSE Pod | 既有路径 |
| guest agent | guest csi-agent | PodTarget | guest 本地 socket RPC | 原 runtime/编排，可能就是 guest sidecar | 既有路径，与新路径共享 Pod 级执行能力 |
| 外部编排 sandbox | 按已有部署确认的 CSI 执行方 | 按环境配置，可为 PodTarget | 外部注入或 flag 提供的 socket RPC | 外部 controller/runtime | 既有路径，不等同于 agent binary |
| DS 到 RunD Pod | 集群 DS | PodTarget | HostPath 文件 | 用户手动提供，之后可 webhook 注入 | 第一、二阶段新增组合 |
| DS 到 runc Pod | 集群 DS | PodTarget | 新 Pod RPC/gRPC | 用户手动提供或 webhook 注入 | 第三阶段新增组合 |

新增组合与既有组合共享 BuildMountRequest、FSType 分派、凭据/选项处理、PodTarget 执行、进程监管及部分状态语义。只在请求交付、目标映射、启动编排和清理责任发生差异的地方引入接口。

runc 业务 Pod 内的 FUSE 也应使用 PodTarget，不应占用其他 Pod 共用的 attachPath。因此“是否跳过 global mount”是作用域决定的，而不是按 runc/rund 一刀切。

### 3.3 runv 属于 RunD 测试实现，不是路径选择依据

实际部署目标是 rund。之前验证过的 runv 环境，只视为 RunD family 的一种测试实现。

不能通过 handler == runv、RuntimeClass 字面值、ACK 特定 label，把 agent、DS 和外部 controller 分开；也不能因为叫 rund 就判定必须 agent。RuntimeClass 是入口线索，具体运行时实现、目录共享、设备、RPC 可达性与 cleanup 能力需另行核实。

实验代码里的 ACK runv 精确匹配不是最终设计。runv 上的验证不能认证其他 rund 版本的传播、强杀清理或扩展功能。本文不根据名称宣称两个实现完全等价。

## 4. 配置与所有权

### 4.1 用户仍只有两类主要配置

第一类选择“是否由集群 CSI 直接向 Pod 的 mountproxy 交付请求”，不再命名为是否使用 sidecar。第二类控制是否使用 webhook 构造消费方。

新 gate 和 PV key 的正式名字待评审，不再沿用此前草案中的 FuseSidecar / mount-mode=sidecar；以下使用逻辑值描述契约：

| 全局 DS 直接交付默认 | PV 显式意图 | 对集群插件的要求 |
| --- | --- | --- |
| 关闭 | 未配置 | 保持当前部署契约 |
| 开启 | 未配置 | 尝试解析受支持的 DS→Pod 交付计划；不越过已有所有权约束 |
| 任意 | 启用 DS→Pod 交付 | 显式要求新交付契约 |
| 任意 | 保持当前部署契约 | 覆盖全局默认，不改变原提供者和交付方式 |
| 任意 | 非法值 | 配置错误，不静默猜测 |

“保持当前部署契约”可能仍是 guest sidecar 或外部 sandbox RPC，不代表禁止 sidecar。全局默认只作用于集群插件支持的常规 FUSE 数据卷，不改变 guest agent 的进程默认，不自动改造 rootfs、COCO 等特殊路径。

guest agent 收到声明由 DS 独占交付的执行计划时，应报告所有权冲突；普通全局默认不能让 agent 擅自接管或让 host 与 agent 同时提交同一次挂载。原 RundCSIProtocol3 保留原语义，与新意图分开解析。

### 4.2 webhook 的四格矩阵

| DS→Pod 交付意图 | webhook | 预期 |
| --- | --- | --- |
| 未选择 | 关闭 | 原提供者继续工作，原成功/失败语义保留 |
| 未选择 | 开启 | 不接管原提供者，不为这些卷新增消费方 |
| 已选择且执行权成立 | 关闭 | 使用用户手动提供的合法消费方；缺失明确报错 |
| 已选择且执行权成立 | 开启 | 注入或验证匹配的消费方；opt-out 时验证手动配置 |

Pod 的 inject=false 只取消自动构造，不撤销交付意图。webhook 的存在也不证明 Pod 已被正确注入；NodePublish 仍要验证实际 PodSpec。验证的是消费方契约，不是在同步 CSI 调用里等进程启动。

外部 controller 已提供的消费方不能因为启用了 webhook 再重复注入；应先验证它是否属于该卷的已声明提供者。容器名称或 socket 非空都不足以认领执行权。

### 4.3 组件策略与迁移

ControllerPublish 没有 PodInfo，admission 时 Pod 通常未调度，DS 才知道实际 Node。因此 controller、DS、webhook 需要同源交付策略和版本校验，不可分别猜测是否创建独立 FUSE Pod。

若严格要求只修改某个 DS 的 gate，需设计节点策略发布与 admission/调度协调。在异构节点上，webhook 不能在未调度时自动得知未来 DS 的默认值；这仍是待解决项。建议全局策略从一处安装配置分发，逐卷差异由 PV 表达，不再增加用户必须填写的 transport 开关。

变更交付意图不是热迁移。旧共享挂载、agent 或外部 controller 仍拥有该卷时，不得直接再启动新的挂载执行者。需明确迁移顺序及旧挂载清理；不满足条件时返回 OwnershipConflict。阶段一不包含对外部 controller 管理路径的自动迁移。

## 5. 中央执行计划与伪代码

### 5.1 执行计划

以下字段是内部模型，不要求用户逐项配置：

~~~text
ExecutionPlan {
    receiver             # ClusterDS / GuestAgent / ExistingDelegate
    submissionOwner      # 谁有权提交这次挂载
    mountScope           # SharedAttach / PodTarget
    delivery             # ExistingSocket / File / PodRPC
    consumerProvider     # FusePod / Runtime / ExternalController / Manual / Webhook
    runtimeFamily        # Runc / RunD / Other；runv 不单列为一种模式
    targetMapping        # host 请求目标到消费方可见目标的明确映射
    completionPolicy     # 既有同步结果 / 异步交付加独立执行结果
    cleanupOwner         # 优雅清理者、异常接管者与其实际能力
    policyRevision
}
~~~

SharedAttach 计划才使用共享 attachPath；PodTarget 计划直接使用该 Pod 的目标。这个派生结果只影响当前请求，不修改进程级 SKIP_GLOBAL_MOUNT。

旧参数解析器作为兼容适配器保留，包括原来会报错的 direct=true、有 socket、skipGlobalMount=false 组合。新组合应在验证新意图与所有权后生成独立计划，不通过修改旧组合表把错误配置直接变成合法 sidecar。

### 5.2 解析器

~~~text
ResolvePlan(context):
    if an execution record already exists:
        return ValidateAndReuseRecordedPlan(context)  # 不按新 gate 偷换所有者

    facts = DescribeExistingContract(context)
    # 记录输入来源：binary/环境、PV、请求 socket、外部编排契约
    # 不把 socket 来源未知，猜成 FusePod / agent / 新 sidecar

    if context.receiver == GuestAgent:
        ValidateNoConflictingDSOwnership(context)
        return ExistingAgentAdapter(facts)
        # 通常同样得到 PodTarget + 本地 RPC + guest mountproxy

    intent = ResolveDSDeliveryIntent(PV, sharedPolicy)
    if intent preserves existing deployment:
        return ExistingDeploymentAdapter(facts)
        # 可能是 SharedAttach，也可能已经是 PodTarget + 外部 RPC

    ValidateExclusiveSubmissionOwnership(facts, intent)
    ValidateSupportedDataVolume(context)

    if an existing consumer contract is explicitly compatible and retained:
        return ReuseCompatiblePlan(facts)
        # 必须有真实提供者/所有权依据；不能仅凭“有 socket”复用
        # 第一阶段不自动迁移外部编排路径

    pod = GetAndVerifyPodIdentityAndNode(context)
    runtime = ResolveRuntimeFamilyAndCapabilities(context, pod)

    if runtime is RunD and file-sharing contract is verified:
        delivery = File
    else if runtime is Runc and stage >= 3 and PodRPC is supported:
        delivery = PodRPC
    else:
        return error(UnsupportedDeliveryCapability)

    consumer = ValidateDeclaredConsumer(pod, selectedVolumes, delivery)
    if consumer missing:
        return error(MissingMountConsumer)
    if consumer ambiguous or incompatible:
        return error(InvalidMountConsumer)

    return ExecutionPlan(
        receiver=ClusterDS, mountScope=PodTarget, delivery=delivery,
        provider=consumer.provider, cleanupOwner=verifiedLifecycleContract)
~~~

消费方验证包括约定命令、UID、配置与业务目标卷映射、探针和唯一所有者。RuntimeClass 名不能替代 runtime 能力证据。API 或身份解析失败不能自动当作 runc。

核心区别是“事实与计划”分离：先记录当前部署和请求真正提供了什么，再判断能否组成一个支持的计划；driver 不负责猜测部署类型。

### 5.3 CSI 与执行层

AttachmentPlan 是卷/节点级的部分计划，只决定 controller 是否需要维护独立 FUSE Pod、既有委托动作及策略版本。完整 ExecutionPlan 在 NodePublish 获得 PodInfo 后完成。controller 不能从节点支持 RunD 推断该 Pod 的 runtime，也不能提前确定同卷各 Pod 是手动还是 webhook 提供消费方。两端只比较共同字段，不要求 controller 预知 Pod 级 transport/provider。

~~~text
ControllerPublish(volume, node):
    plan = ResolveAttachmentPlan(volume, node, sharedPolicy)
    if plan.requiresStandaloneFusePod:
        return ExistingFusePodManager.EnsureAndReturnEndpoint(plan)
    if plan is managed by an existing runtime/controller:
        return ExistingControllerAdapter.Publish(plan)
    PersistAttachmentPlan(plan)
    return PublishContext(plan)             # 新 Pod 消费方不在这里同步启动

NodePublish(request):
    plan = ResolvePlan(request.context)
    if plan.receiver == ExistingDelegate or plan requires an opaque old adapter:
        return ExistingDeploymentAdapter.NodePublish(request)
        # 保留 host skip、COCO/rootfs 等旧语义，不强制进入新的构造/交付流程
    ValidateControllerNodePlanAgreement(plan)
    PersistPublicationOwnership(plan)

    mountRequest = BuildMountRequest(request)
    executor = MountExecutorFor(plan.mountScope, plan.delivery)

    if plan.completionPolicy == ExistingSynchronousResult:
        return executor.ExecuteAndWait(plan, mountRequest)

    manifest = ResolveCompleteInventoryForConsumer(plan)
    # 只包含该消费方管理的卷，不包含共享 attach 或其他提供者的卷
    executor.Deliver(plan, manifest, mountRequest)
    return Delivered                        # 尚未启动的业务 sidecar 不能被同步等待

NodeUnpublish(request):
    plan = LoadRecordedPlanOrIdentifyOldDeployment(request)
    return CleanupCoordinatorFor(plan.mountScope, plan.cleanupOwner).Release(plan)

ControllerUnpublish(volume, node):
    plan = LoadRecordedAttachmentPlanOrIdentifyOldDeployment(volume, node)
    return AttachmentManager.ReleaseRecordedResources(plan)
~~~

BuildMountRequest、驱动分派和 PodTarget 执行尽量复用；SharedAttach 与 PodTarget 的挂载位置由计划决定。ExistingSocket 也可以执行 PodTarget，不能把“RPC”默认解释成共享挂载。

SharedAttach 的 Pod 解绑只解除该 Pod 的 bind，不能直接终止其他 Pod 仍在使用的共享客户端；后者由既有 attach/引用生命周期释放。PodTarget 则针对该 Pod 的独立目标清理。取消请求、解除本地路径和确认 guest/runtime 清理结果仍需协调，不能仅调用一个 transport.Cancel 就视为完成。

没有旧记录时需要可验证的兼容识别规则；无法确认所有权就保留并报错，不能同时调用两种清理。执行记录持久化位置、控制器与 DS 的计划一致性检查，是实现前必须明确的接口。

### 5.4 webhook

~~~text
OnPodCreate(pod):
    if webhook disabled:
        admission is not called

    intents = ResolveDSDeliveryIntents(pod.PVCs, sharedDefaultPolicy)
    if no selected volume requires a new Pod consumer:
        return pod unchanged

    ValidateNoExistingProviderConflict(pod, intents)

    if injection opted out:
        ValidateManualConsumers(pod, intents)
        return pod

    return InjectOrValidateDeclaredConsumers(pod, intents)
~~~

不开 webhook 时，DS 执行相同的消费方契约验证，缺失返回 FailedPrecondition。开启 webhook 时，可以提前在 admission 拒绝错误，但不能因此删除 DS 的校验。

## 6. 分支与可见现象

| 条件 | 预期 |
| --- | --- |
| 保持常规 host 部署 | 独立 FUSE Pod、原 socket 与共享 attach 行为保留 |
| 保持 guest agent 部署 | guest mountproxy 可以仍是 sidecar；原本地 RPC 与 PodTarget 行为保留 |
| 保持外部 controller sandbox 部署 | 原 endpoint 提供方式和配置语义保留，不误判为 agent binary |
| 不启用新交付且原 rund 配置报错 | 保留错误，不新增 virtiofs fallback |
| DS→Pod 意图、唯一合法手动消费方 | 新计划使用 PodTarget；RunD 文件、runc 后续 PodRPC |
| DS→Pod 意图、没有消费方 | FailedPrecondition，说明 Pod、卷和缺失契约；不能静默回到共享 FUSE Pod |
| 两个提交者/消费方声称拥有同一卷 | OwnershipConflict，不双重挂载 |
| 仅有外部 socket，提供者/目标契约未知 | 不能据此启用新交付；显式新请求报不兼容/依据不足 |
| 目录、UID、业务目标映射错误 | 可预检的错误提前拒绝；跨 guest 共享不可见时探针失败并记录诊断 |
| RunD 被命名为 runv、rund 或其他 class | 不凭名称分支；验证具体能力，支持的同族实现采用同一执行组合 |
| 第一、二阶段显式要求新增 runc PodRPC | Unimplemented，不暗中切到文件 |
| 新消费者的挂载失败/进程退出 | 初始结果与运行期错误分开；startup/readiness 分别处理，不承诺透明恢复 |
| Pod 被强删、cleanup 无法确认 | 保留 CleanupUnknown/Pending 与独立诊断，不递归删除目标树 |
| 只放入一个等待文件的 sidecar，却没有相应交付契约 | 不隐式改变旧 CSI 执行；消费者缺清单无法就绪，属于配置冲突 |
| 组件策略版本不一致 | PolicyMismatch，不能由 controller 与 DS 各选一个计划 |

错误需说明是哪一维不成立：输入配置、提交所有权、消费方契约、runtime 能力、交付可见性、执行失败或清理失败。例如：

~~~text
Pod request delivery selected for volume <volume>, but Pod <namespace>/<name>
has no matching mountproxy consumer; enable injection or configure a manual consumer
~~~

所有日志、状态与 Events 均保留关联身份，不输出凭据。

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

### 阶段一：DS→手动提供的 Pod mountproxy、rund、HostPath

不部署 webhook，不实现 runc sidecar RPC。先集中验证一条新执行路径和默认分支。

| 用例 | 预期 |
| --- | --- |
| 保持既有部署、未配新消费方 | 与旧环境基线一致，记录是原有成功路径还是原有错误；不新增 virtiofs fallback |
| 要求 DS→Pod 交付、未配消费方 | 明确 MissingMountConsumer；不创建新的挂载消费方，不静默成功 |
| 要求 DS→Pod 交付、手动配置正确 | 完整 manifest、请求、响应均经 HostPath，业务真实 I/O 通过 |
| 手动配置目录/UID/目标错误 | 能识别的配置错误提前拒绝，共享不可见时探针明确失败 |
| 多卷，其中一个缺失/失败 | 其他卷结果仍可观察，业务不启动；清单不因只收到部分请求而缩水 |
| sidecar 重启、DS 重启 | 不重建 live 源目录，旧 session 不误放行，所有权可恢复或明确报错 |
| Job、优雅删除、请求取消 | 明确卸载、状态回写、进程回收及目录保留顺序 |
| 强杀、guest/节点故障 | 记录清理未知与孤儿处理边界，不伪造成功；优先确认 HostPath 挂载残留和源目录引用 |

### 阶段二：增加可选 webhook

验证第 4.2 节完整四格矩阵：是否要求 DS→Pod 交付 × webhook 关闭/开启。另测 opt-out + 合法手动 sidecar、opt-out + 无消费方、webhook 不可用、无匹配 profile、重复注入和错误绑定。

webhook 不可用时：需要自动注入的请求拒绝；符合 admission bypass 的手动配置仍应可运行。不开启自动注入的合法用法不能依赖 webhook 在线。

混合卷必须单独覆盖：同 Pod 一个既有共享 FUSE 卷、一个新的 PodTarget 卷；新 manifest 和注入只管理后者，不能双重挂载，也不能等待其他提供者的状态。

### 阶段三：DS→runc Pod mountproxy RPC

保持交付意图与 webhook 的四格矩阵，为新的 runc PodTarget 计划增加 RPC executor，不改变既有 RPC 路径。验证实际挂载失败结果、启动顺序无死锁、会话与身份隔离、取消、重连、DS/sidecar 重启和结果持久化。

runc 的既有共享挂载仍使用原独立 FUSE Pod。rund HostPath 不因新增 RPC 被迁移。guest csi-agent 与外部 controller 路径每阶段均回归，验证共同执行能力复用而执行权没有变化。

每次实现只跑直接相关的简单代码测试；完整 E2E 等新的环境按上述阶段执行，不提前创建集群。

## 10. 评审需确认的决策

1. **配置语义与命名**：选择的是集群 CSI 到 Pod 的请求交付契约，不是是否存在 sidecar；gate/PV key 正式名字待确认，不直接采用此前二分名称。
2. **缺消费方策略**：显式要求新的 DS→Pod 交付但无消费方，建议直接报配置错误；保持当前部署契约时执行原流程，不将默认与失败后的回退混为一谈。
3. **默认策略来源**：建议相关组件同源配置；若必须只改 DS gate，需先设计异构节点策略与 admission 的协调。
4. **持久化执行计划**：确定 controller/node 的记录位置与旧状态迁移，保证解绑按原所有者执行。
5. **runc RPC 结果契约**：确认接受异步交付加独立结果，还是要求改变启动编排以实现初次 NodePublish 同步挂载。
6. **旧基线与 runtime 能力**：先验证实际 rund 环境的当前部署行为；默认报错是可接受基线，virtiofs 不是新增默认，runv/rund 名称不承担角色或能力判定。
7. **第一阶段准入**：固定完整 sidecar spec 与异常清理仍未解决，不能用已有固定 args/probe 和优雅删除 E2E 代替。

实验实现的历史环境与验证证据见 [既有调研与 Review 说明](rund3-fuse-design-review-zh.md)、[验证记录](rund3-fuse-validation.md)。这些记录不能替代本设计三个阶段的验收。
