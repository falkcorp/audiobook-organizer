<!-- file: docs/agent-tasks/logging-slog/TASK-01-writeback-isbn.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e43ad09-2ace-43cf-aaef-c8ff4512065a -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — Wire logging.Info(ctx) into ISBN enrichment (SLOG-W13a)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sl-writeback-isbn" -b agent/sl-writeback-isbn origin/main
cd "$REPO/.worktrees/sl-writeback-isbn"
git rebase origin/main
```

## Goal
Replace the 2 raw `slog.Info(...)` calls inside `internal/metafetch/isbn.go`'s `EnrichBookISBN` with `logging.Info(ctx, ...)`, so ISBN/ASIN enrichment log lines carry the `opID` of the enclosing `itunes`-style maintenance op (`runIsbnEnrichment` → `EnrichMissingISBNs(ctx,...)` → `EnrichBookISBN`). This requires adding a `ctx context.Context` parameter to `EnrichBookISBN` and threading it through its one in-op caller plus its one out-of-op (background-goroutine) caller.

**IMPORTANT — verified 2026-07-01, do NOT touch `runBulkWriteBack`:** the workstream's original evidence said `runBulkWriteBack` (in `internal/server/metadata_ops.go`) still uses raw slog. That is stale. Run `grep -n 'slog\.' internal/server/metadata_ops.go` yourself — you will see only `slog.LevelInfo` / `slog.LevelWarn` / `slog.LevelError` / `slog.LevelDebug` constants (around lines 293–304), used to pick a log *level* for a generic `Log(level, msg)` helper. That is correct, existing code — it is NOT a raw `slog.Info()`/`slog.Warn()` call, and `runBulkWriteBack` itself already logs exclusively via `progress.Log(...)`. If you find yourself editing `metadata_ops.go`, stop — that is out of scope for this task.

## Background (verify before editing)
Re-run these greps yourself before editing — line numbers drift:

```bash
grep -n "slog\." internal/metafetch/isbn.go
# expect 2 hits inside EnrichBookISBN:
#   slog.Info("ISBN enrichment found for ()", "value", isbn, "value", title, "name", src.Name())
#   slog.Info("ASIN enrichment found for", "value", asin, "value", title)

grep -n "func (s \*ISBNService) EnrichBookISBN\|func (s \*ISBNService) EnrichMissingISBNs" internal/metafetch/isbn.go

grep -n "EnrichBookISBN(" -r --include="*.go" .
# expect callers in:
#   internal/metafetch/isbn.go (inside EnrichMissingISBNs — HAS ctx already)
#   internal/metafetch/service_fetch.go (inside queueISBNEnrichment goroutine — NO ctx available)
#   internal/server/isbn_enrichment_test.go (6 test call sites)
#   internal/metafetch/service_mock_test.go (2 test call sites)
```

Key facts:
- `internal/metafetch/isbn.go`'s `EnrichBookISBN(bookID string) (bool, error)` currently has **no** `ctx` parameter.
- Its only production caller inside `EnrichMissingISBNs(ctx context.Context, ...)` (same file) **already has** `ctx` in scope — that call site already uses `logging.Warn(ctx, ...)` for the error path (this was fixed in an earlier wave, commit 7f5c28f1). You just need to thread the same `ctx` one hop further into `EnrichBookISBN`.
- Its other caller, `queueISBNEnrichment` in `internal/metafetch/service_fetch.go`, is a **detached background goroutine with no ctx of its own** (`go func(bid string) { ... }(id)`). Per the workstream ground rule, code outside an op-context flow can stay raw slog — do NOT try to invent a context for it. Just pass `context.Background()` at that one call site so the code compiles; leave `queueISBNEnrichment`'s own `slog.Warn`/`slog.Info` calls (lines ~33–36 of `service_fetch.go`) untouched — they are out of scope for this task.

## Step-by-step
1. Open `internal/metafetch/isbn.go`. Change the signature:
   ```go
   func (s *ISBNService) EnrichBookISBN(bookID string) (bool, error) {
   ```
   to:
   ```go
   func (s *ISBNService) EnrichBookISBN(ctx context.Context, bookID string) (bool, error) {
   ```
2. In the same function, replace:
   ```go
   slog.Info("ISBN enrichment found for ()", "value", isbn, "value", title, "name", src.Name())
   ```
   with:
   ```go
   logging.Info(ctx, "ISBN enrichment found", "isbn", isbn, "title", title, "source", src.Name())
   ```
   and replace:
   ```go
   slog.Info("ASIN enrichment found for", "value", asin, "value", title)
   ```
   with:
   ```go
   logging.Info(ctx, "ASIN enrichment found", "asin", asin, "title", title)
   ```
   (Cleaning up the duplicate-key `"value"` attrs and the garbled message text is intentional — the old raw calls had a bug where `"value"` was reused as the attr key twice. Use the clearer attr names above.)
3. Confirm `log/slog` has no other uses in this file (`grep -n "slog\." internal/metafetch/isbn.go` should now return nothing), then remove `"log/slog"` from the import block. Keep `"github.com/falkcorp/audiobook-organizer/internal/logging"` (it's already imported for the existing `logging.Warn` call).
4. In the same file's `EnrichMissingISBNs`, update the call site:
   ```go
   found, err := s.EnrichBookISBN(books[i].ID)
   ```
   to:
   ```go
   found, err := s.EnrichBookISBN(ctx, books[i].ID)
   ```
5. Open `internal/metafetch/service_fetch.go`. In `queueISBNEnrichment`, update:
   ```go
   found, err := mfs.isbnEnrichment.EnrichBookISBN(bid)
   ```
   to:
   ```go
   found, err := mfs.isbnEnrichment.EnrichBookISBN(context.Background(), bid)
   ```
   Add `"context"` to that file's import block if it is not already imported (check with `grep -n '"context"' internal/metafetch/service_fetch.go` first — it may already be there for other reasons).
6. Fix the test call sites. In `internal/server/isbn_enrichment_test.go`, there are 6 occurrences of `svc.EnrichBookISBN("book-1")` (or similar single-arg calls). Update each to `svc.EnrichBookISBN(context.Background(), "book-1")` (or whatever the literal argument is at that call site — do not change the string literal, only add the leading `context.Background(),` argument). Add `"context"` to the import block of that test file if missing.
7. In `internal/metafetch/service_mock_test.go`, there are 2 occurrences (`svc.EnrichBookISBN("nonexistent")` and `svc.EnrichBookISBN("b1")`). Update both the same way: prepend `context.Background(),`. Add `"context"` to that file's imports if missing.
8. Bump the file-header `version` and `last-edited` fields on every file you touched: `internal/metafetch/isbn.go`, `internal/metafetch/service_fetch.go`, `internal/server/isbn_enrichment_test.go`, `internal/metafetch/service_mock_test.go`. Bump the patch version (e.g. `1.4.1` → `1.4.2`) and set `last-edited` to today's date.

## How to test
```bash
cd "$REPO/.worktrees/sl-writeback-isbn"
go build ./internal/metafetch/... ./internal/server/...
go test ./internal/metafetch/... ./internal/server/... -run 'ISBN|Isbn' -v
go vet ./internal/metafetch/... ./internal/server/...
```
All of the above must pass with no compile errors and no test failures. If `go vet` complains about an unused `"log/slog"` import in `isbn.go`, you missed step 3.

## Acceptance criteria
- [ ] `internal/metafetch/isbn.go`'s `EnrichBookISBN` takes `ctx context.Context` as its first parameter and uses `logging.Info(ctx, ...)` for both former `slog.Info` call sites; `log/slog` import removed from that file.
- [ ] `EnrichMissingISBNs` passes its own `ctx` into `EnrichBookISBN`.
- [ ] `queueISBNEnrichment` in `service_fetch.go` passes `context.Background()` into `EnrichBookISBN` (its own raw `slog.Warn`/`slog.Info` lines are left untouched — out of scope).
- [ ] `runBulkWriteBack` / `internal/server/metadata_ops.go` is untouched (verified out of scope).
- [ ] All 8 test call sites (6 in `isbn_enrichment_test.go`, 2 in `service_mock_test.go`) updated to pass `context.Background()`.
- [ ] `go build ./internal/metafetch/... ./internal/server/...` and `go test ./internal/metafetch/... ./internal/server/... -run 'ISBN|Isbn'` pass.
- [ ] File headers (version + last-edited) bumped on every touched file.

## Commit message
```
fix(metafetch): wire logging.Info(ctx) into ISBN/ASIN enrichment (SLOG-W13a)

Thread ctx one hop from EnrichMissingISBNs into EnrichBookISBN so the
enclosing runIsbnEnrichment op's opID is attached to ISBN/ASIN enrichment
log lines. Detached background-goroutine caller (queueISBNEnrichment)
passes context.Background() and keeps its own raw slog calls, per the
op-context-only rule for this workstream.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/sl-writeback-isbn
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "slog.Info" internal/metafetch/isbn.go` returning no hits means this task is already done — stop, do not re-edit. Rollback: `git revert <commit-sha>` on the merge commit; no data migrations or state involved, this is a pure code change.