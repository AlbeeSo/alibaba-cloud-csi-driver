#!/bin/bash
set -e

# Demo 6: credential rotation hook for agent-memory-fuse.
#
# agent-memory-fuse supports live credential rotation via its
# rotate-credential subcommand, which reads a JSON object from stdin.

read -r ak
read -r sk
read -r token
read -r expiration

printf '{"accessKeyId":"%s","accessKeySecret":"%s","stsToken":"%s"}' \
    "$ak" "$sk" "$token" | \
    /usr/local/bin/agent-memory-fuse rotate-credential --mountpoint "$mountpoint"

echo "rotated agent-memory-fuse credential (expires $expiration)"
