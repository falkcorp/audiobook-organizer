<!-- file: docs/agent-tasks/platform-core/PC-16-05-library-roots-search-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b0cb457-1fc9-4369-9c23-b5c0896dfbae -->
<!-- last-edited: 2026-11-04 -->

# PC-16-05 — Library and search index: fileops, watcher and pathutil generic parts; `legacy.LibraryRootSource`; `bleveindex` and `storage/index` with `DirtySet`

| Field | Value |
|---|---|
| ID | PC-16-05 |
| Title | Library and search index: fileops, watcher and pathutil generic parts; legacy.LibraryRootSource; bleveindex and storage/index with DirtySet |
| Tier | **T3 — Opus / high.** Roots served from HEAD's `import_path:` rows through a legacy-only source, and a derived index whose results must not change; a wrong dirty-set binding loses marks across a rollback. |
| Wave | W16 (stage S7, release `S7`) |
| Depends on | PC-16-04 |
| Trailer | `Stage-Release: S7 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (rows `storage/index`, `library` (rest)), §4 (05-storage §5S.3.8, 05-library 5L.3.1), §5 rules 10, 18 |
| Owning spec | 05-library §5L.3, §5L.4, §5L.9, §5L.10; 05-storage §5S.3.8, §5S.3.9, §5S.3.11; 09 S7 "Moves", "Data/keys"; 04 §8.1 row S7 |

**Gate.** Merges only after the `S6` soak row exists (`check_soak.py` check (b)).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-05" -b feat/pc-16-05-library-index origin/main
cd "$REPO/../abo-pc-16-05"
git worktree list
make web-deps
```

## Goal

1. **Search index.** `core/storage/index` (L1: `Spec`, `Field`, `Doc`, `DirtySet`, `Opener`, `Index`, `Query`
   builders, the reconciler as a `lifecycle.Component`, the rebuild as a `tasks` definition with a declared
   `tasks.Parallelism`) and its engine `core/internal/bleveindex`, generalised from `internal/search/bleve_index.go`
   (scorch; mapping and rebuilding markers as **siblings** of the index directory; open failure = `Degraded`, not a
   boot failure; mapping change = drop + rebuild). Books opens its `books` index with `Spec.LegacyPath` = its existing
   index directory and `Spec.Dirty` = **books' own `DirtySet`** over HEAD's `idx:sidx:dirty:` family, so the previous
   build drains the marks this build leaves and the reverse. ABO's query parser and per-user field routing stay books
   code compiling to `index.Query` or `QueryString`. **Search results for the fixture query set equal S6's.**
2. **Library (rest).** The rest of core `library` (on top of the S1 leaves already in `core/library`): root
   registration and routing, `RootFS`, `Transfer`, `Hash`, `Watch`, path mapping, from the generic parts of
   `internal/fileops`, `internal/watcher` and `internal/pathutil`; and the legacy-only root source
   `legacy.LibraryRootSource` with `legacy.LibraryCoverID` in `core/legacy/library.go`. Books implements the source
   over its `import_path:` rows (plus its books root from the blob, read-only there) and passes it on its manifest.
   The scanner/autoscan/filesystem field-merge conversion is **not** in this PR (PC-16-06).

## Files

Create:
- `core/library/roots.go` (root registration, routing by `Covers(LibraryCoverID(role, id))`, merged root list).
- `core/legacy/library.go` (`LibraryRootSource`, `LibraryCoverID`), plus `legacytest.LibraryRootSourceSuite`.
- `core/internal/bleveindex/`
- `core/storage/index/` (with `indextest.DirtySetSuite` in `core/storage/index/indextest`).

Modify:
- `internal/fileops/` — generic copy/reflink/hash/sync seams move to core; `book_file_hash_recorder.go` and
  `write_tags_safe.go` stay books.
- `internal/watcher/` — becomes books' use of `Manager.Watch` (books' audio-extension filter passed as `Extensions`).
- `internal/pathutil/` — generic parts (`ValidateRel`, `SplitRoot`, `JoinRoot`, `CutPathPrefix`, `IsWithin`,
  `UnderProtectedPath`, `ShouldSkipDir`) become library internals; `FrozenITunesSegment`, `VersionsDirName` and
  `PathVars` stay books.
- `internal/search/` — engine lifecycle leaves for `bleveindex`; query parser and per-user routing stay.
- `internal/books/store/` — books' `legacy.LibraryRootSource` over `import_path:` and books' `index.DirtySet` over
  `idx:sidx:dirty:`.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD). Zero hits or a moved count: STOP.

```bash
git grep -n 'const bookMappingVersion = "4"' -- internal/search/bleve_index.go     # 1 (:167)
git grep -n 'const searchDirtyPrefix = "idx:sidx:dirty:"' -- '*.go' | grep -v _test   # 1 (748fd787a: internal/database/pebble_store_search_dirty.go:39; your base: internal/books/store/)
git grep -n 'func (p \*PebbleStore) CreateImportPath\|func (p \*PebbleStore) UpdateImportPath' -- '*.go' | grep -v _test   # 2 (pebble_store_importpaths.go:138, :187 at 748fd787a)
git grep -n 'func New(callback Callback' -- internal/watcher/watcher.go          # 1 (:68)
ls internal/pathutil/root.go internal/pathutil/prefix.go internal/pathutil/protected.go internal/pathutil/hidden.go   # all exist
ls internal/fileops/copy.go internal/fileops/reflink.go internal/fileops/hash.go internal/fileops/service.go internal/fileops/register.go   # all exist

M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/search\"" -- '*.go' | grep -v _test.go | wc -l     # 19
git grep -l "\"$M/internal/watcher\"" -- '*.go' | grep -v _test.go | wc -l    # 1 (internal/server)
git grep -l "\"$M/internal/fileops\"" -- '*.go' | grep -v _test.go | wc -l    # 30
git grep -l "\"$M/internal/pathutil\"" -- '*.go' | grep -v _test.go | wc -l   # 79

test -d core/library && echo ok        # S1 leaves (PC-03-02); 0 = STOP
```

The importer counts are large. This PR moves code, it does not rewrite every importer: where a books package keeps
using a generic helper, keep a thin books wrapper or rewire the import; list `old ==> new` in the PR description.

## Steps

1. Read 05-storage §5S.3.8 and §5S.3.9 (derived indexes, storage generation), 05-library §5L.3 (whole Go API),
   §5L.4, §5L.5, §5L.6, §5L.9, §5L.10, and 03 §3.3 on library roots ("neither routed nor exclusive").
2. **Golden first.** Before moving anything, write the fixture query set test against the current engine: a fixture
   library, a set of queries covering the parser's features (field terms, tags, relative dates, regex terms,
   per-user fields), and their ordered result IDs and totals, committed as a golden (name coined here:
   `TestSearchFixtureQuerySetMatchesS6`). It must pass unchanged after the move.
3. Build `storage/index` and `bleveindex` per §5S.3.8: `MarkDirty(b, id)` records the ID in the dirty set inside the
   entity's batch and touches the index's storage generation; the reconciler wakes every
   `core.index.reconcile.interval` and after any commit that carried a mark, draining with a bounded pool of
   `core.index.reconcile.workers`. `Spec.Dirty` is the only door for an app's dirty set (no `legacy.Stores.Dirty`).
4. Books' `DirtySet` writes HEAD's `idx:sidx:dirty:` rows in HEAD's format; it runs `indextest.DirtySetSuite` with
   HEAD fixtures. The index directory and mapping version stay HEAD's, so S6 opens the same index (or rebuilds it:
   it is derived data).
5. Build the rest of `library` and `legacy.LibraryRootSource` per 5L.3.1. Books' source: covers every `import/…`
   ID and its books root; `Create` allocates the ID as HEAD does (`nextID("import_path")` under `counterMu`, one
   books store instance shared with `CreateImportPath`); `Update` rewrites only library's fields with an `Expect` on
   the bytes it read (no `legacy.WriteLock`); fixed fields (`Watch`, `Sentinel` = `SentinelBootstrap`,
   `Quarantine` = `.failed`, `AppDir` = `.mediacore`, `MinFree` = 0) refuse a change with
   `library.ErrRootFieldFixed`; it keeps books' memdb `import_paths` table current; nothing is stored beside the
   `import_path:` row. Non-import roots go to `core.library.roots` through `config.Store`.
6. The kernel loads roots after storage migrations (5L.4 step 1); a failing root is disabled with health
   `critical` and the app still boots; one `library.Scope` per enabled module for `module.Deps.Library`. Watchers
   are `lifecycle.Component`s; an fsnotify overflow yields a rescan (`TestWatchOverflowYieldsRescan`).
7. Keep behaviour: ingest file mode stays `LibraryFileMode` 0664; file and parent-directory fsync seams kept;
   reflink refuses an existing destination; the 5-second watcher debounce kept.
8. Add `internal/books/store` to `TestLegacyFamiliesWrittenOnlyByLegacyStores`' allow-list for `import_path:` and
   `idx:sidx:dirty:` only if it is not already there.

## Code contract (copy exactly)

```go
package index // 05-storage §5S.3.8
type Spec struct { Name, MappingVersion string; Fields []Field; LegacyPath string; Dirty DirtySet
	Rebuild func(ctx context.Context, w RebuildWriter) error
	Document func(ctx context.Context, id string) (Doc, bool, error) }
type DirtySet interface {
	Mark(b *storage.Batch, id string) error
	Pending(ctx context.Context) iter.Seq2[string, error]
	Clear(ctx context.Context, b *storage.Batch, id string) error
}
type Opener interface { Open(ctx context.Context, s Spec) (Index, error) }
type Index interface {
	Search(ctx context.Context, q Query, page Page) (Result, error)
	Count(ctx context.Context, q Query) (uint64, error)
	Facets(ctx context.Context, q Query, fields []string, size int) (map[string][]FacetValue, error)
	MarkDirty(b *storage.Batch, id string) error
	State() State
	Close() error
}

package legacy // 05-library 5L.3.1
type LibraryRootSource interface {
	List(ctx context.Context) ([]library.RootSpec, error)
	Covers(id string) bool
	Create(ctx context.Context, b *storage.Batch, spec library.RootSpec) (library.RootID, error)
	Update(ctx context.Context, b *storage.Batch, spec library.RootSpec) error
	Delete(ctx context.Context, b *storage.Batch, id library.RootID) error
}
func LibraryCoverID(r library.Role, id library.RootID) string // "<role>/<root id>"
```

Native dirty set (core apps): `c/index/<module>/<name>/dirty/<id>`. `RootFS`, `Transfer`, `Hash`, `Watch`,
`PathMapper` and `RootSpec` are declared in 05-library 5L.3.1–5L.3.7; copy them.

## Tests to write first

Index: `TestSearchFixtureQuerySetMatchesS6` (name coined here; the 09 S7 done-check "search results for the fixture
query set equal S6's"), `indextest.DirtySetSuite` on the native set and on books' set, plus a rollback case: marks
left by this build in HEAD's family are drained by HEAD's reconciler code (name coined here:
`TestBooksDirtySetDrainedByPrevBuild`).

Library (05-library §5L.9): `TestRootFSRefusesDotDotAndSymlinkEscape`, `TestRenameNoReplaceNeverClobbers`,
`TestTransferCopyPublishIsNoClobber`, `TestTransferCrossDeviceMoveOrder`, `TestTransferRecoveryFromJournal`,
`TestTransferPermPolicyHasNoDefault`, `TestTransferAutoFallbackChain`, `TestMoveRefusedWhenSeedingUnknown`,
`TestTransferChecksFreeSpaceFirst`, `TestSentinelBlocksDestructiveOps`, `TestSentinelBootstrapLegacyRoot`,
`TestRootOverlapAcrossOwnersRefused`, `TestPathMapperLongestPrefixBoundary`, `TestPathMapperNoUnicodeFolding`,
`TestWatchDebounceCoalesces` (synctest), `TestWatchOverflowYieldsRescan`, `TestWatchFallsBackToPolling`,
`TestHashSameDescriptorSizeAndDigest`, `TestDisabledModuleRootsInert`, `TestTransferInCommitsWithCallerBatch`,
`TestScopeCannotActAsAnotherOwner`, `TestLegacyRootSentinelBootstrap`, `TestLibraryNewRejectsWrongLegacySource`,
`TestConfigEntryRoots_RoundTrip`, `TestRootRouting_CoversDecides`, `TestLibraryErrorsPassKeyNameGate`, and
`TestImportRootsStayLegacyFormat` (books' source under `legacytest.LibraryRootSourceSuite`, HEAD fixtures; its
"LastScan/BookCount written by a concurrent scan survive a library edit" half needs PC-16-06's field-merge
conversion: write it now, mark that half `t.Skip("PC-16-06")`, and say so in the PR).

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                            # exit 0
(cd core && go test ./storage/index/... ./internal/bleveindex/... ./library/... ./legacy/... -race -count=1)   # all "ok"
go test ./internal/search/... ./internal/fileops/... ./internal/watcher/... ./internal/pathutil/... ./internal/books/... ./internal/server/... -race -count=1   # all "ok"
go test ./internal/search/... -run 'TestSearchFixtureQuerySetMatchesS6' -count=1 -v | grep -E '^--- (PASS|FAIL)'   # --- PASS
git grep -n '"idx:sidx:dirty:\|"import_path:' -- core | grep -v _test.go | wc -l          # 0 (core holds no ABO key name; TestCoreHoldsNoAppKeyNames)
make ci                                                                                  # exit 0
make -C core ci                                                                          # exit 0
```

## Out of scope

The scanner/autoscan/filesystem field-merge conversion and its R-test row (PC-16-06); moving organizer, versions,
quarantine, Deluge import and iTunes transfers onto `RootFS`/`Transfer` and deleting the three no-clobber renames
(05-library §5L.10 "Order"; see Planner notes); the browse route under `core:library.browse`; moving ABO roots to a
native keyspace (a later cutover); hnsw, chromem-go and go-memdb (stay books, §5S.3.9).

## Rollback

Binary swap: roots stay HEAD's `import_path:` rows and blob fields; the dirty set stays HEAD's family; the index
directory and format are unchanged or rebuilt (derived data). Nothing is stored beside a legacy row.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S7 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- CLAUDE.md concurrency: the reconciler drains with `core.index.reconcile.workers`, the rebuild declares
  `tasks.Parallelism`; no unbounded fan-out over the library.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- 05-library §5L.10 "Order" moves writers one subsystem at a time (organizer, versions, quarantine, Deluge import,
  iTunes transfer) and then deletes `fileops` copy/reflink and the three renames, and maps `internal/fileops`'
  browse service to a core route under `core:library.browse`; no inventory row covers those steps or names their
  stage. This brief keeps them out of scope; the planner should add rows or state that they follow S10.
- `internal/books/store/` is also where PC-16-02's books gate store and PC-16-03's books job store live; the chain
  orders them. `internal/books/module.go` (`Manifest.Library`, `legacy.Stores.Roots`, the `books` index `Spec`) is
  not in the row's files but must change here; PC-16-06 edits it next.
