- [ ] **PERF-REVIEW-WARM** Review page `GET /metadata/cache/review?view=index&all=true` still runs 2.6–4.2 s warm on prod (target < 3 s). The incremental snapshot is already built (metadata_cache_snapshot.go v2.1.0). The slow-listing phase log (snapshot / overlay / prepare / encode) only fires at ≥ 5 s, so the warm phase split on prod is unknown. After perf/slow-page-requests deploys:
  - lower that threshold (or sample the phase timings at 2 s);
  - read which phase dominates;
  - fix that phase.

  Cold builds (64–125 s) happen only in the first ~90 s after a restart. The summary index from that branch removes one of the build's two whole-cache decodes.
- [ ] **PERF-RESTART-CADENCE** Prod restarted 7 times in 7 hours on 2026-10-06. Every restart drops memdb (about 130 s warmup), the review snapshot, the list cache and the metadata-cache summary index, and nearly every request over 60 s in Tempo was in the first minutes after one. Decide whether to batch deploys, and confirm none of the restarts were crashes.
