<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-320-frontend-job-gate-is-a-computed-if-that-can-sile.md -->
<!-- version: 1.7.0 -->
<!-- guid: 9c524384-ce2e-4766-ac7b-aa81fb2e2ce2 -->
<!-- last-edited: 2026-09-10 -->

# TASK-320 — frontend job gate is a computed `if:` that can silently skip a required-looking check (CI-06)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `CI-06` (audit_ci.json)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `CI-06` (audit_ci.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-320" -b agent/ci-tooling-320-frontend-job-gate-is-a-computed-if-that origin/main
cd "$REPO/.worktrees/ci-tooling-320"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a companion assertion job (or explicit else branch) that fails if `has-frontend` is not exactly 'true' for a repo known to have `web/`.

Why it matters: A skipped job reports as passing to required-status checks; a misbehaving config-detection step would let a PR merge with zero frontend build/test coverage while showing green.

## Background (verify before editing)

- L54: `if: needs.frontend-config.outputs.has-frontend == 'true'`, where `has-frontend` is produced by an external action (`falkcorp/gha-get-frontend-config`, L46). If that action returns an empty/unexpected output, the `frontend` job is skipped rather than failed.
- Anchor: `.github/workflows/frontend-ci.yml:54` (audit `CI-06`, confidence low, severity low).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e .github/workflows/frontend-ci.yml   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '48,60p' .github/workflows/frontend-ci.yml   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `.github/workflows/frontend-ci.yml` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_320.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_320.md`.

## Commit message

```
fix(ci-tooling): frontend job gate is a computed `if:` that can silently skip a require (CI-06)

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
