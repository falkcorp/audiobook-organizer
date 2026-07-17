# Plan: Progress Reporting — Real Numbers + Start/End Steps

## Goal

Replace every `UpdateProgress(pct, 100, …)` scaling pattern with real
`(current, total)` counts, and adopt a uniform **start + N + end** step model so
no operation ever shows `0/0` (which the UI renders as a broken bar).

## Background

`sdk.Reporter.UpdateProgress(current, total int, message string)` stores raw
counts; the UI derives `%`. Today most plugins scale work to `(pct, 100)`
which:

1. Discards the real numerator/denominator (the user can't tell if the op is
   chewing through 10 items or 300K).
2. Breaks when `total == 0` — current code paths often pass `(0, 100)` then
   skip straight to `(100, 100)` with no visible progress.
3. Inflates the percentage early (e.g. `pct*0.8`) so the bar lies.

## Step Model

Every op gets at least these steps:

```
step 0           = "starting / loading"            (current=0, total=N+2)
step 1..N        = each unit of real work          (current=i+1, total=N+2)
step N+1         = "finalizing / writing results"  (current=N+1, total=N+2)
step N+2         = "done"                          (current=N+2, total=N+2)
```

So when `N == 0` we still get `(0,2) → (1,2) → (2,2)` — never `0/0`.

Helper to centralize this:

```go
// internal/plugins/sdk/progress.go  (new)
type Progress struct {
    r        sdk.Reporter
    total    int   // N+2
    cur      int
}

func NewProgress(r sdk.Reporter, n int) *Progress { ... }   // total = n+2
func (p *Progress) Start(msg string)               { ... }   // (0, total)
func (p *Progress) Step(msg string)                { ... }   // cur++, with pct in msg
func (p *Progress) StepN(i, n int, msg string)     { ... }   // jump (start+i, total)
func (p *Progress) Finalize(msg string)            { ... }   // (total-1, total)
func (p *Progress) Done(msg string)                { ... }   // (total, total)
```

Message format (kept consistent across all ops):

```
"<verb> <i>/<N> (<extra>) (<pct>%)"
e.g. "Clearing fingerprints 1088/308857 (cleared=1088) (0.35%)"
```

`pct` formatted `%.2f` when `N >= 100`, `%.0f` otherwise.

## Files To Change (27)

| Area | Files |
|---|---|
| acoustid | `reset_all.go` (partial done), `backfill.go` |
| deluge | `centralization.go`, `path_update.go`, `protected_paths.go` |
| dedup | `full_scan.go`, `embed_scan.go`, `embed_async.go`, `llm_review.go`, `split_book_scan.go`, `book_signature_scan.go` |
| maintenance | `metadata.go`, `orphan_book_files.go`, `optimize.go`, `cleanup.go`, `author.go`, `dedup_ops.go` |
| server | `metadata_handlers.go`, `ai_handlers.go`, `diagnostics_ops.go`, `duplicates_ops.go`, `duplicates_handlers.go`, `scheduler_maintenance_window_op.go` |
| scheduler | `extra_ops.go` |
| dedup pkg | `series_dedup.go`, `book_dedup.go` |
| reconcile | `reconcile.go` |

81 call sites total.

## Order Of Operations

1. **Add helper** `internal/plugins/sdk/progress.go` + unit tests covering
   `n=0`, `n=1`, large-N, and `pct` formatting.
2. **Pilot** on `acoustid/reset_all.go` (already partly done) + `backfill.go`,
   refactor to use the helper, verify on prod-shape data.
3. **Sweep** remaining 25 files via `/parallel-sweep` — one worktree/PR per
   plugin package (acoustid / deluge / dedup / maintenance / server /
   scheduler / dedup-pkg / reconcile = 8 waves).
4. **Frontend audit**: check `web/src/**` op-progress components handle
   `total != 100` correctly (they should — they compute `current/total*100`
   themselves — but verify with one snapshot test).

## Test Strategy

- `progress_test.go` — covers helper math + zero-item case.
- Each touched plugin keeps its existing tests; add one progress assertion
  that `total > 0` always and final call satisfies `current == total`.
- `make ci` per PR.
- Manual smoke: trigger acoustid.reset-all, observe bar shows real
  `1088/308857 (0.35%)` not `1/100`.

## Rollback

Each wave is a self-contained PR; revert individually if one op breaks. The
helper is additive — leaving it in place after revert is harmless.

## Non-Goals

- Not changing the `UpdateProgress` SDK signature.
- Not changing how the frontend renders progress.
- Not unifying log lines (separate concern, tracked under UOS-tagging).
