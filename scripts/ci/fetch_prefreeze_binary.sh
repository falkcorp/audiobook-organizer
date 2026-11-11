#!/usr/bin/env bash
# file: scripts/ci/fetch_prefreeze_binary.sh
# version: 1.0.0
# guid: 9c4e1f27-6b3a-4d85-a0e2-7f5b8c1d3e64
# last-edited: 2026-11-10
#
# F2-15: `fetch_prefreeze_binary.sh <tag>` downloads that release's bare binary for
# this host (linux/amd64 in CI) into a cache dir and prints its path (09 §9.2.1: the
# rollback target is the pre-freeze release). `--resolve <file>` prints the one line
# of ci/prefreeze_release.txt that is neither blank nor a `#` comment, and exits 1
# unless there is exactly one (the reader rule shared with F3-07 and F3-08).
set -euo pipefail
if [ "${1:-}" = "--resolve" ]; then
  tags="$(grep -v '^#' "${2:?--resolve <file>}" | grep -v '^[[:space:]]*$' || true)"
  [ -n "$tags" ] && [ "$(wc -l <<<"$tags")" -eq 1 ] || { echo "${2}: want exactly one tag line" >&2; exit 1; }
  echo "$tags"; exit 0
fi
tag="${1:?usage: fetch_prefreeze_binary.sh <tag> | --resolve <file>}"
asset="audiobook-organizer_$(uname -s)_$(uname -m | sed 's/^aarch64$/arm64/')"
dir="${PREFREEZE_CACHE:-${HOME}/.cache/audiobook-organizer-tests/prefreeze}/${tag}"
[ -x "${dir}/${asset}" ] || { gh release download "$tag" ${GITHUB_REPOSITORY:+-R "$GITHUB_REPOSITORY"} -p "$asset" -D "$dir" --clobber >&2 && chmod +x "${dir}/${asset}"; }
echo "${dir}/${asset}"
