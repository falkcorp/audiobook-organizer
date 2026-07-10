<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-03-exact-explosion-gates-drain-parity.md -->
<!-- version: 1.0.0 -->
<!-- guid: c264f155-355b-454f-9f10-a65a7a8bd660 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Exact-layer explosion #1512: verify/close emission gates, drain-gate parity, drain flag v2 (INIT-2 T3) [⚠ review-critical]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate. — For THIS task that means: code + tests only; the prod drain RUN is TASK-06, do not run any apply against prod here.
**File-ownership:** INIT-2 OWNS all structural edits to `internal/dedup/engine.go` and `internal/database/embedding_store.go`. This task edits `engine.go` — TASK-05 edits the same file and MUST wait for this PR to merge (wave order). Never run concurrently with any INIT-1 engine.go work.

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Sonnet-class · dedup-correctness subagent · **Why:** an over-broad guard silently suppresses real duplicates — needs judgment + coordinator line-review · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-pipeline-hardening-exact-gates-drain-parity" -b agent/dedup-pipeline-hardening-exact-gates-drain-parity origin/main
cd "$REPO/.worktrees/dedup-pipeline-hardening-exact-gates-drain-parity"
git rebase origin/main
```

## Goal

Bound the exact-layer candidate explosion (#1512, ~387k pending rows emitted before the
importer fixes and the Jul-1 guard sweep) at the CODE level: (1) verify every exact-layer
emitter routes through the single chokepoint `upsertExactCandidate` and that its five-gate
chain plus the emission-time `hasPlausibleAudio` gates are intact; (2) close any proven gap
additively AT THE CHOKEPOINT ONLY; (3) make `DrainStaleCandidates`' gate chain exactly mirror
the chokepoint's (gate-for-gate, same order) so the TASK-06 prod drain purges precisely what
today's gates would refuse to emit; (4) bump the drain done-flag v1→v2 so the apply can run.
REUSE the existing gates (`identifiersConflict`, `isBoilerplateTitle`,
`hasKnownShortDuration`, `isPartVsWholeMismatch`, `hasPlausibleAudio`) — do not write new
predicate logic for concepts that already have a named function.

## Background (verify before editing)

- Chokepoint chain (already shipped, verify intact): `upsertExactCandidate(a, b, layer, sim)`
  applies, in order: non-primary-version skip → `identifiersConflict` → `isBoilerplateTitle`
  (either side) → `hasKnownShortDuration` (either side) → `de.isPartVsWholeMismatch`. It
  deliberately has NO AcoustID veto (in-code NOTE explains why — do not add one).
- Pair-level dedupe already exists: `UpsertCandidateNew` (embedding_store.go) canonicalizes
  A/B order and point-reads `dedupPairKey` before insert — the "dedupe emission keys" part of
  the master-plan task is ALREADY DONE at the store; your job is to CONFIRM it with a test,
  not rebuild it. (You may read `internal/database/embedding_store.go` but MUST NOT edit it —
  it belongs to TASK-04's lane.)
- Emission-time gates: `checkExactTitle` and `checkExactISBN`/`checkExactISBNIndexed`/
  `checkExactISBNScan` apply `hasPlausibleAudio` on both sides;
  `CollectExactAcoustID` is INTENTIONALLY ungated (`.github/copilot-instructions.md`
  §"Candidate emission gate"); the `collectors_exact.go` collectors are SCORING-path only and
  intentionally ungated (their doc comments say so) — leave all of that alone.
- Drain twin: `DrainStaleCandidates` (`internal/dedup/drain_stale.go`) re-applies "the SAME
  guard chain upsertExactCandidate applies TODAY" per its package doc, with reason buckets
  (`missing_book`, `identifier_conflict`, `boilerplate_title`, `short_duration`,
  `part_vs_whole`) and soft status `stale-drain`. It was written 2026-07-03 — audit whether
  every CURRENT chokepoint gate (including the non-primary-version skip and any gap you
  close) has a drain twin + reason bucket; add what is missing.
- Done-flag: `drainStaleDoneFlag = "dedup_stale_drain_v1_done"` in
  `internal/plugins/dedup/drain_stale.go`; its doc comment says "Bump to v2 if the drain
  criteria ever change and a re-run is required" — this task changes criteria (parity fixes),
  so bump to `dedup_stale_drain_v2_done`.
- Nil/unknown semantics (spell-out, both here and in tests): all gates are conservative —
  missing identifiers never conflict; nil/≤0 duration is never "short" and never
  part-vs-whole; a missing book in the DRAIN buckets as `missing_book` (would-purge), but at
  EMISSION time a nil book pointer must simply not emit. Do not flip any of these defaults.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  # Edit target: chokepoint (~engine.go:1435, 1 hit)
  grep -n 'func (de \*Engine) upsertExactCandidate' internal/dedup/engine.go
  # Gate helpers to REUSE (>=1 hit each)
  grep -n 'func identifiersConflict\|func isBoilerplateTitle\|func hasKnownShortDuration\|func hasPlausibleAudio\b' internal/dedup/engine.go
  # Emission-time gated emitters (context; edit only if a gap is proven)
  grep -n 'func (de \*Engine) checkExactTitle\|func (de \*Engine) checkExactISBN' internal/dedup/engine.go
  # Edit target: drain gate chain + reason buckets (drain_stale.go:~61,102; >=3 hits)
  grep -n 'CONS-16\|CONS-17\|part_vs_whole\|staleDrainStatus\|func (de \*Engine) DrainStaleCandidates' internal/dedup/drain_stale.go
  # Edit target: done-flag (1 hit)
  grep -n 'drainStaleDoneFlag = ' internal/plugins/dedup/drain_stale.go
  # Store-side pair dedupe to CONFIRM, not edit (>=1 hit)
  grep -n 'func (s \*EmbeddingStore) UpsertCandidateNew\|dedupPairKey' internal/database/embedding_store.go
  ```
  Zero hits on any edit-target grep at execution time = STOP and report.

## Step-by-step

1. Run the anchor greps. Enumerate every caller of `upsertExactCandidate` and every direct
   `UpsertCandidate*` call with layer `"exact"`/`"metadata_hash"` in `internal/dedup/`
   (`grep -rn 'upsertExactCandidate(\|Layer:\s*"exact"\|"metadata_hash"' internal/dedup --include='*.go' | grep -v _test`).
   Any exact-family emitter that bypasses the chokepoint is a proven gap.
2. If a gap exists: reroute that emitter through `upsertExactCandidate` (additive, no
   signature changes). If no gap: record that in the PR description — do NOT invent a guard.
3. Diff the drain's gate chain against the chokepoint's, gate-for-gate and in the same order
   (read `DrainStaleCandidates`' per-candidate evaluation). Add any missing gate twin + a new
   reason-bucket const following the existing `drainReason*` naming, and extend the report
   plumbing (`ReasonCounts`, `Samples`) — it is map-driven, so new buckets flow through.
4. In `internal/plugins/dedup/drain_stale.go`, change `drainStaleDoneFlag` to
   `"dedup_stale_drain_v2_done"` and update its doc comment (v1 precedent line stays).
5. Purely additive elsewhere: do not modify existing gate predicates' semantics, do not touch
   the AcoustID NOTE, do not touch `collectors_exact.go`, do not edit
   `embedding_store.go`, do not restructure the emit()/full-scan section (that is TASK-05's).
6. Tests — extend `internal/dedup/engine_exact_guard_test.go` and the drain tests:
   (a) table-driven PARITY test: for each gate, a pair rejected by the chokepoint is
   would-purge in the drain with the matching reason bucket, and a pair passing all gates is
   kept by both (name: `TestUpsertExactCandidateGateParityWithDrain`);
   (b) anti-over-suppression: a known-good duplicate pair (plausible audio both sides,
   same-ish duration, distinct dirs, no identifier conflict) still EMITS through the
   chokepoint and is KEPT by the drain (name: `TestExactEmitHappyPathSurvives`);
   (c) pair-dedupe confirmation: two `upsertExactCandidate` calls for the same pair produce
   one stored candidate (drive via the store fixture the existing guard tests use);
   (d) nil/unknown conservatism: nil-duration pair still emits (not "short", not
   part-vs-whole).
7. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
8. Run the gate.

## How to test

```bash
make ci
go test -race ./internal/dedup/... -short
```

Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck
to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "dedup_stale_drain_v2_done" internal/plugins/dedup/drain_stale.go` hits (flag bumped)
- [ ] `grep -n "TestUpsertExactCandidateGateParityWithDrain" internal/dedup` hits and the test is green (parity proven)
- [ ] Anti-over-suppression: `TestExactEmitHappyPathSurvives` exists and is green (known-good dup pair still emits AND drain keeps it)
- [ ] Nil-duration pair still emitted (conservative defaults unchanged — test (d) green)
- [ ] PR description states either "no bypass emitters found" or lists the rerouted ones (exact count)
- [ ] Tests green; vet/lint clean on changed files (`make ci` exits 0).
- [ ] File headers bumped on every changed file (`grep -n "last-edited: " <file>` shows 2026-07-10 or later).

## Commit message

```
fix(dedup): drain-gate parity with upsertExactCandidate + drain flag v2 (INIT-2 T3, #1512)

Audits every exact-family emitter against the single chokepoint, mirrors
the current gate chain into DrainStaleCandidates (new reason buckets where
missing), and bumps the drain done-flag to v2 so the gated prod drain
(TASK-06) can re-run against the ~387k stale backlog with current criteria.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-pipeline-hardening-exact-gates-drain-parity
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "dedup_stale_drain_v2_done" internal/plugins/dedup/drain_stale.go` hits AND `grep -n "TestUpsertExactCandidateGateParityWithDrain" internal/dedup -r` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; emission gates return to the pre-audit state and the drain flag returns to v1; no prod data is touched by this task (the drain RUN is TASK-06 and is separately gated).
