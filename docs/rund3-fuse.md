# RunD 3 FUSE volumes and optional native sidecars

This implementation supports ossfs, ossfs2 and customfuse through the same
Pod-scoped file protocol. The installation serves either legacy RunD or RunD 3;
mixed runc/RunD service in one enabled CSI installation is not supported.

## Selecting the protocol

Set `deploy.featureGates: RundCSIProtocol3=true` in the CSI chart and
`spec.runtimeClassName: rund` in workload Pods. No PV transport attribute is
needed. With the gate disabled, the existing paths remain in use. With the gate
enabled, ControllerPublish skips separate FUSE Pods, and NodePublish verifies
the live Pod UID and runtimeClass before writing requests. Missing PodInfo, API
errors or a different runtimeClass fail closed. Configure the same gate for all
CSI controllers, node plugins and the webhook; changing it requires rolling
those components and recreating consuming Pods. It is not a live migration.

Kubernetes 1.33+ native sidecars, `/dev/fuse`, privileged sidecars, hostPath and
bidirectional mount propagation are required. The RunD environment must expose
the Pod's configuration directory and CSI volume subtree to the sidecar and
support the propagation required to reach application mounts. Actual RunD
host/guest behavior needs validation with that runtime. A RuntimeClass named
`rund` backed by a runc handler can exercise Kubernetes wiring in a test cluster;
it does not validate RunD host/guest behavior.

## Optional webhook

The webhook automates sidecar construction; it does not select the protocol.
Leave `fuseSidecarWebhook.enabled: false` to manage sidecars yourself. With it
enabled, the admission service handles CREATE for runtimeClass `rund`, resolves
bound PVCs/PVs, and injects one OSS sidecar (both ossfs and ossfs2 drivers) and/or
one customfuse sidecar. It prepends them before application init containers and
sets participating application mounts to HostToContainer propagation, retaining
read-only flags. All required volume handles must pass the startup probe.

Configure operator-owned image profiles, rather than accepting privileged images
from arbitrary Pod annotations. The OSS image must contain both clients and this
branch's mountproxy; use the mount-proxy Dockerfile's `aio` target. A customfuse
image contains mountproxy and its `/entrypoint.sh`, which can dispatch different
clients using mount options. An optional PV attribute
`fuse.csi.alibabacloud.com/sidecar-profile` selects another configured profile for
the same CSI driver. A Pod must use one profile per driver, because its mounts
share one runner session. For other arrangements use manual injection.

Build the webhook image using `build/fuse-sidecar-webhook/Dockerfile`. Example
chart values (replace every placeholder):

```yaml
deploy:
  featureGates: RundCSIProtocol3=true
fuseSidecarWebhook:
  enabled: true
  image: REGISTRY/fuse-sidecar-webhook:TAG
  tlsSecretName: fuse-sidecar-tls
  caBundle: BASE64_PEM_CA
  profiles:
    oss:
      driver: ossplugin.csi.alibabacloud.com
      image: REGISTRY/mount-proxy-aio:TAG
    customfuse:
      driver: customfuseplugin.csi.alibabacloud.com
      image: REGISTRY/custom-client:TAG
```

Provide a `kubernetes.io/tls` Secret in the release namespace. The certificate
must be signed by the supplied CA and include SAN
`<release>-fuse-sidecar.<namespace>.svc`. Manage certificate issuance and renewal
externally (for example with cert-manager); roll the webhook after certificate
replacement. CA rotation must update the webhook configuration's caBundle too.
The chart does not generate keys or depend on cert-manager. `imagePullSecrets`
are used by the webhook and merged into injected Pods. Operator profiles may
set sidecar `resources`; admission does not copy credentials into Pod specs.
Restrict `namespaceSelector` to approved namespaces if appropriate. The
privileged sidecar and its Pod subtree mounts are trusted infrastructure, not
a security boundary against a privileged tenant.

Use Pod annotation `fuse.csi.alibabacloud.com/inject: "false"` to opt out even
when the webhook is installed. This bypass also works while the webhook is
unavailable, through an admission match condition. The Pod must then supply its
own sidecar, mounts and probes; CSI still publishes the same files. Existing
Pods are never mutated. Names beginning with the injected `fuse-oss` and
`fuse-customfuse` containers/volumes are reserved; conflicts are rejected.

FUSE PVCs must be bound before admission. WaitForFirstConsumer FUSE claims cannot
be resolved at this point and receive a clear admission error. Unrelated disk
and NFS volumes are left unchanged, including unbound claims with a non-FUSE
StorageClass. Application `subPath`/`subPathExpr` mounts on FUSE volumes are
rejected: kubelet would resolve them before the sidecar has mounted the client.
Unknown profiles, incorrect PV bindings and block-mode FUSE claims are rejected.

## Shared envelope and mountproxy semantics

The JSON embeds the existing `directvolume.MountInfo` used by block and NFS:
`Source`, `DeviceType: fuse`, `FSType`, `MountOpts`, and `Extra`. FUSE adds:

* `apiVersion`, `podUID`, `generation` and optional `requiredFeatures`.
* `MountProxy`, the actual `proxy.MountRequest`, including options, flags,
  credentials and future optional fields. No second request DTO is maintained.

CSI writes the descriptor beside the kubelet `mount` directory as
`vol_attr.json`, and publishes the same content to:

```text
/var/run/ossfs/file-volumes/<podUID>/desired/<sha256(volumeHandle)>.json
/var/run/customfuse/file-volumes/<podUID>/desired/<sha256(volumeHandle)>.json
```

Each driver uses its own root; sidecars see only their Pod UID directory through
Downward API + subPathExpr. A second UID-scoped mount exposes the Pod's
`volumes/kubernetes.io~csi` subtree. CREATE admission does not need a Pod UID.
The chart's `deploy.kubeletRootDir` is used for the kubelet hostPath. Custom
state roots must also match the corresponding CSI node component's
`CSI_FUSE_FILE_ROOT`; the default chart roots already match.

Mountproxy currently uses JSON over Unix sockets, while CSI uses gRPC. File
delivery preserves the mount request and `proxy.Response` result semantics,
but is asynchronous: NodePublish acknowledges durable delivery, not successful
mounting. Waiting there would prevent kubelet from starting the sidecar.
`status/<id>.json` stores `observedGeneration`, runner `session`, heartbeat,
`phase`, `result` (the original mount response), `unmountResult` and a lifecycle
`error`. An initially successful mount whose process later exits keeps its
original result and changes lifecycle phase to Failed. Startup/readiness require
all explicitly expected volumes, matching identities/generations/sessions and
fresh Mounted heartbeats. Extra requests are not executed.

Unknown optional mount-request fields survive decode/re-encode and participate
in the generation digest. Adding fields to `proxy.MountRequest` requires no
parallel JSON struct update; known field names are derived from its JSON tags.
New behavior that an old consumer must not ignore needs a required feature name
or a protocol-version change. Unknown required features and versions fail
explicitly. This does not make an old executable implement a newer feature.

Files are atomic replacements with mode 0600 and directories 0700. Desired files
and vol_attr.json contain secrets and must be exposed only to trusted processes.
Status errors redact request source and Secret values; client stderr may still
contain sensitive data, so protect sidecar logs. Deletion removes the desired
request and descriptor; each worker explicitly unmounts before driver shutdown.
Status/session files contain no request credentials and remain for diagnosis;
operators may apply a retention policy to deleted-Pod directories.

The common transport rejects fd-passing, transparent recovery, overlays and host
metrics paths individually. Those require additional lifecycle mechanisms.
Secret credential passthrough (including supplied STS credentials) is supported;
live credential rotation, RRSA/agent identity and configuration changes require
additional work. Recreate Pods for changed credentials/configuration. These
restrictions are independent of which FUSE client the sidecar uses. Readiness
loss does not stop an already-running application; sidecar restart may require
Pod recreation because existing application binds can reference the old mount.

## Validation

The file-transport workflow runs protocol, identity, admission, CSI integration
and real subprocess tests with the race detector. Tests cover gate/runtime
selection, manual bypass, two drivers/multiple volumes, AdmissionReview over TLS,
binding/profile/path conflicts, UID identity, stale status, process exit,
teardown, the common RunD envelope and unknown-field preservation. Kernel mount
operations and Kubernetes API storage are simulated in those integration tests.

Real OSSFS2 two-Pod/two-volume reads, independently verified object write-back,
read-only enforcement, failed-mount startup blocking, Job completion and no
leaked host mounts were validated on the earlier manually injected runc version.
That result does not certify this newer webhook/gating/schema or real RunD.
Cluster acceptance for this revision must repeat those cases with automatic
injection, manual bypass, mixed ossfs/ossfs2/customfuse clients and the target
RunD runtime. The static manifests remain useful for manual-sidecar tests.
