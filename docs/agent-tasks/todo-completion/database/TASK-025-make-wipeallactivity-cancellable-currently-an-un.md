<!-- file: docs/agent-tasks/todo-completion/database/TASK-025-make-wipeallactivity-cancellable-currently-an-un.md -->
<!-- version: 1.0.0 -->
<!-- guid: 35a0c11f-0ef3-4af0-b637-8219e2ee91d7 -->
<!-- last-edited: 2026-08-21 -->

# TASK-025 — Make WipeAllActivity cancellable (currently an uncancellable full scan reachable from a request path) (TODO.md L1970)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** Interface signature change across 5 files (4 implementations + the call site), same shape and risk profile as L1970's sibling L1957's CountByPrefix work in this same scope, but the change itself (threading a ctx through an existing loop) is mechanical once the interface is widened. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1970 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`WipeAllActivity` still does an uncancellable fu" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-025-make-wipeallactivity-cancellable-currently-an-un" -b agent/database-025-make-wipeallactivity-cancellable-currently-an-un origin/main
cd "$REPO/.worktrees/database-025-make-wipeallactivity-cancellable-currently-an-un"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a `context.Context` parameter to `WipeAllActivity` across the ActivityStorer interface and all 4 implementations, and have PebbleActivityStore's implementation check `ctx.Err()` between tier-batches (or per-batch inside the existing 500-row commit loop) the same way its already-cancellable sibling Query does, so an abandoned wipe request via handleWipe stops scanning promptly instead of running every tier to completion regardless of client disconnect.

## Background (verify before editing)

- internal/database/pebble_activity_store.go:606-630's WipeAllActivity loops over `actTiers`, calling `s.scanTierKVs(context.Background(), tier, nil, nil)` (hardcoded background context) then deleting in 500-row batches — no cancellation check anywhere in this path.
- The activity-cancellation work (referenced by this item) deliberately left Prune, WipeAllActivity, Summarize, and CompactByDay context-free 'per scope' while making Query/GetDistinctSources cancellable — this item's scope is WipeAllActivity specifically, since it is the one reachable from a live request path (handleWipe); the other three maintenance methods share the same defect shape but are noted here only as related, not in this item's scope.
- internal/server/maintenance_fixups.go:89's handleWipe is a real HTTP endpoint (`s.handleWipe`) — an abandoned client request today leaves the wipe scanning to completion server-side regardless.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'WipeAllActivity(' internal/database/activity_storer.go   # 1 hit at L28: `WipeAllActivity() (int64, error)` — interface declares WipeAllActivity with no context parameter
  grep -n 'context.Background()' internal/database/pebble_activity_store.go   # ≥1 hit inside WipeAllActivity's body (~L611) — PebbleActivityStore's implementation hardcodes context.Background() internally
  grep -rn 'func.*WipeAllActivity(' --include="*.go" internal/database/ | grep -v _test.go   # 4 hits: dual_write_activity_store.go, nuts_activity_store.go, pebble_activity_store.go, activity_store_instrumented.go — the 4 implementations to update
  grep -n 'WipeAllActivity()' internal/server/maintenance_fixups.go   # 1 hit at L420, inside handleWipe (defined L89) — reachable from an HTTP handler
  ```

### Reuse — don't invent

- Use `Query/GetDistinctSources' existing ctx-aware cancellation pattern (the sibling methods that ARE already cancellable per the activity-cancellation work)` in `internal/database/pebble_activity_store.go` (verify: `grep -n 'func (s \*PebbleActivityStore) Query' internal/database/pebble_activity_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. Change the interface at internal/database/activity_storer.go:28 from `WipeAllActivity() (int64, error)` to `WipeAllActivity(ctx context.Context) (int64, error)`.
2. Update internal/database/pebble_activity_store.go:606's implementation to accept and thread the ctx: replace `s.scanTierKVs(context.Background(), tier, nil, nil)` with `s.scanTierKVs(ctx, tier, nil, nil)`, and add a `select { case <-ctx.Done(): return total, ctx.Err(); default: }` check between tiers (and optionally inside the 500-row batch loop for finer-grained cancellation, matching whatever granularity Query already uses — check its pattern first).
3. Update internal/database/nuts_activity_store.go:414's implementation similarly.
4. Update internal/database/dual_write_activity_store.go:122-124's implementation to accept and forward ctx to both `d.nuts.WipeAllActivity(ctx)` and `d.pebble.WipeAllActivity(ctx)`.
5. Update internal/database/activity_store_instrumented.go:137's wrapper to accept and forward ctx to `i.store.WipeAllActivity(ctx)`.
6. Update the call site internal/server/maintenance_fixups.go:420 to pass the handler's request context: `svc.Store().WipeAllActivity(c.Request.Context())` or equivalent (wipeActivity's own signature already takes a ctx param per L1957's investigation — thread it through to this call too).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_025.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A cancelled wipe mid-run must return a partial, honest count (rows actually deleted so far), not silently claim success or silently claim 0 — match the honesty bar already established for the sibling total-count fix in L1957 of this scope.

## Tests

- internal/database/pebble_activity_store_test.go — extend TestPebbleActivityStore_WipeAllActivity (confirmed exists at L230) with a case: seed many rows, call WipeAllActivity with an already-cancelled context, assert it returns promptly with ctx.Err() and a partial (not necessarily zero) count rather than completing the full scan.
- internal/database/store_coverage_test.go — TestCoverage_WipeAllActivity (confirmed exists) must still pass with the new signature.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./...` succeeds with the widened signature across all 4 implementations and the call site.
- [ ] `go test ./internal/database/... ./internal/server/...` passes including the new cancellation test.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_025.md`.

## Commit message

```
refactor(database): Make WipeAllActivity cancellable (currently an uncancellable (TODO L1970)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Lower severity than the read-path cancellation work per the item's own framing ('a wipe is rare and operator-initiated, not fired on every page load') — reasonable to schedule after L1957 (same file, same handler) rather than urgently. Prune/Summarize/CompactByDay share this exact defect shape per the item but are explicitly out of this item's stated scope — flag to the coordinator as a likely follow-up sweep.
