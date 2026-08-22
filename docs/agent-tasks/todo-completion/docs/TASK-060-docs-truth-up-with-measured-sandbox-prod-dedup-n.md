<!-- file: docs/agent-tasks/todo-completion/docs/TASK-060-docs-truth-up-with-measured-sandbox-prod-dedup-n.md -->
<!-- version: 1.0.0 -->
<!-- guid: 97969510-d708-48b8-95f8-d4535b748fd9 -->
<!-- last-edited: 2026-08-21 -->

# TASK-060 — Docs truth-up with measured sandbox/prod dedup numbers (T13)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs subagent · **Why:** mechanical doc-number updates against already-measured values, following a fully pre-written brief · **Depends on:** none · **External blockers:** TODO.md L10831 (prod_run) — not a task in this package; coordinator confirms it is resolved or explicitly waives it before dispatch · **Wave:** 1

Source: `TODO.md` line 10849 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**T13**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-060-docs-truth-up-with-measured-sandbox-prod-dedup-n" -b agent/docs-060-docs-truth-up-with-measured-sandbox-prod-dedup-n origin/main
cd "$REPO/.worktrees/docs-060-docs-truth-up-with-measured-sandbox-prod-dedup-n"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Execute the 4 steps in docs/agent-tasks/error-correction-2026-07/TASKS.md:398-412 for the parts that don't require the still-pending T03 sandbox run: (1) update docs/dedup/STATUS.md and (2) docs/operations/pending-prod-actions.md's CONS-10/PH-2 rows with T04's measured PROD numbers (dismissed=7891, dismiss_errors=0, outcome=completed — TODO.md:10836-10839); (3) add/confirm the executive-summary backlog-drain entry for T04's prod apply; (4) grep docs/ for the stale baseline numbers 9,074/9074 and 10,319 and label every hit as the 2026-07-17 baseline rather than current state. Leave the T02/T03-sourced sandbox-specific rows explicitly marked 'pending T03' rather than filling them with stale or extrapolated numbers.

## Background (verify before editing)

- TASKS.md:410-411: 'Verify no stale claims remain: grep docs/ for 9,074/9074 and 10,319 — every hit must be labeled as the 2026-07-17 baseline, not current state.'
- TODO.md:10828-10830 already contains a clarifying note this task should propagate into docs/dedup/STATUS.md: the sandbox (7,878) and prod (7,891) purgeable counts are two DIFFERENT populations, not a drift, per a 2026-08-11 docs-audit correction — make sure the truth-up doesn't reintroduce that confusion.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'T13 — Docs truth-up' -A15 docs/agent-tasks/error-correction-2026-07/TASKS.md   # 1 hit ~L398, 4 numbered steps + ACCEPTANCE line through L413 — T13's brief is fully specified with 4 concrete steps and an acceptance line
  grep -n 'T04.*ALL DONE' TODO.md   # 1 hit ~L10836, dismissed=7891 — T04 (prod numbers) is available now
  grep -n '\*\*T03\*\*' TODO.md   # 1 hit ~L10831, unchecked '- [ ]' — T03 (sandbox numbers) is NOT yet available, so sandbox-derived doc updates are blocked
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Run `grep -n '9,074\|9074\|10,319' docs/dedup/STATUS.md docs/operations/pending-prod-actions.md docs/executive-summaries/*.md` to find every stale-baseline mention.
2. In docs/dedup/STATUS.md, replace/annotate any bare 9,074-style projection with the T04 measured result (dismissed=7891) labeled '2026-07-18 PROD apply result', explicitly distinguishing it from the T02 sandbox measurement (7,878) as noted in TODO.md:10828-10830 — do not merge them into one number.
3. In docs/operations/pending-prod-actions.md, update the CONS-10/PH-2 rows (search `grep -n 'CONS-10\|PH-2' docs/operations/pending-prod-actions.md`) to reflect T04's completed state.
4. Check `docs/executive-summaries/2026-08-*.md` (current month, per this repo's convention of using the CURRENT month's summary) for a T04 backlog-drain entry in plain language ('thousands of false duplicate suggestions removed'); add one if missing.
5. For any row that genuinely needs the T03 sandbox-run number (not yet available), write 'PENDING — awaiting T03 sandbox purge wave (TODO.md:10831)' rather than guessing or reusing the T02 pre-purge number.
6. Add a changelog.d/ fragment (headerless, per this repo's fragment convention) noting the docs truth-up.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_060.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Do not backfill sandbox-only rows with the prod number (7891) as a stand-in for the sandbox number (which will be different once T03 runs, per the 7,878-vs-7,891 two-populations note) — mark them PENDING instead.

## Tests

- n/a — docs-only change; no automated test covers doc content.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n '9,074\|9074\|10,319' docs/dedup/STATUS.md docs/operations/pending-prod-actions.md` — every remaining hit is inside a sentence explicitly labeling it as the 2026-07-17 baseline.
- [ ] `grep -n 'dismissed=7891' docs/dedup/STATUS.md` returns ≥1 hit (T04's real measured number is now recorded).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_060.md`.

## Commit message

```
refactor(docs): Docs truth-up with measured sandbox/prod dedup numbers (T13)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Partial completion only: the T04/prod-derived doc updates can land now; the T02/T03/sandbox-derived rows must wait for todo_line 10831 (T03) to actually run on sandbox. Do not mark T13 fully checked off in TODO.md until T03 lands — check off only the prod-numbers portion, or leave the box open with a progress note.
