<!-- file: docs/plans/2026-07-05-store-getter-fidelity-unification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d615a3c-953e-4df7-8682-6899af0a9b25 -->
<!-- last-edited: 2026-07-05 -->

# Implementation Plan — Store getter fidelity unification (STOREFID)

Companion to [`docs/specs/2026-07-05-store-getter-fidelity-unification.md`](../specs/2026-07-05-store-getter-fidelity-unification.md).
**Plan only — no code changes until GATE-2 approval of the spec.** Per-task weak-model-proof
briefs are generated *after* approval (they depend on the locked naming/type decisions).

## Choke points & cross-cutting rules

- Every getter is declared on the `database.Store` interface and implemented in **four**
  places: `PebbleStore`, `MemStore`, `MockStore`, and **mockery-generated mocks**. Every
  rename touches all four.
- **Mockery gotcha (hard rule for every brief):** local mockery is v3.7.1 vs the repo's pinned
  v2.53.6 — regenerate mocks **scoped to the changed interface only**, never repo-wide, and
  hand-verify the diff. An unscoped regen rewrites every mock in the repo.
- **Gate:** GitHub **Minimal CI** (`ci.yml`) is the authoritative per-PR gate — NOT local
  `make ci` (its `staticcheck` step is red on `main` from a pre-existing backlog). Local gate =
  `go build ./... && go vet ./<pkg> && go test -race ./<pkg>`.
- Worktree per wave; version headers mandatory; rebase/FF merges; coordinator owns all git/gh.

## Phases & waves

Ordered so each is independently shippable and green. Sizes are the getter's non-test / test
call counts (the migration weight).

### Phase 1 — Cleanup (no type change, near-zero risk)
- **P1-T1 · unexport the dead `_Pebble` twins.** `GetBooksByAuthorID_Pebble`,
  `GetBooksBySeriesID_Pebble`, `GetAllBookSummaries_Pebble` — **0 external callers**. Unexport
  to `get…Full` (or inline into their wrappers). Removes 3 misleading public names. *(Haiku.)*
- **P1-T2 · rename the mis-named full getter.** `GetAllBooksFrom` → `GetAllBooksFullFrom`
  (2 callers, incl. `Engine.getAllPrimaryBooksWithFullFields`). *(Haiku.)*
- **P1-T3 · doc-contract the 9 slim getters.** Add a doc comment to each listing the exact
  stripped fields + "route through the Full variant / GetBookByID if you need them." Pure
  comments. *(Haiku.)*

### Phase 2 — Types foundation (additive, trivially green)
- **P2-T1 · introduce `BookCore` + `Book.Core()` + TWO tests:** (1) field-name parity
  (`fields(Book) − fields(BookCore) == the 9 heavy names`); (2) **copy-completeness reflection
  test** (`TestBookCoreCopiesAllFields`: populate every `Book` field non-zero, call `Core()`,
  assert every `BookCore` field is copied) — this one guards against a `Core()` that forgets a
  field, which parity alone misses. Purely additive — nothing returns `BookCore` yet. *(Opus —
  get the field partition exactly right; both tests lock it.)*
- **P2-T2 · introduce `BookFileCore` + `BookFile.Core()` + the same two tests** (heavy set:
  `FingerprintFailure*`, `AcoustIDFingerprint`, `AcoustIDSeg0..6`; keep `FingerprintFailedAt`,
  `AcoustIDFingerprintDurationSec`). *(Opus.)*

> **HARD CHECKPOINT after Phase 2 — STOP and re-evaluate before Phase 3.** Phases 1–2 are
> independently green and near-zero-risk, and together with the P4 lint + doc contracts they
> capture most of the *bug-prevention* value. Phase 3 is where cost and merge risk concentrate:
> 6 waves that all serialize on the shared `Store` interface + mock file (the `engine.go`-chain
> pattern, 6 deep), culminating in the 79-caller `GetAllBooks` wave. Do not run the tail on
> autopilot — reassess appetite (and whether option 4(C) should replace per-read projection)
> at this checkpoint.

### Phase 3 — Per-getter migration (wave-batched ascending by call count)
Each wave: rename getter → `…Core` on the interface + PebbleStore + MemStore + MockStore,
change return type to `[]…Core`, regen the mock scoped, then **fix that getter's callers**.
Ascending order proves the pattern on 4-caller getters before the 79-caller one.

| Wave | Getter → new | callers | tier |
|---|---|---|---|
| P3-W1 | `GetBookFilesForIDs` → `…Core` (`map[string][]BookFileCore`) | 4 | Sonnet |
| P3-W2 | `GetBooksByAuthorID[WithRole]` → `…Core` | 9 | Sonnet |
| P3-W3 | `GetAllBookFiles` → `…Core` | 18 | Sonnet |
| P3-W4 | `GetBooksBySeriesID` → `…Core` | 22 | Sonnet |
| P3-W5 | `GetAllBooks` → `GetAllBooksCore` | **79** | Sonnet (⚠ largest; may split by package) |
| P3-W6 | `GetDuplicateBooksByMetadata`, `GetFolderDuplicates`, `GetBookFilesNeedingDelugeImport` → `…Core` | 1+2+n | Haiku |

### Phase 4 — Regression guard
- **P4-T1 · naming lint.** CI test asserting: no exported `Store` getter returns `Book`/`[]Book`/
  `BookFile` while its `PebbleStore` body delegates to `p.mem()`; no new exported `_Pebble`
  method. *(Sonnet.)*

## Mechanical vs judgment (the real review work)

- **Mechanical (compiler-driven):** the renames and, at each call site, swapping `Book`→`BookCore`
  in the type. `go build` enumerates every site; most are one-token changes.
- **Judgment (⚠ where latent bugs hide):** at each migrated call site, ask *does this caller
  need a heavy field — and is it a hot path?* If it needs a heavy field it must be **re-routed
  to the Full getter** (e.g. `GetBookByID` / `GetAllBooksFullFrom`), not just retyped — a
  `BookCore` that needed a heavy field is a `BookSignatureScan`-class bug. **But** the full bulk
  path (`GetAllBooksFullFrom`) is N `GetBookByID` point-reads: fine for a batch scan, a latent
  regression on a hot request path — so a rerouted *hot* site needs a targeted read (fetch only
  the ids it needs), not a full-library hydration. **This audit is the point of the whole
  initiative**; every brief must call it out per call site, and the compile error makes each one
  impossible to skip.

## Dependency graph

```
P1-T1 ─┐
P1-T2 ─┼─(independent, any order)
P1-T3 ─┘
P2-T1 ── P2-T2   (types; must precede all of Phase 3)
        └────────> P3-W1 → W2 → W3 → W4 → W5 → W6   (serialize: all touch the Store interface
                                                       + mock; sibling-rebase between waves)
P3-* ────────────> P4-T1   (lint after the getters are typed)
```
Phase 1 can run concurrently with Phase 2. Phase 3 waves **serialize** (shared interface + mock
file → same-file collisions), each rebasing on the prior merge — same discipline as the
concurrency sweep's `engine.go` chain.

## Test strategy

- P2 parity tests lock the field partition (drift → red).
- Each P3 wave: `go test -race ./<changed pkgs>` + the getter's existing tests still pass
  (renamed). Add one test per Core getter asserting the returned `BookCore` has the safe fields
  populated (guards against a bad `.Core()` projection dropping a surviving field).
- P4 lint runs in CI on every future PR.

## Rollback

- Phase 1: revert the single rename commit (mechanical).
- Phase 2: `BookCore` is additive with no callers — deleting it is a clean revert.
- Each Phase 3 wave: independent revert (rename back + retype); no data/schema touched — this is
  a pure API-surface refactor, zero persisted-state risk.

## Out of scope (per spec §8)

Not un-stripping memdb; not merging `BookSummary`; not renaming the 24 unambiguous Full getters;
no JSON/API wire changes; non-object getters (IDs/counts/tags/stats) untouched.

## Estimate

~11 tasks across 4 phases. Phase 1 (3× Haiku) + Phase 2 (2× Opus) are days. Phase 3 is the bulk
(~132 call-site edits over 6 serialized waves, mostly mechanical with per-site judgment). Full
package = design spec (this) + plan (this) + 11 weak-model-proof briefs — briefs generated on
GATE-2 approval.
