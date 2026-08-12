# Task 021: 1.12 — Tag operation log lines with originating op ID

**Depends on:** none
**Estimated effort:** M
**Wave:** 7 (async operations)
**Spec:** `docs/superpowers/bot-tasks/2026-05-04-tag-operation-logs.md`

## Goal

Pipe `op.ID` into a context-bound logger so that log lines emitted inside operation goroutines
are tagged with the operation ID and written to `operation_logs` — making them visible in the
Activity-page log view.

## Context

Full spec: `docs/superpowers/bot-tasks/2026-05-04-tag-operation-logs.md`

Key points:
- `internal/logger/` — likely has a central logger; add `WithOperation(id string) *slog.Logger`
- `internal/operations/registry/` — when dispatching an op, install the op-scoped logger in ctx
- `operation_logs` table — already exists for storing per-op log lines
- `Reporter` interface in `pkg/plugin/sdk/reporter.go` — already has `Log(msg)` for explicit logs;
  this task routes ALL log output (including from `slog`) to both journalctl AND operation_logs

## Files to modify

- `internal/logger/logger.go` (or create it) — add `WithOperation` and context helpers
- `internal/operations/registry/` — install op logger in dispatch context
- `internal/operations/registry/` — add DB write path for op-tagged log lines
- High-traffic call sites: `internal/server/acoustid_backfill.go`, `internal/fingerprint/` —
  convert from `log.Printf` to `logger.FromContext(ctx).Warn/Info`

## Instructions

### 1. Add context logger helpers

```go
// internal/logger/logger.go
type contextKey struct{}

// WithOperation returns a logger that tags every line with op=<id> and
// writes to both slog default handler AND the operation_logs writer if set.
func WithOperation(ctx context.Context, opID string) context.Context {
    l := slog.Default().With("op", opID)
    return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the context-bound logger, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
    if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
        return l
    }
    return slog.Default()
}
```

### 2. Install op logger at dispatch time

In `internal/operations/registry/` (find the `dispatch` or `run` function), after creating
the operation's context, call:

```go
ctx = logger.WithOperation(ctx, op.ID)
```

Also wire an `slog.Handler` that, for this context, writes to `operation_logs` via a store
write. This can be a simple `MultiHandler` that fans out to (a) the default handler and
(b) a DB-writing handler.

### 3. Convert high-traffic `log.Printf` sites

```bash
grep -rn "log\.Printf\|log\.Println\|fmt\.Printf" internal/server/acoustid_backfill.go internal/fingerprint/
```

Replace with `logger.FromContext(ctx).Info("message", "key", val)`.

### 4. Add DB write handler

Implement a `slog.Handler` that writes each record to `operation_logs` using the op ID
from the `"op"` attribute. This keeps the DB write async (channel-based) to avoid blocking.

## Test

```bash
go test ./internal/logger/... -v -count=1
go test ./internal/operations/... -v -count=1
make ci
```

Manual: run an AcoustID scan, open Activity page, expand the op — verify log lines from
`acoustid_backfill.go` appear in the op's log view.

## Commit

```
feat(ops): context-bound op logger routes log lines to operation_logs (1.12)
```

## PR title

`feat(ops): tag log lines with operation ID — 1.12`

## After merging

Mark `- [ ] **1.12**` as `- [x]` in `TODO.md`.
