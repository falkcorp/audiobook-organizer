<!-- file: docs/plans/2026-08-19-split-the-pebblestore-surface.md -->
<!-- version: 1.2.0 -->
<!-- guid: 7a2e4d19-6c83-4b15-9f07-2d81e5a3c6b4 -->
<!-- last-edited: 2026-08-19 -->

# Splitting the PebbleStore surface

**Status: PROPOSED — awaiting approval. Nothing in here has been executed.**

Written 2026-08-19, immediately after consumer-side narrowing of `database.Store`
finished at 10 references (#2598, main `d151740c`). This plan covers what comes
next and, more importantly, argues that the obvious next step is the wrong one.

## Goal

Make `database.Store` (40 sub-interfaces, ~398 methods) unreachable so it can be
deleted, without a big-bang rewrite of `*PebbleStore` (558 methods, 48 files).

## Measurement first

All figures re-measured on merged main `d151740c`, 2026-08-19.

| thing | count | where |
|---|---|---|
| `database.Store` sub-interfaces | 40 | `internal/database/store.go:18` |
| `database.Store` methods | ~398 | composed |
| `*PebbleStore` methods (non-test) | **558** | 48 files under `internal/database/` |
| `database.Store` consumer refs left | **10** | 6 by design, 3 test helpers, 1 hands-off |
| **`AsPebbleStore` code call sites** | **16** | 9 packages outside `internal/database` |

> **Counting note.** Two filters are load-bearing and both bit me while measuring.
> Strip comment lines (`grep -vE ':[0-9]+:\s*//'`) — the narrowing PRs left a
> "was `database.Store` — 398 methods — until 2026-08-19" note at nearly every
> site, so a raw grep returns ~120 hits. And do **not** filter
> `^internal/database/`: `internal/database/dbtest/invariants.go` holds 2 of the
> 10 real refs. Also beware `server.go:472` (a match inside a `slog.Error`
> *string*) and `indexed_store.go:53` (`database.StoreUnwrapper`, a different
> type).

## Why splitting the struct is NOT the next step

The plan of record (`docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`,
phases 3–4) says "split `PebbleStore`". Having now read the struct, that is not
tractable as written, for a reason that is about state rather than method count.

`PebbleStore`'s fields are genuinely **shared across every domain**, not
partitionable by domain:

- `db *pebble.DB` — one handle, every method uses it.
- `memPtr atomic.Pointer[MemStore]` + `memPending memPendingBuffer` — the warm
  in-memory query layer and its pending-write buffer. Books, authors, series,
  files and ops all read and write-through it.
- `counterMu` (ID allocation), `opsMu` (v2 op CAS), `reviewMu` (review upserts),
  `libraryCountsRecomputeMu`, `primaryCountMu`/`primaryCountRecomputeMu`.
- `libGen cache.Generation` — bumped by book mutations, read by response caches.
- `warmupCancel` / `warmupDone` — a single lifecycle owned by `Close()`.

Split the type into N structs and every one of them needs a pointer back to that
shared core, so you get N facades over one object plus N new indirections — more
code, same coupling, and a `Close()` whose ownership is now ambiguous. The
558-method count is a symptom; the shared mutable core is the actual constraint.

**There is also a blocker that has to be cleared regardless:** 16 sites in 9
packages name `*PebbleStore` concretely. A type that 9 packages depend on by name
cannot be split without touching all of them in the same change. Clearing that
first turns the struct split from one large risky PR into an optional later one.

## The actual next step: split the concrete-type surface into named capabilities

`database.Store` is not the only coupling to `internal/database` — the concrete
type is the other half, and it is the one nothing has attacked yet.

Every one of the 16 `AsPebbleStore` sites wants a *small capability* that
`database.Store` does not declare. The methods they call:

`CountByPrefix`, `WipeByPrefixes`, `KeyCount`, `ClearAllAcoustIDFingerprints`,
plus the warmup/LSH/version-group capabilities already named this week.

This is the **exact pattern already proven three times** in the last two days —
`warmupWaiter` (#2597), `lshCandidateStore` (#2598), `vgBackfiller` — each a
named interface plus a `resolveX` function over `database.AsCapability[T]`, each
with a decorator test. The work is to finish applying it.

### Ordered steps

Each step is one PR, independently revertable, no behaviour change.

> **Corrected 2026-08-19, before executing.** The first draft of this step said
> "9 of 16 sites want the same three methods — one PR." Reading the call sites
> instead of the method names shows **four distinct capabilities**, not one.
> Grouping them would have built exactly the too-wide interface the Risks
> section below names as the top hazard. Method-name overlap is not capability
> overlap; read what each caller invokes.

1. **`prefixWiper`** — `WipeByPrefixes`, `CountByPrefix`. Covers
   `internal/server/maintenance_fixups.go`, **6 sites, one file, one package**.
   The single biggest clean win and the right first PR.
2. **`keyCounter`** — `KeyCount` only, for `handlers/diagnostics.go` (1 site).
   Separate from step 1: `diagnostics.go` never calls the prefix pair, and
   `maintenance_fixups.go` never calls `KeyCount`.
3. **`aggregatesBackfillMarker`** — `IsBookAggregatesBackfillDone`,
   `MarkBookAggregatesBackfillDone`, for
   `maintenance/jobs/recompute_book_aggregates.go` (1 site).
4. **`sweep_pebble_metrics_ttl.go` (1 site) needs a decision, not an interface.**
   It calls `ps.DB()` and hands the raw `*pebble.DB` to
   `database.NewPebbleMetricsStore`. An interface returning `*pebble.DB` moves
   the concrete dependency without removing it. The real fix is for
   `internal/database` to expose a metrics-store constructor that takes the
   store, not the handle — a small change in `internal/database`, not here.
5. **`acoustidFingerprintResetter`** — `ClearAllAcoustIDFingerprints`, for
   `plugins/acoustid/reset_all.go`.
6. **`registry_wire.go` (3 sites) + `activity/register.go` (1)** — read what each
   actually calls and give each its own interface; do not merge them into one
   wide "wiring" interface.
7. **`scanner/process_file.go` + `dedup/lifecycle.go`** — both already converted
   to `AsPebbleStore` this week; swap to their named capability.
8. **Delete `AsPebbleStore`** once the last caller is gone, keeping
   `AsCapability[T]` and `StoreUnwrapper`. This is the step that makes the
   concrete type private to `internal/database`.
9. **Only then** reconsider the struct split.

> **Numbering fixed 2026-08-19.** This list had two steps numbered `4`, which
> made every reference to "step 7" ambiguous. The former second `4` is now `7`
> and the two after it shifted up; the Rollback section below was updated to
> match.

> **Step 8's precondition, restated 2026-08-19 after measuring.** As originally
> written ("no package outside `internal/database` names `*PebbleStore`") the
> gate is unreachable, but its *purpose* — no package outside `internal/database`
> needs the concrete type to do work — is **already satisfied**.
>
> Measured on the `refactor/isbn-index-capability` branch: **36** code
> references to `*database.PebbleStore` outside `internal/database` in non-test
> files, and **36 of 36 are conformance assertions** of the form
> `_ SomeNarrowIface = (*database.PebbleStore)(nil)`, in exactly four files: 17 in
> `maintenance/jobs/store_slices.go`, 15 in `plugins/maintenance/store_slices.go`,
> 3 in `server/wire_abs_routes.go`, 1 in `organizer/service.go`. **Zero** are
> runtime dependencies.
>
> Count comment lines separately or this reads as 73; and note the assertions sit
> inside `var ( ... )` blocks, so a filter requiring `var` on each line matches
> only 2 of the 36 and makes the other 34 look like real dependencies. Both
> mistakes were made while measuring this.
>
> Those assertions are load-bearing drift detectors: each pins that
> `*PebbleStore` still satisfies one narrow consumer interface. They cannot move
> into `internal/database` — the interfaces are declared in packages that already
> import `internal/database`, so asserting there is an import cycle by
> construction.
>
> Consequence for step 8: deleting or unexporting `AsPebbleStore` leaves all 36
> assertions naming the type, so the type stays nameable either way and the step
> buys close to no decoupling. It also costs
> `internal/server/indexed_store_capability_test.go`'s
> `TestIndexedStoreResolvesConcretePebbleStore`, which asserts both that
> `AsPebbleStore` resolves through `indexedStore` **and** that the bare
> `wrapped.(*database.PebbleStore)` form does not — the second half being the
> signal that would tell you when the call sites stop needing the helper.
> **Step 8 is therefore deliberately not executed.** Read that test before
> revisiting.

### What this does NOT do

It does not shrink `database.Store`. The remaining 10 refs are the composition
root, the decorator, two test helpers and the hands-off maintenance lane; they
die only when `database.Store` itself is deleted, which is downstream of step 8.
This plan makes that possible later; it does not do it now.

## Test strategy

- Per PR: a decorator test in the style of
  `internal/server/indexed_store_capability_test.go` /
  `internal/dedup/lsh_candidate_store_capability_test.go` — a capable store, a
  decorator embedding `database.Store` with `Unwrap()`, and a plain store;
  assert resolve-through, and assert nil on the uncapable backend.
- Mutation-test each new resolver before calling it verified (revert the
  resolver to a bare assertion; the decorator test must go red). Commit first —
  `git checkout --` wipes uncommitted work.
- `go build ./...`, `go vet ./...`, `bash scripts/check-interface-width.sh`
  (baseline 1), plus the touched packages' tests. **`make ci` cannot pass on
  main** — 10 pre-existing staticcheck findings, none from this work.
- Local `go test ./internal/database/` takes ~600s on macOS and hits the default
  10m timeout. That is the **APFS temp-filesystem effect**, not a failure: same
  commit measured 532s on a normal `TMPDIR` vs 33.7s on a RAM disk vs 35.5s on
  Linux. Use `-timeout 25m` or a RAM-disk `TMPDIR`; do not "fix" the fixtures.

## Rollback

Every step is additive: a new interface + a new `resolveX` function + swapped
call sites. Reverting one PR restores the `AsPebbleStore` call it replaced, and
nothing else depends on the new interface. No data, schema or wire format is
touched. Step 8 (deleting `AsPebbleStore`) is the only irreversible-feeling one
and is a pure deletion of an unused helper by then — but see the restated
precondition above: it is currently judged not worth doing.

## Risks

- **A capability interface that is too wide re-creates the problem.** Keep each
  to the methods its callers actually invoke; resist grouping by package.
- **A composite assertion takes the worst reachability of its members** — if an
  interface mixes a `database.Store` method with a `*PebbleStore`-only one, the
  pair fails through a decorator even though half of it would have resolved.
  Compile-probe membership (`var _ interface{ M(...) } = (database.Store)(nil)`,
  built with `-gcflags=-e`) rather than grepping for the method name; a grep gave
  a false negative on 3 of 4 methods this week.
- **Do not claim a site is a live production bug without tracing its store.**
  The serviceregistry container holds the **bare** store (`Container.Build` is
  eager and `Override("store", resolvedStore)` is never replaced); only
  `Server.Store()` read after `Start` is wrapped. Four such claims were made on
  2026-08-19 and three were wrong.

## Out of scope / needs a decision

- `plugins/maintenance/deps.go:22` `StoreProvider.Store()` — the hands-off
  missing-file-repair lane. Not to be touched until that lane is resolved.
- `JobStore` → per-job interfaces. The shared `JobStore` was chosen deliberately
  in the #2534 arbitration; not to be reopened unasked.
- **Dedup Tier-0 collectors are dead code** (found while doing #2598, tracked in
  `TODO.md`): `Engine.SetLSHStore` and `Engine.SetAcoustIDBookFileStore` have no
  call sites, both fields are always nil, and all four call sites of
  `CollectLSHAcoustID` / `CollectExactAcoustID` sit behind `if != nil`, so
  neither collector ever runs. Wiring them **changes production dedup behaviour**
  (more candidate pairs, uncalibrated), so it is a decision, not a cleanup.
