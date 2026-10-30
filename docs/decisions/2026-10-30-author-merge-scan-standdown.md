<!-- file: docs/decisions/2026-10-30-author-merge-scan-standdown.md -->
<!-- version: 1.0.0 -->
<!-- guid: 48b506ab-da77-4d37-952b-54779259781d -->
<!-- last-edited: 2026-10-30 -->

# Author merge holds the library-scan stand-down

## Decision log

- Chosen: the op takes the stand-down ONCE, in `RegisterAuthorMergeOp.Run` and in `runAIScanApply` (helper `Server.holdAuthorMergeStandDown`), and `mergeAuthorsInto` only renews it (`authorMergeSpec.Scan`). The series merge acquires inside `MergeSeriesWithOptions` because that function is the whole loop; here the AI apply calls `mergeAuthorsInto` once per result, and the registry keys a hold by op id, so acquiring inside the loop would release the gate between results and re-park the production scanner for each one. One hold also covers the narrator reclassify, which rewrites `book_authors` outside `mergeAuthorsInto`.
- Chosen: fail closed like the series ops: an empty op id is refused (the gate is keyed by it and the undo rows would be unattributable), and an acquire error (scan will not park) refuses the run before any write. The metadata ops' `HoldScanStandDown` convention (run ungated without an op id) was not used, since the brief and TODO say to mirror the series pattern.
- Chosen: the lease is renewed BEFORE each author's writes (not after, as the series merge does), so no author is ever written without a fresh lease check. A lost lease sets `authorMergeOutcome.Aborted` and stops the loop; `entities.author-merge` still reports the unmerged ids in the `AuthorMergeFailedIDsPrefix` form so the Authors view keeps exactly those variants, and fails even if the ids it did not reach were all already merged. `ai.author-scan-apply` ends the whole run (the remaining results are not attempted) and leaves the result it was on unapplied.
- Chosen: a preview (`dry_run`) of the AI apply does not acquire, like `DedupSeries`: it writes nothing and acquiring parks the production scanner. `entities.author-merge` has no preview.
- Chosen: reuse the existing test seam `Server.scanStandDownGateOverride` (via `Server.scanGate()`), production behaviour is unchanged (`*Server` delegates to the registry and no-ops without one).
- Chosen: `ConcurrencyKey` untouched on both ops (both stay `entities.author-merge`); `library.scan`'s key is not involved.
- Rejected: acquiring inside `mergeAuthorsInto` per call (see above).
- Rejected: `registry.HoldScanStandDown`: it runs ungated without an op id (the fallback the series ops deliberately diverge from) and renews on `UpdateProgress`, which the AI apply calls per result, not per author.
- Test change: `TestAIScanApplyOp_ACancelStopsBetweenResultsAndKeepsWhatMerged` ran with an empty op id; a live apply now refuses that, so the test reporter got an id. It still asserts the cancel.
