<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-19-shutdown-escape-hatch.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7afb30fd-c2ee-4994-8704-edf9ff35a64a -->
<!-- last-edited: 2026-07-03 -->

# TASK-19 — Shutdown escape-hatch + Registry.Shutdown goroutine-tracking fix (SYS-1 / BUG-2)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus · **Wave:** 1 · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-19-shutdown-escape-hatch" -b agent/cr-19-shutdown-escape-hatch origin/main
cd "$REPO/.worktrees/cr-19-shutdown-escape-hatch"
git rebase origin/main
```

## Goal

Close two related shutdown races, same class as `PEBBLE-CLOSED-SHUTDOWN-RACE`
(fixed 2026-07-02, see `internal/operations/registry/shutdown_race_test.go`
and `TODO.md` "Open Bugs" entry for that ID — read both before touching
anything):

- **SYS-1** — `Server.Start`'s three one-time startup jobs
  (`stripMovementAtoms`, `remuxMalformedM4BFiles`, `transcodeMalformedM4BFiles`)
  are **not** context-aware. On first boot after upgrade they can run for
  minutes over a large library. Shutdown's `bgWG.Wait()` has only a 30s grace
  period (`server_lifecycle.go`, see grep below) before it logs a warning and
  "proceeds with shutdown anyway" — closing the embedding store and, via
  `cmd/root.go`'s deferred `closeStore`, the main PebbleStore, while those
  goroutines may still be mid-walk touching the store.
- **BUG-2** — `Registry.Shutdown` has two goroutines it does NOT reliably wait
  for: (a) if the shutdown ctx passed to `Shutdown` expires during the drain
  poll, remaining ops are marked interrupted and `Shutdown` proceeds to cancel
  its internal context and return, while the abandoned op's `safeRun`
  goroutine (spawned in `worker.go`'s `executeRun`) may still be running
  plugin code that writes to the store; (b) the final `goroutineWG.Wait()` in
  `Shutdown` has its own hard 2s timeout and "proceeds" regardless. The
  `notifyDepCompletion`/`notifyDepFailed` goroutines ARE enrolled in
  `goroutineWG` (that was the actual `PEBBLE-CLOSED-SHUTDOWN-RACE` fix) — but
  the per-op `safeRun` goroutine is not, so nothing durably waits for it once
  the ctx-bounded drain poll gives up.

This is subtle concurrency work. **Before changing anything**, read
`shutdown_race_test.go` end to end and understand all the mechanisms already
in play: inline `Stop`s in `server_lifecycle.go`, `container.Stop`, the named
`s.bgWG` (bounded WaitGroup with names, in `internal/server`), and the
registry's internal `goroutineWG` (plain `sync.WaitGroup`, gated by
`r.notifyStopped` under `r.mu` to prevent Add-after-Wait races). Do not
introduce a fifth ad hoc tracking mechanism — reuse these four.

## Background (verify before editing — line numbers below were correct as of
2026-07-03 but WILL drift; re-run the greps first)

### SYS-1 anchors

```bash
grep -n "stripMovementAtoms\|remuxMalformedM4BFiles\|transcodeMalformedM4BFiles\|bgWG\.Wait\|proceeding\|bgCtx" internal/server/server_lifecycle.go
```

Confirm the shape: three `s.bgWG.Add("...")` / `go func(){ defer s.bgWG.Done(...); s.xxx() }()`
blocks around the area the comments already flag ("NOTE: ... does not check
bgCtx"), and the 30s `select { case <-bgDone: ...; case <-time.After(30 *
time.Second): slog.Warn("Background goroutines did not stop within 30s —
proceeding with shutdown anyway", ...) }` later in the same function, well
before `embeddingStore.Close()`.

None of the three job functions take a `context.Context` today:

```bash
grep -n "func (s \*Server) stripMovementAtoms\|func (s \*Server) remuxMalformedM4BFiles\|func (s \*Server) transcodeMalformedM4BFiles" internal/server/*.go
grep -n "func (r \*Remuxer) RemuxMalformedFiles\|func (t \*Transcoder) TranscodeMalformedFiles" internal/remux/*.go
```

Compare against the existing ctx-aware convention already used by
`backfillAcoustIDs` (same file group, already correct — copy this pattern,
do not invent a new one):

```bash
grep -n "func (s \*Server) backfillAcoustIDs" -A 25 internal/server/acoustid_backfill.go
```

It takes `ctx context.Context`, and inside its per-book loop does:

```go
select {
case <-ctx.Done():
    return
default:
}
```

`stripMovementAtoms` and `RemuxMalformedFiles`/`TranscodeMalformedFiles` all
iterate via `filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr
error) error { ... })`. The ctx check goes inside that closure, per-file,
returning `fs.SkipAll` (Go 1.20+) to stop the walk early and cleanly when
canceled — do NOT return a bare error from inside the callback if it would be
swallowed by an existing `_ = filepath.WalkDir(...)` (verify with grep how the
return value is currently handled at each site; today it's discarded with
`_ =`, so `fs.SkipAll` is safe and idiomatic here).

### BUG-2 anchors

```bash
grep -n "func (r \*Registry) Shutdown" -A 95 internal/operations/registry/registry.go
grep -n "goroutineWG\|notifyStopped\|shuttingDown" internal/operations/registry/registry.go
grep -n "done := make(chan error, 1)\|r.safeRun(runCtx" -A 3 internal/operations/registry/worker.go
grep -n "func (r \*Registry) executeRun\|func (r \*Registry) startWorker" -A 5 internal/operations/registry/worker.go
```

Confirm: `executeRun` is called **synchronously** from inside `startWorker`'s
loop body (`case qr := <-r.nextRun: abandoned := r.executeRun(ctx, qr)`), and
`startWorker` itself is already enrolled in `goroutineWG` via
`r.goroutineWG.Add(1)` / `go func(slot int){ defer r.goroutineWG.Done(); ...
}(i)` before it starts looping. That means `executeRun`'s own `goroutineWG.Add(1)`
for the `safeRun` goroutine (see Step 2 below) happens while its parent
worker goroutine is still alive and has not called its own `Done()` — so the
counter cannot be observed at zero between this `Add` and any concurrent
`Wait()`, and no extra `notifyStopped`-style gate is needed for this specific
enrollment (unlike `notifyDepCompletion`/`notifyDepFailed`, which are spawned
from contexts that are NOT guaranteed to still hold an outstanding `Add`).
Verify this call chain yourself with the grep above before relying on this
reasoning — this is exactly the kind of assumption that a `-race` repro test
will falsify if wrong.

## Step-by-step

### Part A — SYS-1: make the three startup jobs ctx-aware

1. `internal/server/movement_atom_cleanup.go`:
   - Change `func (s *Server) stripMovementAtoms()` to
     `func (s *Server) stripMovementAtoms(ctx context.Context)`.
   - Inside the `filepath.WalkDir` callback, before the extension check, add:
     ```go
     select {
     case <-ctx.Done():
         return fs.SkipAll
     default:
     }
     ```
   - Update the call site in `server_lifecycle.go` (`s.stripMovementAtoms()`
     → `s.stripMovementAtoms(s.bgCtx)`).
2. `internal/remux/remux.go` and `internal/remux/transcode.go`:
   - Change `func (r *Remuxer) RemuxMalformedFiles()` to
     `func (r *Remuxer) RemuxMalformedFiles(ctx context.Context)`, same for
     `Transcoder.TranscodeMalformedFiles`.
   - Add the identical `select { case <-ctx.Done(): return fs.SkipAll;
     default: }` guard inside each `filepath.WalkDir` callback.
   - Add `"context"` to each file's imports.
3. `internal/server/malformed_m4b_wrappers.go`:
   - Update `remuxMalformedM4BFiles()` / `transcodeMalformedM4BFiles()`
     wrappers to accept and forward `ctx context.Context` to the
     `remux`/`Transcoder` calls. Add `"context"` to imports.
   - Update the call sites in `server_lifecycle.go` to pass `s.bgCtx`.
4. Do **not** change the `s.bgWG.Add("...")`/`Done("...")` tracking around
   these three jobs — it's correct and already named per-job; ctx-awareness
   is additive, shortening the worst case, not replacing the existing guard.
5. Leave the 30s `bgWG.Wait()` timeout and its warning log in place as
   defense-in-depth (do not remove it or turn it into a hard error) — the
   consultancy recommendation's primary fix is making the jobs finish inside
   the window, not raising the window.

### Part B — BUG-2: enroll the per-op run goroutine in `goroutineWG`

6. In `internal/operations/registry/worker.go`, inside `executeRun`, find the
   in-process run block:
   ```go
   done := make(chan error, 1)
   go func() {
       done <- r.safeRun(runCtx, def, qr.params, reporter)
   }()
   ```
   Wrap it so the goroutine is enrolled in `goroutineWG` for its full
   lifetime, mirroring `notifyDepCompletion`'s comment style:
   ```go
   done := make(chan error, 1)
   r.goroutineWG.Add(1)
   go func() {
       defer r.goroutineWG.Done()
       done <- r.safeRun(runCtx, def, qr.params, reporter)
   }()
   ```
   Add a comment explaining why no `notifyStopped`-style gate is required
   here (see Background) — this call happens synchronously inside
   `executeRun`, itself called from the already-`goroutineWG`-enrolled
   `startWorker` goroutine, so the counter is guaranteed non-zero across the
   `Add`.
7. This also covers the "abandoned during shutdown" path (`worker.go` ~line
   276-281, the `go func() { <-done; r.releaseRunHandle(...); ... }()`
   monitor) for free: `goroutineWG.Done()` fires when `r.safeRun` actually
   returns and sends to `done`, regardless of which goroutine drains that
   channel value.
8. Do not touch `Registry.Shutdown`'s existing 2s `goroutineWG.Wait()`
   timeout or its "proceeding" warning log — leave the escape hatch itself
   as a logged, deliberate decision; the fix is making the wait actually
   cover this goroutine, not eliminating the timeout.

### Part C — regression test proving the BUG-2 gap is now closed

9. Add a new test, `internal/operations/registry/registry_pebble_race_test.go`,
   following the exact pattern of `shutdown_race_test.go` (real `PebbleStore`,
   not a mock — the whole point of that prior fix was that mocks didn't catch
   this class of bug). Design:
   - Register an op definition whose `Run` blocks past the test's shutdown
     ctx deadline (e.g. sleeps on a channel closed after a short delay, or
     ignores `ctx.Done()` briefly to simulate a slow/misbehaving plugin) and
     then performs a real store write (e.g. `store.UpdateOperationV2Status`
     or another store call) after that delay.
   - Enqueue the op, let it start running, then call `Shutdown` with a ctx
     that expires almost immediately (forcing the SYS-1/BUG-2 "ctx expired
     during drain poll" branch at `registry.go`'s `case <-ctx.Done():` inside
     the outer `select`).
   - Immediately after `Shutdown` returns, call `store.Close()` (matching
     what `cmd/root.go`'s deferred `closeStore` does in production).
   - Run under `-race` and assert no panic — before this task's fix, this
     should reproduce the "pebble: closed" panic (or a `-race` data race);
     after the fix in Part B, it should not, because `goroutineWG.Wait()`
     inside `Shutdown` — even bounded by its own 2s timeout — now has enough
     of a chance to observe the enrolled run goroutine, and more importantly
     the run goroutine's write happens-before `Shutdown`'s `goroutineWG.Done()`
     is only reachable after the write completes.
   - If the repro does NOT panic even before your fix (e.g. because the test
     op's `Run` finishes fast enough in practice), tighten the timing
     (larger sleep in `Run`, shorter shutdown ctx) until it reliably
     reproduces pre-fix and passes post-fix. Do not commit a test that can't
     demonstrate the bug.
10. Bump the file header (version bump + `last-edited`) on every file you
    touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/server/... -run 'MovementAtom|Remux|Transcode' -count=1
go test ./internal/remux/... -count=1
go test ./internal/operations/registry/... -race -count=1 -timeout 5m
go vet ./internal/server/... ./internal/remux/... ./internal/operations/registry/...
```

If `go test ./internal/operations/registry/... -race` runs long locally,
scope to the new test first (`-run TestRegistryPebbleRace` or whatever name
you choose) before running the full `-race` package.

## Acceptance criteria

- [ ] `stripMovementAtoms`, `RemuxMalformedFiles`, and
      `TranscodeMalformedFiles` all accept and honor a `context.Context`,
      stopping their `filepath.WalkDir` early via `fs.SkipAll` when the
      context is done.
- [ ] All three call sites in `server_lifecycle.go` pass `s.bgCtx`.
- [ ] The existing 30s `bgWG.Wait()` timeout and its warning log are
      unchanged — this is defense-in-depth, not removed.
- [ ] The per-op `safeRun` goroutine in `executeRun` (`worker.go`) is
      enrolled in `r.goroutineWG` for its full lifetime.
- [ ] A new real-Pebble `-race` regression test (`registry_pebble_race_test.go`
      or equivalent name) demonstrates the pre-fix panic/race and passes
      clean post-fix.
- [ ] `go build ./...`, the targeted `go test` commands above, and `go vet`
      on the three touched packages are all green.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(server,registry): close SYS-1/BUG-2 shutdown escape hatches (consultancy-roadmap)

Three one-time startup jobs (stripMovementAtoms, remux/transcode malformed
M4Bs) ran without checking bgCtx, so the 30s shutdown grace period could
expire and proceed to close the store while they were still mid-walk
(SYS-1). Separately, Registry.Shutdown's per-op safeRun goroutine was never
enrolled in goroutineWG — only the notifyDepCompletion/notifyDepFailed
goroutines were, from the prior PEBBLE-CLOSED-SHUTDOWN-RACE fix — so a
ctx-timeout during the drain poll could let Shutdown return while a plugin
goroutine still wrote to the store (BUG-2). Both are the same race class:
make the jobs ctx-aware and enroll the goroutine in the wait group that
actually guards store.Close().

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-19-shutdown-escape-hatch
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, check whether either half is already fixed:

```bash
grep -n "func (s \*Server) stripMovementAtoms(ctx" internal/server/movement_atom_cleanup.go
grep -n "func (r \*Remuxer) RemuxMalformedFiles(ctx\|func (t \*Transcoder) TranscodeMalformedFiles(ctx" internal/remux/*.go
grep -n "r.goroutineWG.Add(1)" -B 2 -A 4 internal/operations/registry/worker.go
```

If the three job functions already take `ctx context.Context` AND
`worker.go`'s `safeRun` launch already wraps with `r.goroutineWG.Add(1)` /
`defer r.goroutineWG.Done()`, Part A and Part B are both done — verify the
regression test in Part C exists and is green, and if so this task is
complete with no further changes needed. If only one half is already fixed,
implement the other half only (do not touch the already-fixed one beyond
re-verifying it still passes `-race`).

Rollback = revert the commit. The three job functions regain their
no-context signatures (reintroducing the SYS-1 exposure), and the `safeRun`
goroutine reverts to being un-enrolled (reintroducing the BUG-2 gap) — no
other invariants are affected, since `s.bgWG`, `container.Stop`, and the
registry's `notifyStopped` gating are all left untouched by this change.
