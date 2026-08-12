# Task 038: TECHDEBT-1 — Deprecated code sweep

**Depends on:** none
**Estimated effort:** M
**Wave:** 10 (UI polish + tech debt)

## Goal

Sweep the codebase for deprecated code and fix it. Four clusters, each ships as its own PR:

1. **React Router v7 future flags** — opt in to suppress deprecation warnings
2. **Go deprecated APIs** — replace `ioutil.*` with `io`/`os`, remove unused mocks, clean `staticcheck`
3. **Frontend dependencies** — `npm outdated`, remove any dead Material-UI v4 imports, kill stray `console.log`
4. **Test hygiene** — replace `t.Skip` markers, remove `//nolint` that no longer apply

## Context

- React Router: the project uses v6; future flags `v7_startTransition` and `v7_relativeSplatPath`
  can be opted in via `<BrowserRouter future={{...}}>` in `web/src/App.tsx`
- `ioutil`: deprecated since Go 1.16; `ioutil.ReadFile` → `os.ReadFile`, `ioutil.NopCloser` → `io.NopCloser`, etc.
- MockStore: `internal/database/mocks/` — check for mocks of deleted interfaces
- `staticcheck`: run `staticcheck ./...` to find deprecated API usage

## Files to modify

Cluster A — React Router:
- `web/src/App.tsx` — add `future` prop to `<BrowserRouter>`
- `web/src/setupTests.ts` (or test setup) — add `future` to `<MemoryRouter>` in tests

Cluster B — Go deprecated APIs:
- Any file with `"io/ioutil"` import (search: `grep -rn '"io/ioutil"' --include="*.go" .`)

Cluster C — Frontend deps + console.log:
- `web/package.json` + `web/package-lock.json`
- Any `console.log` in production source (not catch blocks): `grep -rn "console\.log" web/src/ | grep -v "_test\|\.test\." | grep -v "//"`

Cluster D — Test hygiene:
- Files with `t.Skip(`: `grep -rn "t\.Skip" --include="*.go" internal/`
- Files with `//nolint` comments: verify each is still needed

## Instructions

### Cluster A: React Router

In `web/src/App.tsx`, find `<BrowserRouter>` and add:
```tsx
<BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
```

In test files that use `<MemoryRouter>`, add the same `future` prop.
Verify no React Router deprecation warnings in browser console after this change.

### Cluster B: Go ioutil

```bash
grep -rln '"io/ioutil"' --include="*.go" .
```

For each file, replace:
- `ioutil.ReadFile(f)` → `os.ReadFile(f)`
- `ioutil.WriteFile(f, d, m)` → `os.WriteFile(f, d, m)`
- `ioutil.NopCloser(r)` → `io.NopCloser(r)`
- `ioutil.Discard` → `io.Discard`
- `ioutil.TempDir(...)` → `os.MkdirTemp(...)`
- `ioutil.TempFile(...)` → `os.CreateTemp(...)`

Then remove the `"io/ioutil"` import.

### Cluster C: Frontend

```bash
cd web && npm outdated   # review what's outdated
npm audit               # check for new advisories
grep -rn "console\.log" src/ | grep -v "\.test\." | grep -v "// "
```

Remove or downgrade any `console.log` calls to `console.debug` or remove entirely.

### Cluster D: Test hygiene

For each `t.Skip(...)`, add a comment explaining WHY it's skipped, or remove the skip if
the underlying issue is fixed. For each `//nolint`, verify the lint issue still applies —
remove the directive if it doesn't.

## Test

```bash
make ci
staticcheck ./...
cd web && npm run coverage
```

## Commit (one per cluster)

```
chore(fe): opt into React Router v7 future flags (TECHDEBT-1)
chore(go): replace io/ioutil with io/os stdlib (TECHDEBT-1)
chore(fe): remove stray console.log + update npm deps (TECHDEBT-1)
chore(test): document or remove t.Skip markers + stale nolint directives (TECHDEBT-1)
```

## PR title

`chore: deprecated code sweep — TECHDEBT-1`

## After merging

Mark `- [ ] **TECHDEBT-1**` as `- [x]` in `TODO.md`.
