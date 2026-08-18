#!/usr/bin/env bash
# file: scripts/check-interface-width.sh
# version: 1.0.0
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
output=$(golangci-lint run --enable-only interfacebloat,nolintlint ./... 2>&1) || true
actual=$(printf '%s\n' "$output" | grep -cE '\((interfacebloat|nolintlint)\)$' || true)

echo "interface-width: baseline=$baseline actual=$actual"

if [[ "$actual" -gt "$baseline" ]]; then
  echo
  printf '%s\n' "$output" | grep -E '\((interfacebloat|nolintlint)\)$' || true
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
