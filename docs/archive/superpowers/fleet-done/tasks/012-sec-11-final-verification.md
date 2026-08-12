# Task 012: SEC-AUDIT-11 — Final security alert verification

**Depends on:** tasks 001–011 all merged
**Estimated effort:** S (1 hour)
**Wave:** 5 (last task of the security sweep)

## Goal

Verify that all 236 open security alerts have been addressed — either fixed or consciously
dismissed with rationale. Confirm 0 (or only accepted-risk) open alerts remain.

## Context

- Alert inventory as of 2026-05-03: 236 open (231 error/high, 5 warning/medium)
- After tasks 001–010: all high/error alerts should be fixed or dismissed
- This task is the gate — do not mark the security sweep complete until this passes

## Instructions

### 1. Pull current open alerts

```bash
# Total open count
gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&per_page=100" \
  --paginate | jq '[.[] | select(.state == "open")] | length'

# Breakdown by rule
gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&per_page=100" \
  --paginate | jq 'group_by(.rule.id) | map({rule: .[0].rule.id, count: length}) | sort_by(.count) | reverse'

# Open Dependabot alerts
gh api "repos/jdfalk/audiobook-organizer/dependabot/alerts?state=open" \
  | jq '[.[] | {number:.number, package:.security_advisory.cve_id, severity:.security_advisory.severity}]'
```

### 2. Review remaining open alerts

For each remaining open alert:
- If it's a legitimate issue not yet fixed: create a follow-up TODO entry and document why
  it's deferred
- If it's a true negative (false positive): dismiss it via the GitHub UI or API with a
  rationale comment
- If it's an accepted risk: document in `docs/security/path-validation-policy.md`

### 3. Dismiss false positives via API (if needed)

```bash
gh api -X PATCH "repos/jdfalk/audiobook-organizer/code-scanning/alerts/<ALERT_NUMBER>" \
  --field state=dismissed \
  --field dismissed_reason=false_positive \
  --field dismissed_comment="Path validated by util.SafeJoin; see docs/security/path-validation-policy.md"
```

### 4. Acceptance criteria

- ✅ All 236 previously-open alerts are either fixed or dismissed with rationale
- ✅ `govulncheck -mode=binary` passes in CI (task 001)
- ✅ `npm audit --audit-level=high` passes (task 002)
- ✅ `make ci` passes on main

### 5. Write summary

Create `docs/security/audit-2026-05-03/closeout.md` with:
- Final alert counts (before vs. after)
- Summary of what was fixed vs. dismissed
- Any deferred items with rationale
- Date of closeout

## Commit

```
docs(security): audit closeout — final verification (SEC-AUDIT-11)
```

## PR title

`docs(security): security audit closeout — SEC-AUDIT-11`

## After merging

Mark `- [ ] **SEC-AUDIT-11**` as `- [x]` in `TODO.md`.
The security sweep is complete.
