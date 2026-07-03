<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-05-retire-embed-async-nightly.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9c5baaa3-5a2e-40b8-8c2a-b4fcd1a3f5ba -->
<!-- last-edited: 2026-07-03 -->

# TASK-05 — Retire/gate nightly `dedup.embed-async` (quota-dead OpenAI Batch API)

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Haiku · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-05-retire-embed-async-nightly" -b agent/cr-05-retire-embed-async-nightly origin/main
cd "$REPO/.worktrees/cr-05-retire-embed-async-nightly"
git rebase origin/main
```

## Goal

`dedup.embed-async` is nightly-scheduled (`"0 3 * * *"`) but always delegates
to the OpenAI Batch API — which is quota-dead now that the primary embedding
backend is local Ollama (bge-m3). Every night this either 429s against a dead
OpenAI account (pure ops noise) or, worse, could one day submit vectors of the
wrong dimension into the index if OpenAI quota is ever restored mid-cutover.
Fix: (1) remove the op from the default nightly cron schedule, keeping it
invocable manually for a future OpenAI-restored world, and (2) add a runtime
guard inside the op's run function that skips with a clear `slog.Warn` when
the configured embedding backend is not OpenAI, so manual invocation is also
safe.

## Background (verify before editing)

- `internal/plugins/dedup/embed_async.go` defines `embedAsyncDef()`
  (`sdk.OperationDef` for op ID `"dedup.embed-async"`) with
  `Schedule: &sched` where `sched := "0 3 * * *"` — nightly 03:00 server
  time. Its `Run` field points at `runEmbedAsync`, a one-line wrapper that
  calls `p.runEmbedScanMode(ctx, true, reporter)`.
  - `runEmbedScanMode(ctx, async=true, ...)` (in `embed_scan.go`) calls
    `p.engine.EmbedBooksAsync(ctx)`, which (in `internal/dedup/engine.go`)
    ends by calling `de.embedClient.CreateEmbeddingBatch(ctx, items)` —
    the OpenAI Batch API. Ollama's `/v1` surface has no batch endpoint.
  - The sync path (`dedup.embed-scan` without `async:true`) is what the
    local-embeddings cutover actually uses
    (`docs/status/2026-07-02-local-cutover-and-matching.md:55-57`
    explicitly avoids `embed-async` for this reason).
  - `sdk.OperationDef.Schedule` is `*string` (`internal/operations/registry/types.go:77`)
    — setting it to `nil` removes the op from the cron scheduler entirely
    while leaving the op ID registered and manually invocable (e.g. via
    `POST /api/v1/dedup/embed-async`, wired in
    `internal/server/wire_dedup_routes.go:56` →
    `internal/server/handlers/dedup/handler.go:1611` `TriggerEmbedAsync`).
  - Embedding backend config lives in `internal/config/config.go`:
    `EmbeddingConfig.BaseURL` (`embedding.base_url`, env `EMBEDDING_BASE_URL`)
    is empty by default (OpenAI) and non-empty when overridden to point at
    Ollama or another local endpoint — the same signal already used at
    `internal/server/server.go:622`
    (`config.AppConfig.Embedding.BaseURL != ""` ⇒ non-OpenAI backend
    configured). The separate `config.AppConfig.OpenAIAPIKey`
    (`internal/config/config.go` ~line 262, `openai_api_key`) is the OpenAI
    credential; empty means no OpenAI key is configured at all. Treat
    **either** condition (`BaseURL != ""` OR `OpenAIAPIKey == ""`) as
    "not an OpenAI backend" — gate on that combined check.
  - `internal/plugins/dedup/reembed_embeddings.go` already imports
    `"github.com/falkcorp/audiobook-organizer/internal/config"` from this
    same package — use the identical import path.
  - The package already uses `log/slog` elsewhere (e.g.
    `internal/plugins/dedup/check_book.go:135` calls
    `slog.Warn("dedup.check-book CheckBook error", "id", sub.ID, "err", err)`)
    — match that key=value logging style.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (p \*Plugin) embedAsyncDef\|func (p \*Plugin) runEmbedAsync\|Schedule:\s*&sched\|sched :=" internal/plugins/dedup/embed_async.go
  grep -n "Schedule \*string" internal/operations/registry/types.go
  grep -n "BaseURL\s*string\|OpenAIAPIKey\s*string" internal/config/config.go
  grep -n "p.embedAsyncDef()" internal/plugins/dedup/plugin.go
  ```
  Confirm the current body of `embedAsyncDef()` still sets
  `Schedule: &sched` with `sched := "0 3 * * *"`, and that `runEmbedAsync`
  is still the one-line delegate shown above — if either has already
  changed (e.g. `Schedule` is already `nil`), stop and see
  "Idempotency / Rollback" below instead of re-doing the change.

## Step-by-step

1. Open `internal/plugins/dedup/embed_async.go`.
2. In `embedAsyncDef()`, remove the nightly cron schedule: delete the
   `sched := "0 3 * * *"` local variable and change `Schedule: &sched` to
   `Schedule: nil` (or simply omit the `Schedule` field — the zero value of
   `*string` is `nil`). Update the comment `// nightly at 03:00 server time`
   to explain the op is now manual-only (e.g. "manual invocation only —
   nightly cron retired; OpenAI Batch API is quota-dead under the Ollama
   cutover, see docs/status/2026-07-02-local-cutover-and-matching.md").
3. Update `DisplayName` and `Description` if they reference "nightly" —
   re-verify current text with `grep -n "DisplayName\|Description" internal/plugins/dedup/embed_async.go`
   before editing, keep the "[deprecated — use embed-scan with async:true]"
   suffix intact.
4. In `runEmbedAsync`, before delegating to `runEmbedScanMode`, add a guard:
   ```go
   if config.AppConfig.Embedding.BaseURL == "" && config.AppConfig.OpenAIAPIKey == "" {
       slog.Warn("dedup.embed-async skipped: no OpenAI backend configured (Ollama/local embedding is primary)", "base_url", config.AppConfig.Embedding.BaseURL)
       return nil
   }
   ```
   Adjust the condition to match what you verify in config.go — the intent
   is: skip (do not error) whenever the effective embedding backend is not
   OpenAI, i.e. either a non-empty override `BaseURL` is set (pointing at
   Ollama/local) or there is no OpenAI API key configured at all. Add the
   `"log/slog"` and
   `"github.com/falkcorp/audiobook-organizer/internal/config"` imports to
   `embed_async.go` (re-check they aren't already imported).
5. Do NOT touch `embed_scan.go`, `engine.go`, `batch_poller.go`, or
   `batch_poller_register.go` — this task is scoped to the schedule +
   runtime guard on `embed_async.go` only. (Model-aware skip parity with
   the sync path, mentioned in OPS-3, is a separate, larger follow-up —
   do not attempt it here.)
6. Add or extend a test in `internal/plugins/dedup/*_test.go` (find the
   existing test file for this package with
   `ls internal/plugins/dedup/*_test.go | xargs grep -l "embed_async\|embedAsyncDef\|runEmbedAsync"`;
   if none exists, create `internal/plugins/dedup/embed_async_test.go`) that:
   - Asserts `embedAsyncDef().Schedule` is `nil`.
   - Asserts `runEmbedAsync` returns `nil` (no error, no batch submission)
     when `config.AppConfig.Embedding.BaseURL` is empty and
     `config.AppConfig.OpenAIAPIKey` is empty — save/restore
     `config.AppConfig` around the test to avoid polluting other tests
     (check how existing tests in this package snapshot/restore
     `config.AppConfig`, e.g. `grep -n "config.AppConfig" internal/plugins/dedup/*_test.go`).
7. Bump the file header (version + `last-edited`) on every file you touch,
   per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/plugins/dedup/... -run 'EmbedAsync|Embed' -count=1 -v
go vet ./internal/plugins/dedup/...
```

## Acceptance criteria

- [ ] `embedAsyncDef().Schedule` is `nil` — the op no longer appears on any
      cron schedule; `grep -n "Schedule" internal/plugins/dedup/embed_async.go`
      shows no `"0 3 * * *"` literal.
- [ ] The op ID `dedup.embed-async` remains registered (still returned by
      `plugin.go`'s op list) and still reachable via
      `POST /api/v1/dedup/embed-async` for manual invocation.
- [ ] `runEmbedAsync` skips (returns `nil`, logs `slog.Warn`, does not call
      `runEmbedScanMode`/`EmbedBooksAsync`) when the effective backend is not
      OpenAI (empty `OpenAIAPIKey` and no override `BaseURL`, or an explicit
      non-OpenAI `BaseURL`).
- [ ] New/updated test covers both the nil-Schedule assertion and the
      skip-guard behavior; `go test ./internal/plugins/dedup/...` is green.
- [ ] `go build ./...` and `go vet ./internal/plugins/dedup/...` are clean.
- [ ] File headers bumped on every changed file.
- [ ] `embed_scan.go`, `engine.go`, and the batch-poller files are
      untouched (confirm with `git diff --stat origin/main`).

## Commit message

```
fix(dedup): retire nightly cron + gate dedup.embed-async on OpenAI backend

dedup.embed-async was scheduled nightly at 03:00 against the OpenAI Batch
API, which is quota-dead now that Ollama/bge-m3 is the primary embedding
backend. This produced nightly 429 noise and risked ingesting wrong-dimension
vectors if OpenAI quota is ever restored mid-cutover. Remove the cron
schedule (manual invocation only) and add a runtime skip-with-warn guard so
the op is a no-op whenever the configured backend isn't OpenAI.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-05-retire-embed-async-nightly
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `embedAsyncDef().Schedule` is already `nil` (verify with
`grep -n "Schedule" internal/plugins/dedup/embed_async.go`) AND
`runEmbedAsync` already contains a backend-gating check before calling
`runEmbedScanMode`, this task is done — no further action needed. If only
the schedule removal is done but the runtime guard is missing (or vice
versa), do the missing half only. Rollback = revert the commit; the op ID,
its registration in `plugin.go`, and the `POST /dedup/embed-async` route are
untouched by this change and remain in effect either way.
