<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/TASK-342-library-scan-killed-by-the-watchdog-while-its-ow.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6fcfc041-8201-4cc2-a06e-87ef072decb9 -->
<!-- last-edited: 2026-09-10 -->

# TASK-342 — Library scan killed by the watchdog while its own auto-backup ran (TODO.md:2304)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “Library scan killed by the watchdog while its own auto-backup ran (2026-09-05)”, lines 2304

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “Library scan killed by the watchdog while its own auto-backup ran (2026-09-05)”, lines 2304. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-342" -b agent/misc-go-342-library-scan-killed-by-the-watchdog-whil origin/main
cd "$REPO/.worktrees/misc-go-342"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under section “Library scan killed by the watchdog while its own auto-backup ran (2026-09-05)” (lines 2304 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L2304 — - [ ] **`dedup.MergeBooks` hard-delete path has no audio-route guard** — surfaced by the — evidence: internal/reconcile/itunes_heal.go:314 still calls the legacy internal/dedup/book_dedup.go MergeBooks (:395-427) directly. That function still takes keepID as given with no HasAudioRoute check before deleting mergeIDs rows — grep 'HasAudioRoute' in book_dedup.go returns no matches. The function's own updated doc comment (:388-394) now explicitly documents this as a known follow-up ('that caller still carries the same ext-ID/ITL gap and is tracked as a follow-up'), confirming it is still open.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**`dedup.MergeBooks` hard-delete path has no audio-rou" TODO.md   # the source item still exists (line numbers drift)
  test -e internal/reconcile/itunes_heal.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_misc_go_342.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/reconcile/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_misc_go_342.md`.

## Commit message

```
fix(misc-go): Library scan killed by the watchdog while its own auto-backup ran (TODO.md:2304)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
