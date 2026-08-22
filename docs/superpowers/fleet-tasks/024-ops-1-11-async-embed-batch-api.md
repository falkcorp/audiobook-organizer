<!-- file: docs/superpowers/fleet-tasks/024-ops-1-11-async-embed-batch-api.md -->
<!-- version: 1.0.0 -->
<!-- guid: cca53a97-b160-4667-832c-b4aa34f0347d -->
<!-- last-edited: 2026-08-22 -->

# Task 024: 1.11 — Async embed via OpenAI Batch API for nightly re-scans

**Depends on:** none
**Estimated effort:** L
**Wave:** 7 (async operations)
**Spec:** `docs/superpowers/bot-tasks/2026-05-04-async-embed-batch-api.md`

## Goal

Add an async embedding path that submits the full primary-book set as one OpenAI Batch job
(`endpoint=/v1/embeddings`), routes results via the existing universal batch poller, and
writes embeddings back. 50% cost discount vs. sync path.

## Context

Full spec: `docs/superpowers/bot-tasks/2026-05-04-async-embed-batch-api.md`

Key points:
- Existing sync path: `Engine.EmbedBatch` in `internal/dedup/engine.go` — keep it for interactive callers
- New file: `internal/ai/embedding_batch.go` — JSONL builder/submitter/parser
- Universal batch poller: find it by searching for `metadata tag` or `BatchPoller` in `internal/` — add an embeddings handler
- New route: `POST /api/v1/dedup/embed-async` — enqueues an Operation that submits the batch
- Scheduled nightly job: add opt-in cron to `internal/scheduler/` or `internal/scheduled/`
- Batch limits: 50K requests / 200MB per batch. 10K books × ~120 bytes fits in one batch.
- Custom ID: each JSONL line's `custom_id = bookID` for result join
- Content-hash check: skip books whose embedding hash hasn't changed (same as sync path)
- Resume: store batch ID in `result_data` of the Operation so restart can re-attach

## Files to create/modify

- `internal/ai/embedding_batch.go` (new) — JSONL helpers
- `internal/dedup/engine.go` — add `EmbedBooksAsync(ctx, ids []string) (batchID string, error)`
- Universal batch poller (find file) — add embeddings result handler
- `internal/server/` — add `POST /api/v1/dedup/embed-async` route
- `internal/scheduler/` or `internal/scheduled/` — add nightly-embed-refresh job (opt-in)

## Instructions

### 1. `internal/ai/embedding_batch.go`

```go
// BuildEmbeddingBatchLines builds JSONL lines for an OpenAI batch embedding request.
// Each line: {"custom_id": bookID, "method": "POST", "url": "/v1/embeddings",
//              "body": {"model": "text-embedding-3-small", "input": text}}
func BuildEmbeddingBatchLines(books []BookEmbeddingInput) ([]byte, error) { ... }

// ParseEmbeddingBatchResults parses the output JSONL from a completed batch.
// Returns map[bookID][]float32.
func ParseEmbeddingBatchResults(outputFile io.Reader) (map[string][]float32, error) { ... }
```

### 2. `Engine.EmbedBooksAsync`

```go
func (e *Engine) EmbedBooksAsync(ctx context.Context, ids []string) (string, error) {
    // Build JSONL for books with changed content hash
    // Upload via openai client Files API
    // Create batch job with endpoint=/v1/embeddings
    // Return batch ID
}
```

### 3. Universal batch poller — add embeddings handler

Search for `BatchPoller` or `metadata_tag` in `internal/`. When a batch with tag
`type=embeddings` completes:
1. Download output file
2. Parse via `ParseEmbeddingBatchResults`
3. For each `bookID → vector`: upsert into embeddings store + chromem

### 4. Route and Operation

```go
// POST /api/v1/dedup/embed-async
// Enqueues "embed-books-async" operation, returns op_id immediately
// Worker: calls EmbedBooksAsync, stores batchID in result_data, marks op as pending-batch
// On poller completion: marks op complete
```

### 5. Nightly scheduler entry (opt-in)

Add to scheduler: `nightly-embed-refresh` at 2AM, disabled by default (config flag
`EmbedBatchNightly bool`). When enabled, triggers the async route.

## Test

```bash
go test ./internal/ai/... -run TestEmbedding -v -count=1
go test ./internal/dedup/... -v -count=1
make ci
```

Manual: trigger async embed, check Operation appears, wait for batch (or mock), verify embeddings updated.

## Commit

```
feat(dedup): async batch-API embedding path for nightly re-scans (1.11)
```

## PR title

`feat(dedup): OpenAI Batch API async embedding — 1.11`

## After merging

Mark `- [ ] **1.11**` as `- [x]` in `TODO.md`.
