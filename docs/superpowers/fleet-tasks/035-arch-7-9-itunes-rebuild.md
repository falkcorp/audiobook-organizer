<!-- file: docs/superpowers/fleet-tasks/035-arch-7-9-itunes-rebuild.md -->
<!-- version: 1.0.0 -->
<!-- guid: da2f5332-ecb5-444b-89eb-9a3769ed5d8d -->
<!-- last-edited: 2026-08-22 -->

# Task 035: 7.9 — Full iTunes library rebuild from scratch

**Depends on:** none
**Estimated effort:** L
**Wave:** 9 (architecture)

## Goal

Implement full iTunes library rebuild from scratch — generating a new .itl file from the
current library state, replacing the diff-and-batch mode that already ships.

## Context

- Diff-and-batch mode already shipped (commit `286140d`) — that's the incremental update path
- "Full rebuild" = generate an entirely new ITL binary from the library DB, not patching the existing one
- `internal/itunes/itl.go` — has the ITL parser; check if a writer/marshaler exists
- The ITL binary format is a tagged binary plist; writing requires understanding the format
- This enables task 033 (partial export) which needs an ITL writer

## Files to create/modify

- `internal/itunes/itl_writer.go` (new) — ITL binary marshal support
- `internal/server/itunes_handlers.go` — add `POST /api/v1/itunes/rebuild-library` handler
- `web/src/` — add "Rebuild Library" button in iTunes panel

## Instructions

### 1. Implement ITL writer

Study `internal/itunes/itl.go` to understand the binary format (magic bytes, header structure,
tagged fields, HDFM blocks, etc.). Implement a writer that:

```go
// MarshalITL serializes a LibraryData struct to the binary ITL format.
// The output can be written directly to an .itl file.
func MarshalITL(lib *LibraryData) ([]byte, error) { ... }
```

`LibraryData` should include:
- Library metadata (name, app version, date)
- Track list (one entry per book file, with all iTunes fields: PID, title, artist, album, duration, etc.)
- Playlist list (at minimum: "Library" master playlist containing all tracks)

Map `database.Book` fields to iTunes track fields:
- `Title` → Name
- `AuthorName` → Artist  
- `SeriesName` → Album
- `Duration` → Total Time (milliseconds)
- `FilePath` → Location (file:// URL)

### 2. Rebuild handler

```go
// POST /api/v1/itunes/rebuild-library
func (s *Server) handleRebuildITL(c *gin.Context) {
    books, err := s.store.GetAllBooks(c.Request.Context())
    lib := buildLibraryData(books)
    itlData, err := itunes.MarshalITL(lib)
    // Write to configured iTunes library path (backup old first)
    // Return {op_id: ...} as async operation
}
```

Make this an async Operation (like other long-running handlers) so it appears in Activity.

### 3. Frontend

In the iTunes Settings panel, add:
- "Rebuild Library" button (with confirmation dialog warning it replaces the iTunes library)
- Progress shown via the operations bell

## Test

```bash
go test ./internal/itunes/... -run TestMarshal -v -count=1
# Round-trip test: marshal a sample library, parse it back, verify fields match
make ci
```

## Commit

```
feat(itunes): full ITL library rebuild from scratch (7.9)
```

## PR title

`feat(itunes): full library rebuild from scratch — 7.9`

## After merging

Mark `- [ ] **7.9**` as `- [x]` in `TODO.md`.
Task 033 (partial export) can now proceed.
