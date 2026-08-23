#!/usr/bin/env bash
# file: .github/scripts/check-rc-ordinal.sh
# version: 1.0.1
# guid: 49d5c680-8da1-44d4-907f-f3394d52c900
# last-edited: 2026-08-22
#
# Counts how many -rc.N prerelease tags exist for a given base version in a
# `gh release list --json tagName,isPrerelease` JSON payload, and fails
# (exit 1) once the count reaches the "never accumulate more than 10 RCs on
# a version" threshold (TODO.md, owner directive 2026-08-08: "we are never to
# get above 10 RCs"). The next step past 10 is a stable release, not rc.11.
#
# Uses the same tagName-parsing pattern as the RC-counting jq in
# .github/workflows/cleanup-rc-releases.yml (strip -rc.N to get the base,
# match "^$base-rc\.[0-9]+$" to select same-base RCs) so the two pieces of
# RC-accounting logic in this repo stay consistent.
#
# Usage: check-rc-ordinal.sh <releases-json-file> <base-version>
#   <releases-json-file>  path to JSON matching
#                         `gh release list --json tagName,isPrerelease`
#   <base-version>        base version to count against, e.g. v0.217
#
# Exit codes:
#   0  count is below the threshold (or no matching RCs at all)
#   1  count has reached the threshold — cut a stable release instead
#   2  usage error (bad args, missing/unreadable file)

set -euo pipefail

MAX_RCS=10

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <releases-json-file> <base-version>" >&2
  exit 2
fi

RELEASES_FILE="$1"
BASE="$2"

if [[ ! -f "$RELEASES_FILE" ]]; then
  echo "error: releases file not found: $RELEASES_FILE" >&2
  exit 2
fi

# Counted with string operations, NOT by interpolating $base into a regex.
# A version is full of dots and `.` is a regex metacharacter, so
# test("^" + $base + "-rc\\.[0-9]+$") with base v0.217 also matches v0X217-rc.1
# and over-counts. Anchoring does not help -- it fixes prefix confusion
# (v0.217 vs v0.217.9), which is a different bug. startswith/ltrimstr are
# literal, so the base can contain anything and the ordinal is still the only
# part matched as a pattern.
# shellcheck disable=SC2016  # $base is a jq variable (--arg), not a shell variable
COUNT=$(jq --arg base "$BASE" \
  '[.[] | select(.isPrerelease
      and (.tagName | startswith($base + "-rc."))
      and ((.tagName | ltrimstr($base + "-rc.")) | test("^[0-9]+$")))] | length' \
  "$RELEASES_FILE")

echo "Base version: $BASE"
echo "RC releases found for $BASE: $COUNT"

if (( COUNT >= MAX_RCS )); then
  echo "::error::${BASE} has ${COUNT} release candidate(s) (limit ${MAX_RCS}) — cut a stable release for ${BASE} instead of another RC. See TODO.md: 'Never accumulate more than 10 RCs on a version.'"
  exit 1
fi

echo "OK: ${COUNT} RC(s) for ${BASE} is below the ${MAX_RCS} threshold."
exit 0
