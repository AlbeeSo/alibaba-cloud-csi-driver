# OSSFS2 native sidecar over the file transport

Requires Kubernetes 1.33+, a Linux node with `/dev/fuse`, privileged containers,
hostPath and bidirectional mount propagation. No webhook or RunD customization is
needed for the runc demo. See the [protocol contract](../../customfuse/6-file-sidecar/README.md).

Build and install this branch's CSI plugin/controller using the repository Helm
chart with OSS enabled. Use the ordinary self-managed CSI provisioner, not the ACK
managed provisioner. Build a mount-proxy image from `build/mount-proxy/Dockerfile`
with target `ossfs2`; it must include this branch's mount-proxy executable and an
OSSFS2 client. The existing socket transport remains the default for other PVs.

Create a private test bucket in Beijing, with objects `first/hello.txt` containing
`first-ossfs2-volume` and `second/hello.txt` containing `second-ossfs2-volume`.
Create Secret `ossfs2-file-credentials` in `fuse-file-demo`, preferably using
bucket-scoped temporary STS credentials. Keys are `AccessKeyId`, `AccessKeySecret`,
`SecurityToken`, `Expiration`; permanent credentials use the first two keys only.
Configure imagePullSecrets when using private images. Replace `OSS_BUCKET` and
`OSSFS2_FILE_IMAGE` in the manifest before applying it. Change the endpoint/region
and kubelet hostPath when testing outside Beijing or with a custom kubelet root.

```sh
kubectl apply -f rendered-demo.yaml
kubectl -n fuse-file-demo rollout status deployment/ossfs2-reader --timeout=180s
kubectl -n fuse-file-demo logs -l app=ossfs2-file-reader -c reader
```

The two replicas each mount two independent OSS prefixes. The reader checks both
contents, writes a Pod-UID-specific object under the first prefix, and confirms
the second volume rejects writes. Verify `first/write-<podUID>` through the OSS
API as well, so a local FUSE cache cannot hide a failed object-store write-back.

`mountTransport: file` makes ControllerPublish skip the separate fuse Pod and
NodePublish atomically write the request and return. The sidecar executes the
existing OSSFS2 driver, including its normal options and credential interceptors.
The default host directory is `/var/run/ossfs/file-volumes/<podUID>` (visible to
the node plugin under `/host/var/run/ossfs/file-volumes`). `CSI_FUSE_FILE_ROOT`
can override the node-side root; match the sidecar's hostPath accordingly.

Only Secret credential passthrough, including a provided STS token, is supported
in this demo. Live credential rotation, RRSA/agent identity, overlay, fd-passing
and recovery are not supported in file mode. Recreate the Pod to apply changed
credentials or mount configuration. Mounted status has the same readiness
semantics as the existing mount-proxy driver; it is not an ongoing guarantee
that every remote OSS operation will succeed.

Cluster tests should focus on two-Pod directory isolation and real propagation,
a failing mount keeping the reader blocked, and native sidecar/Job shutdown with
no leaked host FUSE mounts. `TestOSSFS2File*` covers publish/unpublish and option /
STS credential serialization without a cluster; `TestMountFile*` in the OSSFS2
proxy package exercises process supervision without a real FUSE mount. Both run
in the file-transport CI workflow.

## Validation recorded for this demo

On ACK Kubernetes 1.33.3 with containerd/runc, a self-managed CSI deployment and
OSSFS2 v2.0.9.ack.3-a1f29fc (with the modified mount-proxy), the following passed:

* Two Pods, each mounting both prefixes, read the expected objects and had
  separate Pod-UID request directories.
* Each Pod wrote an object through OSSFS2; an independent OSS API read confirmed
  the contents. The second volume rejected writes.
* An invalid OSSFS2 option produced a Failed status with a reason and kept the
  application container from starting.
* A Job completed and its native sidecar exited successfully. Its two mount
  statuses became Unmounted and the node had no remaining FUSE mounts for it.
* Scaling the two-Pod Deployment to zero produced four Unmounted statuses and
  left no FUSE mounts for either Pod in the node's mount table.

This validates manually supplied sidecars on runc. Webhook injection and RunD
host/guest mount propagation remain separate follow-up work.
