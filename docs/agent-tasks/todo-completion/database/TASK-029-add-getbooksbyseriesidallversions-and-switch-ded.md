<!-- file: docs/agent-tasks/todo-completion/database/TASK-029-add-getbooksbyseriesidallversions-and-switch-ded.md -->
<!-- version: 1.0.0 -->
<!-- guid: f0cd10db-a680-4ff4-90d9-014ecb3bbfaf -->
<!-- last-edited: 2026-08-21 -->

# TASK-029 — Add GetBooksBySeriesIDAllVersions and switch DedupSeries's merge loop to it before DeleteSeries (TODO.md L3966)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · database subagent · **Why:** New store-interface method across MemStore + PebbleStore + the Store interface + MockStore, plus updating the dedup call site -- more surface than part 1 but still a mechanical mirror of an existing, already-reviewed pattern. · **Depends on:** TASK-044 · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3966 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`dedup.series-dedup` still has no dry-run parame" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-029-add-getbooksbyseriesidallversions-and-switch-ded" -b agent/database-029-add-getbooksbyseriesidallversions-and-switch-ded origin/main
cd "$REPO/.worktrees/database-029-add-getbooksbyseriesidallversions-and-switch-ded"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add GetBooksBySeriesIDAllVersions (mirroring GetBooksByAuthorIDAllVersions's primaryOnly=false pattern) to MemStore and PebbleStore, add it to the database.Store interface and MockStore, and switch DedupSeries's merge loop to call it instead of GetBooksBySeriesIDCore before DeleteSeries, so a book on a non-primary version of a to-be-deleted series is relinked rather than orphaned.

## Background (verify before editing)

- TODO.md:3966-3969's stated mechanism: 'its merge loop reassigns books via the listing getter GetBooksBySeriesIDCore (which filters trashed and non-primary rows) before calling DeleteSeries unconditionally -- the mechanism that strands books on a deleted series ID.'
- The author-side fix for the identical class of bug already exists and is documented with a measured incident (memdb_reads.go:503-523's GetBooksByAuthorIDAllVersions doc comment: '86 books relinked... vs 84 warm... two missing links were co-author credits on non-primary versions').

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "GetBooksBySeriesIDCore" internal/dedup/series_dedup.go   # >=3 hits incl L344, L492, L565 — DedupSeries's merge loop uses the primary-filtered listing getter
  grep -n "func.*GetBooksBySeriesID" internal/database/memdb_reads.go internal/database/pebble_store.go   # only Core-typed getters, no AllVersions variant — No series equivalent of GetBooksByAuthorIDAllVersions exists yet
  grep -n "func (m \*MemStore) GetBooksByAuthorIDAllVersions" internal/database/memdb_reads.go   # 1 hit L524 — The author-side precedent this should mirror
  ```

### Reuse — don't invent

- Use `GetBooksByAuthorIDAllVersions / getBooksByAuthorID(primaryOnly=false) as the pattern to mirror for series` in `internal/database/memdb_reads.go` (verify: `grep -n "func (m \*MemStore) GetBooksByAuthorIDAllVersions" internal/database/memdb_reads.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/memdb_reads.go, near GetBooksBySeriesIDCore (line 453), add `func (m *MemStore) GetBooksBySeriesIDAllVersions(seriesID int, limit, offset int) ([]BookCore, error)` that calls the same underlying scan without the primary-version filter -- mirror the primaryOnly-bool-parameter refactor pattern used for getBooksByAuthorID/GetBooksByAuthorIDAllVersions.
2. In internal/database/pebble_store.go, near GetBooksBySeriesIDCore (line 1811), add the PebbleStore-side equivalent, mirroring GetBooksByAuthorIDWithRoleCore's 'complete set' semantics (pebble_store.go:2018).
3. Add the new method to the database.Store interface (internal/database/iface_book.go, next to the existing GetBooksBySeriesIDCore declaration around line 101-109) and to internal/database/mock_store.go's MockStore.
4. In internal/dedup/series_dedup.go, change the three call sites at lines 344, 492, 565 from store.GetBooksBySeriesIDCore(...) to store.GetBooksBySeriesIDAllVersions(...) so DeleteSeries never runs against an incomplete view of the series' books.
5. Add a conformance test in the shape of internal/database/series_getter_conformance_test.go asserting GetBooksBySeriesIDAllVersions returns a superset of GetBooksBySeriesIDCore (mirror TestAuthorGetters_WithRoleIsASupersetOfCore at internal/database/author_getter_conformance_test.go:270).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_029.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A series with zero books at all -- AllVersions returns empty slice, not error.
- A book present via both the AllVersions series getter and already counted in the primary-only set -- must not be double-relinked (mirror the author-side dedup-by-bookID logic already used in getBooksByAuthorID).

## Tests

- internal/database/series_getter_conformance_test.go: new test mirroring TestAuthorGetters_WithRoleIsASupersetOfCore for the series pair.
- internal/dedup/series_dedup_test.go: TestDedupSeries_RelinksNonPrimaryVersionBooks -- a book with a non-primary version linked only to the series-to-be-deleted must be relinked, not orphaned, after the merge.

Anti-over-suppression test: `TestDedupSeries_RelinksNonPrimaryVersionBooks -- proves the fix actually catches the orphaning case, not just that the code compiles.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/database/... ./internal/dedup/... -run 'SeriesID|DedupSeries' -v exits 0.
- [ ] Anti-over-suppression test: `TestDedupSeries_RelinksNonPrimaryVersionBooks -- proves the fix actually catches the orphaning case, not just that the code compiles.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_029.md`.

## Commit message

```
feat(database): Add GetBooksBySeriesIDAllVersions and switch DedupSeries's m (TODO L3966)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go test ./internal/database/... ./internal/dedup/... -run 'SeriesID|DedupSeries' -v exits 0.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Do this together with part 1 (dry-run param) before dedup.series-dedup is wired to any trigger -- the TODO explicitly says 'before anything wires it to a trigger', and it currently has 0 production runs so there is no urgency, only correctness debt to close before it goes live.
