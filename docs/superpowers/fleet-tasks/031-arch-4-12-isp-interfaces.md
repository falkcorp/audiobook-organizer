<!-- file: docs/superpowers/fleet-tasks/031-arch-4-12-isp-interfaces.md -->
<!-- version: 1.0.0 -->
<!-- guid: bac4b039-7823-432e-be76-2f2b05ce168f -->
<!-- last-edited: 2026-08-22 -->

# Task 031: 4.12 — Narrow extracted service deps to ISP sub-interfaces

**Depends on:** none (4.11 is already complete)
**Estimated effort:** M
**Wave:** 9 (architecture)

## Goal

Update extracted service packages to accept narrow store interfaces (e.g., `BookReader`)
instead of the full `database.Store` interface, reducing coupling and enabling easier testing.

## Context

- `internal/server/interfaces.go` already defines 4 narrow sub-interfaces:
  `BookReader`, `BookWriter`, `FileReader`, `FileWriter` (or similar names — check the file)
- Extracted packages from 4.11: `internal/audiobooks/`, `internal/aiscan/`, `internal/reconcile/`,
  `internal/scanner/`, `internal/importer/`, `internal/quarantine/`, `internal/writeback/`,
  `internal/fileops/`, `internal/sysinfo/`
- Plan in `docs/superpowers/plans/2026-04-17-store-iface-sweep.md` — read it for context

## Files to modify

- `internal/server/interfaces.go` — extend with any missing sub-interfaces
- Each extracted package's `Service` struct — change field type from `database.Store` to
  the narrowest interface that covers its actual usage
- Compile-time assertion files — add `var _ NarrowInterface = (*PebbleStore)(nil)` checks

## Instructions

### 1. Read the existing interfaces

```bash
cat internal/server/interfaces.go
```

List what sub-interfaces already exist and what methods they cover.

### 2. Audit each extracted package

For each package, run:
```bash
grep -n "s\.store\." internal/audiobooks/service.go | sed 's/.*s\.store\.\([A-Za-z]*\).*/\1/' | sort -u
```

This gives the exact store methods each service uses. Map each to the appropriate sub-interface.

### 3. Add missing sub-interfaces

If a package needs a method not covered by existing sub-interfaces, add a new narrow interface
or extend an existing one in `internal/server/interfaces.go`.

Example:
```go
// AudiobookReader is the store subset needed by AudiobookService for reads.
type AudiobookReader interface {
    GetBookByID(ctx context.Context, id string) (*database.Book, error)
    GetAllBookSummaries(ctx context.Context) ([]database.BookSummary, error)
    GetBookFiles(ctx context.Context, bookID string) ([]database.BookFile, error)
}
```

### 4. Update each service struct

```go
// Before:
type Service struct {
    store database.Store
}

// After:
type Service struct {
    store AudiobookReader  // or whichever narrow interface fits
}
```

Update `NewService(store NarrowInterface)` constructor accordingly.

### 5. Add compile-time assertions

At the bottom of `internal/database/pebble_store.go`:
```go
var _ AudiobookReader = (*PebbleStore)(nil)
```

### 6. Run the sweep

Packages to update (check each exists first):
- `internal/audiobooks/` → `BookReader + BookWriter` or narrower
- `internal/scanner/` → `ScanReader` (just the methods scanner needs)
- `internal/importer/` → `ImportWriter`
- `internal/reconcile/` → `ReconcileReader`
- `internal/quarantine/` → `QuarantineWriter`

Skip packages that already use narrow interfaces.

## Test

```bash
go build ./...   # must compile
go test ./...
make ci
```

## Commit

```
refactor(arch): narrow Store deps to ISP sub-interfaces in extracted packages (4.12)
```

## PR title

`refactor(arch): ISP narrow store interfaces — 4.12`

## After merging

Mark `- [ ] **4.12**` as `- [x]` in `TODO.md`.
