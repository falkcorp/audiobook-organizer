<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-006-add-a-scheduled-detect-only-backstop-workflow-fo.md -->
<!-- version: 1.0.0 -->
<!-- guid: 22c0f086-eb22-4508-b640-47f50e85ec30 -->
<!-- last-edited: 2026-08-21 -->

# TASK-006 — Add a scheduled detect-only backstop workflow for auto-revert.yml (TODO.md L46)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · ci-tooling subagent · **Why:** New standalone workflow file with real logic (find the CI run for main's tip, age-check it, dedupe against open issues) — needs careful gh-cli scripting and a dedupe check the existing workflow itself lacks, not pure boilerplate. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 46 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Add a scheduled detect-only backstop for `auto-rev" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-006-add-a-scheduled-detect-only-backstop-workflow-fo" -b agent/ci-tooling-006-add-a-scheduled-detect-only-backstop-workflow-fo origin/main
cd "$REPO/.worktrees/ci-tooling-006-add-a-scheduled-detect-only-backstop-workflow-fo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new scheduled workflow (e.g. every 15 minutes) that checks whether main's HEAD has a failed 'Continuous Integration' run older than 30 minutes with no matching open auto-revert issue, and if so files a detect-only issue (never pushes/reverts) so a missed workflow_run event cannot leave red main silently unwatched.

## Background (verify before editing)

- The existing auto-revert.yml already has all the issue-body rendering and gh-issue-create machinery (lines 264-305) — the backstop should reuse the SAME label set (bug, ci, automation) so it is discoverable the same way, but must NOT duplicate an issue if one is already open for the same failing SHA (the existing workflow's 'File the bug' step has no such dedupe check either — worth flagging as a shared gap, not just adding it to the new workflow).
- gh run list --workflow 'Continuous Integration' --branch main --limit 1 --json conclusion,headSha,createdAt,url gives the latest gate run for main's tip without needing to call the revert-decision script at all — this workflow only needs to detect and report, not decide what to revert.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'schedule:\|cron:' .github/workflows/auto-revert.yml   # 0 hits — auto-revert.yml has no cron trigger
  grep -n 'gh issue create' .github/workflows/auto-revert.yml   # 1 hit at L305 — the existing issue-filing step creates issues with no pre-check
  grep -n 'gh issue list' .github/workflows/auto-revert.yml   # 0 hits — no pre-check list/search exists before the gh issue create call — no pre-check gh issue list/search exists before filing (the dedupe gap)
  ```

### Reuse — don't invent

- Use `.github/workflows/scripts/auto_revert.py's latest_conclusion_by_sha/select_span helpers (for computing which SHA the current CI gate ran against)` in `.github/workflows/scripts/auto_revert.py` (verify: `grep -n 'def latest_conclusion_by_sha\|def select_span' .github/workflows/scripts/auto_revert.py`) — do NOT write a parallel helper.

## Step-by-step

1. Create .github/workflows/auto-revert-backstop.yml with `on: schedule: - cron: '*/15 * * * *'` plus `workflow_dispatch:` for manual testing.
2. permissions: issues: write (read-only otherwise — this workflow never pushes or reverts, hence 'detect-only').
3. Step 'Find latest CI run for main': `gh run list --repo "$GITHUB_REPOSITORY" --workflow 'Continuous Integration' --branch main --limit 1 --json conclusion,headSha,createdAt,url --jq '.[0]'` capture into outputs.
4. Step 'Check age and conclusion': fail-soft/skip (not error) if conclusion != 'failure', or if `createdAt` is within 30 minutes of now (use `date -u -d"$CREATED_AT" +%s` / `date +%s` diff, matching the 30-minute threshold from the TODO).
5. Step 'Check for existing open issue': `gh issue list --repo "$GITHUB_REPOSITORY" --label ci --label automation --state open --search "CI red on main at ${FAILING_SHA:0:8}" --json number --jq 'length'` — if >0, skip filing (this is the dedupe the main workflow itself lacks).
6. Step 'File backstop issue': only if steps 4 and 5 both indicate 'act' — `gh issue create --repo "$GITHUB_REPOSITORY" --title "CI red on main, auto-revert did not fire (backstop)" --body "..." --label bug --label ci --label automation --label auto-revert-backstop`, explaining in the body that this is a DETECT-ONLY alert (auto-revert.yml normally handles this via workflow_run and did not).
7. Add a step summary block mirroring the existing workflow's Summary step for consistency.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_006.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- No CI runs yet on a brand-new repo/branch: `gh run list` returns empty — treat as 'nothing to check', not an error.
- CI run still in progress (conclusion null, not yet 'failure' or 'success'): skip, do not treat as failure.
- Rate limiting from running every 15 minutes indefinitely: gh api calls here are read-mostly and cheap, but note the interval choice in the workflow's header comment so a future editor does not need to re-derive why 15 minutes was chosen against the TODO's 30-minute threshold.

## Tests

- No unit test framework applies to a GitHub Actions workflow file directly; validate with `workflow_dispatch` manual runs against a deliberately-red test branch, and add a short comment block documenting that manual validation path (mirroring how auto-revert.yml documents its dry_run input).

Anti-over-suppression test: `The dedupe check (step 5) must not swallow a genuinely NEW red-main incident that happens to share a title substring with an old resolved one — scope the --search to also require --state open (already done) so a closed prior incident's issue never suppresses a new one.` — a known-good input still passes with the new guard active.

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `gh workflow view auto-revert-backstop.yml` shows the schedule trigger.
- [ ] A manual `gh workflow run auto-revert-backstop.yml` against a known-red main (or a fork test) files exactly one issue, and a second immediate run does not file a duplicate.
- [ ] Anti-over-suppression test: `The dedupe check (step 5) must not swallow a genuinely NEW red-main incident that happens to share a title substring with an old resolved one — scope the --search to also require --state open (already done) so a closed prior incident's issue never suppresses a new one.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_006.md`.

## Commit message

```
feat(ci-tooling): Add a scheduled detect-only backstop workflow for auto-rever (TODO L46)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n 'schedule:\|cron:' .github/workflows/auto-revert.yml` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Consider whether the dedupe gap found in step 5 (the EXISTING auto-revert.yml's own issue-filing step has no dedupe check either) deserves its own follow-up TODO — a flapping CI failure that auto-revert.yml itself handles repeatedly could already be filing duplicate issues today, independent of this backstop.
