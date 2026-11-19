#!/usr/bin/env bash
# file: scripts/ci/apt_install.sh
# version: 2.1.0
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
#     with. Attempt 2 points the Ubuntu archive at the first host of APT_MIRRORS,
#     attempt 3 at the second, and so on round the list. A runner whose default
#     mirror trickles (us-east-2.ec2.archive.ubuntu.com delivered a 67 MB ffmpeg
#     set at 108 kB/s on 2026-11-18) no longer fails all three attempts on that
#     one host. Three source shapes are rewritten:
#       - a deb822 `URIs:` line or classic `deb` line naming a *archive.ubuntu.com
#         host (the host is replaced);
#       - a `mirror+file:/path`, `mirror+http(s)://...` or `mirror:` URI, which is
#         how the hosted ubuntu-26.04 runner image configures the archive: its
#         /etc/apt/sources.list.d/ubuntu.sources says
#         `URIs: mirror+file:/etc/apt/apt-mirrors.txt`, and the real host
#         (us-east-2.ec2.archive.ubuntu.com, priority 1) is only in that list file
#         (the whole URI is replaced by http://HOST/ubuntu/);
#       - nothing else: security.ubuntu.com and ports.ubuntu.com are left alone,
#         and comment lines are never touched.
#     The rewrite is left in place afterwards: the runner is discarded, and a
#     mirror that worked is the one later steps want. A call that rewrote
#     nothing says so on stderr.
#   * Partial downloads are kept. Nothing here deletes apt's archive directory
#     between attempts, so a killed attempt's partial .deb files are resumed
#     (HTTP Range) by the next one, from whichever mirror it uses.
#   * Throughput floor. The download of attempts 1 .. N-1 runs under
#     apt_watchdog.py, which kills it when the archive directory grows by less
#     than APT_INSTALL_MIN_KIBPS KiB/s (default 400) over 20 s, after a 20 s
#     grace; apt's own Acquire timeout only fires on a full stall. The watchdog
#     logs the measured rate when it kills, and the average when a download
#     finishes. The LAST attempt has no floor, only the wall-clock cap, so a
#     uniformly slow network still finishes if it can. APT_INSTALL_MIN_KIBPS=0
#     turns the floor off.
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
# CI-proof hook: APT_INSTALL_TEST_DL_LIMIT_KIB=N caps attempt 1's download at
# N KiB/s (Acquire::http(s)::Dl-Limit), so a real run can show the floor killing
# a slow first attempt and the rotation then succeeding. Attempts 2.. are never
# limited. Unset in every workflow except a throwaway proof PR.
set -uo pipefail
[ "$#" -ge 1 ] || { echo "usage: $0 <package>..." >&2; exit 2; }
export DEBIAN_FRONTEND=noninteractive
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
call_timeout="${APT_INSTALL_CALL_TIMEOUT:-600}"
attempts="${APT_INSTALL_ATTEMPTS:-3}"
backoff="${APT_INSTALL_BACKOFF:-10}"
floor_kib="${APT_INSTALL_MIN_KIBPS:-400}"
read -ra mirrors <<<"${APT_MIRRORS:-archive.ubuntu.com azure.archive.ubuntu.com}"
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

# set_mirror HOST: point the Ubuntu archive sources at HOST. See the header for
# the shapes handled. Comment lines are skipped so a commented-out example never
# counts as a hit.
set_mirror() {
  local host="$1" f hit=0 live
  for f in $(sources_files); do
    [ -f "$f" ] || continue
    live=$(grep -v '^[[:space:]]*#' "$f" || true)
    if grep -Eq 'https?://[^/[:space:]]*archive\.ubuntu\.com' <<<"$live"; then
      sudo sed -i -E "/^[[:space:]]*#/! s#(https?://)[^/[:space:]]*archive\.ubuntu\.com#\1${host}#g" "$f" && hit=1
    fi
    if grep -Eq 'mirror(\+[a-z]+)?:[^[:space:]]+' <<<"$live"; then
      sudo sed -i -E "/^[[:space:]]*#/! s#mirror(\+[a-z]+)?:[^[:space:]]+#http://${host}/ubuntu/#g" "$f" && hit=1
    fi
  done
  if [ "$hit" = 1 ]; then
    echo "apt_install: archive mirror is now ${host}" >&2
  else
    echo "apt_install: no Ubuntu archive source (archive.ubuntu.com host or mirror+ URI) to point at ${host}; keeping the configured mirror" >&2
  fi
}

# download: the download-only install, under the throughput floor except on
# the last attempt.
download() {
  local attempt="$1" dl_opts=()
  shift
  if [ "$attempt" = 1 ] && [ -n "${APT_INSTALL_TEST_DL_LIMIT_KIB:-}" ]; then
    echo "apt_install: TEST HOOK: attempt 1 download limited to ${APT_INSTALL_TEST_DL_LIMIT_KIB} KiB/s" >&2
    dl_opts=(-o "Acquire::http::Dl-Limit=${APT_INSTALL_TEST_DL_LIMIT_KIB}" -o "Acquire::https::Dl-Limit=${APT_INSTALL_TEST_DL_LIMIT_KIB}")
  fi
  if [ "$attempt" -lt "$attempts" ] && [ "$floor_kib" != 0 ] && command -v python3 >/dev/null 2>&1; then
    local wd_args=()
    if [ -n "${APT_WATCHDOG_ARGS:-}" ]; then read -ra wd_args <<<"$APT_WATCHDOG_ARGS"; fi
    # The watchdog runs as root (inside sudo) because the process group it
    # kills is root's.
    timeout -k 10 "$call_timeout" sudo python3 "$here/apt_watchdog.py" \
      --dir "$archive_dir" --floor-kib "$floor_kib" ${wd_args[@]+"${wd_args[@]}"} -- \
      apt-get "${opts[@]}" ${dl_opts[@]+"${dl_opts[@]}"} install -y --no-install-recommends --download-only "$@"
  else
    timeout -k 10 "$call_timeout" sudo apt-get "${opts[@]}" ${dl_opts[@]+"${dl_opts[@]}"} install -y --no-install-recommends --download-only "$@"
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
