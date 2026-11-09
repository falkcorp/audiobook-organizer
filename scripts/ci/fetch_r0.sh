#!/usr/bin/env bash
# file: scripts/ci/fetch_r0.sh
# version: 1.0.0
# guid: 2f6b8d41-9c3a-4e75-a1d0-7b4e5c8a3f92
# last-edited: 2026-11-09
#
# Put the R0 release binary where internal/books' TestR0OpensR1Store finds it,
# without that test touching the network. The archive is restored from
# actions/cache (the calling job's step), else fetched here with `gh release
# download` (GITHUB_TOKEN, bounded retry), checked against the SHA-256 pinned
# in internal/books/r0store_test.go (r0Tag, r0ArchiveSHA256["Linux_x86_64"]),
# extracted, and exported to later steps as ABO_R0_BINARY (r0BinaryEnv).
#
# Exit: 0 ok; 1 the archive does not match its pinned checksum; 3 the download
# failed on every attempt (a caller may fall back to building from the tag).
#
# Keep R0_TAG / R0_SHA256 in step with r0store_test.go and the cache key.
set -euo pipefail

R0_TAG="${R0_TAG:-v0.223.0}"
R0_ARCHIVE="${R0_ARCHIVE:-audiobook-organizer_${R0_TAG#v}_Linux_x86_64.tar.gz}"
R0_SHA256="${R0_SHA256:-acbe9e096b04d80bd1a49d8e0ecc1bf7dc8171e004ea26cf795513e881ba1e6d}"
R0_ATTEMPTS="${R0_ATTEMPTS:-5}"
R0_BACKOFF="${R0_BACKOFF:-5}"

dir="${HOME}/.cache/audiobook-organizer-tests/r0/${R0_TAG}"
mkdir -p "$dir"
archive="${dir}/${R0_ARCHIVE}"

verified() { [ -f "$archive" ] && echo "${R0_SHA256}  ${archive}" | sha256sum -c --status - 2>/dev/null; }

if ! verified; then
  rm -f "$archive"
  delay="$R0_BACKOFF"
  for attempt in $(seq 1 "$R0_ATTEMPTS"); do
    if gh release download "$R0_TAG" -R "${GITHUB_REPOSITORY:?}" -p "$R0_ARCHIVE" -D "$dir" --clobber; then
      break
    fi
    echo "::warning::R0 download attempt ${attempt}/${R0_ATTEMPTS} failed"
    rm -f "$archive"
    if [ "$attempt" -lt "$R0_ATTEMPTS" ]; then
      sleep "$delay"
      delay=$((delay * 2))
    fi
  done
  if [ ! -f "$archive" ]; then
    echo "::warning::the R0 release download failed on every attempt"
    # Nothing to cache: an empty directory would take the key forever.
    rm -rf "$dir"
    exit 3
  fi
fi

if ! verified; then
  echo "::error::the R0 archive does not match its pinned checksum"
  rm -f "$archive"
  exit 1
fi

out="${RUNNER_TEMP:?}/r0"
mkdir -p "$out"
member="audiobook-organizer_Linux_x86_64"
tar -xzf "$archive" -C "$out" "$member"
bin="${out}/${member}"
chmod +x "$bin"
echo "ABO_R0_BINARY=${bin}" >> "${GITHUB_ENV:?}"
