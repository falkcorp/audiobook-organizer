<!-- file: docs/agent-tasks/todo-completion/database/TASK-022-finish-killing-database-store-narrow-the-remaini.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0f3bb6ed-c3f7-4ec4-8549-3ef324e4eaf9 -->
<!-- last-edited: 2026-08-21 -->

# TASK-022 — Finish killing database.Store — narrow the remaining references per the existing 3-bucket plan (TODO.md L1227)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · database subagent · **Why:** The only remaining bucket that is NOT explicitly 'by design' or blocked is the 8-reference Server.Store() chain (internal/plugins/maintenance/deps.go ×3, internal/server/server_maintenance_deps.go ×2, plus callers) — and the TODO explicitly warns that deps.go forwards into missing_file_repair.go/missing_file_audit.go, a hands-off prod lane the owner said not to touch without asking first, making this a judgment-heavy narrowing task, not mechanical search-and-replace. · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 1227 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Finish killing `database.Store` — 18 references " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-022-finish-killing-database-store-narrow-the-remaini" -b agent/database-022-finish-killing-database-store-narrow-the-remaini origin/main
cd "$REPO/.worktrees/database-022-finish-killing-database-store-narrow-the-remaini"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Narrow the remaining reachable-and-narrowable database.Store references (the Server.Store() chain: internal/plugins/maintenance/deps.go and internal/server/server_maintenance_deps.go plus their callers) to the minimal interface each caller actually needs, per this repo's established narrowing pattern (see the CLAUDE.md worked example removing the OrganizeStore/database.Store coupling entirely rather than shrinking the interface) — while explicitly NOT touching missing_file_repair.go/missing_file_audit.go without asking first, and leaving the 7 by-design composition-root references and 3 test-helper references alone (both already justified per the TODO).

## Background (verify before editing)

- The 7 'left by design' references (server.go's store field/Store()/NewServer/nil-store error text, indexed_store.go's embedded database.Store/StoreUnwrapper/Unwrap()) are explicitly NOT this task's job — they go away only when PebbleStore itself is split (item L1194, PARKED) making database.Store unreachable, not by narrowing them now.
- The 3 test-helper references (internal/testutil/integration.go, internal/database/dbtest/invariants.go ×2) already have a verified-genuine rationale per the TODO ('integration tests poke at any domain a scenario needs') — leave alone.
- This repo's CLAUDE.md worked example (2026-08-18) is directly on point: the correct fix for a callback/parameter threading a wide store interface for the sake of 1-2 methods is usually to REMOVE the parameter entirely (the implementor closes over its own store), not merely narrow the interface — apply that same judgment here rather than mechanically shrinking database.Store to a smaller-but-still-too-wide interface.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  ls docs/plans/2026-08-18-decouple-database-layer.md   # file exists — the decoupling plan doc exists
  grep -rl 'database\.Store\b' internal | grep -v '^internal/database/' | grep -v _test | wc -l   # 82 (illustrating why a flat grep is not the right instrument here — re-derive via the plan doc's method) — a naive grep overcounts wildly vs. the TODO's precise 18
  ```

### Reuse — don't invent

- Use `the 3-bucket classification already done in the TODO text/plan doc (composition root / test helpers / Server.Store() chain)` in `docs/plans/2026-08-18-decouple-database-layer.md` (verify: `grep -n 'phase' docs/plans/2026-08-18-decouple-database-layer.md`) — do NOT write a parallel helper.

## Step-by-step

1. Re-derive the precise reference set using the plan doc's own method (read docs/plans/2026-08-18-decouple-database-layer.md for how it counted — likely an AST-based tool under tools/cmd/, not a flat grep) rather than trusting either the TODO's stated 18 or this scout's flat 82-file grep.
2. For the Server.Store() chain specifically: read internal/plugins/maintenance/deps.go's 3 references and internal/server/server_maintenance_deps.go's 2 references plus their call sites, and determine for each whether the CLAUDE.md pattern applies (implementor already has its own store reference and the wide param can be deleted) or a genuinely narrower interface is needed.
3. Do NOT touch any code path that deps.go forwards into missing_file_repair.go or missing_file_audit.go without first asking the owner — these run against prod and are explicitly hands-off per the TODO.
4. Execute the narrowing/removal for whatever remains after step 3's exclusion, updating Server.Store()'s own signature only if ALL its callers have been narrowed (partial narrowing leaves Server.Store() itself unchanged).

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_022.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A reference inside missing_file_repair.go/missing_file_audit.go's call chain must be left untouched per the standing hands-off instruction, even if narrowing it would be mechanically easy — ask first, per CLAUDE.md.

## Tests

- Existing tests for the touched files must continue to pass; no new test is implied unless step 2 reveals an interface split, in which case add a narrow interface-conformance test (`var _ NarrowInterface = (*database.PebbleStore)(nil)`) matching this codebase's existing convention.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go build ./... && go vet ./...` succeeds after the change.
- [ ] The re-derived reference count (step 1) drops from its current value, with the reduction fully accounted for in the PR description (which buckets closed, which remain and why).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_022.md`.

## Commit message

```
refactor(database): Finish killing database.Store — narrow the remaining referen (TODO L1227)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

Depends conceptually on L1194 (PebbleStore split, PARKED) only for the 7 by-design composition-root references, which are explicitly NOT part of this task's scope — this task is just the Server.Store()-chain bucket. Re-verify the precise count via the plan doc's real methodology before starting; do not trust a flat grep (illustrated above returning 82 files vs. the claimed 18 references).
