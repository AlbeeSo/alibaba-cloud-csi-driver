# Substrate CSI bridge

The bridge exposes `substrate.csi.alibabacloud.com` and translates Substrate's
logical Actor volumes into pre-generated NAS CSI publish requests. It does not
create AgenticFS data on behalf of an E2B client. The annotation producer and
the Substrate caller must implement the contracts below.

## Helm deployment

The chart defaults to `enableSubstrate: false`, preserving the existing
deployment. With the option enabled it creates:

- A dedicated ServiceAccount and read-oriented Kubernetes RBAC.
- A Controller Deployment with the bridge and an Envoy mTLS sidecar.
- A Controller TCP Service, accepting Controller/Identity RPCs only.
- A bridge Node DaemonSet and registrar for each configured node pool.
- Actor hostPath mounts with bidirectional propagation and persistent node-local
  state under the bridge's CSI socket directory.
- Projected Service DNS trust and an audience-bound API token; the Controller
  serving certificate and client trust also use projected Substrate signers.
- A `CSIDriver`, optional `CSIDriverConfig`, and optional `ate-storage` class.

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
| `substrate.apiEndpoint` | `api.ate-system.svc:443` | Substrate API TLS endpoint |
| `substrate.apiAudience` | `api.ate-system.svc` | Audience of the projected API token |
| `substrate.actorRoot` | `/var/lib/ateom-gvisor/actors` | Must match atelet's host target layout |
| `substrate.mountProxySocket` | `/run/cnfs/alinas-mounter.sock` | Existing NAS broker socket |
| `substrate.createDriverConfig` | `true` | Create the Substrate CR when Controller is enabled |
| `substrate.storageClass.create` | `true` | Create the bridge reference class |
| `substrate.storageClass.name` | `ate-storage` | Must match the annotation producer's template slots |
| `substrate.envoyImage` | `envoyproxy/envoy:v1.39.0` | TLS frontend image |

`controller.enabled`, `controller.replicas`, `plugin.enabled`, `nodePools`,
`deploy.kubeletRootDir`, and `imagePullSecrets` are also honored. Node pools must
be non-overlapping and use the same kubelet root for the global driver config.
The Node bridge uses health port 11262 to avoid the native plugin's 11260.

The state directory is mounted at `/csi/substrate-state`; using an ephemeral
directory instead breaks cleanup after a Pod restart. Never run two Node
registrars for this driver on the same node/socket during migration.

### External prerequisites

These are not supplied by this CSI chart:

1. Substrate control plane and the `CSIDriverConfig` CRD.
2. `PodCertificate` and `ClusterTrustBundle` projection support, with the
   `servicedns.podcert.ate.dev/identity` and
   `podidentity.podcert.ate.dev/identity` signers and live trust bundles.
3. Substrate authorization permitting the dedicated bridge ServiceAccount to
   query the intended Actors and templates. Kubernetes RBAC alone does not
   configure Substrate's application authorization policy.
4. An operational NAS mount broker, AgentIdentity credential service, NAS/AP
   connectivity and the required per-Actor permissions.
5. A Substrate API with `ResourceMetadata.annotations`. Public upstream does
   not currently expose that field; see the API snapshot provenance below.

Do not turn off TLS verification to work around missing CA/signers. No CA
private key, cloud key or business token is placed in this chart.

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

The one access restriction that can be tightened is read-only:

```text
effectiveReadOnly = annotationReadOnly || bridgeRequestReadOnly
```

Supported read-only booleans, reader-only access modes and `ro` flags are
recognized; conflicting `rw` options are removed. An existing target cannot
silently change access mode. Unpublish it before republishing in another mode.

## Identity keys

| Key | Meaning / producer |
|---|---|
| `csi.alibabacloud.com/actor.uid` | Actor UID, validated against logical volume ID and API response |
| `csi.alibabacloud.com/actor.name` | Actor name used for `GetActor` |
| `csi.alibabacloud.com/actor.namespace` | Actor atespace, not the Kubernetes namespace |
| `csi.storage.k8s.io/pod.uid` | Current worker Pod UID |
| `csi.storage.k8s.io/pod.name` | Current worker Pod name |
| `csi.storage.k8s.io/pod.namespace` | Current worker Pod Kubernetes namespace |
| `csi.alibabacloud.com/substrate-mode` | Selects Substrate path/credential handling |
| `csi.alibabacloud.com/substrate-binding-digest` | Controller-generated configuration fingerprint |

The Actor keys are sent as CreateVolume parameters and returned in
VolumeContext. Worker PodInfo is injected on each Run/Restore from the current
assignment, not stored as immutable volume identity. On the storage side,
`sandboxId`/credential `ResourceID` and EFC ownership remain Actor-scoped;
restoring real PodInfo must not change the identity used to access data.

The digest is a SHA256 of the canonical annotation request and backend driver
(or the verified Golden association). It detects configuration changes between
CreateVolume and NodePublish and prevents rebinding a target to different
storage. It is not a signature, secret, credential, or substitute for API
authorization. Worker placement does not affect it. A stricter caller read-only
request adds a local binding restriction without changing the Controller value.

New publish calls need the Actor name and atespace. Older binding files remain
readable and can still be unpublished without querying an Actor. Coordinate
the Substrate API/atelet and bridge upgrade; do not expect the old UID-only
caller to work with a name-based generated API client.

## API client provenance

The driver uses generated `ControlClient.GetActor` and `GetActorTemplate`, checks
the returned UID, and verifies Golden Actor/template association. There are no
handwritten RPC paths or locally trimmed message definitions.

`pkg/substrate/internal/ateapipb` is an unchanged snapshot of the complete API
package from the deployed Substrate integration, with source revision and hashes
in `SOURCE.json`. This is necessary because public upstream currently omits
annotations. It is not advertised as compatibility with an unextended public
server. Once an annotation-capable lightweight API module is published, replace
the snapshot with that dependency; do not hand-edit the copied schema.

## Validation and remaining system work

Local tests cover multiple read-only sources, Actor/worker identity separation,
configuration drift, old binding cleanup, failed publish followed by restart,
and generated-client TLS/token rotation. Helm tests cover disabled output,
enabled resources, trust/token wiring, socket paths and component switches.
The workflow `helm-substrate` installs Helm and runs those render checks in CI.

On an isolated privileged Linux container, set `BRIDGE_REAL_MOUNT_TEST=1` to run
the real Golden placeholder bind/unbind and read-only mount tests. The optional
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

| Files | Responsibility |
|---|---|
| `actor_client.go` | TLS/token connection and generated API calls |
| `internal/ateapipb/*` | Unmodified API schema/client snapshot and provenance |
| `identity.go`, `pkg/volumecontext/*` | Actor versus Pod identity contract |
| `controller.go`, `resolve.go` | Logical lifecycle, annotation selection and digest |
| `node.go`, `bindings.go` | Validated publishing and durable backend identity |
| `placeholder.go` | Golden-only isolated bind mounts |
| `readonly.go` | Read-only restriction and idempotence checks |
| `*_test.go` | Unit, restart, gRPC, Linux mount and optional live-read coverage |
| Helm `substrate-*` / `_substrate.tpl` | Deployment, registrar, TLS, state and runtime configuration |

Writing a binding before calling NAS, re-querying the Actor on publish, no-op
logical Create/Attach/Stage, and Actor-independent Unpublish are deliberate
design choices, not missing lifecycle implementations.
