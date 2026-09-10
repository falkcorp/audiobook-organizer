<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-360-c716-resolved-the-3-954-book-api-vs-store-gap-de.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1eb0dc4f-3ac6-414e-86ae-53074bd000b5 -->
<!-- last-edited: 2026-09-10 -->

# TASK-360 — C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0  (TODO.md:10432)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0 unexplained”, lines 10432

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · ci-tooling subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0 unexplained”, lines 10432. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-360" -b agent/ci-tooling-360-c716-resolved-the-3-954-book-api-vs-stor origin/main
cd "$REPO/.worktrees/ci-tooling-360"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under section “C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0 unexplained” (lines 10432 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L10432 — - [ ] **CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/`logger.sanitizeLogLine` — evidence: Only one CodeQL model file exists at HEAD: .github/codeql/models/path-sanitizers.model.yml, and it contains only path-injection barrierModel/barrierGuardModel rows for internal/util and internal/security/pathvalidation — no rows for logging.Sanitize/SanitizeErr/sanitizeLogLine and no log-injection extensible predicate anywhere (`find . -iname '*.model.yml'` returns exactly that one file). `git log --all --oneline --grep=CA12` and `--grep=sanitizeLogLine` are both empty.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/" TODO.md   # the source item still exists (line numbers drift)
  test -e .github/codeql/models/path-sanitizers.model.yml   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_360.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_360.md`.

## Commit message

```
fix(ci-tooling): C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 i (TODO.md:10432)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
