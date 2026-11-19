# Sidecar 迭代检查点

更新于 2026-11-19。此文件用于中断后回溯，**不是功能已全部验收或可合并的声明**。设计见 [交付方案](fuse-sidecar-delivery-design-zh.md)，验收清单见 [测试计划](fuse-sidecar-unified-acceptance-zh.md)。

## 已落代码

- 首位消费者无需额外元数据、无需 PVC GET；后序 init 的诊断及必要绑定兜底保留。
- OSS/customfuse 的 provisioner 默认 extra-create-metadata；新增 `mountMode: sidecar` controller 提示。
- 精确保留名称识别、customfuse 文件发布、原生 Secret 目录投影和 guest RRSA 限制。
- RunC OSS2 文件配置加延迟 SCM_RIGHTS fd 交接、按卷 release、既有 recovery/监控对接。
- 可选 annotation webhook、独立 TLS 服务、默认关闭的 chart、已绑定卷和权限限制。
- 非特权消费方写 Released，宿主机负责实际卸载；guest/direct 成功卸载仍写 Unmounted。GC 不递归删除数据目录。

## 已有证据

- 首轮全仓 Linux 测试：62 个包成功；随后新增的修复另有相关模块回归。
- 文件协议、proxy 全目录、OSS/customfuse、监控集成测试通过。
- 最新延迟 fd 适配的相关模块测试及 OSS/filevolume race 测试通过。
- Helm 真正渲染的 chart 测试通过：metadata 参数、默认关闭 webhook、特权注入显式许可。
- 独立 ACK 1.33/runv 测试变体的 RunD RRSA 双卷基线通过：读取、写回、只读；CSI node 已禁止 PVC get/list/watch。
- 延迟 fd 修正后，RunC 真机双卷通过：sidecar 非特权、drop ALL capabilities、无绑定元数据、PVC API 权限禁用，两个卷 Mounted 且数据读取/只读检查成功。
- 最新源码的 Linux `go test ./pkg/... ./cmd/...` 全部通过；已提交版本的 lint、增量覆盖率及拼写 CI 全部通过。未提交变更仍需提交后 CI。
- RunC 真机双卷 recovery 通过：终止一个 daemon 后恢复计数增长，同 Pod 两卷仍可读取。
- RunD 混合 OSS1/OSS2、customfuse guest/native 双卷以及 native webhook 注入并供后序 init 使用存储均通过。
- guest customfuse 的真实 webhook 注入双卷通过；卷级 mountMode 与 annotation 的四组合已有真实证据，包括无提示时的旧独立 FUSE Pod 和自动发现 sidecar。
- 缺少消费者出现 FailedMount；异步客户端挂载失败出现 Failed 状态和 startupProbe Unhealthy，业务未启动，此时未出现新的 FailedMount。
- desired 被删时撤销 Ready、保留数据挂载，恢复原请求后重新 Ready。正常删除及强删的多个 native/custom 用例已确认 Pod、UID 控制目录和 host UID 挂载消失；独立 OSS API 确认远端哨兵保留。

## 已确认并修正的真实启动问题

直接复用同步 fd 流程、在 NodePublish 返回前建立 FUSE 挂载，会阻塞同 Pod 消费方创建。真实实验捕获 containerd 线程卡在 FUSE stat/getattr；管道模拟的集成测试无法发现这一点。

当前修正为：NodePublish 只准备目录、配置和延迟 offer；sidecar 已启动并请求接管时，CSI 才建立内核挂载并交付 fd。单测、集测及真实 RunC 双卷的数据面已通过。失败用例的旧 Pod 已请求删除，并重启测试 CSI 释放其 pending fd。必须核对旧 UID 控制目录和宿主机挂载清理，不能把 Pod API 删除当作 runtime 已清理。

真实 recovery 用例又定位出连接选择问题：同一路径存在底层 PVC ext4 bind 和上层 FUSE 记录，旧 helper 取第一条，尝试 flush ext4 minor 对应的不存在连接。已改为扫描全部精确匹配记录，只选择真实 FUSE、排除 virtiofs、拒绝多 FUSE 歧义；不能用遇第一条即停止的 mountinfo filter。新增真实文本解析回归，并在更新镜像后通过实际 daemon 故障恢复。

另外补齐 runtimeClass/runtimeclass 大小写兼容、冲突参数明确报错；recovery 要求显式可写 fusectl 投影。节点不支持 recovery 的降级有集成测试，尚无另一种真实内核节点的 E2E 证据。

## 下一步

1. 完成新失败用例的清理确认、采集真实 metrics endpoint，并收拢生命周期证据。
2. native FD 的逐卷 SDK IO 指标尚未通过：两个原版隔离 SDK 镜像均能读写，但未分卷输出指标；guest/direct 已确认正常。继续定位或记录客户端能力边界，不以生命周期指标代替。
3. 对照完整验收矩阵记录 PASS、NOT RUN 和外部阻塞，补齐必要回归；不能将计划中全部项目自动标为通过。
4. 扫描源码、文档和提交历史中的资源标识及凭据，更新同一个 PR，跑完 CI。
5. 清理本轮自建集群、私有测试镜像、bucket 和测试身份，保留清理回执。

真实 rund 参考集群的完整 RRSA 验证仍等待管理员授权；不能用 runv 的通过结果代替。所有集群 ID、kubeconfig、IAM/OSS/镜像仓库信息和原始日志仅留在被 Git 忽略的本地目录，没有写入本文件。
