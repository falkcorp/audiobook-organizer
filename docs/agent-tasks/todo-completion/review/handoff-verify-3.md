# Handoff — group-3 brief verifier (paused by coordinator)

## Output file (valid JSON, 7 entries so far)
`/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/patches/verify-3.json`

## Briefs examined (7 of 29)
- TASK-001 (audiobooks) — FAIL (2 fatal findings: broken verify grep for cache-key convention; TTL-only cache misses existing `svc.libGen.Key()` generation-scoping pattern for correctness-grade invalidation)
- TASK-002 (audiobooks) — FAIL (2 fatal findings: unchecked acceptance criterion #2; contradictory zero-hit-grep interpretation between Background and Idempotency sections)
- TASK-003 (audiobooks) — FAIL (1 fatal: fully subsumed/duplicated by TASK-002 at the same line, wave-3 vs TASK-002's wave-1, "Depends on: none" is wrong)
- TASK-004 (audiobooks) — FAIL (1 fatal + 1 advisory: cites nonexistent `ptrBool` helper — real name is `boolPtr`; cites nonexistent `getBooksByAuthorID` dedup logic)
- TASK-005 (audiobooks) — FAIL (1 fatal: predicate added to steps 4/5 without updating the `hasFPFilters`/`hasFingerprintingFilters` gates that decide whether the post-filter code path runs at all — the new filter would silently no-op when it's the only active filter)
- TASK-016 (config) — FAIL (1 fatal + 1 advisory: persistence.go step 4 keeps `c.WriteBackMetadata = b` verbatim after step 1 renames that field away — compile error; struct-literal alias-read needs a helper function, brief says "helper or inline" but inline isn't syntactically possible there)
- TASK-017 (config) — PASS (all anchors verified at HEAD exactly; step 3's catch-all grep correctly surfaces the one existing hardcoded-default test assertion that needs updating; no fatal issues found)

## Briefs remaining (22, in order)
config workstream (4 left):
- TASK-018 (config)
- TASK-019 (config)
- TASK-020 (config)
- TASK-021 (config)

web workstream (18 left, not yet started):
- TASK-158, TASK-159, TASK-160, TASK-161, TASK-162, TASK-163, TASK-164, TASK-165, TASK-166, TASK-167, TASK-168, TASK-169, TASK-170, TASK-171, TASK-172, TASK-173, TASK-174, TASK-175

## In-progress notes
- No brief left half-verified. TASK-017 was fully completed (verdict written) before this pause.
- Method used per brief: (1) Read the TASK-*.md file in full; (2) run every "Re-verify these anchors" grep from the Background section plus any additional greps needed to check symbols cited in Steps/Tests/Edge-cases against the real repo at `/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer`; (3) check for cross-task collisions using each workstream's README.md collision/wave table (a task scheduled in a later wave touching the same file/line as an earlier-wave task can find the anchor already changed — this caught TASK-003); (4) check the FATAL checklist (10 items) in VERIFIER-INSTRUCTIONS.md; (5) append one JSON entry per brief to verify-3.json immediately (not batched at the end).
- README.md files already read for context and don't need re-reading: `dryrun/docs/agent-tasks/todo-completion/{web,config,audiobooks}/README.md`.
- audiobooks workstream is now fully done (5/5 examined). Its README.md collision table is worth re-checking against the config workstream's `internal/config/config.go` collision entries for TASK-016..TASK-021, TASK-070 (wave-serialized 1..7) — has not yet been cross-checked for the same kind of subsumption bug found in TASK-003.

## How to resume
1. Read `/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/VERIFIER-INSTRUCTIONS.md` again if context was lost.
2. Read this handoff file.
3. Read `/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/patches/verify-3.json` to confirm the 7 entries already present (do not re-verify TASK-001..005, TASK-016, TASK-017).
4. Continue with TASK-018 in `/private/tmp/claude-501/-Users-jdfalk-repos-github-com-jdfalk-audiobook-organizer/f21a92f9-ff10-4ce5-a715-d13a59db3783/scratchpad/dryrun/docs/agent-tasks/todo-completion/config/TASK-018-fix-ai-backend-local-base-url-hardcoded-develope.md`, then TASK-019, TASK-020, TASK-021, then all 18 web/TASK-*.md files.
5. Append to verify-3.json after every ~5 briefs (use Edit, not Write, to avoid clobbering prior entries — always re-validate with `python3 -c "import json; json.load(open('...verify-3.json'))"` after each edit).
6. When all 29 briefs are done, report final 3-line summary with pass/fail counts as instructed in VERIFIER-INSTRUCTIONS.md.

## Counts so far
COMPLETED: 7 — TASK-001(fail), TASK-002(fail), TASK-003(fail), TASK-004(fail), TASK-005(fail), TASK-016(fail), TASK-017(pass)
REMAINING: 22 — TASK-018, TASK-019, TASK-020, TASK-021 (config); TASK-158, TASK-159, TASK-160, TASK-161, TASK-162, TASK-163, TASK-164, TASK-165, TASK-166, TASK-167, TASK-168, TASK-169, TASK-170, TASK-171, TASK-172, TASK-173, TASK-174, TASK-175 (web)
BLOCKED: 0
