# Manual customfuse sidecar example

中文配置步骤和多 driver 说明见 [手动 sidecar 指南](../../../docs/rund3-fuse-manual-sidecar-zh.md)。

This example uses the shared [RunD 3 FUSE protocol](../../../docs/rund3-fuse.md).
Enable `RundCSIProtocol3=true` on the CSI installation. The Pod uses
`runtimeClassName: runv` on ACK sandbox and opts out of automatic injection so it can supply its
own native sidecar. Kubernetes 1.33+ and the mount/privilege prerequisites in the
protocol documentation are required.

The fixture image contains mountproxy and a small read-only Python FUSE client.
It requires no cloud credentials. From the repository root:

```sh
podman build --platform linux/amd64 -f examples/customfuse/6-file-sidecar/Dockerfile -t YOUR_REGISTRY/fuse-file-demo:dev .
podman push YOUR_REGISTRY/fuse-file-demo:dev
sed 's|FUSE_FILE_DEMO_IMAGE|YOUR_REGISTRY/fuse-file-demo:dev|' examples/customfuse/6-file-sidecar/demo.yaml | kubectl apply -f -
kubectl -n fuse-file-demo rollout status deployment/reader --timeout=180s
kubectl -n fuse-file-demo logs deployment/reader -c reader
```

If the base-image mirror is unreachable, pass
`--build-arg DEMO_BASE_IMAGE=docker.io/library/debian:bookworm-slim`.
Set imagePullSecrets for a private registry. Match the state hostPath to the CSI
installation. Share the existing PVCs at `/pod-volumes/<PV name>/mount` instead
of mounting a second HostPath alias of the kubelet tree. The two replicas each mount two volumes and must read
their distinct contents.

For automatic injection, configure a customfuse operator image profile, remove
the manual initContainer and its state hostPath volume, and remove the
injection opt-out annotation. The webhook adds the sidecar and propagation.
For native OSS clients see the [OSSFS2 example](../../oss/file-sidecar/README.md).

Use a Job with the same Pod spec and `restartPolicy: Never` to check completion,
removing the reader's final sleep loop. Exercise missing/failed mounts, two-Pod
isolation, process exit, sidecar restart and graceful deletion as described in
the shared protocol documentation. Existing application binds may remain tied
to an old FUSE connection after a sidecar restart; recreate the Pod.

The fixture always mounts read-only. Real custom clients must honor the
`readOnly` option passed to their entrypoint. See the shared documentation for
request/response semantics, credentials, feature compatibility and test coverage.
