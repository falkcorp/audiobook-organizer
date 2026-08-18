<!-- file: docs/plans/2026-08-18-interface-decomposition-options.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8b4e1f72-5d93-41a6-b0c8-3e7a92d5f416 -->
<!-- last-edited: 2026-08-18 -->

# Splitting the store up — options, and how to stop it repeating

## What we're actually looking at

| Thing | Size |
|---|---|
| **`PebbleStore` — one concrete struct** | **496 methods** |
| `database.MockStore` (its mock) | 399 methods |
| `database.Store` (the composed interface) | 398 methods / 40 embeds |
| Interfaces declared in `internal/database` | 65 |
| `maintenance.JobStore` | 187 methods / 12 embeds |
| `iface_misc.go` — declarations in one file | 27 |
| Consumer-side interfaces elsewhere in `internal/` | 343 (median 2 methods) |

Widest individual interfaces: `BookReader` 35 · `OpsV2Store` 32 · `OperationStore` 30 ·
`TagStore` 27 · `BookFileStore` 27 · `BookWriter` 16.

> **Correction (measured after this doc's first draft).** `golangci-lint`'s `interfacebloat` at a
> threshold of 8 reports **28 violating interfaces, 12 of them OUTSIDE `internal/database`** —
> including the widest in the repo, `ServerDeps` at **43** methods
> (`internal/plugins/maintenance/deps.go`), plus `EntitiesStore` 30, `AudiobooksStore` 22,
> `LibraryStore` 19, `SystemStore` 17. So "the debt is confined to `internal/database`" is
> **wrong**, and the "343 consumer-side interfaces, median 2" figure below is true but
> misleading: the median hid a fat tail. Option 2 is still the right prevention story; it is not
> evidence that the consumer side is already clean.

**The 496-method struct is the root.** The interfaces are downstream: `Store` is wide because
it mirrors `PebbleStore`, `JobStore` is wide because it composes `Store`'s pieces, and every
mock is wide because it mocks those. Split only the interfaces and the struct keeps generating
pressure to re-pack them.

**One fact that changes the economics:** `PebbleStore`'s 496 methods are already spread across
**46 files, already grouped by domain** — `pebble_store_authors.go` (28), `pebble_store_tags.go`
(27), `pebble_store_bookfiles.go` (24), `pebble_store_ops_v2.go` (32)… The seams we'd want
already exist physically. They're just not expressed in the type system.

---

## Axis 1 — How to split

### Option 1: Split the interfaces by data domain (noun)

`BookReader` 35 → `BookByIDReader` 3, `BookSearchReader` 3, `BookCountReader` 3, … Shared
interfaces stay in `internal/database`, just smaller and more numerous.

- **Effort:** low. One PR per interface, zero consumer changes if the old name is kept as a
  composition of the new pieces (type checker proves the method set is identical).
- **Gets you:** declarations stop being huge. Immediate, visible, safe.
- **Doesn't get you:** any change to *who owns* the interfaces. `internal/database` is still a
  central catalogue, so the next `GetBookByFoo` still gets appended to the nearest interface.
  **Shrinks the symptom, leaves the pressure.**
- **Verdict:** necessary but not sufficient. Good first move, bad only move.

### Option 2: Consumer-defined interfaces (the Go idiom)

Delete the shared catalogue. Each consuming package declares the interface it needs at the point
of use — `organizer.bookStore { GetBookByID; UpdateBook }` — and `internal/database` exports only
concrete types.

- **Effort:** medium, but spread thin — per-package, parallelisable, each verified by the compiler.
- **Gets you:** **structural prevention.** An interface with exactly one consumer can never grow
  past what that consumer uses, and deleting a call site *shrinks* it. This codebase already does
  this **343 times** with a median of 2 methods, so it's not a new convention — it's the existing
  majority idiom being made universal.
- **Costs:** duplication. `OperationsRegistry` is already redeclared in 6 packages,
  `WriteBackEnqueuer` in 8. More mocks (though each is tiny). No single place to "see the API".
- **Verdict:** the strongest answer to "prevent this in future." The duplication is the price,
  and it's the price Go's stdlib pays deliberately.

### Option 3: Split the concrete struct into sub-services ← attacks the 496

`store.Books().GetByID(...)`, `store.Files().Upsert(...)`, `store.Ops().Enqueue(...)`.
`PebbleStore` becomes a thin holder of ~12 focused services of 10–35 methods each.

- **Effort:** highest blast radius (every call site changes shape) — **but far cheaper than it
  looks**, because the 46 domain-grouped files already are the services. Mostly a receiver
  rename per file plus one accessor. Can land incrementally: add `Books()` alongside the existing
  flat methods, migrate callers, then delete the flat method.
- **Gets you:** the only option that fixes **the struct**. 496 → ~12 × ~30. `MockStore`'s 399
  methods decompose the same way. Discoverability goes from "grep 46 files" to "look at Books()".
- **Costs:** the migration is wide and touches everything. Needs to be staged.
- **Verdict:** the real fix for what you're describing. Highest value, highest cost.

### Option 4: Generic capability interfaces (`Getter[T]`, `Lister[T]`)

- **Verdict: I recommend against it.** The signatures aren't uniform enough —
  `GetAllBooksFullFrom`, `GetBooksByTitleInDir`, `GetBookFileByAcoustIDFuzzy` don't reduce to a
  type parameter without inventing a query object, which is a much bigger redesign than the one
  you asked for. Listed for completeness.

### These compose

They're stages of one path, not rivals: **1 → 2 → 3** is a valid order (make declarations small,
push ownership to consumers, then split the struct). So is **3 → 1**, since splitting the struct
makes the interface split fall out. The only genuinely exclusive choice is Option 4 vs. the rest.

---

## Axis 2 — How to prevent the recurrence

Splitting once without a gate means measuring this again in six months. Ranked by
value-per-effort:

### P1: A width ratchet in CI ★ highest value-per-effort

An AST check (`go/types`, not grep — grep undercounts `Store` vs `database.Store` by ~15%):

- no **interface** may declare more than **N** methods (suggest 8)
- no **struct** may declare more than **M** methods (suggest 40)
- existing violations are grandfathered in a checked-in baseline file, and **the baseline can
  only ever shrink**. A PR that widens anything, or adds a new violator, fails.

This is the single mechanism that makes the split permanent. It also means we don't have to
finish the split before we get the benefit — the ratchet locks in each PR's progress.

### P2: Ban new interface declarations inside `internal/database`

Lint fails on `type X interface` added to that package. Forces Option 2 for all *new* code
without requiring the existing 65 to be migrated first. Pairs naturally with P1.

### P3: Ban `misc`-named files and cap declarations per file

`iface_misc.go` holds 27 declarations. A file named `misc` is where wide types go to avoid
review. Cheap, mostly symbolic, but it closes a real hiding place.

### P4: Ban `database.Store` / `maintenance.JobStore` as a parameter type in new code

AST-based. The wide union stays legal where it already is, but no new function can acquire 398
methods by accident. This was phase 2.2 of the existing plan.

### P5: Mock-cost friction

Require per-consumer mocks (mockery `.mockery.yaml` scoped to consumer packages). Makes wide
interfaces annoying to mock rather than illegal. Weak on its own; fine as a side effect of P2.

### P6: Review checklist / CODEOWNERS on `internal/database`

Humans forget. Listed last for honesty — it's what we have now, and it produced a 496-method
struct.

**My recommendation: P1 + P2.** P1 makes progress irreversible; P2 stops new debt at the source.
Both are one CI job and neither blocks on finishing the refactor.

---

## What I'd do, if you want a recommendation

1. **P1 ratchet first**, with the baseline set at today's numbers. One PR, no refactor, and every
   PR after it is protected.
2. **Option 1** across the 6 wide interfaces — fast, zero-risk, visible (PRs of one file each).
3. **Option 3 staged** on the two widest domains first (`bookfiles`, `ops_v2`), to prove the
   accessor pattern before committing to all 12.
4. **Option 2 as the standing rule** for all new consumers, enforced by P2.

`maintenance.JobStore` (187 → per-job interfaces, 37 jobs) rides along with step 2 and is the
biggest single reduction available.

---

## Not in scope here

The `PermissionAware` PR (PR-3 step 1) and the rest of the v1 teardown are tracked in `PLAN.md`
in this worktree and are independent of every option above.
