<!-- file: docs/specs/2026-06-22-work-item-contract.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1e2d3c4-b5a6-7890-fedc-ba9876543210 -->
<!-- last-edited: 2026-06-22 -->

# Work-Item Execution Contract

**Status:** Design approved — ready for implementation (PR I)  
**Connects to:** ARCH-3 (op launch helpers, ✅ done), PERF-2 (scanner batch pipeline), watchdog saga (#1562–#1567)

---

## Problem

Every fan-out operation (fingerprint N books, apply tags to M files, etc.) reimplements the same four concerns:

1. Sequential-vs-parallel fan-out choice
2. `ctx.Done()` polling between items
3. `reporter.UpdateProgress(i, total, label)` after each item
4. Watchdog heartbeat (`reporter.SetCurrentItem`) to prevent false stall detection

This creates ~40–60 lines of loop boilerplate per op that drifts across plugins. Recent watchdog incidents (#1562–#1567) were partly caused by early-return paths inside these loops that skipped the heartbeat stamp.

---

## Proposed API

### `RunItemsOptions`

```go
// RunItemsOptions configures the RunItems call.
type RunItemsOptions struct {
    // Concurrency is the number of goroutines to use.
    // 0 or 1 = sequential (default).
    // > 1 = parallel worker pool of that size.
    Concurrency int

    // PerItemTimeout, if > 0, wraps each item's context with this deadline.
    // Pairs with the per-op os.Stat timeout pattern from #1562.
    PerItemTimeout time.Duration

    // ErrMode controls what happens when an item's fn returns an error.
    // ErrModeFail (default): cancel remaining items and return the first error.
    // ErrModeCollect: run all items; return a joined error of all failures.
    ErrMode ErrMode

    // Label formats the SetCurrentItem label for item i.
    // Default: "item %d/%d" (1-indexed).
    Label func(i, total int, item any) string
}

type ErrMode int

const (
    ErrModeFail    ErrMode = iota // fail-fast: cancel on first error
    ErrModeCollect                // best-effort: collect all errors
)
```

### `Reporter.RunItems`

```go
// RunItems fans out fn over items, handling progress, heartbeat, ctx
// propagation, per-item timeout, and worker concurrency.
//
// After each item, it calls UpdateProgress(i+1, len(items), label) and
// SetCurrentItem(label). On ctx cancellation it returns ctx.Err().
//
// The generic constraint allows typed items without interface boxing in
// the hot loop.
RunItems[T any](ctx context.Context, items []T, fn func(ctx context.Context, item T) error, opts ...RunItemsOptions) error
```

### Usage (before vs after)

**Before** (acoustid/backfill.go, ~40 lines):

```go
for i := startIdx; i < total; i++ {
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }
    b := books[i]
    reporter.SetCurrentItem(fmt.Sprintf("book %d/%d: %s", i+1, total, b.Title))
    if err := processSingle(b); err != nil {
        failed++
        continue
    }
    fingerprinted++
    reporter.UpdateProgress(i+1, total, fmt.Sprintf("fingerprinted=%d", fingerprinted))
}
```

**After** (~5 lines):

```go
return reporter.RunItems(ctx, books[startIdx:], func(ctx context.Context, b Book) error {
    return processSingle(b)
}, RunItemsOptions{
    Concurrency: p.def.ItemConcurrency,
    PerItemTimeout: 30 * time.Second,
})
```

---

## Open Questions — Resolved

### 1. Relationship to UOS dependency scheduling (PR #1440)?

**Decision:** Separate layers, no conflict.

PR #1440 is op-to-op scheduling (which ops must complete before others start). `RunItems` is within-op item scheduling (how a single op fans out over its work items). They operate at different granularities and can coexist without coupling. Do not merge these layers.

### 2. Error semantics — fail-fast vs best-effort?

**Decision:** Expose both via `ErrMode`; default to `ErrModeFail`.

- **Fingerprint scans** → fail-fast is too aggressive (one bad file shouldn't abort 50K); these should use `ErrModeCollect`.
- **Schema migrations / rename chains** → fail-fast is correct to prevent partial state.
- Default to `ErrModeFail` to preserve current sequential behavior. Ops that need `ErrModeCollect` must opt in explicitly.

### 3. Per-item timeout — declarable?

**Decision:** Yes, via `RunItemsOptions.PerItemTimeout`.

The `os.Stat` timeout wrapper introduced in #1562 was an ad-hoc fix. `PerItemTimeout` generalizes it: when set, `RunItems` wraps each item's context with `context.WithTimeout(ctx, PerItemTimeout)`. The per-op `OperationDef.Timeout` remains a separate ceiling on the entire run.

### 4. Should `ItemConcurrency` live on `OperationDef` or on `RunItemsOptions`?

**Decision:** `RunItemsOptions` only (not on `OperationDef`).

Reasons:
- A single op may have multiple `RunItems` calls with different concurrency needs (e.g., fast DB phase sequential, slow I/O phase parallel).
- `OperationDef` is already large; adding per-item details increases its blast radius.
- Plugins can define a local constant (e.g., `const workerCount = 4`) and pass it in `RunItemsOptions.Concurrency`.

`OperationDef` gains **no new fields** in this PR.

---

## Implementation Plan (PR I)

**Files to change:**

| File | Change |
|------|--------|
| `internal/operations/registry/reporter.go` | Add `RunItems` to `Reporter` interface |
| `internal/operations/registry/reporter_db.go` | Implement `RunItems` on `reporterDB` |
| `internal/operations/registry/run_items.go` | New file: `RunItemsOptions`, `ErrMode`, `runItemsImpl` |
| `internal/plugins/acoustid/backfill.go` | Migrate 2 fan-out loops |
| `internal/plugins/acoustid/scan.go` | Migrate 1 fan-out loop |
| `internal/plugins/acoustid/fingerprint_rescan.go` | Migrate 1 fan-out loop |
| `internal/plugins/deluge/centralization.go` | Migrate 1 fan-out loop |
| `internal/plugins/deluge/path_update.go` | Migrate 1 fan-out loop |

**Fan-out sites to migrate (6):**

1. `acoustid/backfill.go` — books loop (~line 114)
2. `acoustid/backfill.go` — files loop (~line 128)
3. `acoustid/scan.go` — books loop
4. `acoustid/fingerprint_rescan.go` — files loop
5. `deluge/centralization.go` — books loop
6. `deluge/path_update.go` — paths loop

**Test strategy:**

- Unit test `runItemsImpl` with table-driven cases: sequential, parallel, fail-fast, collect, per-item timeout, ctx cancellation
- Existing plugin integration tests confirm no behavioral regression

**Rollback:** `RunItems` is additive — the old loops continue to compile until each is migrated. Migration can be reverted per-file without breaking the interface.
