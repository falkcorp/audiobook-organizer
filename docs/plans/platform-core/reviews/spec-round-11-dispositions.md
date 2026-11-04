<!-- file: docs/plans/platform-core/reviews/spec-round-11-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: f6b01e7b-a97e-4688-99d0-68fbfef44760 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 11 (narrow verification) — dispositions

Inputs: `spec-round-11-verification.md` (1 BLOCKER, 2 MAJOR) and `spec-round-11-blocker-sweep.md` (0 BLOCKER, 1 MAJOR).
Both files also list notes they did not file. All findings are accepted, and so are the listed notes. 0 rejected.

## Round-11 resolutions (binding)

- **R11-1 One set of HTTP instruments, and gin never sees the mux pattern** (VER11-1; amends R10-3).
  - The legacy wrapper clears `r.Pattern` before it calls gin. The SEC2-22 post-match check receives the matched
    pattern through a request-context value instead (`httpapi.MatchedPattern(ctx)`). otelgin therefore falls back to no
    route for gin's `NoRoute` and for SPA loads, as at HEAD.
  - Core stage 2 records into the **same** OTel instruments that otelgin uses: the same MeterProvider, instrument names
    and units, created once and shared. Two requests with an identical label set then aggregate into one series instead
    of producing a duplicate family. HEAD's `WithoutScopeInfo` stays.
  - The reviewer's scratch-module experiment shows this design gathers cleanly and matches HEAD's golden.
  - `TestLegacySeriesLabelValuesMatchHEAD` and a new `TestNoRouteSeriesSharedNoDuplicate` cover it.
- **R11-2 Bots and the trailer** (VER11-2). Check (d) treats a commit by a dependency or workflow bot (Dependabot,
  Renovate, GitHub Actions) as `Stage-Release: none`. This holds only when the commit touches nothing outside
  dependency manifests, lockfiles and `.github/`; otherwise it is refused. 03 §3.2 item 6 holds.
- **R11-3 No `/api/v1/operations*` route is a core alias** (VER11-3; completes R10-1). 05-tasks drops "every other
  `/api/v1/operations*` route is a core alias". Every legacy operations route stays books' gin route, whose handler calls
  core's tasks client from T0+T1. 09 S5 and the alias counts agree.
- **R11-4 The soak naming covers every stage** (BLK11-1).
  - The release-ID and trailer grammars add `UI0` and `H1`, `H2`, `H3`.
  - The pass criterion "every owner at level 0" becomes "every owner at the levels the release declares
    (`--print-storage-levels`), checked against both the build and the store".
- **R11-5 Notes adopted.**
  - Check (c)'s "backwards value" clause admits the exact-inverse revert that R10-7 allows.
  - 06 uses `set_aside_started` for the reset state.
  - `seed` is not an offline-open user: 09 is corrected to match 05-storage.
  - H1 and H2 require a fresh archive, taken and verified before the cutover, because both delete every pre-cutover
    checkpoint.
  - The two leaf mutexes that the verification reviewer named are added to the rank table.
  - The verification file's other notes are applied where they name a concrete text fix.

After these are applied, one fresh reviewer verifies R11-1 to R11-5 only, including the R11-1 experiment, before Phase 5.
