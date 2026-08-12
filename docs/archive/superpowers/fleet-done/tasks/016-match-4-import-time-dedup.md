# Task 016: MATCH-4 — Deduplicate on metadata hash at import time

**Depends on:** none
**Estimated effort:** S–M
**Wave:** 6 (features, independent)

## Goal

When a new book is scanned and its computed `metadata_source_hash` matches an existing book,
automatically flag/merge instead of creating a new duplicate record.

## Context

- `metadata_source_hash` is `sha256("{source}:{canonical_id}")` stored on books table (migration 055)
- Populated in `ApplyMetadataCandidate` via the apply pipeline
- Store interface: check for `GetBookByMetadataHash` or similar — if not present, add it
- Dedup engine: `internal/dedup/engine.go`
- Scanner creates new book records in `internal/scanner/service.go` or `internal/importer/service.go`
- PebbleDB is the production DB: `internal/database/pebble_store.go`

## Files to modify

- `internal/database/store.go` — add `GetBookByMetadataSourceHash(ctx, hash string) (*Book, error)`
- `internal/database/pebble_store.go` — implement it
- `internal/scanner/service.go` or `internal/importer/service.go` — call dedup check post-import
- `internal/dedup/engine.go` — add `CheckMetadataHashDuplicate` helper

## Instructions

### 1. Add store method

```go
// GetBookByMetadataSourceHash returns the book with the given metadata_source_hash,
// or nil if none exists.
GetBookByMetadataSourceHash(ctx context.Context, hash string) (*Book, error)
```

Implement in `pebble_store.go` using the existing PebbleDB index patterns (look at how
`GetBookByID` or `GetBookByPath` are implemented for reference).

### 2. Add dedup check at import time

In the scanner/importer, after a new book record is created and metadata is applied (so the
hash is set), call:

```go
existing, err := s.store.GetBookByMetadataSourceHash(ctx, newBook.MetadataSourceHash)
if err == nil && existing != nil && existing.ID != newBook.ID {
    slog.Info("metadata hash duplicate detected at import",
        "new_book_id", newBook.ID,
        "existing_book_id", existing.ID,
        "hash", newBook.MetadataSourceHash,
    )
    // Flag both books for dedup review (don't auto-merge — let the user decide)
    _ = s.store.FlagForDedupReview(ctx, newBook.ID, existing.ID)
}
```

### 3. Add `FlagForDedupReview` if not present

Check if this method exists. If not, it's a simple store write that marks a pair of books
as dedup candidates. Look at how existing dedup candidates are stored in `internal/dedup/`.

### 4. Surface in UI (optional for this PR)

The dedup tab should already show flagged candidates. Verify that books flagged via
`FlagForDedupReview` appear in `BookDedup.tsx`. If not, that's a follow-up.

## Test

```bash
go test ./internal/database/... -run TestMetadataHash -v -count=1
go test ./internal/scanner/... -v -count=1
make ci
```

## Commit

```
feat(dedup): flag metadata-hash duplicates at import time (MATCH-4)
```

## PR title

`feat(dedup): import-time metadata hash dedup — MATCH-4`

## After merging

Mark `- [ ] **MATCH-4**` as `- [x]` in `TODO.md`.
