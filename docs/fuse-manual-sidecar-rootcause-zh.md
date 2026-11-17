# 手动 FUSE sidecar：根因排查与修复记录

本轮日期：北京时间 2026-11-18。范围仍是集群 CSI、RunD、手动原生 sidecar、文件交付；未增加 webhook 或 runc sidecar 功能。runc Pod 只用于原生文件行为对照。

前一版把未实现的绑定验证、清理接管和 GC 直接归为“外部阻塞”，结论不成立。本轮逐项验证责任边界；以下事实与旧镜像记录分开，旧失败不被改写成通过。

## 1. 两卷映射交换：我们的验证缺失

根因：原代码只检查 `/pod-volumes/<PV>/mount` 这个 key 出现在 sidecar 中，没有验证该 mount 引用的 PVC 实际绑定哪个 PV。两个 key 都合法时，交换 volumeMount.name 仍通过，业务读到相反 prefix。

修复：提交配置前，从标准 PVC API 查询 Bound claim 的 `spec.volumeName`，与实际 CSI target 中的 PV 名比较。不依赖用户补写映射注解，不信任额外 PV 字符串，也不查询 RuntimeClass 对象。

成功绑定按 PodUID/namespace/claim 缓存在 4096 项 LRU 中。依据是标准 [PVC 使用保护与绑定关系](https://kubernetes.io/docs/concepts/storage/persistent-volumes/#storage-object-in-use-protection)：正常 Kubernetes 生命周期中，正在使用的 claim 不会被删除重建，Pod 的 volume source 也不能随意更换。管理员强制移除保护并破坏绑定不属于缓存的正确性前提。

成本不能再写成“所有新逻辑零 API”：

- RuntimeClass 分类：仍复用原来的一次 Pod GET，没有新增查询。
- 冷绑定校验：每个 Pod/claim 一次额外 PVC GET。
- 缓存命中：不再 GET PVC；错误和未绑定结果不缓存。
- CSI 重启或 LRU 淘汰后重新校验；没有全集群 PVC list/watch/informer，没有新增 RBAC 权限。

1/10/100 卷计数与重试、PodUID 更换、并发发布均有本地回归。真实集群交换两个 PVC 后，FailedMount 明确提示绑定关系，业务未启动，没有发布可执行 desired。混合 ossfs1/ossfs2 后续验证使用同一套固定 sidecar 参数。

补充配置保护：PV 的 runtimeclass 声明不能选择实际 Pod runtime。已明确拒绝实际 runc、未声明或空 RuntimeClass 的文件 sidecar；否则可能误把 guest 内挂载方案用于宿主机。显式未知 sandbox 类仍按既定分支处理，运行时隔离能力由该 RuntimeClass 的管理员配置保证，不能从名字自动证明。

## 2. 无 ACK 清理：我们把标准生命周期设计错了

根因：把 guest ACK 设为 NodeUnpublish 的必要成功条件，但 kubelet 要先停止 Pod 容器，随后才发起卷卸载。sidecar 被 SIGKILL 或整个 sandbox 停止后，消费者不能再响应，永远等待 ACK 是我们制造的死结。

排查依据为 Kubernetes v1.33.3 源码：

- [卷管理器](https://github.com/kubernetes/kubernetes/blob/v1.33.3/pkg/kubelet/volumemanager/populator/desired_state_of_world_populator.go#L210) 在 Pod 容器仍未终止时保留卷；API 对象消失不是卸载条件。
- [KillPod](https://github.com/kubernetes/kubernetes/blob/v1.33.3/pkg/kubelet/kuberuntime/kuberuntime_manager.go#L1455) 停止容器并调用 StopPodSandbox，失败进入 kubelet 重试。
- [CRI 的 Stop/RemovePodSandbox 契约](https://github.com/kubernetes/cri-api/blob/master/pkg/apis/runtime/v1/api.proto#L27) 要求停止相关进程；sandbox 最终资源由运行时回收。

修复后的责任：

```text
正常退出：业务结束 → 原生 sidecar SIGTERM → 尽力 drain，记录结果
异常退出：kubelet → CRI 停止容器/sandbox；不要求已经死掉的进程回 ACK
NodeUnpublish：记录 ACK 是否存在 → 安全清理 host target → 回收自有控制文件
运行时后续 GC：释放 guest/container/sandbox 的 namespace 和 VM 资源
```

ACK 仍保留优雅卸载结果与错误，不能伪造；缺失 ACK 不再阻塞 kubelet 的标准卸载。也没有用 Pod NotFound、超时或猜测 PID 代替运行时停止。

同时修正了信任边界：共享 JSON 可能丢失、损坏或被改成别的 UID/target，不能用它授权删除，也不能让它阻止合法 CO 请求清理。NodeUnpublish 始终只处理 CO 指定的 host target 和由该 target 派生的 UID/key；不使用 JSON 中的其他路径。活消费者执行显式 UnmountRequest 时仍严格验证身份和 generation，不混淆这两种场景。

本轮没有新增 CRI 客户端、进程扫描、私有“guest 已销毁”标记或另一个 agent。若某个 runtime 在标准停止成功后仍留有存活的 FUSE 进程，应记录为它的契约问题；CSI 不接管 VM 管理。

真实集群已验证：SIGKILL sidecar 后先确认没有 Unmounted ACK，重启 CSI 保留未释放状态，然后强删整个 Pod。新 Pod 双卷读取恢复；旧 UID 的两次 NodeUnpublish 成功，控制目录删除。全程没有直接调用 Stop/RemoveSandbox 绕过 kubelet。

## 3. 控制文件 GC：我们的缺失实现与并发缺陷

已实现：host target 清理成功后，按 UID/key 删除本卷的已知控制文件；其他尚有 desired/status/unmount 文件的卷保留。没有其他卷的已知控制键时，清理已知平面文件和空目录。没提交成功的 inventory 成员可由后续孤儿 GC 退休。

后台每 5 分钟以及 CSI 启动时，检查 kubelet 的本地 Pod 目录；只有该目录已不存在，才回收遗留的自有控制文件。kubelet pods 根目录缺失或检查失败时保留。此检查用于元数据回收，不是一个独立 guest 消亡证明。

删除使用 Go 标准库 [os.Root](https://pkg.go.dev/os#Root) 限制在 UID 控制目录内，不使用 RemoveAll、不解析 desired.Target 去删除数据、不沿符号链接进入其他目录。未知内容保留，数据目标非空时仍返回错误。

真实双卷并发回收暴露了一个竞态：另一卷刚删除 UID 目录，本卷的 readdirent 返回 ENOENT，被当成清理失败。已按幂等退休处理，并补 25 轮多卷并发真实文件系统测试。

已验证正常删除、无 ACK 强删、部分挂载失败、sidecar 尚未启动的部分交付、挂载中取消，以及模拟 CSI 崩溃后遗留控制文件的启动 GC。活跃 UID 和业务 I/O 保留。

又用“B 的 desired 被误删，只先回收 A”补了一条失败回归：旧 GC 提前删除 B 的结果。已修复为其他卷的 status/tombstone 也保留到它自己的 Unpublish；无法发布成功的 Pending 元数据可由后续孤儿 GC 回收。此处先复现测试失败，再修改实现，不以“文件不存在”推导其他卷已经退休。

## 4. 宿主机目录非空：一次测试工具污染

宿主机删除安全测试创建了自己的空子目录，没在收尾时 rmdir。后续 NodeUnpublish 因目标目录非空而拒绝删除，是正确的防误删行为，不是应当放松的产品限制。

排查确认仅有该测试创建的空目录后，只对其执行 rmdir；随后 kubelet 正常重试完成清理。没有在 CSI 中增加递归删目录、忽略 ENOTEMPTY 或强制删除逻辑。远端哨兵通过独立 OSS API 确认保留。

## 5. HostPath 与 projected 更新：已隔离到运行时共享文件接口

为了排除我们的 JSON/轮询/sidecar 代码，单独创建三个普通 Pod：runc writer、runc reader、rund reader。没有 CSI 卷、FUSE 或 mountproxy；每次读取都新启动 cat。

| 操作 | runc reader | rund reader |
|---|---|---|
| 目录直接挂载，host 原子替换 v1→v2 | 读到 v2 | 仍读 v1 |
| 目录 subPath，相同操作 | 读到 v2 | 仍读 v1 |
| host 删除旧文件 | 新打开报不存在 | 仍读旧文件 |
| 新建一个新名字的文件 | 能读取 | 能读取 |
| 标准 ConfigMap 更新 | 约 58 秒读到 v2 | 约 146 秒窗口末仍为 v1 |
| projected ConfigMap 更新 | 约 58 秒读到 v2 | 同样仍为 v1 |

针对 rund Pod 又单独确认：kubelet 的 host ConfigMap/projected 文件均已是 v2，guest 仍为 v1。guest 对这四种目录暴露的文件系统均是 `virtio_fs`，带 `dax=inode`；containerd 使用 io.containerd.rund.v2。

因此可以排除 CSI JSON 解析、我们持有旧 FD、误用了“单文件 subPath”、以及 kubelet 没更新投射内容。已定位到该测试运行时暴露的 host/guest 文件接口。不能凭这些观测断言具体是哪一个缓存函数、参数或 agent 版本出了错；更深的实现不可见，应交给运行时维护者确认。

社区对照：[Kata mount 分类](https://github.com/kata-containers/kata-containers/blob/main/src/runtime/virtcontainers/mount.go#L400) 对部分 Secret/ConfigMap 使用 [watchable-bind 路径](https://github.com/kata-containers/kata-containers/blob/main/src/runtime/virtcontainers/container.go)。这不证明测试版本包含同样实现，也不是通用 HostPath/projected 一致性保证。不把这套镜像同步机制复制进 CSI 来掩盖运行时问题。

测试版本是 RunD 家族的 runv 2.2.4 变体；只能报告该版本的结果，不能据此宣称用户实际 rund 环境必然相同。最小复现见 [独立 YAML](../examples/oss/file-sidecar/file-coherence-repro.yaml)，不含凭据或环境资源标识。

复现步骤：替换 YAML 的 TEST_NODE_NAME，应用后等待三个 Pod 运行并确认初始读到 v1；然后执行：

```sh
kubectl -n csi-file-coherence exec writer -- sh -ec 'printf v2 > /probe/direct/.next; mv /probe/direct/.next /probe/direct/value'
kubectl -n csi-file-coherence exec reader-runc -- cat /direct/value
kubectl -n csi-file-coherence exec reader-rund -- cat /direct/value
kubectl -n csi-file-coherence patch configmap coherence --type=merge -p '{"data":{"value":"v2"}}'
```

轮询两种 reader 的 `/config/value` 和 `/projected/value`。目录 subPath 同样可对 `/probe/nested/value` 原子替换后读取 `/nested/value`。若测试删除，只有 writer 的该测试文件可以 rm；收尾只 rmdir 已知空测试目录，不递归删 HostPath 根目录。此复现无需读取真实 RRSA token。

## 6. RRSA：同一投射链路的限制，保留外部问题

此前已确认 host 轮换到新 token、guest 仍读旧 token甚至过期。本轮不再读取或传播 token，而用无敏感信息的标准 projected ConfigMap 重现相同文件更新问题，证明不能仅通过调整 OSSFS 鉴权代码解决投射可见性。

仍使用业务 Pod SA 的 RRSA，STS 交换在 guest 内进行；没有把节点身份或固定 AK 注入 guest，没有 token mirror。86400 秒 token 只是已同意的测试窗口，不计为轮转通过。

剩余需要运行时提供的是：标准目录挂载的更新/删除可见性，以及 ConfigMap/projected AtomicWriter 更新在 guest 内的正确传播。我们保留标准投射、原子文件写入和最小复现，不用原地覆盖 JSON、猜 revision 路径或私有运行时分支换取测试变绿。

## 7. 已补充的生命周期与成本证据

- 混合 ossfs1/ossfs2 使用同一镜像、相同固定 args/probe 和业务 SA RRSA，两卷读取及退休通过。初次失败来自测试把 ossfs2 可接受的路径写法直接用于 ossfs1；修正测试路径，没有放松产品既有验证。
- 使用标准 Pod DNS 配置阻塞挂载；确认两卷均 Mounting 后删 Pod，约 3.2 秒完成控制状态回收，业务未启动。
- 自建节点正常云平台重启，bootID 变化后同一 PodUID 自动恢复两卷，独立 OSS 哨兵仍在；约 318.5 秒包含整机重启与恢复。没有手动重建 Pod或打开 requiresRepublish。
- 冷/热 API 次数分别测试 1/10/100 卷。Linux arm64 本地绑定缓存命中基准约 0.29 微秒、200 B/3 allocations；只是校验函数，不是完整 NodePublish 或 API 延迟。
- 真实 kubelet Readiness exec 指标：双卷、47 次样本，均值约 27.6 毫秒，p50/p95/p99 所在累计桶上界均为 50 毫秒。这是单节点小样本，不是 CPU 消耗或生产规模 SLO。
- 最终 v12 镜像验证：sidecar 前的 init 使用数据 PVC 时明确拒绝；损坏 desired 导致 NotReady 但不丢已有 I/O，标准删除仍完成；双副本共享 PV 缩容后，仅被删除 UID 退休，存活副本 daemon PID 和双卷读取保持。

没有把客户端 QPS 限制自动调大来掩盖冷 GET 成本；也没有把缺少大规模压测包装成运行时问题。
