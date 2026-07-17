<!-- file: docs/agent-tasks/perf-cleanup/TASK-01-resetall-runitems.md -->
<!-- version: 1.0.0 -->
<!-- guid: 75844a0f-6241-4d5c-abbb-4feec252c692 -->
<!-- last-edited: 2026-07-01 -->

# TASK-01 — Migrate acoustid/reset_all.go to registry.RunItems (ARCH-4b)

**Priority:** P3 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-resetall-runitems" -b agent/pc-resetall-runitems origin/main
cd "$REPO/.worktrees/pc-resetall-runitems"
git rebase origin/main
```

## Goal

`internal/plugins/acoustid/reset_all.go` is the last of the three ARCH-4b sites
still using raw for-loops instead of `registry.RunItems`
(`internal/plugins/acoustid/lsh_backfill.go` and `internal/plugins/acoustid/backfill.go`
were already migrated). Migrate the **per-row fallback loop** (the slow path
used when the store is not a `*database.PebbleStore`) and the **dedup-candidate
deletion loop** to `registry.RunItems`, preserving all existing behavior:
progress reporting, cancellation via `ctx.Done()`, and the counters
(`cleared`, `deleted`) that feed the final summary message.

The **PebbleStore fast path** (`pebble.ClearAllAcoustIDFingerprints`, a bulk
batched call with its own internal progress callback) is a single function
call, not a loop — leave it exactly as-is. Do not try to force it through
`RunItems`; it already has better performance characteristics than a
per-item driver could give it.

## Background (verify before editing)

Re-run these — line numbers drift:
```bash
grep -n "for \|RunItems\|ClearAllAcoustIDFingerprints\|ListCandidates\|DeleteCandidate" internal/plugins/acoustid/reset_all.go
grep -n "func RunItems\[" internal/operations/registry/run_items.go
grep -n "registry.RunItems(ctx, reporter" internal/plugins/acoustid/backfill.go
```

At authoring time:
- `internal/plugins/acoustid/reset_all.go:95` — `for i := range files {` — the
  per-row fallback loop over `[]database.BookFile` that clears
  `AcoustIDSeg0..6` via `p.store.UpdateBookFile(f.ID, &updated)`. This is the
  loop to migrate. It currently increments `cleared` and steps progress every
  500 items.
- `internal/plugins/acoustid/reset_all.go:134` (outer) and `:147` (inner) — a
  `for { ... for _, c := range cands { ... } }` pagination loop that pages
  through `p.embeddingStore.ListCandidates(database.CandidateFilter{Layer:
  "acoustid", ...})` and calls `p.embeddingStore.DeleteCandidate(c.ID)` for
  each, incrementing `deleted`. This is a *second, independent* loop with a
  different item type (`database.DedupCandidate`, not `database.BookFile`).
  `RunItems` is generic over a single item type per call — do **not** try to
  merge these two loops into one `RunItems` call. Flatten each loop
  separately: (a) load the full `files` slice up front (already true — it's
  loaded via `p.store.GetAllBookFiles()`) and drive it with one `RunItems`
  call; (b) for the candidate-deletion loop, first drain all pages into a
  single `[]database.DedupCandidate` slice (the pagination logic already
  exists — reuse it to build the slice instead of deleting inline), then
  drive deletion with a second `RunItems` call.
- `internal/operations/registry/run_items.go:81` —
  `func RunItems[T any](ctx context.Context, r Reporter, items []T, fn func(ctx
  context.Context, item T) error, opts ...RunItemsOptions) error`. Concurrency
  defaults to sequential (0/1); leave it sequential here — the original loops
  were sequential and PebbleStore access is not proven safe for concurrent
  writes from this call site.
- Reference migration example: `internal/plugins/acoustid/backfill.go:118-150`
  — shows the `registry.RunItems(ctx, reporter, slice, func(ctx, item) error
  {...}, registry.RunItemsOptions{...})` pattern, including a `Label` func and
  progress offset. `sdk.Reporter` is a type alias for `registry.Reporter`
  (`pkg/plugin/sdk/reporter.go:12`), so `reporter` in `runResetAll` can be
  passed directly to `RunItems` with no adapter.

## Step-by-step

1. Re-run the grep commands above to confirm anchors haven't drifted. If line
   numbers differ, adjust but keep the same functions/loops.
2. Migrate the **per-row fallback loop** (`for i := range files`):
   - Keep the existing `ctx.Done()` check semantics — `RunItems` already
     polls `ctx.Done()` between items, so you can drop the manual `select`
     inside the loop body.
   - Move the skip-if-already-clear check (`if f.AcoustIDSeg0 == "" && ...
     { continue }` → becomes `return nil` early in the item func) and the
     `UpdateBookFile` call into the `RunItems` callback. Increment `cleared`
     inside the callback (use a `*int` or closure variable exactly like
     `backfill.go`'s `fingerprinted`/`skipped`/`failed` pattern — no mutex
     needed since `RunItems` runs sequentially by default here).
   - Use `registry.RunItemsOptions{Label: func(i, t int) string { return
     fmt.Sprintf("Clearing fingerprints %d/%d (cleared=%d)", i+1, t, cleared)
     } }` to replicate the existing progress text (drop the `i%500==0`
     throttle — `RunItems` already steps progress once per item, which is
     fine given `RunItems` itself is the loop driver now).
   - On `UpdateBookFile` error: keep the existing behavior of logging a
     warning and continuing rather than failing the whole op — return `nil`
     from the item func after logging, do not return the error (matches
     current `continue`-on-error semantics).
3. Migrate the **candidate-deletion loop**: change the pagination code to
   collect all pages into one `var allCands []database.DedupCandidate` slice
   first (same `ListCandidates`/`Layer: "acoustid"`/`pageSize` pagination
   logic, but appending instead of deleting inline), then call `RunItems`
   once over `allCands` with an item func that calls
   `p.embeddingStore.DeleteCandidate(c.ID)`, increments `deleted`, and
   logs+continues (`return nil`) on a delete error exactly like today.
4. Preserve the `prog.Finalize(...)` / `prog.Done(...)` calls and the final
   summary log line exactly as they are today — only the two loop bodies
   change, not the surrounding progress/summary scaffolding.
5. Preserve `ResumePolicy: sdk.ResumeDrop` in `resetAllDef()` — this op does
   not need checkpoint/resume semantics (unchanged), so do not add
   `CheckpointFn` to either `RunItemsOptions`.
6. Add a new test file `internal/plugins/acoustid/reset_all_test.go` (none
   exists today — confirm with `find . -iname reset_all_test.go`). Cover, using
   the `MockStore`/mock embedding store already used by sibling tests in this
   package (check `internal/plugins/acoustid/backfill_test.go` for the
   fixture pattern):
   - Slow-path fallback: a `MockStore` (not `*database.PebbleStore`) with a
     few `BookFile`s that have non-empty `AcoustIDSeg*` fields — assert all
     are cleared and `cleared` count matches.
   - Candidate deletion: a mock embedding store with a couple of
     `"acoustid"`-layer candidates across 2+ pages (set a small page size via
     the filter if the mock supports it, or seed enough candidates to force
     multiple `ListCandidates` calls) — assert all are deleted.
   - Context cancellation mid-loop still returns promptly (RunItems handles
     this — just assert `runResetAll` returns a non-nil error when ctx is
     pre-cancelled).
7. Bump the file header in `internal/plugins/acoustid/reset_all.go` (version
   1.2.0 → 1.3.0, `last-edited` → today's date) and add a header to the new
   test file per `.standards/instructions/file-headers.md`.

## How to test

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/pc-resetall-runitems
go build ./...
go test ./internal/plugins/acoustid/... -run TestResetAll -v -count=1
go test ./internal/plugins/acoustid/... -count=1
go vet ./internal/plugins/acoustid/...
```

## Acceptance criteria
- [ ] Per-row fallback loop (`for i := range files`) migrated to `registry.RunItems`; `cleared` count and progress text unchanged in behavior.
- [ ] Candidate-deletion loop migrated to `registry.RunItems` (pages drained into a slice first, then `RunItems` drives deletion); `deleted` count unchanged in behavior.
- [ ] PebbleStore fast path (`pebble.ClearAllAcoustIDFingerprints`) left untouched.
- [ ] Cancellation via `ctx.Done()` still works (relies on `RunItems`'s built-in polling).
- [ ] New `reset_all_test.go` covers slow-path clearing, candidate deletion, and context cancellation.
- [ ] `go build ./...`, `go test ./internal/plugins/acoustid/...`, `go vet ./internal/plugins/acoustid/...` all green.
- [ ] File headers bumped on every changed/added file.

## Commit message
```
refactor(acoustid): migrate reset_all.go to registry.RunItems (ARCH-4b)

Flattens the per-row fingerprint-clear fallback and the dedup-candidate
deletion loop onto the shared registry.RunItems driver, matching the pattern
already used by lsh_backfill.go and backfill.go. The PebbleStore bulk-clear
fast path is unchanged.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/pc-resetall-runitems
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "registry.RunItems" internal/plugins/acoustid/reset_all.go` — if both loops already call `RunItems`, this task is done. Rollback: revert the commit; the PebbleStore fast path and op registration are untouched so revert is safe at any time.
