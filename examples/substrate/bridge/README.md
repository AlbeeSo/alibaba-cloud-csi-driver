# Substrate CSI bridge

The bridge exposes `substrate.csi.alibabacloud.com` and translates Substrate's
logical Actor volumes into pre-generated NAS CSI publish requests. It does not
create AgenticFS data on behalf of an E2B client. The annotation producer and
the Substrate caller must implement the contracts below.

## Helm deployment

The chart defaults to `enableSubstrate: false`, preserving the existing
deployment. With the option enabled it extends the existing workloads:

- The existing `csi-provisioner` and `csi-plugin` processes also serve the bridge
  driver, on a separate socket. No additional Deployment, DaemonSet or bridge
  process is created.
- The existing `alicloud-csi-provisioner` and `alicloud-csi-node` ServiceAccounts
  are retained. Only the Node workload receives an audience-bound token for the
  Substrate API; the bridge Controller does not query Actors or templates.
- One Envoy sidecar and one `csi-provisioner` Service expose the native NAS
  Controller on port 443 and the bridge Controller on port 444. Both require
  mTLS and accept Controller/Identity RPCs only.
- One `csi-provisioner-substrate` ConfigMap defines both TLS listeners.
- One additional bridge registrar is added to each existing Node Pod.
- Actor hostPath mounts with bidirectional propagation and persistent node-local
  state under the bridge's CSI socket directory.
- Both workloads retain Pod identity projections for TLS. Only the Node workload
  projects the Actor API token and Service DNS server trust; the Controller keeps
  its certificate and Pod identity client trust for inbound mTLS.
- A bridge `CSIDriver`, per-driver `CSIDriverConfig` objects, and optional
  `ate-storage` class. Native NAS configuration is emitted only when its
  Controller is enabled; two driver names cannot share one `CSIDriverConfig`.

```sh
helm upgrade --install csi deploy/charts/alibaba-cloud-csi-driver \
  --namespace kube-system \
  --set enableSubstrate=true \
  --set images.plugin.tag=<image-containing-this-change> \
  --set images.controller.tag=<image-containing-this-change> \
  --set deploy.regionID=<region>
```

Published images predating this driver do not gain bridge support by enabling
the chart option. Use images built with the implementation. The chart does not
publish images or install the separate CNFS NAS mount broker.

Key values:

| Value | Default | Meaning |
|---|---|---|
| `enableSubstrate` | `false` | Enable all bridge resources |
| `substrate.apiEndpoint` | `api.ate-system.svc:443` | Substrate API TLS endpoint, required only for Node |
| `substrate.apiAudience` | `api.ate-system.svc` | Audience of the Node's projected API token |
| `substrate.actorRoot` | `/var/lib/ateom-gvisor/actors` | Must match atelet's host target layout |
| `substrate.mountProxySocket` | `/run/cnfs/alinas-mounter.sock` | NAS-specific broker socket shared by native NAS and bridge |
| `substrate.createDriverConfig` | `true` | Create the native NAS and bridge configs for enabled Controllers |
| `substrate.storageClass.create` | `true` | Create the bridge reference class |
| `substrate.storageClass.name` | `ate-storage` | Must match the annotation producer's template slots |
| `substrate.envoyImage` | `envoyproxy/envoy:v1.39.0` | TLS frontend image |

`controller.enabled`, `controller.replicas`, `plugin.enabled`, `nodePools`,
`deploy.kubeletRootDir`, and `imagePullSecrets` are also honored. Node pools must
be non-overlapping and use the same kubelet root for the global driver config.
The original process health ports are unchanged; there is no second bridge
process competing for them. The NAS-only `--nas-mount-proxy-sock` override takes
precedence over `--mount-proxy-sock` and the `AlinasMountProxy` default, without
changing OSS's per-volume proxy selection.

The state directory is `/csi/substrate.csi.alibabacloud.com/substrate-state`
inside the shared Node container. It still maps to the same node-local bridge
socket directory used by the standalone layout. Using an ephemeral directory
instead breaks cleanup after a Pod restart. Never run the old standalone Node
driver and the shared Node driver on the same node/socket during migration.

The shared Service selects `app: csi-provisioner`. `substrate-nas` points the NAS
driver to `dns:///csi-provisioner.<namespace>.svc:443`; `substrate-csi-bridge`
points the bridge to port 444. Each config retains its own Node socket. Disable
`substrate.createDriverConfig` when these resources are managed externally,
and do not leave competing configs for the same driver name.

### External prerequisites

These are not supplied by this CSI chart:

1. Substrate control plane and the `CSIDriverConfig` CRD.
2. `PodCertificate` and `ClusterTrustBundle` projection support, with the
   `servicedns.podcert.ate.dev/identity` and
   `podidentity.podcert.ate.dev/identity` signers and live trust bundles.
3. Substrate authorization permitting `alicloud-csi-node` to query the intended
   Actors and templates. The bridge Controller needs no such permission.
   Kubernetes RBAC alone does not
   configure Substrate's application authorization policy.
4. An operational NAS mount broker, AgentIdentity credential service, NAS/AP
   connectivity and the required per-Actor permissions.
5. A Substrate API with `ResourceMetadata.annotations`. Public upstream does
   not currently expose that field; see the API client provenance below.

Do not turn off TLS verification to work around missing CA/signers. No CA
private key, cloud key or business token is placed in this chart.

The shared identity projection puts the certificate at
`/run/podidentity.podcert.ate.dev/credential-bundle.pem`. The Node additionally
projects Service DNS server trust at `trust-bundle.pem` in that directory,
matching the native Substrate credential-path contract. Envoy uses a separate `client-trust-bundle.pem` from
the Pod identity signer for inbound client verification. The directory name
does not determine which issuer a trust bundle must contain.

The CNFS mount broker is a separate workload: these CSI Pod projections do not
automatically configure its credentials. The broker must independently have the
client identity and correct trust for its credential endpoint.

## Two requests, one authority

Substrate's request describes a virtual bridge volume. The entry in
`ate.dev/csi-volume-publish-requests` describes the actual NAS volume:

```json
[
  {
    "volumeName": "data",
    "driver": "nasplugin.csi.alibabacloud.com",
    "request": {"volumeId": "backend-id", "targetPath": "/data"}
  }
]
```

This is a structural example; a real request also needs filesystem capability,
AgenticFS/AgentIdentity attributes and its credential provider. The encoded
array is limited to 256 KiB. Never put credentials in annotations/VolumeContext.

The actual request remains authoritative for backend ID, server/path, fsType,
mount options, provider and PublishContext. The bridge sets the host target and
validated identity metadata. It does not merge arbitrary outer VolumeContext,
fsType or mount flags into the actual request.

Read-only configuration belongs to the annotation request. Its `Readonly`,
access mode, mount flags and `VolumeContext.options` are forwarded unchanged.
The NAS driver applies their semantics and option precedence; the bridge does
not normalize conflicting `rw` options or reinterpret options overridden by
mount flags.

Outer `Readonly=true`, either READER_ONLY access mode, or an outer mount flag
containing `ro` is unsupported. The bridge returns `FailedPrecondition` before
Actor lookup or downstream mounting rather than silently publishing writable
storage. Outer VolumeContext is not interpreted as read-only configuration.
Configure business read-only access in the annotation producer, not in the
bridge StorageClass's mountOptions.

For a target with an existing binding, only an explicit inner `Readonly=true`
or READER_ONLY mode requires an already-mounted filesystem to report `ro`.
This catches writable remounts without a configuration change. The check does
not infer requirements from inner options or mount flags, and does not require
RW when no explicit read-only requirement exists. Untracked mounts still cannot
be claimed. Golden placeholders are writable bind mounts; they do not inherit
outer read-only constraints.

## Identity keys

| Key | Meaning / producer |
|---|---|
| `csi.alibabacloud.com/actor.uid` | Actor UID, checked against logical volume ID; also checked against the API response at publish |
| `csi.alibabacloud.com/actor.name` | Actor name used for `GetActor` |
| `csi.alibabacloud.com/actor.namespace` | Actor atespace, not the Kubernetes namespace |
| `csi.storage.k8s.io/pod.uid` | Current worker Pod UID |
| `csi.storage.k8s.io/pod.name` | Current worker Pod name |
| `csi.storage.k8s.io/pod.namespace` | Current worker Pod Kubernetes namespace |
| `csi.alibabacloud.com/substrate-mode` | Selects Substrate path/credential handling |

The Actor keys are sent as CreateVolume parameters and returned in
VolumeContext. Worker PodInfo is injected on each Run/Restore from the current
assignment, not stored as immutable volume identity. On the storage side,
`sandboxId`/credential `ResourceID` and EFC ownership remain Actor-scoped;
restoring real PodInfo must not change the identity used to access data.

CreateVolume validates only its request: logical volume name, filesystem
capabilities, capacity range, absence of content source/secrets, and complete
Actor parameters whose UID matches the volume name. It returns the logical ID,
requested capacity, Substrate mode and Actor keys. It performs no Actor/template
lookup, annotation parsing, backend provisioning or driver-side state write.
Missing or invalid annotations and unavailable Actor APIs are handled at publish,
not at create.

NodePublish reads the current Actor configuration and retains identity,
annotation, target and protocol checks, with the read-only boundaries above. A legitimate annotation change
after create and before the first local binding takes effect on first publish.
There is no cross-stage digest in VolumeContext and no comparison against a
Controller snapshot of the configuration.

The node-local binding still stores a SHA256 of the canonical annotation request
and backend driver (or the verified Golden association). It prevents rebinding
the same target to different storage; identical requests remain retryable.
Worker placement does not affect this digest. No outer read-only salt is added;
the canonical annotation digest and persisted binding format remain unchanged.
The digest is not a substitute for authorization.
The binding is written before calling NAS, so even a failed publish can leave it
in place. Changing that target's configuration requires Unpublish first; deleting
and recreating the logical volume is not required.

New publish calls need the Actor name and atespace. Older binding files remain
readable and can still be unpublished without querying an Actor. Coordinate
the Substrate API/atelet and bridge upgrade; do not expect the old UID-only
caller to work with a name-based generated API client.

When upgrading from the cross-stage-digest implementation, upgrade all Node
instances before the Controller. New Nodes ignore an old digest left in persisted
VolumeContext; old Nodes still require one and cannot consume new Controller
responses. A single Helm upgrade rolls both workloads concurrently, so a volume
created during that window may fail its first publish until the Node restarts and
the publish is retried. Keep node-local binding files intact throughout the
upgrade.

Targets previously tightened by outer read-only signals, such as manually adding
`ro` to the bridge StorageClass, can have salted bindings. Unpublish them before
republishing with the new behavior; do not bypass or rewrite those bindings.

## API client provenance

The driver uses generated `ControlClient.GetActor` and `GetActorTemplate`, checks
the returned UID, and verifies Golden Actor/template association. There are no
handwritten RPC paths.

`pkg/substrate/internal/ateapipb` is a minimal generated projection of the
deployed Substrate API: only the two lookups above and the message closure they
need, with verbatim message, field and RPC names and numbers and omitted field
numbers marked `reserved`. `SOURCE.md` records the source revision, the
extraction rule and the refresh procedure. The projection exists because public
upstream currently omits annotations; it is not advertised as compatibility
with an unextended public server. Once an annotation-capable lightweight API
module is published, replace the projection with that dependency; refresh it
only by re-extraction, never by hand-editing.

## Validation and remaining system work

Local tests cover inner read-only passthrough, early rejection of outer read-only
signals, actual mount-mode checks, Actor/worker identity separation,
current configuration on first publish, node-local binding conflicts, old binding
cleanup, failed publish followed by restart,
and generated-client TLS/token rotation. Helm tests cover disabled output,
enabled resources, node-only API trust/token wiring, Controller-only operation
without Actor API configuration, socket paths and component switches.
After changing Substrate-related code or chart templates, run the local self-check
with Go and Helm installed:

```sh
bash hack/check-substrate-helm.sh
```

It runs the render/socket-selection tests and Helm lint with Substrate disabled and enabled. It
does not require a cluster or deploy resources, and has no dedicated workflow.

On an isolated privileged Linux container, set `BRIDGE_REAL_MOUNT_TEST=1` to run
the real Golden placeholder bind/unbind, writable access and read-only request
rejection tests. The optional
`TestActorLookupLive` performs only API reads when `SUBSTRATE_LIVE_*` is set.

These do not prove the full system sequence:

```text
Golden publish → checkpoint → placeholder unpublish
→ business Actor publishes real storage → restore Golden snapshot
```

That sequence needs the Substrate/runtime integration environment. Previously
observed restore failures depended on deleted source paths, and same-Actor FULL
restore could invalidate guest bind references after backend replacement. This
CSI change does not claim to fix those VMM/runtime issues. Preserve a separate
system-level result for the exact deployed versions; do not weaken cleanup or
leave source placeholders mounted merely to make restore pass.

## File responsibilities

The direct `pkg/substrate` directory now contains seven implementation files and
eleven test files. Its internal `ateapipb` package is a minimal generated
projection of the deployed API plus one provenance file; most of its added lines
are generated API definitions, not additional runtime components. The small
metadata checks were folded into `resolve.go`, and shared identity helpers now
live in the existing `pkg/mounter/utils/agentidentity` package rather than a new
`pkg/volumecontext`.

| Files | Responsibility |
|---|---|
| `actor_client.go` | TLS/token connection and generated API calls |
| `internal/ateapipb/*` | Minimal generated API projection and provenance |
| `controller.go` | Logical CSI Controller lifecycle |
| `resolve.go` | Publish-time Actor validation, annotation selection and node-local digest |
| `node.go` | Node publish/unpublish and target validation |
| `bindings.go` | Durable backend identity for restart/deletion-safe cleanup |
| `placeholder.go` | Golden-only isolated bind mounts |
| `readonly.go` | Outer read-only rejection and explicit inner read-only mount checks |
| `*_test.go` | Unit, restart, gRPC, Linux mount and optional live-read coverage |
| Helm `plugin.yaml` / `controller.yaml` | Shared processes, registrar, TLS sidecar and mounts |
| Helm `_substrate.tpl` / `substrate-support.yaml` | One identity/flag definition, shared Service/TLS configuration and per-driver configs |

`agentidentity.IsSubstrateVolumeContext` is the single parser for the Substrate
mode key. The existing `common.IsSubstrateVolumeContext` delegates to it so
existing callers keep their API. Only the exact string `true` enables the mode.

Writing a binding before calling NAS, re-querying the Actor on publish, no-op
logical Create/Attach/Stage, and Actor-independent Unpublish are deliberate
design choices, not missing lifecycle implementations.
