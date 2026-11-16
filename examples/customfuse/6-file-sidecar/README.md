# Pod-scoped FUSE file transport demo

This experimental transport targets Kubernetes 1.33+ native sidecars. Start on
Linux/runc; RunD host/guest sharing and propagation still require separate testing.
No webhook or external mount-proxy socket is used. It is not the existing
block/NFS `vol_attr.json` protocol and needs no RunD runtime schema changes.

For the native OSSFS2 client, use the [OSSFS2 example](../../oss/file-sidecar/README.md).
It uses the same file protocol with the existing OSSFS2 driver and credential
interceptors. The example below uses a small customfuse fixture.

## Build and run

Install the **customfuse controller and node plugin built from this branch** using
the existing Helm chart (see [installation](../../../docs/customfuse.md)). Use an
isolated test cluster: the demo sidecar is privileged and uses bidirectional mount
propagation. The demo filesystem is read-only, holds no external data and requires
no cloud credentials.

From the repository root:

```sh
podman build --platform linux/amd64 -f examples/customfuse/6-file-sidecar/Dockerfile -t YOUR_REGISTRY/fuse-file-demo:dev .
podman push YOUR_REGISTRY/fuse-file-demo:dev
sed 's|FUSE_FILE_DEMO_IMAGE|YOUR_REGISTRY/fuse-file-demo:dev|' examples/customfuse/6-file-sidecar/demo.yaml | kubectl apply -f -
kubectl -n fuse-file-demo rollout status deployment/reader --timeout=180s
kubectl -n fuse-file-demo logs deployment/reader -c reader
kubectl -n fuse-file-demo exec deployment/reader -c reader -- cat /first/hello.txt /second/hello.txt
```

If the default base-image mirror is unreachable, pass
`--build-arg DEMO_BASE_IMAGE=docker.io/library/debian:bookworm-slim` to the build.

The reader must print both distinct volume contents. To exercise Job completion,
use the same Pod spec in a Job, set Pod `restartPolicy: Never`, and remove the
reader's final sleep loop. The native sidecar should stop after the reader exits.
Adjust the kubelet hostPath if the cluster does not use `/var/lib/kubelet`.

## Contract and lifecycle

Set PV `volumeAttributes.mountTransport: file`. ControllerPublish skips creating a
separate fuse Pod. NodePublish requires `podInfoOnMount` and a filesystem target;
it writes configuration and returns **before the real mount succeeds**. It does
not wait for the workload's sidecar, which cannot start until publish returns.

The node's default root is `/host/var/run/customfuse/file-volumes`, already backed
by the chart's host `/var/run/customfuse` mount. `CSI_FUSE_FILE_ROOT` can override
the node-side root; the Pod hostPath must then match its host location.

```text
<root>/<podUID>/
  desired/<sha256(volumeHandle)>.json
  status/<sha256(volumeHandle)>.json
  session.json
```

The sidecar mounts only its UID subdirectory, using Downward API and
`subPathExpr`. A separate mount exposes this Pod's CSI volume subtree at
`/pod-volumes`, with bidirectional propagation. Request targets are relative
`<CSI-volume-directory>/mount`, never caller-specified host absolute paths.
This namespace scoping is not a security sandbox against a privileged client.

Desired files contain `apiVersion`, `podUID`, a content-digest `generation` and
the existing mount request (`source`, relative `target`, `fstype`, `volumeID`,
`options`, `secrets`). Files are atomically replaced, mode 0600; directories 0700.
Secrets are present in the desired file: mount it only into the trusted sidecar.
Statuses contain phase, observed generation, runner session, timestamp and a
bounded error with source/Secret values redacted. The original client can still
write sensitive data into its own stderr; treat sidecar logs accordingly.

Each explicitly expected volume has an independent mount worker. Missing requests
remain Pending; failures remain Failed until sidecar restart. The startup probe
requires **all expected handles**, matching generations, the current session and
fresh Mounted heartbeats. An empty directory cannot pass. Extra request files are
not executed. Process exit or loss of a mount changes its status to Failed.

On SIGTERM/SIGINT, workers explicitly unmount before the driver terminates and
reaps its FUSE clients. Native sidecar ordering keeps mounts alive until the app
stops. NodeUnpublish removes the desired file and cleans the kubelet target; it
does not wait indefinitely for an already stopped sidecar. Status files remain
for diagnostics. Root-owned per-Pod state directories need an external retention
policy after Pod deletion; a garbage collector is outside this demo.

## Checks to run on Linux

* Scale the Deployment to two replicas: the same PVs must produce separate UID
  directories and independent FUSE processes, without cross-Pod JSON visibility.
* Change one PV's `source` to `fail` **before creating it**: that volume reports
  Failed with the entrypoint error, the other mounts, and the reader never starts.
* Stop the FUSE client: status/readiness fail. Restart the sidecar: old mount
  remnants are cleaned, both volumes remount and receive a new session.
  An already running application's bind mounts may still refer to the old FUSE
  connection; recreate the Pod to restore its view. Transparent recovery is not
  part of this transport demo.
* Delete a reader Pod: the app stops first, sidecar cleanup runs, and the
  Deployment replacement gets a new UID directory. Check node mountinfo for leaks.
* Repeat publish with identical inputs: configuration is unchanged. A changed
  configuration for an existing publish is rejected; recreate the Pod to apply it.

Automated tests use a fake mount backend for multi-volume concurrency, missing
requests, process failure, teardown, stale readiness, traversal and Pod isolation.
They do not substitute for real Linux FUSE/mount propagation validation.

The integration tests in `pkg/customfuse/file_transport_integration_test.go` run
the actual CSI publish/unpublish methods, on-disk JSON, file runner, mount-proxy
driver dispatcher and readiness check together. Only kernel mounting/FUSE is
simulated. They verify delayed second-volume publication, two Pods sharing a PV,
read-only and Secret delivery, duplicate publish, independent timeout/failure,
process exit, unpublish isolation and a restarted runner rejecting stale success.
`pkg/mounter/proxy/server/customfuse/file_mount_test.go` additionally runs actual
child processes through customfuse and its interceptors, checking environment
delivery, stderr failures, exit notification and process reaping.

The `FUSE file transport contracts` CI workflow runs these packages with the race
detector on every pull request, including changes to shared dependencies. Tests
are ordinary `go test` tests: no opt-in build tag, cluster, FUSE device, cloud
account or image pull is required.

```sh
go test -race ./pkg/mounter/proxy/filevolume
# Linux only (the existing CSI packages depend on Linux mount interfaces):
sudo -E env "PATH=$PATH" go test -race ./pkg/customfuse ./pkg/mounter/proxy/server/customfuse
```

Keep cluster E2E focused on the parts these tests cannot model: Downward API /
subPathExpr scoping and real mount propagation (two Pods, two volumes), startup
ordering when one real mount fails, and native-sidecar termination / Job completion
with no leaked host mounts. The broader error/retry/serialization matrix belongs
in the integration tests above.

## Demo limits

File mode supports customfuse and OSSFS2 with Secret passthrough. A customfuse image has one shared
entrypoint; it may dispatch by `$source` or other options for multiple clients.
Credential rotation, live config changes, fd-passing, automatic runtime recovery,
webhook injection and RunD integration are not implemented here. Read-only must
also be honored by the client's entrypoint (this demo always mounts read-only).
Readiness failure after startup does not suspend an already running application.
Test application behavior during sidecar restart explicitly.
