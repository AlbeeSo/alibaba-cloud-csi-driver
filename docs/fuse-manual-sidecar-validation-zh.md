# RunD 手动 sidecar 验证记录

状态更新：2026-11-18 已完成 v9–v12 根因修复与隔离 ACK 复测；此前绑定错配、无 ACK 清理、控制文件 GC 的实现缺口均已修复。剩余已定位的外部问题是测试运行时的文件更新/投射一致性，影响 RRSA 轮换和控制文件更新，**不是生产验收完成**。设计与矩阵见 [测试计划](fuse-manual-sidecar-test-plan-zh.md)，逐项根因见[根因记录](fuse-manual-sidecar-rootcause-zh.md)。原始资源和日志仅在忽略的 `.local/` 下保存，本文不记录环境标识或凭据。

实现和责任边界见 [设计文档](fuse-manual-sidecar-design-zh.md)，配置和排障见 [使用说明](fuse-manual-sidecar-usage-zh.md)。下表按实际镜像版本记录；后续版本的本地测试不能倒推旧 E2E 已经通过。

鉴权继续使用 24 小时 projected OIDC token 的已授权测试窗口；STS 换证在 guest 内执行。自动轮换未通过。本轮用不含 token 的原生文件复现隔离到运行时接口，没有重新引入 token mirror、进程识别或 CRI 专用补偿。

## 2026-11-18 根因修复复测（当前结论）

新建独立 ACK v1.33.3，沿用下述 runv 2.2.4 版本作为 RunD 家族测试环境；非托管 CSI、无 webhook、双卷 RRSA。v9–v12 的镜像分别记录，不用后续本地测试改写旧 E2E 结果。

| 镜像/用例 | 实际结果与边界 |
|---|---|
| v9 交换两个 PVC 映射 | 标准 Bound PVC 校验明确 FailedMount；业务未启动，无可执行 desired；修复 v8 错卷问题 |
| v9 正常删除 | 旧 UID 控制目录退休，新 Pod 双卷读取恢复 |
| v9 无 ACK 强删 | 先确认 sidecar 被 SIGKILL、没有 Unmounted；CSI 重启保留未释放状态；强删后标准 kubelet NodeUnpublish 成功，旧 UID 状态消失、新 UID 双卷成功 |
| v9 孤儿 GC | 实际旧 Pod 本地目录已消失后，仅恢复其控制文件模拟 CSI 崩溃残留；CSI 启动 GC 删除旧状态，保留活跃 UID 和 I/O |
| v9 部分失败与未完成挂载 | Failed/Mounted 双卷删除后状态退休；DNS 阻塞的两卷 Mounting 时删 Pod，约 3.2 秒回收，业务未启动 |
| v9 daemon 单卷故障 | 第一卷 ENOTCONN、第二卷可读，Pod NotReady，不落回本地目录写入 |
| v9 host 清理安全 | 远端哨兵保留；测试遗留空子目录导致 ENOTEMPTY，精确 rmdir 自有测试叶子后 kubelet 重试成功；产品未忽略非空目录 |
| v9 ossfs1 + ossfs2 | 同镜像、固定 args/probe、业务 SA RRSA 双卷读取与删除退休通过；仅修正 ossfs1 测试 prefix 的前导斜线 |
| v10 runtime 保护 | 实际 runc 或未声明 class 的 Pod 明确 FailedMount；不在 host 启动文件 sidecar |
| v10 节点正常重启 | bootID 变化，同一 PodUID 自动恢复双卷，OSS 哨兵保留，约 318.5 秒；没有手动重建 Pod，requiresRepublish 保持 false |
| v12 最终镜像 | DS/controller/sidecar 同版本，固定 args 为 --file-mode，双卷读取通过 |
| v12 init 顺序保护 | sidecar 前的 init 使用同一 PVC 时明确 FailedMount，不启动早期数据消费者 |
| v12 控制 JSON 损坏 | guest 内 desired 改成 {} 后 NotReady，但已有 I/O 保留；删除 Pod 后标准 NodeUnpublish 和 UID 退休完成，不使用不可信 target 决定删除路径 |
| v12 双副本缩容 | 两个 UID 只见自身请求；缩掉的 UID 状态消失，存活副本 daemon PID 不变且双卷可读 |
| 独立原生文件复现 | 无 CSI/FUSE；runc 看到 host 替换/删除和 ConfigMap/projected 更新，rund 仍旧值，新文件名可见；host 投射本身已更新。定位运行时共享文件接口，详见根因记录 |
| 本地最终回归 | v12 focused 全部通过，另完整 pkg/oss 测试通过；包含真实文件系统并发退休、损坏 JSON、安全路径、绑定缓存、init 顺序和三种 driver 子进程测试 |

成本：1/10/100 卷冷缓存每 Pod/claim 一次 PVC GET，热缓存无新增 PVC 查询；RuntimeClass 分类复用原 Pod GET。Linux arm64 缓存命中函数约 0.29 微秒，不能代表完整 RPC。真实双卷 readiness exec 47 个样本均值约 27.6 ms，累计 p50/p95/p99 桶上界 50 ms；不是生产规模 SLO。未进行大规模并发或完整 IOPS 压测，不将它们归咎于封闭实现。

## 环境

自建 ACK v1.33.3，单独创建测试节点及子网；复用一个无 vSwitch 的现有 VPC，因为账号 VPC 配额已满。非托管 CSI 使用仓库 Helm chart，无 webhook。RuntimeClass 名为 rund，handler 为 runv，实际 runtime type 为 io.containerd.rund.v2。host kernel 为 5.10.134-19.8.al8.x86_64，guest kernel 为 4.19.91-685c634.al7.x86_64。业务与 FUSE 运行在 guest 内。

安全沙箱在本次独立节点池的 `kubernetes_config` 中选择 `runtime: Sandboxed-Container.runv`、`runtime_version: 2.2.4`，不是用一个覆盖全集群节点的布尔开关。节点使用 ALinux3 弹性裸金属实例和 containerd 1.6.28；集群原有 runv RuntimeClass，实验复制为名字 rund、相同 handler/scheduling 的别名。实际 Pod 使用 rund；未按 runv 字符串走另一套代码。

参考环境只读 API 显示 RuntimeClass rund/handler rund，Kubernetes v1.36.2，containerd 2.3.6；节点 exec 被 qianzhou 的默认只读限制拒绝，没有开启 admin，没有部署或修改参考环境。尚不能确认其 guest 共享文件系统缓存行为与自建环境相同。

RRSA 使用业务 Pod ServiceAccount；kubelet 的 projected OIDC token 通过 volume 提供给 guest，ossfs2 在 guest 内调用 STS 获取凭据。不是 host 执行 RRSA 换证，也不是 CSI 获取一次性 AK 后落盘。

## v1–v8 历史问题与证据（保留原结果）

| 用例/版本 | 实际结果 | 结论/后续 |
|---|---|---|
| 初始 RRSA 信任策略错误 | 两卷均失败；业务未启动；STS AuthenticationFail.NoPermission 可定位 | 修正测试角色策略后首次挂载通过；说明失败不能被当作交付成功 |
| v1 双卷 RRSA | 业务两前缀读、第一卷写回、第二卷 RO 验证通过；独立 OSS API 确认写入 | 基本链路通过，不代表生命周期完成 |
| v1 guest 删除 desired | NotReady；两卷仍可读、第一卷仍可写；daemon PID 不变；恢复后 Ready | 文件消失不得作为卸载指令 |
| v1 单卷 daemon SIGKILL | 第一卷 ENOTCONN，第二卷仍可读；第一卷不能落到本地目录继续写 | 进程监督与故障隔离有效 |
| v1 sidecar 随后重启 | sidecar 重新 Mounted/Pod Ready，但业务两卷仍为 ENOTCONN | **失败**：业务 bind 保留旧 FUSE；不能用 sidecar 视图宣称透明恢复 |
| v3 重启防护本地测试 | readiness 成功前持久化 session；其后重启不再次挂载，报 PodRecreationRequired | 修复业务旧 bind 导致的假 Ready，真实复测待补 |
| v1 强删 | 约 0.8 秒 API 已删除但旧 runtime 仍活着；约 3.3 秒 sandbox NOTREADY 且 rund 进程退出；新 UID 双卷读通过 | API 删除不等于 guest 消亡；本次旧 sidecar 仍来得及写 Unmounted，不能替代“无 hook”测试 |
| PVC 数据 host 视图 | host 未看到远端 hello；扫描约 976 个进程 mount 表未发现该 UID 的 host FUSE 别名 | 支持本版本的 guest 隔离结论，不是任意 RunD 的通用证明 |
| emptyDir 对照组 | 两卷读写/RO 通过，host 未看到远端 hello；第二卷迟到时业务未启动 | 只比较数据面；配置由诊断工具导入，**不是 CSI+emptyDir 的端到端实现** |
| 控制文件双向可见性 | host 原地写/新增文件可见，guest 写回 host 可见；host 原子替换既有文件时 guest 可继续读旧内容 | 普通文件可见不等于原子替换一致性；协议需明确不可变文件与存在性检测 |
| v3 单纯 Go ReadDir 刷新 | 既有文件原子替换的复测失败 | **未解决**：不能仅凭一次 ls 后成功就认定缓存被刷新 |
| projected token 轮换 | host 已生成新 token，guest canonical token 仍指向旧 revision，甚至过期 | **失败**：首次 RRSA 成功不能代替轮换验证 |
| v4 token revision mirror 实验 | 已暂停并移出待交付代码 | 不作为运行时轮换问题已解决的证据 |
| v5 长 token 测试窗口 | 请求 86400 秒，实际 JWT exp−iat 为 86400 秒；无 token mirror，双卷业务读取通过 | 仅用于继续测试，不代表 RRSA 自动轮换通过 |
| v5 双卷中一卷权限失败 | 一卷 Failed，另一卷 Mounted；失败原因可见，业务容器保持等待 | 单卷失败不会误放行业务，也不阻止其他卷执行 |
| v5 部分挂载失败后正常删除 | Pod 删除完成，两卷均留下 Unmounted 状态 | 覆盖本次优雅退出；不代表异常退出接管已完成 |
| v5 guest 删除 desired/恢复复测 | NotReady，双卷可读、第一卷可写，daemon PID 不变；恢复 Ready | 该视图中缺失可检测，保留挂载的语义通过 |
| v5 host 删除 desired/恢复复测 | 25 秒内未变 NotReady；恢复文件后业务正常 | **失败**：目录扫描未解决 host→guest 删除可见性；不以 guest 内删除通过替代 |
| v5 runtime SIGKILL sidecar | 容器重启计数增加，但 runtime bind 旧 PVC 时 StartError / Socket not connected，进程尚未启动；Pod NotReady | 没有假 Ready；此次未执行到应用层 PodRecreationRequired 屏障 |
| v5 SIGKILL 后强删整 Pod | 新 UID 双卷读取成功；旧 UID 没有 Unmounted ACK，状态目录仍在 | 应用恢复通过；旧控制状态清理和异常接管**未通过** |
| v5 显式撤销 + cwd EBUSY | host 首次创建 unmount 指令可见；目标 CleanupPending，另一卷可读，Pod NotReady；释放 cwd 后 Unmounted | 正常 umount 失败不转 lazy success；单卷撤销不取消其他 worker |
| v5 CSI DS 重启 | 相同业务 PodUID，双卷仍可读、Ready | 不依赖 CSI 进程内存维护已成功的 guest 挂载 |
| v5 已成功双卷正常删 Pod | 两卷 Unmounted；新 Pod Ready；旧控制状态保留 | 优雅卸载通过，最终状态 GC 未实现 |
| v5 无消费者 | FailedMount、业务不启动，但先报 RRSA token 缺失 | 错误定位不够明确，已调整校验顺序 |
| v6 无消费者复测 | FailedMount 明确提示 native mountproxy sidecar，业务不启动 | 改进报错通过；未先写 inventory/desired |
| v5 有限任务自然完成 | Pod Succeeded，两卷 Unmounted，无 preStop | 原生 sidecar 在业务完成后可正常退出；未验证 Job controller 的全部重试行为 |
| v6 host 专用子目录递归删除 | host 不见远端哨兵；删除自有测试子目录内容后，guest 与独立 OSS API 均确认哨兵仍在 | 支持此 runtime 版本的隔离；不推导所有 runtime/故障视图均安全 |
| v6 busy SIGTERM 退出竞态 | 曾观察一卷 CleanupPending、一卷 Unmounted，随后被重启屏障覆盖为 PodRecreationRequired | **失败，v7 已修复**：重启拒绝不应销毁旧卸载证据；旧脚本事后读状态存在竞态，不能将最终两项记为清理通过 |
| v7 busy SIGTERM 复测 | 约 1.3 秒观察到 CleanupPending / Unmounted；等待重启后两项证据仍保留 | 修复通过：拒绝重启不创建新 session、不覆盖已有清理证据；busy 卷不拖住另一卷 |
| v7 应用 preStop 耗尽 grace | 配置 5 秒 Pod grace、30 秒 preStop，约 7.8 秒后 Pod 删除；本次两卷仍收到 Unmounted | 该窗口优雅清理赶上了；不替代 SIGKILL 无 ACK 结果，也不承诺耗尽 grace 都能清理 |
| v7 startupProbe 失败触发重启 | restartCount 增加，业务未运行；采样时 sidecar 已退出，exec 不能读取状态 | 启动门槛通过；没有取得足够证据证明所有重启窗口无重复 daemon，保持部分覆盖 |
| v7 host 删除连续观测 | 12 次采样 host 文件持续缺失，guest status 始终 Mounted、Pod 始终 Ready | 确认并非 CSI 重建了文件导致未观测到删除；共享路径可见性前提仍未满足 |
| v8 最终镜像基线 | CSI DS/controller 和 sidecar 同版本，Pod Ready，业务双卷读取成功 | 覆盖 Result 成功检查、重复 PVC 目标拒绝等最新校验 |
| v8 TERM 后重启证据 | 先确认业务 Ready；TERM 后从日志确认应用层重启屏障执行，Pod NotReady，两卷 Unmounted 和原 session 保留 | 修复有真实执行证据；不依赖“容器还没启动”的 runtime 失败代替 |
| v8 两个 PVC 映射交换 | 确认业务容器已运行后，两卷读取均成功，但读到相反 prefix；sidecar 仍报告 Mounted | **失败/P0**：不能只验证 target key 存在；需要可信 PVC→PV 绑定信息。未悄悄增加逐卷 API GET |

容器内 `kill -KILL 1` 未终止 PID 1，已作废此注入方式。真实 SIGKILL 使用节点 containerd task kill，并检查 restartCount/StartError；测试工具失败不算故障注入成功。

映射交换第一轮曾在业务启动前 exec，读取失败不构成产品证据，已作废；上表只采用等待业务 Running 后的第二轮。busy 退出与独立 TERM 重启测试分别覆盖“各卷清理结果不互相拖累”和“应用层屏障确实执行”。

## 本地回归

Linux 一次性 Podman 容器，Go 1.26.1，GOMAXPROCS=2，`-p 1`，2026-11-17 已执行：

- mountproxy 主程序：自动注册 driver、drain 卡住、driver 终止卡住、正常退出及原错误保留。
- OSS：DirectPublish 新分流及单次 GET、GET 失败不重试、UID/节点错误、手动契约、RRSA 投射和身份；原 DetermineRuntimeType、NodePublishVolume_RuntimeTypes 回归。
- filevolume 全包：多卷独立失败/超时、缺失请求、显式撤销、旧 generation/session/UID、扩展字段、启动门槛及放行后重启。
- file backend/三种 driver：实际子进程监督与错误、底层 PVC 投影保留、driver 选择、FUSE 设备、Secret interceptor。
- 特权容器中真实私有 mount namespace：cwd 导致 EBUSY 时 mount 仍附着；释放后正常卸载与重复卸载；目录保留。此项用 tmpfs 验证 syscall 行为，真实 FUSE EBUSY 另由上表 E2E 覆盖。

最终源码的全部上述本地测试已再次通过，日志保存为本地 `/tmp/fuse-lifecycle-focused-tests-v8.log`。包含 1/10/100 卷 API 计数、探针必须有成功 Response、重复 PVC 目标拒绝，以及重启证据保留修复；不将镜像构建成功当作以上测试已执行的证据。

镜像 v8 后又补充了 MountRequest 已知字段大小写规范化：Go JSON 接受旧大小写输入，但不能将其误当成扩展字段、进而覆盖已知字段的零值。该改动经过最终本地全 proxy 包及上述回归，日志 `/tmp/fuse-lifecycle-focused-tests-final.log`；没有在已经开始销毁的集群追加 E2E。正常 E2E 请求使用规范字段名，最后真实镜像仍标为 v8，不冒称新镜像已测试。

## 当前外部阻塞与未覆盖项

- 外部阻塞：测试运行时的 host→guest 已有文件替换/删除、标准 ConfigMap/projected 更新不可见。独立复现已排除 CSI/FUSE；内部具体缓存实现不可见，需运行时维护者确认。控制一致性与 RRSA 自动轮换仍未通过，长 token 只是测试窗口。
- 已修复而不再列为外部阻塞：PVC/PV 绑定验证、无 ACK NodeUnpublish、逐卷退休、孤儿元数据 GC、多卷退休竞态和提前 init 消费。
- 未覆盖：生产规模并发/CPU/IOPS、所有磁盘写失败与内核不可中断组合、全部 runtime/kubelet 故障窗口。这些是测试覆盖限制，不是已证明“我们无法解决”的问题。正常节点重启已有真实证据。
- emptyDir 只做过数据面对照，不交付完整 CSI+emptyDir 替代方案；当前保留标准 CSI 触发和 guest 数据入口，不能用卷类型名称推导防误删保证。

## 矩阵对照

这里的“部分”表示相应场景有证据，但不能把整行所有故障方式都记作 PASS。

| 测试计划 ID | 本轮覆盖与状态 |
|---|---|
| C01–C03 | U 已覆盖路由、消费者关键契约及 Pod 身份失败；R 无 sidecar 错误复测通过 |
| C04–C07 | U/I 覆盖双卷、混合 driver、不同 handle、重试与变更拒绝；R 双卷通过；大规模与所有并发组合未测 |
| C08 | v8 错卷已由 v9 Bound PVC 校验修复并真实复测；v12 增加提前 init 消费拒绝 |
| C09–C10 | 部分 U/I：JSON 校验、UID/generation/路径与扩展字段；真实磁盘满/所有 fsync 故障未测 |
| C11 | guest 删除/恢复通过；host 删除**失败**；整个 state 误删未验收 |
| M01–M03 | 权限拒绝、部分失败、进程超时/退出已有 U/I/R；所有 DNS/网络故障组合未测 |
| M04–M05 | 双卷业务视图、独立 OSS 写回、RO、混合 ossfs1/2、双副本缩容隔离已验证；未穷举跨 bucket 组合 |
| M06 | U/I 覆盖 Secret 处理/错误脱敏；真实主链路 RRSA 无 JWT/STS AK 落 desired；不声称所有第三方 stderr 都已证明无敏感内容 |
| P01–P02 | U/I 旧状态拒绝；R 单卷 daemon kill NotReady、另一卷可读；重启视图问题已重现 |
| P03–P04 | 规模/限流未测；启动前部分成功重启覆盖不完整 |
| T01–T02 | R 正常删除、滚动更新、有限任务、缩容、部分失败、双卷 DNS 阻塞 Mounting 时删除；未穷举所有窗口 |
| T03 | R 配置慢 preStop 耗尽 grace，本次两卷仍 ACK；不推导所有耗尽窗口通过 |
| T04–T07 | 部分 U/I/R：TERM、KILL、EBUSY、独立清理、退出上限；不代表所有内核阻塞/namespace 引用已回收 |
| T08–T10 | 持久撤销/旧状态 U/I；CSI 重启、无 ACK 强删、旧 UID 退休、新 UID 恢复 R 通过；NodeUnpublish 不等待死消费者 |
| T11–T12 | 标准节点重启同 UID 恢复、CSI 启动孤儿元数据 GC 通过；不增加 guest reaper，其他失联/崩溃窗口未穷举 |
| S01–S02 | 部分 R：host/guest/业务视图、进程 mount 表观察、限范围递归删除哨兵保留；不是任意 runtime 证明 |
| S03 | 逐卷退休、孤儿 GC、多 UID 隔离真实复测；本地覆盖多卷并发、desired 缺失但其他卷结果仍须保留、未知文件和符号链接 |
| F01 | U 1/10/100 卷计数：一次 Pod GET/NodePublish，冷缓存一次 PVC GET/Pod/claim，热缓存无额外 PVC 查询；无 PV/Node/RuntimeClass GET |
| F02 | 单节点双卷 exec probe 小样本与缓存函数基准已有；完整规模/IOPS 未运行 |
| A01–A02 | RRSA 初次换证、读写、拒绝权限、投射/SA 校验已覆盖；轮转**失败**，长 token 仅测试窗口 |

## 测试资源收尾

2026-11-18 第二轮资源已全部回收：ACK、节点 ECS、自建 vSwitch、key pair、专用 ACR namespace/repository/VPC link、OSS bucket 和 RRSA role/policy 已删除。另行查询确认安全组、SLB、EIP、节点 RAM role、OIDC provider 不存在，按集群标签查询无残余云盘；原有 VPC 和 ACR 实例保留，参考环境未修改。

清理正确性的产品证据来自销毁前的正常删除、无 ACK 强删、孤儿状态 GC 和 v12 双副本缩容。销毁集群不计作 CSI 清理通过。最后收尾脚本曾使用错误的业务读取路径，该额外断言作废，不覆盖已有 v12 正确路径读取/缩容证据，也不补记“最终缩到零独立验收通过”。

最终 v12 Linux 回归日志为本地 `/tmp/fuse-rootcause-tests-v12.log`，完整 OSS 包为 `/tmp/fuse-rootcause-full-oss-tests.log`。双卷示例及无凭据原生复现 YAML 均通过本地解析和文档链接检查；新一轮的原生复现 YAML 未另记 server-side dry-run。50 个变更文件已扫描实际测试资源标识和凭据模式，未检出；`.local/` 不进入提交。旧 prototype 的 webhook/runc 实验不属于本分支。

### 2026-11-17 历史环境

2026-11-17，已保存本地原始记录后销毁自建测试环境，并进行删除后的独立查询：

- ACK 集群、测试节点 ECS、独立 vSwitch、SSH key pair 均已删除。
- ACK 创建的安全组、SLB、EIP、节点角色和 OIDC provider 均确认不存在；按集群标签查询无剩余云盘。
- 本轮专用 ACR namespace/repository、VPC endpoint link、OSS 测试 bucket、专用 RRSA role/policy 已移除。
- 创建前已存在的 VPC 与 ACR 实例保留；参考集群未修改。

**整套测试环境销毁是实验收尾，不是 CSI 独立清理/状态 GC 通过的证据。** v1–v8 的 guest 无 ACK 和控制状态残留保持原失败结果；v9–v12 用新集群、新镜像独立复测，不因旧节点释放而改写旧记录。

双卷 RRSA 示例已通过 server-side dry-run；最终变更文件通过真实测试资源标识、账号信息和凭据模式检查，`.local/` 保持忽略提交。检查通过不替代提交前人工 diff review；本轮没有把测试资源信息写入公开示例。
