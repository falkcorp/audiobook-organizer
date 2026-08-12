# Task 026: ACOUSTID-STATS-2 — GET /maintenance/acoustid-stats handler

**Depends on:** task 025 (GetAcoustIDStats store method)
**Estimated effort:** S
**Wave:** 8 (AcoustID)

## Goal

Add `GET /api/v1/maintenance/acoustid-stats` HTTP handler that returns the AcoustID fingerprint
coverage stats from the store.

## Context

- Store method `GetAcoustIDStats` added in task 025
- Pattern: look at `handleGetMaintenanceSHADedupStats` or similar in `internal/server/`
  for the exact maintenance handler pattern to follow
- Route registration: `internal/server/server_lifecycle.go` in the maintenance route group

## Files to modify

- `internal/server/acoustid_handlers.go` (or `maintenance_handlers.go`) — add handler
- `internal/server/server_lifecycle.go` — register route

## Instructions

### 1. Add handler

```go
// handleGetAcoustIDStats returns fingerprint coverage stats.
// GET /api/v1/maintenance/acoustid-stats
func (s *Server) handleGetAcoustIDStats(c *gin.Context) {
    stats, err := s.store.GetAcoustIDStats(c.Request.Context())
    if err != nil {
        httputil.RespondWithError(c, http.StatusInternalServerError, err)
        return
    }
    httputil.RespondWithOK(c, stats)
}
```

### 2. Register route

In `server_lifecycle.go`, find the maintenance route group (search for `/maintenance/`).
Add:
```go
maintenance.GET("/acoustid-stats", s.handleGetAcoustIDStats)
```

### 3. Add test

In `internal/server/acoustid_handlers_test.go` (or existing test file):
```go
func TestHandleGetAcoustIDStats(t *testing.T) {
    // Set up mock store returning a non-nil AcoustIDStats
    // GET /api/v1/maintenance/acoustid-stats
    // Assert 200, correct JSON structure
}
```

## Test

```bash
go test ./internal/server/... -run TestAcoustIDStats -v -count=1
make ci
```

## Commit

```
feat(acoustid): GET /maintenance/acoustid-stats endpoint (ACOUSTID-STATS-2)
```

## PR title

`feat(acoustid): acoustid-stats endpoint — ACOUSTID-STATS-2`

## After merging

Mark `- [ ] **ACOUSTID-STATS-2**` as `- [x]` in `TODO.md`.
Task 027 can start.
