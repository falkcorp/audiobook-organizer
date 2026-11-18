#!/usr/bin/env bash
# file: scripts/ci/apt_cached_install.sh
# version: 1.0.0
# guid: 7c4e2a91-5d38-4b06-9f1a-e3b8d6c20f57
# last-edited: 2026-11-18
#
# Install apt packages WITHOUT a live mirror on a warm cache.
# Usage: scripts/ci/apt_cached_install.sh <cache-dir> <package>...
#
# <cache-dir> is the directory an actions/cache step restores and saves (keyed
# on the package list and the runner image version, see ci.yml).
#   * Hit  (cache-dir holds MANIFEST.sha256 and the .deb files it lists): verify
#     every .deb against the manifest, then `dpkg -i` them. No apt-get, no
#     network. If the manifest does not verify or dpkg fails, fall through.
#   * Miss: scripts/ci/apt_install.sh (retrying, time-bounded, signature-checked
#     by apt) with apt's archive directory pointed at <cache-dir>, so the .deb
#     files apt downloaded (the packages plus whichever dependencies the image
#     lacks) are kept; then write MANIFEST.sha256 for the next run.
# A miss therefore costs what the old step cost; a hit costs a dpkg run.
set -uo pipefail
[ "$#" -ge 2 ] || { echo "usage: $0 <cache-dir> <package>..." >&2; exit 2; }
cache_dir="$1"
shift
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
manifest="$cache_dir/MANIFEST.sha256"

installed() {
  local p
  for p in "$@"; do dpkg -s "$p" >/dev/null 2>&1 || return 1; done
}

if [ -s "$manifest" ]; then
  if (cd "$cache_dir" && sha256sum --quiet -c MANIFEST.sha256); then
    echo "apt_cached_install: cache hit, installing from $cache_dir without apt"
    export DEBIAN_FRONTEND=noninteractive
    # A runner whose background apt run was killed leaves dpkg half-configured
    # ("dpkg was interrupted"); finish it first so the install below can run.
    sudo dpkg --configure -a || true
    # shellcheck disable=SC2046
    if sudo dpkg -i --force-confold $(awk '{print "'"$cache_dir"'/" $2}' "$manifest") &&
      installed "$@"; then
      exit 0
    fi
    echo "apt_cached_install: cached install failed, falling back to apt" >&2
  else
    echo "apt_cached_install: cache manifest did not verify, falling back to apt" >&2
  fi
else
  echo "apt_cached_install: cache miss"
fi

# Miss (or unusable hit): rebuild the cache directory from scratch.
sudo rm -rf "$cache_dir"
sudo mkdir -p "$cache_dir"
sudo dpkg --configure -a || true
APT_INSTALL_EXTRA_OPTS="-o Dir::Cache::archives=$cache_dir" "$here/apt_install.sh" "$@" || exit 1
sudo rm -rf "$cache_dir/lock" "$cache_dir/partial"
sudo chown -R "$(id -u):$(id -g)" "$cache_dir"
(cd "$cache_dir" && ls -1 ./*.deb >/dev/null 2>&1 && sha256sum ./*.deb | sed 's# \./# #' >MANIFEST.sha256) ||
  echo "apt_cached_install: no .deb files kept; nothing to cache" >&2
exit 0
