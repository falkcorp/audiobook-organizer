<!-- file: docs/agent-tasks/ux-small-items/TASK-04-category-ladders-residual.md -->
<!-- version: 1.0.0 -->
<!-- guid: 981065dc-6e80-4280-ba3f-b86c18a1495d -->
<!-- last-edited: 2026-07-10 -->

# TASK-04 — Confirm zero residual work on Audible category ladders (CATEGORY-LADDERS-RESIDUAL)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none — READ-ONLY task; changes ZERO files, so no collision with anything (wave 1, parallel-safe).

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · read-only-verification subagent · **Why:** pure grep-and-report against pre-verified anchors; no edit surface at all · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-category-ladders-residual" -b agent/ux-small-items-category-ladders-residual origin/main
cd "$REPO/.worktrees/ux-small-items-category-ladders-residual"
git rebase origin/main
```
(The worktree exists only to guarantee you are reading a clean, current HEAD. **You will make NO edits, NO commits, NO push, NO PR.** Remove the worktree when done: `git -C "$REPO" worktree remove .worktrees/ux-small-items-category-ladders-residual`.)

## Goal

The master plan says Audible category ladders are "mostly shipped; confirm no residual." Produce a verification REPORT (chat output only — do not write report files) proving whether any category-ladder work remains: TODO section state, code anchors, and any open GitHub issues. If a residual IS found, describe it precisely for the coordinator — do not fix it yourself.

## Background (verify — this task IS the verification)

- TODO.md category-ladders section (~:1940-1950): all five items were `[x]` at HEAD `fce58498` (CAT-1 through the UI/search-filter item, citing PRs #548, #561, #1728).
- Parsing code verified present in `internal/metadata/audible.go`: `CategoryLadders` response field, `audibleCategoryLadder` type, and the dedupe-collect loop over `p.CategoryLadders`.

- **Re-verify these anchors** — line numbers drift:
  ```bash
  grep -n "CategoryLadders\|audibleCategoryLadder" internal/metadata/audible.go   # expect hits ~:81, :143, :323, :326
  grep -n "CAT-1\|category ladder" TODO.md                                        # section items — check every one is [x]
  gh issue list --state open --search "category ladder" --repo falkcorp/audiobook-organizer   # expect empty
  ```

## Step-by-step

1. Run the three commands above; capture their VERBATIM output.
2. Inspect the TODO.md section: list every item and its checkbox state. Any `[ ]` item = residual.
3. Sweep for stragglers: `grep -rn "category_ladders" internal/ web/src --include="*.go" --include="*.ts" --include="*.tsx" | head -20` — confirm the field flows end-to-end (parse → store → UI/search) or note the gap.
4. Deliver the report in your final message (NOT a file): verbatim grep outputs, per-item checkbox table, an explicit verdict line `RESIDUAL: yes/no — <detail>`, and the mandatory footer `COMPLETED: <n> — <list>` / `REMAINING: <n> — <list>` / `BLOCKED: <n> — <list>`.
5. Confirm you changed nothing: `git status --porcelain` must be empty; include that output in the report.
6. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path added — nothing is added at all).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (You change no files, so this gate applies vacuously — running it is OPTIONAL for this read-only task; the deliverable is the report.)

## Acceptance criteria

- [ ] Report contains verbatim outputs of all three re-verify commands.
- [ ] Explicit `RESIDUAL: yes/no` verdict with evidence.
- [ ] `git status --porcelain` output included and EMPTY (zero repo changes).
- [ ] Anti-over-suppression: N/A
- [ ] COMPLETED/REMAINING/BLOCKED footer present with exact counts.

## Commit message

```
(no commit — read-only verification task; if you are about to commit anything, you have
exceeded scope: stop and report instead)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

No PR. Report-only. The coordinator records the verdict; if residual = yes, the coordinator cuts a NEW task — you do not fix it here.

## Idempotency / Rollback

Read-only: re-running this task any number of times is harmless (done = the report exists in the coordinator's hands; there is no repo state to check). Rollback = N/A — nothing was changed; at most remove the leftover worktree with `git worktree remove .worktrees/ux-small-items-category-ladders-residual`.
