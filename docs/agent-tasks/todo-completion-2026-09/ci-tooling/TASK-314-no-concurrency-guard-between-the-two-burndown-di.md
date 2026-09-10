<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-314-no-concurrency-guard-between-the-two-burndown-di.md -->
<!-- version: 1.6.0 -->
<!-- guid: 6daf4417-b89b-4639-a1ed-78ce0075ebaf -->
<!-- last-edited: 2026-09-10 -->

# TASK-314 — No concurrency guard between the two burndown-dispatch workflows sharing the same task hub, with a real Sunday overlap window (CI-05)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `CI-05` (audit_ci.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `CI-05` (audit_ci.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-314" -b agent/ci-tooling-314-no-concurrency-guard-between-the-two-bur origin/main
cd "$REPO/.worktrees/ci-tooling-314"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a shared `concurrency: { group: burndown-dispatch-${{ github.repository }}, cancel-in-progress: false }` to both workflows so hard-burndown queues behind an in-flight nightly-burndown.

Why it matters: Two concurrent dispatch batches against the same hub can claim overlapping tasks and double model spend for the Sunday window.

## Background (verify before editing)

- nightly-burndown.yml:29-30 runs 08:00 and 20:00 UTC daily (`mode: full` L54, `max_tasks: 8` L60) against `hub_repo: falkcorp/burndown-tasks` (L55). hard-burndown.yml:29 runs Sundays 10:00 UTC against the same hub (L52) with `max_tasks: 8` (L58). Neither declares a top-level `concurrency:` (grep across all workflows confirms). ci.yml:19 and auto-revert.yml:42 do declare one for their own races.
- Anchor: `.github/workflows/hard-burndown.yml:29` (audit `CI-05`, confidence medium, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e .github/workflows/hard-burndown.yml   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '23,35p' .github/workflows/hard-burndown.yml   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `.github/workflows/hard-burndown.yml` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_314.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_314.md`.

## Commit message

```
fix(ci-tooling): No concurrency guard between the two burndown-dispatch workflows shari (CI-05)

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
