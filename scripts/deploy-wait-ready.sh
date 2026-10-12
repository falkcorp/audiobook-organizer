#!/usr/bin/env bash
# file: scripts/deploy-wait-ready.sh
# version: 1.0.0
# guid: 08d3f99b-10a7-4a18-b6b6-c092d9177c08
# last-edited: 2026-10-12
#
# Wait for a restarted server to report "ready":true on /health.
# Usage: deploy-wait-ready.sh BASE_URL [TIMEOUT_SECONDS]   (default 300)
# Polls every 5 s. No answer or "ready":false keeps polling. A 200 body with
# no "ready" field fails at once: that binary predates readiness reporting.
set -euo pipefail
url="${1:?usage: deploy-wait-ready.sh BASE_URL [TIMEOUT_SECONDS]}/health"
timeout="${2:-300}"
start=$SECONDS
while ((SECONDS - start < timeout)); do
  body=$(curl -ksf --max-time 5 "$url") || body=""
  case "$body" in
    *'"ready":true'*) echo "   ready after $((SECONDS - start))s"; exit 0 ;;
    "" | *'"ready":false'*) ;;
    *) echo "ERROR: $url answered with no ready field after $((SECONDS - start))s (binary predates readiness reporting?)" >&2; exit 2 ;;
  esac
  sleep 5
done
echo "ERROR: $url not ready after $((SECONDS - start))s (limit ${timeout}s)" >&2
exit 1
