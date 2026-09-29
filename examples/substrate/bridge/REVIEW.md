# Review disposition

## Addressed

| Review item | Result |
|---|---|
| Binding persistence | Removed from the PR; lifecycle implementation and its reviewed persistence logic are retained on the backup branch |
| Uppercase error strings | Actor client errors now start with lowercase text |
| Two-request responsibility | `node.go` states that the annotation owns backend configuration and that virtual capabilities must not be merged |
| Create/publish responsibility | Create validates only the logical request, without Actor lookup or a cross-stage digest; first publish uses current configuration and retains all publish-side validation |
| Stateless lifecycle | No configuration digest or write-ahead record; corrected failed requests can retry, and live mount state drives Unpublish. The caller owns unpublish-before-reconfigure |
| Read-only passthrough | Inner Readonly, access mode, mount flags and options remain unchanged, including conflicting options whose precedence belongs to NAS; outer Readonly, READER_ONLY and ro mount flags fail before lookup or mounting; no union, salt or normalization |
| Mounted read-only consistency | Existing targets are checked for actual ro only when the inner request explicitly sets Readonly or READER_ONLY; no inference from options/flags or writable-mode requirement |
| Deployment wiring | `enableSubstrate` extends the original plugin/provisioner processes, reuses their ServiceAccounts, and adds a shared Envoy/Service/ConfigMap, bridge registrar and persistent state; Actor API client initialization, token and server trust are Node-only, while Controller inbound mTLS remains |
| Deployment regressions | Default-disabled render stays byte-identical; enabled render, custom paths, component switches and server-side dry-run are checked |
| Check execution | `hack/check-substrate-helm.sh` provides an opt-in local check; no dedicated GitHub Actions workflow |
| Actor versus worker identity | Separate Actor keys; standard PodInfo carries the current worker, while credentials and EFC ownership remain Actor-scoped |
| Shared identity helpers | Folded into the existing agentidentity utility package; one Substrate mode predicate, with the old common entry point retained |
| Client API usage | Handwritten Invoke paths and the hand-written proto are removed; generated GetActor/GetActorTemplate are used and tested against the deployed API |
| Filesystem metrics | Substrate skips pod-oriented files; short metric labels and shared NAS server initialization are retained |
| Shared broker scope | No change to pkg/mounter/proxy or cmd/mount-proxy-server relative to the selected base; no lifecycle protocol in this PR |

## Explicit limitations

1. **Public API packaging:** public Substrate currently has no metadata annotations.
   The client package is therefore a mechanically extracted, provenance-pinned
   projection of the deployed integration API - the two lookups the bridge needs,
   with verbatim names and numbers - not a claim that a public upstream module
   works unchanged. A published annotation-capable API module is still needed to
   remove that projection. Go, Kubernetes, gRPC and other dependency versions were
   left at the existing CSI baseline; incompatible dependency experiments were
   reverted.
2. **Golden restore:** real placeholder mounting, restart and unpublish have been
   exercised. Placeholders now stay writable and reject outer read-only requests.
   The complete checkpoint → remove placeholder →
   publish real storage → restore Golden workflow is a separate runtime integration
   acceptance test. Known source-path/rebinding failures are not marked fixed.
3. **External deployment services:** signers/trust bundles, Substrate API permission,
   NAS mount broker, cloud identity and storage connectivity remain prerequisites.
   Helm configures the CSI side but does not manufacture those external services.
4. **Release artifact:** the chart requires a CSI image containing this branch.
   Enabling the option cannot add the driver to an older published binary.
5. **Chart consolidation:** native NAS and bridge have separate logical driver
   configs and sockets but share workloads and TLS infrastructure. Migrate old
   standalone bridge resources deliberately; do not run duplicate Node drivers
   against the same socket. The shared-workload consolidation has render and
   build coverage, not a fresh live deployment result.
6. **Stateless contract:** existing NAS mount equivalence is delegated to backend
   idempotence and caller sequencing. This PR does not detect annotation drift on
   an already-mounted target, journal intent, or reconcile orphan helpers. Those
   capabilities and their limits are preserved on the lifecycle backup branch.

## Validation performed

- CSI bridge, identity helpers, credential code and Helm tests passed with race
  detection; affected Substrate packages passed their full unit tests with race
  detection.
- Linux build, vet and scoped golangci-lint passed. No existing lint rule was
  disabled to obtain this result.
- Generated-client TLS reads succeeded for both a normal Actor and a Golden Actor
  in the test cluster, using a short-lived, Pod-bound ServiceAccount token.
- Rendered new Helm resources passed Kubernetes server-side dry-run; they were not
  installed over the running components.
- A temporary privileged Linux test Pod with no hostPath mounts ran the actual NAS
  identity parser, Actor-owned metrics test, Golden bind/unbind gRPC test and
  readonly bind-mount test before removal of the outer read-only feature. That
  historical read-only placeholder result is not a current capability. Uploaded
  test binaries were checksum-verified before execution. The Pod was deleted afterwards.
- Existing workload Deployments/DaemonSets, Actor VMs and NAS data were not replaced
  or rolled back during these checks.

## Why the files exist

The implementation is split by responsibility rather than by an expanding set of
runtime features: lookup/identity, logical Controller operations, Node forwarding,
live mount cleanup, Golden source directories and read-only restrictions. Test files cover
those boundaries and their lifecycle combinations. Helm files describe actual
deployment responsibilities. The large API files are generated upstream API
definitions with recorded hashes, not handwritten bridge business logic.

The binding implementation and configuration digest have been removed. The
separate metadata helper file and the new `pkg/volumecontext` directory have
also been removed. Controller/Node workloads are
the existing provisioner/plugin Pods, not a second deployment stack.

See [README.md](README.md) for the field contract, deployment values, upgrade
constraints and the deliberate no-op/cleanup behaviors retained from the design.
