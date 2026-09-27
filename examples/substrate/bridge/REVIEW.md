# Review disposition

## Addressed

| Review item | Result |
|---|---|
| Unchecked binding file Close/Remove errors | Read/close errors are joined; temporary-file cleanup errors are propagated; directory Sync and Close are both checked |
| Uppercase error strings | Actor client errors now start with lowercase text |
| Two-request responsibility | `node.go` states that the annotation owns backend configuration and that virtual capabilities must not be merged |
| Read-only precedence | Documented as the union of supported read-only restrictions; covered by unit, gRPC, restart and real Linux bind-mount tests |
| Deployment wiring | `enableSubstrate` now adds Controller/Envoy, Node/registrar, Service, ServiceAccount/RBAC, persistent state and trust/token projections |
| Deployment regressions | Default-disabled render stays byte-identical; enabled render, custom paths, component switches and server-side dry-run are checked |
| Actor versus worker identity | Separate Actor keys; standard PodInfo carries the current worker, while credentials and EFC ownership remain Actor-scoped |
| Client API usage | Handwritten Invoke paths and the trimmed proto are removed; generated GetActor/GetActorTemplate are used and tested against the deployed API |

## Explicit limitations

1. **Public API packaging:** public Substrate currently has no metadata annotations.
   The client package is therefore an unmodified, provenance-pinned snapshot of
   the deployed integration API, not a claim that a public upstream module works
   unchanged. A published annotation-capable API module is still needed to remove
   that snapshot. Go, Kubernetes, gRPC and other dependency versions were left at
   the existing CSI baseline; incompatible dependency experiments were reverted.
2. **Golden restore:** real placeholder mounting, read-only behavior, restart and
   unpublish have been exercised. The complete checkpoint → remove placeholder →
   publish real storage → restore Golden workflow is a separate runtime integration
   acceptance test. Known source-path/rebinding failures are not marked fixed.
3. **External deployment services:** signers/trust bundles, Substrate API permission,
   NAS mount broker, cloud identity and storage connectivity remain prerequisites.
   Helm configures the CSI side but does not manufacture those external services.
4. **Release artifact:** the chart requires a CSI image containing this branch.
   Enabling the option cannot add the driver to an older published binary.

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
  readonly bind-mount test. Uploaded test binaries were checksum-verified before
  execution. The Pod was deleted afterwards.
- Existing workload Deployments/DaemonSets, Actor VMs and NAS data were not replaced
  or rolled back during these checks.

## Why the files exist

The implementation is split by responsibility rather than by an expanding set of
runtime features: lookup/identity, logical Controller operations, Node forwarding,
durable binding, Golden placeholder and read-only restrictions. Test files cover
those boundaries and their lifecycle combinations. Helm files describe actual
deployment responsibilities. The large API files are generated upstream API
definitions with recorded hashes, not handwritten bridge business logic.

See [README.md](README.md) for the field contract, deployment values, upgrade
constraints and the deliberate no-op/cleanup behaviors retained from the design.
