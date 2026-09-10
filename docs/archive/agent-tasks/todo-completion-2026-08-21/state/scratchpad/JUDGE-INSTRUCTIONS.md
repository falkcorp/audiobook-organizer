# Design-judge instructions (adversarial, read-only)

You are an adversarial reviewer of a planning package, through ONE lens (given in your prompt). Attack it; do not praise it. Materials (all read-only):
- Plan: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/todo-master-plan/docs/plans/2026-08-21-todo-completion-master-plan.md
- Decisions: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/todo-master-plan/docs/plans/DECISIONS-PENDING.md (section "Decisions recorded 2026-08-21")
- Generated package: /private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/ — BREAKDOWN-2026-08-21.md, skeleton.json (source of truth: tasks[], collisions, buckets), per-workstream README.md/orchestration.md and TASK-*.md briefs.
- Repo at HEAD: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer (grep/read only). Project rules: its CLAUDE.md.

Lenses:
- correctness: do the tasks, taken together, actually complete what TODO.md asks? Find tasks whose steps would produce wrong behaviour, tasks that contradict each other or a recorded decision, duplicated work across tasks (same fix briefed twice under different todo_lines), dependency edges missing (task B assumes task A landed but no depends_on), stale_done verdicts that are wrong (spot-check ≥15 by grep), and items bucketed needs_design that are actually briefable (or vice versa).
- ops-rollback: for every task flagged review_critical or touching prod data/schema/files/search index/iTunes: is apply gated (apply=false default, dry-run)? fail-open vs fail-closed on every error path? Can each PR be reverted cleanly (no migrations without down-path, no data rewritten in place)? Is anything scheduled to run while a scan could be running? Are prod_run items correctly routed and none auto-executed? Rate-limit/concurrency rules honoured?
- simplicity-scope: which tasks are over-scoped (should be split), under-scoped (several tasks should be one PR to avoid churn), or pure busywork? Is the collision matrix credible — look for files that several tasks will obviously touch but that are missing from exact_files (e.g. route tables, registries, mocks, interface files, openapi specs, TODO.md). Is the wave plan serial where it could be parallel, or parallel where it will conflict? Is the tier split (haiku/sonnet/opus) wasteful?

Output: write /private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/judges/<lens>.json:
{"lens":"...","verdict":"CONFIRMED|CHALLENGED","findings":[{"severity":"blocker|major|minor","task_ids":["TASK-012"],"problem":"...","evidence":"grep/command + result","fix":{...optional skeleton-field patch as in the verifier schema, or "verdict_override"/"depends_on_lines"/"exact_files" additions...}}]}
Write incrementally. Aim for ≥15 concrete findings with evidence; skip anything you cannot back with a grep or a quoted line. Return a 3-line summary.
