#!/usr/bin/env bash
# file: scripts/ci/apt_cached_install.sh
# version: 1.1.0
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
#     lacks) are kept; then write MANIFEST.sha256 for the next run. If no
#     manifest could be written the directory is deleted, so the cache step
#     saves nothing (a manifest-less directory would be saved and then miss
#     forever, because an exact-key hit is never re-saved).
# A miss therefore costs what the old step cost; a hit costs a dpkg run.
set -uo pipefail
[ "$#" -ge 2 ] || { echo "usage: $0 <cache-dir> <package>..." >&2; exit 2; }
cache_dir="$1"
shift
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
manifest="$cache_dir/MANIFEST.sha256"
apt_opts="-o Dir::Cache::archives=$cache_dir"

installed() {
  local p
  for p in "$@"; do
    [ "$(dpkg-query -W -f='${db:Status-Status}' "$p" 2>/dev/null)" = installed ] || return 1
  done
}

if [ -s "$manifest" ]; then
  if (cd "$cache_dir" && sha256sum --quiet -c MANIFEST.sha256); then
    echo "apt_cached_install: cache hit, installing from $cache_dir without apt"
    export DEBIAN_FRONTEND=noninteractive
    # A runner whose background apt run was killed leaves dpkg half-configured
    # ("dpkg was interrupted"); finish it first so the install below can run.
    sudo dpkg --configure -a || true
    mapfile -t debs < <(awk -v d="$cache_dir" '{print d "/" $2}' "$manifest")
    if sudo dpkg -i --force-confold --refuse-downgrade "${debs[@]}" &&
      installed "$@"; then
      exit 0
    fi
    # dpkg may have failed part-way and left packages unpacked but not
    # configured; apt refuses to install over that unless told to fix it.
    apt_opts="$apt_opts -o APT::Get::Fix-Broken=true"
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
APT_INSTALL_EXTRA_OPTS="$apt_opts" "$here/apt_install.sh" "$@" || exit 1
sudo rm -rf "$cache_dir/lock" "$cache_dir/partial"
if ! { sudo chown -R "$(id -u):$(id -g)" "$cache_dir" &&
  (cd "$cache_dir" && ls -1 ./*.deb >/dev/null 2>&1 && sha256sum ./*.deb | sed 's# \./# #' >MANIFEST.sha256) &&
  [ -s "$manifest" ]; }; then
  echo "apt_cached_install: packages are installed but no manifest could be written (no .deb files kept, or chown/manifest write failed); discarding $cache_dir so nothing is cached" >&2
  sudo rm -rf "$cache_dir"
fi
exit 0
