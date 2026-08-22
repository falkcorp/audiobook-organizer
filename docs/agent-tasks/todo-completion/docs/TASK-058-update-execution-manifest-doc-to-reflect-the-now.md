<!-- file: docs/agent-tasks/todo-completion/docs/TASK-058-update-execution-manifest-doc-to-reflect-the-now.md -->
<!-- version: 1.0.0 -->
<!-- guid: 55a7efc3-33b0-4eca-be90-48d2e5d88550 -->
<!-- last-edited: 2026-08-21 -->

# TASK-058 — Update execution-manifest doc to reflect the now-settled human gates (TODO.md L10635)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · docs subagent · **Why:** mechanical status-table edit reflecting decisions already made elsewhere in this session · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10635 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Execution-manifest human gates**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-058-update-execution-manifest-doc-to-reflect-the-now" -b agent/docs-058-update-execution-manifest-doc-to-reflect-the-now origin/main
cd "$REPO/.worktrees/docs-058-update-execution-manifest-doc-to-reflect-the-now"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Edit the status/notes columns of docs/plans/2026-07-10-execution-manifest.md's INIT-5, INIT-6, INIT-7, INIT-8, and REPO-SIZE-1 (INIT-9 TASK-06) rows to record the 2026-08-21 owner decisions (all four INIT items PARKED; REPO-SIZE-1 decided as Option (d) forward-only), and remove/annotate the 'DECISIONS-PENDING #1-#5' section (L81-85) as resolved.

## Background (verify before editing)

- docs/plans/2026-07-10-execution-manifest.md:24 'INIT-5 ... T2 = real-Deluge spike, STOP-FOR-HUMAN sign-off before T3 ... 1/7 ... T2 spike = human gate blocking T3-T7 (DECISIONS-PENDING #2)'.
- L25 'INIT-6 ... STOP-FOR-HUMAN ... GATED ... spec review open (PR #1935; DECISIONS-PENDING #3)'.
- L26 'INIT-7 ... HOLD ... greenlight not given (DECISIONS-PENDING #5)'.
- L27 'INIT-8 ... GATED ... STOP-FOR-HUMAN review session (DECISIONS-PENDING #4)'.
- L28 'INIT-9 ... TASK-06 REPO-SIZE-1 = STOP-FOR-HUMAN plan-only ... T06 REPO-SIZE-1 plan written, decision pending (DECISIONS-PENDING #1)'.
- L81-85 lists the same five items under a 'Human decisions needed' section.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'DECISIONS-PENDING' docs/plans/2026-07-10-execution-manifest.md   # ≥5 hits at L24-27,81-85 — manifest lists these 5 items as open human gates with DECISIONS-PENDING refs
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Edit L24: change status cell to note 'PARKED 2026-08-21 (owner decision #2, this scout package) — T2 Deluge spike not resumed'.
2. Edit L25: change status cell to 'PARKED 2026-08-21 (owner decision #3)'.
3. Edit L26: change status cell to 'PARKED 2026-08-21 (owner decision #5) — hold-lift not given'.
4. Edit L27: change status cell to 'PARKED 2026-08-21 (owner decision #4)'.
5. Edit L28: change status cell to 'DECIDED 2026-08-21 (owner decision #1): Option (d) forward-only + GitHub Support gc, no rewrite — see docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:223'.
6. Edit the L81-85 'Human decisions needed' list: either delete it or replace each bullet with '[RESOLVED 2026-08-21] <original text>'.
7. Bump the file's version header comment (check for one near the top of the file; add/update per repo Markdown header convention if present).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_058.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- None — pure status doc update, no code path depends on this file's content.

## Tests

- n/a — documentation-only edit; no test asserts manifest table contents.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n DECISIONS-PENDING docs/plans/2026-07-10-execution-manifest.md` returns 0 hits (all resolved and reworded) or all remaining hits are inside a 'RESOLVED' annotation.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_058.md`.

## Commit message

```
refactor(docs): Update execution-manifest doc to reflect the now-settled hum (TODO L10635)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is purely a doc close-out reflecting decisions already made in this scout package's owner-decision list; do not re-litigate the decisions themselves.
