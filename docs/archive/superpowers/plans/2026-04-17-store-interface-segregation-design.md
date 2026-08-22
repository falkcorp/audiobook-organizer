<!-- file: docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6915fa3e-b5ea-4e39-98f0-45aa8548ce1e -->

# Store Interface Segregation — Design

**Backlog:** architecture refinement; follow-on to 4.4 (DI rollout).
**Status:** brainstormed, awaiting implementation plan.
**Scope:** ~281-method `database.Store` → ~25 focused sub-interfaces. Proof-point migration of 3 consumers. Catalog for the remaining 76 files.

## 1. Problem

`internal/database/store.go` defines a single `Store` interface with ~281 methods. Every service in `internal/server/*` that needs even one DB call takes `*database.Store` (or the interface), and so must be given a mock with 281 methods when tested. In practice this shows up as:

- Test setup that drags in unrelated concerns: a playlist-evaluator test instantiates a full mock even though it only calls `GetBookByID`.
- Handwritten stubs drifting from the real interface when contributors decline to regenerate mocks (see the prior `stubStore` incident that triggered backlog 5.9).
- Services that advertise more dependencies than they actually use — 18 of the 79 consumers keep a `Store` field but call *zero* methods on it, because they were given `Store` for symmetry with sibling types.

The Interface Segregation Principle says callers should depend on the smallest interface that gets the job done. Applying it to `Store` unlocks materially smaller tests and makes each service's real dependencies visible in its type signature.

## 2. Non-goals

- **Not a rewrite of the implementations.** `PebbleStore` and `sqliteStore` still implement the full surface — they just satisfy the sub-interfaces automatically via Go's structural typing.
- **Not a removal of `Store`.** The top-level `Store` interface stays as an umbrella that embeds every sub-interface. Callers that genuinely need wide access (the server bootstrap, test helpers) continue to use it.
- **Not a fix for the 18 noop consumers.** Those pass-through dependencies are a separate cleanup tracked in a follow-up issue.
- **Not a performance change.** Compile time may improve slightly; runtime is identical.

## 3. Design

### 3.1 Slicing — hybrid read/write split

The domain survey (section 6) shows four "hot" domains consumed heavily by services that genuinely only need reads: **Book** (41 files), **Author** (14), **Series** (12), **User** (6). These get the full three-interface treatment:

```go
type BookReader interface {
    GetBookByID(id string) (*Book, error)
    GetAllBooks(limit, offset int) ([]Book, error)
    GetBookByFilePath(path string) (*Book, error)
    GetBookByITunesPersistentID(pid string) (*Book, error)
    GetBookByFileHash(hash string) (*Book, error)
    GetBookByOriginalHash(hash string) (*Book, error)
    GetBookByOrganizedHash(hash string) (*Book, error)
    GetDuplicateBooks() ([][]Book, error)
    GetFolderDuplicates() ([][]Book, error)
    GetDuplicateBooksByMetadata(threshold float64) ([][]Book, error)
    GetBooksByTitleInDir(normalizedTitle, dirPath string) ([]Book, error)
    GetBooksBySeriesID(seriesID int) ([]Book, error)
    GetBooksByAuthorID(authorID int) ([]Book, error)
    GetBooksByVersionGroup(groupID string) ([]Book, error)
    SearchBooks(query string, limit, offset int) ([]Book, error)
    CountBooks() (int, error)
    ListSoftDeletedBooks(limit, offset int, olderThan *time.Time) ([]Book, error)
    GetBookSnapshots(id string, limit int) ([]BookSnapshot, error)
    GetBookAtVersion(id string, ts time.Time) (*Book, error)
    GetBookTombstone(id string) (*Book, error)
    ListBookTombstones(limit int) ([]Book, error)
    GetITunesDirtyBooks() ([]Book, error)
}

type BookWriter interface {
    CreateBook(book *Book) (*Book, error)
    UpdateBook(id string, book *Book) (*Book, error)
    DeleteBook(id string) error
    SetLastWrittenAt(id string, t time.Time) error
    MarkITunesSynced(bookIDs []string) (int64, error)
    RevertBookToVersion(id string, ts time.Time) (*Book, error)
    PruneBookSnapshots(id string, keepCount int) (int, error)
    CreateBookTombstone(book *Book) error
    DeleteBookTombstone(id string) error
}

type BookStore interface {
    BookReader
    BookWriter
}
```

Same three-way shape for `AuthorReader`/`AuthorWriter`/`AuthorStore`, `SeriesReader`/`SeriesWriter`/`SeriesStore`, `UserReader`/`UserWriter`/`UserStore`. The remaining domains get a single interface each because no survey caller consumed them read-only.

### 3.2 Single-interface domains

From the survey's method-call evidence, these sub-interfaces cover everything else:

| Interface | Covers |
|---|---|
| `LifecycleStore` | `Close`, `Reset` |
| `NarratorStore` | narrator CRUD + book-narrator joins |
| `WorkStore` | Work CRUD |
| `SessionStore` | session CRUD + sweep |
| `RoleStore` | Role CRUD |
| `APIKeyStore` | APIKey CRUD + revoke + touch |
| `InviteStore` | Invite CRUD + ConsumeInvite |
| `UserPreferenceStore` | global + per-user preference KV |
| `UserPositionStore` | UserPosition + UserBookState |
| `BookVersionStore` | BookVersion CRUD + trash/purge lists + torrent-hash lookup |
| `BookFileStore` | BookFile CRUD + Upsert + batch + move |
| `BookSegmentStore` | deprecated segment CRUD (kept until segment-removal PR) |
| `PlaylistStore` | legacy auto-generated series-playlist methods |
| `UserPlaylistStore` | smart + static user playlist CRUD |
| `ImportPathStore` | ImportPath CRUD |
| `OperationStore` | Operation + logs + state + results + changes + summary + retention prunes |
| `TagStore` | book/author/series tag methods (the 25 `*Tag` entries) |
| `UserTagStore` | the `*BookUserTag` variants |
| `MetadataStore` | MetadataFieldState + MetadataChangeRecord + alternative titles |
| `HashBlocklistStore` | `IsHashBlocked`, DoNotImport CRUD |
| `ITunesStateStore` | LibraryFingerprint + DeferredITunesUpdate + ITunesDirty |
| `PathHistoryStore` | path change history |
| `ExternalIDStore` | ExternalIDMapping CRUD + tombstones + bulk |
| `RawKVStore` | `SetRaw`, `GetRaw`, `DeleteRaw`, `ScanPrefix` |
| `PlaybackStore` | PlaybackEvent + PlaybackProgress + book/user stats |
| `SettingsStore` | `GetSetting`, `SetSetting`, `DeleteSetting`, `GetAllSettings` |
| `StatsStore` | `DashboardStats`, `CountFiles`, `CountAuthors`, `CountSeries`, location counts |
| `MaintenanceStore` | `Optimize`, scan cache, rescan markers |
| `SystemActivityStore` | system activity log CRUD + prune |

Total: **4 triple-interfaces** (Book, Author, Series, User) + **29 single-interfaces** = 41 named interfaces, plus the top-level `Store` umbrella.

### 3.3 File layout

```
internal/database/
  store.go          — Store umbrella + shared types (shrunk from ~1200 lines to ~500)
  iface_book.go     — BookReader, BookWriter, BookStore
  iface_author.go   — AuthorReader, AuthorWriter, AuthorStore
  iface_series.go   — SeriesReader, SeriesWriter, SeriesStore
  iface_user.go     — UserReader, UserWriter, UserStore
  iface_tags.go     — TagStore, UserTagStore
  iface_itunes.go   — ITunesStateStore, ExternalIDStore, PathHistoryStore
  iface_ops.go      — OperationStore
  iface_misc.go     — the remaining single-interfaces (one block each)
```

Split by *likely co-change*. Narrator/Work/Playlist/Playback/Stats/etc. are stable and sparse enough to share `iface_misc.go`. The `Store` umbrella in `store.go` becomes:

```go
type Store interface {
    LifecycleStore
    BookStore
    AuthorStore
    SeriesStore
    UserStore
    NarratorStore
    WorkStore
    SessionStore
    RoleStore
    APIKeyStore
    InviteStore
    UserPreferenceStore
    UserPositionStore
    BookVersionStore
    BookFileStore
    BookSegmentStore
    PlaylistStore
    UserPlaylistStore
    ImportPathStore
    OperationStore
    TagStore
    UserTagStore
    MetadataStore
    HashBlocklistStore
    ITunesStateStore
    PathHistoryStore
    ExternalIDStore
    RawKVStore
    PlaybackStore
    SettingsStore
    StatsStore
    MaintenanceStore
    SystemActivityStore
}
```

Shared types (`Author`, `Book`, `Series`, status constants, etc.) stay where they are in `store.go` — they're data, not interface surface.

### 3.4 Composition at call sites

Services encode their real dependencies inline rather than pulling in a prebuilt composition type. This surfaces the dependency set on the struct:

```go
// internal/server/audiobook_service.go
type AudiobookService struct {
    store interface {
        database.BookStore
        database.AuthorReader
        database.SeriesReader
        database.NarratorStore
        database.StatsStore
        database.HashBlocklistStore
        database.TagStore
        database.BookFileStore
    }
    // ... other fields
}
```

Inline anonymous interfaces are idiomatic Go for this pattern (see `io.ReadWriter`'s ancestor uses in stdlib). They keep the dependency list next to the struct where it's discoverable.

Constructors mirror the struct:

```go
func NewAudiobookService(
    store interface {
        database.BookStore
        database.AuthorReader
        // ...
    },
    // ...
) *AudiobookService { ... }
```

Callers keep passing `*PebbleStore` unchanged — it satisfies every sub-interface.

### 3.5 Mocks

`.mockery.yaml` grows new entries — one per sub-interface:

```yaml
packages:
  github.com/jdfalk/audiobook-organizer/internal/database:
    interfaces:
      Store:           # unchanged — still the full-surface mock
      BookReader:
      BookWriter:
      BookStore:       # optional — embedding both Reader + Writer, mockery handles it
      AuthorReader:
      AuthorWriter:
      AuthorStore:
      SeriesReader:
      SeriesWriter:
      SeriesStore:
      UserReader:
      UserWriter:
      UserStore:
      # ... every single-interface listed below
      NarratorStore:
      LifecycleStore:
      # ... etc
```

Output: `internal/database/mocks/mock_book_reader.go`, `mock_author_store.go`, etc. One file per interface. The `mocks-check` target (CI gate from backlog 5.9) stays as-is — it already diffs generated mocks against committed ones, and adding new interfaces just adds new generated files.

**Tests migrate opportunistically.** Existing tests continue to use the full `mocks.Store` until their services narrow. New tests for narrowed services get narrow mocks.

## 4. Proof-point migrations (in this PR)

Three services migrate in the first PR to prove the pattern:

### 4.1 `internal/server/audiobook_service.go`

Currently takes full `Store`. Actually uses: `AuthorReader`+`AuthorWriter`, `BookReader`+`BookWriter`, `BookFileStore`, `NarratorStore`, `SeriesReader`+`SeriesWriter`, `HashBlocklistStore`, `StatsStore`, `TagStore`.

**After:** the struct field becomes an inline anonymous interface (shown in section 3.4). Constructor mirrors. Nothing else in the file changes — method bodies already call only the narrow subset.

### 4.2 `internal/server/playlist_evaluator.go`

Currently takes full `Store`. Actually uses: `BookReader` + `UserPositionStore`.

**After:** struct field becomes `interface { database.BookReader; database.UserPositionStore }`. Constructor mirrors. Cleanest proof that a "narrow" service really can be narrow.

### 4.3 `internal/server/reconcile.go`

Currently takes full `Store`. Actually uses: `BookReader`+`BookWriter`, `BookFileStore`, `ImportPathStore`, `OperationStore`.

**After:** same inline-interface pattern. Demonstrates the read+write shape that the first two don't — several unrelated single-interfaces plus a read/write split domain.

Each proof-point carries a test update: the service's existing test file replaces `mocks.NewStore(t)` with the narrowest mock composition that still compiles, and the test gets demonstrably smaller.

## 5. Implementation sequence (this PR)

1. **Define the interfaces.** Create `iface_*.go` files with every sub-interface. Make `Store` embed them. Commit.
2. **Update `.mockery.yaml`.** Add one entry per new interface. Run `mockery`. Commit generated mocks.
3. **Migrate `playlist_evaluator.go`.** Smallest surface, lowest risk. Narrow struct + constructor, update test. Commit.
4. **Migrate `audiobook_service.go`.** Largest surface, highest demonstration value. Commit.
5. **Migrate `reconcile.go`.** Represents the "moderate" shape. Commit.
6. **Add the catalog** (section 6) to `docs/superpowers/plans/` as a migration tracker for the follow-on PR.

Each step ends with `make test` green. If step 3's test refactor reveals a missing sub-interface method, the fix is a one-line addition to the interface in `iface_*.go` — mockery regenerates cleanly.

## 6. Migration catalog (follow-on PR — full sweep)

This table is the deliverable for the agent that finishes the migration after this PR ships. Each row tells the agent: what type does this file currently depend on, what should it depend on afterward, and any gotchas.

### Classification legend

- **read-only** — struct only reads; use `*Reader` interfaces
- **write-only** — struct only writes; use `*Writer` interfaces
- **read-write** — struct does both; use `*Store` convenience interfaces
- **test** — `_test.go` file; narrow the mock composition
- **noop** — struct has a `Store` field but calls zero methods on it; **do not migrate — open a separate issue to remove the unused field**

### `cmd/`

| File | Class | Interfaces | Notes |
|---|---|---|---|
| `cmd/commands_test.go` | noop | — | test file, mocks `Store` for convenience; leave as-is |
| `cmd/dedup_bench_types.go` | read-only | `AuthorReader` | tiny surface |
| `cmd/seed.go` | read-write | `AuthorStore`, `BookStore`, `SeriesStore` | dev seed command |

### `internal/auth/`, `internal/config/`, `internal/logger/`, `internal/metadata/`, `internal/operations/`, `internal/search/`, `internal/transcode/`, `internal/testutil/`

| File | Class | Interfaces | Notes |
|---|---|---|---|
| `internal/auth/context.go` | noop | — | context helpers only — `Store` appears in type sigs but no method calls |
| `internal/auth/seed.go` | read-write | `RoleStore`, `UserStore` | bootstrap |
| `internal/config/persistence.go` | read-write | `SettingsStore` | single-domain |
| `internal/logger/operation.go` | read-write | `OperationStore` | defines its own `OperationStore` interface locally — rename/align to the new DB-level one |
| `internal/metadata/enhanced.go` | read-write | `BookStore` | |
| `internal/operations/queue.go` | read-write | `OperationStore` | |
| `internal/operations/state.go` | read-write | `OperationStore` | checkpoint persistence |
| `internal/search/index_builder.go` | read-write | `AuthorReader`, `BookReader`, `SeriesReader`, `TagStore` | read-only across 4 domains; anonymous-interface is ideal here |
| `internal/transcode/transcode.go` | read-write | `BookReader`, `BookFileStore` | |
| `internal/testutil/integration.go` | write-only | `LifecycleStore`, `OperationStore` | test fixture seeder |

### `internal/server/` (core services)

| File | Class | Interfaces | Notes |
|---|---|---|---|
| `internal/server/ai_handlers.go` | read-write | `AuthorReader`, `AuthorWriter`, `OperationStore` | |
| `internal/server/ai_scan_pipeline.go` | noop | — | field but no calls — remove the field |
| `internal/server/archive_sweep.go` | read-write | `BookStore`, `BookFileStore` | |
| `internal/server/audiobook_service.go` | read-write | `BookStore`, `AuthorReader`, `AuthorWriter`, `SeriesReader`, `SeriesWriter`, `NarratorStore`, `StatsStore`, `HashBlocklistStore`, `TagStore`, `BookFileStore` | **proof-point — done in this PR** |
| `internal/server/audiobook_update_service.go` | noop | — | field but no calls |
| `internal/server/author_series_service.go` | noop | — | field but no calls |
| `internal/server/batch_poller.go` | read-write | `OperationStore` | |
| `internal/server/batch_service.go` | noop | — | field but no calls |
| `internal/server/changelog_service.go` | noop | — | field but no calls |
| `internal/server/config_update_service.go` | noop | — | field but no calls |
| `internal/server/dashboard_service.go` | noop | — | field but no calls |
| `internal/server/dedup_engine.go` | noop | — | field but no calls — was originally a proof-point candidate; swapped for `reconcile.go` |
| `internal/server/deluge_integration.go` | read-write | `BookReader`, `BookVersionStore` | |
| `internal/server/diagnostics_service.go` | noop | — | field but no calls |
| `internal/server/duplicates_handlers.go` | read-write | `AuthorStore`, `BookStore`, `SeriesStore`, `OperationStore` | multi-domain |
| `internal/server/external_id_backfill.go` | read-write | `BookReader`, `BookFileStore`, `SettingsStore` | |
| `internal/server/file_move.go` | write-only | `BookWriter` | |
| `internal/server/import_collision.go` | read-only | `BookReader` | clean read-only example |
| `internal/server/import_path_service.go` | noop | — | field but no calls |
| `internal/server/import_service.go` | noop | — | field but no calls |
| `internal/server/indexed_store.go` | read-write | `BookStore` | **special — this type wraps `Store` and forwards book CRUD. Narrow both the wrapper's field and the forwarded surface.** |
| `internal/server/isbn_enrichment.go` | noop | — | field but no calls |
| `internal/server/itl_rebuild.go` | read-only | `BookReader` | |
| `internal/server/itunes.go` | read-write | `BookStore`, `AuthorReader`, `AuthorWriter`, `SeriesReader`, `SeriesWriter`, `BookFileStore`, `HashBlocklistStore`, `ITunesStateStore` | wide surface but narrow vs. full `Store` |
| `internal/server/itunes_position_sync.go` | read-write | `BookStore`, `BookFileStore`, `UserPositionStore` | |
| `internal/server/itunes_track_provisioner.go` | read-write | `AuthorReader`, `BookFileStore`, `ExternalIDStore` | |
| `internal/server/maintenance_fixups.go` | read-write | `BookStore`, `AuthorStore`, `SeriesStore`, `BookFileStore`, `ExternalIDStore`, `StatsStore`, `UserTagStore` | widest surface after `itunes.go` |
| `internal/server/merge_service.go` | noop | — | field but no calls |
| `internal/server/metadata_batch_candidates.go` | read-write | `BookReader`, `OperationStore`, `RawKVStore` | |
| `internal/server/metadata_fetch_service.go` | noop | — | field but no calls |
| `internal/server/metadata_state_service.go` | noop | — | field but no calls |
| `internal/server/metadata_upgrade.go` | noop | — | field but no calls |
| `internal/server/middleware/auth.go` | read-write | `UserReader`, `RoleStore`, `SessionStore` | |
| `internal/server/organize_preview_service.go` | noop | — | field but no calls |
| `internal/server/organize_service.go` | noop | — | field but no calls |
| `internal/server/pipeline_checkpoint.go` | read-write | `UserPreferenceStore` | |
| `internal/server/playlist_evaluator.go` | read-write | `BookReader`, `UserPositionStore` | **proof-point — done in this PR** |
| `internal/server/playlist_itunes_sync.go` | read-write | `UserPlaylistStore` | |
| `internal/server/read_status_engine.go` | read-write | `BookFileStore`, `UserPositionStore` | |
| `internal/server/reconcile.go` | read-write | `BookStore`, `BookFileStore`, `ImportPathStore`, `OperationStore` | **proof-point — done in this PR** |
| `internal/server/rename_service.go` | noop | — | field but no calls |
| `internal/server/revert_service.go` | noop | — | field but no calls |
| `internal/server/scan_service.go` | noop | — | field but no calls |
| `internal/server/server.go` | read-write | full `Store` — keep as-is | bootstrap; legitimate wide-access consumer |
| `internal/server/sweeper.go` | read-write | `BookStore` | tombstone cleanup |
| `internal/server/system_service.go` | noop | — | field but no calls |
| `internal/server/undo_engine.go` | read-write | `BookStore`, `OperationStore` | |
| `internal/server/version_fingerprint.go` | read-write | `BookVersionStore` | |
| `internal/server/version_ingest.go` | read-write | `BookVersionStore`, `BookFileStore` | |
| `internal/server/version_lifecycle.go` | read-write | `BookReader`, `BookVersionStore`, `BookFileStore` | |
| `internal/server/version_swap.go` | read-write | `BookStore`, `BookVersionStore`, `BookFileStore` | |
| `internal/server/work_service.go` | noop | — | field but no calls |
| `internal/server/writeback_outbox.go` | read-write | `BookReader`, `UserPreferenceStore` | |

### `internal/server/` (tests)

| File | Class | Interfaces | Notes |
|---|---|---|---|
| `internal/server/cover_history_test.go` | test | `BookWriter`, `LifecycleStore` | |
| `internal/server/entity_tag_handlers_test.go` | test | `AuthorWriter`, `SeriesWriter`, `LifecycleStore` | |
| `internal/server/import_collision_test.go` | test | `BookWriter`, `LifecycleStore` | |
| `internal/server/middleware/auth_permission_test.go` | test | `UserWriter`, `SessionStore`, `LifecycleStore` | |
| `internal/server/read_status_engine_test.go` | test | `BookFileStore`, `UserPositionStore`, `LifecycleStore` | |
| `internal/server/revert_service_organize_test.go` | test | `LifecycleStore` | |
| `internal/server/server_test.go` | test | `LifecycleStore` | |
| `internal/server/undo_engine_prop_test.go` | test | `OperationStore`, `LifecycleStore` | |
| `internal/server/user_handlers_test.go` | test | `UserWriter`, `RoleStore`, `LifecycleStore` | |
| `internal/server/version_lifecycle_prop_test.go` | test | `BookVersionStore`, `BookWriter`, `LifecycleStore` | |
| `internal/server/version_lifecycle_test.go` | test | `BookVersionStore`, `BookWriter`, `LifecycleStore` | |

### Mock files (do not edit by hand)

| File | Class | Notes |
|---|---|---|
| `internal/database/mocks/mock_store_coverage_test.go` | noop | regenerated by mockery; adjust only if coverage test needs to call the new mocks |
| `internal/operations/mocks/mock_queue.go` | noop | regenerated; references `database.Store` transitively |

### Counts

- Eligible for migration: **61 files** (79 − 18 noop)
- Done in this PR (proof-points): **3** (audiobook_service, playlist_evaluator, reconcile)
- Remaining for follow-on PR: **58**
- Field-but-no-calls noops for separate cleanup: **18**

## 7. Risks and mitigations

- **Noop fields hiding real dependencies.** Some of the 18 noops may actually *use* the store through a method on a different receiver. Mitigation: before removing a field, grep for `<field>\.` to confirm no dynamic-dispatch call sites.
- **Mockery config bloat.** ~40 new mock files. Mitigation: group them under one mockery block; CI only runs `mockery-check` which diffs fast.
- **Test flakiness from mock surface changes.** Narrowing a mock can reveal that a test was relying on an unrelated call. Mitigation: each proof-point migration commits only after `make test` is green.
- **Interface drift.** A future method added to `Store` but not to the right sub-interface breaks mocks quietly. Mitigation: the new sub-interfaces are *where* methods get defined; `Store` only *embeds* them. Adding a method to the wrong sub-interface is a compile error at the `PebbleStore` call site.
- **`indexed_store.go` wrapper.** This type wraps `Store` to layer bleve indexing on writes. Its interface must stay wide enough to forward every method it currently forwards. Mitigation: survey its forwarded surface first, narrow to the specific interfaces it forwards.

## 8. Open questions

None blocking. Remaining decisions (e.g., exact placement of `TagStore` methods between three types, handling of the `logger.OperationStore` name collision) are resolved during implementation and documented in the implementation plan.

## 9. Success criteria

- All 9 PRs listed in the implementation plan land.
- `make test` green after every PR.
- `mockery` produces 40+ new mock files; `mocks-check` CI gate stays green.
- The three proof-point tests show a measurable mock-size reduction (expected: ≥ 80% fewer expected-call stubs).
- The migration catalog is complete enough that a follow-on agent can finish the 58 remaining files without asking a human to reclassify any row.
