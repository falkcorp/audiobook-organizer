<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-192-clamp-composescore-against-per-kind-confidence-b.md -->
<!-- version: 1.0.0 -->
<!-- guid: d26b2ad8-7531-4d8e-9412-e95e4d6ddca8 -->
<!-- last-edited: 2026-08-21 -->

# TASK-192 — Clamp ComposeScore against per-kind confidence bounds; route calibrate-composite Round 2 through a new apply_confidence param (INIT-1 T05)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Why:** owner decision explicitly names Opus tier; touches the core scoring formula (noisy-OR composition) where a clamping-order or precedence mistake silently shifts every dedup candidate's score across the whole corpus · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10512 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**INIT-1 T05 follow-up — per-kind confidence field" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-192-clamp-composescore-against-per-kind-confidence-b" -b agent/dedup-192-clamp-composescore-against-per-kind-confidence-b origin/main
cd "$REPO/.worktrees/dedup-192-clamp-composescore-against-per-kind-confidence-b"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

(a) In internal/dedup/unified/compose.go's ComposeScore, clamp each primary signal's Confidence against its per-kind bound (via the existing confidenceOverride map, guarded by confidenceMu) BEFORE using it in the noisy-OR product at line 67 -- so `s.Confidence` is replaced with `clamp(s.Confidence, override.MinConfidence, override.MaxConfidence)` for kinds with an active override, leaving kinds with no override completely unaffected (bit-identical FormulaVersion behavior for the unconfigured case). (b) In internal/plugins/dedup/calibrate_composite.go, add a new `ApplyConfidence bool json:"apply_confidence"` field to the op's Params struct, independent of the existing `Apply` (bands) field, and add a new `applyConfidenceOverrides(confSuggestions []confSuggestion, log *slog.Logger) error` method mirroring applyBandThresholds's persistence pattern (config.NewUpdateService(p.store).ApplyUpdates with a `dedup.signals.confidence.<kind>.min_confidence`/`max_confidence` payload), gated on `params.ApplyConfidence` exactly the way band writes are gated on `params.Apply` today -- the two apply flags must be independently settable (an operator can apply confidence bounds without touching bands, or vice versa).

## Background (verify before editing)

- Persistence scaffolding for this feature already shipped 2026-07-18: config.DedupSignalConfig.Confidence (internal/config/config.go:241), unified.SetKindConfidenceOverrides (config.go:200, mirrors SetBandThresholds), and registry_wire.go's wiring (L177) all already round-trip a per-kind confidence bound through UpdateConfig/restart -- the ONLY missing piece is (1) ComposeScore actually reading it, and (2) calibrate-composite's Round 2 sweep actually writing it.
- FormulaVersion = "noisy-or-v1" (compose.go:12) exists specifically so a formula change like this one can be detected corpus-wide by a re-score detector -- confirm whether adding a clamp requires bumping this constant (it changes ComposeScore's OUTPUT for any pair with an active per-kind override, even though the algorithm's shape is unchanged) as part of this item's design, and if so, follow whatever the existing re-score-detection consumer expects when FormulaVersion changes.
- applyBandThresholds (calibrate_composite.go:648-675) is the exact pattern to mirror: build a payload map, call config.NewUpdateService(p.store).ApplyUpdates(payload), log the previous values as an explicit rollback record.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'notDup \*= (1.0 - s.Confidence)' internal/dedup/unified/compose.go   # 1 hit, L67 — ComposeScore reads Signal.Confidence with no clamp against per-kind bounds
  grep -n 'does not clamp it against these bounds' internal/config/config.go   # 1 hit, in the DedupSignalConfig.Confidence field doc comment, L235-241 — the confidence-bound override storage (MinConfidence/MaxConfidence) already exists and is documented as unwired into scoring, citing the exact decision this item resolves
  grep -n 'unified.SetKindConfidenceOverrides' internal/server/registry_wire.go   # 1 hit, L177 — SetKindConfidenceOverrides is already wired from config into the unified package at server startup/update time
  grep -n 'ADVISORY only\|Never persisted' internal/plugins/dedup/calibrate_composite.go   # hits at L527-529 confirming Round 2's advisory-only status — calibrate-composite's Round 2 sweep computes suggestions but never persists them -- only the existing single Apply flag gates band-threshold persistence
  grep -n 'config.NewUpdateService(p.store)' internal/plugins/dedup/calibrate_composite.go   # 1 hit, L669, inside applyBandThresholds — the exact persistence pattern to mirror for confidence already exists for bands (config.NewUpdateService(p.store).ApplyUpdates(payload))
  ```

### Reuse — don't invent

- Use `confidenceOverride / confidenceMu package-level state (compose.go is in the same package, can read directly)` in `internal/dedup/unified/config.go` (verify: `grep -n 'confidenceOverride map\[string\]KindConfidenceOverride' internal/dedup/unified/config.go`) — do NOT write a parallel helper.
- Use `applyBandThresholds's config.NewUpdateService(p.store).ApplyUpdates(payload) persistence pattern -- mirror for confidence` in `internal/plugins/dedup/calibrate_composite.go` (verify: `grep -n 'func (p \*Plugin) applyBandThresholds' internal/plugins/dedup/calibrate_composite.go`) — do NOT write a parallel helper.
- Use `DedupKindConfidence{MinConfidence,MaxConfidence} -- the persisted payload shape to write under dedup.signals.confidence.<kind>` in `internal/config/config.go` (verify: `grep -n 'type DedupKindConfidence struct' internal/config/config.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/dedup/unified/compose.go, inside ComposeScore's primary-signal loop (around line 65-68), before computing `notDup *= (1.0 - s.Confidence)`, look up any active override for s.Kind (read confidenceMu.RLock() / confidenceOverride[string(s.Kind)] / RUnlock() -- same package, no export needed) and clamp: `conf := s.Confidence; if ov, ok := confidenceOverride[string(s.Kind)]; ok { if ov.MinConfidence > 0 && conf < ov.MinConfidence { conf = ov.MinConfidence }; if ov.MaxConfidence > 0 && conf > ov.MaxConfidence { conf = ov.MaxConfidence } }` then use `conf` in place of `s.Confidence` in the noisy-OR product. Zero MinConfidence/MaxConfidence means 'not set' per DedupKindConfidence's own doc comment (config.go:246) -- do not clamp against a zero bound.
2. Decide (and document in the PR) whether this clamp addition requires bumping FormulaVersion -- since it changes ComposeScore's numeric output for any signal kind with an active override, even though unconfigured kinds are unaffected byte-for-byte.
3. In internal/plugins/dedup/calibrate_composite.go, add `ApplyConfidence bool `json:"apply_confidence"`` to the Params struct (find it near line 132, alongside the existing `Apply bool `json:"apply"``).
4. Add a new method `func (p *Plugin) applyConfidenceOverrides(suggestions []confSuggestion, log *slog.Logger) error` modeled on applyBandThresholds (L648-675): build a payload of the form `map[string]any{"dedup": map[string]any{"signals": map[string]any{"confidence": map[string]any{ <kind>: map[string]any{"min_confidence": ..., "max_confidence": ...} } }}}` for each suggestion's kind, call `config.NewUpdateService(p.store).ApplyUpdates(payload)`, and log the previous (pre-override) bound per kind as a rollback record the same way applyBandThresholds logs previous_certain_min/previous_high_min.
5. In runCalibrateComposite, after the existing band-apply block (around line 620-641), add an independent block: `if params.ApplyConfidence { if err := p.applyConfidenceOverrides(confSuggestions, log); err != nil { return err } }` -- this must NOT be gated on `allTargetsMet` (the band-apply gate), since confidence-bound suggestions are a separate, independently-reviewable recommendation with their own evidence (CertainRecall/HighRecall per suggestion, already computed by sweepConfidenceAdvisory).
6. Update the op's DisplayName/description string (line ~141-146) to mention the new apply_confidence param alongside the existing apply description.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_192.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A kind with MinConfidence set but MaxConfidence left at zero (or vice versa) must clamp only the bound that is actually set -- confirm both independently, per DedupKindConfidence's documented 'zero means not set' semantics.
- Concurrent ComposeScore calls during a live confidence-override update (SetKindConfidenceOverrides called from a config-update request while dedup scans are running) must not race -- confirm the existing confidenceMu RWMutex is actually held for the read added in this item, not bypassed.
- applyConfidenceOverrides must handle an empty confSuggestions slice (Round 2 sweep found nothing to suggest) as a no-op, not an error.

## Tests

- internal/dedup/unified/compose_test.go: TestComposeScore_ClampsConfidenceAgainstActiveOverride -- set an override via SetKindConfidenceOverrides for one kind with MinConfidence above that kind's raw signal confidence, call ComposeScore, assert the resulting score reflects the CLAMPED (higher) confidence, not the raw one; then clear overrides (SetKindConfidenceOverrides(nil) or equivalent reset) and assert an UNCONFIGURED kind's score is byte-identical to before this change (regression test proving no behavior change for the common unconfigured case).
- internal/plugins/dedup/calibrate_composite_test.go: TestRunCalibrateComposite_ApplyConfidenceIndependentOfApply -- run the op with apply=false, apply_confidence=true and assert confidence overrides ARE persisted while band thresholds are NOT changed; run with apply=true, apply_confidence=false and assert the inverse.

Anti-over-suppression test: `TestComposeScore_ClampsConfidenceAgainstActiveOverride's second half (unconfigured-kind byte-identical regression check) is the anti-suppression test -- proves the clamp addition does not silently change scoring for the overwhelming majority of kinds that have no override configured` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/unified/... ./internal/plugins/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/dedup/unified/... -run TestComposeScore -count=1 -v passes, including the clamp test and the no-override-no-change regression test
- [ ] go test ./internal/plugins/dedup/... -run TestRunCalibrateComposite -count=1 -v passes, including the independent-apply-flags test
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `TestComposeScore_ClampsConfidenceAgainstActiveOverride's second half (unconfigured-kind byte-identical regression check) is the anti-suppression test -- proves the clamp addition does not silently change scoring for the overwhelming majority of kinds that have no override configured` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/unified/... ./internal/plugins/dedup/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_192.md`.

## Commit message

```
refactor(dedup): Clamp ComposeScore against per-kind confidence bounds; route (INIT-1 T05)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this changes the core dedup scoring formula that drives auto-merge/auto-resolve decisions across the whole corpus -- opus tier and careful review per the owner decision's own tier assignment. depends conceptually on todo_line 10512 part 3 (omnibus detection, spec-only, unrelated) not being confused with this -- they share a todo_line but are wholly independent deliverables in different files.
