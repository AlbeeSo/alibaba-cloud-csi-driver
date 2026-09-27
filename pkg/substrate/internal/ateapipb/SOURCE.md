# Substrate API snapshot

The `.proto`, generated Go messages, generated gRPC client/server, and `source.go`
in this directory are copied unchanged from `pkg/proto/ateapipb` in the deployed
Substrate integration. The exact source revision and file hashes are recorded
in `SOURCE.json`.

This is the complete upstream integration API package, not a hand-maintained
wire projection. Refresh the files together from Substrate; do not edit message
fields or RPC definitions in this copy.

The public `github.com/agent-substrate/substrate` API currently has no
`ResourceMetadata.annotations`. Depending on its public module would silently
drop the deployed annotation extension required by this bridge. Its monolithic
module also introduces unrelated dependency upgrades. This snapshot lets the
bridge use the generated `ControlClient.GetActor` and `GetActorTemplate` methods
without an internal-network Go module replacement or manual gRPC method strings.

This integration API differs from public upstream. Publishing an annotation-
capable, lightweight Substrate API module remains a prerequisite for replacing
the snapshot with a normal public module dependency. No claim is made that the
bridge works with an unextended public Substrate server.
