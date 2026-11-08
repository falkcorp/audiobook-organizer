#!/bin/sh
# file: scripts/ci/image_probe.sh
# version: 1.0.1
# guid: 2a1411e8-5147-4bcd-981d-3d653bf58b0d
# last-edited: 2026-11-08
#
# Probe a running audiobook-organizer container (the docker-image PR job, 09 §9.4 R1).
#
# Usage: scripts/ci/image_probe.sh <container> <base-url>
#
#   <container>  the container name or id (docker exec / docker inspect target)
#   <base-url>   where the container's port is published, e.g. http://127.0.0.1:18484
#
# Environment:
#   READY_TIMEOUT        seconds to wait for "ready":true on /health (default 300)
#   PROBE_STORAGE_LEVELS 1 runs `/app/audiobook-organizer --print-storage-levels` inside the
#                        container and requires exit 0 and JSON on stdout. Off by default:
#                        the flag does not exist until R1-23, which sets it in docker-image.yml.
#   PROBE_BUILD_ID       1 requires the X-App-Build response header of `/` to equal the
#                        content of <meta name="mediacore-build"> in the served HTML
#                        (TestBuildID_HeaderEqualsEmbeddedMeta). Off by default; R1-19
#                        sets it to 1 in docker-image.yml.
#
# Exit 0 when every enabled probe passes; otherwise exit 1 after one line on
# stderr beginning "image_probe:" that names the failure. POSIX sh, curl and
# docker only.

set -eu

fail() {
	echo "image_probe: $*" >&2
	exit 1
}

[ "$#" -eq 2 ] || fail "usage: image_probe.sh <container> <base-url>"
container=$1
base=${2%/}
timeout=${READY_TIMEOUT:-300}
case $timeout in
'' | *[!0-9]*) fail "READY_TIMEOUT must be a number of seconds, got '$timeout'" ;;
esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 1. Wait for readiness. /health answers 200 as soon as the process serves and
# reports "ready":true only when the store's warm-up is done (a degraded or
# warming process must not pass a probe).
deadline=$(($(date +%s) + timeout))
while :; do
	if curl -fsS --max-time 5 "$base/health" >"$tmp/health" 2>/dev/null &&
		grep -Eq '"ready"[[:space:]]*:[[:space:]]*true' "$tmp/health"; then
		break
	fi
	running=$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || echo unknown)
	[ "$running" != "false" ] || fail "container $container exited before $base/health reported \"ready\":true"
	[ "$(date +%s)" -lt "$deadline" ] ||
		fail "no \"ready\":true on $base/health within ${timeout}s"
	sleep 2
done
echo "image_probe: ready on $base/health"

# 2. Storage levels: wired here, off until R1-23.
if [ "${PROBE_STORAGE_LEVELS:-0}" = "1" ]; then
	docker exec "$container" /app/audiobook-organizer --print-storage-levels >"$tmp/levels" 2>"$tmp/levels.err" ||
		fail "--print-storage-levels exited non-zero: $(tr '\n' ' ' <"$tmp/levels.err")"
	first=$(tr -d ' \t\r\n' <"$tmp/levels" | cut -c1)
	case $first in
	'{' | '[') ;;
	*) fail "--print-storage-levels did not print JSON" ;;
	esac
	echo "image_probe: --print-storage-levels printed JSON"
fi

# 3. Build ID: on in docker-image.yml since R1-19.
if [ "${PROBE_BUILD_ID:-0}" = "1" ]; then
	curl -fsS --max-time 10 -D "$tmp/headers" -o "$tmp/index.html" "$base/" ||
		fail "GET $base/ failed"
	header=$(tr -d '\r' <"$tmp/headers" | sed -n 's/^[Xx]-[Aa][Pp][Pp]-[Bb][Uu][Ii][Ll][Dd]:[[:space:]]*//p' | head -n 1)
	[ -n "$header" ] || fail "GET $base/ returned no X-App-Build header"
	flat=$(tr '\n\r' '  ' <"$tmp/index.html")
	meta=$(printf '%s' "$flat" | sed -n 's/.*<meta[^>]*name="mediacore-build"[^>]*content="\([^"]*\)".*/\1/p')
	if [ -z "$meta" ]; then
		meta=$(printf '%s' "$flat" | sed -n 's/.*<meta[^>]*content="\([^"]*\)"[^>]*name="mediacore-build".*/\1/p')
	fi
	[ -n "$meta" ] || fail "the served HTML has no <meta name=\"mediacore-build\" content=...>"
	[ "$header" = "$meta" ] || fail "X-App-Build '$header' differs from the embedded mediacore-build meta '$meta'"
	echo "image_probe: X-App-Build equals the embedded mediacore-build meta"
fi

echo "image_probe: ok"
