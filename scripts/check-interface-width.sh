#!/usr/bin/env bash
# file: scripts/check-interface-width.sh
# version: 1.4.0
# guid: 5f1c07a3-84be-4d29-9e60-3b7a2d5c81ef
# last-edited: 2026-08-18
#
# Ratchet on the number of `interfacebloat` findings. See
# .interface-width-baseline for why this counts rather than listing files.
set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "$root"

# Scope the result cache to THIS checkout.
#
# golangci-lint's cache is keyed by file CONTENT, and every git worktree of this
# repo declares the same module path with byte-identical files. A shared cache
# therefore replays whichever PATH was recorded first, and a finding's path is
# not cosmetic: `//nolint` suppression is resolved by re-reading the source
# there, and .golangci.yml drops anything matching `\.worktrees/`. So a shared
# cache corrupts the count in BOTH directions, and both were measured on
# 2026-08-18 against a true count of 4:
#
#   overcount 6 -- run from .worktrees/pathrepair, BookReader and ServerDeps
#     attributed to ../absplit/ which had been deleted. No file to read, so their
#     explained //nolint went unseen and both leaked into the count.
#
#   undercount 0 -- run from the repo root while .worktrees/widthgate existed.
#     All four findings replayed with .worktrees/ paths, where the exclusion
#     swallowed them. The gate then reported "went DOWN (4 -> 0)" and advised
#     setting the baseline to 0, which would have disabled it permanently.
#
# The undercount is the dangerous one: it is a silent pass whose remediation text
# argues for making the silence permanent. Package loading is not the culprit and
# cannot be -- each sibling worktree has its own go.mod, so `go list ./...` from
# the root returns 0 packages under `.worktrees/`. Only cached POSITIONS cross
# the boundary, so isolating the cache closes both directions at the source.
cache_home=${XDG_CACHE_HOME:-$HOME/.cache}
if command -v shasum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | shasum -a 256 | cut -d' ' -f1)
elif command -v sha256sum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | sha256sum | cut -d' ' -f1)
else
  echo "FAIL: neither shasum nor sha256sum found; cannot scope the lint cache" >&2
  exit 2
fi
export GOLANGCI_LINT_CACHE="$cache_home/golangci-lint-width/${cache_key:0:16}"
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
# A finding's PATH is load-bearing, not cosmetic: golangci-lint resolves
# `//nolint` suppression by re-reading the source at the position it reported. If
# that path does not exist, no directive is found and the finding it was
# suppressing leaks into the count.
#
# That is reachable here. The result cache is keyed by file CONTENT, and every
# git worktree of this repo declares the same module path with byte-identical
# files, so an unchanged file replays whichever path was recorded first --
# including a path into a worktree that has since been deleted. Measured
# 2026-08-18 from .worktrees/pathrepair: 6 findings, two of them (BookReader,
# ServerDeps) attributed to ../absplit/... which was not on disk at all. Both
# carry an explained //nolint at exactly the reported line in the live tree.
# `golangci-lint cache clean` restored the true count of 4, matching CI.
#
# .golangci.yml excludes `\.worktrees/`, and its comment concludes that
# cross-worktree attribution "does not change the number". That held when it was
# written, before any //nolint:interfacebloat override existed -- suppressed
# findings are precisely the ones it does not hold for. The exclusion also only
# matches when the run starts at the repo root; line 11 cds to the worktree root,
# so a sibling renders as `../name/` and the pattern cannot fire.
#
# A poisoned cache is wrong in BOTH directions -- it inflates the count here, and
# nothing stops it from replaying a path whose line happens to carry an unrelated
# nolint and hiding a real finding. Neither number is a measurement, so refuse to
# report one.
stale=$(printf '%s\n' "$output" | grep -E '\(interfacebloat\)$' \
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
