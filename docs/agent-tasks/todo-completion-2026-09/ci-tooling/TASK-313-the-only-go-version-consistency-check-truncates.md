<!-- file: docs/agent-tasks/todo-completion-2026-09/ci-tooling/TASK-313-the-only-go-version-consistency-check-truncates.md -->
<!-- version: 1.0.0 -->
<!-- guid: 43c3105b-5a67-4bc9-a3ff-6e93cfda5b45 -->
<!-- last-edited: 2026-09-10 -->

# TASK-313 — The only Go-version consistency check truncates to major.minor, never checks .envrc/Dockerfiles, and only warns instead of failing (CI-04)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `CI-04` (audit_ci.json)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Depends on:** none · **Wave:** 1 

Source: Wave 3 audit finding `CI-04` (audit_ci.json). Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-313" -b agent/ci-tooling-313-the-only-go-version-consistency-check-tr origin/main
cd "$REPO/.worktrees/ci-tooling-313"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make the mismatch a hard failure (`::error::` + `exit 1`) and extend the check to grep the exact `go1.27.1` pin out of .envrc and both Dockerfiles.

Why it matters: A gate that can only warn is not a gate: if go.mod, ci.yml, .envrc or a Dockerfile drift on the patch version, the workflow named 'Check version consistency' reports the mismatch and then reports success.

## Background (verify before editing)

- L104-117: `GO_MOD_VER=$(grep '^go ' go.mod | awk '{print $2}' | sed 's/\.[0-9]*$//')` strips go.mod to major.minor before comparing against `CI_GO_VER` ('1.27' from ci.yml:47); the mismatch branch at L116 is `::warning::`, never `exit 1`. The check never reads `.envrc` (`GOTOOLCHAIN=go1.27.1`) or either Dockerfile's `golang:1.27.1-alpine`, so the exact patch pins (Makefile:43, .envrc:10, Dockerfile:27, Dockerfile.build-cgo:23) have no automated cross-check.
- Anchor: `.github/workflows/test-action-integration.yml:105` (audit `CI-04`, confidence high, severity medium).

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -f .github/workflows/test-action-integration.yml   # the file the finding is anchored to still exists
  sed -n '99,111p' .github/workflows/test-action-integration.yml   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `.github/workflows/test-action-integration.yml` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_ci_tooling_313.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_ci_tooling_313.md`.

## Commit message

```
fix(ci-tooling): The only Go-version consistency check truncates to major.minor, never  (CI-04)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Pure code change: rollback = `git revert` the commit. If the re-verify greps show the fix already present, run acceptance instead of re-implementing.

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
