# Task 008: SEC-AUDIT-4 — Fix path injection in iTunes/transfer/audiobook handlers

**Depends on:** task 005 (SafePath package must be merged first)
**Estimated effort:** M (5–6 hours)
**Wave:** 4 (parallel with tasks 006, 007, 009–010, after task 005 merges)

## Goal

Fix 20+ path-injection CodeQL alerts in iTunes handlers, transfer service, audiobook handlers,
and audiobook service by replacing unguarded filepath operations with `safepath.Join`.

## Context

- Alerts: #627–#603, #619–#588 (exact numbers may differ post MaD pack)
- Files:
  - `internal/server/itunes_handlers.go` — handler layer for iTunes sync/import
  - `internal/itunes/service/transfer.go` — file transfer between iTunes and library
  - `internal/server/audiobooks_handlers.go` — audiobook CRUD handlers
  - `internal/audiobooks/service.go` — audiobook business logic
  - `internal/server/server.go` — possibly some path construction during startup
- iTunes paths are particularly sensitive: they reference a remote Windows machine mount;
  the scanner must NEVER write to iTunes paths (protected path check via `isProtectedPath`)

## Files to modify

- `internal/server/itunes_handlers.go`
- `internal/itunes/service/transfer.go`
- `internal/server/audiobooks_handlers.go`
- `internal/audiobooks/service.go`

## Instructions

1. Get precise alert locations:
   ```bash
   gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100" \
     | jq '.[] | select(.rule.id == "go/path-injection") | select(
         (.most_recent_instance.location.path | contains("itunes")) or
         (.most_recent_instance.location.path | contains("audiobooks"))
       ) | {number:.number, path:.most_recent_instance.location.path, line:.most_recent_instance.location.start_line}'
   ```

2. For iTunes handlers: when a user-supplied file path flows into `os.Open`, `os.Stat`,
   or `filepath.Join`, validate it with `safepath.Join` against the known iTunes mount root
   (from `AppConfig`). Return 400 on escape attempts.

3. For the transfer service: transfer paths go from iTunes mount to library root. Both
   endpoints need separate SafePath validation against their respective roots.

4. For audiobook handlers: paths from book records (DB-sourced) are generally trusted, but
   any path derived from query params or request bodies must be validated.

5. Keep the existing `isProtectedPath` check — `safepath.Join` is additive protection, not
   a replacement.

6. Bump version headers on every modified file.

## Test

```bash
go test ./internal/server/... -run TestITunes -v -count=1
go test ./internal/audiobooks/... -v -count=1
go test ./internal/itunes/... -v -count=1
make ci
```

## Commit

```
fix(itunes): replace unsafe filepath ops with safepath.Join (SEC-AUDIT-4)
```

## PR title

`fix(security): path injection in iTunes/audiobook handlers — SEC-AUDIT-4`

## After merging

Mark `- [ ] **SEC-AUDIT-4**` as `- [x]` in `TODO.md`.
