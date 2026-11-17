# 不使用 webhook 时配置 RunD FUSE sidecar

用户可以直接在 Deployment 的 Pod 模板中配置 mountproxy sidecar，不需要部署 webhook，也不需要在业务容器中启动挂载程序。当前实现使用 Kubernetes 1.33+ 原生 sidecar，配置在 `initContainers` 中，并设置 `restartPolicy: Always`。

## 1. 集群侧配置

CSI 使用本分支镜像，controller 和 node 统一开启已有 gate：

```yaml
# CSI Helm values；不是业务 Deployment 的字段
deploy:
  featureGates: RundCSIProtocol3=true
fuseSidecarWebhook:
  enabled: false
```

ACK 安全沙箱使用 `runtimeClassName: runv`。也支持 handler 为 `runv`、调度 selector 为 `alibabacloud.com/container-runtime: Sandboxed-Container.runv` 的 RuntimeClass 别名；实际 Node 也必须匹配。不能只把普通 runc 的 class 改名为 rund。

## 2. 两卷 OSSFS2 Deployment

先准备 namespace `fuse-file-demo`，以及下面两个已绑定的 CSI PVC。完整 PV、PVC 和 Deployment 示例见 [demo.yaml](../examples/oss/file-sidecar/demo.yaml)；其中还需替换 OSS 桶和镜像，并创建对应的凭据 Secret。已有 PVC 时只需部署下面的 Deployment。

| Pod volume 名称 | PVC 名称 | PVC 绑定的 PV 名称 | PV 的 CSI volumeHandle |
| --- | --- | --- | --- |
| first | oss-first | ossfs2-file-a | ossfs2-file-a |
| second | oss-second | ossfs2-file-b | ossfs2-file-b |

下面例子恰好让 PV 名与 volumeHandle 相同，实际可以不同：**sidecar 的目标路径使用 PV 名，`--file-expected-volumes` 使用 volumeHandle，`volumes` 引用 PVC 名。** 可以用 `kubectl get pvc` 查看绑定关系，再用 `kubectl get pv <PV名> -o yaml` 查看 handle。

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ossfs2-reader
  namespace: fuse-file-demo
spec:
  replicas: 1
  selector:
    matchLabels: {app: ossfs2-manual-reader}
  template:
    metadata:
      labels: {app: ossfs2-manual-reader}
      # 即使将来安装了 webhook，也保留用户自己的 sidecar。
      annotations:
        fuse.csi.alibabacloud.com/inject: "false"
    spec:
      runtimeClassName: runv
      automountServiceAccountToken: false
      terminationGracePeriodSeconds: 45
      # 私有仓库需要自行创建 Secret，并按需取消注释。
      # imagePullSecrets:
      #   - name: registry-credentials
      initContainers:
        - name: mountproxy
          # 包含本分支 mountproxy 和 ossfs2 客户端，替换后再应用。
          image: REGISTRY/mount-proxy-ossfs2:TAG
          command: [csi-mount-proxy-server]
          restartPolicy: Always
          securityContext:
            privileged: true
            runAsUser: 0
          resources:
            requests: {cpu: 500m, memory: 512Mi}
            limits: {cpu: "2", memory: 1Gi}
          env:
            - name: POD_UID
              valueFrom:
                fieldRef: {fieldPath: metadata.uid}
          args:
            - --driver=ossfs2
            - --file-state-dir=/volume-state
            - --file-target-dir=/pod-volumes
            - --file-expected-volumes=ossfs2-file-a,ossfs2-file-b
            - --timeout=30s
          startupProbe:
            exec:
              command:
                - csi-mount-proxy-server
                - --file-check-ready
                - --file-state-dir=/volume-state
                - --file-expected-volumes=ossfs2-file-a,ossfs2-file-b
            periodSeconds: 2
            timeoutSeconds: 2
            failureThreshold: 60
          readinessProbe:
            exec:
              command:
                - csi-mount-proxy-server
                - --file-check-ready
                - --file-state-dir=/volume-state
                - --file-expected-volumes=ossfs2-file-a,ossfs2-file-b
            periodSeconds: 2
            timeoutSeconds: 2
            failureThreshold: 3
          volumeMounts:
            - name: state
              mountPath: /volume-state
              subPathExpr: $(POD_UID)
            - name: first
              mountPath: /pod-volumes/ossfs2-file-a/mount
              mountPropagation: Bidirectional
            - name: second
              mountPath: /pod-volumes/ossfs2-file-b/mount
              mountPropagation: Bidirectional
      containers:
        - name: reader
          image: mirrors-ssl.aliyuncs.com/busybox:1.36
          command: [sh, -ec]
          args:
            - |
              ls -la /first /second
              while true; do sleep 10; done
          volumeMounts:
            - name: first
              mountPath: /first
              mountPropagation: HostToContainer
            - name: second
              mountPath: /second
              readOnly: true
              mountPropagation: HostToContainer
      volumes:
        - name: state
          hostPath:
            path: /var/run/ossfs/file-volumes
            type: DirectoryOrCreate
        - name: first
          persistentVolumeClaim: {claimName: oss-first}
        - name: second
          persistentVolumeClaim: {claimName: oss-second, readOnly: true}
```

资源配置是起点，需按卷数、缓存、客户端线程数和业务吞吐调整；RunD 还需要为业务和沙箱 overhead 留出资源。

## 3. 必须保持一致的配置

- `POD_UID` 由 Downward API 提供，配置目录使用 `subPathExpr`。不要手写 UID，不要把其他 Pod 的配置一起挂进容器。
- `/var/run/ossfs/file-volumes` 必须与 host CSI 的 `CSI_FUSE_FILE_ROOT` 一致。sidecar 需要读 desired，也需要写 status/session，因此不能把整个 state 挂成只读。
- `--file-state-dir` 与 state 的 mountPath 一致；`--file-target-dir` 加 `<PV名>/mount` 与 sidecar 的 PVC mountPath 一致。
- runner 和两个探针的预期 volumeHandle 集合相同，包含该 sidecar 管理的全部卷。增加卷时同步修改参数、探针、sidecar volumeMounts 和业务 volumeMounts/volumes。
- sidecar 和业务引用相同的 Pod PVC volume，分别使用 Bidirectional 和 HostToContainer。不要另挂 kubelet CSI 目录的 HostPath 别名；真实 RunD 测试中该别名不能传播到业务 PVC。
- 不要省略 startupProbe，也不要把 sidecar 移到普通 `containers` 中。前者保证首次挂载成功后才启动业务，原生 sidecar 保证 Job 完成与停止顺序。
- 业务只读卷应显式配置 `readOnly: true`。当前不支持业务 FUSE 卷的 subPath/subPathExpr；state 的 UID subPathExpr 是另一回事。

挂载所需的 OSS Secret 由 PV 的 `nodePublishSecretRef` 交给 CSI，再进入 UID 隔离的请求文件；不需要给 sidecar 再注入明文 AK 环境变量，也不需要让 sidecar 访问 Kubernetes API。

## 4. ossfs 和 customfuse 怎么改

| 类型 | 镜像及参数 | state 的 hostPath |
| --- | --- | --- |
| ossfs2 | 包含 ossfs2 的 mountproxy 镜像，`--driver=ossfs2`，PV `fuseType: ossfs2` | `/var/run/ossfs/file-volumes` |
| ossfs | 包含 ossfs 的镜像，`--driver=ossfs`，PV `fuseType: ossfs` | `/var/run/ossfs/file-volumes` |
| 同组 ossfs + ossfs2 | 镜像包含两种客户端，`--driver=ossfs,ossfs2`，一个 sidecar 管理对应卷集合 | `/var/run/ossfs/file-volumes` |
| customfuse | 镜像包含 mountproxy 和客户 `/entrypoint.sh`，`--driver=customfuse`，PV driver 为 `customfuseplugin.csi.alibabacloud.com` | `/var/run/customfuse/file-volumes` |

customfuse 完整例子见 [demo.yaml](../examples/customfuse/6-file-sidecar/demo.yaml) 和 [镜像说明](../examples/customfuse/6-file-sidecar/README.md)。同一个 Pod 同时使用 OSS 和 customfuse 时，配置两个原生 sidecar，各用独立的 state volume、预期卷集合和 PVC mount；都通过 startupProbe 后才启动业务。

## 5. 观察结果与生命周期

```sh
kubectl -n fuse-file-demo rollout status deployment/ossfs2-reader --timeout=180s
kubectl -n fuse-file-demo describe pod <POD_NAME>
kubectl -n fuse-file-demo logs <POD_NAME> -c mountproxy
kubectl -n fuse-file-demo logs <POD_NAME> -c reader
```

sidecar 内 `/volume-state/status/` 可查看逐卷结果，`desired/` 含凭据，不要直接打印。客户端日志也可能包含敏感内容，分享前需检查。

初次挂载失败时 startupProbe 不通过，业务不启动；上述探针会在持续失败约两分钟后重启 sidecar。挂载成功后的客户端故障使 Pod NotReady，不自动重启业务或透明恢复连接，通常需要重建 Pod。优雅终止时业务先停，mountproxy 收信号后显式卸载，再结束客户端；不需要额外编写 PreStop hook。

完整边界见 [生命周期说明](rund3-fuse-design-review-zh.md#3-生命周期)。当前示例是 hostPath + UID subPathExpr 方案；若以后由 RunD 直接提供 Pod 专属配置 bind mount，需要另行对齐 state 的挂载接口和状态回写路径，不影响业务与 sidecar 共享同一 PVC 的要求。
