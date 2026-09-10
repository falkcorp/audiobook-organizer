<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-341-the-barrier-still-does-not-fire-and-deleting-the.md -->
<!-- version: 1.0.0 -->
<!-- guid: 80344cc8-7c18-5c75-9294-798fd4966e53 -->
<!-- last-edited: 2026-09-10 -->

# TASK-341 — 🔴 The barrier still does not fire, and deleting the invalid file did not fix it (TODO.md:1935)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “CodeQL: the repo's path-sanitizer barrier model does not credit SecureJoin, and we now know it is not the predicate names” (L1911), items at lines 1935
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): HOLD-FOR-OWNER** — shape: MIXED (needs a diagnostic canary before a real fix is codeable) · class: security, but unresolved-root-cause — item lists 4 untested candidate causes and says 'next step is a canary, not another guess' · Section title is unrelated (reflink/zdb) to the cited item (CodeQL barrier); the item's own text says a guess-fix is not acceptable, it needs a canary/diagnosis step first.
> **Do NOT dispatch this brief to a worker.** It needs an owner decision or a prod run; it is listed in BREAKDOWN under *Held for the owner* and gated in PRIORITY-MATRIX.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · ci-tooling subagent · **Depends on:** none · **Wave:** owner-gated — not a worker task · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “CodeQL: the repo's path-sanitizer barrier model does not credit SecureJoin, and we now know it is not the predicate names” (L1911), items at lines 1935. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-341" -b agent/ci-tooling-341-the-barrier-still-does-not-fire-and-dele origin/main
cd "$REPO/.worktrees/ci-tooling-341"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “CodeQL: the repo's path-sanitizer barrier model does not credit SecureJoin, and we now know it is not the predicate names” (TODO.md line 1911; items at lines 1935 as of HEAD 42d187168):
  - L1935: **🔴 The barrier still does not fire, and deleting the invalid file did not fix it.**

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L1935 — **🔴 The barrier still does not fire, and deleting the invalid file did not fix it.** — evidence: TODO.md's own text at HEAD (lines 1935-1951, verified by direct read) is unchanged from the 2026-09-08 filing and still says the barrier does not fire and lists four untested candidate causes with 'Next step is a canary, not another guess.' PR #3132 (commit 46d97a0cd, 'fix(codeql): drop a model file declaring a predicate the Go pack lacks') only deleted the invalid .github/codeql/models/go-sanitizers.model.yml — confirmed via `git log --oneline -- .github/codeql/models/`, no commit after 46d97a0cd touches .github/codeql/models/path-sanitizers.model.yml or any workflow config-file wiring. The underlying alert (cover.go:250-area path-injection barrier not firing) has no follow-up commit.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**🔴 The barrier still does not fire, and deleting the invali" TODO.md   # item L1935 still exists (line numbers drift; text is the anchor)
  sed -n '1911p' TODO.md   # the enclosing heading: CodeQL: the repo's path-sanitizer barrier model does not credit Secure
  test -e .github/codeql/models/go-sanitizers.model.yml   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_341.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_341.md`.

## Commit message

```
fix(ci-tooling): 🔴 The barrier still does not fire, and deleting the invalid file did n (TODO.md:1935)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
