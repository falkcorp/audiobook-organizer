#!/usr/bin/env bash
# file: scripts/ci/apt_install.sh
# version: 1.0.0
# guid: 3b7e9d52-6a14-4c8f-b0e1-5d2a7f9c4e68
# last-edited: 2026-10-23
#
# Retrying, time-bounded apt-get update + install for CI runners. A mirror
# hang once held a job for 34 minutes; every apt call is capped here.
# Usage: scripts/ci/apt_install.sh <package>...
set -uo pipefail
[ "$#" -ge 1 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
export DEBIAN_FRONTEND=noninteractive
opts=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
for attempt in 1 2 3; do
  if timeout 600 sudo apt-get "${opts[@]}" update &&
    timeout 600 sudo apt-get "${opts[@]}" install -y --no-install-recommends "$@"; then
    exit 0
  fi
  echo "apt_install: attempt ${attempt}/3 failed for: $*" >&2
  [ "$attempt" -lt 3 ] && sleep $((attempt * 10))
done
echo "apt_install: all 3 attempts failed installing: $*" >&2
exit 1
