# RunD 3 FUSE file protocol validation

The E2E record below covers the implementation at `8ab92f00e`. The subsequent
change that enables all linked file-capable drivers without `--driver` passed
local Linux regression tests with the race detector; its E2E is deferred to a new test
environment. No cluster was created or changed for that adjustment.

Validated on 2026-11-16 with Kubernetes 1.33.3-aliyun.1 and ACK security sandbox
2.2.4, containerd 1.6.28. RuntimeClass `runv` uses handler `runv`; the node's
handler resolves to `io.containerd.rund.v2`, running `containerd-shim-rund-v2`.
The only protocol feature gate is `RundCSIProtocol3=true`. CSI controller/node
components and the optional webhook were installed through the repository Helm
chart without the managed CSI addon.

The CSI and mountproxy executables were built from this branch. Test images used
the official ossfs v1.91.12.ack.2 and ossfs2 v2.0.9.ack.3 runtimes, combined for
the OSS sidecar, and the example's read-only Python FUSE client for customfuse.
This validates those clients and binaries inside the real RunD guest; it does
not certify every production image build target or every RunD distribution.

| Case | Result |
| --- | --- |
| Automatic admission with no hand-written sidecars | Passed: one OSS and one customfuse native sidecar injected per Pod |
| Two Pods, each using five volumes | Passed: one ossfs, two ossfs2 and two customfuse volumes per Pod |
| Pod UID configuration isolation | Passed: each sidecar saw only its Pod's expected desired requests |
| RuntimeClass alias | Passed: a second class with the same handler and scheduling contract ran the five-volume Job |
| Actual guest execution | Passed: guest kernel 4.19 differed from host 5.10, boot IDs differed, business-container mountinfo contained all five FUSE mounts |
| OSS reads and writes | Passed for ossfs and ossfs2; separate OSS API reads verified both clients' written objects |
| Read-only OSS volume | Passed: write rejected |
| Automatically injected Job | Passed: application completed, both sidecars exited successfully, all five volumes became Unmounted |
| Invalid ossfs mount option | Passed: error response recorded, application remained blocked, deletion cleaned the mount |
| Invalid ossfs2 mount option | Passed: error response recorded, application remained blocked, deletion cleaned the mount |
| Failing customfuse entrypoint | Passed: error response recorded, application remained blocked, deletion cleaned the mount |
| Webhook unavailable | Passed: automatic admission failed closed |
| Manual sidecars with injection disabled and webhook unavailable | Passed: Pod started and read all five volumes |
| FUSE process exit after successful mount | Passed: readiness failed, lifecycle error was recorded, original successful mount response remained intact |
| Graceful Deployment shutdown | Passed: ten volumes became Unmounted through explicit sidecar cleanup; no host FUSE mounts remained |
| Separate controller-managed FUSE Pods | None were created |

No permanent OSS access keys were placed in the cluster. Tests used temporary
credentials scoped to an isolated bucket. Resource identifiers, credentials,
kubeconfig, certificates and generated execution manifests are excluded from the
repository and Docker build contexts.

Deployment validation exposed a Helm serialization issue: the chart's string
list of image pull secret names needed conversion into Kubernetes object
references for the webhook Pod and injected Pod configuration. The fix includes
a Helm integration test that decodes the rendered objects into Kubernetes types
and verifies the private-registry configuration. CI installs Helm and runs this
contract without needing a cluster or cloud account.

The broader protocol/error matrix remains in integration tests: CSI publication,
real JSON files, driver dispatch, readiness, missing volumes, stale sessions,
identity mismatches, independent failures, original responses versus later
process exit, and cleanup. Admission tests use HTTPS and fake Kubernetes API
storage. Cluster tests focus on real admission, propagation, client I/O and
native-sidecar lifecycle.

## Runtime findings and remaining scope

Sharing a HostPath of the kubelet CSI subtree did not propagate guest mounts to
the application's separately exported PVC. The implementation now mounts each
existing PVC into the sidecar, sharing its exact volume source. Business reads
and independent OSS API write-back checks passed after this change. Readiness
and credential interceptors distinguish a new FUSE mount from the existing
virtiofs projection, and cleanup preserves the projection underneath it.

This RunD configuration does not forward host devices to privileged containers.
Mountproxy creates and opens the guest's FUSE character device when absent.
No pre-created device or runtime configuration workaround was needed for E2E.

The host mount table cannot enumerate guest-only FUSE mounts. Cleanup evidence
therefore combines guest-written Unmounted responses, successful Job sidecar
exits, graceful Pod deletion and the host mount check; the host check alone is
not proof of guest cleanup.

The existing Serverless arrangement has a host csi-plugin that skips and a guest
csi-agent that mounts. Explicit agent-role tests verify that both agent
constructors retain their socket execution path with the gate enabled, and
classic `rund` selection retains delegation. That environment has not been
tested end to end. No extra feature gate or delivery-mode parameter was added.

Bound FUSE claims and Secret credential passthrough are the supported scope.
WaitForFirstConsumer FUSE admission, application subPath mounts, credential
rotation, overlays, fd-passing and transparent recovery retain the restrictions
documented in the [protocol contract](rund3-fuse.md).
