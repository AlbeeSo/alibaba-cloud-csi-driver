# Substrate OSS storage

The OSS driver and the Substrate bridge together give a Substrate Actor an OSS
bucket under the Actor's own scoped identity, mirroring what
[nas-agent-identity.md](./nas-agent-identity.md) and
[substrate-bridge.md](./substrate-bridge.md) provide for NAS. As with NAS, there
are two complementary forms, and a deployment may use either or both:

- **Native**: Substrate calls `ossplugin.csi.alibabacloud.com` directly. The OSS
  driver recognises the `substrate-mode` marker in the volume context and mounts
  under the Actor UID.
- **Bridge-routed**: Substrate calls `substrate.csi.alibabacloud.com`, whose
  driver-agnostic backend registry forwards a stored OSS publish request to the
  in-process OSS node server. The bridge changes only routing and identity
  metadata; the stored request stays authoritative.

Both forms reach the same ossfs mount carrying the same `substrate_*` options, so
the data-access identity is the Actor UID either way. This is the OSS counterpart
of the NAS symmetry the bridge was generalised for: neither form is special, and
serving OSS added one `Backend` value to the registry, not an OSS-specific branch
to the bridge.

## Why OSS differs from NAS

The two drivers obtain the scoped STS credential in different places, and that
difference drives the whole OSS design:

| | NAS / alinas | OSS / ossfs |
|---|---|---|
| Where the exchange runs | In Go, inside the mount-proxy `alinas` jwtauth interceptor | Inside the ossfs client process |
| How rotation reaches the mount | A CSI-side refresher renews and pushes the credential into the live mount via `alinas-tls-cert-refresh` | ossfs renews internally; CSI is not in the loop after mount |
| What CSI supplies | Sensitive mount options (a masked STS triple) | Stable **file paths** as `substrate_*` mount options; ossfs reads them and does the exchange |

Because ossfs owns the exchange and its refresh, the CSI-side OSS substrate work
is a **mount-option contract**, not a Go credential client. OSS does not reuse
the NAS jwtauth interceptor, refresher or sink. CSI's responsibility ends at
emitting the option strings; the exchange, the mTLS handshake and the internal
renewal live in ossfs1/ossfs2 (see [The ossfs option
contract](#the-ossfs-option-contract)).

## Native OSS-driver path

The native form is the OSS analog of the NAS substrate handling. The end-to-end
chain:

1. The volume context carries `csi.alibabacloud.com/substrate-mode=true`, the
   Actor identity keys (`actor.uid`/`actor.name`/`actor.namespace`) and the
   current worker PodInfo — the same keys the bridge injects (see
   [substrate-bridge.md, Identity keys](./substrate-bridge.md#identity-keys)).
2. `pkg/oss/utils.go parseOptions` sets `Options.SubstrateMode` from
   `utils.IsSubstrateVolumeContext` (true only for the exact string `true`) and
   reconciles the sandbox identity with the Actor UID exactly as
   `pkg/nas/nodeserver.go` does:
   - `agentidentity.ValidateActorIdentity` must pass;
   - a stored `sandboxId` that names a different Actor is refused
     (`PermissionDenied`), not silently mounted under the wrong identity;
   - an empty `sandboxId` defaults to the Actor UID.
3. Auth type resolves to `AuthTypeAgentIdentity`. Both ossfs runtimes share one
   choke point — `ossfs/manager.go` and `ossfs2/manager.go` each append
   `ossfpm.GetAgentIdentityOptions(o)` in that case — so the option set is
   identical for ossfs1 and ossfs2.
4. In substrate mode `GetAgentIdentityOptions` emits the `substrate_*` set below
   and **omits** `agent_identity_token_file` (the ACS token dir is not used).
5. `ValidateAgentIdentity` still requires `AGENT_IDENTITY_ENDPOINT`, but in
   substrate mode it returns before the `AGENT_IDENTITY_TOKEN_DIR` check: the
   client validates the projected SA-token and certificate files at mount time,
   inside the ossfs container, where they are actually visible. CSI cannot see
   that filesystem, so it does not pretend to check it.

Agent identity is a sandbox-only (RunD/coco) path and is never used with the
fuse-pod (runc) manager (see the `AgentIdentityConfig` doc comment in
`pkg/mounter/fuse_pod_manager/fuse_pod_manager.go`). The direct
`getAuthOptions`/mount-option path is the one that runs, so
`MakeAgentIdentityConfig` needs no substrate branch.

## Bridge-routed OSS path

The bridge is driver-agnostic. A `Backend{Driver, Node, OwnsMount, Validate}` is
registered per driver name, and `main.go` registers an in-process backend for
every driver the build actually serves:

```go
backends := map[string]substrate.Backend{}
if nasServers != nil {
    backends[substrate.NASDriverName] = substrate.NASBackend(nasServers.NodeServer)
}
if ossServers != nil {
    backends[substrate.OSSDriverName] = substrate.OSSBackend(ossServers.NodeServer)
}
```

A backend whose server was not constructed is simply absent, and a stored request
naming it is refused at publish (`FailedPrecondition`, `publish request for
driver %q has no backend in the Substrate bridge`) rather than panicking or
silently succeeding. The single `ossServers` instance is shared between the
standalone OSS driver and the bridge backend, so per-process side effects (fuse
pod managers, volume locks) are not duplicated — the same reason NAS is
constructed once.

- **Publish** routes by the stored request's `driver`:
  `n.backend(resolved.Driver)` → `backend.Validate(request, actor)` →
  `checkBackendTarget(backend, target)` → `backend.Node.NodePublishVolume(...)`.
  `validateOSSRequest` requires `authType=agent-identity` and refuses CNFS
  routing (`containernetworkfilesystem` non-empty), because CNFS rewrites the
  bucket and endpoint the request names — it would mount a *different* volume
  rather than fail, which the bridge must not do silently.
- **Unpublish** routes by which backend *owns the live mount*:
  `n.backendForMount(live)`. `ossMount` recognises fstype `fuse.ossfs`
  (`mounterutils.OssFuseAttachDir`); both ossfs runtimes attach under that type,
  and a shared-path bind mount of the global mount keeps it, so one check covers
  every OSS runtime form. A `fuse.ossfs` mount goes to OSS unpublish,
  `nfs`/`nfs4`/`alinas` to NAS, a verified Golden source to placeholder cleanup,
  and anything else is refused.

The bridge's read-only passthrough, target validation, Golden placeholder and
statelessness rules are unchanged and driver-independent; see
[substrate-bridge.md](./substrate-bridge.md).

## The `substrate_*` mount-option contract

In substrate mode `GetAgentIdentityOptions` emits exactly these options. Values
in `<...>` are filled at mount time; the file paths are fixed projected
locations, never credentials:

| Option | Value | Meaning |
|---|---|---|
| `agent_identity_endpoint` | `$AGENT_IDENTITY_ENDPOINT` | Credential-service endpoint (shared with the ACS form) |
| `agent_identity_cred_provider` | `<sandboxCredProviderName>` | Credential provider named in the exchange |
| `substrate_actor_uid` | `<sandboxId == actor UID>` | `resourceId` the credential is scoped to |
| `substrate_sa_token_file` | `/var/run/secrets/kubernetes.io/serviceaccount/token` | Projected SA token, read as the bearer on every renewal |
| `substrate_client_cert` | `/run/podidentity.podcert.ate.dev/credential-bundle.pem` | PodCertificate bundle for mTLS |
| `substrate_client_key` | `/run/podidentity.podcert.ate.dev/credential-bundle.pem` | Same bundle (see below) |

`agent_identity_token_file` is deliberately absent: that option belongs to the
ACS sandbox form, which reads `<sandboxId>.token` from `AGENT_IDENTITY_TOKEN_DIR`.
Substrate presents the projected SA token and PodCertificate instead. The paths
are the `agentidentity.SATokenFile` and `agentidentity.PodCertBundleFile`
constants, the same projected layout the NAS substrate path uses.

### One combined cert/key bundle

Substrate projects a single `credential-bundle.pem` holding both the client
certificate and its private key. CSI passes that one path to **both**
`substrate_client_cert` and `substrate_client_key`. This is not a shortcut: both
ossfs runtimes take the two blocks out of the one PEM — ossfs1 points libcurl's
certificate and key options at the same file, and ossfs2 (photon) parses the
certificate block and the key block from the same content. No private key is
placed in the volume context, the annotation, or logs; only the fixed projected
path crosses the CSI boundary.

### Rotation

kubelet periodically rotates the projected SA token and PodCertificate in place.
Because ossfs re-reads `substrate_sa_token_file` and the cert/key bundle on every
credential renewal, that rotation is picked up **without a remount**. This is an
ossfs-side guarantee; CSI's contribution is to hand over stable paths rather than
a copied secret, so there is nothing on the CSI side to refresh.

## The ossfs option contract

The `substrate_*` options are consumed by the ossfs clients, which implement the
exchange and its internal rotation. That work ships in the ossfs repositories,
not here:

- **ossfs1** — `feat/substrate-agent-identity` (libcurl; the per-request
  `CheckIAMCredentialUpdate` gate re-reads the SA token and re-sets the cert/key
  on each pooled handle).
- **ossfs2** — the substrate branch off `origin/ack/master` (PhotonLibOS;
  `AgentIdentityCredentialsProvider::get_credentials` re-reads the files on each
  refresh, constructing a fresh request every time).

Both build the same `GetResourceCredential` exchange the ACS form uses — a POST
carrying `Authorization: Bearer <sa token>` and a body whose `resourceId` is the
Actor UID and whose `credentialProviderName` is the configured provider, over the
mTLS channel — and parse the same response. They differ from the ACS form only in
request construction (SA token + Actor UID + client certificate, instead of an
ACS token file + sandboxId). Their mock tests (request body / bearer /
action-name assertions, SA-token rotation, cert/key loading) live in the ossfs
repositories.

An ossfs image predating that support does not gain it from the CSI change alone:
CSI only emits the options, so the bucket will not mount under the Actor identity
until the node runs an ossfs built with the substrate branch. This is the OSS
counterpart of the bridge's "use images built with the implementation" caveat.

## Validation and tests

CSI-side coverage, all green under `GOOS=linux` in the project's Go image:

| Test | Package | Asserts |
|---|---|---|
| `Test_parseOptions_substrateMode` | `pkg/oss` | `SubstrateMode` set; `sandboxId` reconciled to, or refused against, the Actor UID |
| `TestValidateAgentIdentity_Substrate` | `pkg/mounter/fuse_pod_manager/oss` | Endpoint still required; token dir not required in substrate mode |
| `TestGetAgentIdentityOptions_Substrate` | `pkg/mounter/fuse_pod_manager/oss` | Exact `substrate_*` option set; no `agent_identity_token_file` |
| `TestPublishRoutesToOSSBackend` | `pkg/substrate` | A stored OSS request is forwarded to the OSS backend |
| `TestPublishRejectsWhatOSSBackendCannotServe` | `pkg/substrate` | Refuses non-agent-identity, CNFS routing, and an unregistered driver |
| `TestUnpublishRoutesOSSMountByOwnership` | `pkg/substrate` | A live `fuse.ossfs` mount unpublishes through the OSS backend |
| `TestOSSMountOwnership` | `pkg/substrate` | `ossMount` recognises `fuse.ossfs` and nothing else |

These do not prove the full sequence (projected token/cert present in the ossfs
container, a live credential service, a real bucket mount and its in-client
renewal); that needs the Substrate/runtime integration environment together with
the ossfs-side tests.

## File responsibilities

| File | Responsibility |
|---|---|
| `pkg/oss/utils.go` | Set `SubstrateMode`; reconcile `sandboxId` with the Actor UID |
| `pkg/mounter/fuse_pod_manager/oss/oss_fuse_pod_manager.go` | `Options.SubstrateMode` (derived, not persisted; `json:"-"`) |
| `pkg/mounter/fuse_pod_manager/oss/agent_identity.go` | Substrate branch of `GetAgentIdentityOptions` and `ValidateAgentIdentity` |
| `pkg/mounter/fuse_pod_manager/oss/{ossfs,ossfs2}/manager.go` | Shared agent-identity choke point (unchanged; both call `GetAgentIdentityOptions`) |
| `pkg/substrate/backend.go` | `OSSBackend` |
| `pkg/substrate/utils.go` | `validateOSSRequest`, `ossMount` |
| `pkg/substrate/node.go` | `OSSDriverName` |
| `main.go` | Construct `ossServers` once; register the NAS and OSS backends |

## Limitations and non-goals

- **No worker-side OSS deployment change.** Substrate OSS uses the direct
  in-process ossfs path; the fuse-pod (runc) manager and the OSS mount-proxy
  broker are neither involved nor modified.
- **CNFS routing is refused through the bridge** (`validateOSSRequest`), because
  it would substitute a different bucket/endpoint than the request names.
- **No credential material crosses the CSI boundary.** Only fixed projected paths
  appear in the volume context and mount options; the SA token, certificate and
  exchanged STS triple stay inside the sandbox and the ossfs process.
- **The bridge does not compare a live OSS mount against a new request.** The
  same passthrough rule as NAS applies: reconfiguration requires unpublish first.
