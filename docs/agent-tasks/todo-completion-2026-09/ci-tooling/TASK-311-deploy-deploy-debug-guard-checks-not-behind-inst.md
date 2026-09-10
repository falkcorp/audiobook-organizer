<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-311-deploy-deploy-debug-guard-checks-not-behind-inst.md -->
<!-- version: 1.7.0 -->
<!-- guid: 759acc14-dc67-4c94-82ea-35044a602bd9 -->
<!-- last-edited: 2026-09-10 -->

# TASK-311 — deploy/deploy-debug guard checks 'not behind' instead of 'exactly equals' origin/main, allowing unpushed commits to ship (CI-01)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `CI-01` (audit_ci.json) · adversarial re-check 2026-09-10: **CONFIRMED**

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `CI-01` (audit_ci.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-311" -b agent/ci-tooling-311-deploy-deploy-debug-guard-checks-not-beh origin/main
cd "$REPO/.worktrees/ci-tooling-311"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the is-ancestor check with: `read ahead behind < <(git rev-list --left-right --count HEAD...origin/main); [ "$ahead $behind" = "0 0" ] || (echo "HEAD and origin/main diverge — push/pull first"; exit 1)` in both deploy and deploy-debug.

Why it matters: An operator can `make deploy`/`make deploy-debug` from a checkout with local, unpushed commits and the pre-flight passes, shipping code to production that never went through review/CI — the exact incident class the TODO.md comment is trying to prevent.

## Background (verify before editing)

- Lines 72-74 (deploy) and 102-104 (deploy-debug): `git fetch origin main` then `git merge-base --is-ancestor origin/main HEAD || (echo "local main behind origin — pull first"; exit 1)`. `--is-ancestor origin/main HEAD` only fails when HEAD is missing commits from origin/main; it passes cleanly when HEAD is AHEAD of origin/main (local commits never pushed/reviewed). TODO.md:372 documents the intended precondition as `git rev-list --left-right --count HEAD...origin/main` == `0 0`, a bidirectional check — but that is not what the deploy targets run.
- Anchor: `Makefile.local.example:72` (audit `CI-01`, confidence high, severity high).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — Makefile.local.example:74 and :104 use `git merge-base --is-ancestor origin/main HEAD` — passes when HEAD is strictly ahead. TODO.md:371-372 documents the intended bidirectional `rev-list --left-right --count HEAD...origin/main` == `0 0`.
  - Blast radius: Committed Makefile has no deploy targets; every developer's gitignored Makefile.local is copied from this example, so the bug is real in practice but unmeasurable from the repo.
  - Existing tests to extend: none
  - Standing-ban contact: IS the deploy gate; verify by running the corrected snippet against a deliberately-ahead branch, no deploy

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e Makefile.local.example   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '66,80p' Makefile.local.example   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `Makefile.local.example` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_311.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
actionlint .github/workflows/*.yml 2>/dev/null || true; python3 -m py_compile scripts/*.py; go build ./...
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_311.md`.

## Commit message

```
fix(ci-tooling): deploy/deploy-debug guard checks 'not behind' instead of 'exactly equa (CI-01)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
