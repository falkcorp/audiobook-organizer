<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-202-wire-per-file-intro-classification-into-first-ai.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8c7d23f-45a2-4f4d-8951-07aceaad1260 -->
<!-- last-edited: 2026-08-21 -->

# TASK-202 — Wire per-file intro classification into First Aid as a tier-2 signal beside the duration probe (TODO.md L8316)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** adding a new tier-2 signal that 'lets the verdict pick the fixer' implies a decision-routing change in an existing diagnostic/repair-selection system -- needs the same care as part 2's classifier-ranking change · **Depends on:** TASK-200, TASK-201 · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8316 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Per-file intro transcription as the primary book" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-202-wire-per-file-intro-classification-into-first-ai" -b agent/missing-file-lane-202-wire-per-file-intro-classification-into-first-ai origin/main
cd "$REPO/.worktrees/missing-file-lane-202-wire-per-file-intro-classification-into-first-ai"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Locate the actual 'First Aid' diagnostic system this TODO item refers to (confirm the exact file/module name at HEAD before implementing -- 'First Aid' may be a colloquial name for the missing-file/library-validate-repair probe referenced elsewhere in TODO.md as '[[first-aid-library-validate-repair]]', or a distinct module; do not assume missing_file_audit.go is correct without verifying against that cross-reference first) and add per-file intro classification as a SECOND tier-2 signal alongside the existing duration probe, such that the combined verdict from both signals determines which repair path ('fixer') First Aid selects for a given book.

## Background (verify before editing)

- The TODO's 'Measured facts worth keeping' section for this same item references '[[first-aid-library-validate-repair]]'s probe already found 434 of 1,019 directory-shaped books confidently linkable' -- this wikilink-style reference is the strongest lead on which module 'First Aid' actually names; confirm it before writing code, since guessing the wrong target file wastes the whole implementation.
- This is a SEPARATE consumer from part 2's regroup-shattered classifier -- both read the same underlying per-file intro classification (from part 1's backfill) but drive different downstream decisions (regroup verdict vs repair-fixer selection).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'registry.RunItems' internal/plugins/maintenance/missing_file_audit.go   # at least 1 hit, confirming this file already uses the worker-pool pattern a tier-2 signal addition would extend — First Aid's probe/duration-signal machinery this item must extend, confirmed to exist under this name in the codebase (missing_file_audit.go is the closest match found for a 'First Aid' style diagnostic probe among the RunItems-using maintenance ops)
  ```

### Reuse — don't invent

- Use `ClassifyIntro -- same per-file classification signal part 2 wires into the regroup classifier` in `internal/transcribe/classify.go` (verify: `grep -n 'func ClassifyIntro' internal/transcribe/classify.go`) — do NOT write a parallel helper.

## Step-by-step

1. Search TODO.md and docs/ for '[[first-aid-library-validate-repair]]' or 'First Aid' to identify the exact module/file this item targets -- do not assume it is missing_file_audit.go without this confirmation.
2. Once the correct module is identified, add per-file intro classification (via ClassifyIntro) as a tier-2 signal in whatever probe/signal-collection structure that module already uses for the existing duration probe.
3. Extend the verdict/fixer-selection logic so it consults both the duration probe and the new intro signal when both are available, following the same 'intro outranks a pure proxy when directly available' philosophy part 2 applies (confirm with the owner or the TODO text whether First Aid's combination rule should be identical to the regroup classifier's, or independently designed for this different decision context).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_202.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- same absent-transcript-is-not-continuation caveat as parts 1 and 2 applies here too -- a book First Aid hasn't yet had a per-file transcript for must not have its fixer selection silently altered by a false 'no evidence of shattering' reading

## Tests

- A new or extended test in the correct module's test file: assert the tier-2 intro signal is consulted and influences fixer selection for a book where it provides evidence the duration probe alone would miss.

Anti-over-suppression test: `N/A -- covered by the regression test asserting absent-intro-data leaves fixer selection unchanged` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... && go vet ./... exit 0
- [ ] the relevant test suite for the confirmed First Aid module passes with the new tier-2 signal wired in
- [ ] Anti-over-suppression test: `N/A -- covered by the regression test asserting absent-intro-data leaves fixer selection unchanged` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_202.md`.

## Commit message

```
feat(missing-file-lane): Wire per-file intro classification into First Aid as a tier- (TODO L8316)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`go build ./... && go vet ./... exit 0`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: influences which repair ('fixer') First Aid selects for a book, a prod-data-adjacent decision path. FIRST STEP must be confirming the exact target module via the '[[first-aid-library-validate-repair]]' cross-reference -- this scout could not fully confirm the target file within budget and flags missing_file_audit.go only as the closest candidate found, not a confirmed match. Depends on todo_line 8316 part 1 (backfill) for real coverage, same as part 2.
