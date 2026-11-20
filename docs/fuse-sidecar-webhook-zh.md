# 可选 FUSE sidecar 注入

当前代码已有本地 HTTP admission、双卷、幂等、精确名称和错误分支测试；集群 E2E 尚未完成，本文不代表已完成验收。

## 三个独立决定

1. PV 的 `spec.csi.volumeAttributes.mountMode: sidecar` 阻止 controller 创建独立 FUSE Pod，静态卷不需要 StorageClass 或 CreateVolume。动态卷可在 SC 的 `parameters.mountMode: sidecar` 设置，由 CreateVolume 透传到 PV。ControllerPublish 与 NodePublish 都以 PV 的 VolumeContext 为准，不额外 GET SC/PVC。要求 sidecar 却没有消费者时 NodePublish 报 FailedPrecondition。
2. 安装 webhook 并在业务 Pod 设置 `csi.alibabacloud.com/inject-mountproxy: "true"`，请求生成消费者。Deployment 写在 Pod template annotation。未安装 webhook 的 annotation 不会自行生成容器。
3. node 根据最终 Pod 中精确保留名称选择消费者，并校验身份、卷投影、probe 和权限；不信任 annotation、镜像名称或 command 作为凭据授权。

手工模式不依赖 webhook。正常首位 sidecar 不依赖额外元数据，不 GET PVC。webhook 在 admission 为每个唯一 PVC GET PVC/PV，以获得既有协议要求的 `/pod-volumes/<PV-name>/mount`；不 GET Secret、不执行 mount。

## 安装

chart 的 `fuseSidecarWebhook.enabled` 默认 false。开启时必须提供：

```yaml
fuseSidecarWebhook:
  enabled: true
  tlsSecretName: mountproxy-admission-tls
  caBundle: <base64-encoded-CA-certificate>
  ossImage: <image-with-compatible-mountproxy-and-OSS-clients>
  customImage: <image-with-compatible-mountproxy-and-custom-entrypoint>
  allowPrivileged: false
```

预创建标准 TLS Secret，证书 DNS SAN 包含 `csi-fuse-sidecar-webhook.<安装namespace>.svc`。Secret 更新后每次 TLS 握手重新加载证书；CA 轮换还需更新 caBundle，建议先同时信任新旧 CA 再换证书。chart 不生成私钥、不读取云凭据。webhook Deployment 使用 controller 镜像，但只启动 HTTPS admission，不初始化 CSI/cloud 服务。

Kubernetes >=1.30 的 matchConditions 在 apiserver 侧只选择显式 annotation；未请求注入的 Pod 不受 webhook 停机影响。请求注入时 failurePolicy=Fail，超时 5 秒，内部 API 预算 4 秒。RBAC 仅 GET PVC/PV，没有读取 Secret 或修改 Pod 的权限。

## 注入结果与限制

注入一个首位 Always init sidecar，固定 args `[--file-mode]`，Pod UID env、隔离控制目录、多卷投影以及 startup/readiness probe。增加卷不改变 args。startupProbe 每 2 秒检查完整卷集，连续失败预算约 300 秒；readinessProbe 一次失败即撤销 Ready。无额外 preStop shell，SIGTERM 进入 mountproxy drain，Pod terminationGracePeriodSeconds 必须覆盖约定的 20 秒内部清理预算。

PV 必须明确声明 `runtimeclass: runc` 或 `rund`，不能从 Pod 默认 RuntimeClass 猜内核能力。RunD Pod 还需显式 guest RuntimeClass；`kata-remote` 保留 COCO。RunC OSS2 注入非特权、HostToContainer 的 fd 消费者。RunD、OSS1、customfuse 的直接挂载需要显式 `allowPrivileged: true`，否则 admission 报错；业务应用容器无需特权。

一个消费者不能混用 fd/direct 能力、runc/rund 或 OSS/customfuse，当前明确拒绝这些组合。已有合法手工消费者不覆盖镜像或入口；非法同名容器、多候选、重复 PVC 或生成卷名冲突明确报错。reinvocation 不重复注入，并再次校验其他 webhook 对最终顺序的影响。

只支持已绑定 PVC。未绑定卷（包括 WaitForFirstConsumer）在 admission 明确拒绝；不会等待绑定，也没有后台自动重试。需先使用可预绑定的卷或手工消费者路径，不能通过延长 webhook 超时解决调度循环。混合 Pod 中的未绑定非 FUSE PVC 也受此限制，因为当前实现需要先解析绑定才能确定 driver。

RunD 只允许 RRSA；注入 audience=sts.aliyuncs.com 的目录型 projected token，轮换由 kubelet/runtime 提供。RunC 可用 RRSA 或 `secretRef`，后者注入同 namespace 的只读 Secret 目录 `/volume-secrets/<secretRef>`，不复制 Secret 内容。customfuse 入口负责使用 `rrsa_token_file` 或 `credential_dir`，镜像必须包含实际客户端，不能将 Pod Started 当成鉴权通过。

## fd 生命周期

RunC OSS2 的请求和状态走 file，只有 fd 经 Unix socket SCM_RIGHTS 交接。CSI 在 NodePublish 时创建普通目标目录、请求和延迟 fd offer，随后返回。sidecar 已启动并请求 fd 后，CSI 才创建内核挂载并交付连接；完成 FUSE_INIT 后 probe 才放行业务。内核挂载和 daemon 执行失败都是异步失败，显示在 sidecar 日志和 probe/Pod 状态，不保证出现新的 CSI FailedMount。

不能把旧同步 fd 流程直接挪到 NodePublish 返回之前：本轮真实 RunC 实验已看到 containerd 工作线程卡在 FUSE stat/getattr，导致接管 fd 的同 Pod sidecar 根本无法创建。管道模拟 fd 的本地集成测试不能暴露这个循环。延迟 offer 修正后，实际双卷传播、FUSE_INIT、读写及只读访问已通过；消费者非特权、drop ALL capabilities、无绑定元数据，且 CSI node PVC 查询权限禁用。

recovery 复用已有 OSS2 supervisor 和内核 flush 控制，仍需要可写的 `/sys/fs/fuse/connections` HostPath，只有 fd 并不够。手工配置缺失时 NodePublish 明确拒绝；webhook 在 PV `recovery: "true"` 时生成该投影。这个目录是节点 FUSE 连接控制权限，虽不要求容器 privileged/SYS_ADMIN，仍应由可信 mountproxy 独占，不能等同于完全无宿主机控制权限。节点内核不支持时仍按已有能力探测降级并暴露 recovery_degraded。若只通过全局 recovery FeatureGate 开启而未声明 PV 参数，手工模板或 webhook 配置仍须满足同一投影契约，不能隐式获得权限。

非特权 sidecar 退出时停止本卷 daemon、禁止本卷 recovery、等 supervisor 关闭 fd，再写 `Released`。它不能 umount，不写 `Unmounted`。宿主机 NodeUnpublish 负责实际卸载、删除控制文件；普通 guest/direct 模式仍在成功 umount 后写 `Unmounted`。两种确认都校验 generation、PodUID、执行 session，不能借旧状态确认新请求。

丢失 NodeUnpublish 时，控制 GC 仅在本机 kubelet Pod 目录消失后关闭残余 pending fd 并删除已知平面控制文件。不存在递归清理数据挂载点的逻辑。Pod 强删不等于 runtime 已退出；无法确认的清理保持可观测，不宣称 runtime 故障下一定没有残留。CSI 重启若丢失 pending fd 且已有内核挂载，拒绝盲目创建新连接，要求重建 Pod；daemon recovery 不等于 CSI/sidecar 进程重启恢复。

## OSS 客户端指标兼容

现有 OSS 客户端镜像从 `run/fuse.ossfs/<key>/globalmount` 字符串推导 `/var/run/ossfs/<key>` 指标目录，直接传 `/pod-volumes/<PV>/mount` 会导致客户端找不到 mount_point_info，只有 mountproxy 生命周期指标正常。

file adapter 保留 MountRequest.Target 和生命周期身份。开启客户端指标时，同一 PVC 额外投影到 `/run/fuse.ossfs/<PV>/globalmount`，OSS 子进程使用该路径；数据路径不能用软链接代替，因为客户端会规范化路径。webhook 自动添加第二个 volumeMount；手工模板需声明同一投影，也可通过 `use_metrics=false` 关闭客户端 IO 指标，生命周期指标不受影响。只有 `/var/run/ossfs/<PV>` 指标目录使用容器内软链接，指向 UID 隔离目录里的本卷 metrics。没有新增 hostPath 或特权，JSON 目标协议不变。内部 MountOperation.ClientTarget 仅用于客户端 argv，恢复连接选择和卸载仍使用原始 Target；socket 模式不设置这个字段。自定义客户端不被套用 OSS 的路径规则。

单元/集成回归已覆盖双卷隔离、幂等、冲突拒绝和原始目标不变。真实 guest/direct 客户端已产出逐卷 IO 指标。原生 FD 的两个旧 ACK 镜像可读写但指标落根目录；2026-11-20 已确认 SDK 错把 libfuse 设备路径传给指标解析器。客户端修复分离逻辑路径，FD/direct 两种本地真实 FUSE 双卷集成和新镜像在 Kubernetes 的真实 OSS/RRSA 双卷、逐卷 IO/恢复计数及 Prometheus 均已验证；根目录无共享指标。详见[最终验收记录](fuse-sidecar-unified-results-zh.md)。


## 恢复与节点重启的使用边界

OSS2 daemon recovery 不承诺所有旧业务文件 FD 透明继续使用：遇到 ESTALE/EBADF 时应用应关闭并重开；不要删除 SDK 的旧 inode/句柄检查。完整节点重启后应等待 Node 新 boot ID、容器新实例及新 IO，旧 Ready/历史日志不能作为恢复成功依据。Pod 终止期间 CSI 拒绝新挂载，使用可重试的 Aborted 保留 kubelet 的卸载元数据；用户仍通过标准 Pod 删除/重建操作，不手工删控制文件或数据目录。
