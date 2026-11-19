#!/usr/bin/env bash
# file: scripts/ci/apt_install.sh
# version: 2.0.0
# guid: 3b7e9d52-6a14-4c8f-b0e1-5d2a7f9c4e68
# last-edited: 2026-11-18
#
# Retrying, time-bounded apt-get update + install for CI runners. A mirror
# hang once held a job for 34 minutes; every apt call is capped here.
# Usage: scripts/ci/apt_install.sh <package>...
#
# Each attempt is update, then a download-only install, then the real install
# (which finds every .deb already downloaded and so needs no network):
#   * Mirror rotation. Attempt 1 uses whatever mirror the runner is configured
#     with. Attempt 2 rewrites the Ubuntu archive host in the apt sources to the
#     first host of APT_MIRRORS, attempt 3 to the second, and so on round the
#     list. A runner whose default mirror trickles (us-east-2.ec2.archive.ubuntu.com
#     delivered the 67 MB ffmpeg set at under 400 KiB/s) no longer fails all three
#     attempts on that one host. Only archive.ubuntu.com hosts are rewritten
#     (security.ubuntu.com and ports.ubuntu.com are left alone). The rewrite is
#     left in place afterwards: the runner is discarded, and a mirror that
#     worked is the one later steps want.
#   * Partial downloads are kept. Nothing here deletes apt's archive directory
#     between attempts, so a killed attempt's partial .deb files are resumed
#     (HTTP Range) by the next one, from whichever mirror it uses.
#   * Throughput floor. The download of attempts 1 .. N-1 runs under
#     apt_watchdog.py, which kills it when the archive directory grows by less
#     than APT_INSTALL_MIN_KIBPS KiB/s (default 400) over 20 s, after a 20 s
#     grace; apt's own Acquire timeout only fires on a full stall. The LAST
#     attempt has no floor, only the wall-clock cap, so a uniformly slow network
#     still finishes if it can. APT_INSTALL_MIN_KIBPS=0 turns the floor off.
#
# APT_INSTALL_CALL_TIMEOUT (seconds, default 600) caps each apt-get call. A
# caller that also sets a step timeout-minutes should pick a value so that
# 3 attempts x 3 calls x the cap, plus the backoff, fits inside the step; in
# practice the floor ends a slow attempt long before the cap, and the offline
# install call takes seconds. Where the cap does not fit, the step cap fires
# first and the last attempt may never start.
#
# Test hooks (scripts/test_apt_install.py): APT_SOURCES_FILES (space-separated
# files to rewrite), APT_INSTALL_ATTEMPTS, APT_INSTALL_BACKOFF (seconds per
# attempt number), APT_WATCHDOG_DIR, APT_WATCHDOG_ARGS.
set -uo pipefail
[ "$#" -ge 1 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
export DEBIAN_FRONTEND=noninteractive
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
call_timeout="${APT_INSTALL_CALL_TIMEOUT:-600}"
attempts="${APT_INSTALL_ATTEMPTS:-3}"
backoff="${APT_INSTALL_BACKOFF:-10}"
floor_kib="${APT_INSTALL_MIN_KIBPS:-400}"
read -ra mirrors <<<"${APT_MIRRORS:-azure.archive.ubuntu.com archive.ubuntu.com}"
opts=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)
archive_dir=/var/cache/apt/archives
# APT_INSTALL_EXTRA_OPTS: extra apt-get -o options, space separated (used by
# apt_cached_install.sh to keep the downloaded .deb files in a cacheable directory).
if [ -n "${APT_INSTALL_EXTRA_OPTS:-}" ]; then
  read -ra extra_opts <<<"$APT_INSTALL_EXTRA_OPTS"
  opts+=("${extra_opts[@]}")
  for o in "${extra_opts[@]}"; do
    case "$o" in Dir::Cache::archives=*) archive_dir="${o#Dir::Cache::archives=}" ;; esac
  done
fi
archive_dir="${APT_WATCHDOG_DIR:-$archive_dir}"

sources_files() {
  if [ -n "${APT_SOURCES_FILES:-}" ]; then
    echo "$APT_SOURCES_FILES"
  else
    echo /etc/apt/sources.list /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list
  fi
}

# set_mirror HOST: point every *archive.ubuntu.com apt source at HOST.
set_mirror() {
  local host="$1" f hit=0
  for f in $(sources_files); do
    [ -f "$f" ] || continue
    if grep -Eq 'https?://[^/[:space:]]*archive\.ubuntu\.com' "$f"; then
      sudo sed -i -E "s#(https?://)[^/[:space:]]*archive\.ubuntu\.com#\1${host}#g" "$f" && hit=1
    fi
  done
  if [ "$hit" = 1 ]; then
    echo "apt_install: archive mirror is now ${host}" >&2
  else
    echo "apt_install: no archive.ubuntu.com source to point at ${host}; keeping the configured mirror" >&2
  fi
}

# download: the download-only install, under the throughput floor except on
# the last attempt.
download() {
  local attempt="$1"
  shift
  if [ "$attempt" -lt "$attempts" ] && [ "$floor_kib" != 0 ] && command -v python3 >/dev/null 2>&1; then
    local wd_args=()
    if [ -n "${APT_WATCHDOG_ARGS:-}" ]; then read -ra wd_args <<<"$APT_WATCHDOG_ARGS"; fi
    # The watchdog runs as root (inside sudo) because the process group it
    # kills is root's.
    timeout -k 10 "$call_timeout" sudo python3 "$here/apt_watchdog.py" \
      --dir "$archive_dir" --floor-kib "$floor_kib" ${wd_args[@]+"${wd_args[@]}"} -- \
      apt-get "${opts[@]}" install -y --no-install-recommends --download-only "$@"
  else
    timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" install -y --no-install-recommends --download-only "$@"
  fi
}

for attempt in $(seq 1 "$attempts"); do
  if [ "$attempt" -gt 1 ]; then
    set_mirror "${mirrors[$(((attempt - 2) % ${#mirrors[@]}))]}"
  fi
  if timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" update &&
    download "$attempt" "$@" &&
    timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" install -y --no-install-recommends "$@"; then
    exit 0
  fi
  echo "apt_install: attempt ${attempt}/${attempts} failed for: $*" >&2
  [ "$attempt" -lt "$attempts" ] && sleep $((attempt * backoff))
done
echo "apt_install: all ${attempts} attempts failed installing: $*" >&2
exit 1
