<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-007-wire-scripts-test-check-memory-leaks-py-into-a-c.md -->
<!-- version: 1.1.0 -->
<!-- guid: 926a8516-ff6a-45b6-bfd7-85a408b24d14 -->
<!-- last-edited: 2026-09-02 -->

# TASK-007 — Wire scripts/test_check_memory_leaks.py into a CI job (repo-guards) (TODO.md L50)

> **Status 2026-09-02:** ✅ DONE — PR #2700 merged 2026-08-22 (a19851ebd).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** One-line addition to an existing CI step, no new logic — just add a second unittest discover invocation (or widen the existing one) pointed at scripts/. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 50 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`scripts/test_check_memory_leaks.py` is executed b" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-007-wire-scripts-test-check-memory-leaks-py-into-a-c" -b agent/ci-tooling-007-wire-scripts-test-check-memory-leaks-py-into-a-c origin/main
cd "$REPO/.worktrees/ci-tooling-007-wire-scripts-test-check-memory-leaks-py-into-a-c"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make .github/workflows/ci.yml's repo-guards job actually execute scripts/test_check_memory_leaks.py, since check-memory-leaks.py itself is a live, actively-used tool (Makefile `make check-memory-leaks`-style target and memory-leak-scan.yml both invoke it) — the test for it should not be dead weight.

## Background (verify before editing)

- scripts/ contains exactly one test_*.py file today (`ls scripts/test_*.py` → scripts/test_check_memory_leaks.py only), so widening the discovery to include scripts/ will not accidentally pick up unrelated tests.
- Deleting scripts/test_check_memory_leaks.py instead (the TODO's other named option) is the WRONG choice here: check-memory-leaks.py is actively wired into memory-leak-scan.yml and the Makefile, so its regression test is worth keeping — only the wiring is missing.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'unittest discover' .github/workflows/ci.yml   # 1 hit at L238, -s .github/workflows/scripts — the discover command only searches .github/workflows/scripts, not scripts/
  ls scripts/test_check_memory_leaks.py   # file exists — scripts/test_check_memory_leaks.py exists at repo root scripts/, a different directory
  grep -n 'check-memory-leaks.py' Makefile .github/workflows/memory-leak-scan.yml   # hits in both — Makefile:157 and memory-leak-scan.yml:48,110 — the checker it tests (check-memory-leaks.py) IS actively run elsewhere, so deleting the test would remove coverage of a live tool, not dead code
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In .github/workflows/ci.yml, at the 'Test the auto-revert span selector' step (~L237-238), add a second line: `python3 -m unittest discover -s scripts -p 'test_*.py' -v` right after the existing discover command (or rename the step to cover both, e.g. 'Test the auto-revert span selector and repo scripts').
2. Update the step's preceding comment (lines 233-236) to no longer claim the file is 'executed by nothing' once this lands.
3. Bump ci.yml's version-header-equivalent (workflow files in this repo may not carry the standard header — confirm via `head -5 .github/workflows/ci.yml`; if it has one, bump it).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_007.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If a future contributor adds an unrelated test_*.py to scripts/ root, it will now also run in this step — acceptable, since that is the same convention already used for .github/workflows/scripts.

## Tests

- The CI job itself IS the test — a subsequent `git push` / PR run of repo-guards must show the scripts/test_check_memory_leaks.py test cases executing (visible in the Actions log), not just the .github/workflows/scripts ones.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n "discover -s scripts" .github/workflows/ci.yml` returns 1 hit.
- [ ] A CI run's repo-guards job log contains output from scripts/test_check_memory_leaks.py's unittest cases (e.g. test names from `TestUntrackedListeners` or similar).
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_007.md`.

## Commit message

```
feat(ci-tooling): Wire scripts/test_check_memory_leaks.py into a CI job (repo- (TODO L50)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -n "discover -s scripts" .github/workflows/ci.yml` returns 1 hit.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

(none)
