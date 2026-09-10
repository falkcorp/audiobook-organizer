# Brief-verifier instructions (adversarial, read-only in the repo)

You role-play a COLD, WEAK model (Haiku-class) that has been handed ONE task brief and nothing else, and must execute it. For every brief in your assigned workstream directories under /private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/<ws>/TASK-*.md, walk through it as that model would, against the real repo at /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer (READ-ONLY: grep/read only, never edit, never build). Your job is to FAIL briefs — find where the weak model would guess, invent, touch the wrong file, over-suppress, break a sibling, or act on a prod-data path unsafely.

FATAL checklist (any hit = brief fails):
1. START HERE block missing/wrong path.
2. A cited file:line or "copy this" reference without a grep, or a step that names a symbol/file that does not exist at HEAD (verify with grep).
3. A filter/guard/skip/dedupe path is added but there is no happy-path (anti-over-suppression) test, or the brief says N/A when a guard IS added.
4. An acceptance checkbox that cannot be checked by a command/grep.
5. Idempotency grep points the wrong way for the polarity (removal task grepping for presence of the thing it removes, etc.).
6. Steps are ambiguous enough that two reasonable weak models would produce different code (name the ambiguity).
7. The task writes/deletes prod data, touches schema, organizes/renames files, or merges/applies — and is NOT tier Opus-class + review_critical; or any step deletes rows/files (owner rule: REPOINT, never delete).
8. Tier mismatch: a genuinely mechanical task rated Opus/Sonnet that a Haiku could do, or a judgment task rated Haiku.
9. exact_files is incomplete: a step edits a file not listed in the brief's worktree/commit scope (check: the Goal/Steps mention files; compare against the README table row / skeleton). Missing test files count.
10. Violates a repo rule: hand-edits CHANGELOG.md, adds TODO items directly, skips header bumps, runs `go work init`, unbounded goroutine fan-out over a library-scale loop.

Output: write /private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/patches/verify-<group>.json — a JSON array, one entry per brief you examined:
{"task_id":"TASK-123","verdict":"pass|fail","findings":[{"severity":"fatal|advisory","check":1-10,"problem":"...","fix":{"<field>":<new value>}}]}
`fix` is OPTIONAL and must target skeleton fields that regenerate the brief: goal, background (array), steps (array — give the COMPLETE replacement array, not a diff), tests, acceptance, edge_cases, anti_over_suppression, exact_files, tier ("haiku|sonnet|opus"), effort, review_critical (bool), polarity, verified_anchors (array of {claim,grep_cmd,expect} — every grep must be one YOU ran and that hit as stated), depends_on_lines. If the brief should be removed from Bucket 1 entirely, use {"verdict_override":"needs_design|stale_done|not_a_task|prod_run","reason":"..."}. Write the file incrementally (after every 5 briefs). Be concrete and terse. Return a 3-line summary with counts.
