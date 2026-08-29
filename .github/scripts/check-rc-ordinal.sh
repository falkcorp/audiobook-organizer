#!/usr/bin/env bash
# file: .github/scripts/check-rc-ordinal.sh
# version: 2.0.0
# guid: 49d5c680-8da1-44d4-907f-f3394d52c900
# last-edited: 2026-08-29
#
# Counts how many -rc.N prerelease tags exist for a given base version in a
# `gh release list --json tagName,isPrerelease` JSON payload and REPORTS
# whether the "never accumulate more than 10 RCs on a version" threshold
# (TODO.md, owner directive 2026-08-08) has been reached.
#
# v2.0.0 CONTRACT CHANGE (owner directive 2026-08-29: "We don't want it to
# fail, just have it push a new minor release"). Previously this script
# exited 1 at the threshold, which turned every subsequent merge to main into
# a red "Prerelease on Merge" run while doing nothing to stop RCs piling up --
# v0.219.3 reached rc.33, 23 past the limit, with the guard failing all the
# way. Failing was never the remedy; cutting the next minor release is.
#
# So this script now only COUNTS AND REPORTS. Deciding what to do about the
# verdict belongs to the caller (.github/workflows/prerelease.yml), which
# dispatches a minor Production Release instead of failing the run.
#
# Outputs (stdout, and $GITHUB_OUTPUT when set):
#   rc_count      integer count of same-base RCs found
#   at_threshold  "true" once rc_count >= MAX_RCS, else "false"
#
# Usage: check-rc-ordinal.sh <releases-json-file> <base-version>
#   <releases-json-file>  path to JSON matching
#                         `gh release list --json tagName,isPrerelease`
#   <base-version>        base version to count against, e.g. v0.219.3
#
# Exit codes:
#   0  counted successfully (REGARDLESS of whether the threshold was hit --
#      check the at_threshold output, not the exit status)
#   2  usage error (bad args, missing/unreadable file)

set -euo pipefail

MAX_RCS="${MAX_RCS:-10}"

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

AT_THRESHOLD=false
if (( COUNT >= MAX_RCS )); then
  AT_THRESHOLD=true
  echo "::notice::${BASE} has ${COUNT} release candidate(s) (limit ${MAX_RCS}) — promoting to the next MINOR release instead of minting another RC. See TODO.md: 'Never accumulate more than 10 RCs on a version.'"
else
  echo "OK: ${COUNT} RC(s) for ${BASE} is below the ${MAX_RCS} threshold."
fi

emit() {
  echo "$1=$2"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "$1=$2" >> "$GITHUB_OUTPUT"
  fi
}
emit rc_count "$COUNT"
emit at_threshold "$AT_THRESHOLD"

exit 0
