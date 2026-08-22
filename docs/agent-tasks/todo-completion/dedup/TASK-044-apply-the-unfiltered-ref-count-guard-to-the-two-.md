<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-044-apply-the-unfiltered-ref-count-guard-to-the-two-.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7546f64-fa65-455d-b9ba-f04eaddb6b21 -->
<!-- last-edited: 2026-08-21 -->

# TASK-044 — Apply the unfiltered ref-count guard to the two remaining series deleters (internal/dedup/series_dedup.go, internal/maintenance/jobs/cleanup_series.go) (TODO.md L4288)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · dedup subagent · **Why:** same fix pattern as the already-shipped L4281 fix, but applied across two different packages with their own store interfaces (bookSoftDeleter/seriesUnlinker/seriesMerger narrow interfaces in cleanup_series.go), so it's not a pure copy-paste · **Depends on:** none · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4288 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Two more series deleters have no cache invalidat" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-044-apply-the-unfiltered-ref-count-guard-to-the-two-" -b agent/dedup-044-apply-the-unfiltered-ref-count-guard-to-the-two- origin/main
cd "$REPO/.worktrees/dedup-044-apply-the-unfiltered-ref-count-guard-to-the-two-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the filtered GetBooksBySeriesIDCore ref-count checks in DedupSeries/MergeSeries (internal/dedup/series_dedup.go) and csUnlinkAndDeleteSeries/csMergeSeriesGroup (internal/maintenance/jobs/cleanup_series.go) with the same unfiltered-count, fail-closed guard pattern already shipped for the HTTP handlers (internal/server/handlers/entities/series_refcount.go), preventing these two remaining call sites from deleting a series that still has trashed or non-primary books pointing at it.

## Background (verify before editing)

- The exact same defect that produced 6,893 dangling series references via the HTTP handlers (fixed in L4281 / #2400) still exists in these two maintenance/dedup packages, which sit outside internal/server and cannot import the entities package's seriesRefCounts helper directly — a package-appropriate equivalent (or a shared exported helper in internal/database) is needed.
- Cache invalidation is a separate, smaller concern than the TODO item implies: PebbleStore.DeleteSeries already calls DeleteSeriesFromMemDB on every delete (verified above), so the memdb-level cache is already consistent from any caller. The remaining gap is the HTTP-layer seriesCache (in-handler, request-scoped) which these non-HTTP packages have no path to and arguably don't need to invalidate directly — that part of the TODO ('no path to the server's caches') is more a design note than a blocking defect.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "GetBooksBySeriesIDCore" internal/dedup/series_dedup.go   # 4 hits: L105, L344, L492, L565 — DedupSeries/MergeSeries in series_dedup.go still use the filtered counter at 4 call sites
  grep -n "GetBooksBySeriesIDCore" internal/maintenance/jobs/cleanup_series.go   # 2 hits: L68, L160 — csUnlinkAndDeleteSeries/csMergeSeriesGroup in cleanup_series.go still use the filtered counter
  grep -n "p.DeleteSeriesFromMemDB(id)" internal/database/pebble_store_series.go   # 1 hit ~L188 — PebbleStore.DeleteSeries already notifies memdb on delete (confirming the TODO's own claim, so store-layer invalidation is already covered — only the ref-count guard is missing)
  ```

### Reuse — don't invent

- Use `seriesRefCounts helper pattern (fail-closed, unfiltered)` in `internal/server/handlers/entities/series_refcount.go` (verify: `grep -n "func seriesRefCounts" internal/server/handlers/entities/series_refcount.go`) — do NOT write a parallel helper.
- Use `database.AsSeriesBookRefStore capability accessor` in `internal/database` (verify: `grep -rn "func AsSeriesBookRefStore" internal/database/*.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add an exported helper analogous to seriesRefCounts, either promoted into internal/database (e.g. `func SeriesRefCounts(store any) (map[int]int, error)` wrapping AsSeriesBookRefStore(store).GetAllSeriesBookRefCounts()) so both internal/dedup and internal/maintenance/jobs can call it without importing internal/server/handlers/entities, OR duplicate the small helper locally in each package if a shared database-layer helper is out of scope — prefer the shared helper per this repo's Fix-It-Right depth rule (avoid a third copy of the same 9-line function).
2. In internal/dedup/series_dedup.go: replace the ref-count check before each store.DeleteSeries(...) call (near L366 in DedupSeries, L536 in MergeSeries) to consult the unfiltered counts instead of a fresh GetBooksBySeriesIDCore call per series.
3. In internal/maintenance/jobs/cleanup_series.go: same replacement in csUnlinkAndDeleteSeries (before L152's store.DeleteSeries) and csMergeSeriesGroup (before L177's store.DeleteSeries).
4. Confirm each function's narrow store interface (seriesUnlinker, seriesMerger, whatever DedupSeries/MergeSeries declare) already includes or can be extended to include the GetAllSeriesBookRefCounts capability method — check via database.AsSeriesBookRefStore's type assertion requirements.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_044.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A series with GetAllSeriesBookRefCounts returning an error (store cannot answer the unfiltered question) must fail CLOSED (refuse to delete), matching seriesRefCounts' own documented contract — not silently fall back to the filtered count.

## Tests

- internal/dedup/series_dedup_test.go: add TestDedupSeries_RefusesDeleteWhenTrashedBooksRemain and TestMergeSeries_RefusesDeleteWhenNonPrimaryBooksRemain mirroring the HTTP handler's existing coverage pattern (check series_refcount_test.go or handler_test.go if one exists for the shipped fix, for the exact assertion shape to mirror).
- internal/maintenance/jobs/cleanup_series_test.go: add equivalent tests for csUnlinkAndDeleteSeries and csMergeSeriesGroup.

Anti-over-suppression test: `TestDedupSeries_RefusesDeleteWhenTrashedBooksRemain / TestMergeSeries_RefusesDeleteWhenNonPrimaryBooksRemain (must assert the delete is REFUSED, not just that the count differs — guards against a guard that computes the right number but doesn't act on it)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/maintenance/jobs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/dedup/... ./internal/maintenance/jobs/... -run Series passes with the new tests included.
- [ ] Anti-over-suppression test: `TestDedupSeries_RefusesDeleteWhenTrashedBooksRemain / TestMergeSeries_RefusesDeleteWhenNonPrimaryBooksRemain (must assert the delete is REFUSED, not just that the count differs — guards against a guard that computes the right number but doesn't act on it)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/maintenance/jobs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_044.md`.

## Commit message

```
refactor(dedup): Apply the unfiltered ref-count guard to the two remaining se (TODO L4288)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: same class of bug that stranded 13,322 books behind 6,893 dangling series on prod (2026-08-14) — this closes the remaining 2 of what appear to have been (at least) 3 total unguarded call sites.
