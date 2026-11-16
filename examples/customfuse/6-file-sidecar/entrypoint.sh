#!/bin/sh
set -eu
exec python3 /demo_fs.py "$mountpoint" "$source"
