# scope-19 handoff — usage limit stop

Output file `scout/scope-19.json` currently contains a valid JSON array with
**4 objects, all DONE**: DEC-6 parts 1-4 (todo_line 90006). Full detail (steps,
tests, acceptance, verified anchors) already written there — do not redo.

## DONE (4/7 objects written to scope-19.json)
- todo_line 90006 DEC-6 part 1 — itunes_error_test.go, version_lifecycle_test.go
- todo_line 90006 DEC-6 part 2 — itunes_integration_test.go, indexed_store_test.go, similar_books_test.go, e2e_workflow_test.go
- todo_line 90006 DEC-6 part 3 — server_coverage_phase2_test.go, deluge_integration_test.go, search_reconciler_test.go, maintenance_window_handlers_test.go, user_tags_authz_test.go, playlist_handlers_test.go, handlers_integration_test.go
- todo_line 90006 DEC-6 part 4 — cover_history_test.go, server_middleware_test.go, ai_jobs_handlers_test.go, entity_tag_handlers_test.go, import_collision_test.go, reading_handlers_test.go, user_handlers_test.go, organize_integration_test.go, server_op_registration_test.go, metadata_handlers_test.go

Key DEC-6 finding baked into those 4 objects: `grep -rn "func newTestServer" internal/server`
returns **0 hits** — the decision's named helper doesn't exist. The real shared
fixture is `setupTestServerWithStore` (internal/server/server_test.go:151, already
reused 275x). Measured 46 real hand-built `NewServer(...)` sites across 23 files
(not exactly "~60" but close), split disjointly into the 4 parts above (12/12/12/10).
`fingerprint_rescan_test.go`'s `newRescanTestServer` is deliberately EXCLUDED (hand-
builds a bare `&Server{store: mockStore}` on purpose — do not migrate it).

## REMAINING (3/7 objects — NOT yet written to scope-19.json)

Research is COMPLETE for all three below — only JSON authoring remains. Anyone
resuming can write these directly from the notes here without re-investigating.

### todo_line 90010 — DEC-10 ComposeScore clamp + apply_confidence
verdict: **actionable**. tier: **opus**, review_critical: **true**.

Key evidence gathered:
- `internal/dedup/unified/compose.go` `ComposeScore` (L47) — its final `.Score`
  ALREADY clamps to [0,100] (L79-82, `if score > 100.0 { score = 100.0 }`),
  tested by `TestComposeScore_CapRespected` (compose_test.go:169-189). **This part
  of the decision text is stale** — do not re-implement it.
- The REAL open gap, per `internal/plugins/dedup/calibrate_composite.go`'s own
  extensive header comment (grep `"DECISIONS-PENDING.md row 10"` → 1 hit ~L38):
  `unified.ComposeScore` reads `Signal.Confidence` DIRECTLY and never clamps it
  against `cfg.Signals[kind].Min/MaxConfidence` — so Round-2 confidence-bound
  calibration is "advisory only" with ZERO effect on live scoring.
- `scoreWithClamp` (calibrate_composite.go:198-215) already implements the clamp
  loop, but ONLY for the calibration simulation (`scoreAll`, used by the sweep),
  never persisted or applied.
- `calibrateCompositeParams` (L122-133) has `Apply bool json:"apply"` (bands only)
  — NO `apply_confidence` field exists yet (grep confirms 0 hits).
- `config.DedupSignalConfig.Confidence map[string]DedupKindConfidence` (config.go:241,
  DedupKindConfidence at L250-255) is ALREADY a working persistence surface, wired
  into `unified.LoadScoreConfig` via `unified.SetKindConfidenceOverrides` +
  `internal/server/registry_wire.go:177`. So persistence infra already exists;
  only "does it get WRITTEN via an apply_confidence toggle" and "does live scoring
  ever CONSUME it" are missing.
- Production live `ComposeScore` call sites (the ones that must NOT change their
  raw/display behavior): `internal/dedup/engine.go:685`, `internal/dedup/engine.go:2983`,
  `internal/dedup/rescore.go:276`.
- The actual auto-merge threshold/apply gate: `internal/dedup/auto_resolve.go:214`
  `if c.Band != unified.BandCertain { return false, "band is not CERTAIN" }` inside
  `autoResolveEligible`.
- `unified.UnifiedDedupScore` is a type ALIAS for `models.UnifiedDedupScore`
  (internal/dedup/unified/score.go:87 → struct defined internal/models/dedup_score.go:44)
  — this is where a new additive field (e.g. `ClampedBand`) would be added; PebbleDB
  stores it as JSON so an additive field needs no formal migration.

Planned goal/steps (drafted, not yet written to JSON):
1. Extract `scoreWithClamp`'s per-primary-signal clamp loop into an EXPORTED
   `unified.ClampSignalConfidence(signals, cfg) []Signal` in `internal/dedup/unified`
   (new code or add to compose.go) so it's usable from BOTH calibrate_composite.go
   AND the lower-layer `internal/dedup` package (engine.go/rescore.go) without
   duplicating logic across the plugins→dedup layering boundary.
2. Rewrite `scoreWithClamp` to call the new exported helper (pure refactor, same
   output — existing calibrate_composite tests must still pass unchanged).
3. Add `ApplyConfidence bool json:"apply_confidence"` to `calibrateCompositeParams`,
   independent of `Apply` (bands).
4. Add `applyConfidenceBounds(...)` mirroring `applyBandThresholds` (L648-670
   pattern: build a `dedup.signals.confidence.<kind>.min_confidence/max_confidence`
   payload, `config.NewUpdateService(p.store)`, log previous values as rollback).
5. Wire `params.ApplyConfidence` into `runCalibrateComposite` (~L620-641) as a
   parallel block to the existing band-apply block. OPEN SUB-DECISION left to the
   Opus executor + reviewer: whether `ApplyConfidence` needs its own
   precision/recall gate mirroring Round-1's `allTargetsMet` (today Round 2 has
   no such gate at all — it's purely advisory) — flag this explicitly in the PR,
   don't silently invent a numeric threshold.
6. Add `ClampedBand string json:"clamped_band,omitempty"` to `models.UnifiedDedupScore`
   (additive).
7. At engine.go:685, engine.go:2983, rescore.go:276: after computing the normal
   `composed := unified.ComposeScore(...)` (UNCHANGED, still the raw/display value),
   ALSO compute `clampedComposed := unified.ComposeScore(unified.ClampSignalConfidence(signals, cfg), ...)`
   and set `composed.ClampedBand = clampedComposed.Band` before persisting.
8. In `auto_resolve.go`'s `autoResolveEligible` (L211-214): when confidence
   overrides ARE configured for a kind (non-default `cfg.Signals[kind]`), gate on
   the clamped band instead of/in addition to the raw `c.Band`; when NO overrides
   are configured, behavior must be byte-for-byte identical to today (config.go's
   own doc comment at L220-226 confirms "existing configs are byte-for-byte
   unchanged" when the map is absent — lean on that for the fallback condition).
9. Bump headers; changelog fragment.

Tests to specify: `TestClampSignalConfidence_ClampsOnlyPrimaryKindsWithinConfiguredBounds`
(unified/compose_test.go), `TestCalibrateComposite_ApplyConfidencePersistsBoundsIndependentlyOfApply`
(plugins/dedup/calibrate_composite_test.go), `TestAutoResolveEligible_UnchangedWhenNoConfidenceOverridesConfigured`
(auto_resolve_test.go — THIS is the anti_over_suppression test: a known-good CERTAIN
pair must still auto-merge with zero overrides configured), `TestAutoResolveEligible_RespectsClampedBandWhenConfidenceOverridesConfigured`.

exact_files: internal/dedup/unified/compose.go, internal/dedup/unified/score.go,
internal/dedup/unified/config.go, internal/models/dedup_score.go, internal/dedup/engine.go,
internal/dedup/rescore.go, internal/dedup/auto_resolve.go, internal/plugins/dedup/calibrate_composite.go,
internal/config/config.go, internal/server/registry_wire.go, plus the 3 test files named above.

gate: `go build ./... && go vet ./... && go test ./internal/dedup/... ./internal/plugins/dedup/... -count=1`
effort: L. domain: internal/dedup.

### todo_line 90011 — DEC-11 generateTargetPath collision counter
verdict: **actionable**. tier: **sonnet**, review_critical: **false**. effort: **S**.

Key evidence:
- The decision's suggested grep (`func generateTargetPath`) returns 0 hits because
  the real thing is a METHOD: `internal/organizer/organizer.go:301` (exported
  `GenerateTargetPath`, passthrough) and `:321` (unexported `generateTargetPath`,
  the actual builder). Verified: `grep -n "func (o \*Organizer) generateTargetPath" internal/organizer/organizer.go` → 1 hit L321.
- No existing counter: `grep -rn "prometheus.NewCounter" internal/organizer` → 0 hits.
- The perfect sibling pattern to copy: `itunesLocationUnmappable` in
  `internal/metrics/metrics.go` (~L109-115) — a detection-only CounterVec, `Register()`
  wiring at L168-176, one-line helper `RecordITunesLocationUnmappable` (L182-184).
- The run-scoped caller that must hold the collision map: `internal/organizer/service.go`
  `organizeBooks` (L943), an 8-worker bounded pool (`numWorkers = 8` at L950) iterating
  `booksToOrganize`; each worker computes `newPath` via `orgSvc.OrganizeOneBook(...)`
  at L1005 (which calls `ReOrganizeInPlace` → `GenerateTargetPath`). Existing
  cross-worker shared-state pattern to copy: `var statsMu sync.Mutex` (L947).

Planned goal/steps (drafted, not yet written to JSON):
1. metrics.go: add `organizeTargetPathCollision = prometheus.NewCounterVec(...)`
   Namespace "audiobook_organizer", Name "organize_target_path_collision_total",
   no labels (avoid unbounded path-cardinality). Register it in `Register()`.
   Add `func RecordOrganizeTargetPathCollision() { ...Inc() }`.
2. service.go organizeBooks: add `var pathMu sync.Mutex; claimedPaths := make(map[string]string, len(booksToOrganize))`
   beside `statsMu`.
3. Right after `newPath, err = orgSvc.OrganizeOneBook(...)` (L1005), when
   `err == nil && newPath != ""`: lock pathMu, check `claimedPaths[newPath]` against
   `book.ID`; if present and different, call `metrics.RecordOrganizeTargetPathCollision()`
   + `log.Warn(...)` with both book IDs and the path; else record the claim; unlock.
   Must run BEFORE the existing oldPath==newPath/alreadyInRoot branching (L1025+)
   and must NOT alter any of those branches.
4. Import internal/metrics into service.go if not already imported.
5. New test file `internal/organizer/target_path_collision_test.go`:
   `TestOrganizeBooks_LogsCollisionWhenTwoBooksShareATargetPath` — two books engineered
   to generate the identical target path but different IDs; assert BOTH still organize
   successfully (no behavior change) AND the counter increments by exactly 1.

exact_files: internal/metrics/metrics.go, internal/organizer/service.go,
internal/organizer/target_path_collision_test.go (new).
gate: `go build ./... && go vet ./... && go test ./internal/organizer/... ./internal/metrics/... -count=1`
anti_over_suppression: N/A (detection-only, no filter added) — but DO include the
happy-path assertion (both books still organize) as the structural equivalent.

### todo_line 90013 — DEC-13 book_file no-bytes categorizing report op
verdict: **actionable** (NOT stale_done). tier: **sonnet**, review_critical: **false**. effort: **S**.

Key finding: **TASK-074 has ZERO overlap** — it builds `maintenance.unknown-author-audit`,
entirely about the "Unknown Author" placeholder baked into organized FilePaths, unrelated
to book_file byte presence. Verified: grepping TASK-074's file body for "book_file"/"bytes
on disk" → 0 hits.

The REAL, partial overlap is with an op that already exists and already covers most of
this: **`maintenance.missing-file-audit`** (`internal/plugins/maintenance/missing_file_audit.go`,
registered plugin.go:62). It is REPORT-ONLY (`Capabilities: []sdk.Capability{sdk.CapLibraryRead}`
only, L279) and ALREADY buckets every book_file row into:
- Missing (`fileMissing`) = "path-missing" ✓ already covered
- Unreadable (`fileUnreadable`) = "unreadable" ✓ already covered, exact term match
- Recoverable (opt-in `Classify:true` pass, L660-748, derived-candidate stat at a nearby
  path) = "sibling-present" ✓ already covered (narrowly, only for the track-slash shape bug)

The ONE bucket decision 13 names that is genuinely MISSING: **"zero-size"** — a row whose
path exists on disk but the file is 0 bytes. The stat sweep (L403-417) only branches on
`os.Stat` error (nil/NotExist/other) and never inspects `info.Size()`, so a truncated/
corrupt 0-byte file is currently silently counted as `filePresent`, indistinguishable
from a healthy file. Verified: `grep -n "\.Size()" internal/plugins/maintenance/missing_file_audit.go` → 0 hits.

Planned goal/steps (drafted, not yet written to JSON): scope the delta ONLY —
1. Add `fileZeroSize` to the `fileExistence` enum (L287-292), inserted after
   `filePresent`.
2. In `auditMissingFiles`' stat sweep (L403-417), capture `info` from `os.Stat` and
   branch: `if info.Size() == 0 { fileZeroSize } else { filePresent }`. Add
   `var zeroSize atomic.Int64` beside the existing counters (L398).
3. Add `ZeroSize int` (and `ZeroSizeSample []string`) to `missingFileReport` (~L216-218),
   populate from the roll-up loop's switch (L452-465, new `case fileZeroSize:` arm).
4. Update `missingFileReport.summary()` (L248-257) and the final `log.Info` in
   `runMissingFileAudit` (L355-359) to surface `zero_size`.
5. Test: `TestMissingFileAudit_SeparatesZeroSizeFromPresent` — 3 rows (real present,
   truncated-to-0-bytes, nonexistent) → assert Present==1, ZeroSize==1, Missing==1,
   with none folded into another bucket.
6. Edge case to spell out: zero-size rows must NOT be fed into the `Classify` pass
   (which only runs over `fileMissing` rows, L481-497) — their bytes ARE present at
   the recorded path, just empty; feeding them in would misreport a truncated file
   as "recoverable via a sibling."
7. No new op registration needed — this extends the already-registered op in place.

exact_files: internal/plugins/maintenance/missing_file_audit.go,
internal/plugins/maintenance/missing_file_audit_test.go.
gate: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... -count=1`

## Next action for whoever resumes
Author 3 more JSON objects (todo_line 90010/90011/90013) into scope-19.json using
the notes above, following the exact schema in SCOUT-INSTRUCTIONS.md and the style
of the 4 already-written DEC-6 objects (full verified_anchors with real grep_cmd/expect
pairs — all the greps above were actually run and returned the stated results).
No further codebase investigation should be needed; this file has everything.
