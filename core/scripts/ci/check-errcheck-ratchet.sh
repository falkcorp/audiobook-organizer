#!/usr/bin/env bash
# file: core/scripts/ci/check-errcheck-ratchet.sh
# version: 1.0.2
# guid: c13f1080-a3a4-46a7-95d6-d68fc528942a
# last-edited: 2026-11-07
#
# Ratchet on the number of `errcheck` findings in the core module: core's own
# copy of scripts/check-errcheck-ratchet.sh with its own baseline,
# core/.errcheck-baseline (09 §9.4 R1's CI classification: one baseline per
# module, so a move of code into core can never silently re-baseline the root's
# 770). It lives under core/scripts/ci/ so an export of core/ alone runs it
# (12 §12.3.11); it reads nothing above core/ and does not call git.
#
# ONE-WAY, like the root's (owner decision D47): only a count that goes UP
# fails. A drop prints a ::notice:: naming the new number and exits 0; lower
# core/.errcheck-baseline in the PR that earned it (the scheduled
# ratchet-lower job does it too). The baseline starts at the count measured on
# core's own code, not at any share of the root's.
#
# Config. golangci-lint looks for its config in the current directory and then
# every parent. Inside this repository that finds the ROOT's .golangci.yml; in
# an export of core/ it finds nothing and falls back to defaults, and the two
# counts differ (the root config sets check-blank and `tests: false`). So this
# script writes the config it needs to a temp file and passes --config, which
# makes the count the same in both places. The settings are the root's for
# errcheck: check-blank ON (so `_ = f()` is a finding, which is the point of
# the gate), test files out of scope, and the bucket-(f) exemptions that name no
# ABO package. Add an exemption by its FULLY-QUALIFIED name and say why next to
# it, then confirm the count dropped.
#
# GOOS/GOARCH are pinned for the reason the root script gives: findings inside
# build-tagged files are visible only under the GOOS that selects the file, and
# CI runs linux/amd64.
set -euo pipefail

# --measure (07-G4): print only the measured count on stdout and exit 0, so the
# scheduled baseline-lowering job can read it. Every refusal path below
# (exit 2: no usable measurement) still applies; only the comparison is skipped.
measure_only=0
if [[ "${1:-}" == "--measure" ]]; then
  measure_only=1
fi

export GOOS=linux
export GOARCH=amd64

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

# Scope the result cache to THIS checkout (the root script's long comment says
# why: golangci-lint's cache is keyed by file content, so a checkout sharing it
# with another worktree of the same module can replay that one's paths and
# miscount in either direction). The key is the module directory's own path,
# resolved from this script's location, never from git: an export of core/
# alone has no repository.
cache_home=${XDG_CACHE_HOME:-$HOME/.cache}
if command -v shasum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | shasum -a 256 | cut -d' ' -f1)
elif command -v sha256sum >/dev/null 2>&1; then
  cache_key=$(printf '%s' "$root" | sha256sum | cut -d' ' -f1)
else
  echo "FAIL: neither shasum nor sha256sum found; cannot scope the lint cache" >&2
  exit 2
fi
export GOLANGCI_LINT_CACHE="$cache_home/golangci-lint-errcheck-core/${cache_key:0:16}"
BASELINE_FILE=.errcheck-baseline
baseline=$(grep -vE '^\s*(#|$)' "$BASELINE_FILE" | head -1 | tr -d '[:space:]')

if ! [[ "$baseline" =~ ^[0-9]+$ ]]; then
  echo "FAIL: $BASELINE_FILE does not contain a bare integer (read: '$baseline')" >&2
  exit 2
fi

# golangci-lint picks the config format from the file extension, so the file
# needs a .yml name: a temp directory, not a bare mktemp file.
cfgdir=$(mktemp -d "${TMPDIR:-/tmp}/core-golangci.XXXXXX")
trap 'rm -rf "$cfgdir"' EXIT
cfg="$cfgdir/golangci.yml"
cat > "$cfg" <<'YAML'
version: "2"
run:
  tests: false
  timeout: 15m
  # Paths are reported relative to the working directory (core/), not to the
  # temp directory this config lives in: the exclusions below and the on-disk
  # check after the run both read them.
  relative-path-mode: wd
linters:
  default: none
  enable:
    - errcheck
  settings:
    errcheck:
      check-blank: true
      check-type-assertions: false
      exclude-functions:
        - (io.Closer).Close
        - (io.ReadCloser).Close
        - (net.Conn).Close
        - (net.Listener).Close
        - (*github.com/cockroachdb/pebble/v2.Iterator).Close
        - (*github.com/cockroachdb/pebble/v2.Batch).Close
        - (*github.com/cockroachdb/pebble/v2.Snapshot).Close
        - (*encoding/json.Encoder).Encode
        - os.Remove
        - os.RemoveAll
        # Progress reporting: storage.LogReporter is the two-method slice of
        # ABO's registry.Reporter the compaction sampler needs, and its Log is
        # the op-log write, whose only failure mode is that a progress line did
        # not land. The root's .golangci.yml exempted it as
        # internal/compactprogress.LogReporter until R1-20 moved the package
        # into core/storage; the exemption moved with it.
        - (github.com/falkcorp/mediacore/storage.LogReporter).Log
        # A type test, not a dropped error: in `_, ok := errors.AsType[T](err)`
        # the blank is the matched value and ok carries the answer. `go fix`
        # (fix-check) rewrites errors.As to this form, so without the
        # exemption the two gates contradict each other.
        - errors.AsType
  exclusions:
    generated: lax
    paths:
      - ^web/
      - ^examples/[^/]+/web/
issues:
  max-issues-per-linter: 0
  max-same-issues: 0
YAML

# --enable-only is kept even though the generated config enables exactly one
# linter: it makes the count attributable no matter what config a future edit
# of this script produces.
set +e
output=$(golangci-lint run --config "$cfg" --enable-only errcheck ./... 2>&1)
rc=$?
set -e
if [[ "$rc" -ne 0 && "$rc" -ne 1 ]]; then
  printf '%s\n' "$output" >&2
  cat >&2 <<MSG

FAIL: golangci-lint exited $rc, so it did not produce a usable finding count and
this gate measured nothing. Exit 0 means clean and 1 means findings; anything
else is a run failure, most often a version mismatch -- the config generated
above is v2 format, and a v1 binary earlier on PATH exits 3 without linting
anything.

  golangci-lint --version   # expected: 2.x, CI pins v2.14.0

MSG
  exit 2
fi

# Exit 1 with no "N issues" summary is a third "did not run" shape: the binary
# started but died before linting (an "exec format error", a panic, an OOM kill
# reported through a wrapper). Only a run that reached its summary line produced
# a count.
if [[ "$rc" -eq 1 ]] && ! grep -qE '^[0-9]+ issues' <<< "$output"; then
  printf '%s\n' "$output" | tail -5 >&2
  cat >&2 <<MSG

FAIL: golangci-lint exited 1 without printing its "N issues" summary, so it did
not finish a run and the count above is not a measurement.

MSG
  exit 2
fi

# A typecheck failure is also "did not run": golangci-lint exits 1 for it, the
# same as for real findings, and under --enable-only every other linter's
# findings are dropped, so a tree the linter could not type-check reports ZERO
# findings and exit 1 -- the shape of a complete clean-up. Refuse the number.
if grep -qE '^\* typecheck: [0-9]+$|\(typecheck\)$' <<< "$output"; then
  printf '%s\n' "$output" | grep -E '\(typecheck\)$|^\* typecheck:' | head -5 >&2
  cat >&2 <<MSG

FAIL: golangci-lint could not type-check the tree (see the typecheck lines
above), so any count it printed is not a measurement. The usual cause is a Go
toolchain newer than the one golangci-lint's own x/tools understands: compare
\`go version\` with the Go that built the binary (\`golangci-lint version\`).

MSG
  exit 2
fi

# A finding whose file does not exist on disk means the result cache replayed an
# entry recorded for another checkout of this module, so any //nolint at that
# position went unread and the count is not a measurement in either direction.
# The `|| true` on the grep is load-bearing: grep exits 1 when it matches
# nothing, which is a CLEAN tree, and under `set -euo pipefail` that would abort
# the script here with an exit code indistinguishable from a ratchet failure.
stale=$({ printf '%s\n' "$output" | grep -E '\(errcheck\)$' || true; } \
  | sed -E 's/^([^:]+):[0-9]+:[0-9]+:.*/\1/' | sort -u \
  | while IFS= read -r f; do [[ -n "$f" && -f "$f" ]] || printf '%s\n' "$f"; done)
if [[ -n "$stale" ]]; then
  printf '%s\n' "$stale" | sed 's/^/  /' >&2
  cat >&2 <<MSG

FAIL: the paths above were reported by golangci-lint but do not exist on disk, so
its result cache is replaying stale entries from another checkout of this module.

  golangci-lint cache clean

MSG
  exit 2
fi

# grep -c exits non-zero on a count of 0, the normal case for core today;
# `|| true` keeps that from aborting under set -e.
actual=$(printf '%s\n' "$output" | grep -cE '\(errcheck\)$' || true)

if [[ "$measure_only" -eq 1 ]]; then
  echo "$actual"
  exit 0
fi

echo "errcheck-ratchet: baseline=$baseline actual=$actual"

if [[ "$actual" -gt "$baseline" ]]; then
  echo
  printf '%s\n' "$output" | grep -E '\(errcheck\)$' || true
  cat >&2 <<MSG

FAIL: core errcheck findings went UP ($baseline -> $actual).

A new discarded error return was introduced. Handle it, log it, or -- if it is
genuinely progress or log reporting whose only failure mode is "the report did
not update" -- add it BY FULLY-QUALIFIED NAME to the exclude-functions list in
this script and say why in a comment next to it.
MSG
  exit 1
fi

if [[ "$actual" -lt "$baseline" ]]; then
  # One-way ratchet (D47): a drop is good news, not a failure.
  echo "::notice::core errcheck findings went DOWN ($baseline -> $actual); lower core/$BASELINE_FILE to $actual (the scheduled ratchet-lower job does this)"
fi

echo "errcheck-ratchet: OK"
