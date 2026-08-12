# Task 009: SEC-AUDIT-5 — Fix path injection in scanner/reconcile/OpenLibrary

**Depends on:** task 005 (SafePath package must be merged first)
**Estimated effort:** M (4–5 hours)
**Wave:** 4 (parallel with tasks 006–008, 010, after task 005 merges)

## Goal

Fix 15+ path-injection CodeQL alerts in scanner, reconcile, OpenLibrary, and importer
services by replacing unguarded filepath operations with `safepath.Join`.

## Context

- Alerts: #618–#608
- Files:
  - `internal/scanner/service.go` — filesystem scanning logic
  - `internal/reconcile/reconcile.go` — reconciles DB records with filesystem state
  - `internal/server/openlibrary_service.go` — OpenLibrary metadata fetch (may have path components)
  - `internal/importer/service.go` — imports files into the library

## Files to modify

- `internal/scanner/service.go`
- `internal/reconcile/reconcile.go`
- `internal/server/openlibrary_service.go` (if it has filesystem paths)
- `internal/importer/service.go`

## Instructions

1. Get precise alert locations:
   ```bash
   gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100" \
     | jq '.[] | select(.rule.id == "go/path-injection") | select(
         (.most_recent_instance.location.path | contains("scanner")) or
         (.most_recent_instance.location.path | contains("reconcile")) or
         (.most_recent_instance.location.path | contains("openlibrary")) or
         (.most_recent_instance.location.path | contains("importer"))
       ) | {number:.number, path:.most_recent_instance.location.path, line:.most_recent_instance.location.start_line}'
   ```

2. For the scanner: scan roots come from `AppConfig.ImportPaths`. Any path constructed by
   combining an import root with a DB-sourced or filesystem-derived filename should use
   `safepath.Join(importRoot, relativePath)`. The scanner's `isExcluded` and `walkDirFn`
   paths need careful review.

3. For reconcile: it reads paths from the DB and checks them on disk. DB-sourced paths are
   generally trusted but should still be validated against the library root to prevent
   any injection via corrupted DB records.

4. For openlibrary_service.go: if it touches the filesystem (e.g., writing a downloaded
   cover to disk), validate the destination path. If it's pure HTTP, it may have no path
   alerts — skip if the alert list is empty for this file.

5. For importer: imported file paths come from user-supplied import paths or Deluge data.
   Validate all destination paths against `AppConfig.RootDir` before writing.

6. Bump version headers on every modified file.

## Test

```bash
go test ./internal/scanner/... -v -count=1
go test ./internal/reconcile/... -v -count=1
go test ./internal/importer/... -v -count=1
make ci
```

## Commit

```
fix(scanner): replace unsafe filepath ops with safepath.Join (SEC-AUDIT-5)
```

## PR title

`fix(security): path injection in scanner/reconcile/importer — SEC-AUDIT-5`

## After merging

Mark `- [ ] **SEC-AUDIT-5**` as `- [x]` in `TODO.md`.
