# Task 011: SEC-AUDIT-10 — Document path validation policy + dismissal comments

**Depends on:** tasks 006–010 (at least partially merged so the policy reflects what's shipped)
**Estimated effort:** S (1.5 hours)
**Wave:** 5 (after Wave 4)

## Goal

Create `docs/security/path-validation-policy.md` documenting the project's path validation
approach, and add dismissal rationale comments to the 13 already-dismissed CodeQL alerts
(#560–#547) in `internal/maintenance/jobs/`.

## Context

- 13 alerts in `internal/maintenance/jobs/` were previously dismissed (#544–#560) because
  they all use `util.SafeJoin` — they are true negatives, not vulnerabilities
- The policy doc serves as the authoritative reference for why certain patterns are safe
- The `internal/security/safepath` package (task 005) is the canonical validation mechanism

## Files to create/modify

- `docs/security/path-validation-policy.md` (new)
- `internal/maintenance/jobs/*.go` — add `// #nosec` comments with rationale on dismissed lines

## Instructions

### 1. Create `docs/security/path-validation-policy.md`

Write a policy document covering:
- **Rule 1:** All filesystem paths that include values from user input, the database, or
  external sources MUST be constructed via `internal/security/safepath.Join` or
  validated with `internal/util.WithinRoot` before use.
- **Rule 2:** `safepath.SafePath` is the preferred type for passing validated paths between
  functions. Where string return is needed for external APIs, call `.String()`.
- **Rule 3:** Protected paths (iTunes mount, Deluge download dir) are additionally guarded
  by `isProtectedPath` — SafePath validation and protected-path checks are complementary.
- **Dismissed alerts:** List the 13 dismissed alerts (#544–#560) with the rationale that
  each uses `util.SafeJoin` which is now declared as a sanitizer in the CodeQL MaD pack.
- **Sanitizer declarations:** Reference `.github/codeql/models/go-sanitizers.yml`.
- **Review checklist:** Steps for reviewing new filesystem code for path injection risks.

### 2. Add dismissal comments to `internal/maintenance/jobs/`

Find the 13 already-dismissed alerts. For each flagged line add:
```go
// #nosec G304 -- path validated by util.SafeJoin above; see docs/security/path-validation-policy.md
```

Use the CodeQL alert API to find the exact lines:
```bash
gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=dismissed&per_page=100" \
  | jq '.[] | select(.most_recent_instance.location.path | contains("maintenance/jobs")) | {number:.number, path:.most_recent_instance.location.path, line:.most_recent_instance.location.start_line}'
```

## Test

```bash
make ci   # ensure comments don't break compilation
```

Manual: confirm `docs/security/path-validation-policy.md` renders correctly on GitHub.

## Commit

```
docs(security): path validation policy + dismissal rationale comments (SEC-AUDIT-10)
```

## PR title

`docs(security): path validation policy — SEC-AUDIT-10`

## After merging

Mark `- [ ] **SEC-AUDIT-10**` as `- [x]` in `TODO.md`.
