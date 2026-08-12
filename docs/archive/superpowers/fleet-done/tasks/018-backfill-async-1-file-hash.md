# Task 018: BACKFILL-ASYNC-1 — Async file-hash backfill operation

**Depends on:** none
**Estimated effort:** M
**Wave:** 7 (async operations)

## Goal

Convert `handleBackfillFileHashes` from a synchronous HTTP handler into a proper async
queued operation that appears in Active Operations, is resumable after restart, and emits
an activity summary on completion.

## Context

- Current handler: `internal/server/` — search for `handleBackfillFileHashes`
- Operation system: `internal/operations/registry/` — look at how other ops are defined
  (e.g., `dedup-acoustid-scan`, `backfill-book-files` from old ASYNC tasks)
- Plugin SDK: `pkg/plugin/sdk/` — operations are registered as `OperationDef`
- Checkpoint pattern: `operations.SaveParams` → `SaveCheckpoint` per batch → `LoadCheckpoint` on resume
- Activity: use `activity.EmitInfo` for summary on completion
- PebbleDB: `GetAllBookFiles` or similar to iterate files missing hashes

## Files to modify

- `internal/operations/state.go` — add `BackfillFileHashesParams{DryRun bool}`
- `internal/server/` (find the maintenance plugin registration) — register new op
- `internal/server/` (find `handleBackfillFileHashes`) — convert to enqueue-and-return
- Add worker function `runBackfillFileHashes(ctx, reporter, params)` near the handler

## Instructions

### 1. Define params struct in `internal/operations/state.go`

```go
type BackfillFileHashesParams struct {
    DryRun bool `json:"dry_run"`
}
```

### 2. Register the operation

Find where maintenance operations are registered (search for `"backfill-"` or `maintenance`
in `internal/server/` or the maintenance plugin). Add:

```go
{
    ID:          "backfill-file-hashes",
    Name:        "Backfill Missing File Hashes",
    Description: "Computes SHA-256 hashes for book files that are missing them.",
    Run:         s.runBackfillFileHashes,
    ResumePolicy: operations.ResumePolicyCheckpoint,
}
```

### 3. Implement `runBackfillFileHashes`

```go
func (s *Server) runBackfillFileHashes(ctx context.Context, reporter operations.Reporter, rawParams json.RawMessage) error {
    var params operations.BackfillFileHashesParams
    if err := json.Unmarshal(rawParams, &params); err != nil {
        return err
    }

    files, err := s.store.GetBookFilesWithoutHash(ctx)
    if err != nil { return err }

    reporter.SetTotal(len(files))
    for i, f := range files {
        if ctx.Err() != nil { return ctx.Err() }

        hash, err := computeFileHash(f.FilePath)
        if err != nil {
            reporter.Log(fmt.Sprintf("skip %s: %v", f.FilePath, err))
            continue
        }
        if !params.DryRun {
            _ = s.store.UpdateBookFileHash(ctx, f.ID, hash)
        }
        reporter.SetProgress(i + 1)
        if i%50 == 0 {
            reporter.SaveCheckpoint(map[string]any{"index": i})
        }
    }
    reporter.EmitInfo(fmt.Sprintf("Backfilled hashes for %d files", len(files)))
    return nil
}
```

### 4. Convert handler to enqueue-and-return

The old `handleBackfillFileHashes` should now just call:
```go
opID, err := s.queue.Enqueue(ctx, "backfill-file-hashes", operations.BackfillFileHashesParams{DryRun: dryRun})
```
And return `{"op_id": opID}`.

### 5. Add store methods if missing

Check for `GetBookFilesWithoutHash` and `UpdateBookFileHash` in `store.go`. Add to interface
and implement in `pebble_store.go` if missing.

## Test

```bash
go test ./internal/server/... -run TestBackfill -v -count=1
make ci
```

Manual: trigger backfill, verify it appears in Activity page, verify restart resumes.

## Commit

```
feat(ops): async backfill-file-hashes operation with checkpoint resume (BACKFILL-ASYNC-1)
```

## PR title

`feat(ops): async file-hash backfill — BACKFILL-ASYNC-1`

## After merging

Mark `- [ ] **BACKFILL-ASYNC-1**` as `- [x]` in `TODO.md`.
