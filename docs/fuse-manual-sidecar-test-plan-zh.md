# RunD 手动 FUSE sidecar：生命周期设计与验收计划

状态：验收要求与调研依据。新分支为 `codex/fuse-coco-runtime-split`，基线为 `upstream/master` 的 `3cc89c86b`。当前实现及边界以 [设计文档](fuse-manual-sidecar-design-zh.md) 为准，实际执行结果见 [验证记录](fuse-manual-sidecar-validation-zh.md)，操作见 [使用说明](fuse-manual-sidecar-usage-zh.md)。本文矩阵不代表全部通过。

## 1. 范围与验收边界

只验证 **集群 CSI DaemonSet + RunD + Deployment 自带原生 sidecar + HostPath 文件交付**。先用 ossfs2 验证；ossfs1 复用相同生命周期。此阶段不增加 webhook，不实现 runc sidecar RPC，不改造 csi-agent 或 COCO。

测试时 PV 配置 `spec.csi.volumeAttributes.runtimeclass: rund`，Deployment 配置 `spec.template.spec.runtimeClassName: rund`，不直接配置 `direct`。前者经现有解析逻辑得到内部值 `directAssigned=true`；后者是实际 Pod 使用的 RuntimeClass，两者是不同输入。

只在原内部条件 `directAssigned=true / socket为空 / skipGlobalMount=false` 下增加分流：实际 Pod 的 `spec.runtimeClassName` 为 `kata-remote` 时保留 COCO，否则走手动 sidecar。runv 仅作为 RunD 的测试变体，不用于区分产品模式；当前目标配置使用 rund。

原有 `RundCSIProtocol3=true` 的提前 skip 保留。因此测试安装必须关闭该旧 gate，确保真正走到新增分支。不能把 CSI 提前返回成功当成文件交付成功。

本实验的通过标准不是 Pod Ready，而是以下四项同时成立：

1. 业务容器首次启动前，所有声明的卷均已在业务视图可用；失败原因明确可见。
2. 正常终止、失败回滚、sidecar 重启均不会误卸 kubelet 的底层 PVC 投影，不删除远端对象。
3. 强制终止、节点/guest 故障、CSI 重启下，有明确的异常接管者和可验证的回收结果；证据不足必须保持 Unknown/CleanupPending。
4. 宿主机清理路径不能进入远端持久化数据；如现有 RunD 传播方式不能满足，必须调整数据路径设计，不能靠文档提醒用户不要 rm-rf 来通过验收。

## 2. 固定 sidecar 契约

```yaml
spec:
  runtimeClassName: rund
  terminationGracePeriodSeconds: 90
  initContainers:
    - name: alibabacloud-csi-mountproxy
      image: <aio-mountproxy-image>
      command: [csi-mount-proxy-server]
      args: [--file-mode]
      restartPolicy: Always
      securityContext:
        privileged: true
        runAsUser: 0
      env:
        - name: POD_UID
          valueFrom:
            fieldRef:
              fieldPath: metadata.uid
      startupProbe:
        exec:
          command: [csi-mount-proxy-server, --file-check-ready]
        periodSeconds: 2
        timeoutSeconds: 2
        failureThreshold: 60
        terminationGracePeriodSeconds: 30
      readinessProbe:
        exec:
          command: [csi-mount-proxy-server, --file-check-ready]
        periodSeconds: 2
        timeoutSeconds: 2
        failureThreshold: 1
        successThreshold: 1
      volumeMounts:
        - name: state
          mountPath: /volume-state
          subPathExpr: $(POD_UID)
        - name: data-a
          mountPath: /pod-volumes/<pv-a-name>/mount
          mountPropagation: Bidirectional
        - name: data-b
          mountPath: /pod-volumes/<pv-b-name>/mount
          mountPropagation: Bidirectional
  containers:
    - name: app
      image: <application-image>
      volumeMounts:
        - name: data-a
          mountPath: /data/a
          mountPropagation: HostToContainer
        - name: data-b
          mountPath: /data/b
          mountPropagation: HostToContainer
  volumes:
    - name: state
      hostPath:
        path: /var/run/ossfs/file-volumes
        type: DirectoryOrCreate
    - name: data-a
      persistentVolumeClaim:
        claimName: <pvc-a-name>
    - name: data-b
      persistentVolumeClaim:
        claimName: <pvc-b-name>
```

本实验以两个卷为基本用例：业务容器分别使用与 sidecar 相同的 `data-a`、`data-b` PVC volume，`mountPropagation: HostToContainer`。不能换成 HostPath 别名后只检查 sidecar 自己能否看到文件。完整资源示例见 [双卷 Deployment](../examples/oss/file-sidecar/demo.yaml)。

双卷验收至少包括：两卷成功；A 成功/B 迟到；A 成功/B 永不到达；A 成功/B 挂载失败；运行后单卷 daemon 退出；删除 Pod 时一卷卸载正常/另一卷 EBUSY。每种情况分别记录两个卷的状态与清理结果。后续再验证更多卷及 ossfs1/ossfs2 混合，不以单卷成功作为本实验通过标准。

固定识别名称为 `alibabacloud-csi-mountproxy`，启动 command/args 是推荐配置而非识别条件；probe 和默认目录保持约定。guest 仅允许 RRSA，不交付 CSI Secrets。增减卷仍需修改 Pod 的 volumes/volumeMounts。CSI 从已有 Pod 中的 sidecar 挂载声明生成完整 inventory，不额外查询 PVC/PV，不要求用户维护 expected-volumes 参数。inventory 的 key 是 kubelet CSI 目标目录名；MountRequest.VolumeID 保持真实 volumeHandle。

手动声明的 PVC 名和 mountPath 中 PV 名必须对应。单凭 Pod 对象不能验证 PVC→PV 绑定；完整且一致的 PV/PVC/namespace VolumeContext 元数据可按配置契约免查，缺失时才查询标准 Bound PVC 的 spec.volumeName，成功绑定按 PodUID/namespace/claim 缓存。任何已给字段冲突不使用缓存或 GET 掩盖。分别测试元数据快速路径与兜底的冷/热 API 成本；静态填写及 Retain 重绑定需同步维护元数据，不声称它是实时绑定证明。

## 3. probe 与 hook 的具体职责

### startupProbe

只有完整 inventory 的每个卷均满足以下条件才返回 0：desired 有效、PodUID 相符、generation 匹配、status 属于当前 sidecar session、phase=Mounted、心跳新鲜。不能扫描已经到达的请求后认为“现有这些都成功”即可。

`--file-check-ready` 是一次性检查命令，不发起挂载：上例要求 A、B 两卷全部满足条件才退出 0；任一请求缺失、挂载失败或状态过期则退出 1。startupProbe 用它阻止业务提前启动，readinessProbe 用它持续判断 Pod 是否 Ready；它不保证远端服务始终可读写。

原生 sidecar 的 startupProbe 成功后才放行后续初始化和业务容器。120 秒为本实验默认启动窗口；单卷挂载默认超时 30 秒。最后到达的请求也要有执行时间；压测要检查这两个预算是否合适。达到探针失败阈值时 kubelet 会重启 sidecar，因此必须验证部分成功卷的回滚及重启幂等性。

探针读取本地状态文件，首次成功时持久化 ready-session 屏障；不访问 API server，不执行 stat/list/read 远端 FUSE 路径。退出 1 的输出包含卷 key、phase、脱敏原因。屏障用于禁止业务启动后 sidecar 重启透明重挂：业务 bind 可能仍引用旧 FUSE，必须重建整个 Pod。

### readinessProbe

使用同一命令，持续校验 session/generation/心跳和逐卷状态。进程退出、mount 丢失或心跳过期均使 Pod NotReady。默认心跳 1 秒，过期阈值 10 秒；正常持续检测的目标上界约为 14 秒，需在 CPU 紧张情况下实测。

NotReady 不会停止正在运行的业务进程，也不会撤销已经打开的文件句柄。不得把 readiness 当作 I/O fencing。挂载存在且 daemon 存活也不等于远端可达；远端读写健康由有超时的业务验证或单独隔离的测试进程检查，不能把可能卡死的远端 I/O 放进 exec probe。

必须区分“挂载阶段返回失败”和“挂载成功后首次 I/O 才鉴权失败”。后者是否在 ossfs2 中出现需要实测；不能仅凭 mountinfo 就宣称已验证远端权限。若要求 startup 连延迟鉴权失败也阻止业务启动，需要另行确定与最小权限兼容的 driver 健康操作，并在独立受监督进程执行；父进程超时后仍须处理卡死子进程及挂载引用。当前不能宣称已具备这种保证。

### livenessProbe

本阶段不配置 livenessProbe。不能因为某个远端服务短暂故障而循环杀死整个多卷 sidecar，破坏其他正常卷。进程退出交给 restartPolicy；进程挂死交给 readiness 告警和异常接管，不能把自动重启假定为安全恢复。

### postStart / preStop

不配置 postStart：挂载由文件消费者执行，启动顺序由 startupProbe 控制。

不配置 sidecar preStop：不运行 shell umount、不调用 rm、不重复发起另一套 drain。mountproxy 必须作为容器主进程接收 SIGTERM/SIGINT，使用同一幂等 shutdown 路径。kubelet 正常终止时先停止业务容器，再终止原生 sidecar，但整个 Pod 共用 grace period，业务耗尽预算时 sidecar 可能直接被 SIGKILL。90 秒只是本实验预算，不提供清理保证。

应用需要 flush 时可由应用自身实现 preStop；该耗时计入 90 秒预算。平台不得注入任意业务 shell。实验用应用明确设置可控的正常 flush、延迟退出和不退出三种行为。

## 4. 生命周期与所有权

### 发布与挂载

```text
CSI NodePublish
  复用公共包装层已经获取的 Pod（包括失败结果，不重复 GET）
  校验 PodUID、目标路径、节点和手动 sidecar 契约
  写完整 inventory；原子发布 desired(generation, MountRequest)
  返回交付成功 —— 此时不能宣称 guest 挂载成功

mountproxy
  为完整 inventory 中每个 key 建独立 worker
  收到 desired → Mounting → 调用现有 driver/interceptor
  确认 FUSE 层，而非底层 virtiofs/PVC 投影 → Mounted
  失败 → Failed + 原始 mount-proxy response 的脱敏错误
  持续记录 daemon 退出、mount 状态和心跳

startupProbe 所有卷成功 → 业务启动；否则保留阻塞并显示具体失败
```

CSI 不能同步等待这个 sidecar 的挂载结果，否则 kubelet 尚未启动容器时会形成启动死锁。必须明确区分“NodePublish 成功”和“卷可用”。Pod 外部诊断如何在 sidecar 消失后仍保留失败/清理结果，是验收的一部分。

### 正常清理：sidecar 是 guest 内的优雅清理者

目标顺序：进入 Draining 并停止接受新挂载 → 取消未完成挂载并回收子进程 → 逐卷正常卸载 FUSE 层 → 验证该视图不再有自有 FUSE mount → 结束对应 daemon 并等待退出 → 记录结果 → 退出。

不得卸载底层 PVC/virtiofs 投影。多卷清理并行，单卷卡死不能阻塞其他卷结果。file-mode 为 drain 和 driver 进程回收共用 20 秒退出上限；超时返回未确认错误并退出，不伪造 Unmounted。阻塞的内核调用和最终挂载引用仍由 runtime 负责，退出有界不等于回收有界。

lazy detach 只能记为 Detached/CleanupPending，不能直接记 Unmounted。仍有打开 fd、传播副本或其他 namespace 引用时，原路径消失不意味着资源已回收。必须记录正常 unmount、lazy detach、失败及无法确认四种不同结果。

### 异常清理：沿用 kubelet 与 CRI 的生命周期

sidecar SIGKILL/OOM/guest 崩溃时没有 hook 可执行。kubelet 负责停止容器和 sandbox，runtime 负责释放 guest 的进程、namespace 与 VM；CSI 不增加私有 guest 回收器，也不能仅凭 API NotFound 或心跳超时宣告 guest 消亡。

前版要求 CSI 独立取得 guest 销毁证据后才允许 NodeUnpublish，错误地重建了 kubelet/CRI 已有的责任链，并在消费者已停止时等待 ACK。现已按 v1.33.3 的停止容器后卸载卷的顺序修正；依据、故障注入与边界见[根因记录](fuse-manual-sidecar-rootcause-zh.md#2-无-ack-清理我们把标准生命周期设计错了)。若 runtime 标准停止成功后仍留下存活的 FUSE，应作为 runtime 契约问题记录，不能靠 CSI 猜测 PID 补偿。

### NodeUnpublish 与状态回收

当前协议：NodeUnpublish 尝试提交显式 Unmount 指令，携带 PodUID、卷 handle/target、被撤销的 generation 及 operation ID；活消费者严格验证后执行 drain。ACK 记录优雅清理结果，不是 kubelet 卸载成功的前提。随后只安全清理 CO 指定的 host target；非空或清理失败则返回错误，由 kubelet 重试。损坏/缺失的共享 JSON 不能改变删除范围，也不能阻止合法的 CO teardown。

desired 意外消失不构成卸载授权：记录 RequestMissing 并使 probe 失败，但继续监督已有 daemon，不主动卸载正在服务业务的卷。重新出现的相同 generation 可以恢复状态；不同 generation 必须拒绝覆盖。删除整个配置目录也按异常处理，不能猜测这是 NodeUnpublish。宿主机 root 伪造有效指令不在单纯文件协议的防御边界内。

删除 desired 不是卸载完成。host target 安全清理成功后，只退休对应 UID/key 的已知控制文件；其他卷即使误丢 desired，其 status/tombstone 也保留。使用 os.Root 限制路径，未知文件保留，只 rmdir 空目录，禁止递归删除数据路径。

CSI 启动及每 5 分钟执行本地元数据 GC：仅 kubelet 本地 Pod 目录已不存在时退休孤儿控制文件。该目录存在、检查失败或 pods 根目录不可用时保留，不用 API NotFound 判断。kubelet 自身先排除 desired/running Pod 和残余卷挂载；这里不新增 API watch、CRI 客户端或 guest 存活探测。GC 只收元数据，不替 runtime 管理 FUSE。

## 5. Corner case 矩阵

层级：U=单元测试；I=进程/文件/故障注入集成测试；R=真实 RunD 数据面验证。U/I 能覆盖的不要重复铺大量 E2E；R 只保留 namespace、runtime、kubelet 顺序等无法模拟的事实。下表均为验收要求，不表示已通过。

| ID | 场景 | 必须观察到的行为 | 层级 |
|---|---|---|---|
| C01 | 无 sidecar、普通 init、缺 startupProbe、缺 UID env/state、非 privileged、错误传播方式 | NodePublish 明确拒绝；不产生可执行 desired | U |
| C02 | kata-remote / rund / runv / 未知 class / nil class | kata-remote 保留 COCO；实际 runc/空 class 拒绝文件模式；其他显式 sandbox class 验证手动契约；分类不新增 GET | U/R |
| C03 | Pod GET 失败、Pod 同名重建、UID 缺失、Pod 在其他节点 | 失败关闭，不猜测路径；同一 NodePublish 不重试 GET | U |
| C04 | 多卷、混合 ossfs1/2、同 PV 多 Pod、volumeHandle 与 PV 名不同 | 一套固定 args/probe；请求不串卷/串 Pod，真实 handle 保持不变 | U/I/R |
| C05 | 第一个请求到达后第二个迟到/永不到达 | 第一卷完成也不能放行业务；显示缺失卷 | U/I |
| C06 | 相同请求重试、CSI 超时后重试、并发同卷发布 | 不重复启动 daemon，不产生部分 JSON；generation 稳定 | U/I |
| C07 | 不同请求覆盖同一 target、挂载后修改参数/Secret | 明确拒绝不支持的更新；不能悄悄保留旧配置却报成功 | U/I |
| C08 | PVC 挂载路径配错、两个 mountPath 指向同一声明、PVC/PV 名错配 | 从标准 Bound PVC 校验目标 PV，错配明确失败；数据消费者 init 必须在 sidecar 后 | U/R |
| C09 | JSON 写一半、rename/fsync 失败、磁盘满、只读 state、sidecar 无写权限 | 不执行半请求；CSI/sidecar 给出持久化失败；旧状态不能冒充新成功 | U/I |
| C10 | 畸形 JSON、未知必需字段、generation/UID 错、越界/符号链接路径 | 不执行；状态错误可见；未知可选 MountRequest 字段可保留 | U/I |
| C11 | 持续 I/O 时误删 desired/整个状态目录，再恢复请求 | 不自动卸载；明确告警，不丢 daemon 监督；正常终止仍可清理；新旧指令不串代 | U/I/R |
| C12 | 完整/部分/冲突绑定元数据；前置 init 的重复或不相关 PVC | 完整且匹配时冷/热零 PVC 查询；缺失才兜底，冲突直接拒绝；非首位 INFO，前置 PVC WARNING 数量去重，列出 managed 交集并保持同 PVC 的明确错误 | U/I/R |
| M01 | 错 AK、无权限、错误 endpoint、DNS/网络失败 | 挂载阶段失败时阻止业务启动，原因脱敏可见且进程回收；延迟到首次 I/O 的失败单独记录，不能伪称 mount 探测已覆盖 | I/R |
| M02 | mount 卡住到超时、启动进程后未出现 mount、mount 后立刻退出 | 状态不能误报 Mounted；取消有界，daemon 不遗留 | I/R |
| M03 | 一卷失败、一卷成功、一卷卡住 | 各卷结果独立；Pod 不 Ready；重启/退出时所有已成功卷也处理清理 | I/R |
| M04 | 只有底层 virtiofs 投影、sidecar 能读而业务不能读 | 不误认挂载成功；真实业务容器必须独立验证读写 | I/R |
| M05 | 只读卷、不同 bucket/prefix 的两卷、两个副本 | RO 写失败；写入对象经独立 OSS API 验证；无跨卷/跨 UID 泄露 | R |
| M06 | credential 临时文件、stderr/探针/状态/Event | 状态隔离且权限正确；错误中没有 AK/SK/token；清理后 Secret 文件消失 | U/I/R |
| P01 | 旧 session/旧 generation/旧 UID 的成功 status、时间倒退/过期 | probe 失败；不会因为遗留 Mounted 放行 | U/I |
| P02 | daemon 异常退出、mount 消失、runner 停止更新 | 规定检测窗口内 NotReady，具体卷和原因可定位 | U/I/R |
| P03 | CPU 限流、大量卷、probe 频繁启动 | probe 无 API/远端 I/O；不会因资源不足持续误重启 | I/R |
| P04 | startupProbe 达阈值触发 sidecar restart | 已挂卷清理；新 session 屏蔽旧结果；无双 daemon | I/R |
| T01 | 正常删除、滚动替换、缩容、Job 正常结束 | 业务先退出再清理；退出顺序、结果和耗时有证据 | I/R |
| T02 | 挂载尚未完成时删 Pod、仅部分卷已成功时删 Pod | 取消 in-flight mount，已成功卷全清理，不在 drain 后新增 mount | I/R |
| T03 | 应用 preStop 很慢/不退出，耗尽 Pod grace | 不依赖 sidecar hook 必达；异常接管可判定剩余状态 | R |
| T04 | sidecar SIGTERM、重复信号、shutdown 与请求撤销并发 | 同一幂等 drain；无重复卸载底层投影，无死锁 | I |
| T05 | sidecar OOM/SIGKILL，保留旧 guest/业务进程 | 旧状态过期；重启不盲目叠加挂载；不能无条件继续业务 I/O | I/R |
| T06 | 打开文件、cwd、并发写、其他 namespace 引用导致 EBUSY | 不递归删文件；lazy 只记 Pending；释放引用后可确认回收 | I/R |
| T07 | unmount 返回错误/阻塞、daemon 退出阻塞、状态写失败 | 每卷原因保留；其他卷继续清理；退出有界；不伪报 Unmounted | I/R |
| T08 | desired 删除但挂载仍在、NodeUnpublish 早到/重复/丢失 | desired 撤销不是清理成功；可重试/可接管，不能继续新增挂载 | U/I/R |
| T09 | CSI 在撤销前后重启、清理中崩溃、sidecar 同时重启 | 依据持久状态恢复；不丢清理任务，不误执行旧 generation | I/R |
| T10 | API Pod 已删除但 guest 仍运行；Pod 同名新 UID | 不凭 NotFound 删状态；新旧隔离，旧 guest 仍可被追踪 | I/R |
| T11 | guest/runtime/kubelet 重启、节点失联/重启/恢复 | runtime 销毁与重建证据明确；恢复后对账；无法确认时 Unknown | R |
| T12 | NodeUnpublish 前后 CSI 崩溃、遗留控制状态 | kubelet/runtime 继续标准停止与卸载；本地 Pod 目录消失后 GC 自有控制文件，保留活跃 UID | I/R |
| S01 | host、guest、业务、sidecar 的全部数据挂载视图 | 记录 mount ID、父子关系、传播属性、进程/namespace/沙箱身份 | R |
| S02 | 故障后宿主机清理脚本访问数据目录 | 隔离测试 prefix 的哨兵对象不被删；若能遍历远端，判安全阻塞 | R |
| S03 | 状态目录残留、清理失败、其他 Pod 同 handle | 状态可诊断可控增长；隔离清理不触及其他 Pod/远端对象 | U/I/R |
| F01 | 默认和新路径 API 调用计数，1/10/100 卷 | 分类复用 Pod GET；完整元数据冷/热无 PVC API；缺失元数据每 Pod/claim 冷兜底一次 GET，热缓存无新增查询；无 PV/Node/RuntimeClass 查询；用节点 SA 撤销 PVC 权限验证零查询 | U/I/R |
| F02 | 并发 Pod 启动、DS CPU/内存、state IOPS、probe 开销 | 报告 p50/p95/p99；区分 API、落盘、guest 启动、mount、放行时间 | I/R |
| A01 | RRSA OIDC token 投射、STS 换证、token 轮换和 sidecar 重启 | OSSFS2 自身使用业务 Pod SA 的 RRSA；JSON 不存 token/AK/SK；轮换后持续读写 | U/I/R |
| A02 | RRSA issuer/audience/sub/role 错误、越权 prefix、STS 故障、过期 token | 无节点角色或固定凭据回退；脱敏原因可定位，其他身份/卷不受影响 | U/I/R |

性能验收不能止于 GET 计数。当前逐卷每秒状态持久化和每 2 秒 exec probe 都可能放大节点 IOPS/CPU；需要分别测量 exec 成本、JSON 读取次数及 fsync 次数。desired 的持久化与可过期心跳应区分持久性要求；优化后仍须保证进程崩溃时旧心跳不能误放行。未测量前不声称这套默认频率适用于大规模部署。

## 6. 测试证据与实施顺序

1. U：API 调用计数、分流、UID/路径校验、generation、状态机、脱敏。每个用例固定断言预期 code/phase/副作用，不只断言返回 nil。
2. I：真实子进程 + 可控 mount/unmount backend，注入 mount 超时、子进程退出、EBUSY、磁盘错误、重启和竞态。补少量 Linux mount namespace 测试验证真实 syscall，不能全部用 fake mounter。
3. R：先采集 RunD 的 host/guest mount 拓扑，确认数据安全边界与 runtime 接管接口，再进行真实 OSS 单卷/多卷与故障验证。生命周期 P0 未解决前，不扩大到 webhook/runc。

每个 R 用例保存：脱敏 YAML、镜像版本、时间线、逐卷 desired generation/status/session、sidecar/app 日志、CSI 调用结果、各 namespace mountinfo、相关 daemon PID/退出状态、runtime 沙箱销毁证据、独立 OSS API 哨兵校验和最终状态目录。不能仅贴 Pod Ready 或 CSI 日志。

故障测试只使用独立测试 prefix 和可丢弃哨兵数据。本轮用户明确授权自建隔离集群故障验证，已在该范围内测试强删；不对参考集群或共享业务执行故障注入。

## 7. 当前实现与剩余验证

| 项目 | 当前情况 | 验收前必须补齐 |
|---|---|---|
| runtimeClass 查询 | 新代码复用 Pod，缓存本次 GET 失败；正常/失败路径调用计数测试通过 | 仍需规模性能数据，不能把无新增 API 等同于无额外耗时 |
| 手动 sidecar 与 inventory | 固定配置、多卷、绑定验证和 init 顺序保护已实现并复测 | 完整规模部署成本仍需实际目标环境验收 |
| mount 与 readiness | 已有 worker/session/结果文件和固定命令 | 具体失败探针输出、真实业务视图、挂载和心跳检测边界 |
| 正常卸载 | 已移除 file-mode 的 lazy 成功回退，EBUSY 保持 CleanupPending | 真实 EBUSY/释放及双卷退出通过；全局残余引用证明未完成 |
| 异常停止 | kubelet/CRI 负责，SIGKILL 后强删、CSI 重启、节点重启已验证 | 不承诺所有厂商 runtime 的未测异常行为 |
| NodeUnpublish | 不强制已停止消费者回 ACK，安全 host 清理后退休控制文件 | ENOTEMPTY 等错误仍保留，禁止放松为递归删除 |
| 宿主机误删远端 | 自建版本中 host 不见远端，专用哨兵误删实验保留数据 | 实际目标 runtime 的隔离仍须确认，不作跨版本推断 |
| 外部文件接口 | 无 CSI/FUSE 的原生 HostPath/ConfigMap/projected 复现旧视图 | 运行时维护者处理一致性；RRSA 轮换仍未通过 |
| 完整验证 | 本地回归及多轮真实 RunD 故障验证已有记录 | 未运行的规模和其他故障组合明确保留，不等同于外部阻塞 |

本次已执行 Linux 容器测试及真实 RunD 的部分 R 项。逐项事实见验证记录；不能用本地断言或一次真实运行替代尚未执行的运行时故障、规模和最终清理要求。

## 8. Kubernetes 依据

官方 [Sidecar Containers](https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/) 说明原生 sidecar 的启动门槛和与应用的终止顺序。[Pod termination](https://kubernetes.io/docs/concepts/workloads/pods/pod-termination/) 说明 grace 耗尽与强删时的限制。[Probes](https://kubernetes.io/docs/concepts/workloads/pods/probes/) 说明 startup/liveness 失败的重启行为。以上行为不能单独推导出 guest 挂载引用已清理，RunD 的实际生命周期必须通过 R 层用例验证。

## 9. ACK Fluid internal 分支调研与设计修正

调研基线：已 fetch 的 `origin/internal`，提交 `913f1a50`，提交日期 2026-02-02。此前基于 master/本地旧业务分支的分析作废。未修改 Fluid 原工作区；本节为代码证据，尚无真实 RunD 故障验证结果。

### 9.1 RunD 的数据路径并非普通 CSI 路径

[`mutator_ali_rund.go`](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/application/inject/fuse/mutator/mutator_ali_rund.go#L52) 会：

- 把对应数据 PVC volume 替换为 `emptyDir: {}`。
- 让业务/初始化容器和 FUSE sidecar 引用同一个 volume；同 PVC 多个声明会统一到一个共享入口。
- 业务挂载使用 HostToContainer，并追加 FUSE 目录 subPath。
- 过滤原数据 HostPath volume，改写 sidecar volume 引用。

这里解决的是 Pod 级数据共享与挂载入口组织，不是增加一个异常回收器。被替换的数据卷不再通过该 PVC 的 CSI NodePublish/NodeUnpublish 回调管理；其他仍保留的 CSI 卷不受此结论影响。

[`prependFuseNativeSidecar`](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/application/inject/fuse/mutator/mutator_default.go#L447) 保留 FUSE 容器模板，将它放进 initContainers 并设 restartPolicy=Always。普通 sidecar 也仍支持，不能混为一条生命周期。

### 9.2 到底谁清理：按事件区分

| 事件 | internal 代码能确认的执行者 | 不能推导出的保证 |
|---|---|---|
| 正常删除有 grace 的 Pod | kubelet 调用容器模板的 preStop；FUSE 客户端/脚本执行卸载；runtime 停止容器与 sandbox；kubelet 回收 emptyDir | preStop 一定成功、剩余 grace 足够、所有传播引用已消失 |
| sidecar 自己正常退出 | 镜像/客户端的退出逻辑；有 EXIT trap 的脚本可尝试清理；容器退出后由 runtime 管理 namespace | 进程自己退出会自动执行 kubelet preStop；所有镜像都有 EXIT trap |
| 仅 kill 某个 FUSE daemon，sidecar/guest 尚在 | 存活的进程管理器/客户端 helper 可能处理；动态挂载示例使用 supervisor，特定 passthrough 使用 auto_unmount | supervisor 重启或 FUSE 连接断开等于所有旧挂载引用已解除 |
| sidecar 容器整体 SIGKILL/OOM，guest/业务仍在 | hook/trap 不能依靠；runtime 终止进程；原生 sidecar 可被重启 | 容器重启前旧 mount 已清理；共享 guest 中其他 namespace 没有引用 |
| 强删且 kubelet 可达 | API 先删除对象，kubelet/runtime 执行实际终止；随后 kubelet 做卷回收 | API 删除响应等于容器/guest 已停止 |
| 强删且节点失联，或 runtime 清理失败 | Fluid AppController 没有这条 RunD 孤儿 guest 的专用清理路径；最终依赖节点/runtime 恢复、重试或人工处理 | Pod NotFound 可作为最终清理成功凭据 |

preStop 来自实际 runtime/client 模板，不是 RunD mutator 生成一个通用清理脚本。例如：

- [Thin 默认 lifecycle](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/ddc/thin/transform_fuse.go#L162)：`umount <target>`，允许 profile/runtime 覆盖。
- [JindoFSx chart](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/charts/jindofsx/templates/fuse/daemonset.yaml#L81)：调用镜像内 `/prestop.sh`。该脚本正文不在本仓库，不能凭 chart 证明镜像内部的所有卸载行为。
- [动态挂载示例](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/addons/dynamic-mount/base/entrypoint.sh)：entrypoint trap SIGTERM；[prestop](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/addons/dynamic-mount/base/prestop.sh) 按 mountinfo 卸载后仅尝试 rmdir；[mount-helper](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/addons/dynamic-mount/base/mount-helper.sh) 注册 SIGTERM/EXIT trap，但部分 umount 使用 `|| true`。这是特定 addon 示例，不能当成所有 Fluid RunD 镜像的统一实现。

`auto_unmount` 只在上述特定 passthrough 示例明确出现。对应 [libfuse 3.16.2](https://github.com/libfuse/libfuse/blob/fuse-3.16.2/lib/mount.c#L297) 依靠 helper/通信 fd 安排自动卸载，不是 Kubernetes 的最终回收确认。杀单 daemon 与杀整个容器/guest 必须分别测试，不能预设 helper 在两者中都存活且完成卸载。

### 9.3 controller 和 CSI 不是 RunD sidecar 的通用兜底

[`ShouldInQueue`](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/ctrl/watch/pod.go#L94) 忽略 delete 事件，跳过 Pod restartPolicy=Always，并只遍历普通 Containers 的状态；它不是监控原生 init sidecar 的异常清理器。符合条件的普通 sidecar 场景，由 [FluidAppController](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/controllers/v1alpha1/fluidapp/implement.go#L57) exec preStop 或 umount；容器消失后无法靠 exec 清理。其实现也将特定 exit 137 忽略，不能拿返回成功证明引用归零。

普通 CSI 的 [NodeUnpublish](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/csi/plugins/nodeserver.go#L204) 按 target 加锁、逐层卸载、清理错误返回重试；[FuseRecover](https://gitlab.alibaba-inc.com/eml/ack-fluid/blob/913f1a50/pkg/csi/recover/recover.go#L239) 共用锁避免与卸载竞态。它们处理 CSI host target 和 bind 恢复，不构成已替换成 emptyDir 的 RunD 数据卷的 guest 清理链。

### 9.4 emptyDir 不能独立解决宿主机误删远端数据

**源码结论：emptyDir 提供 Pod 生命周期内的目录，不提供“递归删除时不跨 FUSE”的保证。** Kubernetes [v1.33.0 empty_dir.go](https://github.com/kubernetes/kubernetes/blob/v1.33.0/pkg/volume/emptydir/empty_dir.go#L462) 的默认 medium teardown 最终调用 `os.RemoveAll(dir)`；memory medium 先卸载其自身再删除，也不能拿来证明所有 FUSE 别名/引用都已消失。

如果宿主机清理进程的视图仍能通过该目录或子目录进入一个活的、可写的远端 FUSE，递归删除就仍可能删除远端对象。这是条件性风险，不代表每次 kubelet 清理都会发生。标准 [Bidirectional 传播语义](https://kubernetes.io/docs/concepts/storage/volumes/#mount-propagation) 允许容器 mount 返回宿主机；卷名为 emptyDir 不会自动改变传播规则。

RunD 存在 guest kernel 与 host kernel 边界。若 FUSE 仅存在于 guest，host 只看到底层本地 emptyDir，且 runtime 没有将远端视图重新导出到 host，那么 host 清理不应进入远端 FUSE。**这是需要真实 runtime 验证的隔离条件，不是 ACK Fluid 的 Go mutator 能证明的事实。** guest 内 Pod 子容器共享与 host 视图的区别必须同时取证。

因此不能仅把当前 PVC 替换成 emptyDir 就宣称风险解决。这样还会移除当前配置生产者依赖的 CSI NodePublish 触发条件。若要借鉴，需要同时设计“保留 CSI 交付触发、guest 内独立共享数据卷”的映射，或引入另一配置生产者；目前不在未经验证时直接切换实现。

### 9.5 对本方案的明确约束

1. HostPath 仅承载 PodUID 隔离的控制文件；数据传播是否允许到 host 单独验证。
2. 正常回收由 mountproxy SIGTERM drain 承担；异常回收不能依赖 preStop、auto_unmount 或 NodeUnpublish 任一单点。
3. 误删 desired 不触发卸载；使用显式、可确认、匹配 generation 的撤销指令。
4. CSI 不递归删除数据；标准 kubelet/CRI 负责容器、sandbox 与卷停止顺序。CSI 不能承诺它不具备的 guest 卸载能力。
5. 显式区分清理成功、失败、Detached 和 Unknown；参考 Fluid 的锁/重试，但不能照搬其忽略错误的分支。

实测新增对照组：现有 CSI PVC 数据入口与 Fluid 风格 emptyDir 数据入口；每组验证双卷正常退出、单 daemon kill、容器整体 kill、Pod 强删、guest 销毁、旧 UID/新 UID 并存，以及 host 的独立读写/哨兵删除。先验证只读拓扑和隔离测试 prefix，再触碰测试数据；禁止在未知生产路径上 rm-rf。

## 10. 本轮实际验证与交付要求

完整故障验收只在自建的独立 ACK 集群执行，资源已释放。用户后续授权且权限放行后，在实际 rund 环境独立 namespace 补充了无 CSI/FUSE 的原生文件一致性小实验；不更改共享 CSI、现有业务或节点配置，不执行强删/节点重启。实验范围与结果见根因记录第 8 节。不同环境的 runtime/配置、guest 文件系统和传播行为必须分别记录，不能仅比较 RuntimeClass 名字，也不能把原生文件通过当作完整 RRSA/FUSE 验收。

鉴权使用 **业务 Pod ServiceAccount 的 RRSA**：显式 projected OIDC token，由 FUSE 客户端读取并刷新；不使用节点身份，不让 CSI 把一次性 STS 凭据落进 desired 代替 RRSA。必须验证 issuer/audience/sub/role/prefix 限制、token 轮换和 sidecar 重启。

最终交付三份相互链接的材料：设计文档（已验证的协议和责任边界）、验证记录（每个坑的重现/修复/复测证据及未覆盖项）、使用文档（RRSA、双卷手动 sidecar、固定 probe、故障定位和安全清理）。涉及特殊设计的每一点都要有对应测试记录；不能只交正常挂载演示。
