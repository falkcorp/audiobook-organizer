<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-307-frontend-ci-yml-grants-an-unjustified-broad-perm.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3ffe9d6c-690f-43a9-8eb5-5decf64dc13c -->
<!-- last-edited: 2026-09-10 -->

# TASK-307 — frontend-ci.yml grants an unjustified, broad permission ceiling to an external reusable workflow (CI-02)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `CI-02` (audit_ci.json)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · ci-tooling subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: Wave 3 audit finding `CI-02` (audit_ci.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-307" -b agent/ci-tooling-307-frontend-ci-yml-grants-an-unjustified-br origin/main
cd "$REPO/.worktrees/ci-tooling-307"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Trim to what the reusable workflow actually requires (likely `contents: read` + `checks: write`, matching ci.yml); if id-token/packages/actions writes are genuinely needed, add a comment justifying each.

Why it matters: Runs on push to main/develop and same-repo PRs. `actions: write`, `packages: write`, `id-token: write` handed to code the workflow doesn't control is more privilege than lint/build/test needs and breaks the least-privilege discipline used everywhere else in the repo.

## Background (verify before editing)

- Lines 20-26: top-level `permissions: contents: write, actions: write, checks: write, packages: write, id-token: write, attestations: write`, with no justifying comment — unlike nightly-burndown.yml, hard-burndown.yml, codeql.yml, auto-revert-backstop.yml which carry tightly-scoped, commented permissions. The `frontend` job (L51-61) is a `uses:` call to `falkcorp/github-common/.../reusable-ci.yml`, so this set is the ceiling for every step of a workflow this repo does not control. ci.yml:23-25 does equivalent build/test work with only `contents: read` + `checks: write`.
- Anchor: `.github/workflows/frontend-ci.yml:20` (audit `CI-02`, confidence medium, severity high).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f .github/workflows/frontend-ci.yml   # the file the finding is anchored to still exists
  sed -n '14,26p' .github/workflows/frontend-ci.yml   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `.github/workflows/frontend-ci.yml` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_307.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving the dry-run / guard path writes nothing (fail-closed on error).

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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_307.md`.

## Commit message

```
fix(ci-tooling): frontend-ci.yml grants an unjustified, broad permission ceiling to an  (CI-02)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
