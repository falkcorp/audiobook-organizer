#!/usr/bin/env bash
# file: scripts/check-errcheck-ratchet.sh
# version: 1.0.0
# guid: 9c3e7a1d-5b48-4f0a-8e6c-2d1a9f7b3c05
# last-edited: 2026-09-25
#
# Ratchet on the number of `errcheck` findings, exactly as
# scripts/check-interface-width.sh ratchets `interfacebloat`.
#
# Why a ratchet and not a plain pass/fail gate: Wave 0 of the silent-failure
# sweep (docs/audits/2026-08-11-silent-failure-error-discards.md) wired errcheck
# into .golangci.yml with the bucket-(f) exclude list, but left ~834 pre-existing
# findings unfixed on purpose -- fixing them one at a time is waves 4-13. A bare
# `golangci-lint run --enable-only errcheck` is permanently red at that count and
# gets switched off by whoever sees it next (the exact failure mode
# .golangci.yml's own header warns about). A ratchet answers "did the count go
# UP" instead of "are there any findings", so it can gate from day one while the
# backlog is paid down wave by wave -- same shape as interface-width, which
# solved the identical problem for interfacebloat.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

# Scope the result cache to THIS checkout. See check-interface-width.sh's long
# comment for why this matters: golangci-lint's cache is keyed by file CONTENT,
# every worktree of this repo shares a module path with byte-identical files,
# and a finding's PATH (used to resolve `//nolint` suppression) is not cosmetic.
# A shared cache can silently over- or under-count across worktrees.
cache_home=${XDG_CACHE_HOME:-$HOME/.cache}
if command -v shasum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | shasum -a 256 | cut -d' ' -f1)
elif command -v sha256sum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | sha256sum | cut -d' ' -f1)
else
  echo "FAIL: neither shasum nor sha256sum found; cannot scope the lint cache" >&2
  exit 2
fi
export GOLANGCI_LINT_CACHE="$cache_home/golangci-lint-errcheck/${cache_key:0:16}"
BASELINE_FILE=.errcheck-baseline
baseline=$(grep -vE '^\s*(#|$)' "$BASELINE_FILE" | head -1 | tr -d '[:space:]')

if ! [[ "$baseline" =~ ^[0-9]+$ ]]; then
  echo "FAIL: $BASELINE_FILE does not contain a bare integer (read: '$baseline')" >&2
  exit 2
fi

# --enable-only overrides the `enable:` list in .golangci.yml but NOT its
# `settings:` block, so check-blank and the bucket-(f) exclude-functions list
# still apply -- only errcheck runs, with Wave 0's exemptions intact.
set +e
output=$(golangci-lint run --enable-only errcheck ./... 2>&1)
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

# Same stale-path defense as check-interface-width.sh: a finding whose file does
# not exist on disk means the cache replayed an entry from another worktree, and
# any //nolint at that position went unread.
stale=$({ printf '%s\n' "$output" | grep -E '\(errcheck\)$' || true; } \
  | sed -E 's/^([^:]+):[0-9]+:[0-9]+:.*/\1/' | sort -u \
  | while IFS= read -r f; do [[ -n "$f" && -f "$f" ]] || printf '%s\n' "$f"; done)
if [[ -n "$stale" ]]; then
  printf '%s\n' "$stale" | sed 's/^/  /' >&2
  cat >&2 <<MSG

FAIL: the paths above were reported by golangci-lint but do not exist on disk, so
its result cache is replaying stale entries from another checkout of this module.
Any //nolint on those declarations went unread, so this count is not a
measurement in either direction.

  golangci-lint cache clean

MSG
  exit 2
fi

# grep -c exits non-zero on a count of 0, which is reachable once the backlog is
# paid down to zero; `|| true` keeps that from aborting under set -e.
actual=$(printf '%s\n' "$output" | grep -cE '\(errcheck\)$' || true)

echo "errcheck-ratchet: baseline=$baseline actual=$actual"

if [[ "$actual" -gt "$baseline" ]]; then
  echo
  printf '%s\n' "$output" | grep -E '\(errcheck\)$' || true
  cat >&2 <<MSG

FAIL: errcheck findings went UP ($baseline -> $actual).

A new discarded error return was introduced. Either handle it, log it with
internal/errhandling.MustLog (see its doc comment for when that is the right
call), or -- if it is genuinely bucket (f) progress/log reporting -- add it BY
FULLY-QUALIFIED NAME to .golangci.yml's errcheck.exclude-functions list and say
why in a comment next to it.
MSG
  exit 1
fi

if [[ "$actual" -lt "$baseline" ]]; then
  cat >&2 <<MSG

FAIL: errcheck findings went DOWN ($baseline -> $actual) but the baseline was
not lowered. Set the number in $BASELINE_FILE to $actual in this same PR so the
ratchet holds the ground you just took -- this is expected and good news, it
just needs to be recorded, exactly like paying down the interface-width
baseline.
MSG
  exit 1
fi

echo "errcheck-ratchet: OK"
