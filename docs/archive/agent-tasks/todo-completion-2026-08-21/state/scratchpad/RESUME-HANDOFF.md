# Resume handoff — paused 2026-08-21 20:15 EDT at 97% weekly usage

Planning branch: plan/todo-master-plan, draft PR #2682. Scratchpad is mirrored at
docs/agent-tasks/todo-completion/state/scratchpad/ (restore it to the session scratchpad, or run the tools from there).

## Resume steps (in order)
1. Resume scouts: scope-18 has 8/24 objects (scout/handoff-scope-18.md lists the 16 remaining todo_lines);
   scope-19 has 4 objects, 3 deliverables remaining (scout/handoff-scope-19.md). Same SCOUT-INSTRUCTIONS.md.
2. Resume verifiers: group 5 has 4/29 briefs (patches/handoff-verify-5.md); group 4 is COMPLETE (32/32);
   groups 1-3 were paused earlier (patches/handoff-verify-{1,2,3}.md); group 6 (maintenance, dedup, itunes) NOT started.
3. Copy scope-18/19 JSON into scout-all/, then: python3 gen_package.py scout-all dryrun && python3 apply_patches.py <scratchpad>
   && python3 gen_package.py scout-all dryrun && python3 audit_briefs.py dryrun/docs/agent-tasks/todo-completion <repo> --json audit-dry.json
   (task-ids.json keeps ids stable; apply_patches.py is idempotent via patches/applied.json).
4. Sync dryrun/ into docs/agent-tasks/todo-completion/, commit, rebase branch onto main, mark PR #2682 ready.
5. Execution: dispatch per BREAKDOWN waves, <=4-8 concurrent subagents, gates per brief (never make ci).
