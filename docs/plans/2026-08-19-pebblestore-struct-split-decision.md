<!-- file: docs/plans/2026-08-19-pebblestore-struct-split-decision.md -->
<!-- version: 1.2.0 -->
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

Per-method analysis of all **558** `*PebbleStore` methods. Counts below are the
**AST-derived** ones (`go/parser` + `ast.Inspect`, receiver resolved from each
method's own `Recv` name, field reads matched as `*ast.SelectorExpr` on that
receiver) — see "Reproduction" at the bottom for why these supersede the
original brace-matched figures:

| methods | touch | share |
|---:|---|---:|
| 427 | core fields only | 76.5% |
| 117 | no struct fields at all | 21.0% |
| 6 | core **and** domain-local | 1.1% |
| 8 | domain-local only | 1.4% |

**14 of 558 methods (2.5%) touch any domain-local field.** `db` alone is touched
by **408** of 558 (73.1%).

> ⚠️ **This corrects the figure this document originally published (20 / 3.6%).**
> That count classified `libGen` and `counterMu` as domain-local — the very
> assignment the "Corrections" note below *reverses*. The document contradicted
> itself: 20 in the headline, but 10 + 2 + 1 + 1 = **14** in its own prose. 14 is
> right. See "Reproduction".

The 14 land in just four files, and **ten of them are ops-v2**:

| file | methods | field |
|---|---:|---|
| `pebble_store_ops_v2.go` | 10 | `opsMu` (9), `opsLogSeq` (1) |
| `review_store.go` | 2 | `reviewMu` |
| `pebble_store.go` | 1 | `primaryCount*` (`CountPrimaryBooks`) |
| `pebble_store_stats.go` | 1 | `libraryCountsRecomputeMu` (`GetDashboardStats`) |

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

The 14 methods, in full, are the only ones needing any thought:

- `pebble_store.go` — `CountPrimaryBooks`
- `pebble_store_ops_v2.go` — `AppendOpLogsV2`, `BumpDepRev`,
  `IncrementResumeCountV2`, `PromoteToQueued`, `SetOperationV2StatusIfQueued`,
  `UpdateOpCheckpointV2`, `UpdateOpPhaseV2`, `UpdateOpProgressV2`,
  `UpdateOperationV2Params`, `UpdateOperationV2Status`
- `pebble_store_stats.go` — `GetDashboardStats`
- `review_store.go` — `DeleteReviewItem`, `UpsertReviewItem`

Six methods that earlier versions of this list included are **not** here, because
they touch only `libGen` or `counterMu` and both are core: `LibraryGeneration`,
`CreateBook`, `UpdateBook`, `DeleteBook` (`library_generation.go`,
`pebble_store.go`) and `nextID`, `CreateNarrator` (`pebble_store.go`,
`pebble_store_authors.go`). They stay with `core` and never move, which is
already what step 1 assumes when it puts `libGen`, `counterMu` and `nextID` in
the core struct. See "Reproduction".

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
- **The evidence has now been independently reproduced** — see "Reproduction"
  below. This was the one open caveat on the decision and it is closed.
- **`Close()` ownership** is the one genuine design question and it is entirely
  contained in step 1.

## Reproduction

The first version of this document carried the caveat *"one analysis, taken in
one sitting … not independently reproduced. Re-derive before executing step 1."*
That re-derivation has been done, with a **different instrument**: a `go/parser`
AST walk rather than the original regex + brace-extraction. The two share no code.

The AST pass is the stronger instrument and its numbers are the ones in the table
above:

- it takes each method's receiver name from `fn.Recv.List[0].Names[0]`, so the
  `p` / `s` / `ps` variation that broke two earlier passes cannot affect it;
- the parser has already discarded comments and string literals, so a field name
  appearing in either cannot be miscounted;
- `ast.Inspect` descends into nested `func(){…}` literals that capture the
  receiver, which brace-extraction only handles by accident.

### What the re-derivation found

**The structure reproduced; one published number did not.**

Reproduced exactly: `db` at **408**, the `no struct fields` bucket at **117**, and
— once the taxonomy below is fixed — the identity of every domain-touching method,
name for name.

**Corrected: the headline was 20 (3.6%); it is 14 (2.5%).** This was not a
measurement error. Both passes counted correctly; they were asked the wrong
question, because they inherited a field taxonomy that this document had already
superseded in prose but never propagated to its own table. `libGen` and
`counterMu` were counted as domain-local, while the "Corrections" note reverses
exactly that assignment — `libGen` crosses domains and `counterMu` guards the
shared `nextID` allocator, so both are **core**. Six methods moved out of the
domain-local set as a result: `LibraryGeneration`, `CreateBook`, `UpdateBook`,
`DeleteBook` (all `libGen`), and `nextID`, `CreateNarrator` (both `counterMu`).

The internal contradiction was visible on the page the whole time — the headline
said 20, the prose said 10 + 2 + 1 + 1. **A second instrument agreeing with the
first is not verification when both read the same wrong definition.** Re-running
the same question is what confirms; restating the question against the document's
own corrected text is what caught this.

Direction of travel: the corrected figure makes the case *stronger*, not weaker.
Even less state is domain-local than claimed.

### Two robustness checks on the 14

**1. Transitive reach through unexported helpers.** A field-touch census counts
only *direct* reads, so a method that reaches a field through a private helper
looks field-free. There are **62** unexported `*PebbleStore` methods and **55** of
them touch a struct field — enough to matter. Their fields: `db` (49),
`memPending` (5), `memPtr` (3), `UseMemDB` (1), `rootDir` (1), `counterMu` (1).

**Every one of those is core.** The single helper touching a mutex is `nextID()`
→ `counterMu`, which this document already reclassifies as core. **No unexported
helper touches a genuinely domain-local field**, so no amount of helper
indirection can add a method to the 14. That number is robust.

The same is *not* true of the `117 no struct fields` bucket — some of those reach
`db` transitively through one of the 49 `db`-touching helpers. **Treat 117 as an
upper bound on "genuinely field-free," and 427 core-only as a lower bound.**
Neither bounds the decision.

**2. Accessors that hide a field.** `mem()` is a method
(`func (p *PebbleStore) mem() *MemStore { return p.memPtr.Load() }`), not a field,
so a field-level census cannot see `memPtr` behind it and must special-case the
call — the effect that made `memPtr` look single-file wide in an earlier pass.
The obvious worry is that other accessors hide other fields silently.

Enumerated rather than assumed: **exactly two** unexported `*PebbleStore` methods
have a single-return body, and only one of them reads a field. `mem()` is the
only field-hiding accessor on this struct; `metadataStateKey()` builds a key from
its parameters and touches no state. The special case is complete.

**Standing rule for any future census of this struct: count accessors as well as
fields, and re-read the field taxonomy from the current text rather than
inheriting it.** Encapsulation makes a naive field census worse the better-written
the code is.
