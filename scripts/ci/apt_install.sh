#!/usr/bin/env bash
# file: scripts/ci/apt_install.sh
# version: 1.1.0
# guid: 3b7e9d52-6a14-4c8f-b0e1-5d2a7f9c4e68
# last-edited: 2026-11-18
#
# Retrying, time-bounded apt-get update + install for CI runners. A mirror
# hang once held a job for 34 minutes; every apt call is capped here.
# Usage: scripts/ci/apt_install.sh <package>...
#
# APT_INSTALL_CALL_TIMEOUT (seconds, default 600) caps each apt-get call. A
# caller that also sets a step timeout-minutes should pick a value so that
# 3 attempts x 2 calls x the cap, plus the backoff, fits inside the step.
set -uo pipefail
[ "$#" -ge 1 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
export DEBIAN_FRONTEND=noninteractive
call_timeout="${APT_INSTALL_CALL_TIMEOUT:-600}"
opts=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
for attempt in 1 2 3; do
  if timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" update &&
    timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" install -y --no-install-recommends "$@"; then
    exit 0
  fi
  echo "apt_install: attempt ${attempt}/3 failed for: $*" >&2
  [ "$attempt" -lt 3 ] && sleep $((attempt * 10))
done
echo "apt_install: all 3 attempts failed installing: $*" >&2
exit 1
