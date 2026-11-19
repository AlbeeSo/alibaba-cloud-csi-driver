# FUSE sidecar 单 PR 完整验收计划

更新：2026-11-19。本文是后续实现的验收清单，不是已通过报告。最终采用一个功能 PR，包含公共 file 基础、RunD、RunC、customfuse 和可选 webhook，替代之前三 PR 的交付计划。实现按独立 commit 组织，测试随对应功能提交。历史 RunD 实测仅作为回归证据，不替代新代码、customfuse、RunC 或 webhook 的验收。

设计见[统一交付设计](fuse-sidecar-delivery-design-zh.md)，历史 RunD 细项见[既有矩阵](fuse-manual-sidecar-test-plan-zh.md)。以下新目标与历史冲突时以本文和最终设计为准，例如首位免 PVC GET、customfuse 固定名称和 RunC 非特权 fd 接管；不能把旧拒绝 runc/非 privileged 的测试原样当最终行为。

## 1. 范围、环境与执行规则

| 环境 | 必须确认的身份/能力 | 必须覆盖的路径 |
|---|---|---|
| D1：独立自建 RunD 家族 runv 测试环境 | Kubernetes/containerd/handler/运行时版本、host/guest 内核、控制文件一致性 | file、RRSA、OSS1/OSS2/customfuse、多卷、故障与清理；不支持 fd-passing/recovery |
| D2：实际目标 rund 环境 | 独立记录实际版本与 guest 能力，不由 D1 或名称推导 | 授权范围内原生文件、真实挂载/业务视图/身份及清理；条件不足明确 NOT RUN，不能用 D1 代替 |
| C1：实际共享内核 RunC，recovery 能力具备 | 节点实际 handler、/dev/fuse、内核能力探测、架构 | 非特权 OSS2 fd/recovery、直接挂载 customfuse、手动/webhook、既有独立 FUSE Pod |
| C2：实际共享内核 RunC，recovery 能力不具备 | 正常真实内核环境，不能只依赖版本字符串 | recovery 降级但 fd-passing 保留，非特权保持、告警/指标正确、无恢复能力假承诺 |
| C3：能力/权限失败夹具 | /dev/fuse 不可用、受限 syscall、缺 recovery sysfs 权限、错误镜像 | 明确失败/受控降级；不得静默提权、偷偷切旧挂载或假 Ready |

nil RuntimeClass 不是运行时能力证据；覆盖默认确为 runc、默认实际为隔离 guest 的配置。保留 rund/runv/sandbox 历史别名、kata-remote、csi-agent 的独立分流回归，不能把类名当新功能开关。未掌握的运行时仅给支持边界，不能宣称全兼容。Kubernetes 至少验证 v1.33 的 native sidecar 行为，实际目标更高版本单列；CPU 架构覆盖发布支持范围，缺少某架构真实节点时明确区分交叉构建、集测和实测。

测试层级：U 为单测（fake client、API 计数、状态/字段校验）；I 为 Linux 集测（真实 JSON/socket/SCM_RIGHTS/子进程，必要时隔离 mount namespace 和真实 syscall）；E 为隔离集群真实 E2E。大量错误组合优先 U/I，E 选择关键能力和生命周期链路，避免机械全排列。每个环境至少有手动多卷正例；webhook 必须在 D/C 两条真实支持路径验收，不能仅验证生成 YAML。

### 集测优先，限制 E2E 重复

86 组是验收关注点，不是 86 个 E2E 场景。下表标 E 的组只要求关键行为有真实环境证据，不要求该组每个变体、gate组合或错误码都在集群重演。相同真实用例可以提供多个组的证据，但应逐项关联断言，不能把一个 Ready 截图当成整组通过。

| 优先用集成测试闭环的范围 | 真实组件与故障注入方式 |
|---|---|
| controller/node分流、SC参数保留、API次数、镜像及权限模板 | 经CSI包装层调用真实server，fake client精确计数；参数化四组合/driver/gate/元数据和错误；不部署集群穷举 |
| file配置→worker→driver→probe→drain | 临时目录真实原子写/删除/损坏，真实runner和子进程；可控backend注入超时/EBUSY/取消/状态故障；同时断言进程、状态、清理副作用 |
| fd交接、持有/关闭、重复投递和recovery状态 | 真实Unix socket/SCM_RIGHTS与进程边界；fd计数、接管/断开/重试；driver假进程可控退出，复用现有supervise测试，不用mock代替所有fd收发 |
| syscall安全与未服务FUSE清理 | Linux隔离mount namespace中真实mount/unmount与EBUSY；有/dev/fuse和权限的CI夹具才运行，未具备条件显式skip，不伪造通过 |
| webhook协议、幂等、重入、PVC绑定状态与失败策略 | 经AdmissionReview HTTP/TLS入口调用真实handler、客户端反应器注入API状态；必要时轻量本地apiserver验证patch/schema/validation，不为每个拒绝场景建云集群 |
| customfuse入口、参数、fork/退出与回收 | 真实配置脚本/进程组/管道与runner，覆盖错误组合；另以少量真实自定义文件系统E2E证明数据面 |
| 监控与统计链路 | 真实interceptor/monitor写入、collector读取并抓取实际metrics输出；fake时钟/可控进程模拟故障和恢复，核对计数、标签和清理，不只验证某个写文件函数 |
| capability/版本/架构检测与降级 | 注入探测输入测试全部边界，使用真实支持/不支持节点各一条代表性E2E核验，避免每种内核版本重复跑 |

E2E 收敛为可复用的少量端到端场景族：

1. RunD真实双卷RRSA、业务读写/RO、probe与监控、正常及强删清理；旧runv与实际rund分别记证据。实际身份轮转需真实环境，不用fake STS冒充。
2. RunC真实OSS2 failover镜像、fd接管和非特权、daemon故障恢复、业务旧引用保持、监控与清理；与CSI重启/接管边界故障组合在同一隔离部署内验证。
3. recovery不支持的真实节点：一次完整降级/非特权/读写/指标链路，能力读取失败等其余变体由U/I覆盖。
4. customfuse在RunD/RunC各一条真实双卷及关键退出/清理链路，不重复枚举所有脚本失败形式。
5. webhook在D/C各一条实际注入→挂载→失败可见→清理链路；可与上述场景复用部署，四组合的完整断言以U/I为主。绑定/调度时序须至少验证声明支持的真实流程。
6. 既有独立FUSE Pod、csi-agent/COCO等选择代表性回归，已有相同代码/环境证据可引用；共享卷存活副本、远端哨兵和关键节点重启不能完全由fake替代。

先 U/I 通过再进入 E；失败优先增加稳定的回归集测并修复，再重跑受影响E2E，不因小改动重复全部环境矩阵。只有新变更触及共用协议/清理或出现未解释风险时扩大E2E范围。纯文档、参数错误或已被等价集测证明的分支不触发全量云回归。最终报告单列U/I/E证据与缺口，不能为了减少E2E把真实运行时保证降成mock保证。

测试只使用授权隔离节点/namespace、独立存储 prefix 和可丢弃哨兵。共享目标环境不进行节点重启、runtime 故障、权限变更等破坏性注入。资源不足的项保持 NOT RUN/阻塞，不能用 mock PASS 覆盖。

## 2. driver、鉴权、权限与镜像基线

| driver/路径 | 执行与权限目标 | fd/recovery |
|---|---|---|
| RunD OSS1/OSS2 | guest file、RRSA、当前明确的挂载权限 | 两项明确拒绝 |
| RunD customfuse | 自定义镜像/入口、file、guest 内直接挂载；RRSA 身份契约 | 两项明确拒绝 |
| RunC OSS2 | file 配置/状态 + fd socket；CSI 内核 mount，sidecar 与业务均非 privileged | 复用已有 fd-passing/recovery，按实际节点能力生效 |
| RunC OSS1 | 按实际 driver 能力验证直接挂载及权限，不冒充非特权 | 不支持项明确拒绝，不能套用 OSS2 能力 |
| RunC customfuse | file、自定义入口直接挂载，显式所需权限；业务无须 privileged | 不支持项明确拒绝 |
| 既有独立 FUSE Pod | 保持 socket、共享 globalmount、已有鉴权/生命周期 | 回归 legacy、fd-only、fd+recovery、降级 |

默认 OSS sidecar 精确名 `alibabacloud-csi-mountproxy`，customfuse 精确名 `alibabacloud-csi-customfuse-mountproxy`，不通配、不解析 command/args。当前单消费者约定下两名同时出现报歧义。OSS1+OSS2 的同 sidecar 混合能力/权限必须明确；不能为了兼容直接挂载卷，把原本应非特权的 OSS2 模板静默提权。customfuse 与 OSS 分别真实验收；若多个消费者尚未设计，则不以放开两名共存假装支持混合。

代码镜像基线来自 `pkg/mounter/fuse_pod_manager/oss/utils.go`：OSS2 普通默认 tag 为 `v2.0.9.ack.3-a1f29fc`，fd/recovery 默认 tag 为 `v2.0.8.ack.1.failover-1fd839a`；这只是当前源码选择，不保证任意镜像都有相同能力。记录实际 registry/tag/digest、二进制版本和架构；优先使用仓库已提供的 failover 镜像验证。若仅覆盖新的 mountproxy 二进制，需保留该镜像内真实支持 fd/recovery 的 OSS2 客户端，不能改用普通客户端后把失败归为内核不支持。

## 3. 分流、契约、API 与兼容（U/I，指定项补 E）

| ID | 测试项 | 必须断言 |
|---|---|---|
| B01 | SC 新 key 开/关 × sidecar 有/无四种组合 | 开+无明确 FailedMount；开+有跳过独立 Pod；关+有选择 sidecar但保留提前建 Pod的已知代价；关+无完全旧路径；E 各一例 |
| B02 | key 缺失/非法值、大小写、静态 PV、动态 CreateVolume、存量 PV | 严格解析、参数保留、明确错误；SC 不热补旧 PV，不假设 CSI source 可原地更新 |
| B03 | controller 没有 Pod 信息；Node 复用 context | 不引入 controller 扫描业务 Pod/informer；不开新 Pod/PV/RuntimeClass 查询来猜分流 |
| B04 | 默认/自定义入口、错误名称、同名 app container、普通 init、两名/多候选 | 仅精确 init consumer 识别；结构错误和歧义明确拒绝，不回退，不向非消费者发请求 |
| B05 | 首位 sidecar、非首位无前置 PVC、非首位前置 PVC | 前两者无需额外配置、冷/热均无新增 PVC GET；第三者元数据优先/必要查询兜底；INFO/WARNING 和去重数量正确 |
| B06 | Bound/未绑定/删除 PVC，PV/PVC 交换、重复目标、volumeHandle≠PV名 | 路径协议不变；保留的检查及首位免查后的保护边界明确；不能错误报告仍有 live binding 证明 |
| B07 | Pod GET 失败、UID 重建、错误节点、terminating Pod、缺 PodInfo | 明确失败，无跨 UID 配置或旧缓存复用；失败查询不反复重试放大 |
| B08 | 1/10/100 卷、重复/并发发布、多 Pod 同 PV | API 计数、锁行为、幂等、共享与 Pod 独占隔离；controller 去重；E 双卷+双副本 |
| B09 | rund 历史 feature gate、direct/socket/skip 组合、kata-remote、csi-agent | 保留既有语义；RunD 拒 fd/recovery，RunC file 分流早于旧必需 socket 校验；不伪造 socket |
| B10 | 原有选项、mountFlags、RO、路径/subpath、非法组合、未知扩展 | 完整公共请求语义；未知可选保留，未知必需拒绝；overlay 等未支持能力明确报错，不悄悄丢字段 |
| B11 | 新 CSI/旧 mountproxy，旧 CSI/新镜像，升级回滚 | 明确能力协商/版本错误；旧 generation 不重解释；活动旧卷不被迁移/清理，新请求不会假成功 |

## 4. 文件协议、worker、probe 与异常（U/I + 关键 E）

| ID | 测试项 | 必须断言 |
|---|---|---|
| F01 | 全卷 inventory，第二卷迟到/永不出现、空卷、增减卷 | startup 等完整集合，不以首个文件放行；增加卷不改 args/probe |
| F02 | 部分写、原子 rename、删除/重建、新文件、读旧缓存 | D/C 原生小实验记录可见性；不执行半 JSON；运行时缺陷单列，不能用原地写绕过标准原子协议 |
| F03 | 重复请求、并发写、旧 generation/session/UID、状态损坏 | 幂等、不重复 daemon；旧成功不放行，不覆盖新请求 |
| F04 | 磁盘满、只读、权限错误、fsync/rename 错、损坏 manifest/desired | 提交失败可见，无半交付；损坏控制数据不能授权或重定向清理 |
| F05 | 持续 I/O 时 host 删 desired/整个控制目录 | 缺文件不是卸载命令；daemon 监督不丢；继续 drain/正常回收；告警可定位 |
| F06 | mount 卡住/超时/取消、刚出现就退出、三卷成功失败卡住混合 | 独立状态、取消有界、无假 Mounted，成功卷仍回收 |
| F07 | probe 过期、时钟变化、runner 停止、旧 ready、启动探针重启 | 全卷和新鲜性校验；旧 ready 不放行，无 liveness 重启风暴 |
| F08 | 前置 init 提前读写、返回0、配置准备 init、其他 webhook 重排 | 不接受静默空目录成功；说明实际检测点；native sidecar/优先级 env 不是未经验证的保证 |
| F09 | 首次 NodePublish 已成功后 daemon 失败 | 不能伪称首次返回 FailedMount；probe/Event/逐卷结果/日志给出卷及脱敏原因；业务等待或已放行后变 NotReady |
| F10 | 挂载传播、底层 virtiofs、业务 mount namespace、RO/RW | sidecar 可读不是通过条件；业务独立读写、RO 写拒绝、远端 API 验写回；不误认底层投影为 FUSE |

## 5. fd-passing、去特权、recovery 与不支持节点

源码基线：`EnableFUSEFdPassing`、`EnableOssfs2Recovery` 和卷选项共同决定请求；recovery 自动要求 fd。`CheckKernelForRecovery` 当前按内核 `fuse_flush_pq` 能力符号探测，不只看版本号。节点不支持 recovery 时现有行为为 Recovery=false、RecoveryDegraded=true，**FdPassing 保留**；不能因缺 recovery 重新授予 privileged。新 sidecar 要保持该能力分离。

| ID | 测试项 | 必须断言/层级 |
|---|---|---|
| D01 | gate 开/关、卷选项开/关、recovery→fd、controller/node 配置一致及不一致 | 覆盖解析优先级、镜像选择、权限/sysfs 配置一致性；不一致不得把实际 privileged 报成已去特权；U/I，独立 Pod 与 sidecar 各 E |
| D02 | recovery 符号存在/缺失、读取失败、受限 kallsyms、不能只看内核版本 | 能力探测可测；C1/C2 真节点验证，读取失败不误判支持；U/I/E |
| D03 | 不支持 recovery 的节点请求 recovery | 挂载可降级但 fd 保留、sidecar 非 privileged、业务读写成功；明确 warning 和 degraded 指标；杀 daemon 不假称已 recovery；U/I/E |
| D04 | /dev/fuse 不可用、内核 mount 权限/参数失败 | 明确失败，无 fd/挂载泄漏；不能自动改 privileged、退直接 mount 或吞错；U/I，C3 E |
| D05 | 非特权 securityContext、capability、设备与传播 | 检查实际运行容器不是只看模板；fd 模式 HostToContainer，非 Bidirectional；无无关 CAP_SYS_ADMIN/宿主设备，业务也非特权；I/E |
| D06 | CSI 创建内核挂载后 sidecar 延迟/不启动、fd 尚未接管 | NodePublish 可返回、Pod 可启动，不因 kubelet/CRI stat 阻塞死锁；超时/删除能回收；I/E |
| D07 | 错 UID/key/generation 的 fd、重复交接、多个 fd、截断/异常控制消息、客户端中途断开 | 身份关联、拒绝错投、fd 关闭和接管去重；无跨 Pod连接，/proc fd计数不增长；I |
| D08 | CSI 在建 mount 前后、文件写入前后、fd 接收前后重启 | 明确哪个进程持 fd；配置持久不等于 fd 持久；重试不假恢复原连接、不泄漏；I/E |
| D09 | 接管后 driver 启动失败、FUSE_INIT 卡住/失败、response/结果缺失 | 未初始化不 Ready；清理用不会因未服务 FUSE 卡 D-state 的路径；I/E |
| D10 | OSS2 daemon SIGKILL、并发真实 I/O、反复故障 | 复用既有 fd 持有与 supervise，恢复后原挂载/inode/业务引用有效，无透明重挂；I/E |
| D11 | recovery 重启失败、重试/backoff耗尽、取消与drain竞态 | 中间态/成功/耗尽与 probe/metrics 一致；终止后不再恢复，另一卷不受影响；I/E |
| D12 | recovery sysfs 缺失、只读、权限不足、flush失败 | 明确诊断；不能靠 privileged=true 随意修复或把坏状态报 healthy；I/E |
| D13 | mountproxy SIGKILL/OOM、Pod重建、节点重启 | 与 daemon recovery 区分；不扩大既有恢复保证；正常接管或明确不可恢复并清理，禁止假 Ready；I/E |
| D14 | 普通/支持failover的OSS2镜像、错误架构、旧版/不支持fd客户端、自定义镜像 | 可用镜像真实正例；不支持时明确错误而非盲目挂起/提权；driver能力不可仅看镜像名；U/I/E |
| D15 | OSS1/customfuse/RunD 显式开 fd或recovery；全局gate影响这些路径 | 不支持项明确拒绝或gate按既有driver范围不适用；不把OSS2能力强加全部driver；U/I/E拒绝例 |

## 6. customfuse 专项

| ID | 测试项 | 必须断言/层级 |
|---|---|---|
| C01 | 两个精确名称、近似/通配名字、普通容器同名、默认+custom共存 | custom 名称只精确匹配，歧义报错，默认名不冒充custom；U/I |
| C02 | 用户镜像带mountproxy、/entrypoint.sh或配置入口、额外args/env/ConfigMap | 复用已有入口契约，不解析容器command当身份；参数/目标/真实volumeID不丢失；U/I/E |
| C03 | 真实自定义FUSE客户端单卷/双卷与独立存储路径 | D/C各真实E，不以touch目录/模拟backend代替；读写/RO、远端独立验证、完整probe屏障 |
| C04 | 入口缺失/无执行权限/非0/提前0但未mount、挂错目标、fork子进程 | 明确失败，无假Mounted；进程组及继承stderr的子进程可回收；I/E |
| C05 | 自定义鉴权、配置/Secret volume缺失或错误、RRSA/Secret适配 | RunD仅RRSA、无节点回退；RunC显式volume；不假设OSS rrsa参数能鉴权任意FS；I/E |
| C06 | /dev/fuse、mount权限、传播、privileged显式允许/拒绝 | 独立权限契约，不能承诺任意custom均非特权；不得静默提权；业务容器无须特权；I/E |
| C07 | custom请求fd/recovery、两卷不同driver能力 | 明确不支持；混合权限整体生效；多消费者未设计前拒绝默认+custom两sidecar；U/I |
| C08 | 超时/取消、daemon异常、反复重试、删Pod/强删、CSI重启 | 套用完整生命周期回归、无子进程/挂载泄漏，其他卷和远端哨兵保留；I/E |

## 7. webhook 专项

| ID | 测试项 | 必须断言/层级 |
|---|---|---|
| W01 | annotation缺失/false/true/非法值，Deployment template | 显式注入、非目标不操作；key正式定义且幂等；U/I |
| W02 | annotation×SC key组合，webhook关闭/未安装 | 与controller/node四种分流一致；不因annotation自动改变SC；开key缺consumer明确失败；I/E |
| W03 | OSS与custom模板、合法手动sidecar、同名非法容器、多候选 | 不重复、不覆盖自定义镜像/入口；明确admission错误；U/I/E |
| W04 | init首位、其他mutation先后/重入、sidecar所需卷与probe | 多webhook组合后顺序正确或明确失败；不宣称prepend永远保证顺序；I/E |
| W05 | 单/多/重复PVC，OSS1/2/custom，非FUSE卷、空集合 | 正确按driver与能力选择，去重API，完整inventory，不给无关卷增加权限；U/I |
| W06 | PVC未绑定、Immediate供给、WaitForFirstConsumer、静态PV、PVC/PV消失 | 不阻塞admission等绑定；路径协议保持；未支持流程明确拒绝并标限制，不能记作通过；I/E |
| W07 | runcfd非特权、recovery降级、rund权限、custom直接挂载 | 权限/host挂载/sysfs按能力生成；无静默提权；调度前未知节点能力不能假定recovery必可用；U/I/E |
| W08 | RRSA投射/SA、Secretvolume、自定义ConfigMap、UID subPathExpr、资源 | 正确生成/保留，token目录无subPath，凭据不入env/annotation/JSON；U/I/E |
| W09 | API超时/403、webhook不可用/超时、TLS证书过期/轮换、升级 | 注入目标fail明确，非目标不被拖垮；failurePolicy/selector/timeout行为实测；I/E |
| W10 | 滚动变更、已有Pod不变、删除annotation、回滚/卸载webhook | 不破坏存量挂载；不会删除业务配置/Secret；I/E |
| W11 | 两种环境注入后的真实双卷挂载、失败、正常删除与强删 | 不能只验patch；probe、数据面和清理全链路；E |

## 8. 身份、凭据、隔离与数据安全（U/I/E）

| ID | 测试项 | 必须断言 |
|---|---|---|
| A01 | RRSA正确SA/role、issuer/audience/sub错误、STS/endpoint/DNS失败 | sidecar用业务身份；无host换证/节点角色/固定AK兜底；挂载阶段和首次I/O失败区分 |
| A02 | projected token真实轮转、到期、sidecar重启、业务持续I/O | 记录exp窗口但不打印token；持续读写证明客户端重新换证；长token仅临时测试窗口，不算轮转通过 |
| A03 | RunD显式Secret/其他authType；RunCSecretvolume各格式/缺失/轮换 | RunD拒绝且不落凭据；RunC遵守刷新契约，不支持热更新则明确要求重建，不悄悄用旧值 |
| A04 | 控制JSON、状态、stderr、RPC/日志、Event、metrics脱敏 | 无AK/SK/JWT/Secret正文；错误仍可定位卷、generation和原因 |
| A05 | 伪造保留名、恶意路径/符号链接、其他UID目录、旧请求重放 | 名称不授权CSI Secret；不能越界控制/清理；UID/卷归属校验明确 |
| A06 | host误删控制目录、卸载EBUSY、清理工具遇数据挂载 | 不递归遍历删除远端；隔离哨兵无损；无权请求不能授权卸载其他卷 |

## 9. 完整生命周期与既有路径回归（I/E）

| ID | 测试项 | 必须断言 |
|---|---|---|
| L01 | 正常删除/缩容/滚动升级/Job完成 | 业务先停、sidecar drain；挂载/进程/fd/控制状态最终消失，时序与耗时有记录 |
| L02 | 挂载中删、部分完成删、取消/重复SIGTERM、drain并发发布 | 终止后无新增挂载，已成功卷均清理，幂等无死锁 |
| L03 | preStop耗尽grace、sidecar强杀/OOM、Pod强删 | 不依赖hook/ACK；kubelet/CRI/NodeUnpublish兜底，旧UID退休，不能靠销毁集群证明清理 |
| L04 | 开文件/cwd/其他namespace引用导致EBUSY、unmount报错/卡住 | Pending/Unknown明确，其他卷继续；释放引用后确认回收；lazy detach不等于资源释放 |
| L05 | CSI在publish/unpublish/retire中崩溃，NodeUnpublish重复或缺失 | 重启对账，活UID不删、孤儿GC有界；不凭API NotFound或心跳过期认guest死亡 |
| L06 | kubelet/runtime/节点正常重启、失联再恢复 | 独立环境验证，新旧UID与bootID/namespace明确；恢复与清理不串代，目标环境缺权限则NOT RUN |
| L07 | 两Pod共享PV、同节点/不同节点、删一留一、不同RO选项 | Pod独占与globalmount不混用；存活副本I/O/daemon不受影响，无跨Pod卸载 |
| L08 | 新sidecar和旧独立FUSE Pod同时使用同PV，controller重复attach/unpublish | 共存和锁/引用关系正确，node不误删多余独立Pod，不因一条新路径清理共享挂载 |
| L09 | 旧runc默认独立Pod、OSS1/2/custom、Secret/RRSA、fd/recovery关开 | 数据面、鉴权、ReadOnly、metrics、controller回收、NodeUnstage不回归 |
| L10 | legacy rund/csi-agent/外部socket/COCO及历史RundCSIProtocol3 | 不改变原分流和挂载语义；未支持新能力明确拒绝 |

## 10. FUSE 监控、统计与可观测性回归

现有监控不能因 file 执行路径绕开 interceptor、拒绝 MetricsPath 或只提供 probe 就算覆盖。需核对 `FuseMonitorInterceptor`、MountMonitor、`pkg/metric/fuse_stat_collector.go`、NodeGetVolumeStats 和 CSI RPC 指标的真实链路；如 guest 指标不可由 host 直接采集，必须给出明确的 sidecar 导出/汇聚契约并验证，而不是读取 host `/proc` 冒充guest进程数据。

| ID | 测试项 | 必须断言/层级 |
|---|---|---|
| M01 | mount初始/成功/失败、重试、daemon退出 | status、retry count、exit reason与实际状态一致；file和socket都经过正确monitor；U/I/E |
| M02 | recovery退出/重试/恢复成功/耗尽 | failover count、status和last reason正确，不因ExitChan和recovery回调重复计数；U/I/E |
| M03 | 不支持节点的RecoveryDegraded | `recovery_degraded` 文件及最终exported指标一致；degraded不冒充recovery成功、fd仍工作；U/I/E |
| M04 | FUSE客户端CPU/内存/进程、卷统计/容量、NodeGetVolumeStats、RO | 指向真实daemon/volume，不读错PID namespace；不支持的统计明确，不返回伪零；I/E |
| M05 | OSS1/OSS2/custom和多卷、多Pod同PV指标标签 | driver、PV/真实handle、PodUID/target映射正确；无串卷/旧PID复用/重复时序或凭据标签；U/I/E |
| M06 | 正常清理/失败清理/sidecar重启/新generation | 指标文件与collector生命周期正确；旧指标退场、不误删其他卷、不长期保留假healthy；I/E |
| M07 | MetricsPath路径隔离/guest不可见/只读/写失败，collector重启 | 不暴露任意host路径；错误可见且不改变安全边界；指标故障与挂载失败按契约区分；U/I/E |
| M08 | `/metrics`真实抓取、现有告警依赖指标、CSI挂载耗时 | 用实际collector输出验证，不只assert临时文件；异步交付耗时不能冒充FUSE-ready耗时；I/E |
| M09 | FUSE未初始化、daemon死但fd仍被持有、recovery中抓取 | 采集/健康检查不陷入statfs D-state，不拖住全节点collector；I/E |
| M10 | 既有独立FUSE Pod监控和指标清理 | 新协议不能导致旧指标缺失、label变化、重复monitor或错误卸载；I/E |

## 11. 开销、发布与验收证据

| ID | 测试项 | 必须断言/层级 |
|---|---|---|
| P01 | 1/10/100卷，冷/热缓存及首位正常路径 | Pod/PVC/PV/Node/RuntimeClass API计数明确；首位无额外配置/PVC请求；webhook开销单独统计；U/I |
| P02 | 多Pod并发、控制文件/心跳/probe频率、CSI/sidecar资源 | CPU、RSS、fsync/IOPS、FD/进程数、队列/锁等待，区分逐卷和逐Pod成本；I/隔离规模E |
| P03 | 启动p50/p95/p99分阶段与Ready延迟 | admission、供给、CSI交付、内核mount、fd接管、daemon ready、业务放行分别记录；不以单函数基准承诺生产SLO |
| P04 | 长稳运行+重复创建删除、recovery压力、挂载失败压力 | 内存/FD/控制目录/僵尸及mount数量不无界增长；远端数据安全 |
| P05 | chart默认/开关、webhook关闭、升级/回滚、混合新旧组件 | 默认旧路径不变；feature gate/镜像能力一致性可诊断；不自动迁移活跃挂载；U/I/E |

每个用例必须记录 PASS/FAIL/NOT RUN、代码提交、镜像digest/客户端版本、环境能力、测试层级和证据位置；未实现项不能填PASS。先跑有意义的U/I，再选关键E；故障注入重跑对应回归，不反复全量E2E。新目标的单PR合并条件是全部约定能力和P0生命周期/安全项通过；目标环境授权或运行时缺陷导致的未验收须明确列出，不能自动视为允许带缺口合并。

真实链路至少保存：Pod/CSI/sidecar时间线、逐卷generation/session/phase、probe和脱敏Event、fd/进程/mount namespace归属、实际业务读写与远端哨兵校验、最终无残留证据。fd/recovery还要证明原连接/既有业务引用有效；非特权要核对实际Pod和进程权限。资源信息、JWT、AK/SK和原始含敏感JSON仅保存在忽略目录，不提交。先证明产品回收，再销毁测试云资源，最后独立核查费用资源已删除。
