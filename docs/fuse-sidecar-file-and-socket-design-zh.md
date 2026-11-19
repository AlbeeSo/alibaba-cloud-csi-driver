# RunD file 与 RunC socket sidecar 方案

更新：2026-11-19。本文记录当前讨论达成的分流原则和后续设计边界，不代表两种模式均已实现。RunD 现有实现继续在当前 PR review；RunC socket sidecar 后续使用独立分支/MR，不把两套入口合并重构。

## 实现状态

| 项目 | 状态 |
|---|---|
| RunD 手动 native sidecar、file 请求、多卷 inventory、probe、回收 | 已实现；详见[现有设计](fuse-manual-sidecar-design-zh.md)和[验收记录](fuse-manual-sidecar-validation-zh.md) |
| guest file 仅 RRSA、固定 sidecar 名称 | 已实现，提交 a441f4830；Linux OSS/filevolume 测试、集测与 CI 通过；本次命名/鉴权更新未重跑云 E2E |
| RunC 业务 sidecar、socket 异步交付 | 待实现，本文是设计约定 |
| 新 SC 参数、controller 跳过建 FUSE Pod | 待实现；参数名称尚未确定 |
| 首位 sidecar 正常路径免额外配置、免 PVC GET | 已达成要求，尚未实现；当前代码缺少完整绑定元数据时仍可能 GET PVC |
| webhook | 后续可选能力，当前未实现 |
| sidecar 的 fd-passing/recovery | 不属于首期能力，未实现 |

这里 RunC/RunD 是讨论两条执行路径的称呼，不要求集群存在名为 runc 的 RuntimeClass。Pod runtimeClassName 为 nil 只表示默认 handler，不能据此断言共享宿主机内核。

## 共同契约

一个 Pod 由一个 native sidecar 管理多个 FUSE 卷，名称固定为 `alibabacloud-csi-mountproxy`。按名称识别，不解析启动 command/args 或镜像名称；有该名称但结构不合法时明确报错，不当成“没有 sidecar”回退。

结构包括 restartPolicy=Always 的 init container、所需 probe、UID 隔离和数据/控制挂载。推荐放 initContainers 首位，startupProbe 等全部期望卷成功才允许后续 init 和业务启动；不能只看“目前已收到的卷”就放行。增加卷修改 volumes/volumeMounts，不要求用户修改 expected-volumes 或启动参数。webhook 和用户手工配置遵守同一契约。

名称和结构校验用于识别与配置诊断，不证明容器可信。guest file 仅支持 RRSA，使用业务 Pod projected token 在 sidecar 内换证，CSI 不交付 JWT、STS AK 或 Secret。RunC socket 允许 RRSA；Secret 引用必须通过业务 Pod 显式声明并挂载的 Secret volume 交付，不因为保留名称而向用户容器发送 CSI 持有的 Secret。具体 Secret 文件格式和刷新契约留待 RunC 实现设计。

共同复用 mountproxy 的挂载请求字段、错误含义和 FUSE driver 执行能力，保持卷标识与路径语义，不用重新定义一套简化挂载参数。当前 mountproxy 是 Unix socket 上的自定义 JSON RPC，不是 gRPC；本文的 socket RPC 指沿用该协议，不包含改造为 gRPC。

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
| 是 | 是 | 跳过创建独立 FUSE Pod | 使用 Pod 专属 socket，异步交付 |
| 是 | 否 | 跳过创建独立 FUSE Pod | 明确 FailedMount，不回退 |
| 否 | 是 | 按原逻辑创建独立 FUSE Pod | 使用 Pod 专属 socket，异步交付 |
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
        socket = PodUID 隔离的 socket
        准备 Pod 独占目标并持久提交执行任务
        异步通过 socket 交付
        return deliveryAccepted
    if requireSidecar(req.VolumeContext):
        return error("required mountproxy sidecar is missing")
    socket = 现有 controller PublishContext 的 socket
    return existingSharedGlobalMountFlow(req, socket)
```

`directAssigned=false / socket非空 / skipGlobalMount=false` 仍是这次讨论的既有 RunC 入口，不再额外查询 RuntimeClass 或依赖 nil 判断实际运行时。sidecar socket 必须由 node 根据 Pod 隔离约定确定；controller 跳过创建时不会提供它。地址已知不意味着当时已可连接。

同一入口后仍需区分挂载范围：独立 FUSE Pod 使用现有共享 globalmount；业务 sidecar 使用 Pod 独占目标与生命周期，不能只替换 socket 地址后继续走共享挂载。skipGlobalMount 不新增为要求用户配置的全局 env；新 key 的 controller 行为和 node 的消费者选择也不能合并成“开 key 才识别 sidecar”。

这条路径不根据 RuntimeClass 名称保证 socket 可达。实际运行在隔离 guest 时，若 UDS 不可达，应产生可观测失败，不能静默切成 file；RunD file 使用自己的明确入口。

## 首次发布与异步完成

标准 kubelet 在启动 Pod 容器之前等待卷发布。因此两条新 sidecar 路径都不能让首次 NodePublish 同步等待同 Pod 的 mountproxy 完成 mount：

```text
NodePublish 校验、准备投影、接受任务
  → 返回成功（交付成功，不是远端已挂载）
  → kubelet 启动首位 native sidecar
  → sidecar 消费 file 或 socket 请求并执行 mount
  → 全部期望卷成功，startupProbe 通过
  → 后续 init 和业务启动
```

参考 [Kubernetes v1.33.3 SyncPod](https://github.com/kubernetes/kubernetes/blob/v1.33.3/pkg/kubelet/kubelet.go#L1892)。socket 的执行 response 更直接，但不能回填已经结束的首次 NodePublish。需要保存独立结果，供 probe、日志和后续诊断使用。RPC response 丢失也不能证明 mount 没发生，重投递必须幂等。

CSI 同步校验失败会使 NodePublish 返回错误，用户看到 FailedMount；已接受任务后的 daemon 挂载失败则由 startupProbe 阻塞后续容器，暴露 probe 错误、sidecar 日志与逐卷结果。不能把后一种情况一概称为首次 FailedMount。业务放行后的失败还需 readiness/状态反映，不能把一次 Ready 永久缓存。

GCS FUSE 采用 CSI 先建内核 FUSE 挂载、再向 sidecar 传 fd 的方案，不是同步等待 sidecar；其机制见[官方设计说明](https://github.com/GoogleCloudPlatform/gcs-fuse-csi-driver/blob/main/docs/known-issues.md#the-sidecar-container-mode-design)。本方案不以客户端支持接管 fd 为前提，sidecar 直接执行 mount。相应地，不能天然获得“daemon 尚未接管时访问只阻塞、不落空目录”的属性，必须守住启动顺序屏障。

## 生命周期和清理边界

| 场景 | RunD file 现有原则 | RunC socket 必须落实的要求 |
|---|---|---|
| 多卷/部分失败 | 独立 worker 与结果，全部成功才放行 | 相同屏障和逐卷隔离，不用一个 RPC 成功代表全部成功 |
| CSI 重启 | 请求文件保留，恢复与幂等消费 | 待投递任务与结果可恢复，不能只保存内存队列 |
| 断连/超时 | 心跳/状态超时不等于挂载已撤销 | RPC 断连结果可能未知，使用请求身份确认，不能盲目另起 daemon |
| 正常退出 | native sidecar 接收 SIGTERM drain，EBUSY 明确保留待清理 | 保持明确卸载与进程回收，验证 host 可见挂载清理 |
| Pod 强删/sidecar SIGKILL | 不等已停止 sidecar ACK；kubelet/CRI 与 NodeUnpublish 兜底 | 同样不能依赖 RPC 回复完成回收；旧任务不能复活 |
| 已放行业务后 daemon 失败 | 不透明重挂冒充修复既有业务 bind | 首期同样明确失败；恢复语义另行设计 |

控制文件或 socket 的删除都不等价于卸载。路径要按 PodUID 隔离；删除一个 Pod 不得卸载其他消费者的挂载。只清理自己明确拥有的挂载/控制对象，不对可能承载远端数据的目标执行递归删除。

RunD 已有测试结果不能替代 RunC 验收：runc 的 mount namespace、传播与宿主机残留不同，必须单独验证。RunD 现有回收细节和局限继续以[根因文档](fuse-manual-sidecar-rootcause-zh.md)为准，不因换成 socket 就宣称增强了清理保证。

## 开销要求与尚待落地的细节

正常首位 sidecar 应无需额外绑定元数据、无新增 PVC GET。非首位但前序 init 不挂 PVC，也不因顺序检查查询；只有前序存在 PVC 且需要确定交集时，才考虑已有元数据优先、查询兜底。这个顺序检查目标与当前独立绑定校验的实现不同，尚待调整；跳过绑定校验也意味着失去当前针对手工交换 PV/PVC 映射的额外保护，不能同时宣称两者均保留。

RunC 开发前需补齐具体契约，但不重新讨论已定的 file/socket 选择：

1. 新 key 的正式名称、布尔解析、错误提示与参数保留测试。
2. Pod 专属 socket 路径、共享方式、连接方向、重连和旧 socket 清理。
3. 持久任务的 UID/卷/generation 身份、去重、结果丢失后的确认和 CSI 重启恢复。
4. 固定 probe 如何知道完整卷集合、如何查询异步结果；不要求用户按卷修改参数。
5. privileged sidecar 的目标准备和挂载传播，以及 RRSA/Secret volume 的具体接入。
6. 与现有卷级操作锁、独立 FUSE Pod controller 回收和共享 globalmount 的隔离。

首期 RunC 验收至少覆盖上表四种分流、同名错误配置、nil RuntimeClass 不引入额外请求、双卷与不同 driver、Secret/RRSA、socket 延迟/断连/response 丢失、CSI/sidecar 重启、部分失败、正常删除/强删/挂载中删除、两个 Pod 共用 PV 的隔离、远端哨兵保留和无残留挂载。不能以“能读文件”代替生命周期验收。

fd-passing/recovery 留作独立能力设计：普通文件不能传递已打开 fd，Unix socket SCM_RIGHTS 可在同内核进程间传递；跨 guest 不可直接套用。recovery 还取决于谁持续持有原 FUSE connection，不能把重挂载或持久化 JSON 当成恢复。RunC 本期保留 socket，为未来能力留空间，但不承诺现有 sidecar 已支持。
