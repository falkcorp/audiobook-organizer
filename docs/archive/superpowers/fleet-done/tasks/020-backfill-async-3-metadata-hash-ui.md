# Task 020: BACKFILL-ASYNC-3 — MetadataHashDuplicateCard stats endpoint + UI

**Depends on:** task 019 (metadata hash backfill op) ideally merged, but backend endpoint is independent
**Estimated effort:** M
**Wave:** 7 (async operations)

## Goal

Add a `MetadataHashDuplicateCard` stats panel to the Maintenance tab, matching the style of the
existing SHA Duplicate Detection card: shows coverage %, "Backfill Missing Hashes" trigger button,
and status chip.

## Context

- `BookMetadataHashStats` struct already exists in `store.go` around line 964
- `GetBookMetadataHashStats` may or may not be implemented — check
- The SHA Duplicate Detection card in `MaintenanceTab.tsx` is the visual reference to match
- Operation to trigger: `"backfill-metadata-source-hash"` (task 019)
- Endpoint to create: `GET /maintenance/metadata-hash-stats`

## Files to modify

- `internal/database/store.go` — add `GetBookMetadataHashStats` to interface if missing
- `internal/database/pebble_store.go` — implement it
- `internal/server/` — add `GET /api/v1/maintenance/metadata-hash-stats` route + handler
- `web/src/pages/Maintenance.tsx` (or `web/src/components/maintenance/`) — add the card

## Instructions

### 1. Check existing implementation

```bash
grep -n "GetBookMetadataHashStats\|MetadataHashStats" internal/database/store.go
grep -n "GetBookMetadataHashStats" internal/database/pebble_store.go
```

If it exists, skip to step 3. If not, implement it.

### 2. Implement `GetBookMetadataHashStats`

```go
func (s *PebbleStore) GetBookMetadataHashStats(ctx context.Context) (*BookMetadataHashStats, error) {
    var total, withHash int
    // Iterate all books, count those with metadata_source_hash set vs. total
    // Return BookMetadataHashStats{Total: total, WithHash: withHash, Without: total-withHash}
}
```

### 3. Add route and handler

```go
// Route: GET /api/v1/maintenance/metadata-hash-stats
func (s *Server) handleGetMetadataHashStats(c *gin.Context) {
    stats, err := s.store.GetBookMetadataHashStats(c.Request.Context())
    if err != nil {
        httputil.RespondWithError(c, http.StatusInternalServerError, err)
        return
    }
    httputil.RespondWithOK(c, stats)
}
```

### 4. Add the UI card

In `Maintenance.tsx` (or the appropriate maintenance component), add a card component
matching the SHA Duplicate Detection card style:

```tsx
<MaintenanceCard
  title="Metadata Hash Coverage"
  description="SHA-256 content hashes derived from matched metadata source IDs."
  status={stats?.without === 0 ? "✓ All hashed" : `${stats?.without} missing hashes`}
  statusColor={stats?.without === 0 ? "success" : "warning"}
  coverage={stats ? Math.round((stats.withHash / stats.total) * 100) : null}
  actionLabel="Backfill Missing Hashes"
  onAction={() => triggerOp("backfill-metadata-source-hash", { dry_run: false })}
  loading={statsLoading}
/>
```

Auto-load stats on mount with a `useEffect`. Find the existing SHA card as the exact template.

## Test

```bash
go test ./internal/server/... -run TestMetadataHashStats -v -count=1
npm test
make ci
```

Manual: open Maintenance tab, verify card shows, trigger backfill, verify operation appears.

## Commit

```
feat(maintenance): MetadataHashDuplicateCard stats endpoint + UI (BACKFILL-ASYNC-3)
```

## PR title

`feat(maintenance): metadata hash coverage card — BACKFILL-ASYNC-3`

## After merging

Mark `- [ ] **BACKFILL-ASYNC-3**` as `- [x]` in `TODO.md`.
