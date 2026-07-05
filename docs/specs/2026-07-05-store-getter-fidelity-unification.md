<!-- file: docs/specs/2026-07-05-store-getter-fidelity-unification.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9feb8526-a9ba-4e3d-8368-aa1ccef47499 -->
<!-- last-edited: 2026-07-05 -->

# Design Spec — `database.Store` getter fidelity unification (STOREFID)

**Status:** proposed · **Tier:** L · **Author:** plan-op (manual, tooling-blocked) · **Companion plan:** [`docs/plans/2026-07-05-store-getter-fidelity-unification.md`](../plans/2026-07-05-store-getter-fidelity-unification.md)

Plan only — no code changes here. This spec locks the naming scheme, the concrete
types, and the consolidate/split decisions; the plan sequences the migration.

## 1. Problem

`PebbleStore` getters silently return one of **two different payloads** for the same
Go type. Under the production default (`UseMemDB=true`), some getters return the
**full** Pebble row and others return a **memdb projection** with heavy fields nil'd
to save RAM (`stripBookForMemdb`/`stripBookFileForMemdb`, `internal/database/memdb_strip.go`
— cuts the in-memory radix tree ~10 GB → ~2 GB at 392 K books; **the strip must stay**).

The fidelity is **invisible at the call site and un-learnable from the name**:

- `GetAllBooks` is slim, but `GetAllBooksFrom` (same `GetAll…Books` root) is full.
- `GetBookFiles(bookID)` is full, but `GetBookFilesForIDs(ids)` is slim.
- The team already hit this and bolted on an **ad-hoc `_Pebble` "full" suffix** on exactly
  two of ten slim methods (`GetBooksByAuthorID_Pebble`, `GetBooksBySeriesID_Pebble`).

**This has already caused a production bug and a wrong analysis.** `BookSignatureScan`
filtered on the memdb-stripped `Book.BookSigV1` and silently scanned **zero books** — no
error — for an unknown duration (fixed reactively in PR #1830 / `0abc0ed2`). During review
of that fix, a second analysis wrongly claimed `AcoustIDScan` had the same bug (it does
not — it reads fingerprints via the full `GetBookFiles`). Both errors trace to the same
root: **fidelity is a runtime property with no type or name signal.**

## 2. The fidelity model (three tiers, not two)

The audit found the payload space is **three** tiers, which the current single `Book`
type conflates:

| Tier | Fields | Source | Today's type |
|---|---|---|---|
| **Full** | all ~100 `Book` fields | raw Pebble (`GetBookByID`, iterators) | `Book` |
| **Core** (memdb projection) | ~91 — everything **except** the 9 heavy fields | memdb radix tree | `Book` (heavy fields nil) |
| **Summary** | 27 curated list fields | dedicated `book:summary:` projection | `BookSummary` ✅ already distinct |

**Heavy fields stripped from Core** — `Book`: `Description`, `VersionNotes`, `BookSigV1`,
`BookSigV1Mask`, `BookSigSegments`, `BookSigBuiltAt`, `BookSigCoveragePct`, `Author`,
`Series`. `BookFile`: `FingerprintFailureReason/Detail/DiagnosticJSON`,
`AcoustIDFingerprint`, `AcoustIDSeg0..6`. **Deliberately kept in Core** (do not touch):
`FingerprintFailedAt` (LSH builder reads it), `AcoustIDFingerprintDurationSec` (the
memdb-safe "has-fingerprint" proxy).

The bug space is **Full ⇄ Core confusion** — they are the *same type* today, so the
compiler and the reader cannot tell them apart. `Summary` is already safe (distinct type)
and is largely **out of scope** except for naming consistency.

## 3. Inventory (36 book/book-file object getters)

Call-site counts (non-test / test) drive every decision below.

**Core / slim (9)** — delegate to `p.mem()`, return stripped rows:
`GetAllBooks` (79/63), `GetBooksBySeriesID` (22/12), `GetAllBookFiles` (18/2),
`GetBooksByAuthorID` (9/13), `GetBookFilesForIDs` (4/0), `GetFolderDuplicates` (2/3),
`GetDuplicateBooksByMetadata` (1/1), `GetBooksByAuthorIDWithRole` (n/a), `GetBookFilesNeedingDelugeImport` (n/a).

**Full-but-mis-named (3)** — return full data, name hides it:
`GetAllBooksFrom` (2/2, memdb branch **re-hydrates** via `GetBookByID`),
`GetBooksByAuthorID_Pebble` (**0/0**), `GetBooksBySeriesID_Pebble` (**0/0**).

**Full (24)** — raw Pebble, unambiguous by being the only variant: `GetBookByID` (181/191),
`GetBookFiles` (94/70), `GetBookFileByID`, all `GetBookBy<hash|path|pid>`,
`GetBooksByVersionGroup/WorkID/TitleInDir/MetadataSourceHash`, `GetQuarantinedBooks`,
`ListBooksByITunesPID`, `ListSoftDeletedBooks`, `GetDuplicateBooks`, … (full list in the plan).

**Two facts that shape everything:**
1. **The `_Pebble` twins have zero callers** — pure internal fallbacks. Cleaning them up is free and invisible to the public API.
2. **The blast radius is wildly asymmetric.** Renaming the *full* getters (`GetBookByID` 181, `GetBookFiles` 94) is pure churn for zero safety gain — they are already the correct, unambiguous default. The danger — and the only place worth spending churn — is the *slim* getters.

## 4. Locked decisions

**LD-1 — Fidelity is carried by the TYPE, not just the name.** Introduce compiler-enforced
Core types so reading a stripped field off a slim result is a **compile error**, not a
silent runtime nil. Naming alone (rejected as the *sole* fix) documents the trap but does
not prevent re-introducing it. The user explicitly asked for concrete types; this is the
guarantee.

**LD-2 — Core as a distinct type reached via a `Core()` projection; NOT struct embedding.**
Two shapes were weighed:

- **(A) Embedding** — `type Book struct { BookCore; <9 heavy fields> }`. DRY (no field
  duplication) and JSON stays flat, BUT it **breaks every `Book{Title: …}` struct literal**
  that sets a now-promoted field — potentially hundreds of construction sites across tests,
  importers, and write paths, all in one non-splittable struct-redefinition PR. Rejected as
  the primary approach: the blast radius is enormous and orthogonal to the actual bug.

- **(B, CHOSEN) Distinct type + projection** — leave `Book` **exactly as it is** (flat, all
  fields; zero construction-site churn). Add:

  ```go
  // BookCore = the ~91 fields that survive the memdb strip. No heavy fields exist
  // on this type, so reading e.g. .BookSigV1 off a BookCore is a COMPILE ERROR.
  type BookCore struct { ID, Title, AuthorID, …, IsPrimaryVersion, VersionGroupID, … }

  func (b *Book) Core() BookCore { return BookCore{ID: b.ID, Title: b.Title, …} }
  ```

  Slim getters return `[]BookCore`; the store builds them via `.Core()` (or directly from
  the memdb row). **Blast radius is confined to the ~9 slim getters and their callers** — a
  caller that only reads safe fields just swaps the type name; a caller that reads a stripped
  field **fails to compile** (the bug caught). Same shape for `BookFileCore` / `BookFile`.

  **The projection has one trap it must guard against (see §7).** A hand-written `Core()` that
  *forgets to copy a field* silently zeroes it in every slim result — the same silent-drop
  bug class, reintroduced inside the fix. A field-name parity test does NOT catch this. The
  required guard is a **copy-completeness reflection test**: fully-populate a `Book` (every
  field non-zero), call `Core()`, and assert every `BookCore` field equals its `Book`
  counterpart. O(1), catches every dropped/mis-copied field.

- **(C, viable alternative — record the trade-off) memdb stores `BookCore` natively.** The
  partition already maps cleanly onto the layers: memdb *is* the slim layer, Pebble-direct *is*
  full. If the memdb radix tree held `BookCore` instead of a stripped `Book`, then
  `stripBookForMemdb` **becomes** the single `Book→BookCore` constructor (at insertion — the
  one site that already exists and is already tested), slim getters return `[]BookCore` with
  **zero per-read projection copy**, and the copy-completeness risk of (B) collapses into that
  one well-tested conversion. Cost: more churn inside the memdb read methods (`memdb_reads.go`),
  which currently return `Book`. **Chosen (B) over (C) for staging**: (B) is purely additive at
  the type layer (Phase 2 introduces `BookCore` with no memdb-internals change and stays green),
  and (C) can be adopted later as an internal optimization *behind* the same `[]BookCore` API
  without touching callers. If Phase 3 review finds the per-read `.Core()` copy is a measurable
  cost, promote to (C). Recorded so reviewers don't ask "why project on every read?"

`BookCore` carries the same `json` tags as its `Book` fields; for any slim getter surfaced
through the API, the wire format is preserved **provided the heavy fields are `omitempty`**
(else null-vs-absent differs) — verify per API-surfaced getter during migration.

**LD-3 — Every getter's name is unambiguous about fidelity; retire silent bare names.**
Convention: `Get[All]<Entity><Fidelity>[By<Key>|From]`, `Fidelity ∈ {∅ = Full, Core, Summary}`.
- **Full** getters keep the plain name (default = full, complete payload). No `…Full` suffix — it would churn 275+ call sites for zero gain, and "plain = full" is the safe reading.
- **Core** getters get an explicit `Core` in the name **and** return `[]BookCore` — double signal (grep-able name + compiler-enforced type).
- `GetAllBooksFrom` → `GetAllBooksFullFrom` (kills the worst collision; only 2 callers).
- The bare, currently-slim name `GetAllBooks` is **retired** (not silently re-pointed at full — that would be an invisible semantic flip for 79 callers). Callers move to `GetAllBooksCore` (slim, the common case) or the full cursor reader, each an explicit choice.

**LD-4 — `Summary` stays a third tier, untouched except name consistency.** `BookSummary`
is a deliberate 27-field list projection, already a distinct type and already safe. No
merge with Core (Core is 91 fields; Summary is 27 — different purposes).

## 5. Naming scheme + rename table (all getters)

Fidelity suffix: **`Core`** (memdb projection) · **∅** (full) · **`Summary`** (list projection).
Cardinality/key stay as today (`ByID`, `ByAuthorID`, `From`, `All`).

| Old | New | Return type | Fidelity | Notes |
|---|---|---|---|---|
| `GetAllBooks(limit,off)` | `GetAllBooksCore(limit,off)` | `[]BookCore` | Core | 79 callers — largest wave |
| `GetBooksBySeriesID` | `GetBooksBySeriesIDCore` | `[]BookCore` | Core | 22 callers |
| `GetAllBookFiles` | `GetAllBookFilesCore` | `[]BookFileCore` | Core | 18 callers |
| `GetBooksByAuthorID` | `GetBooksByAuthorIDCore` | `[]BookCore` | Core | 9 callers |
| `GetBooksByAuthorIDWithRole` | `GetBooksByAuthorIDWithRoleCore` | `[]BookCore` | Core | |
| `GetBookFilesForIDs` | `GetBookFilesForIDsCore` | `map[string][]BookFileCore` | Core | 4 callers |
| `GetBookFilesNeedingDelugeImport` | `GetBookFilesNeedingDelugeImportCore` | `[]BookFileCore` | Core | |
| `GetDuplicateBooksByMetadata` | `GetDuplicateBooksByMetadataCore` | `[][]BookCore` | Core | |
| `GetFolderDuplicates` | `GetFolderDuplicatesCore` | `[][]BookCore` | Core | |
| `GetAllBooksFrom` | `GetAllBooksFullFrom` | `[]Book` | Full | 2 callers — kills the collision |
| `GetBooksByAuthorID_Pebble` | *(unexport → `getBooksByAuthorIDFull` or inline)* | `[]Book` | Full | **0 callers** — internal only |
| `GetBooksBySeriesID_Pebble` | *(unexport → `getBooksBySeriesIDFull` or inline)* | `[]Book` | Full | **0 callers** — internal only |
| `GetBookByID`, `GetBookFiles`, `GetBookFileByID`, `GetBookBy*`, `GetBooksByVersionGroup/WorkID/TitleInDir/MetadataSourceHash`, `GetQuarantinedBooks`, `ListBooksByITunesPID`, `ListSoftDeletedBooks`, `GetDuplicateBooks` | **unchanged** | `Book`/`[]Book`/`BookFile` | Full | plain = full; no churn |
| `GetAllBookSummaries`, `…Filtered` | unchanged | `[]BookSummary` | Summary | already distinct |
| `GetAllBookSummaries_Pebble` | *(unexport)* | `[]BookSummary` | Summary | internal fallback |

## 6. Consolidate / split analysis

**Consolidate:**
- **`_Pebble` twins → internal, unexported.** `GetBooksByAuthorID_Pebble` /
  `GetBooksBySeriesID_Pebble` / `GetAllBookSummaries_Pebble` have **zero external callers**;
  they exist only as the `UseMemDB=false` fallback inside their public wrappers. Unexport to
  `get…Full` (or inline) — removes 3 misleading public names at zero call-site cost.
- **Do NOT** collapse Core+Full into one method with a `full bool` param. A boolean flag is
  exactly the invisible-fidelity trap in a new coat — the type (`BookCore` vs `Book`) is the
  point. Two names, two types.

**Split:**
- **`GetAllBooksFrom` is doing two jobs** under one name: cursor pagination *and* full-fidelity
  hydration. Rename makes the fidelity explicit (`GetAllBooksFullFrom`); its cursor semantics
  stay. No behavioral split needed — the name split suffices.
- No other getter serves two masters once the Core/Full type split lands.

**Neither consolidate nor split — leave alone:** the 24 unambiguous Full getters and the
`Summary` family. Touching them is churn without safety gain.

## 7. Regression guard

Three layers so this cannot silently come back:
1. **The type system (primary).** Once slim getters return `BookCore`, a caller that needs a
   heavy field *cannot compile* against a Core result — the `BookSignatureScan` bug becomes
   impossible, not just documented.
2. **Copy-completeness reflection test (guards the fix itself).** `TestBookCoreCopiesAllFields`:
   fully-populate a `Book` (every field non-zero via reflection), call `Core()`, assert every
   `BookCore` field equals its `Book` counterpart. Catches a `Core()` that adds a field to the
   struct but forgets to copy it — otherwise a fresh silent-drop bug hiding inside the fix.
   (Under option 4(C) this collapses into testing the single `stripBookForMemdb` constructor.)
3. **A naming lint (secondary).** A CI grep/`go vet`-style check asserting: (a) no exported
   store getter returns `[]Book`/`Book`/`BookFile` while its `PebbleStore` body delegates to
   `p.mem()`; (b) no new `_Pebble`-suffixed exported method. Catches a future getter added on
   the wrong tier before review.

## 8. Non-goals

- Not un-stripping memdb (the strip is load-bearing for RAM).
- Not merging `BookSummary` into Core.
- Not renaming the 24 unambiguous Full getters.
- Not changing JSON/API wire formats (embedding keeps them flat).
- Not touching non-object getters (IDs, counts, tags, stats) — unaffected by the strip.

## 9. Open questions for GATE-2 approval

1. **Type rollout appetite.** LD-2(B) confines churn to the 9 slim getters + their callers
   (no `Book` struct-literal break), so the type change is tractable in one initiative. Still
   worth staging for safety: **Phase 1** = `_Pebble`/`GetAllBooksFrom` cleanup + doc contracts
   (0–2 callers, days) → **Phase 2** = introduce `BookCore`/`BookFileCore` + parity tests →
   **Phase 3** = migrate each slim getter to `…Core`/typed, wave-batched ascending by call
   count (4 → 9 → 18 → 22 → 79) so the pattern is proven small before the 79-caller `GetAllBooks`
   wave. *Recommendation: proceed staged; each phase is independently shippable and green.*
2. **`Core` vs another suffix** (`Lite`, `Projected`, `Slim`). Recommendation: `Core` (it is the
   core/common fields, not a lossy "lite").
3. Whether the naming lint (§7.2) is worth building now or deferred to Phase 2.
