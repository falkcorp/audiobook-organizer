# Task 022: 1.13 — Broken-files dashboard card + repair pipeline

**Depends on:** none (pairs well with task 021 but is independent)
**Estimated effort:** L
**Wave:** 7 (async operations)
**Spec:** `docs/superpowers/bot-tasks/2026-05-04-broken-files-card-and-repair.md`

## Goal

Persist per-file ffmpeg/fingerprint errors to `book_file_errors` table, surface a dashboard
card "N books with broken files", add a library facet, and wire a repair pipeline.

## Context

Full spec: `docs/superpowers/bot-tasks/2026-05-04-broken-files-card-and-repair.md`

Key points:
- New table `book_file_errors`: `(file_path TEXT PK, book_id TEXT, error_class TEXT, last_message TEXT, occurrences INT, first_seen, last_seen)`
- Error sources: `internal/fingerprint/` and `internal/server/acoustid_backfill.go` emit ffmpeg errors
- Dashboard: extend `GET /api/v1/dashboard/stats` to include `broken_file_count`
- Library: add facet `has_file_errors=true` to `GET /api/v1/audiobooks` filter
- Repair actions: remux (ffmpeg), restore from `.versions/`, mark-ignored, delete-and-rescan
- PebbleDB is production — implement the new table as a PebbleDB store

## Files to create/modify

- `internal/database/pebble_store.go` — add `book_file_errors` operations
- `internal/database/store.go` — add `RecordFileError`, `ListBooksWithFileErrors`, `ClearFileError`, `GetBrokenFileCount`
- `internal/fingerprint/` and `internal/server/acoustid_backfill.go` — call `RecordFileError` on ffmpeg failure
- `internal/server/dashboard_handlers.go` — include `broken_file_count` in stats
- `internal/repair/repair.go` (new package) — repair pipeline
- `internal/server/` — new repair routes: `POST /api/v1/books/:id/repair`
- `web/src/pages/Dashboard.tsx` — add broken-files card
- `web/src/pages/Library.tsx` — add `has_file_errors` facet
- `web/src/pages/BookDetail.tsx` — errors section in Files tab

## Instructions

### 1. PebbleDB schema for file errors

Store as Pebble key `book_file_error:{file_path}` with value `BookFileError` struct.
Add secondary index `book_file_errors_by_book:{book_id}:{file_path}` for book-level lookup.

### 2. Store methods

```go
RecordFileError(ctx context.Context, filePath, bookID, errClass, message string) error
ListBooksWithFileErrors(ctx context.Context) ([]BookWithErrorCount, error)
ClearFileError(ctx context.Context, filePath string) error
GetBrokenFileCount(ctx context.Context) (int, error)
```

### 3. Wire error capture

In `internal/server/acoustid_backfill.go` and `internal/fingerprint/`, when ffmpeg returns
non-zero or chromaprint fails, classify the error and call `store.RecordFileError`.
Error classes: `ffmpeg_invalid_data`, `corrupt_mp3_frames`, `fingerprint_timeout`, `missing_file`.

### 4. Dashboard card

Add to the stats response: `{"broken_file_count": 42}`. In `Dashboard.tsx`, show:
```
⚠ 42 books have broken files → [Review]
```
Clicking navigates to library with `has_file_errors=true`.

### 5. Repair pipeline (`internal/repair/repair.go`)

```go
type Repairer struct { store database.Store; ... }

func (r *Repairer) RemuxFile(ctx context.Context, filePath string) error { ... }   // ffmpeg -i in.mp3 -c copy out.mp3
func (r *Repairer) RestoreFromVersion(ctx context.Context, bookID, fileID string) error { ... }
func (r *Repairer) MarkIgnored(ctx context.Context, filePath string) error { ... }
func (r *Repairer) DeleteAndRescan(ctx context.Context, filePath string) error { ... }
```

Wire as `POST /api/v1/books/:id/repair` with body `{"action": "remux"|"restore"|"ignore"|"delete_and_rescan", "file_path": "..."}`.

### 6. BookDetail files tab

Add an "Errors" section showing each broken file with repair action buttons.

## Test

```bash
go test ./internal/database/... -run TestFileError -v -count=1
go test ./internal/repair/... -v -count=1
make ci
```

Manual: trigger an AcoustID scan on a known-bad file, verify it appears on dashboard card.

## Commit

```
feat(maintenance): broken-files dashboard card + repair pipeline (1.13)
```

## PR title

`feat(maintenance): broken-files card and repair pipeline — 1.13`

## After merging

Mark `- [ ] **1.13**` as `- [x]` in `TODO.md`.
