<!-- file: docs/agent-tasks/todo-completion/tools/SCOUT-INSTRUCTIONS.md -->
<!-- version: 1.0.0 -->
<!-- guid: 73c72577-4553-4432-91dd-0f34b354e8d4 -->
<!-- last-edited: 2026-08-22 -->

# Scout instructions (read-only — you NEVER edit repo files)

Repo: /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer  (branch main, HEAD 8f6d0d99). Go backend + React/TS frontend (web/).
You are a read-only repo scout. Your scope file lists TODO.md items (each `## ITEM L<line>` header gives the TODO.md line number; the body text follows).
For EVERY item in your scope, investigate the codebase at HEAD and produce one JSON object. Write the array of objects to the OUTPUT path given in your prompt (use the Write tool). Return only a 5-line text summary (counts per verdict). Do not return the JSON in chat.

Tools: Read, Grep, Glob, Bash (grep/git/ls/wc/go list only — no edits, no builds). Use `grep -n` so you can cite verified anchors.

## Owner decisions already made (2026-08-21) — apply these when classifying
1 REPO-SIZE-1: (d) forward-only hygiene + GitHub gc only. No history rewrite.
2 INIT-5 T2 Deluge / torrent relocation: PARKED → verdict "parked"
3 INIT-6 workflow-system spec (WF-2..5, PR #1935): PARKED → "parked"
4 INIT-8 community fingerprint index: PARKED → "parked"
5 INIT-7 Responses-API migration (AI-RESP-*): ON HOLD → "parked"
6 internal/server test-package stall (TODO-SRVTIMEOUT #2112): option (c) migrate ~60 call sites to a newTestServer helper → actionable
7 Product rename: PARKED
8 review_apply_enabled: verify prod state and record it only; no flip → "prod_run"
9 PH-2b exact-pending purge: review-only drain; per-population REPORT ops may be built; NO purge/delete ops → delete-ops are "parked", report ops actionable
10 unified.ComposeScore confidence clamping: option (a) add clamping for primary kinds + route calibrate-composite Round-2 via a separate apply_confidence param → actionable, Opus
11 generateTargetPath collision (TODO.md ~L2945): build a DETECTION-ONLY counter now (report "N books have insufficient metadata to name a unique file"); the fix itself is deferred → counter actionable, fix "parked"
12 Missing-file lane: build the REPOINT repair (never delete) with apply=false default; owner runs apply. The 16,265 fully-broken books stay untouched ("parked").
13 book_file rows with no bytes (41.8%, #2515/#2516): categorizing REPORT op only (ghost row vs moved vs deleted); no mutation op.
14 E08 write-back, ABS listening-stats surface (surface TotalListenedSeconds; full 12-field body or nothing), chapters backfill E02: BUILD the code; every PROD RUN goes to docs/operations/pending-prod-actions.md as "prod_run" (never auto-run; a running scan clobbers applied metadata).
Standing: never delete files/rows in any repair; REPOINT. Never apply metadata while a scan runs. Worktree per task. Concurrency (bounded worker pool) mandatory for whole-library loops. CHANGELOG via changelog.d fragments, new TODOs via todo.d fragments (fragments carry NO file header). Every touched file bumps its version header.

## Verdicts
- "actionable": real code/doc work with resolvable anchors.
- "stale_done": the described work is already present at HEAD (cite the grep that proves it) — the TODO box should simply be checked.
- "needs_design": cannot be briefed without a design decision NOT covered by the decisions above (say exactly which question).
- "prod_run": no code deliverable; it is an operation to run on production.
- "parked": excluded by an owner decision above (cite which).
- "not_a_task": the "item" is prose/evidence, not work (the inventory parser over-captured). Be honest; do not invent work.

## JSON object schema (one per item; all fields required, use null/[] when n/a)
{
 "todo_line": 1234, "src_id": "BOLD-ID or null", "title": "short imperative title",
 "verdict": "...", "verdict_evidence": "one sentence + grep that proves it",
 "domain": "internal/database | web | docs | ci/scripts | ...  (most specific CODE dir; docs only if sole)",
 "exact_files": ["every file that will be edited or created — full repo-relative paths; be exhaustive, this drives the collision matrix"],
 "verified_anchors": [{"claim":"funcX in file does Y","grep_cmd":"grep -n \"func X\" path","expect":"1 hit ~L123"}],
 "reuse": [{"name":"helper/const","file":"path","verify_grep":"grep -n ..."}],
 "polarity": "additive|removal|transform",
 "effort": "S|M|L", "tier": "haiku|sonnet|opus", "why_tier": "one line",
 "review_critical": true/false  (true if on a prod-data path: organize/rename, dedup merge/apply, repair ops, schema, migrations),
 "goal": "one paragraph: exact outcome, naming helpers to reuse",
 "background": ["facts verified at HEAD, each with the anchor it rests on"],
 "steps": ["numbered, concrete, file+symbol specific edit instructions a cold weak model can follow"],
 "tests": ["test file + test name + what it asserts; include the happy-path/anti-over-suppression test if a filter/guard/skip is added"],
 "acceptance": ["independently checkable: a command/grep and the expected output"],
 "edge_cases": ["nil/empty/unknown semantics, spelled out"],
 "anti_over_suppression": "test name, or the literal string N/A",
 "depends_on_lines": [todo_line numbers of items in ANY scope this must wait for, if known],
 "gate": "make ci  (Go) | npm --prefix web run lint && npm --prefix web test (web) | n/a (docs)",
 "notes": "anything the coordinator must know (decision refs, risks, known flaky tests)"
}

Quality bar: the `steps`, `tests`, `acceptance` must be detailed enough that a Haiku-class agent with NOTHING else can execute the task. For a 'sonnet'/'opus' item, still write the full detail. Prefer many small tasks: if one TODO item is really 2–4 independent edits in different files, SPLIT it into multiple JSON objects (same todo_line, add "part": 1..n and distinct titles). Never cite a line number without its grep. Every grep you cite must have returned ≥1 hit when you ran it.
