<!-- file: judges/handoff-simplicity-scope.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d2f7a41-9c0e-4b6a-8f21-5e7c9a10b3d4 -->
<!-- last-edited: 2026-08-21 -->

# Handoff — design-judge, lens `simplicity-scope`

**Status at pause:** `judges/simplicity-scope.json` is written and valid.
`verdict: CHALLENGED`, **23 findings** (5 blocker / 13 major / 5 minor). Every finding
carries a grep or a quoted line. This is already above the ≥15 bar; what remains below is
upside, not a gap.

## What I reviewed

- `skeleton.json` in full, programmatically: all 175 `tasks[]` (id, ws, wave, tier,
  tier_label, effort, review_critical, depends_on, todo_line, exact_files, gate),
  the 55-entry `collisions` map, `workstreams[]`, `buckets` sizes
  (b2_needs_design 76 / b3_prod_run 33 / stale_done 110 / parked 13 / not_a_task 25).
- `BREAKDOWN-2026-08-21.md` — §Method, the full GLOBAL same-file collision table, §Cost.
  (NOT read line-by-line: the 18 per-WS task tables at L82–383, and buckets 2/3 at
  L408–633 — see "What remains".)
- `web/README.md` + `web/orchestration.md` in full (used as the representative WS docs).
- One brief in full: `web/TASK-173-…md` (this is where the TODO.md close-out instruction,
  the `⛔ START HERE` block and the acceptance-criteria shape come from). All other briefs
  only grepped.
- Master plan `…/todo-master-plan/docs/plans/2026-08-21-todo-completion-master-plan.md`
  §4 (Waves 0–4) and §5 (standing constraints).
- Repo at HEAD `46628240`: `internal/database/iface_assert.go`, `store.go` interface
  composition, `mock_store.go:30`, `mocks/mock_store.go`, `.mockery.yaml`,
  `internal/plugins/maintenance/plugin.go` (the `defs := []sdk.OperationDef{…}` registrar),
  `web/package.json` / `tsconfig*.json` / `package-lock.json`, `web/src/utils/apiFetch.ts`,
  `TODO.md` lines 53/296/1317/2595/4575/4960/10660/10750.

## What remains (in priority order)

1. **Route/spec registration — the advisor's checks #2 and #3, NOT yet run.** Two live
   hypotheses, each one grep from confirmation or death:
   - `internal/server/wire_abs_routes.go` is already a 2-task collision (TASK-127,
     TASK-156). TASK-153 (`POST /api/session/local`) and TASK-154 (`/local-all`) add new
     ABS endpoints but list only `abs/handler.go` + `abs/play.go`. If ABS routes register
     in `wire_abs_routes.go`, that file goes 2→4 and both tasks are mis-waved.
     Check: `grep -n 'session/local\|POST\|Group' internal/server/wire_abs_routes.go | head -40`
   - TASK-107 (`Export a playlist back to .m3u`, new handler in
     `internal/server/handlers/playlists.go`) lists no wiring file at all.
     Check: `grep -rn 'playlists' internal/server/wire_*.go`
   - `docs/api/openapi.json` is in the matrix for TASK-051/052/053 only, yet TASK-107,
     142, 153, 154, 155 all change the API surface. If any CI job diffs spec-vs-router,
     that is a 3→8 collision plus five `exact_files` additions.
     Check: `grep -rn 'openapi' .github/workflows/ Makefile`
2. **`internal/server/handlers/interfaces.go` / per-handler mocks.** TASK-115 lists it;
   other handler tasks that add store methods may need it and the narrowed
   `internal/server/handlers/*/mocks/*.go` files (mockery generates ~20 of them per
   `.mockery.yaml` L173–243). Same failure shape as blocker #3 (TASK-029).
3. **Buckets 2/3 sanity from the simplicity angle** — BREAKDOWN L408–633. Specifically:
   is anything in `b2_needs_design` (76) actually a 3-line change that was over-thought,
   and does `b3_prod_run` (33) contain code deliverables mislabeled as ops runs?
4. **Per-WS task tables L82–383** — I derived per-WS structure from `skeleton.json`
   instead, so a table-vs-skeleton drift check (like the tier-count drift I did find)
   has not been done for the WS tables.
5. **`missing-file-lane` (26 tasks, 2 waves)** — largest workstream, only spot-checked.
   Its name suggests a lane, but its contents are a grab-bag (frontend a11y, TS
   migration, playlist import, Deluge parsing). Likely a mis-bucketing finding:
   the WS label carries no scoping information, which defeats "one worktree per WS".

## In-progress hypotheses worth keeping

- **The whole wave apparatus is redundant.** Every serialization the waves buy is already
  expressible as a per-file edge. I stated this as finding #7; if resumed, the strongest
  version is a computed one: build the ready-queue schedule from `exact_files` alone and
  report its depth (I expect ≈4–5 rounds) against the wave plan's 8 global barriers over
  ~27 four-wide rounds in wave 1.
- **`tier` vs `tier_label` divergence is a generator bug, not a policy.** All 38
  mismatches are `review_critical` tasks escalated to Opus-class in the label only. The
  briefs render `tier_label`; the skeleton's own `why_tier` prose argues the `tier` value.
  Worth checking which field `/parallel-sweep` actually dispatches on.
- **The `(web)`/`(Go)` gate corruption looks like a template bug, not 10 typos** — the
  annotation is being concatenated into the command string. Probably one generator fix.
- **Unverified:** I did not confirm whether MemStore is compile-asserted against `Store`
  (only PebbleStore and MockStore are, per `iface_assert.go` and `mock_store.go:30`). If
  MemStore is asserted somewhere else, several interface-adding tasks need `memdb_*.go`
  additions too.

## How to resume

```bash
P=/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad
# tasks table I built (id | ws | wave | tier | effort | rc | deps | todo_line | title + files)
cat $P/tasks-table.txt
# findings so far
python3 -m json.tool $P/judges/simplicity-scope.json
```

Append new findings to `findings[]` (the file is a plain array — no ordering constraint).
Keep `verdict: "CHALLENGED"`. Re-validate with
`python3 -c "import json;json.load(open('$P/judges/simplicity-scope.json'))"` after each edit.

Read-only discipline: nothing in the repo or the dry-run package was modified.
