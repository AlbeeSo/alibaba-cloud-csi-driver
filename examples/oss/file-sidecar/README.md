# 业务 Pod 内的 FUSE mountproxy

[demo.yaml](demo.yaml) 是关闭 webhook 时的 RunD RRSA 双卷 Deployment 模板。替换 bucket、endpoint、角色/OIDC 与兼容镜像占位符；先按业务 ServiceAccount 配置 RRSA 信任。RuntimeClass 必须对应真实运行环境，不能仅新建同名空壳；RunD 不支持 fd-passing/recovery。

两个静态 PV 已声明 `volumeAttributes.mountMode: sidecar`，controller 不创建独立 FUSE Pod。动态卷可在 StorageClass 的 `parameters.mountMode: sidecar` 声明，由 CreateVolume 透传到 PV。仅 annotation 不会替卷设置该参数。

mountproxy 放在首个 `initContainers`，使用 `restartPolicy: Always`，名称固定 `alibabacloud-csi-mountproxy`。启动参数固定：

```yaml
command: [csi-mount-proxy-server]
args: [--file-mode]
```

startup/readiness probe 均执行 `csi-mount-proxy-server --file-check-ready`。启动屏障要求该 Pod 完整卷集都成功；不是只检查一个卷，也不是仅检查进程存活。增加卷只需更新 Pod 的 volumes/volumeMounts，不需要 expected-volumes 或按客户端修改 args。首位 sidecar 不需要额外绑定元数据，也没有 PVC GET；示例的绑定字段仅供异常顺序下诊断。

`POD_UID` 只能由唯一的 metadata.uid Downward API 环境变量提供。宿主机控制根目录通过 `subPathExpr: $(POD_UID)` 映射到 `/volume-state`，每个 Pod 只能看到自己的请求；不能将所有 Pod 控制目录直接暴露给消费者。

数据路径保持 `/pod-volumes/<PV-name>/mount`，不是 PVC 名或 volumeHandle。同一 PVC 的第二个 `/run/fuse.ossfs/<PV-name>/globalmount` projection 用于既有 OSS SDK 的指标路径；不是新卷或新 hostPath。可以显式 `use_metrics=false` 关闭 SDK IO 指标，但 mountproxy 生命周期指标仍保留。native FD 的 SDK 逐卷 IO 目前仍有验收缺口，见[结果记录](../../../docs/fuse-sidecar-unified-results-zh.md)。

## RunC OSS2 非特权配置

以同一模板为基础：

1. PV 的 `runtimeClass` 改为 `runc`，保留 `mountMode: sidecar`；Pod 选择已确认使用共享宿主机内核的 handler。没有名为 runc 的 RuntimeClass 时，不要凭 nil 默认值猜测运行环境。
2. mountproxy 的 PVC volumeMount（两组路径）改为 `mountPropagation: HostToContainer`，安全上下文如下；业务容器仍无需特权。
3. 如启用 PV `recovery: "true"`，需要兼容 failover 的 OSS2 镜像，并向 mountproxy 显式投影可写 `/sys/fs/fuse/connections`。这是节点 FUSE 控制权限，只应交给可信消费者。未启用 recovery 不需要该投影。

```yaml
securityContext:
  runAsUser: 0
  runAsNonRoot: false
  privileged: false
  allowPrivilegeEscalation: false
  capabilities:
    drop: [ALL]
```

配置与状态仍走 file；FD 自动经同内核 Unix socket SCM_RIGHTS 交付，无需新的 mode 参数。CSI 在 sidecar 启动并请求接管后才建立内核挂载，不能提前同步等待 sidecar。

RunC 也可在 PV 使用 `secretRef: <同 namespace 的 Secret 名>`、省略 authType，并显式向 mountproxy 挂载只读 Secret 目录 `/volume-secrets/<Secret名>`。OSS2 所需 key 为 AccessKeyId、AccessKeySecret、SecurityToken；使用短期 STS 并按既有客户端契约更新 Secret。CSI 不会将凭据复制到 desired JSON；没有显式 Secret volume 就拒绝。RunD 仅允许 RRSA。

## customfuse 与可选 webhook

customfuse 使用独立名称 `alibabacloud-csi-customfuse-mountproxy`、customfuse PV driver 和包含自定义入口的镜像。入口负责实际客户端、RRSA/Secret 消费；不支持 fd-passing/recovery，两类运行时均由可信 privileged mountproxy 直接挂载。不要同时在一个 Pod 声明 OSS/custom 两个保留消费者，当前会明确拒绝。详见[设计](../../../docs/fuse-sidecar-delivery-design-zh.md)。

启用 chart 的 `fuseSidecarWebhook.enabled` 并配置 TLS/镜像后，在 Deployment 的 **Pod template** 设置：

```yaml
metadata:
  annotations:
    csi.alibabacloud.com/inject-mountproxy: "true"
```

此时可省略手工 mountproxy、控制目录及凭据投影，由 webhook 生成。业务 PVC 与应用 volumeMount 仍由用户声明。卷必须已 Bound；WFFC/未绑定卷会在 admission 明确拒绝。直接挂载还需 chart 显式允许 privileged 注入。无 annotation 不注入，合法手工消费者不会重复注入。详细 chart 配置见[webhook 文档](../../../docs/fuse-sidecar-webhook-zh.md)。

## 失败与回收排查

先用 `kubectl describe pod <pod>` 区分两类失败：缺少消费者、权限或投影结构不符，会在 NodePublish 产生 FailedMount；已投递后客户端失败则显示 init startupProbe Unhealthy/不 Ready，查看 mountproxy 日志和 `/volume-state/status/` 中逐卷 phase/reason。不要将 `/volume-state/desired/` 的完整请求复制到公开 issue。

正常删除由 native sidecar drain，再由 NodeUnpublish 完成宿主机回收。非特权消费者写 Released 而非 Unmounted；guest/direct 成功卸载写 Unmounted。强删不依赖 ACK，runtime/kubelet 和 CSI 兜底，不能只看到 API Pod 消失就宣称已清理。禁止通过递归删除数据路径“修复”残留挂载。

启动后的 daemon 故障由具备能力的 OSS2 recovery 处理；mountproxy 整个主进程被强杀不在原地恢复保证内，可能需要重建 Pod。readiness 撤销不会终止已运行的应用。完整生命周期与限制见[验收记录](../../../docs/fuse-sidecar-unified-results-zh.md)。
