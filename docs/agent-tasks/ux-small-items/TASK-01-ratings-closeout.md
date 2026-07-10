<!-- file: docs/agent-tasks/ux-small-items/TASK-01-ratings-closeout.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5362bb0c-a8a8-4da5-af49-bfa153325d56 -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Verify RATE-5 shipped; fix the stale User Ratings TODO header (RATINGS-CLOSEOUT)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none — this task touches only `TODO.md`; it does not touch `internal/server/`.

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs-closeout subagent · **Why:** two-line verified text edit; failure is cheap and grep-caught · **Depends on:** none (wave 1 — first TODO.md editor)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-ratings-closeout" -b agent/ux-small-items-ratings-closeout origin/main
cd "$REPO/.worktrees/ux-small-items-ratings-closeout"
git rebase origin/main
```

## Goal

The `User Ratings UI` section header in `TODO.md` still says "DB + schema done, API + UI pending" even though every sub-item beneath it (RATE-1..RATE-5) is checked `[x]` complete. Verify RATE-5 really shipped (it did — evidence below), then fix the header to say the section is fully shipped and add the missing shipping citation to the RATE-5 line. TODO.md edits ONLY — do not touch any code, do not re-implement anything.

## Background (verify before editing)

- Header at TODO.md ~:1857 reads `## ⭐ User Ratings UI — DB + schema done, API + UI pending` — STALE.
- Sub-items at TODO.md ~:1868-1872: RATE-1..4 are `[x]` with PRs #542/#552/#553/#554; RATE-5 (`Bulk rating view / quick-rate from list`) is `[x]` with NO citation.
- RATE-5 shipping evidence (verified 2026-07-10 at HEAD `fce58498`): component `web/src/components/audiobooks/BulkRatingDialog.tsx` exists; commits `399ea3f9` and `bd6848e2` ("feat(ui): bulk rating dialog from library row selection (RATE-5)"); TODO checkbox was ticked by commit `cd2b3eb8`.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "User Ratings UI" TODO.md                      # section header, ~line 1857, 1 hit
  grep -n "RATE-5" TODO.md                               # sub-item line, ~line 1872
  test -f web/src/components/audiobooks/BulkRatingDialog.tsx && echo SHIPPED   # must print SHIPPED
  git log --oneline main --grep="RATE-5" | head -3       # expect 399ea3f9 / bd6848e2 / cd2b3eb8
  ```
  If `BulkRatingDialog.tsx` is MISSING or the commits are absent, STOP and report — do not edit the header on unverified evidence.

## Step-by-step

1. Run every re-verify command above. All four must confirm before any edit.
2. In `TODO.md`, change the section header from `## ⭐ User Ratings UI — DB + schema done, API + UI pending` to `## ⭐ User Ratings UI — ✅ fully shipped (DB + schema + API + UI)`.
3. On the RATE-5 line, append the citation ` — BulkRatingDialog.tsx, commits 399ea3f9/bd6848e2 (verified 2026-07-10)` after the existing text. Do not change the `[x]` state or any other line.
4. Keep the change purely additive/corrective — do not reflow the section, do not touch RATE-1..4 lines, do not edit any other TODO.md section (TASK-02/03/05/07/08 own their own sections in later waves).
5. Bump the `TODO.md` file header (version + last-edited); keep the existing guid.
6. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path added).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (This PR is docs-only; Minimal CI green is the merge condition.)

## Acceptance criteria

- [ ] `grep -c "API + UI pending" TODO.md` returns 0.
- [ ] `grep -n "BulkRatingDialog" TODO.md` hits on the RATE-5 line.
- [ ] Anti-over-suppression: N/A
- [ ] Tests green (Minimal CI); vet/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited:" TODO.md` shows 2026-07-10 or later).

## Commit message

```
docs(todo): close out User Ratings section — RATE-5 verified shipped (RATINGS-CLOSEOUT)

Header said "API + UI pending" but RATE-1..5 are all shipped; RATE-5 verified
against BulkRatingDialog.tsx and commits 399ea3f9/bd6848e2. Citation added so
the checkbox is no longer evidence-free.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-ratings-closeout
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "BulkRatingDialog" TODO.md` hits AND `grep -c "API + UI pending" TODO.md` returns 0, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the stale-but-harmless header text is restored, no code or data touched.
