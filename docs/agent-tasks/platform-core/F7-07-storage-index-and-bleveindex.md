<!-- file: docs/agent-tasks/platform-core/F7-07-storage-index-and-bleveindex.md -->
<!-- version: 1.0.0 -->
<!-- guid: be316d76-1be5-4eb1-a49b-5c4e28885509 -->
<!-- last-edited: 2026-11-05 -->

# F7-07 — storage/index and internal/bleveindex, dark: the full-text index contract, the native dirty set, DerivedIndex generations

| Field | Value |
|---|---|
| ID | F7-07 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T3** (Opus / high): an index contract with generations; a falsely accepted warm-start artifact serves stale search and dedup answers |
| Wave | W28 (beside F4-07, F5-13, F7-02, F9-02; file-disjoint) |
| Depends on | F2-05 (core tasks: the full rebuild is a `tasks` definition with a declared `tasks.Parallelism`) |
| Spec | 05-storage §5S.3.8 (package `storage/index`, `core/internal/bleveindex`, the dirty set at `c/index/<module>/<name>/dirty/<id>`, the reconciler, the rebuild, backups through bleve's `CopyTo`), §5S.3.9 (`Generation`, `Derived`, `DerivedIndex`, `DerivedHooks`, why a generation and not a count), §5S.10 (the index and derived tests); 03 §3.3 (`storage/index`); 06 §6.4.10 F7 row (`Storage.Indexes`, `Storage.Derived`, `Registrar.DerivedIndex`); 06 §6.5.2 start step (3a); 04 §8.1 F7 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's `internal/search` keeps its
bleve index at `library.bleve` and its `idx:sidx:dirty:` rows; nothing in ABO opens `storage/index` until F7-09. The
packages run on `hello` and `coretest`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-07-index" -b agent/platform-core-f7-07-index origin/main
cd "$REPO/.worktrees/pc-f7-07-index"
git rebase origin/main
```

## Goal

1. `core/storage/index` (L1, imports `storage`; `storage` never imports it): `Spec`, `Field`, `FieldKind`, `Doc`,
   `Opener`, `Index` (`Search`, `Count`, `Facets`, `MarkDirty`, `State`, `Close`), the engine-neutral `Query`
   constructors (`Match`, `Term`, `Prefix`, `Range`, `And`, `Or`, `Not`, `QueryString`), `Page`, `Result`, `Hit`,
   `FacetValue`, `RebuildWriter`, `State` (05-storage §5S.3.8, code block copied exactly).
2. `core/internal/bleveindex`: the implementation generalising HEAD's `internal/search/bleve_index.go` (scorch;
   mapping and rebuilding markers as siblings of the index directory; an open failure is `Degraded`, not a boot
   failure; a mapping change drops and rebuilds; one scorch async-error callback per opened index under a unique name
   with `sync.Once`, the single allowlisted entry in `TestNoGlobals`); on disk at `<db parent>/index/<module>/<name>.bleve`.
3. The **native dirty set** at `c/index/<module>/<name>/dirty/<id>`, written by `MarkDirty` in the entity's batch, which
   also touches the index's generation; the reconciler as a `lifecycle.Component` waking every
   `core.index.reconcile.interval` and after a commit carrying a dirty mark, draining with a bounded pool of
   `core.index.reconcile.workers`; the full rebuild as a `tasks` definition with a declared `tasks.Parallelism`.
4. §5S.3.9's `Generation`, `Derived` (`Touch`, `Current`), `DerivedIndex`, `DerivedHooks` and the kernel's
   version/generation check at start step (3a), and `Registrar.DerivedIndex(name, hooks, build)` (06 §6.4.10 F7).

## Invariants

- **I1. G4.** The dirty mark commits in the same batch as the entity write; a crash after commit is drained by the
  reconciler on the next boot (`TestIndexMarkDirtyInSameBatch`, `TestIndexReconcilerDrainsAfterCrash`).
- **I2.** An artifact is never falsely accepted: an add and a delete that cancel in a count are still rejected, and
  an artifact from another store generation is rejected (`TestDerivedGenerationRejectsStaleArtifact`).
- **I3.** `Load` runs at start step (3a), never in `app.New` or `Describe`; a wrong build kind name fails P7
  (`TestDerivedIndexRebuildTriggers`).
- **I4.** HEAD's behaviours kept: open failure degrades; mapping change rebuilds (`TestIndexOpenFailureDegrades`,
  `TestIndexMappingChangeRebuilds`).
- **I5.** bleve types never appear in `storage/index`'s exported API.
- **I6. Dark.** ABO's search, its index path and its dirty rows are unchanged.

## Files

| Action | Path |
|---|---|
| create | `core/storage/index/` |
| create | `core/internal/bleveindex/` |
| modify | `core/go.mod`, `core/go.sum`, `go.sum` (`github.com/blevesearch/bleve/v2` into core) |
| modify (not in the row; see Planner notes) | `core/storage/` (the `Derived` handle, `Generation`, `DerivedIndex` in `Spec.Derived`), `core/module/` (`Registrar.DerivedIndex`, `StorageHandles.Indexes`/`Derived`), the kernel's start step (3a) check |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# HEAD's bleve code to generalise. [the spec cites bleve_index.go:104 for bookMappingVersion and :68 for the scorch
#  callback; at both 748fd787a and HEAD f7ac5b11e the const is at :167 and the callback at :69; register.go:42-60 open-failure path;
#  the dirty set in internal/database/pebble_store_search_dirty.go:39 (its R1 path may differ)]
grep -n 'bookMappingVersion\|RegistryAsyncErrorCallbacks' internal/search/bleve_index.go
grep -rn 'sidx:dirty' --include='*.go' internal/ core/ | grep -v _test | head -3
# Nothing of index exists in core. Expected: 0
ls core/storage/index core/internal/bleveindex 2>/dev/null | wc -l
# bleve is a root dependency. Expected: 1 line
grep -n 'blevesearch/bleve/v2 ' go.mod
# Registrar has no DerivedIndex yet. Expected: 0
grep -rn 'func (r \*Registrar) DerivedIndex' core/module/ | wc -l
```

STOP if `core/storage/index` already exists or if `Registrar.DerivedIndex` already exists in another form.

## Steps

1. Read 05-storage §5S.3.8 and §5S.3.9 in full; copy the code blocks exactly.
2. Write the tests first (below), with `vfs` crash injection for the dirty-set crash case.
3. `storage/index`: types and query constructors; `Opener` wired by the kernel into `module.StorageHandles`.
4. `internal/bleveindex`: open, mapping version marker, rebuilding marker, degraded state, drop-and-rebuild, the
   scorch callback registry entry, search/count/facets, `CopyTo` for backups.
5. Dirty set, `MarkDirty`, reconciler component, rebuild definition.
6. `Generation`/`Derived`/`DerivedIndex`/`DerivedHooks`, `Registrar.DerivedIndex`, the (3a) check.
7. Wire on `hello` (one small index) and `coretest`. Headers, fragment, acceptance.

## Code contract (05-storage §5S.3.8 and §5S.3.9, verbatim excerpts)

```go
package index
type Opener interface {
	Open(ctx context.Context, s Spec) (Index, error)
}
type Index interface {
	Search(ctx context.Context, q Query, page Page) (Result, error)
	Count(ctx context.Context, q Query) (uint64, error)
	Facets(ctx context.Context, q Query, fields []string, size int) (map[string][]FacetValue, error)
	MarkDirty(b *storage.Batch, id string) error
	State() State
	Close() error
}

package storage
type Generation [16]byte
func (d Derived) Touch(ctx context.Context, b *Batch) error
func (d Derived) Current(ctx context.Context) (Generation, error)
type DerivedIndex struct {
	Name     string
	Version  string
	Blocking bool
}
```

## Tests (write first; spec names copied exactly, 05-storage §5S.10)

`TestIndexMarkDirtyInSameBatch`, `TestIndexReconcilerDrainsAfterCrash`, `TestIndexOpenFailureDegrades`,
`TestIndexMappingChangeRebuilds`, `TestDerivedGenerationRejectsStaleArtifact`, `TestDerivedIndexRebuildTriggers`,
and the `TestNoGlobals` allowlist entry for the scorch callback. Not here: `TestBooksIndexRebuiltAtCutover` (the
search oracle; F7-08 owns it).

## Acceptance (commands and expected output)

```bash
# Core index packages with the race detector. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./storage/index/... ./internal/bleveindex/... ./storage/...)

# No bleve type in the public API. Expected: 0
grep -rn 'blevesearch' core/storage/index/*.go | grep -v _test | wc -l

# Layering: storage does not import index. Expected: no line naming storage/index
(cd core && go list -f '{{join .Imports "\n"}}' ./storage | grep 'storage/index')

# Dark: ABO's search unchanged. Expected: ok
go test -count=1 ./internal/search/...

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Books' index spec, document builder and query parser on `storage/index`; dropping `idx:sidx:dirty:` (F7-08's M6);
  wiring and moving `internal/search` (F7-09).
- Deleting `library.bleve` (R3-03).
- memdb, hnsw and chromem (stay books', registered as `DerivedIndex` at F7-09).

## Rollback

Revert the PR; nothing in ABO uses it.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; no `go work init`.
- Conventional commit, for example `feat(storage): full-text index contract and derived generations (dark)`, ending
  with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- The reconciler drains with a bounded pool (`core.index.reconcile.workers`) and the rebuild declares its
  `tasks.Parallelism` (CLAUDE.md concurrency rule; this is a library-scale loop).

## Planner notes

- **Files missing from the row.** §5S.3.9's `Generation`, `Derived` and `DerivedIndex` live in package `storage`
  (`core/storage/`), and `Registrar.DerivedIndex` and `StorageHandles.Indexes`/`Derived` in `core/module/` (06
  §6.4.10 F7 row); the row lists only `core/storage/index/` and `core/internal/bleveindex/`. In W28 no other row lists
  `core/storage/` or `core/module/`, so adding them creates no collision.
