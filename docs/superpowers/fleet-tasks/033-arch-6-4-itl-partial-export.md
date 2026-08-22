<!-- file: docs/superpowers/fleet-tasks/033-arch-6-4-itl-partial-export.md -->
<!-- version: 1.0.0 -->
<!-- guid: d1b039e9-60ce-4229-a2da-76babf19b968 -->
<!-- last-edited: 2026-08-22 -->

# Task 033: 6.4 — ITL partial export

**Depends on:** 7.9 (full iTunes library rebuild — needed for partial export to make sense)
**Estimated effort:** M
**Wave:** 9 (architecture)

## Goal

Implement ITL partial export: export a subset of the iTunes library as an ITL file
(e.g., a playlist, a selection of books, or books updated since a given date).

## Context

- Tasks 1-3 + 5 of 6.4 are already done (download, upload+validate, backup list+restore, frontend panel)
- Task 4 (partial export) depends on 7.9 (full rebuild from scratch) which provides the
  underlying ITL writing infrastructure
- The ITL format is a binary property list — `internal/itunes/itl.go` has the parser;
  check if a writer exists
- ITL writer: if it doesn't exist, this task also needs to implement write support

## Files to modify/create

- `internal/itunes/itl.go` (or `itl_writer.go`) — ITL write/marshal support if missing
- `internal/server/itunes_handlers.go` — add `POST /api/v1/itunes/export-partial` handler
- `web/src/` — add "Export Selection" button in iTunes panel

## Instructions

### 1. Check for ITL writer

```bash
grep -n "WriteITL\|MarshalITL\|EncodeITL" internal/itunes/itl.go
```

If no writer exists, this task is blocked on 7.9 which implements it. Document this dependency
and skip implementation until 7.9 ships.

### 2. If writer exists, implement partial export handler

```go
// POST /api/v1/itunes/export-partial
// Body: {"book_ids": ["id1", "id2"], "include_playlists": false}
// Response: ITL binary download
func (s *Server) handleExportPartialITL(c *gin.Context) {
    var req struct {
        BookIDs          []string `json:"book_ids"`
        IncludePlaylists bool     `json:"include_playlists"`
    }
    if err := c.ShouldBindJSON(&req); err != nil {
        httputil.RespondWithBadRequest(c, err.Error())
        return
    }

    books, err := s.store.GetBooksByIDs(c.Request.Context(), req.BookIDs)
    // Build minimal ITL containing only these books
    itlData, err := itunes.BuildITL(books, s.itunesService.GetLibraryMetadata())
    if err != nil {
        httputil.RespondWithError(c, http.StatusInternalServerError, err)
        return
    }

    c.Header("Content-Disposition", `attachment; filename="partial-library.itl"`)
    c.Data(http.StatusOK, "application/octet-stream", itlData)
}
```

### 3. Frontend

In the iTunes panel, add an "Export Selection" button that:
- Uses the currently selected books (from library selection state)
- Calls `POST /api/v1/itunes/export-partial`
- Downloads the resulting ITL file

## Test

```bash
go test ./internal/itunes/... -run TestExportPartial -v -count=1
make ci
```

## Commit

```
feat(itunes): partial ITL export for selected books (6.4)
```

## PR title

`feat(itunes): partial ITL export — 6.4`

## After merging

Mark `- [ ] **6.4**` as `- [x]` in `TODO.md`.
