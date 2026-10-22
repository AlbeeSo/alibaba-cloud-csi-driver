#!/bin/bash
set -e

# Demo 6: agent-memory-fuse with agent-identity credentials.
#
# The credential arrives on stdin as four lines:
#   AccessKeyId
#   AccessKeySecret
#   SecurityToken
#   Expiration
#
# This entrypoint constructs the --credential flag from the STS triple
# and starts agent-memory-fuse in the foreground.

read -r ak
read -r sk
read -r token
read -r expiration

: "${endpoint:?endpoint not set}"
: "${instance:?instance not set}"
: "${store:?store not set}"
: "${scope:?scope not set}"

session="${session:-$(hostname)-$$}"

echo "Starting agent-memory-fuse: endpoint=$endpoint instance=$instance store=$store"
exec /usr/local/bin/agent-memory-fuse mount \
    --endpoint "$endpoint" \
    --instance "$instance" \
    --credential "sts:${ak}:${sk}:${token}" \
    --store "$store" \
    --scope "$scope" \
    --session "$session" \
    "$mountpoint"
