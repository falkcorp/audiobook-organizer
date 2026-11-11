#!/usr/bin/env bash
# file: scripts/ci/fetch_prefreeze_binary.sh
# version: 1.1.0
# guid: 9c4e1f27-6b3a-4d85-a0e2-7f5b8c1d3e64
# last-edited: 2026-11-10
#
# F2-15: `fetch_prefreeze_binary.sh <tag>` fetches that release's binary for this
# host (linux/amd64 in CI) into a cache dir and prints its path (09 §9.2.1: the
# rollback target is the pre-freeze release). The release's checksums.txt covers
# its archives, not the bare binaries, so it downloads the archive and
# checksums.txt, refuses an archive whose SHA-256 differs, and extracts the
# binary. A cached binary is reused only beside the marker naming the checksum
# it was verified against. `--resolve <file>` prints the one line of
# ci/prefreeze_release.txt that is neither blank nor a `#` comment, and exits 1
# unless there is exactly one (the reader rule shared with F3-07 and F3-08).
set -euo pipefail
if [ "${1:-}" = "--resolve" ]; then
  tags="$(grep -v '^#' "${2:?--resolve <file>}" | grep -v '^[[:space:]]*$' || true)"
  [ -n "$tags" ] && [ "$(wc -l <<<"$tags")" -eq 1 ] || { echo "${2}: want exactly one tag line" >&2; exit 1; }
  echo "$tags"; exit 0
fi
tag="${1:?usage: fetch_prefreeze_binary.sh <tag> | --resolve <file>}"
host="$(uname -s)_$(uname -m | sed 's/^aarch64$/arm64/')"
bin="audiobook-organizer_${host}"
archive="audiobook-organizer_${tag#v}_${host}.tar.gz"
dir="${PREFREEZE_CACHE:-${HOME}/.cache/audiobook-organizer-tests/prefreeze}/${tag}"
sha256() { if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

if [ ! -x "${dir}/${bin}" ] || [ ! -s "${dir}/${bin}.verified" ]; then
  mkdir -p "$dir"
  work="$(mktemp -d "${dir}/.fetch.XXXXXX")"
  trap 'rm -rf "$work"' EXIT
  gh release download "$tag" ${GITHUB_REPOSITORY:+-R "$GITHUB_REPOSITORY"} -p "$archive" -p checksums.txt -D "$work" >&2
  want="$(awk -v f="$archive" '$2 == f {print $1}' "${work}/checksums.txt")"
  [ -n "$want" ] || { echo "${tag}: checksums.txt lists no ${archive}" >&2; exit 1; }
  got="$(sha256 "${work}/${archive}")"
  [ "$got" = "$want" ] || { echo "${tag}: ${archive} sha256 ${got}, checksums.txt says ${want}" >&2; exit 1; }
  tar -xzf "${work}/${archive}" -C "$work" "$bin"
  chmod +x "${work}/${bin}"
  mv -f "${work}/${bin}" "${dir}/${bin}"
  echo "${archive} ${want}" > "${dir}/${bin}.verified"
fi
echo "${dir}/${bin}"
