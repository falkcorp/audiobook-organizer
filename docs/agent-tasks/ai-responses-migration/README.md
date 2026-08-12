<!-- file: docs/agent-tasks/ai-responses-migration/README.md -->
<!-- version: 1.1.0 -->
<!-- guid: a3b5a794-03cb-41a8-b038-1d8e6e5447ab -->
<!-- last-edited: 2026-08-11 -->

# Workstream — OpenAI Responses API migration (DEFERRED/optional)

> **STATUS: DEFERRED / OPTIONAL.** Migrate `internal/ai` from the Chat
> Completions API to the `/v1/responses` API. From AI-RESP-A/B/D/E/F. The
> **whole workstream is optional and deferred** — these briefs exist so the
> work is ready to run the instant the team greenlights it. Do **not** start
> any task here without an explicit go-ahead. Do **not** touch
> `embedding_client.go` (AI-RESP-C is a permanent do-not-migrate marker — the
> `/v1/embeddings` endpoint stays on Chat/Embeddings, not Responses).

| Task | TODO id | Title | Priority | Effort | Tier | Wave |
|------|---------|-------|----------|--------|------|------|
| TASK-01 | AI-RESP-A | Migrate metadata_llm_review.go single call to /v1/responses | P3 (deferred) | M | Sonnet | 1 |
| TASK-02 | AI-RESP-B | Migrate openai_parser.go single-shot calls (6 sites) | P3 (deferred) | M | Sonnet | 2 |
| TASK-03 | AI-RESP-D | Migrate Batches API (openai_batch.go) — verify allowlist first | P3 (deferred) | M | Sonnet | 2 |
| TASK-04 | AI-RESP-E | Migrate aijobs.go multi-turn flows (add last_response_id) | P3 (deferred) | L | Sonnet | 2 |
| TASK-05 | AI-RESP-F | Delete remaining Chat Completions call sites in internal/ai/ | P3 (deferred) | S | Haiku | 3 |

## Ground rules (all tasks)

- Go only, in `internal/ai/...` and `internal/aijobs/...` (see note below on
  the corrected `aijobs` path).
- Before migrating any call shape, **verify the OpenAI Go SDK actually supports
  `/v1/responses` for that shape** (single-shot completion, batch, multi-turn
  with `PreviousResponseID`/`last_response_id`). Do not assume parity with Chat
  Completions — check the SDK's `responses` package/types first.
- Build + test the changed packages after every task:
  ```bash
  go build ./internal/ai/... ./internal/ai/aijobs/...
  go test ./internal/ai/... ./internal/ai/aijobs/...
  ```
- **Path correction:** the workstream spec that generated these briefs referred
  to `internal/aijobs/aijobs.go`. The file actually lives at
  `internal/ai/aijobs/aijobs.go`. Every task below uses the corrected path —
  if you find a stray reference to the old path anywhere, treat the corrected
  path as authoritative and verify with `find . -iname aijobs.go`.

## Collision / wave note

AI-RESP-B depends on AI-RESP-A (run A first to establish the migration pattern,
then B/D/E in parallel). `openai_parser.go` is touched by TASK-02 (AI-RESP-B)
and later swept by TASK-05 (AI-RESP-F) — sequence them, never run in parallel.
See `orchestration.md` for the full wave breakdown and dependency diagram.

See ORCHESTRATION.md (one level up) for the coordinator + worker protocol.
