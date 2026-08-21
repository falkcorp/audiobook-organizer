<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-181-find-the-createbook-path-s-that-copy-a-dangling-.md -->
<!-- version: 1.0.0 -->
<!-- guid: ae143ceb-4fc9-44ae-80d0-af8c86821ec5 -->
<!-- last-edited: 2026-08-21 -->

# TASK-181 — Find the CreateBook path(s) that copy a dangling SeriesID onto newly-created per-chapter book rows -- narrowed to 4 remaining candidate call sites after ruling out assignAuthorAndSeries (TODO.md L4222)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Why:** genuine root-cause investigation with no confirmed anchor yet -- requires either production activity-log forensics beyond what a repo scout can do, or tracing the remaining 2-3 candidate CreateBook call sites' SeriesID provenance in detail · **Depends on:** none · **Wave:** 2 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 4222 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Identify what produced the 2026-08-11 burst of p" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-181-find-the-createbook-path-s-that-copy-a-dangling-" -b agent/dedup-181-find-the-createbook-path-s-that-copy-a-dangling- origin/main
cd "$REPO/.worktrees/dedup-181-find-the-createbook-path-s-that-copy-a-dangling-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Trace which code path created the 5,367 per-chapter books on 2026-08-11 and identify where it copies an existing (already-dangling) book's SeriesID onto the new row rather than resolving a fresh series ID. The candidate search space is now narrowed to exactly 2 files (scanner.go and importer.go) and, within importer.go, 2 of its 3 CreateBook sites (L427 and L1797 -- L973's path is now cleared via assignAuthorAndSeries/ensureSeriesID).

## Background (verify before editing)

- The RESOLVED section of this TODO item already proved the mechanism is propagation (copying an existing dangling reference), not minting -- so the remaining work is purely 'which CreateBook call site copies SeriesID from a sibling/parent row'.
- This rescope additionally ruled out importer.go's assignAuthorAndSeries (called before the L973 CreateBook): its ensureSeriesID helper (L2012) always calls GetSeriesByName then CreateSeries on a fresh lookup, never copies a stale ID from another book struct -- confirmed by reading its full body.
- importer.go:427's 'brand new' book-creation path (inside the main import loop) does not visibly set SeriesID in the L400-430 snippet inspected -- meaning if it carries a SeriesID at CreateBook time, that value was set EARLIER in the function (not yet traced in this pass) or is nil.
- importer.go:1797's linkAsVersion function creates `importBook` as a version-link sibling of an `existing` book, copying `importBook.VersionGroupID = existing.VersionGroupID` explicitly (verified) -- but does NOT visibly copy `existing.SeriesID` onto `importBook` in the L1780-1800 snippet inspected; whether importBook already carries a SeriesID value from its own construction (before reaching linkAsVersion) was not traced in this pass and is the most promising unexplored lead.
- scanner.go:2864's `dst.SeriesID = scanned.SeriesID` (noted by the prior scout, not re-verified in this pass) assigns FROM an already-resolved scanned.SeriesID onto an EXISTING dst row during a re-scan merge, not book creation from a copied ID -- still an open question whether 'scanned' can itself carry a stale-but-plausible SeriesID under some merge-during-warmup race.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "SeriesID" internal/dedup/split_book_detector.go   # hits at L31 (comment), L85 (struct field), L137, L372, L378 -- all reads, no CreateBook in this file — split_book_detector.go only reads SeriesID for grouping comparison, never writes/creates a book with it
  grep -n "SeriesID" internal/plugins/maintenance/regroup_shattered_ai.go   # 0 hits — regroup_shattered_ai.go has zero SeriesID references
  grep -n "CreateBook" internal/plugins/maintenance/itunes_regroup.go   # 1 hit ~L261: store.CreateBook(&database.Book{Title: a.Title}) — itunes_regroup.go's single CreateBook call sets only Title
  grep -n "func resolveSeriesID" internal/scanner/scanner.go   # 1 hit ~L2657 (drifted from L2487) — resolveSeriesID always creates-if-absent, ruling out a plain scan as the source (line number has drifted from the prior scout's L2487 citation)
  grep -n "func (imp \*Importer) ensureSeriesID" internal/itunes/service/importer.go   # 1 hit ~L2012, body confirms GetSeriesByName then CreateSeries, no copy from a sibling book — NEW: importer.go's assignAuthorAndSeries (called before the CreateBook at L973) resolves SeriesID fresh via GetSeriesByName-then-CreateSeries, never copies a stale ID -- ruling out that CreateBook call site's series-assignment path
  grep -rn "CreateBook(" internal/scanner internal/dedup internal/plugins internal/itunes --include='*.go' | grep -v _test.go   # exactly 5 hits: scanner.go:2417, itunes_regroup.go:261 (already ruled out), importer.go:427/973/1797 — exhaustive list of every remaining CreateBook call site in the relevant packages
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Trace importBook's full construction path before it reaches linkAsVersion (importer.go:1797) -- find where importBook is built (likely buildBookFromAlbumGroup or a sibling function) and confirm whether its SeriesID field is ever set to a value copied from an existing/sibling book rather than freshly resolved.
2. Trace book's full construction path before the L427 CreateBook call (inside the main import loop, 'brand new' book path) back to wherever SeriesID might be set earlier in that function.
3. Cross-reference book 01KZSX7TW6BZXJX11F8K6Y0DSZ and its 2026-08-11 22:36 siblings against the operations list for that exact minute (the TODO already ruled out 41 named ops for the whole day -- narrow to the single minute) to see if ANY op (even one not on the ruled-out list, e.g. a scan itself, which the TODO notes was suspiciously ABSENT from the operations list that day) was running.
4. Re-verify scanner.go:2864's `dst.SeriesID = scanned.SeriesID` in full context -- confirm whether it's reachable during book CREATION (not just merge-onto-existing) under any code path, since line numbers have drifted since the prior scout's citation.
5. Once the actual copy site is found, fix it to either call the fresh-resolve equivalent of ensureSeriesID/resolveSeriesID for the new row or explicitly drop SeriesID when the source reference is unverified, per the standing rule 'never mint or propagate a dangling reference'.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_181.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book intentionally created with SeriesID=nil (no series) must not be mistaken for the bug -- only a non-nil SeriesID pointing at a NONEXISTENT series row is the defect.

## Tests

- Once the source is found: a regression test in that package asserting a newly-created book never carries a SeriesID that doesn't resolve to an existing series row.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/scanner/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The 6,893-count phantom-series-reference query shows 0 NEW phantom IDs appearing after the fix, re-verified against a fresh scan/regroup run in a sandbox.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/scanner/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_181.md`.

## Commit message

```
refactor(dedup): Find the CreateBook path(s) that copy a dangling SeriesID on (TODO L4222)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this is squarely on the prod-data path (series integrity, book creation). This rescope narrowed the search space from 'every CreateBook call site across 4 packages' to exactly 2 files and, within importer.go, 2 of 3 remaining CreateBook sites (L427, L1797), by fully clearing the L973 path's series-assignment logic (assignAuthorAndSeries/ensureSeriesID) as legitimate. Given the remaining effort/uncertainty, this is still best run as a dedicated investigation session rather than a blind haiku/sonnet code-edit task -- but the search space is now meaningfully smaller than before this rescope.
