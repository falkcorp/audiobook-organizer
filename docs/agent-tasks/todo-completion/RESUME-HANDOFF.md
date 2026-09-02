<!-- file: docs/agent-tasks/todo-completion/RESUME-HANDOFF.md -->
<!-- version: 2.1.0 -->
<!-- guid: 7c1f0f6e-2b7a-4f0e-9a6d-3d1b2c9e8a41 -->
<!-- last-edited: 2026-09-02 -->

# Resume handoff — planning package (paused 2026-08-22 ~00:15 EDT by owner's 20-minute shutdown)

> **Status 2026-09-02 — read this before anything below.** Everything under "FAST-TRACK FIRST" and
> "normal resume" has happened and is history: PR #2682 merged; the fast-track wave shipped as
> TASK-221 (#2690), TASK-222 (#2688), TASK-215 (#2684), TASK-216 (4a95f3696), TASK-218 (#2687) and
> TASK-223 (#2689) — only TASK-217 from that wave is still open; the package then ran through
> 2026-08-23 and went dormant. Reconciled against HEAD on 2026-09-02: **88 of 208 briefs done, 116
> open and still worth doing, 2 superseded, 2 not worth doing**. The authoritative next-steps list
> is the per-workstream status table in
> [`BREAKDOWN-2026-08-21.md` § Reconciliation 2026-09-02](BREAKDOWN-2026-08-21.md#reconciliation-2026-09-02);
> every brief also carries its own `> **Status 2026-09-02:**` line. To resume: pick from the 116
> open briefs (itunes 6/6 and maintenance 15/15 are untouched workstreams; TASK-046 and TASK-086 are
> the two review-critical briefs held on 2026-08-23 that never landed), re-run each brief's
> `grep -n -F` re-verify against HEAD first because TODO.md line numbers below are baseline lines
> of commit 46628240 and have drifted. The "Standing prod facts" section is dated 2026-08-21 and
> is not current.

Branch `plan/todo-master-plan`, draft PR #2682. Package state: **208 briefs, 1017 re-verify greps, 0 audit failures**
(checkpoint 8; scope-21 folded: 208 briefs, 1017 greps). Scratchpad mirrored under `state/scratchpad/`; tools under `tools/`; scout JSON under `scout-json/`;
verifier/judge output under `review/`.

## Verification coverage
- Opus verifier groups 7–10 (160 briefs) COMPLETE and applied (`review/verify-7..10.json`). Sonnet group 4 complete; groups 1–3/5 partial (superseded by 7–10 coverage).
- Correctness / ops-rollback / simplicity judges applied (`state/scratchpad/judges/`). Two package-level findings remain unmatched by design ("ALL review-critical", "ALL-175").
- Generator fixes landed for every systematic verifier finding: test files auto-join `exact_files`; idempotency quotes a presence check; same-line part deps resolve to earlier parts; cycle guard; Go gate never `make ci`.

## 🔴 FAST-TRACK FIRST (owner is blocked on applying metadata in prod)
Live prod incidents 2026-08-21 23:33–23:49, all root-caused, briefs in `scout-json/scope-20.json` (5 objects) and `scout-json/scope-21.json` (5 objects, COMPLETE — NOTE: a `dedupe-book-file-rows` op ALREADY EXISTS at internal/plugins/maintenance/dedupe_book_file_rows.go and its comments claim a prior prod run deleted rows unjournaled — VERIFY before the pending-prod-actions entry; scope text in `state/scratchpad/scopes/scope-21-duplicate-bookfile-rows.md`):
1. **90030** apply pipeline must dedupe `book_file` rows by path — library copy `01KZR9GEH5ZQW9CV1EN130Y7C0` has 42 rows / 21 paths; pipeline wrote every tag twice and raced itself in the rename phase (`stat rename source … no such file`). Sonnet, small.
2. **90033** `internal/operations/registry/registry.go:612-633` `EnqueueOp` ConcurrencyKey dedup returns the running op's id and DROPS the new params — approving more books during a batch apply applies nothing. Opus. Fix: dedupe only on byte-equal params or explicit def opt-in; otherwise queue (Gate 3 in dispatcher.go:107 already serializes runs).
3. scope-20: `ReviewWorkspace.tsx:271` sends `batchFetchCandidates({})` → 400; review list shows empty state during a 35 s `limit=0` query; evidence panel "no recorded derivation" needs cause + re-search; `OperationActivityPanel.tsx:208-228` appends the same SSE line on every progress tick (`op` in deps; use the store's `sequence`).
Order: dispatch 90030 + 90033 + scope-20 items as the first execution wave (own worktrees, PRs, CI green, admin-merge) → `make deploy` (prod restart — tell the owner first; never mid-scan).

## Then: normal resume
1. `cd state/scratchpad`-equivalent: tools are `tools/gen_package.py <scout-all> <out>`, `tools/apply_patches.py <scratchpad>`, `tools/audit_briefs.py <pkg> <repo> --json out`. `tools/task-ids.json` keeps ids stable; `review/applied.json` makes patch application idempotent.
2. Rebase this branch onto main (based on 46628240; TODO.md line refs are BASELINE lines of that commit by design), mark PR #2682 ready.
3. Execute per `BREAKDOWN-2026-08-21.md` waves: ≤4–8 concurrent subagents (iTerm2 dies at 16), Haiku/Sonnet per brief tier, Opus only where the brief says; review-critical PRs stay open for the owner; TODO.md close-out is one coordinator commit per wave; antagonistic passes on Opus.

## Standing prod facts (verified 2026-08-21 23:05–23:40)
`review_apply_enabled=true`, `auto_write_tags_on_apply=true`, `auto_rename_on_apply=true`, `write_back_metadata=false` (not the governing flag for apply); `scheduled.library_scan` every 360 min; prod binary built 2026-08-21 11:07 (`v0.219.1-rc.19-debug`), 2 code commits behind main, none apply-related. With tags written on apply, a later scan re-reads the applied values (no wipe) unless the tag write failed — canary with ffprobe.
