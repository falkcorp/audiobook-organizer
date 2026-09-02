<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-200-build-the-tiered-per-file-intro-transcription-ba.md -->
<!-- version: 1.1.0 -->
<!-- guid: 889007a8-4a33-43b4-bf8a-b150cdd5c8ad -->
<!-- last-edited: 2026-09-02 -->

# TASK-200 — Build the tiered per-file intro-transcription backfill (Tiers 0/1/1b/2/3) (TODO.md L8316)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — intro_tiered_backfill.go ABSENT; 'tiered.*backfill\|TieredBackfill\|introTierBackfill' -> 0 hits; ClassifyIntro classify.go:356; RunItems already used intro_transcribe.go:217. Recommendation: keep — but re-cost it against the new Metal/capability-routed Whisper workers (#2943/#2999/#3001).

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** a 5-tier, ~284,000-file, multi-day-GPU-cost backfill with an escalation rule (1b) whose whole safety property depends on getting the escalation condition exactly right -- a wrong escalation short-circuit would silently under-probe multi-file books the same way the ORIGINAL bug (one arbitrary file per book) did · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8316 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Per-file intro transcription as the primary book" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-200-build-the-tiered-per-file-intro-transcription-ba" -b agent/missing-file-lane-200-build-the-tiered-per-file-intro-transcription-ba origin/main
cd "$REPO/.worktrees/missing-file-lane-200-build-the-tiered-per-file-intro-transcription-ba"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Implement the 5-tier backfill design already specified in the TODO, as a worker-pooled (registry.RunItems) background operation: Tier 0 -- single-file books (~32,600) migrate their existing book-level transcript to the per-file store by copy, zero GPU cost; Tier 1 -- assembled multi-file books probe only the first 3 files; Tier 1b -- escalate to transcribing the FULL file set for a book if all 3 of Tier 1's probed files carry a credits classification (this is what makes Tier 1 safe rather than a repeat of the original per-book-not-per-file blind spot -- 3-for-3 credits is the signal that more files in this book likely also open with a fresh announcement, i.e. it may be MULTIPLE books misfiled as one); Tier 2 -- bookless/shattered/review-queue members get every file transcribed (no cheap-tier shortcut, since these are already known-suspect); Tier 3 -- a lazy, indefinite full sweep so every remaining file eventually gets a transcript over time.

## Background (verify before editing)

- Storage (per-file, not per-book) and the first-file sort fix already shipped in PR #2168 -- this item does not need to touch storage schema, only the backfill logic that populates it.
- The 45.8% credit-parse rate measured across 1,476 review-queue members was explicitly caused by the OLD per-book (one arbitrary file) sampling -- Tier 1b's escalation rule exists specifically so this backfill does not silently reproduce that same under-sampling on multi-file books.
- 72.7% of books are single-file (Tier 0, cheap); 11.3% have 21+ files and hold most of the 317,054 total book_file rows -- the tiering exists because a naive full-corpus pass is measured at ~284,000 files / 12-14 days of GPU time, which the tiers are designed to avoid for the ~72.7% single-file majority.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func ClassifyIntro' internal/transcribe/classify.go   # 1 hit, L356 — the three-outcome parser this backfill depends on is already built
  grep -rln 'tiered.*backfill\|TieredBackfill\|introTierBackfill' internal/transcribe internal/plugins   # 0 hits — no tiered-backfill implementation exists yet anywhere in the transcribe/maintenance plugin code
  grep -n 'registry.RunItems' internal/plugins/maintenance/intro_transcribe.go   # 1 hit, confirming this op already uses the worker-pool pattern the tiered backfill should keep using — an existing intro-transcribe op already exists as a base to extend/tier rather than building from nothing
  ```

### Reuse — don't invent

- Use `existing intro_transcribe.go op (single-file transcription op, worker-pooled via registry.RunItems) -- extend with tiering rather than writing a new op from scratch` in `internal/plugins/maintenance/intro_transcribe.go` (verify: `grep -n 'registry.RunItems' internal/plugins/maintenance/intro_transcribe.go`) — do NOT write a parallel helper.
- Use `intro_migrate_single_file.go (Tier 0's 'single-file books migrate by copy, zero GPU' logic may already partially exist here)` in `internal/plugins/maintenance/intro_migrate_single_file.go` (verify: `grep -n 'func' internal/plugins/maintenance/intro_migrate_single_file.go`) — do NOT write a parallel helper.
- Use `ClassifyIntro (credits/chapter/prose/unknown classification, used to decide Tier 1's 3-file probe vs 1b's escalate-to-full-set)` in `internal/transcribe/classify.go` (verify: `grep -n 'func ClassifyIntro' internal/transcribe/classify.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/plugins/maintenance/ (likely a new file, or extending intro_transcribe.go), implement Tier 0: for every single-file book, copy the existing book-level transcript (if present) into the per-file transcript store introduced by PR #2168 -- no GPU call, a pure data-migration pass. Reuse intro_migrate_single_file.go if it already does exactly this (its name suggests it might -- verify before writing new code).
2. Implement Tier 1: for assembled multi-file books, transcribe only the first 3 files (in reading order, using the per-file sort already fixed in #2168), classify each via ClassifyIntro.
3. Implement Tier 1b's escalation check immediately after Tier 1 completes for a book: if all 3 probed files classify as 'credits', enqueue the REMAINING files of that book for transcription too (full-set escalation) -- this must be a per-book decision made once all 3 probes are in, not a per-file independent choice.
4. Implement Tier 2: for books already flagged bookless/shattered/review-queue (reuse whatever existing query/filter identifies this set -- check the review-queue and regroup_shattered_ai.go's existing selection logic), transcribe every file unconditionally, no probing.
5. Implement Tier 3 as a low-priority, resumable, indefinite background sweep over any file still lacking a transcript after Tiers 0-2 have run -- this should be safe to run continuously/on a schedule without competing for the same worker-pool capacity as an operator-triggered Tier 0-2 run (consider a separate, smaller Concurrency setting).
6. Wire the whole tiered op through registry.RunItems per-tier (bounded worker pool, resumable, per CLAUDE.md's concurrency mandate) and register it as a new or extended operation visible in the ops system.
7. Absent-transcript semantics: per the TODO's own explicit warning ('Absent transcript means "cannot verify", never "continuation"' -- the codebase has been bitten by this exact class of bug four times already), make sure a book/file with NO transcript yet (Tier 3 hasn't reached it) is never treated by any downstream consumer as evidence of continuation -- it must read as unknown/unverified, not as a negative signal.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_200.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with exactly 1 or 2 files (not enough for Tier 1's 3-file probe) -- confirm the design falls back sensibly (e.g. probe however many files exist, up to 3) rather than erroring or skipping.
- A book that moves from 'not yet in the review queue' to 'flagged' WHILE a Tier 1 probe is in flight -- confirm no double-processing or a stuck partial state if a book's tier assignment changes mid-run.

## Tests

- internal/plugins/maintenance/intro_tiered_backfill_test.go (new file): TestTieredBackfill_Tier0MigratesSingleFileBooksWithoutGPUCall -- assert Tier 0 never invokes the transcription backend, only copies existing data.
- TestTieredBackfill_Tier1EscalatesOnThreeForThreeCredits -- seed a multi-file book where the first 3 files all classify as credits, assert the remaining files get enqueued; seed a book where only 2 of 3 do, assert NO escalation happens.
- TestTieredBackfill_Tier2AlwaysFullSet -- seed a review-queue-flagged book, assert every file is transcribed regardless of any probe result.
- TestTieredBackfill_AbsentTranscriptNeverReadsAsContinuation -- the anti-over-suppression check: assert whatever downstream consumer reads per-file transcript state treats a not-yet-transcribed file distinctly from a transcribed-but-classified-as-continuation file.

Anti-over-suppression test: `TestTieredBackfill_AbsentTranscriptNeverReadsAsContinuation` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/transcribe/... -count=1
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
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] T
- [ ] i
- [ ] e
- [ ] r
- [ ] e
- [ ] d
- [ ] B
- [ ] a
- [ ] c
- [ ] k
- [ ] f
- [ ] i
- [ ] l
- [ ] l
- [ ]  
- [ ] -
- [ ] c
- [ ] o
- [ ] u
- [ ] n
- [ ] t
- [ ] =
- [ ] 1
- [ ]  
- [ ] -
- [ ] v
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
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
- [ ] A
- [ ] p
- [ ] p
- [ ] l
- [ ] y
- [ ]  
- [ ] b
- [ ] o
- [ ] o
- [ ] l
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
- [ ] i
- [ ] n
- [ ] t
- [ ] r
- [ ] o
- [ ] _
- [ ] t
- [ ] i
- [ ] e
- [ ] r
- [ ] e
- [ ] d
- [ ] _
- [ ] b
- [ ] a
- [ ] c
- [ ] k
- [ ] f
- [ ] i
- [ ] l
- [ ] l
- [ ] .
- [ ] g
- [ ] o
- [ ] `
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] (
- [ ] a
- [ ] p
- [ ] p
- [ ] l
- [ ] y
- [ ] =
- [ ] f
- [ ] a
- [ ] l
- [ ] s
- [ ] e
- [ ]  
- [ ] d
- [ ] e
- [ ] f
- [ ] a
- [ ] u
- [ ] l
- [ ] t
- [ ] ,
- [ ]  
- [ ] m
- [ ] i
- [ ] r
- [ ] r
- [ ] o
- [ ] r
- [ ] i
- [ ] n
- [ ] g
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
- [ ] m
- [ ] i
- [ ] s
- [ ] s
- [ ] i
- [ ] n
- [ ] g
- [ ] _
- [ ] f
- [ ] i
- [ ] l
- [ ] e
- [ ] _
- [ ] r
- [ ] e
- [ ] p
- [ ] o
- [ ] i
- [ ] n
- [ ] t
- [ ] .
- [ ] g
- [ ] o
- [ ] :
- [ ] 5
- [ ] 2
- [ ] )
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
- [ ] C
- [ ] r
- [ ] e
- [ ] a
- [ ] t
- [ ] e
- [ ] O
- [ ] p
- [ ] e
- [ ] r
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ] C
- [ ] h
- [ ] a
- [ ] n
- [ ] g
- [ ] e
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
- [ ] i
- [ ] n
- [ ] t
- [ ] r
- [ ] o
- [ ] _
- [ ] t
- [ ] i
- [ ] e
- [ ] r
- [ ] e
- [ ] d
- [ ] _
- [ ] b
- [ ] a
- [ ] c
- [ ] k
- [ ] f
- [ ] i
- [ ] l
- [ ] l
- [ ] .
- [ ] g
- [ ] o
- [ ] `
- [ ]  
- [ ] h
- [ ] i
- [ ] t
- [ ] s
- [ ]  
- [ ] f
- [ ] o
- [ ] r
- [ ]  
- [ ] e
- [ ] v
- [ ] e
- [ ] r
- [ ] y
- [ ]  
- [ ] m
- [ ] u
- [ ] t
- [ ] a
- [ ] t
- [ ] i
- [ ] o
- [ ] n
- [ ]  
- [ ] p
- [ ] a
- [ ] t
- [ ] h
- [ ] ;
- [ ]  
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] T
- [ ] i
- [ ] e
- [ ] r
- [ ] e
- [ ] d
- [ ] B
- [ ] a
- [ ] c
- [ ] k
- [ ] f
- [ ] i
- [ ] l
- [ ] l
- [ ] _
- [ ] R
- [ ] e
- [ ] f
- [ ] u
- [ ] s
- [ ] e
- [ ] s
- [ ] W
- [ ] h
- [ ] i
- [ ] l
- [ ] e
- [ ] L
- [ ] i
- [ ] b
- [ ] r
- [ ] a
- [ ] r
- [ ] y
- [ ] S
- [ ] c
- [ ] a
- [ ] n
- [ ] A
- [ ] c
- [ ] t
- [ ] i
- [ ] v
- [ ] e
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
- [ ] a
- [ ] p
- [ ] p
- [ ] l
- [ ] y
- [ ]  
- [ ] p
- [ ] a
- [ ] t
- [ ] h
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] w
- [ ] i
- [ ] t
- [ ] h
- [ ] o
- [ ] u
- [ ] t
- [ ]  
- [ ] w
- [ ] r
- [ ] i
- [ ] t
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] w
- [ ] h
- [ ] e
- [ ] n
- [ ]  
- [ ] a
- [ ]  
- [ ] l
- [ ] i
- [ ] b
- [ ] r
- [ ] a
- [ ] r
- [ ] y
- [ ] .
- [ ] s
- [ ] c
- [ ] a
- [ ] n
- [ ]  
- [ ] o
- [ ] p
- [ ]  
- [ ] i
- [ ] s
- [ ]  
- [ ] r
- [ ] u
- [ ] n
- [ ] n
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] o
- [ ] r
- [ ]  
- [ ] q
- [ ] u
- [ ] e
- [ ] u
- [ ] e
- [ ] d
- [ ] ;
- [ ]  
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] T
- [ ] i
- [ ] e
- [ ] r
- [ ] e
- [ ] d
- [ ] B
- [ ] a
- [ ] c
- [ ] k
- [ ] f
- [ ] i
- [ ] l
- [ ] l
- [ ] _
- [ ] A
- [ ] p
- [ ] p
- [ ] l
- [ ] y
- [ ] T
- [ ] h
- [ ] e
- [ ] n
- [ ] U
- [ ] n
- [ ] d
- [ ] o
- [ ] I
- [ ] s
- [ ] B
- [ ] y
- [ ] t
- [ ] e
- [ ] I
- [ ] d
- [ ] e
- [ ] n
- [ ] t
- [ ] i
- [ ] c
- [ ] a
- [ ] l
- [ ]  
- [ ] a
- [ ] p
- [ ] p
- [ ] l
- [ ] i
- [ ] e
- [ ] s
- [ ]  
- [ ] o
- [ ] n
- [ ]  
- [ ] a
- [ ]  
- [ ] f
- [ ] i
- [ ] x
- [ ] t
- [ ] u
- [ ] r
- [ ] e
- [ ] ,
- [ ]  
- [ ] u
- [ ] n
- [ ] d
- [ ] o
- [ ] e
- [ ] s
- [ ]  
- [ ] v
- [ ] i
- [ ] a
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
- [ ] u
- [ ] n
- [ ] d
- [ ] o
- [ ] ,
- [ ]  
- [ ] a
- [ ] n
- [ ] d
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
- [ ] f
- [ ] i
- [ ] x
- [ ] t
- [ ] u
- [ ] r
- [ ] e
- [ ]  
- [ ] i
- [ ] s
- [ ]  
- [ ] u
- [ ] n
- [ ] c
- [ ] h
- [ ] a
- [ ] n
- [ ] g
- [ ] e
- [ ] d
- [ ] ;
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] b
- [ ] u
- [ ] i
- [ ] l
- [ ] d
- [ ]  
- [ ] .
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] &
- [ ] &
- [ ]  
- [ ] g
- [ ] o
- [ ]  
- [ ] v
- [ ] e
- [ ] t
- [ ]  
- [ ] .
- [ ] /
- [ ] .
- [ ] .
- [ ] .
- [ ]  
- [ ] e
- [ ] x
- [ ] i
- [ ] t
- [ ]  
- [ ] 0
- [ ] .
- [ ] Anti-over-suppression test: `TestTieredBackfill_AbsentTranscriptNeverReadsAsContinuation` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/transcribe/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_200.md`.

## Commit message

```
feat(missing-file-lane): Build the tiered per-file intro-transcription backfill (Tier (TODO L8316)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'func ClassifyIntro' internal/transcribe/classify.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: Tier 1b's escalation logic is the safety-critical piece -- getting it wrong reproduces the exact under-sampling bug (45.8% credit-parse rate from one-file-per-book) this whole feature exists to fix. This is the PREREQUISITE for todo_line 8316 parts 2 and 3 (wiring into the regroup classifier and First Aid both need real per-file transcript coverage to be useful) -- schedule this first.
