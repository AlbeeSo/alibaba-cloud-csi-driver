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

## 已确认并修正的真实启动问题

直接复用同步 fd 流程、在 NodePublish 返回前建立 FUSE 挂载，会阻塞同 Pod 消费方创建。真实实验捕获 containerd 线程卡在 FUSE stat/getattr；管道模拟的集成测试无法发现这一点。

当前修正为：NodePublish 只准备目录、配置和延迟 offer；sidecar 已启动并请求接管时，CSI 才建立内核挂载并交付 fd。单测、集测及真实 RunC 双卷的数据面已通过。失败用例的旧 Pod 已请求删除，并重启测试 CSI 释放其 pending fd。必须核对旧 UID 控制目录和宿主机挂载清理，不能把 Pod API 删除当作 runtime 已清理。

真实 recovery 用例又定位出连接选择问题：同一路径存在底层 PVC ext4 bind 和上层 FUSE 记录，旧 helper 取第一条，尝试 flush ext4 minor 对应的不存在连接。已改为只选择真实 FUSE、排除 virtiofs、拒绝多 FUSE 歧义；新增叠加挂载测试和相关 OSS/driver 回归通过。真实 recovery 必须在更新镜像后重测，当前仍不能标为通过。另一个卷在本次故障时保持 Mounted。

另外补齐 runtimeClass/runtimeclass 大小写兼容、冲突参数明确报错；recovery 要求显式可写 fusectl 投影。自建节点已检测到内核 recovery 符号，但尚未据此宣称真实 recovery 验收通过。

## 下一步

1. 构建并部署包含延迟 fd 和最新修复的镜像，重新验证非特权 RunC 双卷、真实 recovery 与不支持内核时的降级。
2. 验证 customfuse 真客户端、webhook 真 admission 后的完整挂载与正常/强删清理。
3. 收齐失败、断连、CSI/sidecar 重启、旧 generation、监控和远端数据保留的证据，更新验收矩阵。
4. 扫描源码、文档和提交历史中的资源标识及凭据，更新同一个 PR，跑完 CI。
5. 清理本轮自建集群、私有测试镜像、bucket 和测试身份，保留清理回执。

真实 rund 参考集群的完整 RRSA 验证仍等待管理员授权；不能用 runv 的通过结果代替。所有集群 ID、kubeconfig、IAM/OSS/镜像仓库信息和原始日志仅留在被 Git 忽略的本地目录，没有写入本文件。
