<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-17-dedup-auto-resolve.md -->
<!-- version: 1.0.0 -->
<!-- guid: 34655e2f-96ff-4c4d-b888-3479ba4b8eb4 -->
<!-- last-edited: 2026-07-03 -->

# TASK-17 — `dedup.auto-resolve` confidence-tiered op (dry-run, sampling audit, reversible)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus · **Wave:** 4 · **Depends on:** TASK-13 (stale-candidate drain), TASK-15 (bge-m3 recalibration + candidate regeneration), TASK-16 (fingerprint-coverage campaign)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-17-dedup-auto-resolve" -b agent/cr-17-dedup-auto-resolve origin/main
cd "$REPO/.worktrees/cr-17-dedup-auto-resolve"
git rebase origin/main
```

## Read first

Read the full **"Design: Auto-Resolution — Confidence-Tiered Pipeline on the
Existing Unified Score"** section of `docs/consultancy/02-dedup.md` (search for
that exact heading) before writing any code. It defines Tier 0/1/2/3, the
safety rails, and the backlog-drain sequencing that this brief implements
Tier 1 of. Do not skip it — this brief only restates the parts needed to
implement Tier 1; the doc has the full rationale and the steelman on why
fingerprint coverage (not threshold tuning) is the real lever for Tier 2.

## Goal

Add a new op, `dedup.auto-resolve`, that auto-merges **Tier 1 (Band ==
CERTAIN)** dedup candidates through the existing `mergeService.MergeBooks` +
`CleanupCandidatesAfterMerge` path, gated by a hard cap, a global kill switch,
and a mandatory dry-run report. This task builds Tier 1 only — Tier 2 (HIGH,
needs fingerprint corroboration) and Tier 3 (MEDIUM, LLM triage) are follow-on
tasks per the roadmap table, not in scope here.

**This task's acceptance criteria stop at "dry-run report produced."** Do
**not** flip the new `AutoResolveEnabled` kill switch to `true` in any config
default, do not wire `apply=true` into any scheduled/nightly trigger, and do
not run `apply=true` against production data. The owner reviews the dry-run
report and the sampling-audit output and explicitly greenlights the first
capped `apply=true` run out-of-band from this brief.

## Background (verify before editing)

- **Bands already exist and are DB-tunable.** `internal/dedup/unified/score.go`
  defines `BandCertain = "CERTAIN"` (score ≥ 97), `BandHigh`, `BandMedium`,
  `BandReview` as constants, and `internal/dedup/unified/config.go` has
  `SetBandThresholds(certainMin, highMin, mediumMin, reviewMin float64)` for
  DB-persisted overrides. `database.DedupCandidate.Band` (string field,
  `internal/database/embedding_store.go`) is populated by the existing
  scoring pipeline (`unified.ComposeScore`, called from
  `internal/dedup/engine.go` around the `composed := unified.ComposeScore(...)`
  call). `database.CandidateFilter.Band` already supports filtering
  `ListCandidates` by band — use it, don't hand-roll a band comparison.

- **The auto-merge pattern to mirror already exists**: `Engine.ApplyVerdicts`
  in `internal/dedup/engine.go` (search `func (de \*Engine) ApplyVerdicts`) is
  the LLM-verdict analogue of what you're building for Tier 1. Follow its
  shape exactly:
  1. `de.mergeService.MergeBooks([]string{candidate.EntityAID, candidate.EntityBID}, "")` (empty primaryID = auto-pick via `BookIsBetter`);
  2. on success, `de.embedStore.UpdateCandidateStatus(candidate.ID, "merged")`;
  3. tag the survivor via `database.EnsureSingletonBookTag(de.bookStore, result.PrimaryID, "dedup:merge-survivor", "dedup:merge-survivor:auto-certain", "system")` — use the `:auto-certain` suffix (not `:llm-auto`) so this tier's merges are filterable separately from the LLM-verdict auto-merges;
  4. `de.mergeService` may be `nil` (embeddings/merge disabled in this build) — skip with a log line exactly like `ApplyVerdicts` does, don't panic.
  5. Then call `de.CleanupCandidatesAfterMerge([]string{loserID})` (search `func (de \*Engine) CleanupCandidatesAfterMerge`) — `ApplyVerdicts` does NOT currently call this; you must add the call for the new Tier-1 path (it exists and is exercised elsewhere, e.g. the merge HTTP handler — search `CleanupCandidatesAfterMerge(` to see the other call site) so residual pending candidates referencing either merged-away book ID are cleaned up immediately instead of drifting into the stale-candidate backlog TASK-13 exists to drain.

- **Eligibility check (Tier 1 from the design doc), all of which must hold**:
  - `candidate.Band == unified.BandCertain`;
  - `len(candidate.ScoreBreakdown.Suppressors) == 0` (re-verify the field name
    with `grep -n "Suppressors" internal/dedup/unified/score.go` — it lives on
    `UnifiedDedupScore`, which `DedupCandidate.ScoreBreakdown` points to; `nil`
    `ScoreBreakdown` means pre-T015 legacy row — treat as **not eligible**,
    do not merge rows with no breakdown to audit);
  - at least 2 distinct primary signal kinds present in
    `candidate.ScoreBreakdown.Signals` with `Confidence > 0`, from the set
    `{unified.SigExactFile, unified.SigExactAcoustID, unified.SigISBNASIN,
    unified.SigMetaSrcHash}` (re-verify these constant names with
    `grep -n "SigExactFile\|SigExactAcoustID\|SigISBNASIN\|SigMetaSrcHash" internal/dedup/unified/score.go`),
    **OR** the candidate has a `true_dup` labeled example from the
    whole-book-signature oracle: `s.embeddingStore.GetLabeledExample(candidate.ID)`
    returns a non-nil `*database.LabeledExample` with `.Label == "true_dup"`
    and `.LabelReason` containing `"whole-book signatures match"` (this is
    `dataset.wholeBookSignatureMatch`, `internal/dedup/dataset/rules.go:53-58`
    as of this writing — **re-verify the line numbers**, they are cited from
    2026-07-02 and this file has since had at least one version bump; the
    stable anchor is the function name `wholeBookSignatureMatch`, not the line
    number: `grep -n "func wholeBookSignatureMatch" internal/dedup/dataset/rules.go`).
    Do not call `dataset.BuildExample` + `dataset.Classify` yourself here —
    reuse the already-labeled example if `dedup.dataset-backfill apply=true`
    has run (TASK-13 depends on this being current); if `GetLabeledExample`
    returns `nil` (never backfilled), the candidate falls back to the
    signal-kind-count check above, not an automatic pass;
  - `hasPlausibleAudio` (search `func hasPlausibleAudio` in `engine.go`) is
    true for **both** `EntityAID` and `EntityBID` books — fetch via
    `p.store.GetBookByID`;
  - `!identifiersConflict(bookA, bookB)` (search `func identifiersConflict`)
    — this is a second, independent guard beyond the signal-kind check above;
    keep both.

- **Reversibility gap — this is a real prerequisite, not paperwork.** The
  consultancy doc says "`MergeBooks` builds version groups — losers become
  non-primary members, not deletions." That is imprecise: read
  `internal/merge/service.go`'s `MergeBooks` doc comment and body — losers are
  marked `IsPrimaryVersion=false` **and** soft-deleted
  (`MarkedForDeletion=true`, via the per-loser cleanup loop after the
  `ms.db.UpdateBook` calls). There is **no existing unmerge/restore helper**
  anywhere in the codebase (`grep -rn "func.*Unmerge\|func.*RestoreBook" internal/`
  returns nothing). What *does* already exist and is sufficient to build on:
  every `UpdateBook` call performs copy-on-write versioning — it writes the
  **pre-update** book state to `book_ver:<id>:<unix-nano>` before applying the
  new state (`internal/database/pebble_store.go`, inside `UpdateBook`, search
  `versionKey := []byte(fmt.Sprintf("book_ver:`), and there's already
  `GetBookSnapshots(id, limit)`, `GetBookAtVersion(id, ts)`, and
  `RevertBookToVersion(id, ts) (*Book, error)` (search
  `func (p \*PebbleStore) RevertBookToVersion`). Since `MergeBooks` calls
  `ms.db.UpdateBook` for every book in the pair (winner included) before the
  soft-delete step, a full pre-merge snapshot of both sides already lands in
  `book_ver:` automatically — you do not need to add new snapshot-writing
  code. What you must add:
  1. A journal entry per auto-merge, at key `dedup:automerge:<unix-nano>`
     (use the DB's existing generic key/value put — check
     `internal/database/embedding_store.go` or `pebble_store.go` for the
     lowest-friction existing "put arbitrary JSON at a string key" helper,
     e.g. search for how `dedup:label:` keys are written in
     `internal/database/dedup_label.go` and mirror that pattern) containing:
     candidate ID, winner ID, loser ID, the winner's and loser's pre-merge
     `book_ver` timestamp (capture `time.Now()` immediately before calling
     `MergeBooks`, then look up the closest `book_ver:` entry ≤ that timestamp
     via `GetBookSnapshots` — or simpler: call `GetBookSnapshots(id, 1)` for
     each book **immediately after** `MergeBooks` returns, since the just-written
     snapshot is the newest one), and the tag applied.
  2. A small `UnmergeAuto(journalKey string) error` method (naming your
     choice, document it) that reads the journal entry and calls
     `p.store.RevertBookToVersion(id, ts)` for both winner and loser IDs,
     restoring pre-merge `IsPrimaryVersion`/`VersionGroupID`/
     `MarkedForDeletion` state. **Do not wire this into the op's dry-run or
     capped-apply path for this task** — implement it, unit-test it in
     isolation (merge two books, call `UnmergeAuto`, assert both books are
     back to their pre-merge state), and leave it uncalled by
     `dedup.auto-resolve` itself. It exists so the safety-rail requirement
     ("reversibility") is actually buildable before any human clicks apply,
     and so a follow-on task can wire it into an admin "undo merge" endpoint.

- **Safety rails required by the design doc, all must be implemented**:
  1. Dry-run by default (`apply` param defaults to `false`), returning a
     capped sample per outcome bucket — mirror the shape of
     `AcoustIDConflictResult`/`AcoustIDConflictSample` in `engine.go` (search
     `type AcoustIDConflictSample struct`): a result struct with
     `Checked`, `Eligible`, `Merged` (0 when dry-run), `DryRun bool`, and
     `Samples []AutoResolveSample` capped at e.g. 50 entries, each with
     candidate ID, both book IDs/titles, band, and the eligibility reason.
  2. `max_merges` param, default `200`, hard cap on the number of merges
     performed in a single `apply=true` call — stop (not just warn) once hit,
     report how many were skipped due to the cap.
  3. Global kill switch: add `AutoResolveEnabled bool` to `DedupConfig` in
     `internal/config/config.go` (mirror `AutoMergeEnabled`'s doc-comment
     style, default `false`), wire it through `internal/config/persistence.go`
     exactly like `AutoMergeEnabled`/`LLMAutoMergeHighConfidence` (re-verify
     both files' current shape with
     `grep -n "AutoMergeEnabled\|LLMAutoMergeHighConfidence" internal/config/config.go internal/config/persistence.go`
     before editing — do not assume the line numbers above are current). The
     op must check this flag and return an error (not silently no-op) if
     `apply=true` is requested while the flag is `false`; dry-run (`apply=false`)
     must work regardless of the flag so the report can always be generated.

## Step-by-step

1. Re-verify every anchor named above with the grep commands given before
   touching any file. If any anchor has moved or no longer exists, adapt the
   plan to the current code and note the discrepancy in your PR description.
2. `internal/config/config.go` + `internal/config/persistence.go`: add
   `AutoResolveEnabled bool` to `DedupConfig`, default `false`, following the
   exact pattern of `AutoMergeEnabled`.
3. `internal/dedup/engine.go`: add
   `AutoResolveResult`, `AutoResolveSample` types (mirroring
   `AcoustIDConflictResult`/`AcoustIDConflictSample`) and a new method
   `func (de *Engine) AutoResolveCertain(ctx context.Context, apply bool, maxMerges, sampleCap int) (AutoResolveResult, error)` implementing:
   - `de.embedStore.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Band: unified.BandCertain, Limit: 1_000_000})`;
   - for each candidate, run the eligibility check from Background above;
   - dry-run: count eligible, append capped samples, do not merge;
   - apply: perform the merge exactly as in the "auto-merge pattern to mirror"
     section above, write the journal entry, stop at `maxMerges`, report
     `Merged` and `SkippedCap` counts.
4. Add the `UnmergeAuto` helper (Background item above) — in
   `internal/dedup/engine.go` (or a new small file in the same package if
   `engine.go` is getting unwieldy — check current line count with
   `wc -l internal/dedup/engine.go` first) with its own unit test proving a
   merge-then-unmerge round-trip restores both books' pre-merge state.
5. New file `internal/plugins/dedup/auto_resolve.go`: thin op wrapper
   `autoResolveDef() sdk.OperationDef` (`ID: "dedup.auto-resolve"`) with
   JSON params `{apply bool, max_merges int, sample_cap int}` (defaults
   `false`/`200`/`50`), calling `p.engine.AutoResolveCertain(...)`, following
   the exact shape of `internal/plugins/dedup/purge_stale.go` (progress
   updates, `reporter.Logger()` start/end lines, `Isolate: false`,
   reasonable `Timeout`). Return an error immediately if
   `params.Apply && !config.AppConfig.Dedup.AutoResolveEnabled`.
6. Register the op in `internal/plugins/dedup/plugin.go`'s `ops` slice
   (append `p.autoResolveDef()`, with a one-line comment like the existing
   entries).
7. Tests (new `internal/dedup/auto_resolve_test.go` and
   `internal/plugins/dedup/auto_resolve_test.go` as applicable):
   - eligibility check accepts a CERTAIN candidate with 2 primary signals,
     empty Suppressors, plausible audio both sides, no identifier conflict;
   - eligibility check rejects: Band != CERTAIN, non-empty Suppressors,
     `hasPlausibleAudio` false on either side, `identifiersConflict` true,
     `ScoreBreakdown == nil`, and only 1 primary signal kind with no
     `true_dup` labeled example fallback;
   - eligibility check accepts a candidate with only 1 primary signal kind
     but a `GetLabeledExample` returning `Label == "true_dup"` with the
     whole-book-signature reason;
   - dry-run (`apply=false`) never calls `MergeBooks`, returns the expected
     `Checked`/`Eligible` counts and capped samples;
   - apply path merges eligible candidates, respects `max_merges`, calls
     `CleanupCandidatesAfterMerge`, tags the survivor
     `dedup:merge-survivor:auto-certain`, writes a journal entry;
   - apply path returns an error (no merges performed) when
     `AutoResolveEnabled` is `false`;
   - `UnmergeAuto` round-trip test (merge two books, unmerge, assert restored
     state).
8. Bump the file header on every file you touch (version bump + `last-edited`)
   per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/dedup/... ./internal/plugins/dedup/... ./internal/config/... -count=1
go vet ./internal/dedup/... ./internal/plugins/dedup/... ./internal/config/...
```

## Acceptance criteria

- [ ] `dedup.auto-resolve` op registered, dry-run by default (`apply=false`).
- [ ] Dry-run report includes `Checked`, `Eligible`, `DryRun=true`, and a
      capped sample list with per-candidate eligibility reasons — verified by
      test, not just by inspection.
- [ ] Apply path is implemented and unit-tested but is **not** enabled by
      default: `AutoResolveEnabled` defaults to `false`, and `apply=true`
      against a build with the flag `false` returns an error with zero merges.
- [ ] Eligibility logic matches the Background section exactly: Band ==
      CERTAIN, empty Suppressors, ≥2 primary signal kinds OR a `true_dup`
      whole-book-signature labeled example, `hasPlausibleAudio` both sides,
      no `identifiersConflict`.
- [ ] `max_merges` cap is enforced and reported (skipped-due-to-cap count).
- [ ] Survivor tagged `dedup:merge-survivor:auto-certain` (not reusing the
      `:llm-auto` suffix) on every apply-path merge.
- [ ] `CleanupCandidatesAfterMerge` is called after every apply-path merge.
- [ ] A journal entry is written per auto-merge sufficient for `UnmergeAuto`
      to restore both books; `UnmergeAuto` has a passing round-trip test.
- [ ] `go build ./...`, the targeted `go test`, and `go vet` are all green.
- [ ] File headers bumped on every changed file.
- [ ] **Owner-greenlight gate**: this brief's scope ends at a working,
      tested dry-run + sampling-audit report. Do not flip
      `AutoResolveEnabled` to `true` anywhere, do not schedule `apply=true`,
      and do not run `apply=true` against production data — that requires
      explicit owner sign-off after reviewing a dry-run report, handled
      outside this brief.

## Commit message

```
feat(dedup): add dedup.auto-resolve Tier-1 CERTAIN auto-merge op (dry-run default)

Add the confidence-tiered auto-resolution op from the 02-dedup consultancy
design: Tier 1 (Band CERTAIN, corroborated by ≥2 primary signal kinds or a
whole-book-signature true_dup label) auto-merges via the existing
MergeBooks/CleanupCandidatesAfterMerge path, gated by a global kill switch,
a max_merges cap, and a dry-run-first default. Adds an UnmergeAuto helper
built on the existing book_ver CoW snapshots so the reversibility safety
rail is buildable ahead of any human sign-off to enable apply=true.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-17-dedup-auto-resolve
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/plugins/dedup/plugin.go` already registers a `dedup.auto-resolve`
op, this task is done — verify with
`grep -rn "dedup.auto-resolve" internal/plugins/dedup/` and read the existing
implementation against the eligibility/safety-rail checklist above rather than
re-adding it. If `Engine` already has an `AutoResolveCertain`-equivalent
method, confirm it implements the same eligibility checks before concluding
the task is redundant — a same-named stub that skips the Suppressors/
identifiersConflict/hasPlausibleAudio checks is not equivalent and should be
hardened, not left alone.

Rollback = revert the commit. The op is additive (new file, new op
registration, new config field defaulting to its old absent-equivalent
`false`, new Engine method); no existing op, handler, or config default is
modified. `AutoResolveEnabled` defaulting to `false` means reverting is safe
even if the PR merged and was live for a period — no `apply=true` run could
have happened without both the flag being manually flipped and the caller
explicitly passing `apply=true`, which this brief's scope never does.
