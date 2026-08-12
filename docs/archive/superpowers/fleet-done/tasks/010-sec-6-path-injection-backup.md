# Task 010: SEC-AUDIT-6 — Fix path injection in backup/Deluge/remaining

**Depends on:** task 005 (SafePath package must be merged first)
**Estimated effort:** S (2–3 hours)
**Wave:** 4 (parallel with tasks 006–009, after task 005 merges)

## Goal

Fix 10+ path-injection CodeQL alerts in backup and Deluge import services.

## Context

- Alerts: #541, #535–#534, and others
- Files:
  - `internal/backup/backup.go` — creates and extracts backup archives
  - `internal/server/deluge_import_unix.go` — imports files from Deluge downloads
- The backup extractor already has `isPathWithinTarget` with `filepath.Rel` escape check
  (SEC-AUDIT-7d verified this), but CodeQL still flags some paths in the same file.
  The remaining alerts are likely in the archive creation path, not extraction.

## Files to modify

- `internal/backup/backup.go`
- `internal/server/deluge_import_unix.go`

## Instructions

1. Get precise alert locations:
   ```bash
   gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100" \
     | jq '.[] | select(.rule.id == "go/path-injection") | select(
         (.most_recent_instance.location.path | contains("backup")) or
         (.most_recent_instance.location.path | contains("deluge_import"))
       ) | {number:.number, path:.most_recent_instance.location.path, line:.most_recent_instance.location.start_line}'
   ```

2. For `backup.go` — archive creation path:
   Find any `filepath.Join(backupDir, filename)` where `filename` could be user-influenced
   (e.g., book path from the DB used as the archive member name). Replace with `safepath.Join`.
   For archive member names, additionally sanitize to be relative paths with no `..` components.

3. For `deluge_import_unix.go`:
   Deluge provides download paths. Validate that the source path (from Deluge) lies within
   the configured Deluge download directory (`AppConfig.DelugeDownloadDir` or similar).
   Replace raw `filepath.Join(downloadDir, torrentPath)` with `safepath.Join`.

4. Do NOT remove the existing `isPathWithinTarget` check in backup extraction — it's
   already correct. Only touch lines flagged by the alerts.

5. Bump version headers on every modified file.

## Test

```bash
go test ./internal/backup/... -v -count=1
make ci
```

## Commit

```
fix(backup): replace unsafe filepath ops with safepath.Join (SEC-AUDIT-6)
```

## PR title

`fix(security): path injection in backup/Deluge — SEC-AUDIT-6`

## After merging

Mark `- [ ] **SEC-AUDIT-6**` as `- [x]` in `TODO.md`.
