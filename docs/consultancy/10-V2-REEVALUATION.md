<!-- file: docs/consultancy/10-V2-REEVALUATION.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b8e2f6a-9c1d-4e73-b5a2-7f0d3c8e1a95 -->
<!-- last-edited: 2026-07-03 -->

# Consultancy v2 — Re-evaluation After the 31-Task Roadmap (2026-07-03)

Re-evaluation of the July-2 consultancy (docs 00–06) after executing its roadmap:
**waves 1–5 merged (29 tasks + ~15 auxiliary fixes, PRs #1744–#1789); wave 6 (T30/T31
structural splits) in flight at evaluation time.** Three parallel read-only evaluators
(backend/storage, dedup/matching/process, UI/UX — the UI dimension is new; v1
underweighted it and a 20GB browser leak proved the gap).

Fresh prod facts incorporated: stale-drain applied (12,531 → 3,076 drained, 9,455 kept);
bge-m3 re-embed 100% (3,170 new / 24,785 cached / 16,974 ineligible, 0 errors);
threshold calibration RAN and MISSED precision targets (see §2); 398 descriptions
restored from snapshots (0 BookSigV1 wipes); auto-resolve op shipped kill-switch-off.

## 1. Scorecard vs v1

**Resolved (verified with citations by the evaluators):**
- Storage/arch: STOR-1/QUAL-2 (UpdateBook preserve guard), BUG-1/QUAL-4 (model-gated
  scorer fast-path), ARCH-1 (HNSW staleness vs Pebble truth-count), ARCH-2 (atomic
  export), ARCH-5 (safeDelete), BUG-5/QUAL-6 (permanent-error classes in both retry
  loops), NutsDB wiring cut to Pebble-only (STOR-3/ARCH-3/SYS-3 functional half).
- Dedup/matching: DEDUP-1 (drain executed), DEDUPC-1, DEDUP-6/DEDUPC-7 (0 sig wipes),
  MATCH-1/2/3/5/6/9, TOGGLE-1..5, MATCH-7/8 — the entire matching Tier-0/1 set.
- Process/security: SEC-1/2/5, PROC-1/2, OPS-1/3/4/5/6 all shipped. Do not re-touch.
- BUG-4/QUAL-1 (slog corruption): swept AFTER the backend evaluator's snapshot —
  #1788 fixed 79 sites/41 files, value0 55→0, verb-in-message 4→0 (evaluator verdict
  "still open" is superseded).

**Still open (the residual backlog):**
- CTR-1/CTR-2 (High) — the memdb round-trip CLASS: UpdateBook still whole-row across
  ~149 call sites; no BookListRow projection or UpdateBookFields patch API. Three
  instances patched (#1552, PERF-7, STOR-1); the class remains.
- SYS-1 partial — 4 sibling cache warmers still fire-and-forget (WARMERS-NOT-IN-BGWG).
- BUG-2 partial — safeRun enrolled, but Shutdown's 2s escape hatch persists; add a
  PebbleStore closed-atomic + ErrStoreClosed so residual races degrade to errors.
- DEDUPC-2/3/4/5 cluster (non-canonical emit paths in engine.go) + DEDUPC-6 + DEDUP-5
  (ListCandidates Limit:1_000_000 ×3 sites) + MATCH-4 + DEDUP-4.
- SYS-5 — pebble_store.go grew to 11,473 lines (T30 in flight at eval time).

## 2. Headline new findings

1. **make ci is RED on main (High):** sdkguard fails on TWO transitive imports from
   pkg/plugin/sdk — internal/logger AND internal/dedup/unified; staticcheck has
   **42** findings (38 U1000 dead code + 4 SA1019), not the ~18 previously logged.
   Until green, make ci gates nothing.
2. **Calibration verdict — gold set, not thresholds, is the blocker:** 2,841/5,301
   labeled pairs skipped (stale OpenAI-dim embeddings in gold examples), 520 missing;
   no threshold in 0.80–0.99 met 0.98/0.90 precision. Action: re-run
   dedup.mine-gold-labels now that re-embed is 100%, then recalibrate; if still short,
   the rule-mined labels themselves are the ceiling → add a ~200–300-pair
   human-reviewed stratified sample. Flat thresholds stay meanwhile.
3. **Auto-resolve safety confirmed by construction:** Tier-1 CERTAIN requires ≥2 of
   {exact_file, exact_acoustid, isbn_asin, metadata_hash} — embedding signals are
   excluded, so the calibration failure cannot leak into auto-merge decisions.
4. **NutsDB deletion half outstanding:** nuts_activity_store.go, nuts_metrics_store.go,
   dual_write_activity_store.go have zero non-test callers; go.mod still pins nutsdb
   v1.1.0 (documented goroutine leak). Single janitorial PR after soak.
5. **Data campaigns needed:** 29,083 books never had a Description (scope a
   maintenance.description-backfill via metafetch); 346 books have missing source
   files (tonight's transcribe run) — reconcile or purge.

## 3. UI/UX evaluation (new dimension) — top findings

F1 CRITICAL: no list virtualization anywhere (full DOM render at page sizes up to
1000). F2 CRITICAL→FIXED same night (#1789): itemsPerPage had no upper clamp — a
?limit=44929 URL rendered the whole library (same OOM class as the 20GB
useLibraryCache leak fixed in #1787). F3 HIGH: zero React.memo repo-wide;
Library.tsx has 85 useState — every change re-renders every mounted row. F4 HIGH:
Library.tsx is a 2,075-line god component drilling 40+ props. F5: two parallel SSE
stacks. F6: soft-deleted fetches 10,000 rows unpaginated. F7: four overlapping
activity surfaces + inconsistent page-size options. F8: zero tests on exactly the
god-components (Library, ActivityLog, UnifiedDedupTab, MetadataReviewDialog…).
F9: api.ts is a 5,766-line monolith. F10: ~30 anys at API seams. Accessibility:
mouse-only selection model, 56 aria hits across 137 components — thin.

**Frontend priority order:** clamp (done #1789) → virtualize list/grid →
React.memo rows (+ stabilize callbacks) → tests on the refactor surface →
decompose Library.tsx → single SSE manager → shared PAGE_SIZE_OPTIONS + unified
activity surface → paginate soft-deleted → type the seams → split api.ts.

## 4. Recommended next roadmap (synthesis, priority order)

1. **Green main:** sdkguard (break/allow the 2 transitive imports with justification)
   + staticcheck-42 dead-code sweep (+ NutsDB file/dep deletion folded in).
2. **Frontend perf wave:** virtualization + memoization + Library decomposition
   (UI F1/F3/F4), tests first on the change surface (F8-partial).
3. **Kill the memdb class:** BookListRow projection type + UpdateBookFields patch API
   (CTR-1/CTR-2) — retires the footgun family patched 3× per-instance.
4. **Dedup data campaign:** re-mine gold labels → recalibrate → dataset-backfill +
   FullScan re-band of the 9,455 residual → then owner-gated auto-resolve dry-run;
   fix the DEDUPC-2/3/4/5 emit-path cluster in one small PR first.
5. **Lifecycle invariants:** warmers into bgWG; PebbleStore closed-atomic; sloglint in
   CI to hold the #1788 sweep; description-backfill + missing-source reconciliation
   campaigns.

## 5. Process notes from execution (for the workflow itself)

- Regression class discovered: config-gate assumptions (OpenAIAPIKey checks) broke a
  keyless prod when TASK-10 removed the dummy-key shim — silent plugin-wide
  registration skip, caught only by a human question. Recommendation: startup
  assertion logging WHY each optional subsystem is disabled, at WARN not INFO.
- Test-infra flakes cost ~4 gate-hours until root-caused (5 fixes: memdb warmup ×2,
  SweepTick/dispatcher/reporter shutdown races, opv2 atomicity — a REAL prod race —
  CI 10m timeout). The leaked-registry teardown fix (#1781) also surfaced a latent
  prod nil-deref (deluge ProtectedPathCache).
- Child agents stall on background monitors 4/4 times — child prompts must mandate
  foreground verification.
