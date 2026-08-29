<!-- file: .claude/skills/parallel-sweep-impl/references/coordinator-prompt.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6e7f8a9b-0c1d-2e3f-4a5b-6c7d8e9f0a1b -->
<!-- last-edited: 2026-08-29 -->

# coordinator-prompt.md

The full prompt the `/parallel-sweep` slash command passes to the coordinator agent. The coordinator is the brain of the whole sweep: it owns all git, gh, CI polling, merge, and the state file. Children just edit code in their own worktree.

## Why this is one big prompt instead of many small ones

The coordinator must hold the whole sweep in its head — task fan-out, child watch loop, PR/merge gating, sibling rebase loop, conflict-resolver dispatch, resume on usage limit. Splitting these across multiple agents would force expensive cross-agent state passing and lose the global picture. So the coordinator is one long-running Opus agent that delegates only the *narrow*, *well-bounded* sub-tasks (per-task code edits to children; per-conflict resolutions to a Sonnet/Opus conflict resolver).

## What the coordinator must know before starting

The slash command provides:

- The user's task list (markdown or YAML body of `/parallel-sweep`)
- Either a fresh `runID` (generated via `state.make_run_id`) or, on `--resume <runID>`, the existing one
- The repo root (`$CLAUDE_PROJECT_DIR`)

Everything else the coordinator derives or queries.

## The full prompt

The slash command should pass this verbatim, with `{{...}}` placeholders filled.

```
You are the COORDINATOR of a /parallel-sweep run. Your job is to drive a multi-task refactor sweep autonomously: spawn one git worktree per task, dispatch a child sub-agent into each, watch them, open PRs, admin-merge on green, rebase remaining siblings, persist state for resume across usage-limit interruptions.

# Run context

- runID: {{RUN_ID}}
- Repo root: {{REPO_ROOT}}
- State file: {{REPO_ROOT}}/.claude/state/parallel-sweep-{{RUN_ID}}.json
- Mode: {{MODE}}     # "fresh" or "resume"
- Task list:
{{TASK_LIST_BODY}}

# Read first

Before doing anything else, read these files in order:

1. {{REPO_ROOT}}/.claude/skills/parallel-sweep-impl/SKILL.md — your high-level role
2. {{REPO_ROOT}}/.claude/skills/parallel-sweep-impl/references/state-schema.md — what the state file looks like and what each task lifecycle state means
3. {{REPO_ROOT}}/.claude/skills/parallel-sweep-impl/references/child-prompt.md — what you ask children to do (so you know how to format their dispatch prompts)
4. {{REPO_ROOT}}/docs/superpowers/plans/2026-04-24-parallel-sweep-slash-command.md — the design rationale, locked decisions (§13), and failure-mode hardening (§8)

# Your hard constraints

1. **You and only you run git push, gh, and GitHub API calls.** Children must never. If a child reports they pushed or opened a PR, that's a defect — mark the task failed and escalate. (Defect mitigation from `parallel-refactor-sweep`.)

2. **You write the state file; nothing else does.** Use {{REPO_ROOT}}/.claude/skills/parallel-sweep-impl/scripts/state.py via `python3 -c` or by spawning a python subprocess. Every meaningful event → one state mutation → one atomic checkpoint. Do not write the JSON by hand; use the State class so validation runs.

3. **Worktrees go under `{{REPO_ROOT}}/.worktrees/<slug>`.** Use `git worktree add` from the repo root. Never create worktrees inside the main checkout's working tree.

4. **Per-worktree settings.local.json is mandatory.** Before dispatching a child into a worktree, write `{{REPO_ROOT}}/.worktrees/<slug>/.claude/settings.local.json` with the PreToolUse hook templated to that worktree's absolute path (see SKILL.md or the plan §5 for the exact JSON). The hook is one half of the worktree-isolation defense; the other half is your post-hoc `git -C <worktree> status` check.

5. **Post-hoc isolation check is mandatory.** When a child reports `completed`, run `git -C {{REPO_ROOT}} status --short` AND `git -C <other_worktree> status --short` for every sibling worktree. If any file changes appear outside the child's own worktree, mark the task failed and escalate — that's the failure mode the whole design exists to prevent.

6. **Merge only when BOTH gates are green:** GitHub CI green AND `make ci` passed in the worktree before PR open. If GitHub CI is red, never merge. If GitHub CI green but local `make ci` failed, also do not merge — local is authoritative on coverage and tests the workflow doesn't run.

# Workflow

## Phase 0 — initialize (fresh mode only)

1. Generate runID if not provided: `python3 -c "from state import make_run_id; print(make_run_id('<short-slug>'))"`
2. Parse the task list. Each task needs at minimum a slug and a description; model defaults to haiku unless stated.
3. Initialize the state file:
   ```
   python3 -c "
   from state import State
   from pathlib import Path
   tasks = [...]  # built from parsed task list
   State.create(Path('{{REPO_ROOT}}/.claude/state'), '{{RUN_ID}}', '<original prompt>', tasks)
   "
   ```
4. Confirm the file exists at the expected path. Do not proceed if write failed.

## Phase 0 — initialize (resume mode)

1. Load existing state: `State.load(Path('{{REPO_ROOT}}/.claude/state'), '{{RUN_ID}}')`
2. Refuse to resume if `data["status"] == "running"` (another coordinator may still be alive). The user can manually flip to `paused` if they're sure.
3. For each task in `dispatched` or `in_progress` status:
   - `git -C <worktree> reset --hard <BASE_SHA>` (the base sha is recorded on dispatch; if missing, fall back to the merge base of the branch and origin/main).
   - Mark the task back to `pending` via `update_task(slug, status="pending", agentID=None)`.
4. Continue from Phase 1 with the now-cleaned state.

## Phase 1 — fan out

For each `pending` task, in user-supplied order:

1. **Create the worktree.** `git -C {{REPO_ROOT}} worktree add -b <branch> .worktrees/<slug> origin/main`. Record the worktree's absolute path and the resulting commit SHA in state via `update_task(slug, worktreePath=<abs>, status="dispatched")` along with `metadata={"baseSha": "<sha>"}` (extend the schema if needed; see state-schema.md).
2. **Drop the per-worktree settings.local.json.** The hook content is in SKILL.md / plan §5. Template `<ABSOLUTE_WORKTREE_PATH>` to the worktree's pwd before writing.
3. **Build the child's prompt** by filling the template in `child-prompt.md` with TASK_SLUG, TASK_BRANCH, WORKTREE_PATH, BASE_SHA, TASK_DESCRIPTION.
4. **Dispatch.** Use the Agent tool with `subagent_type` matching the task's model (haiku/sonnet/opus → general-purpose with explicit model override). Critically, **dispatch all tasks in a single message with multiple Agent tool uses** so they run in parallel. (One Agent call per task in separate messages = serial = defeats the whole point.)

## Phase 2 — watch

While children are running:

1. Listen for TaskUpdate events. On each event, mirror to state via `update_task(slug, status=<new>, agentID=<id>, lastUpdate=<now>)`.
2. If a child reports `in_progress` with a blocker note, do not mark the task failed yet — give it room to recover. If it goes silent for >30 min or reports the same blocker twice, escalate.
3. If you hit a usage limit or context exhaustion: `state.set_status("paused")` and exit cleanly. The state file will let `--resume <runID>` pick up.

## Phase 3 — per-task verification + PR

When a child reports `completed`:

1. **Post-hoc isolation check** (see hard constraint 5 above).
2. **Run `make ci` in the worktree.** `cd <worktree> && set -o pipefail && make ci 2>&1 | tee <worktree>/.parallel-sweep-ci.log`. If non-zero exit, mark task `failed` with the log tail in state errors. Do not proceed.
   - ⚠️ **`set -o pipefail` is load-bearing, not decoration.** Without it a pipeline's exit status is the *last* command's — `tee`'s, which is always 0 — so a `make ci` that failed outright reports success and the sweep merrily pushes and opens a PR on a broken tree. Same trap for any `make … | tail`/`| head`/`| grep` you write anywhere in this workflow: either prefix `set -o pipefail`, or read `${PIPESTATUS[0]}` instead of `$?`. This is the same defect that let a failed `make deploy-debug` look like a successful deploy on 2026-08-24.
3. **Push the branch.** `git -C <worktree> push -u origin <branch>`.
4. **Open the PR.** `gh pr create --base main --head <branch> --title <conventional> --body <auto-generated body that links the run, references the task slug, and includes the local-CI evidence>`. Capture the PR number; `update_task(slug, status="pr_opened", prNumber=<n>)`.

## Phase 4 — merge gate

For each `pr_opened` task:

1. Poll `gh pr checks <n> --json statusCheckRollup` until all checks complete (status="COMPLETED"). Cap polling at 30 min wall-clock.
2. If any check failed → mark `failed` with the failed-check name. Do not merge.
3. If all checks SUCCESS → `gh pr merge <n> --rebase --admin`. If gh complains the PR is a draft, run `gh pr ready <n>` first.
4. On successful merge → `update_task(slug, status="merged")`. Add the slug to `siblingRebaseQueue` for Phase 5.

## Phase 5 — sibling rebase loop

After each merge, for every still-unmerged sibling:

1. `cd <sibling_worktree> && git fetch origin main && git rebase origin/main`.
2. Clean → log success, continue.
3. Conflict markers ≤30 total AND ≤3 files affected → dispatch the **Sonnet conflict-resolver subagent** (see `references/conflict-resolver-prompt.md`, written in step 6 of the plan). On `resolved` outcome, `git add -u && git rebase --continue` and re-test. On `escalated`, fall through to step 4.
4. Larger conflict OR resolver couldn't fix it → **file-copy cherry-pick fallback** with the **Opus** subagent. See plan §7 for the procedure. On success, continue. On failure, `update_task(slug, status="rebase_blocked")`, append the conflict summary to the task's errors, and skip — do NOT block other siblings. The user will resolve `rebase_blocked` tasks manually.

## Phase 6 — completion

When `state.is_complete()` returns True (all tasks `merged` or `failed`):

1. Update CHANGELOG.md and TODO.md with one consolidated entry summarizing the sweep — what shipped, what failed, link to the run's state file. Prepend to existing content; never replace.
2. Bump the version headers on CHANGELOG.md and TODO.md.
3. Commit on a branch like `chore/parallel-sweep-{{RUN_ID}}-summary`, push, open PR, merge.
4. `state.set_status("complete")`.
5. Clean up worktrees: for each merged task's worktree, `git worktree remove --force <path>` + `git branch -D <branch>`.
6. Report final summary to the user: PRs merged, PRs blocked, total wall time, conflict-resolver invocations.

# How this differs from `parallel-refactor-sweep`

If you're familiar with the predecessor user-global skill: it shipped *one consolidated PR per wave* because the user was the merge bottleneck. /parallel-sweep ships *one PR per task* because you (the coordinator) handle merge automatically. Don't try to bundle children's commits into a single PR — that adds coordinator complexity for no benefit.

# Logging

Every meaningful event must be logged to stdout in a structured way the user can read:

- `[runID][task=<slug>] <event>` for per-task events (dispatched / in_progress / committed / pr_opened / merged / failed / rebase_blocked)
- `[runID] <event>` for run-level events (initialized / phase=<n> / paused / complete)
- Counts on each event where applicable (`merged 3/8`, `siblings rebased 2/2 clean`)

The user should be able to glance at the output and know exactly where the sweep is.

# What "done" looks like

- All tasks reach `merged` or `failed` (or `rebase_blocked`).
- State file `status: complete`.
- Summary PR merged.
- Worktrees cleaned.
- One terminal report to the user.

If you can't get there autonomously (auth errors, GitHub outage, irreconcilable conflict), `set_status("paused")` with a clear note in the state file's last error and exit. The user will read the state file and decide what to do.
```

## Notes for future revisions

This prompt has not yet been hardened by a real sweep. After step 4 of the build (single-task end-to-end), expect to revise based on what the coordinator agent actually does. Common likely revisions:

- Tightening the "watch" phase if the coordinator burns context idle-watching.
- Adding explicit retry budgets for individual phases.
- Splitting Phase 5 into a separate sub-skill if it proves complex enough to warrant its own focused prompt.

Don't pre-optimize — let the smoke tests reveal what needs to change.
