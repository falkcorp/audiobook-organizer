#!/usr/bin/env bash
# file: scripts/gate-merge-pr.sh
# version: 1.0.0
# guid: 0c8f31a6-5d74-42be-9e07-b6a15f230cd8
# last-edited: 2026-08-09
#
# Wait for a PR's CI to finish, then admin-merge it ONLY if every check passed.
#
# Why this exists as a script rather than a snippet people retype:
#
#   1. "pending is not passing." A wait that times out must NOT merge. PR #2202
#      was merged on 2026-08-09 with its own E2E job still running, because the
#      loop treated a timeout as success.
#
#   2. AN EMPTY CHECK ROLLUP ALSO REPORTS pending=0 AND fail=0. Immediately
#      after `gh pr create`, GitHub has not registered the checks yet. A naive
#      `pending==0 && fail==0` gate therefore merges a brand-new PR with ZERO
#      checks run, and it looks exactly like a clean pass in the log. This was
#      caught on 2026-08-09 by noticing a gate report `poll 1 pending=0` — that
#      PR happened to be 40 minutes old so the merge was legitimate, but the
#      same code one minute after `gh pr create` would have merged blind.
#
# Both are the same failure: inferring "green" from an absence rather than from
# a positive result. The guards below require a POSITIVE signal — at least
# MIN_CHECKS checks present AND all of them concluded successfully.
#
# Usage:
#   scripts/gate-merge-pr.sh <pr-number> [max-polls] [sleep-seconds] [min-checks]
#
# Exit codes:
#   0  merged
#   1  usage / lookup error
#   2  a check failed or was cancelled — NOT merged
#   3  timed out while still pending — NOT merged
#   4  PR left the OPEN state without us merging it
set -euo pipefail

PR="${1:?usage: gate-merge-pr.sh <pr-number> [max-polls] [sleep-seconds] [min-checks]}"
MAX_POLLS="${2:-60}"
SLEEP_SECONDS="${3:-45}"
# Most PRs in this repo trigger ~20 checks. Requiring a handful to be present
# before believing "0 pending" is what closes the empty-rollup hole; it is a
# floor, not an exact count, so adding or removing a workflow will not break it.
MIN_CHECKS="${4:-5}"

for ((i = 1; i <= MAX_POLLS; i++)); do
  json=$(gh pr view "$PR" --json state,statusCheckRollup 2>/dev/null) || {
    echo "gate: could not read PR #$PR" >&2
    exit 1
  }

  read -r state total pending failed cancelled <<<"$(
    printf '%s' "$json" | python3 -c '
import sys, json
d = json.load(sys.stdin)
rollup = d.get("statusCheckRollup") or []
pending = sum(1 for c in rollup if c.get("status") in ("QUEUED", "IN_PROGRESS", "PENDING", "WAITING"))
failed = sum(1 for c in rollup if c.get("conclusion") in ("FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED"))
cancelled = sum(1 for c in rollup if c.get("conclusion") == "CANCELLED")
print(d.get("state"), len(rollup), pending, failed, cancelled)
'
  )"

  echo "gate PR#$PR poll $i/$MAX_POLLS state=$state checks=$total pending=$pending fail=$failed cancelled=$cancelled"

  if [[ "$state" != "OPEN" ]]; then
    echo "gate: PR #$PR is $state — not merging here."
    exit 4
  fi

  if ((failed > 0 || cancelled > 0)); then
    echo "gate: $failed failed / $cancelled cancelled — NOT merging."
    exit 2
  fi

  # The two positive conditions, both required.
  if ((total >= MIN_CHECKS && pending == 0)); then
    echo "gate: $total checks present, none pending, none failed — merging."
    gh pr merge "$PR" --rebase --admin
    # Verify rather than trust the exit code: report only what is true.
    merged_at=$(gh pr view "$PR" --json mergedAt -q .mergedAt)
    if [[ -z "$merged_at" || "$merged_at" == "null" ]]; then
      echo "gate: merge command returned but PR is NOT merged." >&2
      exit 1
    fi
    echo "gate: MERGED at $merged_at"
    exit 0
  fi

  if ((total < MIN_CHECKS)); then
    echo "gate: only $total checks registered (need >= $MIN_CHECKS) — checks are still appearing, waiting."
  fi

  sleep "$SLEEP_SECONDS"
done

echo "gate: timed out after $MAX_POLLS polls with checks still pending — NOT merging." >&2
exit 3
