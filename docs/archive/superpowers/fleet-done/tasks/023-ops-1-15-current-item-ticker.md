# Task 023: 1.15 — Reporter.SetCurrentItem live ticker

**Depends on:** none (UOS-03 and UOS-06 are already merged)
**Estimated effort:** M
**Wave:** 7 (async operations)
**Spec:** `docs/superpowers/bot-tasks/2026-05-05-uos-amendment-current-item.md`

## Goal

Add `Reporter.SetCurrentItem(label string)` — a Sonarr/Radarr-style "currently working on:
*Foundation by Asimov*" ticker displayed under the progress bar in the bell icon and Activity
page. Ephemeral (in-memory only, no DB write per update).

## Context

Full spec: `docs/superpowers/bot-tasks/2026-05-05-uos-amendment-current-item.md`

Key points:
- `Reporter` interface: `pkg/plugin/sdk/reporter.go` — add `SetCurrentItem(label string)`
- Registry: `internal/operations/registry/` — the per-run handle gains `currentItem string` (mutex-protected)
- SSE event type: `op.current_item` with payload `{op_id, label}`
- Timeline endpoint: `GET /api/v1/operations/timeline` — include `current_item` in each running op
- Frontend: `useOperationsStore.ts` handles `op.current_item` SSE, `OperationV2` gains optional `current_item`
- Bell icon / op card: shows current item below progress message
- NOT persisted to DB — purely ephemeral registry memory

## Files to modify

- `pkg/plugin/sdk/reporter.go` — add `SetCurrentItem(label string)` to interface
- `pkg/plugin/sdk/reporter_impl.go` (or wherever the concrete reporter is) — implement it
- `internal/operations/registry/` (run handle) — add `currentItem string` + mutex, fan out SSE
- `internal/operations/registry/` (timeline handler) — include `current_item` in response
- `web/src/stores/useOperationsStore.ts` — handle `op.current_item` SSE event
- `web/src/components/operations/OperationsIndicator.tsx` (bell) — display `current_item`
- `web/src/pages/Activity.tsx` or op card component — display `current_item`

## Instructions

### 1. Extend Reporter interface

```go
// pkg/plugin/sdk/reporter.go
type Reporter interface {
    // ... existing methods ...

    // SetCurrentItem sets a human-readable label for what the op is currently
    // processing. Ephemeral: held in registry memory, fanned via SSE, included
    // in timeline snapshots. NOT written to operation_logs or operations_v2.
    // Pass "" to clear.
    SetCurrentItem(label string)
}
```

### 2. Implement in registry run handle

The concrete reporter struct (find it by searching for `type reporterImpl` or similar) gains:
```go
currentItem     string
currentItemMu   sync.Mutex
sseHub          *EventHub  // to fan out SSE
opID            string
```

`SetCurrentItem` implementation:
```go
func (r *reporterImpl) SetCurrentItem(label string) {
    r.currentItemMu.Lock()
    r.currentItem = label
    r.currentItemMu.Unlock()
    r.sseHub.Emit("op.current_item", map[string]string{"op_id": r.opID, "label": label})
}
```

### 3. Include in timeline endpoint

In the timeline response for running ops, add `"current_item": handle.GetCurrentItem()`.

### 4. Frontend

In `useOperationsStore.ts`:
```ts
case "op.current_item":
    if (state.operations[data.op_id]) {
        state.operations[data.op_id].current_item = data.label;
    }
    break;
```

In the bell / op card, below the progress bar:
```tsx
{op.current_item && (
    <Typography variant="caption" noWrap title={op.current_item} sx={{ display: 'block' }}>
        {op.current_item}
    </Typography>
)}
```

### 5. Wire one high-value call site as proof

In `internal/dedup/engine.go` or `internal/server/acoustid_backfill.go`, call
`reporter.SetCurrentItem(book.Title)` inside the per-item loop.

## Test

```bash
go test ./pkg/plugin/sdk/... -v -count=1
go test ./internal/operations/... -v -count=1
npm test
make ci
```

Manual: run an AcoustID scan, watch the bell icon — verify book titles appear as current item.

## Commit

```
feat(ops): Reporter.SetCurrentItem live current-item ticker (1.15)
```

## PR title

`feat(ops): live current-item ticker for operations — 1.15`

## After merging

Mark `- [ ] **1.15**` as `- [x]` in `TODO.md`.
