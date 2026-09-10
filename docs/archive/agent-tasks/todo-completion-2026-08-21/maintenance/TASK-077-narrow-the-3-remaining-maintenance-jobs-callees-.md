<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-077-narrow-the-3-remaining-maintenance-jobs-callees-.md -->
<!-- version: 1.1.0 -->
<!-- guid: de83a5ec-08be-4971-a5b5-38ac5b4a578a -->
<!-- last-edited: 2026-09-02 -->

# TASK-077 — Narrow the 3 remaining maintenance-jobs callees off maintenance.JobStore (vgFixAuthorDirPath, migrateOne, ddMergeDuplicateBook) (TODO.md L5424)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — All 3 still take maintenance.JobStore: fix_version_groups.go:277, backfill_itunes_positions.go:274, dedup_books.go:329. The 5 narrowed exemplars all hit. No commits since 2026-08-21. Recommendation: keep — mechanical, and the pattern to copy is intact.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** Mechanical interface-narrowing with a clear, already-demonstrated pattern in the same file/package (5 sibling functions already done the exact same way) — low risk, but 3 separate small interfaces across 3 files. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 5424 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Store-parameter narrowing: 54 declarations remai" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-077-narrow-the-3-remaining-maintenance-jobs-callees-" -b agent/maintenance-077-narrow-the-3-remaining-maintenance-jobs-callees- origin/main
cd "$REPO/.worktrees/maintenance-077-narrow-the-3-remaining-maintenance-jobs-callees-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Define one new minimal interface per function (in each function's own file, next to the function, mirroring where bookFileLister/folderLinker/etc. are NOT defined — check whether store_slices.go is the maintenance/jobs package's convention too, or whether internal/maintenance/jobs has its own local-interface file/pattern to follow instead since these 3 live in a different package than the 5 already-done ones) exposing exactly the methods each function calls, and change each function's `store maintenance.JobStore` parameter to the new narrow type. Zero call sites change (Run() still holds a JobStore, which structurally satisfies each narrower interface).

## Background (verify before editing)

- vgFixAuthorDirPath (fix_version_groups.go:277) calls only GetBookByID, UpdateBook, DeleteBookFilesForBook on its store parameter.
- migrateOne (backfill_itunes_positions.go:274) calls only GetBookByID, ListUserPositionsForBook, GetUserBookState, SetUserPosition, SetUserBookState on its store parameter (its second parameter, bookmarkStore database.BookmarkStore, is already narrow and untouched by this task).
- ddMergeDuplicateBook (dedup_books.go:329) calls only GetExternalIDsForBook, GetBookFiles, UpsertBookFile, ReassignExternalIDs, GetBookUserTags, AddBookUserTag, GetBookByID, UpdateBook on its store parameter (its enqueuer maintenance.WriteBackEnqueuer parameter is already narrow and untouched).
- The 2026-08-16 handoff note (.claude/notes/2026-08-16-store-narrowing-handoff.md) recorded these exact 2 free functions plus migrateOne as 'unblocked' once their own callees (narrowed by PR #2503) stopped requiring the wide type — that PR appears to be the reason 5 of the 8 named siblings are already done; these 3 were evidently not reached in that pass.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func firstAudioFile' internal/plugins/maintenance/intro_transcribe.go   # 1 hit, L878, param type bookFileLister — firstAudioFile already takes a narrow interface, not database.Store/JobStore
  grep -n 'func linkProbedFolder' internal/plugins/maintenance/probe_directory_books.go   # 1 hit, L559, param type folderLinker — linkProbedFolder already takes a narrow interface
  grep -n 'func relinkOne' internal/plugins/maintenance/relink_unlinked.go   # 1 hit, L306, param type folderLinker — relinkOne already takes a narrow interface
  grep -n 'func ApplyMultidisc' internal/plugins/maintenance/regroup_apply.go   # 1 hit, L105, param type multidiscApplier — ApplyMultidisc already takes a narrow interface
  grep -n 'func (p \*Plugin) processTranscribePage' internal/plugins/maintenance/intro_transcribe.go   # 1 hit, L438, param type transcribePageStore — processTranscribePage already takes a narrow interface
  grep -n 'func vgFixAuthorDirPath' internal/maintenance/jobs/fix_version_groups.go   # 1 hit, L277 — vgFixAuthorDirPath still takes the wide maintenance.JobStore
  grep -n 'func (j \*backfillITunesPositionsJob) migrateOne' internal/maintenance/jobs/backfill_itunes_positions.go   # 1 hit, L274 — migrateOne still takes the wide maintenance.JobStore
  grep -n 'func ddMergeDuplicateBook' internal/maintenance/jobs/dedup_books.go   # 1 hit, L329 — ddMergeDuplicateBook still takes the wide maintenance.JobStore
  ```

### Reuse — don't invent

- Use `bookFileLister / folderLinker / multidiscApplier / transcribePageStore (existing narrow-interface pattern in the same package to copy the SHAPE of, one new interface per function)` in `internal/plugins/maintenance/store_slices.go` (verify: `grep -n 'type folderLinker interface' internal/plugins/maintenance/store_slices.go`) — do NOT write a parallel helper.
- Use `Pattern B guidance (narrow interface, one line per site, zero call-site changes) — do not sweep Option C (split-decision) per this repo's own comparison note` in `.claude/notes/2026-08-17-option-b-vs-c-comparison.md` (verify: `grep -n 'Option' .claude/notes/2026-08-17-option-b-vs-c-comparison.md`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/maintenance/jobs/fix_version_groups.go, add a new interface (e.g. `type authorDirPathStore interface { GetBookByID(id string) (*database.Book, error); UpdateBook(id string, book *database.Book) (*database.Book, error); DeleteBookFilesForBook(bookID string) error }` — verify each method's exact signature against database.Store's declarations via grep before typing it, since a mismatched signature fails to compile silently as 'does not implement') near vgFixAuthorDirPath, then change its `store maintenance.JobStore` parameter to `store authorDirPathStore`.
2. In internal/maintenance/jobs/backfill_itunes_positions.go, add a similar new interface for migrateOne's 5 calls (GetBookByID, ListUserPositionsForBook, GetUserBookState, SetUserPosition, SetUserBookState), change its `store maintenance.JobStore` parameter to the new type.
3. In internal/maintenance/jobs/dedup_books.go, add a similar new interface for ddMergeDuplicateBook's 8 calls (GetExternalIDsForBook, GetBookFiles, UpsertBookFile, ReassignExternalIDs, GetBookUserTags, AddBookUserTag, GetBookByID, UpdateBook), change its `store maintenance.JobStore` parameter to the new type.
4. Run `go build ./internal/maintenance/...` — since JobStore embeds all of these sub-methods already (it must, or the current code wouldn't compile), every existing call site (Run() methods passing their own `store JobStore` argument through) should compile unchanged with zero edits.
5. Run `go vet ./...` on the FULL tree (not scoped) per this repo's own standing note that scoped vet misses test-file breakage.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_077.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Do not touch Run()'s own signature in any of the 3 files — MaintenanceJob.Run's `store JobStore` parameter is interface-fixed and explicitly excluded from this sweep by the item's own text.
- migrateOne's second parameter (bookmarkStore database.BookmarkStore) is already a narrow type — leave it exactly as-is; only the first (store) parameter changes.

## Tests

- No new test cases are required for a pure interface-narrowing refactor with no behavior change — but run the EXISTING test suite for each touched file (grep for the corresponding _test.go files: fix_version_groups_test.go, backfill_itunes_positions_test.go, dedup_books_test.go) to confirm zero regressions, since a mismatched method signature in the new narrow interface would be a compile error, not a silent test failure, so 'it builds' is strong evidence here.

Anti-over-suppression test: `N/A — pure refactor, no filter/guard added.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... exits 0
- [ ] go vet ./... exits 0
- [ ] go test ./internal/maintenance/jobs/... passes with no changed test files
- [ ] grep -n 'store maintenance.JobStore' internal/maintenance/jobs/fix_version_groups.go internal/maintenance/jobs/backfill_itunes_positions.go internal/maintenance/jobs/dedup_books.go returns 0 hits for these 3 specific functions (other JobStore-typed declarations in the same files, e.g. Run() itself, are out of scope and expected to remain)
- [ ] Anti-over-suppression test: `N/A — pure refactor, no filter/guard added.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/maintenance/jobs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_077.md`.

## Commit message

```
fix(maintenance): Narrow the 3 remaining maintenance-jobs callees off maintena (TODO L5424)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is the ONLY genuinely-actionable slice of the item's 'maintenance: 8 left' claim — 5 of the 8 named declarations are already done at HEAD. See part 2 below for why the 'outside maintenance: 65' portion of this item needs re-measurement before any further work is scoped against it.
