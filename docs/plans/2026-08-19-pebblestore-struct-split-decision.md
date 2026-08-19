<!-- file: docs/plans/2026-08-19-pebblestore-struct-split-decision.md -->
<!-- version: 1.0.0 -->
<!-- guid: f85db4dd-286a-4361-8bdb-1189236f1fd4 -->
<!-- last-edited: 2026-08-19 -->

# Splitting the PebbleStore struct — the decision, costed

**Status: PROPOSED — awaiting approval. Nothing here has been executed.**

This is the plan the "Plan Before Execution" rule requires before a refactor of
this size. It exists so the question can be answered yes or no on evidence
rather than left open.

Companion to [`2026-08-19-split-the-pebblestore-surface.md`](2026-08-19-split-the-pebblestore-surface.md),
whose "why splitting the struct is NOT the next step" section was measured in
#2607 and found to argue from a false premise. This document supplies the
per-method evidence that document explicitly said it lacked.

## The headline, and it is not what either of us expected

**There is almost no shared mutable state to fight over — because there is
almost no domain-local state at all.**

Per-method analysis of all **558** `*PebbleStore` methods (receiver name resolved
per method, body extracted by brace matching, comments stripped):

| methods | touch | share |
|---:|---|---:|
| 420 | core fields only | 75.3% |
| 118 | no struct fields at all | 21.1% |
| 11 | core **and** domain-local | 2.0% |
| 9 | domain-local only | 1.6% |

**20 of 558 methods (3.6%) touch any domain-local field.** `db` alone is touched
by **407** of 558 (72.9%).

## What that changes

The original objection was "split the type and every piece needs a pointer back
to the shared core, so you get N facades over one object." That is **confirmed,
more strongly than when it was a guess** — but it now means something different
from what it was offered to mean.

It was offered as *this is dangerous, the state is entangled*. The measurement
says the opposite: **the state is not entangled, because the domains barely have
any.** 96.4% of methods need nothing but `db` and the memdb layer. There are no
lock-ordering hazards to design around, no ownership disputes, no risk of two
domain structs racing on a field — because the fields aren't shared between
domains, they're shared between *every* method and the core.

So the split is **mechanically low-risk and high-churn**: it is method
relocation, not state partitioning.

### The corollary, which is the actual decision

If every domain struct embeds `*core` and 96.4% of its methods touch only core
fields, then **the split does not decouple state. It decomposes a method set.**

That is a real benefit — a nameable 30-method `catalogStore` is easier to reason
about, test and mock than a 558-method type — but it is a *legibility* benefit,
not an *isolation* one. Whether it is worth 558 method relocations across 48
files against a production system is a judgement call, and it is the user's.

**This plan does not recommend proceeding.** It makes the trade visible.

## The state, correctly assigned

Re-derived from usage, not from the declaration:

**Core (shared by construction — 7 fields):**
`db`, `memPtr`, `memPending`, `warmupCancel`, `warmupDone`, `UseMemDB`, `rootDir`

**Also core, though the file-level pass suggested otherwise:**

- `libGen` — bumped by `CreateBook`/`UpdateBook`/`DeleteBook` (catalog writes)
  and read by `LibraryGeneration` (cache generation). It **crosses domains**;
  putting it in a catalog struct would force the cache reader to reach into it.
- `counterMu` — guards `nextID`, the shared ID allocator, and is also taken by
  `CreateNarrator`. Shared allocation is core by definition.

**Genuinely domain-local — the entire list:**

| lock / field | owner | methods |
|---|---|---:|
| `opsMu`, `opsLogSeq` | ops-v2 | 10 |
| `reviewMu` | review | 2 |
| `primaryCount` ×5 | `CountPrimaryBooks` | 1 |
| `libraryCountsRecomputeMu` | `GetDashboardStats` | 1 |

The 20 methods, in full, are the only ones needing any thought:

- `library_generation.go` — `LibraryGeneration`
- `pebble_store.go` — `CountPrimaryBooks`, `CreateBook`, `DeleteBook`,
  `UpdateBook`, `nextID`
- `pebble_store_authors.go` — `CreateNarrator`
- `pebble_store_ops_v2.go` — `AppendOpLogsV2`, `BumpDepRev`,
  `IncrementResumeCountV2`, `PromoteToQueued`, `SetOperationV2StatusIfQueued`,
  `UpdateOpCheckpointV2`, `UpdateOpPhaseV2`, `UpdateOpProgressV2`,
  `UpdateOperationV2Params`, `UpdateOperationV2Status`
- `pebble_store_stats.go` — `GetDashboardStats`
- `review_store.go` — `DeleteReviewItem`, `UpsertReviewItem`

## If approved: ordered steps

Each step is one PR, independently revertable, no behaviour change.

1. **Extract `core`** — a struct holding the 9 core fields (7 above plus
   `libGen`, `counterMu`), with `mem()`, `nextID` and the warmup lifecycle as
   its methods. `PebbleStore` embeds `*core`. **Zero methods move.** This is the
   whole risk of the project concentrated into one small, reviewable diff: if
   `Close()` ownership or warmup lifecycle is going to break, it breaks here.
2. **Move ops-v2** — 10 methods plus `opsMu`/`opsLogSeq` into an `opsStore`
   embedding `*core`. Chosen first among domains because it owns the largest
   private lock set, so it is the one real test of whether domain-owned locking
   survives the move.
3. **Move review** — 2 methods plus `reviewMu`. Same shape, trivially.
4. **Move the field-free domains** — tags (35), auth (33), operations (28),
   playback (18), playlists (15), externalids (11), metadata (10) and the rest.
   These touch `db` or nothing, so each is a mechanical relocation. One PR per
   domain, largest first.
5. **Re-home `CountPrimaryBooks` and `GetDashboardStats`** with their private
   counters into whichever domain claims them. Do this LAST — they are one
   method each and settle nothing.
6. **Stop.** Re-measure `database.Store`'s reachability and decide whether the
   remaining consolidation is worth further PRs. Do not commit to finishing at
   the outset.

## Test strategy

- `scripts/verify_interface_split.py` must report the method set IDENTICAL
  after every step — the type checker plus that script are what prove no method
  was dropped or re-signatured.
- `make mocks` must regenerate byte-identically, as a second independent
  instrument, at every step.
- `go test ./internal/database/ -timeout 25m` — **~600s on macOS**; that is the
  APFS temp-filesystem effect (532s normal `TMPDIR` vs 33.7s RAM disk vs 35.5s
  Linux), not a regression. Do not "fix" the fixtures.
- `go build ./...` + `go vet ./...` + `scripts/check-interface-width.sh`.
  **`make ci` cannot pass on main** — 10 pre-existing staticcheck findings.
- Step 1 additionally needs a `Close()`/warmup lifecycle test, because that is
  the one thing embedding could silently change.

## Rollback

Steps 2–6 are method relocations: revert the PR, the methods return. Step 1 is
the only structural change, and it is additive (a new embedded struct, no
signature changes), so reverting it restores the flat struct.

No data, schema, or wire format is touched at any step.

## Risks

- **The benefit is legibility, not isolation.** See the corollary above. If the
  goal is to stop packages depending on wide types, that was already achieved by
  the consumer-narrowing sweep (172 → 10 refs) and does not require this.
- **Churn is the cost, and it is large**: 558 method relocations across 48 files
  in a repo with concurrent work, where every rebase conflict lands in a file
  that just had every method moved.
- **The evidence here is one analysis, taken in one sitting.** Two earlier
  passes at simpler versions of these counts were wrong (18/48 from a
  `[a-z]{1,3}` receiver pattern; 61 files from an unanchored `.db` grep). This
  pass resolves the receiver per method and matches on brace-extracted bodies,
  which is why its numbers differ — but it has not been independently
  reproduced. **Re-derive before executing step 1.**
- **`Close()` ownership** is the one genuine design question and it is entirely
  contained in step 1.
