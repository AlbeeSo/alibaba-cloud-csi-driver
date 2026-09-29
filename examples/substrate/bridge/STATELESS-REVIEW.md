# Stateless bridge PR preparation

## Branches and preservation

- Complete lifecycle backup: `backup/substrate-lifecycle-v1`, snapshot plus pending
  review changes committed as `0cbdd4f38`.
- The actual snapshot was `f8a60895f`, including `3272d4fe8` and the final report.
- Backup design/reuse guide: `examples/substrate/bridge/LIFECYCLE-DESIGN.md` on the
  backup branch. It records I1–I6, commit dependencies, evidence and known limits.
- New PR branch: `feat/substrate-stateless-bridge`, based on `ae9c99263`.
- No original branch was reset; no amend, rebase, squash or force-push was used.

## Selected changes

| Commit | Intent |
|---|---|
| `139f48502` | Cherry-pick of `9ea423cfa`: keep #10 Substrate filesystem metrics skip |
| `2a18b033b` | Cherry-pick of `325613683`: keep reviewed short labels, driver names and shared NAS initialization |
| Stateless implementation commit following these | Remove binding/digest; forward publish and classify live mounts for unpublish; retain readonly and Golden data lifetime |

The final implementation commit hash is available in this branch's log. Roll back
the implementation first, then startup fixes and metrics skip only if those
independent changes are also unwanted. The backup branch is not modified by PR
rollback.

## Contract

- Create remains logical and API-independent; publish still resolves and fully
  validates the current Actor annotation.
- No binding JSON is read or written and no configuration digest is calculated.
- NAS publish forwards the inner request and uses the native driver's idempotence.
- Unpublish uses live state and logicalID as the NAS lock/log key, without an
  Actor lookup. Unsupported filesystems are refused.
- Golden source names remain deterministic and target-scoped. Existing live
  sources are verified; a missing mounted source is not recreated. StateDir is
  actual Golden data storage, not a metadata journal.
- The active map is retained for per-target serialization in the CSI process.
- The ae9c99263 readonly production logic remains: reject outer readonly signals,
  forward the inner request unchanged, and check explicit inner readonly against
  an existing mount without reinterpreting options precedence.
- The controller/atelet is responsible for Unpublish before changing a mounted
  target's configuration. No new mount-equivalence or orphan-helper protocol is
  implied by this PR.

## Validation

- Red tests reproduced the old digest-conflict failure after a corrected request,
  binding file creation on NAS publish, and dependence on state for unpublish.
- PASS: full `pkg/substrate` race tests, including readonly, identity, gRPC,
  corrected failure/restart, idempotence and active-map concurrency cases.
- PASS: Linux amd64 race binary in an isolated privileged test Pod, including
  real Golden bind/unbind and source-content tests (`BRIDGE_REAL_MOUNT_TEST=1`).
- PASS: Linux vet/build, command-line gopls and package golangci-lint.
- PASS: `hack/check-substrate-helm.sh` (render/options tests and both Helm lint modes).
- Verified zero diff from `ae9c99263` in `pkg/mounter/proxy` and
  `cmd/mount-proxy-server`; no lifecycle package or new broker API was carried over.
- Existing business workloads and NAS data were not modified. This is not a new
  cloud NAS/OSS data-plane or complete Golden checkpoint/restore acceptance run.

Real mount tests used only temporary directories in the isolated
`storage-test/csi-stateless-review-0929` Pod, with no business hostPath mounts.
The test Pod was deleted after confirming that no test mounts remained.
