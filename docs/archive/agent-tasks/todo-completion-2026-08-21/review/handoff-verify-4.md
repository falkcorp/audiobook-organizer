<!-- file: docs/agent-tasks/todo-completion/review/handoff-verify-4.md -->
<!-- version: 1.0.0 -->
<!-- guid: a232313b-1c33-4902-a8de-5685f48bea4b -->
<!-- last-edited: 2026-08-22 -->

# Handoff — verify-4 (group 4: server-handlers, docs, scanner, search)

Stopped on usage-limit STOP directive. `patches/verify-4.json` contains a complete,
valid-JSON verdict for **all 32 briefs** in my assigned workstreams (server-handlers=16,
docs=12, scanner=2, search=2). Nothing is missing from the required output — this handoff
is for context/audit trail, not resumption of unexamined briefs.

## Method used
1. Loaded `dryrun/docs/agent-tasks/todo-completion/skeleton.json`, extracted the 32 task
   objects for ws in {server-handlers, docs, scanner, search} into `work/TASK-*.json`.
2. Ran every `verified_anchors[].grep_cmd` from the skeleton against the real repo
   (`work/anchor_results.json`, 82 greps) — this covered check 2 for all 32 tasks in one pass.
3. Cross-checked `tier` vs `tier_label` for all 32 (`work/build_verify4.py` embeds the
   command) — found a systemic mismatch.
4. Checked the `## Idempotency / Rollback` section template against each task's actual
   polarity/goal for all 32 (grep across all files).
5. Full manual read of: TASK-142, TASK-150, TASK-057, TASK-059, TASK-051/052/053 (docs
   README + all three briefs), TASK-183, TASK-146, TASK-182.
6. Lighter review (anchors + idempotency-template check only, no full manual read of
   Steps/exact_files prose) for: TASK-054, 055, 056, 058, 060, 123, 124, 125, 126, 143,
   144, 145, 147, 148, 149, 151, 152, 153, 154, 155, 156, 157.

## Verdict summary
- **20 fail**, **12 pass** (see verify-4.json for the full per-brief findings).

## Key findings (all with concrete grep/read evidence in verify-4.json)

1. **Same-file collision not caught by the wave matrix** (TASK-051/052/053, docs): all
   three list `exact_files: ["docs/api/openapi.json"]`, all Wave 1, `Depends on: none`,
   yet `docs/README.md` claims "No same-file collisions inside this workstream." Three
   parallel weak models would edit the same JSON `paths` object with no serialization.

2. **Systemic Idempotency-template mismatch** (12 tasks: TASK-054, 056, 058, 060, 124,
   143, 144, 146, 147, 148, 151, 152): all render the "the symbol already lives at its
   NEW location and is absent from the old one" *relocation* template, but none of these
   tasks relocate a symbol — they edit a value/comment/fixture/doc in place. Confirmed by
   full read of TASK-146 (rate-limit literal→derived-constant swap, same file same line).
   No concrete grep exists for "old location" in any of these, so the idempotency check
   as written is unrunnable.

3. **Tier/tier_label mismatch on all 4 review-critical tasks** (TASK-057, 142, 150, 157):
   skeleton `tier` field says `"sonnet"` but `tier_label` (what the rendered brief header
   and README both show) says `"Opus-class"`. Verified via a one-line python json load
   printing both fields for all four ids. If a future brief regen trusts `tier` over
   `tier_label`, a review-critical prod-data task silently downgrades to Sonnet.

4. **Idempotency boilerplate applied to non-mutating docs/audit tasks** (TASK-057, 150):
   both are pure documentation/audit deliverables (Tests: "N/A — documentation only" /
   "N/A for the audit itself"; TASK-150 step 6 explicitly says "do not fix it inline
   here") yet both carry the full "apply=false dry-run / CreateOperationChange journal /
   byte-identical undo-fixture test / refuse while library.scan running" mandatory
   boilerplate meant for real data-mutation endpoints. None of those 4 mandatory items
   correspond to any Acceptance checkbox in either brief.

5. **TASK-059 (docs, close-out task) — 4 separate fatals**:
   - Step 1 says "In TODO.md, replace the L10706-10709 bullet body..." which directly
     contradicts the brief's own standard "Do NOT edit TODO.md" boilerplate later in
     the same file.
   - `exact_files` is `[]` in the skeleton, but the brief requires creating a new
     `todo.d/` fragment (and, per step 1, editing TODO.md) — neither is listed.
   - The closure note claims R-9 is "verified resolved or moot" alongside 6 other
     sub-items, but R-9 has zero grep evidence anywhere in the brief/skeleton
     (verified_anchors has exactly 7 entries: DEAD-1, CTX-4, LOG-5, R-10, DEP-1a-d,
     DEP-1e, TEST-2 — no R-9).
   - The DEP-1e re-verify grep (`grep -n 'ITunesPath: b.ITunesPath\|ITunesPath:
     c.ITunesPath' internal/database/bookcore.go`) returns 0 hits at HEAD when run
     literally (verified directly) — actual source has gofmt-aligned multi-space
     formatting at L207/L321, so the exact-space grep is a false negative. The brief's
     inline claim of "1 hit + 2 hits" is also wrong (actual is 2 hits in store.go, 0 in
     bookcore.go with the given pattern, 2 with a corrected pattern).

6. **TASK-142**: step 3 adds a real behavior-changing guard (limit=0 now defaults to a
   cap of 50 instead of "unlimited" per the existing doc comment) but both Tests and
   Acceptance mark "Anti-over-suppression: N/A" — no test covers the previously-legal
   limit=0/large-journal case under the new default.

## Not yet done (would strengthen but weren't required to complete the deliverable)
- Full manual read of Steps/exact_files sections for: TASK-054, 055, 056, 058, 060, 123,
  124, 125, 126, 143, 144, 145, 147, 148, 149, 151, 152, 153, 154, 155, 156. These got
  anchor-verification + idempotency-template checks only; a deeper pass might surface
  additional check-3/6/9 findings I didn't have time to hunt for.
- No exhaustive check of scope-NN.json references (they don't exist anywhere in the
  dryrun package — every brief cites a nonexistent `scope-XX.json` file — I judged this
  out of scope for the FATAL checklist since it's packaging metadata, not a repo-code
  claim, but flagging it here in case another group's findings corroborate it as
  systemic and worth escalating).

## Rule compliance
- (a) No `stale_done` verdict_override used — N/A.
- (b) No 172.16.x.x addresses or tokens written anywhere in verify-4.json or here.
- (c) Did not flag `make ci` being red on main (per rule c).
- (d) `task_id` in every entry matches the `TASK-NNN` id in the brief filename.
