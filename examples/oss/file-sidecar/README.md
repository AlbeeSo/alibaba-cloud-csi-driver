# Manual OSSFS2 sidecar example

Use the [shared RunD 3 FUSE protocol](../../../docs/rund3-fuse.md) with
`RundCSIProtocol3=true` on the self-managed CSI installation and
`runtimeClassName: runv` on the ACK sandbox Pod. This manifest opts out of webhook injection
and supplies a native sidecar explicitly. Kubernetes 1.33+ is required.

Build a mount-proxy image from `build/mount-proxy/Dockerfile` with target
`ossfs2`. It must include this branch's mountproxy executable. For automatic
injection use an operator OSS profile with an `aio` image containing both ossfs
and ossfs2. Remove the manual initContainer, its state hostPath, and the
injection opt-out annotation; the webhook constructs them.

Create a private test bucket in Beijing with `first/hello.txt` containing
`first-ossfs2-volume` and `second/hello.txt` containing `second-ossfs2-volume`.
Create Secret `ossfs2-file-credentials` in `fuse-file-demo`, preferably using
bucket-scoped temporary STS credentials. Keys are `AccessKeyId`,
`AccessKeySecret`, `SecurityToken`, `Expiration`. Configure imagePullSecrets for
private images. Replace `OSS_BUCKET` and `OSSFS2_FILE_IMAGE` before applying.
Change endpoint/region for the target environment. Share the existing PVCs with
the sidecar at `/pod-volumes/<PV name>/mount`; do not substitute a HostPath alias
of the kubelet volume tree.

```sh
kubectl apply -f rendered-example.yaml
kubectl -n fuse-file-demo rollout status deployment/ossfs2-reader --timeout=180s
kubectl -n fuse-file-demo logs -l app=ossfs2-file-reader -c reader
```

The two replicas each mount two independent prefixes, read both objects, write a
Pod-UID-specific object to the first prefix, and confirm the second rejects
writes. Verify `first/write-<podUID>` through the OSS API too, so a local cache
cannot conceal failed object-store write-back.

Use `fuseType: ossfs` and an ossfs-capable image to exercise ossfs through the same
protocol. Both clients use the existing driver option and credential interceptors.
Secret passthrough, including supplied STS credentials, is supported. Recreate
Pods to change credentials/configuration. See the shared documentation for other
capability restrictions, lifecycle semantics and validation boundaries.

See the [validation record](../../../docs/rund3-fuse-validation.md) for tested
runtime versions and lifecycle cases. The original Serverless deployment needs
separate validation. Integration and process tests cover both clients without
a cluster.
