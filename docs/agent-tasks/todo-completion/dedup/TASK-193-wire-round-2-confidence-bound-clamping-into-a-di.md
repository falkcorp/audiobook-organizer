<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-193-wire-round-2-confidence-bound-clamping-into-a-di.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b1a5d03-38b0-4c53-b544-03f2621ff390 -->
<!-- last-edited: 2026-08-21 -->

# TASK-193 — Wire Round-2 confidence-bound clamping into a distinct apply_confidence path; keep the live display score raw (DEC-10)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Why:** Touches the dedup auto-merge threshold gate (auto_resolve.go) and the live scoring call sites feeding it — a wrong clamp changes which pairs get auto-merged in production; the decision's own text calls for review_critical, and the architecture requires moving clamping logic across a package layering boundary (plugins/dedup -> dedup/unified) without duplicating it, since engine.go/rescore.go live in the lower internal/dedup layer and cannot import internal/plugins/dedup. · **Depends on:** none · **Wave:** 7 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 90010 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90010p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-193-wire-round-2-confidence-bound-clamping-into-a-di" -b agent/dedup-193-wire-round-2-confidence-bound-clamping-into-a-di origin/main
cd "$REPO/.worktrees/dedup-193-wire-round-2-confidence-bound-clamping-into-a-di"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extract the confidence-clamping loop that calibrate_composite.go's private scoreWithClamp already implements (L198-215) into an EXPORTED internal/dedup/unified helper (e.g. unified.ClampSignalConfidence(signals, cfg) []Signal), usable both by calibrate_composite.go and by the live production scoring call sites (internal/dedup/engine.go:685,2983 and internal/dedup/rescore.go:276) without duplicating logic across the plugins/dedup -> dedup/unified layering boundary. Add an ApplyConfidence bool json:"apply_confidence" param to calibrateCompositeParams, independent of the existing Apply (bands-only) field, that persists Round-2's recommended per-kind confidence bounds via the same config.NewUpdateService path applyBandThresholds already uses (L648-670) — this is decision 10's 'route Round-2 apply decisions through a SEPARATE apply_confidence value'. The LIVE default unified.ComposeScore call sites must keep producing today's raw, unclamped display Score/Band exactly as now (decision 10's 'the raw composite must remain available'); a NEW additive field (e.g. ClampedBand) on models.UnifiedDedupScore carries the confidence-clamped verdict alongside it, computed at the same call sites using the new unified.ClampSignalConfidence helper. auto_resolve.go's production apply gate (autoResolveEligible, L211-214, currently `if c.Band != unified.BandCertain`) is the one consumer that should honor the clamped verdict once confidence overrides are actually configured for a kind, while falling back to today's raw c.Band behavior byte-for-byte when no overrides are configured (config.go:220-226 already documents that an absent override map is a no-op fallback to compiled-in defaults, which this brief leans on for the zero-config-unchanged guarantee).

## Background (verify before editing)

- ComposeScore (internal/dedup/unified/compose.go:47) already clamps its final Score to [0,100] at L79-82 and this is already tested (compose_test.go:169-189) — that literal part of the decision text does not need new work.
- calibrate_composite.go's own extensive header comment (L14-79 region, citing 'DECISIONS-PENDING.md row 10' at ~L38) explains the REAL gap: ComposeScore reads Signal.Confidence DIRECTLY and ignores cfg.Signals[kind].Min/MaxConfidence, so a config's per-kind confidence bounds have ZERO effect on live scoring; only this op's own private scoreWithClamp (L198-215) simulates the clamp, for calibration replay only.
- config.DedupSignalConfig.Confidence map[string]DedupKindConfidence (config.go:241, DedupKindConfidence at L250-255) already exists as a persistence surface and is already wired into unified.LoadScoreConfig via unified.SetKindConfidenceOverrides + internal/server/registry_wire.go:177/164 (which also wires SetBandThresholds for bands) — so persistence infrastructure is not missing, only (a) a way to WRITE it from Round 2's apply path and (b) any LIVE scoring consumer of it are missing.
- The production auto-merge threshold: internal/dedup/auto_resolve.go's autoResolveEligible (L211) gates Tier-1 auto-resolution on `c.Band != unified.BandCertain` (L214), reading the stored, unclamped Band from database.DedupCandidate.ScoreBreakdown — this is the 'apply decision' the decision text wants routed through a value distinct from the display score.
- unified.UnifiedDedupScore (compose.go's return type) is a type alias for models.UnifiedDedupScore (internal/dedup/unified/score.go:87 -> struct at internal/models/dedup_score.go:44); PebbleDB stores it as JSON, so an additive field needs no formal schema migration — old rows simply read back the zero value for a new field.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "score = 100.0" internal/dedup/unified/compose.go   # 1 hit ~L81 — ComposeScore's Score is already capped at 100
  grep -n "TestComposeScore_CapRespected" internal/dedup/unified/compose_test.go   # 1 hit ~L169 — TestComposeScore_CapRespected already tests the cap
  grep -n "DECISIONS-PENDING.md row 10" internal/plugins/dedup/calibrate_composite.go   # 1 hit ~L38 — calibrate_composite.go cites DECISIONS-PENDING.md row 10 as the open decision this closes
  grep -n "func scoreWithClamp" internal/plugins/dedup/calibrate_composite.go   # 1 hit ~L198 — scoreWithClamp exists as the calibration-only simulation, not wired to persistence or live scoring
  grep -n "SetKindConfidenceOverrides" internal/server/registry_wire.go   # 1 hit ~L177 — config.DedupSignalConfig.Confidence is already a persistence surface, wired into unified.LoadScoreConfig via SetKindConfidenceOverrides
  grep -n "c.Band != unified.BandCertain" internal/dedup/auto_resolve.go   # 1 hit ~L214 — the production auto-merge gate reads the stored Band, not a clamped variant
  grep -n "unified.ComposeScore(" internal/dedup/engine.go internal/dedup/rescore.go   # 3 hits: engine.go:685, engine.go:2983, rescore.go:276 — 3 production call sites feed the live/display score+band from unclamped confidence
  grep -n "type UnifiedDedupScore = models.UnifiedDedupScore" internal/dedup/unified/score.go   # 1 hit ~L87 — UnifiedDedupScore is a type alias for models.UnifiedDedupScore, where a new field would be added
  ```

### Reuse — don't invent

- Use `applyBandThresholds (payload+config.NewUpdateService pattern to mirror for applyConfidenceBounds)` in `internal/plugins/dedup/calibrate_composite.go` (verify: `grep -n "func (p \*Plugin) applyBandThresholds" internal/plugins/dedup/calibrate_composite.go`) — do NOT write a parallel helper.
- Use `scoreWithClamp (confidence-clamp loop to extract into unified package rather than duplicate)` in `internal/plugins/dedup/calibrate_composite.go` (verify: `grep -n "func scoreWithClamp" internal/plugins/dedup/calibrate_composite.go`) — do NOT write a parallel helper.
- Use `config.DedupSignalConfig.Confidence / DedupKindConfidence (persistence types, already exist)` in `internal/config/config.go` (verify: `grep -n "type DedupKindConfidence struct" internal/config/config.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/dedup/unified (new file or added to compose.go), export ClampSignalConfidence(signals []Signal, cfg ScoreConfig) []Signal, reproducing scoreWithClamp's per-primary-signal clamp loop (calibrate_composite.go:198-213: for each primary-kind signal, clamp Confidence to cfg.Signals[kind].{MinConfidence,MaxConfidence} when a per-kind override exists in cfg.Signals; supporting signals and signals with no override pass through unchanged).
2. In internal/plugins/dedup/calibrate_composite.go, rewrite scoreWithClamp's body (L198-215) to call unified.ClampSignalConfidence + unified.ComposeScore instead of its own loop, deleting the duplicated logic. This is a pure refactor — confirm calibrate_composite_test.go's existing scoreWithClamp/scoreAll assertions still pass with byte-identical output.
3. Add ApplyConfidence bool `json:"apply_confidence"` to calibrateCompositeParams (L122-133), documented as independent of Apply (which persists bands only).
4. Add applyConfidenceBounds(suggestions []confSuggestion, log *slog.Logger) error on *Plugin, mirroring applyBandThresholds (L648-670): build a dedup.signals.confidence.<kind>.min_confidence/max_confidence payload from confSuggestions grouped by Kind+Bound (using each suggestion's To value), call config.NewUpdateService(p.store) the same way, log the previous bound values as the rollback record.
5. In runCalibrateComposite (~L620-641), after the existing band-apply block, add a parallel `if params.ApplyConfidence { ... }` block that calls applyConfidenceBounds when confSuggestions is non-empty (log and skip, without erroring, when the sweep produced none). OPEN SUB-DECISION for the executor + reviewer to resolve and justify explicitly in the PR: whether ApplyConfidence should require its own precision/recall floor on confSuggestions before writing, mirroring Round-1's allTargetsMet gate — today Round 2 has no such gate at all (purely advisory); do not silently invent a numeric threshold without stating the reasoning.
6. In internal/models/dedup_score.go, add an additive field to UnifiedDedupScore: `ClampedBand string \`json:"clamped_band,omitempty"\`` (old JSON blobs simply read back "" — no migration needed).
7. In internal/dedup/engine.go at both live composition sites (L685, L2983), after computing `composed := unified.ComposeScore(signals, ..., cfg, pair)` (UNCHANGED — this remains the raw/display value), also compute `clampedComposed := unified.ComposeScore(unified.ClampSignalConfidence(signals, cfg), suppressors, cfg, pair)` and set `composed.ClampedBand = clampedComposed.Band` before persisting.
8. Apply the same two-line addition at internal/dedup/rescore.go:276.
9. In internal/dedup/auto_resolve.go's autoResolveEligible (L211-214), change the CERTAIN-band gate to: when the current cfg has at least one non-default confidence override configured for a kind present in c.ScoreBreakdown.Signals, require c.ScoreBreakdown.ClampedBand == unified.BandCertain instead of (or in addition to) c.Band; when no overrides are configured, the check must remain byte-for-byte identical to today's `c.Band != unified.BandCertain` (lean on config.go:220-226's documented zero-value fallback semantics for the exact condition).
10. Bump version headers on every touched file.
11. Add a changelog fragment under changelog.d/ (no header) describing the new apply_confidence path and the additive ClampedBand field.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_193.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A pair with NO stored ScoreBreakdown (legacy row, nil) must still be rejected the same way it is today (auto_resolve.go:216-218) — the clamped-band computation only ever runs on live-scored rows, never retrofitted onto legacy nil breakdowns.
- A kind absent from cfg.Signals.Confidence must clamp to the SAME range ComposeScore already effectively uses today (DefaultScoreConfig's compiled-in bounds), so its clamped output equals its raw output — no accidental behavior change for unconfigured kinds.
- Multiple confSuggestions for the SAME kind (both a min_confidence and max_confidence bound) must merge into one DedupKindConfidence entry per kind in the persisted payload, not two competing writes.

## Tests

- internal/dedup/unified/compose_test.go: TestClampSignalConfidence_ClampsOnlyPrimaryKindsWithinConfiguredBounds — asserts a primary signal's Confidence outside [Min,Max] is clamped, a supporting signal is untouched, and a kind with no cfg override passes through unchanged.
- internal/plugins/dedup/calibrate_composite_test.go: TestCalibrateComposite_ApplyConfidencePersistsBoundsIndependentlyOfApply — set Apply=false, ApplyConfidence=true, assert UpdateConfig receives the confidence payload and band thresholds are NOT touched.
- internal/dedup/auto_resolve_test.go: TestAutoResolveEligible_UnchangedWhenNoConfidenceOverridesConfigured (anti-over-suppression) — a pair that qualifies for CERTAIN auto-merge under today's baseline config must STILL qualify after this change, with zero confidence overrides configured (proves the default/display path is untouched).
- internal/dedup/auto_resolve_test.go: TestAutoResolveEligible_RespectsClampedBandWhenConfidenceOverridesConfigured — a pair whose raw Band is CERTAIN but whose CLAMPED band drops below CERTAIN (because a configured Max/MinConfidence override tightens a primary signal) must be REJECTED for auto-merge once overrides are configured.

Anti-over-suppression test: `TestAutoResolveEligible_UnchangedWhenNoConfidenceOverridesConfigured — a known-good CERTAIN-band pair must still auto-merge with no confidence overrides configured (the default/no-op state).` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/config/... ./internal/dedup/... ./internal/dedup/unified/... ./internal/models/... ./internal/plugins/dedup/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/dedup/... ./internal/plugins/dedup/... -run 'TestComposeScore|TestClampSignalConfidence|TestCalibrateComposite|TestAutoResolveEligible' -count=1 exits 0.
- [ ] grep -n "apply_confidence" internal/plugins/dedup/calibrate_composite.go returns >=1 hit.
- [ ] go build ./... && go vet ./... exits 0.
- [ ] Anti-over-suppression test: `TestAutoResolveEligible_UnchangedWhenNoConfidenceOverridesConfigured — a known-good CERTAIN-band pair must still auto-merge with no confidence overrides configured (the default/no-op state).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/config/... ./internal/dedup/... ./internal/dedup/unified/... ./internal/models/... ./internal/plugins/dedup/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_193.md`.

## Commit message

```
refactor(dedup): Wire Round-2 confidence-bound clamping into a distinct apply (DEC-10)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

The decision text's literal '[0,100] clamp' is ALREADY implemented and tested in ComposeScore (compose.go:79-82) — that part is stale for the literal score-range interpretation. The real, unimplemented gap — confirmed by calibrate_composite.go's own extensive doc comment citing DECISIONS-PENDING.md row 10 — is that per-kind Signal.Confidence bounds (config.DedupSignalConfig.Confidence, already persistable) have ZERO effect on live scoring. This brief targets THAT gap. One implementation sub-decision (step 5's precision/recall gate for apply_confidence, and step 9's exact fallback condition) is deliberately left for the Opus executor + review_critical reviewer to pin down and justify in the PR, since the owner decision doc does not specify it and inventing a numeric gate here would not be evidence-based.
