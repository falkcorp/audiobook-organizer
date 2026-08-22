<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-112-build-the-first-aid-orchestrator-frontend-trigge.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7239745d-fe85-44ed-b362-6ada21a14512 -->
<!-- last-edited: 2026-08-21 -->

# TASK-112 — Build the First Aid orchestrator + frontend trigger button (dry-run by default, no schedule) (TODO.md L8890)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** sequencing/orchestration across a dozen-plus existing ops with a convergence (re-investigate after fixers) loop and a new frontend surface — architecturally significant even though most called ops already exist · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8890 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**\"First Aid\" — one sequenced library validate + r" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-112-build-the-first-aid-orchestrator-frontend-trigge" -b agent/missing-file-lane-112-build-the-first-aid-orchestrator-frontend-trigge origin/main
cd "$REPO/.worktrees/missing-file-lane-112-build-the-first-aid-orchestrator-frontend-trigge"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build the First Aid orchestrator per .claude/notes/2026-08-05-first-aid-architecture.md's locked design: sequence the already-built tier-1 (investigation) ops over all books, escalate flagged books to tier-2 (already-built probing ops), run tier-3 fixers, then RE-INVESTIGATE and repeat until nothing actionable remains (idempotent convergence, not a hardcoded fixed order) — plus a frontend button to trigger it, dry-run by default, with NO schedule.

## Background (verify before editing)

- Nearly every op named in the item's own roster already exists as an independent maintenance op — the orchestrator's job is SEQUENCING and CONVERGENCE, not reimplementing detection.
- The design doc already locks the tier boundaries and the convergence property — read it fully before writing any code.
- Explicitly excluded from First Aid's roster per the item: purge-deleted, tombstone-cleanup, temp-file-cleanup, cleanup-activity-log, purge-old-logs, cleanup-old-backups, trash-cleanup, archive-sweep, db-optimize, optimize, batch-poller, bulk-write-back, intro-transcribe, extract-wav-clips — server-health janitorial ops, not book-correctness ops.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln 'FirstAid\|first-aid\|first_aid' internal --include='*.go'   # 0 hits — no First Aid orchestrator code exists yet
  ls .claude/notes/2026-08-05-first-aid-architecture.md   # file exists — the locked design doc for it exists
  ls internal/plugins/maintenance/{relink_unlinked,reconcile,orphan_book_files,dedupe_book_file_rows,purge_millisecond_durations,booksig_recovery_audit,duration_reextract,integrity_check,duration_backfill,repair_junk_titles,title_repair,title_backfill,series_denumber_op,regroup_shattered_ai}.go   # 14 existing files, one per named roster op — nearly every roster op it needs to sequence already exists
  ```

### Reuse — don't invent

- Use `every tier-1/2/3 op already implemented (see verified_anchors file list)` in `internal/plugins/maintenance/` (verify: `n/a — see file list above`) — do NOT write a parallel helper.

## Step-by-step

1. Read .claude/notes/2026-08-05-first-aid-architecture.md in full before writing any code.
2. Build a sequencer that runs the tier-1 ops (relink-unlinked-books, reconcile-scan, orphan-book-files-cleanup, dedupe-book-file-rows, purge-millisecond-durations, booksig-recovery-audit) over the whole library; all six exist as files in internal/plugins/maintenance/.
3. Collect each op's flagged output and hand it to the tier-2 ops that ALREADY EXIST: probe-directory-books (probe_directory_books.go), duration-reextract (duration_reextract.go), file-integrity-check (integrity_check.go). malformed-m4b-remux does NOT exist and is explicitly OUT OF SCOPE for this task: do not build it, record it as a gap in the final report.
4. Hand tier-2's escalated verdicts to tier-3 fixers (duration-backfill, repair-junk-titles, title-repair, title-backfill, series-denumber, regroup-shattered-ai), all of which already exist.
5. After a fixer pass, RE-RUN tier-1 investigation on the affected set and loop until a pass finds nothing actionable (convergence), rather than hardcoding a fixed relink-then-regroup order. Cap iterations at a named constant maxFirstAidIterations.
6. Do NOT wire 'missing-input triggering' - that behavior is the sibling brief TASK-113 and is not yet merged. Keep the current waiting_deps parking behavior unchanged.
7. Add the frontend trigger button to the existing System > Maintenance panel in web/src/pages/System.tsx (do NOT create a new page); it calls the orchestrator with apply=false (dry-run) by default. Do NOT add a cron Schedule to the op def.
8. Exclude the janitorial ops list verbatim from the sequence.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_112.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book that OSCILLATES (a fixer's output gets re-flagged by investigation every pass) must trip a max-iteration safety cap and surface as a genuine unresolvable case, not loop forever.

## Tests

- internal/plugins/maintenance/first_aid_orchestrator_test.go: TestFirstAid_ConvergesAfterFixerPass — a fixture where tier-1 flags a book, a fixer resolves it, and the re-investigation pass no longer flags it; the orchestrator terminates rather than looping forever.
- TestFirstAid_ExcludesJanitorialOps — anti-over-suppression: asserts none of the 14 explicitly-excluded op IDs ever appear in the orchestrator's sequence.
- TestFirstAid_DryRunMakesNoWrites — default apply=false path calls zero tier-3 fixers in apply mode.

Anti-over-suppression test: `TestFirstAid_ExcludesJanitorialOps` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 && npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] g
- [ ] o
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] .
- [ ] /
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] p
- [ ] l
- [ ] u
- [ ] g
- [ ] i
- [ ] n
- [ ] s
- [ ] /
- [ ] m
- [ ] a
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] n
- [ ] a
- [ ] n
- [ ] c
- [ ] e
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] -
- [ ] r
- [ ] u
- [ ] n
- [ ]  
- [ ] F
- [ ] i
- [ ] r
- [ ] s
- [ ] t
- [ ] A
- [ ] i
- [ ] d
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ]  
- [ ] a
- [ ] l
- [ ] l
- [ ]  
- [ ] t
- [ ] h
- [ ] r
- [ ] e
- [ ] e
- [ ]  
- [ ] c
- [ ] a
- [ ] s
- [ ] e
- [ ] s
- [ ] ;
- [ ]  
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] F
- [ ] i
- [ ] r
- [ ] s
- [ ] t
- [ ] A
- [ ] i
- [ ] d
- [ ] _
- [ ] C
- [ ] o
- [ ] n
- [ ] v
- [ ] e
- [ ] r
- [ ] g
- [ ] e
- [ ] s
- [ ] A
- [ ] f
- [ ] t
- [ ] e
- [ ] r
- [ ] F
- [ ] i
- [ ] x
- [ ] e
- [ ] r
- [ ] P
- [ ] a
- [ ] s
- [ ] s
- [ ]  
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] r
- [ ] t
- [ ] s
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] o
- [ ] r
- [ ] c
- [ ] h
- [ ] e
- [ ] s
- [ ] t
- [ ] r
- [ ] a
- [ ] t
- [ ] o
- [ ] r
- [ ]  
- [ ] t
- [ ] e
- [ ] r
- [ ] m
- [ ] i
- [ ] n
- [ ] a
- [ ] t
- [ ] e
- [ ] s
- [ ]  
- [ ] w
- [ ] i
- [ ] t
- [ ] h
- [ ]  
- [ ] i
- [ ] t
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ]  
- [ ] <
- [ ] =
- [ ]  
- [ ] m
- [ ] a
- [ ] x
- [ ] F
- [ ] i
- [ ] r
- [ ] s
- [ ] t
- [ ] A
- [ ] i
- [ ] d
- [ ] I
- [ ] t
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ]  
- [ ] a
- [ ] n
- [ ] d
- [ ]  
- [ ] a
- [ ]  
- [ ] n
- [ ] o
- [ ] n
- [ ] -
- [ ] i
- [ ] n
- [ ] c
- [ ] r
- [ ] e
- [ ] a
- [ ] s
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] f
- [ ] l
- [ ] a
- [ ] g
- [ ] g
- [ ] e
- [ ] d
- [ ]  
- [ ] c
- [ ] o
- [ ] u
- [ ] n
- [ ] t
- [ ]  
- [ ] p
- [ ] e
- [ ] r
- [ ]  
- [ ] i
- [ ] t
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] ;
- [ ]  
- [ ] `
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] m
- [ ] a
- [ ] x
- [ ] F
- [ ] i
- [ ] r
- [ ] s
- [ ] t
- [ ] A
- [ ] i
- [ ] d
- [ ] I
- [ ] t
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] s
- [ ] '
- [ ]  
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] r
- [ ] n
- [ ] a
- [ ] l
- [ ] /
- [ ] p
- [ ] l
- [ ] u
- [ ] g
- [ ] i
- [ ] n
- [ ] s
- [ ] /
- [ ] m
- [ ] a
- [ ] i
- [ ] n
- [ ] t
- [ ] e
- [ ] n
- [ ] a
- [ ] n
- [ ] c
- [ ] e
- [ ] /
- [ ] f
- [ ] i
- [ ] r
- [ ] s
- [ ] t
- [ ] _
- [ ] a
- [ ] i
- [ ] d
- [ ] _
- [ ] o
- [ ] r
- [ ] c
- [ ] h
- [ ] e
- [ ] s
- [ ] t
- [ ] r
- [ ] a
- [ ] t
- [ ] o
- [ ] r
- [ ] .
- [ ] g
- [ ] o
- [ ] `
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ] .
- [ ] Anti-over-suppression test: `TestFirstAid_ExcludesJanitorialOps` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1 && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_112.md`.

## Commit message

```
feat(missing-file-lane): Build the First Aid orchestrator + frontend trigger button ( (TODO L8890)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -rln 'FirstAid\|first-aid\|first_aid' internal --include='*.go'` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner design 2026-08-05, architecture LOCKED per the referenced note — do not redesign the tier boundaries, only implement the sequencer/orchestrator around the already-built ops.
