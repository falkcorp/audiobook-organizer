<!-- file: docs/agent-tasks/todo-completion/server/TASK-133-fix-wipeactivity-dry-run-count-saturating-at-2.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d4de98d-daff-4d4f-a3cc-b20d747b9350 -->
<!-- last-edited: 2026-08-21 -->

# TASK-133 — Fix wipeActivity dry-run count saturating at 2 (TODO.md L1957)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Needs either a new dedicated activity-count method across multiple ActivityStorer implementations (pebble, nuts, dual-write, instrumented) or careful reuse of an existing prefix-count primitive against activity's own key scheme — small in isolation but touches an interface with 4 implementations. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 1957 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`wipeActivity` dry-run count saturates at 2.** `" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-133-fix-wipeactivity-dry-run-count-saturating-at-2" -b agent/server-133-fix-wipeactivity-dry-run-count-saturating-at-2 origin/main
cd "$REPO/.worktrees/server-133-fix-wipeactivity-dry-run-count-saturating-at-2"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Fix wipeActivity's dry-run branch (internal/server/maintenance_fixups.go:411-420) so it reports the REAL row count instead of a value that saturates at 2, by adding a dedicated count path to the activity store (mirroring the CountByPrefix pattern already used for other wipe targets in this same file) rather than reusing the paged Query call.

## Background (verify before editing)

- internal/server/maintenance_fixups.go:411-419 currently: `func wipeActivity(ctx context.Context, svc *activity.Service, dryRun bool) (int64, error) { if dryRun { entries, total, err := svc.Query(ctx, database.ActivityFilter{Limit: 1}); ...; return int64(total), nil }; return svc.Store().WipeAllActivity() }`.
- The bounded-scan change in commit 0adf6e97 made Query's `total` a lower bound: the walk stops once it has collected Offset+Limit+1 == 2 matches, so `Limit:1` always yields `total <= 2`.
- internal/server/maintenance_fixups.go:340 and :393 already call `s.CountByPrefix("bf:")` and `s.CountByPrefix("ext_id:")` for exact dry-run counts on OTHER wipe targets in the same handler — this is the established pattern to follow, not a novel design.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func wipeActivity' internal/server/maintenance_fixups.go   # 1 hit at L411, preceded by a NOTE comment starting L405 — wipeActivity's dry-run branch and its pre-existing bug-documenting comment exist exactly as described
  grep -n 'WipeAllActivity()' internal/server/maintenance_fixups.go   # 1 hit inside wipeActivity's non-dry-run branch — the real wipe (non-dry-run) uses WipeAllActivity, unaffected by this bug
  grep -n 'CountByPrefix' internal/server/maintenance_fixups.go internal/database/pebble_store.go   # hits at maintenance_fixups.go:340,393 (existing call sites) and pebble_store.go:4188 (implementation) — CountByPrefix exists as a candidate reusable counting primitive already used elsewhere in the same file
  ```

### Reuse — don't invent

- Use `CountByPrefix (existing exact-count primitive, already used twice in the same file for other wipe targets)` in `internal/database/pebble_store.go` (verify: `grep -n 'func (p \*PebbleStore) CountByPrefix' internal/database/pebble_store.go`) — do NOT write a parallel helper.

## Step-by-step

1. Determine the activity store's actual key prefix scheme (grep `internal/database/pebble_activity_store.go` for how tier keys are laid out — the item notes tiers are scanned via scanTierKVs per `actTiers`) to see whether a simple CountByPrefix-style call per tier is sufficient, or whether a dedicated CountAll method is needed since activity keys may be tiered/dated rather than a single flat prefix.
2. Add a `CountActivity(ctx context.Context) (int64, error)` (or per-tier `CountActivityTier`) method to the ActivityStorer interface (internal/database/activity_storer.go) that counts keys without unmarshalling full ActivityEntry structs — mirroring CountByPrefix's key-only counting approach.
3. Implement it in all 4 ActivityStorer implementations that currently implement WipeAllActivity: PebbleActivityStore (internal/database/pebble_activity_store.go), NutsActivityStore (internal/database/nuts_activity_store.go), DualWriteActivityStore (internal/database/dual_write_activity_store.go — likely delegates to one backend or sums both, check WipeAllActivity's own dual-write pattern at L122-124 for the model), and InstrumentedActivityStorer (internal/database/activity_store_instrumented.go — likely a thin wrap-and-forward like its other methods).
4. In internal/server/maintenance_fixups.go's wipeActivity, replace the `svc.Query(ctx, database.ActivityFilter{Limit: 1})` dry-run branch with a call to the new count method, propagating its error rather than the current silent `_ = entries` discard pattern (which is itself sloppy — the entries var isn't even used).
5. Delete or update the now-resolved NOTE comment at L405-410 that documents this exact bug as 'left alone deliberately... out of scope for the cancellation work' — it is now in scope and fixed.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_133.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the activity store is genuinely too expensive to count exactly at scale (multi-million rows), consider whether an approximate/lower-bound count with an explicit `count_is_lower_bound: true` flag (same pattern as the L2329 soft-deleted-count fix in this scope, already shipped) is preferable to a slow exact count — but per project preference (see L2329 in this scope) prefer the real count unless measured too expensive.

## Tests

- internal/database/*_test.go for each of the 4 implementations — assert CountActivity (or equivalent) returns the exact row count for >2 seeded rows (specifically choosing a count like 5 or 10 to distinguish from the old saturating-at-2 bug).
- internal/server/maintenance_fixups_test.go (or wherever wipeActivity is tested) — dry-run with >2 activity rows seeded must report the real count, not 2.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/database/... ./internal/server/...` passes.
- [ ] Manual: seed >2 activity rows, call the wipe endpoint with dry_run=true, confirm the reported total is the real count, not 2.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_133.md`.

## Commit message

```
fix(server): Fix wipeActivity dry-run count saturating at 2 (TODO L1957)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This bug was noted inline during the activity-cancellation work on branch fix/activityquery and deliberately deferred there as out of scope — this item is the deferred follow-up.
