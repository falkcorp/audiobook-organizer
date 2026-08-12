# Task 002: SEC-AUDIT-9 — Bump follow-redirects npm dependency

**Depends on:** none
**Estimated effort:** S (20 min)
**Wave:** 1 (run immediately, no dependencies)

## Goal

Bump the `follow-redirects` transitive npm dependency to ≥1.16.0 to fix
Dependabot alert #27 (GHSA-r4q5-vmmm-2653).

## Context

- The vulnerability is in a transitive dependency, not a direct one
- Working directory for npm commands: `web/`
- The repo enforces rebase/FF merges only: use `gh pr merge --rebase`

## Files to modify

- `web/package-lock.json` — updated by npm automatically

## Instructions

1. `cd web`
2. Run: `npm update follow-redirects`
3. Run: `npm audit fix` (catches any other auto-fixable issues)
4. Verify follow-redirects is now ≥1.16.0: `npm list follow-redirects`
5. Run `npm test` to confirm nothing broke
6. Stage only `web/package-lock.json` (not `package.json` unless it changed)

## Test

```bash
cd web && npm list follow-redirects   # should show ≥1.16.0
npm test                              # should pass
```

## Commit

```
fix(deps): bump follow-redirects to >=1.16.0 (SEC-AUDIT-9, GHSA-r4q5-vmmm-2653)
```

## PR title

`fix(deps): bump follow-redirects ≥1.16.0 — SEC-AUDIT-9`

## After merging

Mark `- [ ] **SEC-AUDIT-9**` as `- [x]` in `TODO.md`.
