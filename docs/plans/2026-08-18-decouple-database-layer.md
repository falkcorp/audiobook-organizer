<!-- file: docs/plans/2026-08-18-decouple-database-layer.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f7c1d92-8a4b-4e15-9d63-2c8f0b7a4e61 -->
<!-- last-edited: 2026-08-18 -->

# Decouple the database layer: narrow consumers, then split PebbleStore

Chosen end state (user decision, 2026-08-18): **both** — narrow consumers first so
the seams are measured, then split the concrete type along them.

## Goal

No consumer depends on a method it does not call; `internal/database` is navigable.
Endpoint: `database.Store` (398 methods / 40 embeds) deleted, `PebbleStore`
(559 methods) split into domain repos, every consumer owning a measured interface.

## Why this order

Splitting the interface was already done — `Store` is 40 domain interfaces with a
median of 6 methods. It produced no benefit because consumers kept taking the union.
Consumer narrowing is the necessary step; it also *measures* which methods cluster,
so phase 3's seams are evidence rather than judgment. Precedent that the end state
works: `internal/plugins/maintenance/store_slices.go` already declares narrow
interfaces and compile-asserts `*database.PebbleStore` against them, with no union.

## Phase 1 — narrow consumers (in progress)

36 interfaces whose entire body is `database.*` embeds and declare zero methods of
their own: 24 inline anonymous (parameter position, ~1,257 method-slots) and 12
named pass-throughs (~823). Measured usage where probed runs 3–14.

Method: empty the interface, `go build -gcflags=-e ./...`, read the compiler's
enumeration, declare exactly that. Probe **per package** — Go stops type-checking
packages downstream of a failure, so a blank result for a package that never
compiled is silence, not a clean bill.

Order (leaf-first, so downstream packages enumerate):
1. `internal/undo`, `internal/sweep`
2. `internal/server` (`duplicates_helpers`, `undo_engine`, `maintenance_fixups`)
3. `internal/reconcile`, `internal/scanner`, `internal/activity`, `internal/sysinfo`
4. handlers (`operations`, `metadata`, `playlists`, `collections`)
5. remaining leaves (`deluge`, `writeback`, `transcode`, `search`, `auth`, `aiscan`,
   `metafetch`, `playlist`)

Out of scope: `maintenance.JobStore` (settled by the #2534 arbitration).

## Phase 2 — record the seams

Once consumers are narrow, collect every declared interface and cluster the methods
by co-occurrence. That clustering is the input to phase 3 — not taste.

## Phase 3 — split PebbleStore

Split 559 methods into domain repos along phase 2's clusters. Each repo holds the
shared core (`*pebble.DB`, memdb pointer, the five mutexes, cached aggregates) by
pointer; the split is about ownership and navigability, not about separating state.
Embedding preserves method promotion, so interface satisfaction is unaffected.

## Phase 4 — delete `database.Store`

Once nothing outside `internal/database` names it. Handle `database.MockStore`
(built for the union) and the 19 sites returning/asserting `database.Store`.

## Test strategy

`go build ./...` + `go vet ./...` (vet type-checks `_test.go`, which is how
test-only usage is distinguished from dead) + targeted `go test` per touched
package. `make ci` is not a gate: it cannot pass on `main` (10 pre-existing
staticcheck findings). Mutation-test anything that acts as a gate before calling it
verified.

## Rollback

Every phase-1 change is one file's parameter types; revert the commit. Phase 3 is
the only one touching persistence — it lands behind its own PR with the full test
suite, and reverts cleanly because promotion keeps the external method set identical.
