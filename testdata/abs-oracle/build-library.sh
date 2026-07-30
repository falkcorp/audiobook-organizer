#!/usr/bin/env bash
# file: testdata/abs-oracle/build-library.sh
# version: 1.0.0
# guid: 3f1c9e2a-8b47-4d1e-9c62-7a5f0d3b8e14
# last-edited: 2026-07-29
#
# Arranges the repo's existing LibriVox testdata into an Audiobookshelf-shaped
# library for the reference oracle. Output is gitignored derived data.
#
# ABS expects: <library root>/<Author>/<Title>/<audio files>
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SRC="${REPO_ROOT}/testdata/audio/librivox/odyssey_butler_librivox"
DEST="${SCRIPT_DIR}/library"

if [[ ! -d "${SRC}" ]]; then
  echo "ERROR: source testdata not found: ${SRC}" >&2
  exit 1
fi

# Multi-file book: exercises the cumulative startOffset timeline.
MULTI="${DEST}/Homer/The Odyssey"
# Single-file m4b: exercises embedded-chapter extraction and Range seeking.
SINGLE="${DEST}/Homer/The Odyssey (Single File)"

rm -rf "${DEST}"
mkdir -p "${MULTI}" "${SINGLE}"

for f in "${SRC}"/odyssey_0*_homer_butler_64kb.mp3; do
  cp "${f}" "${MULTI}/"
done
cp "${SRC}/odyssey_complete.m4b" "${SINGLE}/"

echo "Built oracle library at: ${DEST}"
find "${DEST}" -type f | sed "s|${DEST}/|  |"
