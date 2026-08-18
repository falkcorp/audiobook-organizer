#!/usr/bin/env bash
# file: scripts/check-interface-width.sh
# version: 1.2.0
# guid: 5f1c07a3-84be-4d29-9e60-3b7a2d5c81ef
# last-edited: 2026-08-18
#
# Ratchet on the number of `interfacebloat` findings. See
# .interface-width-baseline for why this counts rather than listing files.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
BASELINE_FILE=.interface-width-baseline
baseline=$(grep -vE '^\s*(#|$)' "$BASELINE_FILE" | head -1 | tr -d '[:space:]')

if ! [[ "$baseline" =~ ^[0-9]+$ ]]; then
  echo "FAIL: $BASELINE_FILE does not contain a bare integer (read: '$baseline')" >&2
  exit 2
fi

# --enable-only overrides the `enable:` list in .golangci.yml. That is
# load-bearing: the shared config also enables errcheck, which currently has a
# ~927-finding Wave 0 backlog and would drown this gate.
#
# interfacebloat ONLY, deliberately. nolintlint is gated separately by the
# go-lint job, where it sits at 0 findings and can therefore be a plain pass/fail
# check. Counting it here too would mean an unexplained `//nolint` fails both
# jobs, and this one would report "interface width went UP" about a comment.
#
# `|| true`-style guards are needed because golangci-lint exits non-zero whenever
# it reports anything, which is the normal case here, and grep -c exits non-zero
# on a count of 0. Under `set -e` either would abort before the comparison that
# is the whole job.
#
# But the exit code has to be inspected rather than discarded, because only 0 and
# 1 mean "the linter ran". Anything else -- 3 is the common one, emitted when the
# binary on PATH is v1 and reads this repo's v2 config -- means it never
# inspected a single interface, and the count would be a silent 0. That 0 then
# reports as "interface width went DOWN", which is a confident, specific, and
# entirely wrong diagnosis. An instrument that did not run must say so rather
# than answer with a plausible number.
set +e
output=$(golangci-lint run --enable-only interfacebloat ./... 2>&1)
rc=$?
set -e
if [[ "$rc" -ne 0 && "$rc" -ne 1 ]]; then
  printf '%s\n' "$output" >&2
  cat >&2 <<MSG

FAIL: golangci-lint exited $rc, so it did not produce a usable finding count and
this gate measured nothing. Exit 0 means clean and 1 means findings; anything
else is a run failure, most often a version mismatch -- this repo's .golangci.yml
is v2 format, and a v1 binary earlier on PATH exits 3 without linting anything.

  golangci-lint --version   # expected: 2.x, CI pins v2.12.2

MSG
  exit 2
fi
actual=$(printf '%s\n' "$output" | grep -cE '\(interfacebloat\)$' || true)

echo "interface-width: baseline=$baseline actual=$actual"

if [[ "$actual" -gt "$baseline" ]]; then
  echo
  printf '%s\n' "$output" | grep -E '\(interfacebloat\)$' || true
  cat >&2 <<MSG

FAIL: interface width went UP ($baseline -> $actual).

Split the new wide interface into focused pieces, keeping the original name as
their composition so the method set stays byte-identical and no consumer moves
(scripts/split_interface.py does this; scripts/verify_interface_split.py proves
the signature set is unchanged).

If the width is genuinely justified, annotate that one declaration with
    //nolint:interfacebloat // <why this interface has to be this wide>
An override without an explanation, or a bare //nolint, is rejected by
nolintlint -- it has to say why.
MSG
  exit 1
fi

if [[ "$actual" -lt "$baseline" ]]; then
  cat >&2 <<MSG

FAIL: interface width went DOWN ($baseline -> $actual) but the baseline was not
lowered. Set the number in $BASELINE_FILE to $actual in this same PR so the
ratchet holds the ground you just took.
MSG
  exit 1
fi

echo "interface-width: OK"
