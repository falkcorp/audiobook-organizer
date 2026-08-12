# Task 025: ACOUSTID-STATS-1 — GetAcoustIDStats() store method

**Depends on:** none
**Estimated effort:** S–M
**Wave:** 8 (AcoustID — task 026 depends on this)

## Goal

Add `GetAcoustIDStats()` to the store interface and implement it in PebbleDB. Counts books/files
with ≥1 fingerprint segment populated, broken down by library root.

## Context

- AcoustID segments: `acoustid_seg0` through `acoustid_seg6` stored on `book_files` records
  (fields: `AcoustIDSeg0` through `AcoustIDSeg6` in `store.go` around line 672)
- Production DB: PebbleDB (`internal/database/pebble_store.go`)
- Mock store: `internal/database/mocks/` — add the method to the mock too
- Task 026 (endpoint) and 027 (UI) depend on this

## Files to modify

- `internal/database/store.go` — add interface method + `AcoustIDStats` struct
- `internal/database/pebble_store.go` — implement
- `internal/database/mocks/MockStore.go` (or equivalent) — add mock method

## Instructions

### 1. Define the struct in `store.go`

```go
// AcoustIDStats describes fingerprint coverage across the library.
type AcoustIDStats struct {
    TotalFiles      int                      `json:"total_files"`
    WithFingerprint int                      `json:"with_fingerprint"` // ≥1 segment populated
    ByLibrary       []AcoustIDStatsByLibrary `json:"by_library"`
}

type AcoustIDStatsByLibrary struct {
    LibraryRoot     string `json:"library_root"`
    TotalFiles      int    `json:"total_files"`
    WithFingerprint int    `json:"with_fingerprint"`
}
```

### 2. Add to store interface

```go
GetAcoustIDStats(ctx context.Context) (*AcoustIDStats, error)
```

### 3. Implement in `pebble_store.go`

Iterate all `book_files` records. For each file:
- Check if `AcoustIDSeg0 != ""` (any non-empty segment = has fingerprint)
- Derive library root from file path (check how other by-library breakdowns are done —
  search for `GetBookPathPrefixes` or `book_path_prefixes` for the pattern)
- Accumulate totals

```go
func (s *PebbleStore) GetAcoustIDStats(ctx context.Context) (*AcoustIDStats, error) {
    files, err := s.GetAllBookFiles(ctx) // or iterate directly
    if err != nil { return nil, err }

    byLib := make(map[string]*AcoustIDStatsByLibrary)
    var total, withFP int

    for _, f := range files {
        total++
        hasFP := f.AcoustIDSeg0 != "" || f.AcoustIDSeg1 != "" // check all 7 segs
        if hasFP { withFP++ }

        root := libraryRootOf(f.FilePath) // derive from first 2-3 path components
        lib := byLib[root]
        if lib == nil {
            lib = &AcoustIDStatsByLibrary{LibraryRoot: root}
            byLib[root] = lib
        }
        lib.TotalFiles++
        if hasFP { lib.WithFingerprint++ }
    }

    libs := make([]AcoustIDStatsByLibrary, 0, len(byLib))
    for _, v := range byLib { libs = append(libs, *v) }
    sort.Slice(libs, func(i, j int) bool { return libs[i].LibraryRoot < libs[j].LibraryRoot })

    return &AcoustIDStats{TotalFiles: total, WithFingerprint: withFP, ByLibrary: libs}, nil
}
```

### 4. Add mock

In the mock store, add a simple stub:
```go
func (m *MockStore) GetAcoustIDStats(ctx context.Context) (*database.AcoustIDStats, error) {
    args := m.Called(ctx)
    return args.Get(0).(*database.AcoustIDStats), args.Error(1)
}
```

## Test

```bash
go test ./internal/database/... -run TestAcoustIDStats -v -count=1
make ci
```

## Commit

```
feat(acoustid): GetAcoustIDStats store method for fingerprint coverage (ACOUSTID-STATS-1)
```

## PR title

`feat(acoustid): AcoustID stats store method — ACOUSTID-STATS-1`

## After merging

Mark `- [ ] **ACOUSTID-STATS-1**` as `- [x]` in `TODO.md`.
Task 026 can start.
