# Task 019: BACKFILL-ASYNC-2 — Async metadata-hash backfill operation

**Depends on:** none (independent of task 018)
**Estimated effort:** M
**Wave:** 7 (async operations, parallel with task 018)

## Goal

Convert `handleBackfillMetadataSourceHash` to an async queued operation with checkpoint
resume and activity summary — same pattern as BACKFILL-ASYNC-1 but for `metadata_source_hash`.

## Context

- Current handler: search for `handleBackfillMetadataSourceHash` in `internal/server/`
- `metadata_source_hash` is `sha256("{source}:{canonical_id}")` on the books table (migration 055)
- Computed in `ApplyMetadataCandidate` — the backfill reruns that computation for all books
  that have metadata but no hash yet
- Checkpoint key: use `PhaseIndex` (book index) to resume from where it stopped

## Files to modify

- `internal/operations/state.go` — add `BackfillMetadataHashParams{DryRun bool, Force bool}`
- Maintenance plugin registration — add `"backfill-metadata-source-hash"` op
- Handler file — convert to enqueue-and-return

## Instructions

### 1. Define params

```go
type BackfillMetadataHashParams struct {
    DryRun bool `json:"dry_run"`
    Force  bool `json:"force"` // re-compute even if hash already set
}
```

### 2. Register and implement the operation

Follow the same pattern as task 018 (`runBackfillFileHashes`) but:
- Iterate all books (use `GetAllBooks` or `GetAllBookSummaries`)
- For each book with a `MetadataSourceURL` or `MetadataSource` + canonical ID:
  - Compute `sha256(fmt.Sprintf("%s:%s", source, canonicalID))`
  - Skip if hash already set AND `!params.Force`
  - Update via `store.UpdateBookMetadataSourceHash(ctx, book.ID, hash)` (add if missing)
- Checkpoint every 100 books
- Emit activity summary on completion

### 3. Add store method if missing

```go
// In store.go:
UpdateBookMetadataSourceHash(ctx context.Context, bookID, hash string) error
GetBooksWithoutMetadataSourceHash(ctx context.Context) ([]BookSummary, error)
```

Implement in `pebble_store.go`.

### 4. Convert handler to enqueue-and-return

Same pattern as task 018.

## Test

```bash
go test ./internal/server/... -run TestBackfillMetadata -v -count=1
make ci
```

## Commit

```
feat(ops): async backfill-metadata-source-hash operation (BACKFILL-ASYNC-2)
```

## PR title

`feat(ops): async metadata-hash backfill — BACKFILL-ASYNC-2`

## After merging

Mark `- [ ] **BACKFILL-ASYNC-2**` as `- [x]` in `TODO.md`.
