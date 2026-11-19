# FUSE sidecar 本轮实现与验收记录

日期：2026-11-19。本文记录统一功能 PR 的当前证据，不将[验收计划](fuse-sidecar-unified-acceptance-zh.md)中的 86 组自动视为全部通过。设计见[交付方案](fuse-sidecar-delivery-design-zh.md)，注入、权限与生命周期契约见[使用补充](fuse-sidecar-webhook-zh.md)。真实目标 rund 的 RRSA 和 native FD 的客户端逐卷 IO 指标仍有缺口，当前不能作为完整生产验收通过的结论。

## 环境与可信边界

本轮使用独立创建的 ACK v1.33.3、一个安全沙箱节点池、非托管 Helm CSI。相同节点分别使用共享内核 runc 与 rund RuntimeClass；后者的实际 handler 为 runv 2.2.4，仅作为 RunD 家族测试变体。没有根据 runv 字符串选择另一套产品逻辑。

测试使用自己的私有 bucket、受限 `run/*` 权限、业务 Pod SA 的 RRSA 身份，以及独立镜像 namespace。CSI node 的 PVC get/list/watch 权限被移除，验证首位 sidecar 的真实正常路径无需 PVC GET。独立 FUSE Pod 回归额外允许同一测试角色信任 CSI 专用 SA，仍仅能访问测试 prefix。

实际目标 rund 参考集群的 RRSA 仍等待管理员侧 IAM 条件；不能用本轮 runv 证据代替。资源标识、凭据、原始日志、JSON 与 kubeconfig 均不进入版本库。

## 本地与 CI

- 最新功能源码执行 Linux `go test ./pkg/... ./cmd/...` 全部通过。
- proxy 文件协议、真实 Unix SCM_RIGHTS、进程退出与 drain、OSS/customfuse node/controller、webhook HTTP/mutation、监控 collector 均有测试。真实内核 mount 需要集群或有能力的 Linux 夹具，普通单测不伪造该能力。
- 首位 1/10/100 卷直接调用包装后的 NodePublish，禁止 PVC API；非首位前序 PVC 的元数据优先及查询兜底另测。
- 静态 PV 无 SC、动态 CreateVolume 透传都验证 `mountMode: sidecar` 跳过独立 Pod 创建；controller 无可用 Pod manager，不能误走建 Pod 后仍算通过。
- UID 重复/伪造、大小写冲突、Secret 目录结构、缺失 fusectl、旧 generation、退出与指标目录退休竞争有回归。
- 相关 proxy/metric/filevolume 的 race 测试通过；Helm 实际渲染测试通过。CI 结果以 PR 当前 head 为准，不能沿用旧 commit 的绿色状态。

## 真实集群结果

| 项目 | 本轮实际断言 | 结果 |
|---|---|---|
| RunD OSS2 RRSA 双卷 | 业务读取、写回、第二卷只读，固定 args，禁用 node PVC API | PASS |
| RunD 混合 OSS1/OSS2 | 一个 mountproxy 管两个不同客户端，RRSA、只读 | PASS |
| RunC OSS2 fd 双卷 | root UID、非 privileged、drop ALL、无提权，业务可读取两卷 | PASS |
| RunC SecretRef | 显式只读 Secret volume、短期 STS、双卷可读；desired JSON 无凭据值 | PASS；不代表凭据轮换已完整验收 |
| 同节点两 Pod 共用两 PVC | 强删一个后，另一个两卷保持 Mounted，原数据及新写哨兵可读 | PASS |
| RunC daemon recovery | 真正终止一卷 daemon、恢复计数增长，同 Pod 两卷可读 | PASS |
| customfuse guest/native | 固定 custom 名称、自定义入口执行真实 OSS2 客户端、RRSA 双卷 | PASS；不是任意第三方客户端兼容性证明 |
| native OSS webhook | 真 TLS admission、首位 Always init、双卷、后序 init 使用存储 | PASS |
| guest custom webhook | 真正注入 custom 名称、业务双卷读取 | PASS |
| 卷提示和注入四组合 | 有提示/无提示 × 请求注入/不请求注入；手工消费者另测 | PASS；缺消费者为预期失败，无提示仍可能创建多余独立 Pod |
| 缺少消费者 | 有 mountMode 无 sidecar，NodePublish FailedMount，业务未启动 | PASS |
| 异步挂载失败 | 第二卷无权限 prefix，状态 Failed、startupProbe Unhealthy、业务未启动 | PASS；没有新的 FailedMount |
| desired 被删/恢复 | Ready 撤销但挂载与数据保留，恢复同一配置后重新 Mounted | PASS |
| 无 recovery daemon 强杀 | Failed、Ready 撤销、另一卷仍可读 | PASS |
| mountproxy 主进程强杀 | 确认退出 137、Ready 撤销、kubelet 重启因失联挂载点 stat 报错 | PASS；要求重建 Pod，不宣称可原地恢复 |
| 正常删除 guest custom | 实际捕获 Unmounted，随后 Pod、UID 控制目录、host UID 挂载消失 | PASS，约 14 秒 |
| 部分失败后强删 | 已成功卷也清理，捕获 Unmounted，UID 控制/host 挂载消失 | PASS，约 3 秒 |
| native 普通/强删及 custom native 强删 | Pod 消失后继续检查 UID 控制目录与宿主机 mountinfo | PASS |
| native 主进程/daemon 异常后的强删 | 两类异常 UID 的 host 挂载与控制目录均消失 | PASS，分别约 5 秒 |
| 早期 eager-fd 失败实验 | 两个旧 UID 的 host 挂载和控制目录确认消失 | PASS；不是靠最终销毁节点证明 |
| 远端数据安全 | 清理后通过独立 OSS API 再读取原始两个哨兵 | PASS；不是 FUSE 缓存读 |
| 真实监控 endpoint | HTTP 抓取 CSI Prometheus endpoint，存在 FUSE 样本 | PASS；不能代替逐卷 IO 验收 |
| guest/direct 客户端 IO | 原版客户端产生逐卷 capacity/iops/throughput 等文件 | PASS |
| native FD 客户端 IO | 原版与 failover SDK 各双卷对照，排除混入 OSS1 库，仍向未分卷根目录输出 | 未通过，见下文 |

正常删除 native Pod 的部分样例业务循环没有 SIGTERM trap，会耗尽约 90 秒 grace，依靠后续 runtime/kubelet 回收；这些样例只能证明最终清理，不能声称观察到正常 Released ACK。带 trap 的 guest custom 样例已实际观察 Unmounted。公开示例已补业务退出 trap。

## 真实实验发现的实现问题与修复

1. **NodePublish 返回前建立 FUSE 会死锁启动。** containerd 工作线程在未完成 FUSE_INIT 的挂载点 stat/getattr 阻塞，消费 fd 的同 Pod 容器无法创建。改为先写请求和延迟端点、返回，再由已启动 sidecar 请求 fd 时建立内核挂载。修改后真实双卷通过；仅 pipe 模拟不会暴露原问题。
2. **叠加 PVC bind 选错 recovery 连接。** 同一精确路径可能先是 ext4 bind 后是 FUSE；旧查询遇第一条即停止，拿 ext4 minor 去 flush。现在完整扫描精确路径、选择唯一实际 FUSE、排除 virtiofs、拒绝歧义。新增实际 mountinfo 文本解析回归；真 daemon recovery 随后通过。
3. **退出指标与目录退休竞争。** worker 收到退出后可能清理目录，监控尚在写最终状态。现在先落最终指标再转交退出通知，并增加重复和 race 回归。
4. **SDK 指标路径约定。** 原客户端依赖旧 `run/fuse.ossfs/<PV>/globalmount` 格式。软链接会被规范化，不能解决；改用同一 PVC 的第二个标准 volumeMount，仅客户端 argv 使用旧路径，挂载请求 Target、身份与清理协议保持不变。guest 指标已通过；native FD 的 SDK 行为仍未闭环。
5. **UID 环境变量可被重复覆盖。** 只接受唯一的 metadata.uid Downward API 值；伪造常量或重复 env 在发布前拒绝。webhook 对 mountproxy 显式设置 root，native fd 再 drop ALL，不受业务 Pod 非 root 默认值影响。

## 生命周期与兜底

NodePublish 同步成功仅代表配置、隔离、投递条件满足，不代表异步 daemon 已挂载。startupProbe 检查完整卷集，阻止后序 init/业务在部分成功时启动；运行后 readinessProbe 撤销 Ready 不会自动终止已有业务。

正常退出由 Always init sidecar 在业务停止后接收 SIGTERM 并 drain。direct/guest 成功卸载写 Unmounted；非特权 FD 消费者只能停止 daemon、关闭连接并写 Released，实际 umount 属于宿主机 CSI。确认携带 UID、generation 和 session，不接受旧结果替新请求确认。

NodeUnpublish 发明确的卸载请求；desired 缺失不是卸载命令。强删和 SIGKILL 不依赖 sidecar ACK，由 kubelet/CRI、NodeUnpublish 及已验证 Pod 目录消失后的控制 GC 兜底。GC 只清理已知平面控制文件、关闭所属 pending fd，不递归删除远端数据挂载点。API 中 Pod NotFound、心跳超时或空目录都不是 guest 已死亡的证明。

不能保证 runtime/节点永久失联时立即无残留；无法确认的状态必须可观测，不能用超时当作卸载成功。emptyDir 同样不能保证递归删除不穿过存活 FUSE。与 Fluid 的详细对照及 internal 分支研究见[历史根因记录](fuse-manual-sidecar-rootcause-zh.md)；本实现额外强调执行结果、probe 屏障和保守 GC，也承担手工消费者配置及 file 一致性约束，不能宣称整体优于 Fluid。

## 尚未关闭的边界

- **实际 rund RRSA：外部授权阻塞。** 历史自建 runv 的 projected token/原子替换一致性问题不等价于实际 rund 同样存在，也不代表已解决；需具备实际身份后重新验证轮转。长 token 仅是测试窗口。
- **native FD 的逐卷 SDK IO 指标：尚未通过。** pristine 标准/failover OSS2 在相同真实 PVC 投影下均可读写，但仍写未分卷根目录。仓库中没有该闭源 SDK 的指标路径实现，现有公开 CLI 没有目录重定向参数；需客户端侧确认或修复。不能把根目录统计复制到多个卷冒充正确归属，也不采用额外特权、chroot 或注入库绕过。
- **不支持 recovery 的真实节点未测。** 当前仅一个支持能力的节点；能力探测/降级有集成测试，不冒充第二种内核 E2E。
- **完整节点重启/永久断连、跨节点共享卷、全部客户端/架构没有穷举。** 已有进程/协议故障集测不能替代这些真实环境保证，不能据本轮报告宣称已覆盖。
- **webhook 仅支持 Bound PVC。** WFFC/未绑定卷明确拒绝，不等待调度；无需求自动回退到任意路径。native OSS1/customfuse 直接挂载仍需特权 mountproxy，非特权承诺限定 FD-capable OSS2。

上述缺口保留在验收计划，不因 CI 通过或文档入 PR 而消失。
