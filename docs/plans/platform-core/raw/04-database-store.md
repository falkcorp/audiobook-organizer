<!-- file: docs/plans/platform-core/raw/04-database-store.md -->
<!-- version: 1.0.0 -->
<!-- guid: 04584bc2-544e-4946-8150-f02169e3e93f -->
<!-- last-edited: 2026-11-03 -->

# 04 - `database.Store` interface (raw scout data)

Anchor commit: `748fd787a` (the code the anchors were taken from; a docs-only commit at the time of writing was later rebased away). All anchors are `path:line` at that commit. Counts were produced programmatically with a go/ast walk that resolves embedded interfaces recursively inside `internal/database` (non-test files); the ad-hoc tool lives outside the repo and is not committed.

## 1. Headline numbers

| Fact | Value | Anchor |
|---|---|---|
| Exact method count of `database.Store` (flattened, de-duplicated; 445 total = 445 unique, no method is declared twice) | **445** | `internal/database/store.go:48` |
| Direct methods declared on `Store` | 0 (pure composition) | `internal/database/store.go:48-55` |
| Embedded composites (unexported, deliberately) | 6: `catalogStore` 132, `mediaStore` 75, `accountStore` 44, `enrichmentStore` 60, `operationsStore` 92, `platformStore` 42 (sum 445) | `internal/database/store.go:58,70,99,111,123,134` |
| Leaf interfaces that carry the methods | 110 leaf interfaces declare >=1 direct method; the six composites embed 40 exported sub-interfaces between them (7+7+7+7+6+6 -- `BookStore`, `AuthorStore`, ... -- matching the "40" in the `store.go` comment) | `internal/database/iface_*.go`, `store.go` |
| Stale claim in CLAUDE.md / comments | CLAUDE.md and `migrations.go:18` say Store is 398 methods; measured 445 now | `internal/database/migrations.go:18` |
| Capability interfaces that live OFF `Store` (reached by `database.AsCapability[T]` or concrete `*PebbleStore` assertion) | 115 exported interfaces, 283 methods (e.g. `OpsV3Store` 28, `ActivityStorer` 9, `FileProvenanceStore` 8, `VectorANNStore` 6, `SyncIdentityStore` 6, `BookmarkStore` 5) | `internal/database/store_capability.go:89`; list in section 6 |
| Concrete `*PebbleStore` exported methods (all files in `internal/database`, non-test) | 772 (so ~327 exported methods exist beyond the 445 on `Store`) | `internal/database/pebble_store*.go` etc. |

Store composition comment (the "why"): it is a REGROUPING of the former flat list of 40 sub-interfaces, done to satisfy the `interfacebloat` linter; the six composites are unexported on purpose so nobody can name a wide type (`internal/database/store.go:18-45`). The file states the intended idiom is a consumer-side interface declared where it is used (it claims 250 exist outside the package, median 2 methods; my measurement below finds 591 interfaces whose methods are all a subset of Store, median 3 - the claim is directionally right, numerically stale).

## 2. Composition tree (method counts, flattened)

```
Store 445
  catalogStore 132 (store.go:58)
    BookStore 63 (iface_book.go:425) = BookReader 40 + BookWriter 19 + BookCompletenessReader 2 + BookPathIndexStatus 1 + BookDirLister 1
    AuthorStore 25 (iface_author.go:120)   SeriesStore 11 (iface_series.go:37)   NarratorStore 8 (iface_catalog.go:107)
    WorkStore 9 (iface_catalog.go:95)      BookVersionStore 10 (iface_catalog.go:152)   CollectionStore 6 (iface_catalog.go:24)
  mediaStore 75 (store.go:70)
    BookFileStore 44 (iface_bookfile.go:292)  BookSegmentStore 6 (iface_bookfile.go:315)  ChapterReader 1 (store.go:94)
    PlaylistStore 5 (iface_user.go:102)  UserPlaylistStore 9 (iface_user.go:89)  PathHistoryStore 2 (iface_itunes.go:57)  PlaybackStore 8 (iface_user.go:111)
  accountStore 44 (store.go:99)
    UserStore 7 (iface_user.go:29)  SessionStore 7 (iface_auth.go:70)  OAuthIdentityStore 3 (iface_oauth.go:9)  RoleStore 6 (iface_auth.go:60)
    APIKeyStore 9 (iface_auth.go:52)  InviteStore 5 (iface_auth.go:83)  UserPreferenceStore 7 (iface_user.go:51)
  enrichmentStore 60 (store.go:111)
    MetadataStore 7 (iface_metadata.go:249)  MetadataCacheStore 4 (iface_metadata.go:200)  RejectedMetadataStore 3 (iface_metadata.go:257)
    ExternalIDStore 11 (iface_itunes.go:50)  TagStore 26 (iface_tags.go:77)  UserTagStore 4 (iface_tags.go:87)  HashBlocklistStore 5 (iface_bookfile.go:325)
  operationsStore 92 (store.go:123)
    OperationStore 27 (iface_ops.go:104)  OpsV2Store 39 (iface_ops_v2.go:410)  MaintenanceStore 8 (iface_system.go:53)
    SystemActivityStore 3 (iface_system.go:44)  AIJobsStore 10 (iface_metadata.go:265)  ReviewStore 5 (iface_review.go:18)
  platformStore 42 (store.go:134)
    LifecycleStore 2 (iface_system.go:96)  ImportPathStore 7 (iface_system.go:102)  ITunesStateStore 6 (iface_itunes.go:11)
    RawKVStore 7 (iface_system.go:73)  SettingsStore 4 (iface_system.go:20)  StatsStore 8 (iface_system.go:28)  UserPositionStore 8 (iface_user.go:39)
```

Two sub-interfaces sit in a file whose name does not match their domain (e.g. `HashBlocklistStore` in `iface_bookfile.go:325`, `AIJobsStore` in `iface_metadata.go:265`, `ExternalIDStore` in `iface_itunes.go:50`, `UserPositionStore` is in `platformStore` although it is a listening-state interface). Worth knowing when re-homing.

## 3. Methods grouped by domain: counts, callers, generic vs audiobook-specific

Method: for each leaf interface every method name was grepped as `.Name(` across all non-test `.go` files outside `internal/database`, excluding `mocks/`, `tools/`, `web/`, `third_party/`, `testdata/`. "Caller packages" = distinct package directories with at least one hit. CAVEATS: textual match, so a name shared with a non-Store type (`Close`, `Reset`, `Optimize`, `GetSetting`, `GetSession`, ...) over-reports; callers that reach a method through a consumer-side narrow interface ARE counted (good), callers that go through reflection or `AsCapability` on a different interface are counted only if the method name appears. `internal/database/dbtest` appears as a "caller" because it is a non-test helper package.

| # | Domain group | Methods | Owning composite | Caller pkgs (distinct) | Platform-core verdict |
|---|---|---|---|---|---|
| 1 | Books (catalog core) | 63 | catalogStore | 55 | **Audiobook-specific** |
| 2 | Authors / series / narrators / works | 53 | catalogStore | 40 | **Audiobook-specific** |
| 3 | Versions / collections | 16 | catalogStore | 11 | **Mixed** |
| 4 | Files / segments / fingerprints | 58 | mediaStore | 43 | **Mixed** |
| 5 | Playlists / playback / positions | 30 | mediaStore + platformStore | 12 | **Audiobook-specific** |
| 6 | Accounts / auth / RBAC | 44 | accountStore | 14 | **Generic** |
| 7 | Enrichment (metadata, external IDs) | 25 | enrichmentStore | 22 | **Mixed** |
| 8 | Tags | 30 | enrichmentStore | 13 | **Mixed** |
| 9 | Operations v1 (legacy task rows) | 27 | operationsStore | 18 | **Generic** |
| 10 | Operations v2 (task/job system) | 39 | operationsStore | 18 | **Generic** |
| 11 | Maintenance / activity / AI jobs / review | 26 | operationsStore | 12 | **Mixed** |
| 12 | Platform (settings, KV, import roots, stats, lifecycle) | 34 | platformStore | 39 excluding Close/Reset noise (77 raw) | **Mixed** |
| | **Total** | **445** | | | |

Verdict = my judgement of whether the group would belong in a shared platform core (Generic), is tied to the audiobook domain (Audiobook-specific), or splits (Mixed).

### 3.1 Books (catalog core) - 63 methods, 55 caller packages

Entity is the Book row (ISBN/ASIN/iTunes PID/narrator fields); a Sonarr-like would have its own Series/Episode entity. The soft-delete/tombstone/snapshot (book_ver) mechanics are a generic pattern worth extracting, but the method signatures are Book-typed.

Leaf interfaces (methods / caller pkgs): `BookByIDReader` 3/41 (internal/database/iface_book.go:26); `BookBulkReader` 4/26 (internal/database/iface_book.go:40); `BookNaturalKeyReader` 8/11 (internal/database/iface_book.go:85); `BookPathSetReader` 1/6 (internal/database/iface_book.go:105); `BookDuplicateReader` 4/2 (internal/database/iface_book.go:179); `BookRelationReader` 4/20 (internal/database/iface_book.go:194); `BookSearchReader` 5/4 (internal/database/iface_book.go:227); `BookCountReader` 3/6 (internal/database/iface_book.go:250); `BookSnapshotReader` 2/4 (internal/database/iface_book.go:260); `BookLifecycleReader` 4/7 (internal/database/iface_book.go:266); `BookITunesReader` 2/0 (internal/database/iface_book.go:274); `BookMutator` 7/31 (internal/database/iface_book.go:306); `BookSyncMarker` 2/3 (internal/database/iface_book.go:335); `BookVersionMutator` 3/3 (internal/database/iface_book.go:341); `BookTombstoneWriter` 2/2 (internal/database/iface_book.go:350); `BookScanFailStore` 3/2 (internal/database/iface_book.go:356); `BookAggregateWriter` 2/5 (internal/database/iface_book.go:364); `BookCompletenessReader` 2/3 (internal/database/iface_book.go:407); `BookPathIndexStatus` 1/2 (internal/database/iface_book.go:161); `BookDirLister` 1/3 (internal/database/iface_book.go:143)

Caller packages: `cmd`, `internal/audiobooks`, `internal/authorcredit`, `internal/authormerge`, `internal/batch`, `internal/catalog`, `internal/database/dbtest`, `internal/decisions`, `internal/dedup`, `internal/dedup/dataset`, `internal/deluge`, `internal/diagnostics`, `internal/foldercover`, `internal/foldernames`, `internal/importer`, `internal/itunes`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/merge`, `internal/metabatch`, `internal/metadata`, `internal/metafetch`, `internal/operations/opswriter`, `internal/operations/registry`, `internal/organizer`, `internal/playlist`, `internal/plugins/acoustid`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/quarantine`, `internal/reconcile`, `internal/repairs`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/dedup`, `internal/server/handlers/duplicates`, `internal/server/handlers/entities`, `internal/server/handlers/metadata`, `internal/server/handlers/operations`, `internal/server/handlers/system`, `internal/sweep`, `internal/transcode`, `internal/undo`, `internal/versionprimary`, `internal/versionprimary/vptest`, `internal/versions`, `internal/writeback`, `pkg/ops/opstest`, `pkg/plugin/sdk`

### 3.2 Authors / series / narrators / works - 53 methods, 40 caller packages

People/series/work graph with aliases, tombstone chains and author-name folding. Narrator and Work are audiobook concepts; Series/people-credit could generalise but are intertwined with Book.

Leaf interfaces (methods / caller pkgs): `AuthorLookupReader` 5/27 (internal/database/iface_author.go:11); `AuthorAliasReader` 3/4 (internal/database/iface_author.go:22); `AuthorBookReader` 5/21 (internal/database/iface_author.go:29); `AuthorCountReader` 2/6 (internal/database/iface_author.go:52); `AuthorWriter` 8/16 (internal/database/iface_author.go:72); `BookAuthorModifier` 1/9 (internal/database/iface_author.go:87); `AuthorMinter` 1/2 (internal/database/iface_author.go:98); `SeriesReader` 6/26 (internal/database/iface_series.go:9); `SeriesWriter` 5/12 (internal/database/iface_series.go:21); `NarratorStore` 8/10 (internal/database/iface_catalog.go:107); `WorkStore` 7/4 (internal/database/iface_catalog.go:95); `WorkScanStore` 2/1 (internal/database/iface_catalog.go:83)

Caller packages: `cmd`, `internal/aiscan`, `internal/applygate`, `internal/audiobooks`, `internal/authorcredit`, `internal/authormerge`, `internal/batch`, `internal/catalog`, `internal/database/dbtest`, `internal/decisions`, `internal/dedup`, `internal/diagnostics`, `internal/foldernames`, `internal/importer`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/merge`, `internal/metabatch`, `internal/metadata`, `internal/metafetch`, `internal/operations/opswriter`, `internal/organizer`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/repairs`, `internal/scanner`, `internal/search`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks`, `internal/server/handlers/dedup`, `internal/server/handlers/duplicates`, `internal/server/handlers/entities`, `internal/server/handlers/operations`, `internal/server/handlers/system`, `internal/undo`, `internal/work`, `pkg/ops/opstest`

### 3.3 Versions / collections - 16 methods, 11 caller packages

BookVersion (per-torrent/format copies, trash/purge/swap dispositions) is audiobook-flavoured but resembles a Sonarr "release/quality profile" concept; Collection is a generic named grouping.

Leaf interfaces (methods / caller pkgs): `BookVersionReader` 4/9 (internal/database/iface_catalog.go:124); `BookVersionWriter` 3/3 (internal/database/iface_catalog.go:132); `BookVersionDispositionReader` 3/2 (internal/database/iface_catalog.go:139); `CollectionStore` 6/5 (internal/database/iface_catalog.go:24)

Caller packages: `internal/audiobooks`, `internal/deluge`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/repairs`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks`, `internal/undo`, `internal/versions`

### 3.4 Files / segments / fingerprints - 58 methods, 43 caller packages

A media-file table with path/hash/path-key is generic to any library manager. AcoustID/fingerprint windows, segments, chapters, iTunes PID and Deluge import hooks are audiobook/ABO-specific.

Leaf interfaces (methods / caller pkgs): `BookFileReader` 7/37 (internal/database/iface_bookfile.go:19); `BookFileCreator` 2/9 (internal/database/iface_bookfile.go:44); `BookFileUpserter` 6/19 (internal/database/iface_bookfile.go:73); `BookFileBatchUpserter` 3/2 (internal/database/iface_bookfile.go:56); `BookFileMover` 2/7 (internal/database/iface_bookfile.go:99); `BookFileDeleter` 3/1 (internal/database/iface_bookfile.go:129); `BookFileHashStore` 4/3 (internal/database/iface_bookfile.go:165); `BookFileFingerprintStore` 4/4 (internal/database/iface_bookfile.go:195); `FingerprintWindowStore` 8/3 (internal/database/iface_bookfile.go:219); `BookFileITunesStore` 1/1 (internal/database/iface_bookfile.go:250); `BookFileDelugeStore` 1/3 (internal/database/iface_bookfile.go:255); `BookFileStatsStore` 3/1 (internal/database/iface_bookfile.go:270); `BookSegmentStore` 6/0 (internal/database/iface_bookfile.go:315); `ChapterReader` 1/5 (internal/database/store.go:94); `PathHistoryStore` 2/6 (internal/database/iface_itunes.go:57); `HashBlocklistStore` 5/4 (internal/database/iface_bookfile.go:325)

Caller packages: `internal/activity`, `internal/applygate`, `internal/audiobooks`, `internal/bookfiles`, `internal/catalog`, `internal/dedup`, `internal/dedup/dataset`, `internal/deluge`, `internal/fileops`, `internal/foldercover`, `internal/importer`, `internal/itunes`, `internal/itunes/service`, `internal/itunesguard`, `internal/maintenance/jobs`, `internal/merge`, `internal/metabatch`, `internal/metafetch`, `internal/operations/opswriter`, `internal/organizer`, `internal/plugins/acoustid`, `internal/plugins/dedup`, `internal/plugins/deluge`, `internal/plugins/maintenance`, `internal/quarantine`, `internal/readstatus`, `internal/reconcile`, `internal/remux`, `internal/repairs`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/dedup`, `internal/server/handlers/system`, `internal/transcode`, `internal/undo`, `internal/versionprimary`, `internal/versionprimary/vptest`, `internal/versions`, `pkg/ops/opstest`

### 3.5 Playlists / playback / positions - 30 methods, 12 caller packages

Listening state (progress, bookmarks-adjacent positions, listen stats, smart/user playlists with iTunes sync flags). No Sonarr analogue.

Leaf interfaces (methods / caller pkgs): `PlaylistStore` 5/1 (internal/database/iface_user.go:102); `UserPlaylistReader` 6/7 (internal/database/iface_user.go:65); `UserPlaylistWriter` 3/4 (internal/database/iface_user.go:78); `PlaybackStore` 8/0 (internal/database/iface_user.go:111); `UserPositionStore` 8/9 (internal/database/iface_user.go:39)

Caller packages: `internal/audiobooks`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/merge`, `internal/playlist`, `internal/plugins/maintenance`, `internal/readstatus`, `internal/repairs`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/sysinfo`, `internal/undo`

### 3.6 Accounts / auth / RBAC - 44 methods, 14 caller packages

Users, sessions (incl. ABS-session cleanup, one audiobook-ish method), OAuth identities, roles, API keys, invites, per-user preferences. Clean platform-core candidate; only DeleteExpiredABSSessions and the ABS-session origin variant are app-specific.

Leaf interfaces (methods / caller pkgs): `UserReader` 5/11 (internal/database/iface_user.go:11); `UserWriter` 2/4 (internal/database/iface_user.go:23); `SessionStore` 7/4 (internal/database/iface_auth.go:70); `OAuthIdentityStore` 3/1 (internal/database/iface_oauth.go:9); `RoleStore` 6/3 (internal/database/iface_auth.go:60); `APIKeyReader` 4/3 (internal/database/iface_auth.go:19); `APIKeyWriter` 1/2 (internal/database/iface_auth.go:27); `APIKeyLifecycleStore` 3/2 (internal/database/iface_auth.go:32); `APIKeyUsageRecorder` 1/1 (internal/database/iface_auth.go:43); `InviteStore` 5/2 (internal/database/iface_auth.go:83); `UserPreferenceStore` 7/5 (internal/database/iface_user.go:51)

Caller packages: `internal/audiobooks`, `internal/auth`, `internal/maintenance/jobs`, `internal/merge`, `internal/oauth`, `internal/organizer`, `internal/plugins/maintenance`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/abs`, `internal/server/handlers/system`, `internal/server/middleware`, `internal/usermerge`, `internal/writeback`

### 3.7 Enrichment (metadata, external IDs) - 25 methods, 22 caller packages

Provider fetch cache, rejection list, field-lock state, change history and external-ID mapping/tombstones are generic "metadata provider" machinery, but are typed on Book and on audiobook fields.

Leaf interfaces (methods / caller pkgs): `MetadataFieldStateStore` 3/7 (internal/database/iface_metadata.go:221); `MetadataChangeStore` 3/10 (internal/database/iface_metadata.go:228); `AlternativeTitleStore` 1/1 (internal/database/iface_metadata.go:237); `MetadataCacheStore` 4/5 (internal/database/iface_metadata.go:200); `RejectedMetadataStore` 3/1 (internal/database/iface_metadata.go:257); `ExternalIDReader` 3/10 (internal/database/iface_itunes.go:21); `ExternalIDWriter` 4/3 (internal/database/iface_itunes.go:28); `ExternalIDLifecycle` 4/7 (internal/database/iface_itunes.go:36)

Caller packages: `internal/activity`, `internal/audiobooks`, `internal/authorcredit`, `internal/dedup`, `internal/itunes`, `internal/itunes/service`, `internal/itunesguard`, `internal/maintenance/jobs`, `internal/merge`, `internal/metadata`, `internal/metafetch`, `internal/operations/opswriter`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/repairs`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/audiobooks`, `internal/server/handlers/dedup`, `internal/server/handlers/metadata`, `internal/undo`, `pkg/ops/opstest`

### 3.8 Tags - 30 methods, 13 caller packages

Same tag API replicated per entity (book/author/series) plus per-user tags; the shape is generic, the entity binding is not.

Leaf interfaces (methods / caller pkgs): `BookTagReader` 5/12 (internal/database/iface_tags.go:9); `BookTagWriter` 5/5 (internal/database/iface_tags.go:23); `AuthorTagReader` 4/0 (internal/database/iface_tags.go:33); `AuthorTagWriter` 4/0 (internal/database/iface_tags.go:41); `SeriesTagReader` 4/0 (internal/database/iface_tags.go:50); `SeriesTagWriter` 4/0 (internal/database/iface_tags.go:58); `UserTagStore` 4/3 (internal/database/iface_tags.go:87)

Caller packages: `internal/applygate`, `internal/audiobooks`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/metafetch`, `internal/organizer`, `internal/plugins/maintenance`, `internal/repairs`, `internal/search`, `internal/server`, `internal/server/handlers/abs`, `internal/server/handlers/audiobooks`, `internal/undo`

### 3.9 Operations v1 (legacy task rows) - 27 methods, 18 caller packages

Task/operation rows, logs, results, undo-able change records (OperationChange carries book ids). Legacy of v2/v3; core candidate once the Book-typed change record is generalised.

Leaf interfaces (methods / caller pkgs): `OperationLifecycleStore` 4/3 (internal/database/iface_ops.go:14); `OperationReader` 5/6 (internal/database/iface_ops.go:23); `OperationStateStore` 3/3 (internal/database/iface_ops.go:32); `OperationChangeStore` 4/11 (internal/database/iface_ops.go:40); `OperationLogStore` 5/3 (internal/database/iface_ops.go:52); `OperationResultStore` 3/3 (internal/database/iface_ops.go:63); `OperationPruner` 3/1 (internal/database/iface_ops.go:75)

Caller packages: `internal/activity`, `internal/audiobooks`, `internal/maintenance/jobs`, `internal/merge`, `internal/metabatch`, `internal/operations`, `internal/operations/opswriter`, `internal/operations/registry`, `internal/plugins/maintenance`, `internal/repairs`, `internal/scheduler`, `internal/server`, `internal/server/handlers/audiobooks`, `internal/server/handlers/operations`, `internal/sysinfo`, `internal/testutil`, `internal/undo`, `pkg/ops/opstest`

### 3.10 Operations v2 (task/job system) - 39 methods, 18 caller packages

Op definitions, runs, queue, state/checkpoint, dependency revision, completion, batch bucket. Almost entirely domain-free; the task-system core. (The off-Store OpsV3Store, 28 methods, is its successor.)

Leaf interfaces (methods / caller pkgs): `OpDefV2Store` 2/2 (internal/database/iface_ops_v2.go:178); `OpV2RunStore` 7/16 (internal/database/iface_ops_v2.go:199); `OpV2StatusStore` 4/3 (internal/database/iface_ops_v2.go:254); `OpV2QueueStore` 7/11 (internal/database/iface_ops_v2.go:281); `OpV2StateStore` 4/2 (internal/database/iface_ops_v2.go:314); `OpV2ObservabilityStore` 7/3 (internal/database/iface_ops_v2.go:327); `OpV2DepStore` 2/2 (internal/database/iface_ops_v2.go:360); `OpV2CompletionStore` 3/2 (internal/database/iface_ops_v2.go:370); `OpV2BatchStore` 3/2 (internal/database/iface_ops_v2.go:385)

Caller packages: `internal/diagnostics`, `internal/maintenance/jobs`, `internal/metabatch`, `internal/operations/childop`, `internal/operations/registry`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/repairs`, `internal/scheduler`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/opaccess`, `internal/server/handlers/operations`, `internal/server/handlers/repairs`, `internal/testutil`, `internal/usermerge`, `pkg/ops/opstest`

### 3.11 Maintenance / activity / AI jobs / review - 26 methods, 12 caller packages

SystemActivity log and Optimize/CompactionStats are generic. Scan-cache/dirty-folder methods in MaintenanceStore are library-scanner specific. AIJobs (provider batch jobs) and Review (human queue) are generic mechanisms with ABO payloads.

Leaf interfaces (methods / caller pkgs): `MaintenanceStore` 8/6 (internal/database/iface_system.go:53); `SystemActivityStore` 3/3 (internal/database/iface_system.go:44); `AIJobReader` 4/3 (internal/database/iface_metadata.go:271); `AIJobWriter` 6/1 (internal/database/iface_metadata.go:279); `ReviewStore` 5/2 (internal/database/iface_review.go:18)

Caller packages: `internal/ai/aijobs`, `internal/diagnostics`, `internal/logger`, `internal/metafetch`, `internal/organizer`, `internal/plugins/maintenance`, `internal/scanner`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/audiobooks`, `internal/server/handlers/review`, `internal/server/handlers/system`

### 3.12 Platform (settings, KV, import roots, stats, lifecycle) - 34 methods, 39 caller packages

Settings, RawKV, Lifecycle (Close/Reset) are generic. ImportPath = library root folders (Sonarr also has root folders; generic-ish). Stats (book/file/author/series counts, dashboard) and ITunesState are ABO-specific.

Leaf interfaces (methods / caller pkgs): `ImportPathStore` 7/13 (internal/database/iface_system.go:102); `ITunesStateStore` 6/2 (internal/database/iface_itunes.go:11); `RawKVStore` 7/21 (internal/database/iface_system.go:73); `SettingsStore` 4/11 (internal/database/iface_system.go:20); `StatsStore` 8/8 (internal/database/iface_system.go:28); `LifecycleStore` 2/56 (internal/database/iface_system.go:96)

Caller packages: `cmd`, `internal/ai/resultjournal`, `internal/audiobooks`, `internal/authority`, `internal/authority/authoritybuild`, `internal/catalog`, `internal/config`, `internal/covertext`, `internal/decisions`, `internal/diagnostics`, `internal/fileops`, `internal/importer`, `internal/itunes`, `internal/itunes/service`, `internal/maintenance/jobs`, `internal/merge`, `internal/metabatch`, `internal/metadata/dailyquota`, `internal/metafetch`, `internal/operations/registry`, `internal/plugins/dedup`, `internal/plugins/maintenance`, `internal/plugins/metafetch`, `internal/reconcile`, `internal/remux`, `internal/scanner`, `internal/scheduler`, `internal/server`, `internal/server/handlers`, `internal/server/handlers/admindebug`, `internal/server/handlers/audiobooks`, `internal/server/handlers/entities`, `internal/server/handlers/operations`, `internal/server/handlers/repairs`, `internal/server/handlers/system`, `internal/sysinfo`, `internal/usermerge`, `internal/versions`, `pkg/ops/opstest`

### 3.x Observations on callers

- Caller breadth is dominated by `internal/server`, `internal/server/handlers/*`, `internal/plugins/*`, `internal/maintenance/jobs`, `internal/audiobooks`, `internal/merge`, `internal/undo`, `internal/scanner` and `internal/metafetch`; `cmd` touches Books/Authors/Series/Settings/Lifecycle.
- Leaves with zero non-database callers by textual grep: `BookITunesReader`, `BookSegmentStore`, `PlaybackStore`, `AuthorTagReader/Writer`, `SeriesTagReader/Writer` (they may be reached only through `database` internals, HTTP handlers via capability assertion with a different spelling, or be dead; not verified).
- `LifecycleStore.Close` accounts for 56 raw caller packages and is not meaningful as coupling.

## 4. Narrowed store interfaces already declared in consumer packages

Detection rule (heuristic, reproducible): every non-test interface declared outside `internal/database` (excluding `mocks/`) whose complete flattened method set is a subset of `Store`'s 445 method names (embeds of `database.*` interfaces count as subset members). This catches the intended "narrow consumer interface" pattern but also incidental ones (a one-method `Close() error` interface qualifies).

- **591** such interfaces; **372** unexported, 219 exported; 297 are named `*Store`.
- Method-count distribution: median 3, mean 4.1, max 101. Buckets: 1: 173, 2-3: 189, 4-8: 183, 9-20: 39, 21-50: 4, >50: 3.
- By top-level package: `internal/server` 164, `internal/plugins` 120, `internal/maintenance` 37, `internal/audiobooks` 31, `internal/merge` 29, `internal/dedup` 19, `internal/undo` 15, `internal/repairs` 14, `internal/versions` 14, `internal/operations` 13, `internal/metafetch` 12, `internal/organizer` 12, `internal/metabatch` 11, `internal/itunes` 9.
- Counts exclude embedded `database.*` sub-interfaces that could not be flattened from another package: a few rows embed one (the `ext` column in the raw scan was 0-3); their true width is larger than shown. Examples: `ServerOpsStore` embeds one `database.*` interface, `OpsStore` embeds two, `JobStore` three.
- A second set (132 interfaces named `*Store`) is NOT a subset of Store: it adds non-Store methods (e.g. `internal/dedup/store.go:161` `Store`, `internal/scanner/store.go:146` `scannerStore`, `internal/metafetch/service.go:181` `Store`, `internal/merge/store.go:200` `Store`, `internal/organizer/service.go:128` `Store`, `internal/itunes/service/store.go:33` `Store`, `internal/audiobooks/service.go:194` `audiobookStore`). These are consumer interfaces that mix Store methods, embeds and package-local collaborators.

### 4.1 The wide ones (>= 9 methods; 46 of 591)

| Interface | Methods | Declared at |
|---|---|---|
| `ServerOpsStore` | 101 | `internal/server/server_ops_store.go:42` |
| `OpsStore` | 82 | `internal/plugins/maintenance/deps.go:403` |
| `JobStore` | 60 | `internal/maintenance/job.go:442` |
| `EntitiesStore` | 32 | `internal/server/handlers/entities/interfaces.go:109` |
| `serverBookStore` | 26 | `internal/server/server_ops_store.go:55` |
| `AudiobooksStore` | 21 | `internal/server/handlers/audiobooks/interfaces.go:125` |
| `serverEntityStore` | 21 | `internal/server/server_ops_store.go:63` |
| `jobBookStore` | 17 | `internal/maintenance/job.go:300` |
| `OperationsStore` | 17 | `internal/server/handlers/operations/interfaces.go:74` |
| `SystemStore` | 17 | `internal/server/handlers/system/interfaces.go:108` |
| `serverOperationStore` | 17 | `internal/server/server_ops_store.go:77` |
| `pluginStore` | 14 | `internal/plugins/acoustid/plugin.go:106` |
| `opsPeopleStore` | 14 | `internal/plugins/maintenance/deps.go:390` |
| `fsRegroupStore` | 14 | `internal/plugins/maintenance/store_slices.go:150` |
| `itunesRegroupStore` | 14 | `internal/plugins/maintenance/store_slices.go:188` |
| `UserStateRepairStore` | 13 | `internal/merge/repair_user_state.go:32` |
| `opsHousekeeping` | 13 | `internal/plugins/maintenance/deps.go:304` |
| `contributorResolver` | 12 | `internal/audiobooks/service.go:96` |
| `Store` | 12 | `internal/diagnostics/store.go:41` |
| `jobBookFileStore` | 12 | `internal/maintenance/job.go:340` |
| `jobOperationStore` | 12 | `internal/maintenance/job.go:417` |
| `opsBookFileWriter` | 12 | `internal/plugins/maintenance/deps.go:191` |
| `Store` | 12 | `internal/server/handlers/admindebug/handler.go:44` |
| `VersionsStore` | 12 | `internal/server/handlers/versions.go:87` |
| `serverAuthStore` | 12 | `internal/server/server_ops_store.go:84` |
| `serverConfigStore` | 12 | `internal/server/server_ops_store.go:90` |
| `UserProgressMerger` | 11 | `internal/merge/store.go:99` |
| `authorPathLinkStore` | 11 | `internal/plugins/maintenance/author_path_link.go:359` |
| `AuthStore` | 11 | `internal/server/handlers/auth.go:82` |
| `ReclassifyStore` | 11 | `internal/server/handlers/entities/reclassify.go:23` |
| `SwapResumeStore` | 11 | `internal/versions/store.go:154` |
| `dedupCollectorStores` | 10 | `internal/dedup/store.go:31` |
| `combineUndoStore` | 10 | `internal/merge/store.go:157` |
| `Store` | 10 | `internal/metabatch/store.go:48` |
| `metafetchFileStore` | 10 | `internal/metafetch/service.go:107` |
| `UserStore` | 10 | `internal/server/handlers/user.go:42` |
| `TrashedVersionCleaner` | 10 | `internal/versions/store.go:83` |
| `authorSeriesStore` | 9 | `internal/audiobooks/author_series.go:36` |
| `jobBookReader` | 9 | `internal/maintenance/job.go:247` |
| `opsFileAndPathReader` | 9 | `internal/plugins/maintenance/deps.go:140` |
| `opsLinkStore` | 9 | `internal/plugins/maintenance/deps.go:280` |
| `opsBookWriter` | 9 | `internal/plugins/maintenance/deps.go:98` |
| `rbfStore` | 9 | `internal/plugins/maintenance/repoint_book_file_rows.go:221` |
| `scanProgressStore` | 9 | `internal/scanner/store.go:110` |
| `serverAuthorStore` | 9 | `internal/server/server_ops_store.go:197` |
| `KeptSweepStore` | 9 | `internal/versions/store.go:69` |

Pattern take-away: the wide consumer interfaces sit in the server composition layer and the maintenance/ops plugins (`ServerOpsStore` 101 is roughly a quarter of Store); the leaf packages (scanner, dedup, metafetch) mostly declare 2-8 method interfaces. Narrowing is by consumer-declared interface plus (since the regroup) optional composition of the exported `database.XxxStore` leaves; `store.go:18-45` explicitly forbids exporting wide composites.

### 4.2 Full list (all 591), grouped by package

**`cmd`** (2): `diagnosticsCLIStore` 2 @diagnostics.go:115; `purgeStore` 2 @seed.go:249

**`internal/activity`** (1): `changelogStore` 3 @changelog.go:25

**`internal/ai/resultjournal`** (1): `KVStore` 4 @journal.go:81

**`internal/aiscan`** (2): `KeepStore` 2 @keep.go:17; `Store` 4 @pipeline.go:39

**`internal/applygate`** (3): `ManualOnlyFilesReader` 1 @manual_only.go:73; `ManualOnlySeriesReader` 1 @manual_only.go:78; `ManualOnlyTagReader` 1 @manual_only.go:85

**`internal/audiobooks`** (31): `authorReadModel` 5 @author_series.go:14; `seriesReadModel` 4 @author_series.go:23; `authorSeriesStore` 9 @author_series.go:36; `metadataStateStore` 1 @helpers.go:248; `importPathLister` 1 @helpers.go:296; `ExternalIDStore` 7 @helpers.go:470; `pendingFileKV` 4 @purge_pending_journal.go:73; `revertAuthorStore` 5 @revert.go:101; `revertAuthorRestoreStore` 4 @revert.go:114; `revertSeriesStore` 3 @revert.go:123; `revertBookFileStore` 8 @revert.go:135; `bookFilesAtPathReader` 1 @revert.go:3538; `revertTagStore` 2 @revert.go:71; `revertLedgerStore` 5 @revert.go:78; `revertFieldStateStore` 2 @revert.go:93; `revertCacheStore` 3 @revert_metadata_apply.go:87; `retireVersionAccess` 1 @revert_retire.go:33; `rawKV` 3 @revert_settle.go:456; `allBookFilesCoreGetter` 1 @runtime_index.go:71; `authorResolver` 4 @service.go:102; `narratorResolver` 5 @service.go:109; `seriesResolver` 3 @service.go:117; `contributorHydrator` 4 @service.go:126; `bookTagStore` 6 @service.go:135; `bookFileStore` 5 @service.go:150; `bookReader` 8 @service.go:51; `bookPathReader` 1 @service.go:65; `bookWriter` 7 @service.go:75; `contributorResolver` 12 @service.go:96; `tagsByBookIDsStore` 1 @service_tag_facets.go:59; `opChangeReader` 1 @undo_hold.go:89

**`internal/auth`** (1): `seedStore` 2 @seed.go:27

**`internal/authorcredit`** (5): `TitleSource` 2 @authorcredit.go:141; `aliasFinder` 1 @authorcredit.go:561; `authorBooksSource` 1 @authorcredit.go:810; `Store` 2 @authorcredit.go:83; `HistorySource` 1 @authorcredit.go:848

**`internal/authority`** (2): `Reader` 1 @lookup.go:22; `Scanner` 2 @lookup.go:28

**`internal/authormerge`** (3): `DeleteStore` 2 @authormerge.go:259; `JournalStore` 1 @authormerge.go:35; `CreditsStore` 4 @authormerge.go:63

**`internal/bookfiles`** (2): `FileLister` 1 @first_audio_file.go:18; `BookFilesBatcher` 1 @first_audio_file.go:24

**`internal/catalog`** (2): `ScopeReader` 3 @scope.go:24; `allBookFilesCore` 1 @scope.go:37

**`internal/covertext`** (1): `KV` 2 @covertext.go:123

**`internal/decisions`** (1): `pager` 1 @store.go:538

**`internal/dedup`** (18): `ExactAcoustIDStore` 1 @collectors_acoustid.go:48; `ChapterStore` 1 @collectors_chapters.go:19; `ExactFileHashStore` 1 @collectors_exact.go:50; `ISBNASINStore` 2 @collectors_exact.go:57; `MetaSrcHashStore` 1 @collectors_exact.go:64; `DurationCollectorStore` 3 @collectors_metadata.go:54; `MetaFuzzyStore` 3 @collectors_metadata.go:62; `SeriesPruneReader` 2 @series_prune_core.go:41; `SeriesPruneWriter` 3 @series_prune_core.go:47; `SeriesPruneStore` 6 @series_prune_core.go:65; `fillEmptyStore` 1 @split_book_fill_empty.go:27; `SplitBookRecheckReader` 2 @split_book_recheck.go:53; `dedupAuthorStore` 5 @store.go:108; `dedupSeriesStore` 6 @store.go:116; `dedupDuplicateStore` 5 @store.go:135; `dedupCollectorStores` 10 @store.go:31; `dedupBookQueries` 7 @store.go:63; `dedupBookReader` 7 @store.go:73

**`internal/dedup/dataset`** (1): `AdapterSource` 2 @builder.go:61

**`internal/deluge`** (7): `ContentFingerprintStore` 3 @discovery.go:127; `DiscoveryStore` 6 @discovery.go:134; `BookLister` 2 @discovery.go:68; `organizeNotifyStore` 1 @integration.go:140; `swapNotifyStore` 1 @integration.go:169; `BookFileModifier` 1 @mark_in_place.go:20; `Store` 2 @store.go:16

**`internal/diagnostics`** (4): `libraryCounter` 3 @store.go:21; `authorAggregateReader` 3 @store.go:27; `seriesAggregateReader` 3 @store.go:33; `Store` 12 @store.go:41

**`internal/fileops`** (1): `BookFileHashRecorder` 1 @book_file_hash_recorder.go:19

**`internal/foldercover`** (1): `Store` 2 @foldercover.go:64

**`internal/foldernames`** (2): `SeriesBooks` 2 @evidence.go:187; `PointStore` 4 @evidence.go:194

**`internal/importer`** (2): `importEntityStore` 5 @service.go:56; `importBookStore` 4 @service.go:65

**`internal/itunes`** (3): `PIDCoverageStore` 2 @pid_coverage.go:51; `PIDIntegrityStore` 3 @pid_integrity.go:44; `PIDRepairStore` 3 @pid_repair.go:38

**`internal/itunes/service`** (6): `bookWriter` 5 @importer.go:110; `contributorWriter` 6 @importer.go:123; `itunesImportState` 4 @importer.go:135; `importerVersionStore` 1 @importer.go:156; `bookLookup` 7 @importer.go:99; `playlistSyncStore` 3 @playlist_sync.go:40

**`internal/itunesguard`** (1): `MemberStore` 2 @itunesguard.go:75

**`internal/logger`** (2): `RetentionStore` 2 @retention.go:28; `ActivityLogWriter` 1 @standard.go:13

**`internal/maintenance`** (18): `jobBookReader` 9 @job.go:247; `jobBookLookup` 4 @job.go:253; `jobBookRelatedReader` 5 @job.go:262; `jobBookWriter` 5 @job.go:278; `jobManualOnlyReader` 3 @job.go:294; `jobBookStore` 17 @job.go:300; `jobBookFileReader` 6 @job.go:311; `jobBookFileWriter` 6 @job.go:323; `jobBookFileStore` 12 @job.go:340; `jobContributorStore` 8 @job.go:345; `jobUserStateStore` 7 @job.go:358; `jobOperationRecordStore` 7 @job.go:386; `jobOperationStateStore` 3 @job.go:401; `jobUndoLedgerStore` 2 @job.go:412; `jobOperationStore` 12 @job.go:417; `jobKVStore` 2 @job.go:428; `jobExternalIDStore` 2 @job.go:434; `JobStore` 60 @job.go:442

**`internal/maintenance/jobs`** (19): `ddBookModifier` 1 @dedup_books.go:517; `chapterDirLister` 1 @merge_chapter_groups.go:353; `chapterActiveOpsStore` 1 @merge_chapter_groups.go:659; `bookFileCreator` 1 @store_slices.go:103; `bookPager` 1 @store_slices.go:108; `userLister` 1 @store_slices.go:113; `userBookStateWriter` 1 @store_slices.go:118; `bookAggregateRecomputer` 2 @store_slices.go:124; `bookSoftDeleter` 1 @store_slices.go:135; `seriesUnlinker` 2 @store_slices.go:141; `seriesMerger` 2 @store_slices.go:152; `operationLister` 1 @store_slices.go:47; `operationDeleter` 1 @store_slices.go:51; `retentionKVStore` 2 @store_slices.go:57; `retentionOpStateStore` 4 @store_slices.go:69; `retentionFlagStore` 2 @store_slices.go:78; `bookMutator` 1 @store_slices.go:87; `bookFileMutator` 2 @store_slices.go:92; `bookFileReader` 1 @store_slices.go:98

**`internal/merge`** (29): `CombineJournalWriter` 1 @combine_journal.go:386; `combineJournalScanner` 1 @combine_undo_window.go:18; `UserStateReader` 3 @follow_journaled.go:224; `rawGetter` 1 @group_redirect.go:70; `rawDeleter` 2 @group_redirect.go:74; `VersionGroupReader` 2 @hard_delete_keeping_state.go:36; `ITunesGuardStore` 2 @itunes_guard.go:85; `linkPreflightStore` 2 @link_preflight.go:15; `pendingRepairKV` 2 @pending_repair.go:51; `journalPointerReader` 1 @pointer_backfill.go:71; `PointerBackfillStore` 4 @pointer_backfill.go:84; `UserStateRepairStore` 13 @repair_user_state.go:32; `retireLinkWriter` 1 @retire_link.go:50; `ExternalIDReassigner` 1 @service.go:32; `mergeExternalIDReader` 1 @store.go:115; `combineUndoStore` 10 @store.go:157; `combineJournalKV` 3 @store.go:164; `journalKVReader` 2 @store.go:171; `combineFileRowStore` 2 @store.go:181; `combineReverseMappingStore` 5 @store.go:188; `bookReader` 1 @store.go:30; `BookWriter` 5 @store.go:37; `mergeBookFileStore` 6 @store.go:50; `mergeAuthorStore` 4 @store.go:61; `playCountMarkStore` 2 @store.go:79; `userPositionStore` 7 @store.go:86; `UserProgressMerger` 11 @store.go:99; `pendingRepairScanner` 2 @trash_restore.go:48; `JournalPointerWriter` 1 @undo_pointer.go:57

**`internal/metabatch`** (11): `BookFilesGetter` 1 @candidates.go:25; `SeriesBatchGetter` 1 @candidates.go:374; `CandidateFetchResolver` 2 @fetch_ops_index.go:123; `ActiveCandidateFetchLister` 1 @fetch_ops_index.go:207; `CandidateFetchOpLister` 1 @fetch_ops_index.go:48; `ImportPathReader` 1 @import_roots.go:50; `FolderMemoStore` 1 @part_rows.go:143; `operationResultReader` 3 @store.go:24; `ownerManualReader` 2 @store.go:33; `upgradeCursorStore` 2 @store.go:40; `Store` 10 @store.go:48

**`internal/metadata`** (2): `importMetadataStore` 1 @enhanced.go:1085; `batchUpdateStore` 8 @enhanced.go:202

**`internal/metadata/dailyquota`** (1): `RawKV` 2 @dailyquota.go:77

**`internal/metafetch`** (12): `metadataStateStore` 6 @metadata_state_service.go:26; `RejectionStore` 5 @rejected_candidates.go:566; `RejectedCandidateReader` 1 @rejected_candidates.go:82; `metafetchFileStore` 10 @service.go:107; `metafetchFileReader` 4 @service.go:117; `metafetchFileWriter` 5 @service.go:133; `metafetchContributorStore` 7 @service.go:143; `metafetchNarratorStore` 3 @service.go:159; `metadataCacheStore` 4 @service.go:67; `metadataFieldStateStore` 5 @service.go:76; `metafetchBookStore` 8 @service.go:88; `StateSnapshotStore` 1 @state_snapshot.go:49

**`internal/oauth`** (1): `UserStore` 6 @resolve.go:32

**`internal/operations`** (3): `OperationStateWriter` 1 @state.go:45; `OperationStateReader` 1 @state.go:50; `OperationStateDeleter` 1 @state.go:55

**`internal/operations/childop`** (1): `Reader` 1 @follow.go:46

**`internal/operations/opswriter`** (5): `HistoryRecorder` 1 @history.go:53; `ChangeJournal` 1 @history.go:58; `BookStore` 1 @writer.go:51; `BookFileStore` 3 @writer.go:56; `AuthorStore` 1 @writer.go:70

**`internal/operations/registry`** (4): `legacyBackfillStore` 4 @legacy_backfill.go:30; `legacyOpStore` 2 @legacy_op_status.go:25; `opRegistryStore` 1 @register.go:51; `standDownPersister` 2 @scan_standdown.go:33

**`internal/organizer`** (12): `DurableSkipStore` 2 @apply_failure.go:101; `checkpointStore` 1 @checkpoint_store.go:16; `bookMover` 2 @checkpoint_store.go:21; `CollisionStore` 3 @collision.go:72; `OperationChangeWriter` 1 @inplace_multifile.go:437; `OrganizerStore` 4 @organizer.go:63; `RenamePathRecordClearer` 1 @rename_path_failure.go:78; `OrganizerAuditWriter` 3 @service.go:100; `OrganizerBookReader` 3 @service.go:40; `OrganizerBookWriter` 5 @service.go:48; `OrganizerBookFileStore` 7 @service.go:66; `OrganizerContributorStore` 5 @service.go:85

**`internal/playlist`** (3): `userStateReader` 1 @evaluator.go:103; `playlistEvalStore` 2 @evaluator.go:107; `bookLookup` 1 @evaluator.go:99

**`internal/plugins/acoustid`** (5): `pluginStore` 14 @plugin.go:106; `pluginBookReader` 3 @plugin.go:114; `pluginFileStore` 5 @plugin.go:123; `pluginBookSigWriter` 2 @plugin.go:136; `windowSidecarStore` 4 @plugin.go:149

**`internal/plugins/dedup`** (2): `pluginBookReader` 6 @plugin.go:139; `pluginBookWriter` 2 @plugin.go:150

**`internal/plugins/deluge`** (2): `linkBackfillStore` 5 @link_backfill.go:106; `linkVersionReader` 1 @link_backfill.go:124

**`internal/plugins/maintenance`** (105): `authorPathLinkAuthorStore` 5 @author_path_link.go:332; `authorPathLinkBookStore` 6 @author_path_link.go:346; `authorPathLinkStore` 11 @author_path_link.go:359; `titleRelinkStore` 8 @author_strip_merge_relink.go:255; `authorityPlanReader` 1 @authority_build.go:211; `bookShapeStore` 2 @book_shape_report.go:267; `bookFileBatchWriter` 1 @bookfile_batch_write.go:117; `folderBuildStore` 7 @build_folder_book_files.go:115; `combineJournalKV` 2 @combine_journal_check.go:37; `leftoverPathRowsReader` 1 @consolidation_leftovers_samepath.go:37; `creditCensusStore` 8 @credit_census.go:57; `creditsBackfillWriter` 4 @credits_backfill.go:593; `narratorByNameReader` 1 @credits_backfill.go:605; `compactableStore` 2 @db.go:160; `opsBookFileListReader` 5 @deps.go:104; `CombineJournalScanner` 1 @deps.go:1087; `opsBookFileLookup` 4 @deps.go:119; `opsFileAndPathReader` 9 @deps.go:140; `opsBookFileMutator` 7 @deps.go:146; `opsBookFileDeleter` 3 @deps.go:161; `opsBookFileMover` 2 @deps.go:177; `opsBookFileWriter` 12 @deps.go:191; `opsAuthorStore` 7 @deps.go:198; `opsAuthorAliasLister` 1 @deps.go:223; `opsSeriesStore` 8 @deps.go:228; `opsAuthorLinkStore` 6 @deps.go:250; `opsExternalIDLinkStore` 3 @deps.go:269; `opsLinkStore` 9 @deps.go:280; `opsHousekeeping` 13 @deps.go:304; `opsFieldStateStore` 3 @deps.go:324; `opsOperationJournalReader` 2 @deps.go:335; `opsRecordsAndQueue` 6 @deps.go:341; `opsSystemPreferences` 2 @deps.go:364; `opsNarratorStore` 6 @deps.go:374; `opsPeopleStore` 14 @deps.go:390; `OpsStore` 82 @deps.go:403; `opsBookReader` 8 @deps.go:49; `FragmentRepairReader` 2 @deps.go:491; `ExternalIDBookLookup` 1 @deps.go:569; `BookTagReader` 3 @deps.go:632; `VersionPrimaryStore` 1 @deps.go:646; `opsBookMutator` 5 @deps.go:65; `OpQueueReader` 1 @deps.go:665; `opsBookRetirer` 4 @deps.go:82; `opsBookWriter` 9 @deps.go:98; `dcPrefReader` 1 @duplicate_copies_fixer.go:468; `fileHashSetter` 1 @extract_wav_clips.go:227; `fileIntroStore` 4 @file_intro_transcribe.go:396; `filePathCollisionStore` 1 @filepath_collision_report.go:111; `itunesRegroupSnapshotReader` 5 @itunes_regroup.go:382; `junkAuthorEvidenceReader` 5 @junk_author_fixer.go:1306; `junkAuthorCorroborationReader` 2 @junk_author_fixer.go:391; `libraryStateRepairStore` 2 @library_state_repair.go:177; `markMissingStore` 4 @mark_missing_files.go:270; `mergeSamePathStore` 2 @merge_same_path_dupes.go:150; `reapBookResolver` 1 @metadata_cache_reap.go:187; `repointStore` 4 @missing_file_repoint.go:265; `mbfStore` 6 @move_book_file_rows.go:147; `orphanRepointPlanStore` 6 @orphan_book_files_repoint_plan.go:86; `rmReader` 5 @reconcile_fixer.go:236; `recoverStore` 3 @recover_missing_files.go:355; `seriesHistoryReader` 1 @relink_stale_series_fixer.go:628; `rbfStore` 9 @repoint_book_file_rows.go:221; `rbfReader` 6 @repoint_book_file_rows.go:228; `rbfWriter` 3 @repoint_book_file_rows.go:239; `rfStore` 7 @repoint_missing_to_folder_audio.go:307; `unrecordedRenameStore` 5 @repoint_unrecorded_renames.go:301; `bookJournalReader` 1 @retire_into.go:704; `retiredBookGetter` 1 @retired_versions_apply.go:416; `retiredExternalIDMover` 1 @retired_versions_apply.go:420; `retiredVersionsReader` 6 @retired_versions_fixer.go:255; `retiredShellVersions` 1 @retired_versions_fixer.go:269; `retiredVetoReaderStore` 2 @retired_versions_vetoes.go:24; `rewritePathPrefixStore` 6 @rewrite_path_prefix.go:354; `regroupBookMutator` 4 @store_slices.go:104; `regroupFileMover` 2 @store_slices.go:115; `fsRegroupFileStore` 4 @store_slices.go:125; `fsExternalIDReassigner` 1 @store_slices.go:140; `fsRegroupStore` 14 @store_slices.go:150; `itunesBookCreator` 1 @store_slices.go:160; `itunesExternalIDReassigner` 1 @store_slices.go:165; `regroupManualOnlyReader` 2 @store_slices.go:173; `itunesRegroupStore` 14 @store_slices.go:188; `orphanFileScanner` 3 @store_slices.go:209; `bookFileTrackWriter` 2 @store_slices.go:216; `bookByIDReader` 1 @store_slices.go:232; `reviewHoldStore` 1 @store_slices.go:244; `reviewHoldLister` 1 @store_slices.go:251; `bookFileCreator` 1 @store_slices.go:256; `bookFileBatchCreator` 1 @store_slices.go:266; `folderLinker` 3 @store_slices.go:302; `folderRelinker` 4 @store_slices.go:312; `multidiscApplier` 3 @store_slices.go:319; `transcribePageStore` 2 @store_slices.go:326; `bookFileLister` 1 @store_slices.go:49; `transcribeSelectStore` 2 @store_slices.go:56; `bookFileCoreScanner` 1 @store_slices.go:63; `bookModifier` 1 @store_slices.go:71; `bookFileRelinker` 3 @store_slices.go:77; `regroupSnapshotReader` 2 @store_slices.go:85; `regroupBookReader` 3 @store_slices.go:96; `tfReader` 7 @tag_franchise_fixer.go:111; `unknownAuthorAuditStore` 1 @unknown_author_audit.go:99; `vtBookChangesReader` 1 @version_twin_metadata_fixer.go:497; `vtHistoryReader` 1 @version_twin_metadata_fixer.go:503

**`internal/plugins/metafetch`** (6): `asinBackfillBookStore` 6 @asin_backfill.go:179; `asinBackfillMarkerStore` 2 @asin_backfill.go:190; `pcacheSweepStore` 1 @pcache_sweep.go:73; `pluginStore` 3 @plugin.go:86; `markerStore` 2 @register.go:102; `opStatusReader` 1 @register.go:129

**`internal/quarantine`** (5): `BookRows` 3 @service.go:43; `FileRows` 2 @service.go:54; `PathHistory` 2 @service.go:63; `ScanFailCounts` 1 @service.go:69; `Store` 8 @service.go:78

**`internal/readstatus`** (1): `Store` 4 @readstatus.go:52

**`internal/reconcile`** (8): `ElectStore` 8 @elect_primaries.go:47; `mergeDupeStore` 2 @reconcile.go:1796; `bookReader` 4 @reconcile.go:51; `bookWriter` 2 @reconcile.go:58; `fieldLockReader` 0 @reconcile.go:68; `importPathReader` 1 @reconcile.go:72; `operationRecorder` 1 @reconcile.go:80; `Store` 8 @reconcile.go:85

**`internal/remux`** (1): `Store` 2 @remux.go:33

**`internal/repairs`** (14): `OpReader` 1 @engine.go:231; `GuardReader` 6 @guards.go:503; `GuardTagReader` 1 @guards.go:598; `BookModifier` 1 @writer.go:36; `HistoryRecorder` 1 @writer.go:41; `CreditStore` 1 @writer_credits.go:37; `ChangeJournal` 2 @writer_files.go:77; `ChangeReverter` 1 @writer_files.go:86; `BookFileWriter` 5 @writer_files.go:96; `FieldStateStore` 0 @writer_locks.go:27; `retireVersionStore` 2 @writer_retire.go:36; `rawKVWriter` 1 @writer_retire.go:42; `shellReader` 1 @writer_retire.go:48; `TagStore` 2 @writer_tags.go:37

**`internal/scanner`** (8): `bookAuthorsModifier` 1 @scanner.go:4416; `scanServiceStore` 5 @service.go:27; `scanProgressStore` 9 @store.go:110; `scanHistoryStore` 2 @store.go:140; `scanHashLookup` 7 @store.go:30; `scanBookLookup` 7 @store.go:45; `scanBookWriter` 4 @store.go:69; `scanWorksStore` 4 @store.go:98

**`internal/scheduler`** (1): `SchedulerStore` 4 @scheduler.go:40

**`internal/search`** (3): `indexBuilderStore` 3 @index_builder.go:27; `relationsBatchStore` 3 @index_builder.go:83; `TagBookLister` 1 @tag_terms.go:294

**`internal/server`** (63): `apiKeyExpiryStampStore` 4 @apikey_expiry_stamp.go:26; `SettingsReadWriter` 3 @bootstrap.go:46; `previewResultStore` 2 @bulk_apply_preview.go:254; `catalogCoverageBooks` 1 @catalog_coverage_op.go:102; `authorBooksLister` 1 @catalog_harvest_op.go:256; `collectionPurgeStore` 2 @collection_peruser_purge.go:18; `seriesMergeStore` 2 @duplicates_helpers.go:55; `authorMergeStore` 7 @entities_ops.go:157; `authorMergeJournalStore` 2 @entities_ops.go:174; `ExternalIDStore` 7 @external_id_backfill.go:22; `legacyKVCleaner` 2 @itunes_writeback_cleanup.go:32; `seriesPruneReader` 2 @leaf_stores.go:18; `externalIDBackfillStore` 4 @leaf_stores.go:24; `importPathLister` 1 @leaf_stores.go:32; `userPreferenceReader` 1 @leaf_stores.go:40; `entityAssignStore` 1 @leaf_stores.go:46; `authorAssignStore` 3 @leaf_stores.go:51; `adminLookupStore` 3 @leaf_stores.go:59; `adminBootstrapStore` 4 @leaf_stores.go:66; `startupKeyStore` 7 @leaf_stores.go:73; `maintenanceBookStore` 2 @maintenance_fixups.go:24; `maintenanceSeriesStore` 5 @maintenance_fixups.go:32; `maintenanceStore` 7 @maintenance_fixups.go:48; `metadataResultsReader` 3 @metadata_stores.go:117; `operationResultStore` 2 @metadata_stores.go:20; `bulkMetadataFetchCommon` 3 @metadata_stores.go:39; `reconcileOpLister` 2 @reconcile_ops_index.go:38; `bookIDLister` 1 @search_coverage.go:55; `indexChunkStore` 5 @search_reconciler.go:157; `seriesRenameStore` 4 @series_rename_ops.go:50; `expiredSessionPruner` 2 @server_lifecycle.go:2294; `serverBookWriter` 6 @server_ops_store.go:110; `serverBookVersionStore` 7 @server_ops_store.go:131; `serverVersionRowStore` 4 @server_ops_store.go:138; `serverVersionTrashReader` 2 @server_ops_store.go:152; `serverVersionGroupReader` 1 @server_ops_store.go:160; `serverBookFileReader` 6 @server_ops_store.go:167; `serverStatsReader` 8 @server_ops_store.go:181; `serverAuthorStore` 9 @server_ops_store.go:197; `serverAuthorReader` 5 @server_ops_store.go:203; `serverAuthorWriter` 4 @server_ops_store.go:217; `serverAuthorLinkStore` 5 @server_ops_store.go:226; `serverEntityReader` 4 @server_ops_store.go:237; `serverNarratorWriter` 3 @server_ops_store.go:247; `serverTagWriter` 3 @server_ops_store.go:254; `serverTagReader` 2 @server_ops_store.go:261; `serverOperationReader` 6 @server_ops_store.go:267; `serverOperationWriter` 6 @server_ops_store.go:289; `serverOperationV2Store` 5 @server_ops_store.go:300; `serverUserStore` 5 @server_ops_store.go:323; `serverCredentialStore` 7 @server_ops_store.go:332; `serverMetadataStateStore` 4 @server_ops_store.go:345; `serverImportPathStore` 3 @server_ops_store.go:353; `serverSettingsStore` 5 @server_ops_store.go:360; `ServerOpsStore` 101 @server_ops_store.go:42; `serverBookStore` 26 @server_ops_store.go:55; `serverEntityStore` 21 @server_ops_store.go:63; `serverTagStore` 5 @server_ops_store.go:71; `serverOperationStore` 17 @server_ops_store.go:77; `serverAuthStore` 12 @server_ops_store.go:84; `serverConfigStore` 12 @server_ops_store.go:90; `serverBookReader` 7 @server_ops_store.go:99; `searchBackfillStore` 4 @server_search.go:64

**`internal/server/handlers`** (32): `ActivityOperationReader` 1 @activity.go:130; `ActivityOpsStore` 1 @activity.go:83; `ActiveOperationsLister` 1 @activity_compact.go:33; `aiStore` 3 @ai.go:174; `aiReviewGroupsStore` 1 @ai.go:189; `aiHandlerStore` 2 @ai.go:43; `APIKeyHandlerStore` 7 @apikeys.go:114; `AuthUserReader` 3 @auth.go:44; `AuthUserWriter` 2 @auth.go:54; `AuthRoleReader` 1 @auth.go:61; `AuthSessionStore` 5 @auth.go:66; `AuthStore` 11 @auth.go:82; `CacheMetadataStore` 1 @cache.go:79; `CollectionStore` 5 @collections.go:148; `CollectionEvalStore` 2 @collections.go:158; `diagnosticsOperationStore` 1 @diagnostics.go:250; `ImportPathCreator` 1 @filesystem.go:45; `FilesystemStore` 7 @filesystem.go:60; `ITunesStore` 6 @itunes.go:146; `bookFilesBatchReader` 1 @metadata_cache_stale.go:110; `PlaylistStore` 7 @playlists.go:64; `UserInviteStore` 5 @user.go:30; `UserStore` 10 @user.go:42; `VersionBookReader` 2 @versions.go:28; `VersionBookWriter` 2 @versions.go:39; `VersionBookFileStore` 2 @versions.go:45; `VersionBookAuthorStore` 2 @versions.go:51; `VersionExternalIDStore` 2 @versions.go:61; `VersionBookPathChecker` 1 @versions.go:68; `VersionBookDeleter` 1 @versions.go:74; `VersionsStore` 12 @versions.go:87; `WebSessionRevoker` 2 @web_session_revoke.go:32

**`internal/server/handlers/abs`** (11): `CollectionStore` 5 @collections.go:120; `LibraryBookReader` 5 @handler.go:147; `LibraryCreditReader` 3 @handler.go:209; `LibraryAuthorReader` 2 @handler.go:216; `LibrarySeriesReader` 3 @handler.go:222; `ChapterStore` 1 @handler.go:287; `ProgressStore` 5 @handler.go:300; `absBookFileLoader` 2 @mapper.go:309; `PlaylistStore` 5 @playlists.go:70; `ProgressListStore` 3 @userdata.go:57; `UserDataLibraryStore` 2 @userdata.go:99

**`internal/server/handlers/admindebug`** (5): `Store` 12 @handler.go:44; `bookStore` 3 @handler.go:52; `bookFileStore` 3 @handler.go:59; `pathIndex` 3 @handler.go:66; `rawKV` 3 @handler.go:73

**`internal/server/handlers/audiobooks`** (10): `retiredVersionReader` 1 @handler_retired_versions.go:29; `AudiobookSyncStore` 1 @interfaces.go:103; `AudiobooksStore` 21 @interfaces.go:125; `ExternalIDStore` 1 @interfaces.go:268; `AudiobookBookStore` 4 @interfaces.go:44; `AudiobookFileStore` 5 @interfaces.go:67; `AudiobookHistoryStore` 4 @interfaces.go:76; `AudiobookTagStore` 1 @interfaces.go:84; `AudiobookFacetStore` 2 @interfaces.go:89; `AudiobookCreditStore` 4 @interfaces.go:95

**`internal/server/handlers/dedup`** (3): `DedupStore` 8 @interfaces.go:40; `SeriesBooksStore` 1 @interfaces.go:73; `iTunesFlagStore` 2 @itunes_flags.go:51

**`internal/server/handlers/duplicates`** (1): `DuplicatesStore` 2 @interfaces.go:44

**`internal/server/handlers/entities`** (14): `EntitiesStore` 32 @interfaces.go:109; `AuthorEntityStore` 7 @interfaces.go:24; `AuthorAliasEntityStore` 3 @interfaces.go:40; `BookLinkStore` 8 @interfaces.go:47; `BookEntityStore` 3 @interfaces.go:69; `NarratorEntityStore` 3 @interfaces.go:78; `SeriesEntityStore` 5 @interfaces.go:86; `WorkEntityStore` 3 @interfaces.go:96; `ReclassifyStore` 11 @reclassify.go:23; `reclassifyAuthorStore` 4 @reclassify.go:33; `reclassifyNarratorStore` 4 @reclassify.go:39; `unknownAuthorStore` 2 @reclassify.go:47; `bookModifier` 1 @reclassify.go:53; `ReclassifyJournalStore` 2 @reclassify.go:72

**`internal/server/handlers/metadata`** (6): `MetadataBookQueryStore` 2 @interfaces.go:100; `BookRatingWriter` 1 @interfaces.go:111; `MetadataBookStore` 6 @interfaces.go:120; `MetadataEntityResolver` 5 @interfaces.go:45; `MetadataAuditWriter` 1 @interfaces.go:69; `BookSnapshotStore` 3 @interfaces.go:90

**`internal/server/handlers/opaccess`** (1): `OpLookup` 1 @repairs.go:97

**`internal/server/handlers/operations`** (4): `operationsBookStore` 4 @interfaces.go:35; `operationsContributorStore` 7 @interfaces.go:42; `operationsRecordStore` 6 @interfaces.go:52; `OperationsStore` 17 @interfaces.go:74

**`internal/server/handlers/repairs`** (1): `OpStore` 4 @handler.go:87

**`internal/server/handlers/system`** (8): `SystemStore` 17 @interfaces.go:108; `SystemStatsStore` 4 @interfaces.go:27; `SystemBookStore` 2 @interfaces.go:36; `SystemAuthorStore` 2 @interfaces.go:43; `SystemAuditReader` 2 @interfaces.go:53; `SystemLifecycleStore` 1 @interfaces.go:64; `SystemHashBlocklistStore` 3 @interfaces.go:70; `SystemUserPreferenceStore` 3 @interfaces.go:78

**`internal/server/middleware`** (5): `absAPIKeyStore` 1 @absauth.go:409; `roleResolver` 1 @auth.go:27; `authKeyStore` 4 @auth.go:31; `authSessionStore` 7 @auth.go:38; `UserCounter` 1 @auth.go:475

**`internal/sweep`** (2): `fileAuditor` 1 @sweeper.go:163; `tombstoneSweeper` 3 @sweeper.go:38

**`internal/sysinfo`** (2): `dashboardStore` 2 @dashboard_service.go:21; `SystemServiceStore` 5 @service.go:24

**`internal/transcode`** (1): `transcodeStore` 2 @transcode.go:31

**`internal/undo`** (15): `RepairBookCreateStore` 4 @book_create.go:71; `bookTagReader` 1 @book_tag.go:49; `siblingJournalReader` 2 @engine.go:228; `bookFilesReader` 1 @engine.go:673; `fieldStateReader` 1 @engine.go:677; `bookFileByIDReader` 1 @engine.go:681; `externalIDOwnerReader` 1 @engine.go:685; `ConflictChecker` 4 @engine.go:79; `bookAuthorsReader` 1 @engine.go:812; `authorRestoreReader` 1 @engine.go:951; `bookVersionReader` 1 @engine.go:995; `MetadataCacheReader` 1 @metadata_apply.go:136; `SeriesLookup` 2 @restorable.go:489; `BookLookup` 1 @restorable.go:505; `UserStateReader` 2 @user_book_state.go:240

**`internal/usermerge`** (1): `Store` 7 @service.go:45

**`internal/versionprimary`** (3): `BookReader` 1 @ensure.go:247; `EnsureStore` 4 @ensure.go:50; `FileReader` 1 @signals.go:27

**`internal/versions`** (14): `bookTombstoneReader` 1 @lifecycle.go:1276; `swapStore` 7 @store.go:121; `swapPlanStore` 4 @store.go:142; `SwapResumeStore` 11 @store.go:154; `bookFileLister` 1 @store.go:16; `SwapIncompleteLister` 5 @store.go:163; `versionUpdater` 1 @store.go:20; `FingerprintReader` 1 @store.go:25; `TorrentHolderReader` 2 @store.go:32; `versionPurger` 7 @store.go:37; `KeptSweepStore` 9 @store.go:69; `TrashedVersionCleaner` 10 @store.go:83; `IngestStore` 8 @store.go:96; `swapTargetStore` 3 @swap_target.go:52

**`internal/writeback`** (1): `outboxStore` 3 @outbox.go:30

**`pkg/plugin/sdk`** (1): `BookStore` 1 @iterate.go:25

## 5. Implementations of `Store`

| Implementation | File | LOC | Notes |
|---|---|---|---|
| `PebbleStore` (the only production implementation) | `internal/database/pebble_store.go:107` (type); methods spread over 76 `pebble_store*.go` files plus other files in the package | `pebble_store.go` 6,767; all 76 non-test `pebble_store*.go` files 42,111; all non-test `pebble*.go` 49,015; whole non-test `internal/database` package 103,432 | Compile-time proof: `var _ Store = (*PebbleStore)(nil)` at `internal/database/iface_assert.go:13` (plus ~30 sub-interface assertions in the same block). 772 exported methods in total. |
| `MockStore` (hand-written, permissive; unconfigured methods return zero values) | `internal/database/mock_store.go:36` | 4,189 | `var _ Store = (*MockStore)(nil)` at `mock_store.go:31`; kept alongside the mockery mock on purpose (comment at `mock_store.go:15-30`). |
| `mocks.MockStore` (mockery-generated, strict expectations) | `internal/database/mocks/mock_store.go:4564` | 31,408 | Used e.g. `internal/itunes/service/importer_mock_test.go:162` (`var _ Store = (*dbmocks.MockStore)(nil)`). |
| Per-consumer mockery mocks of narrowed interfaces | 81 files under `*/mocks/` directories (e.g. `internal/server/handlers/*/mocks/`) | 57,707 total including the two Store mocks above | Each implements a narrow interface, not Store. |
| `dbtest` helpers | `internal/database/dbtest/newstore.go` | n/a | Builds a real in-memory `PebbleStore` for tests (`NewPebbleStoreInMemory`, `pebble_store.go:578`, uses `vfs.NewMem()`). |
| `pkg/ops/opstest` fakes | `pkg/ops/opstest/fakes.go` | 613 | Fakes for the operation-store capability interfaces, not for Store. |
| Decorators | none at HEAD | - | The Bleve `indexedStore` decorator was removed (comments at `internal/server/index_queue.go:10`, `internal/database/store_capability.go:69`); `StoreUnwrapper` + `AsCapability` remain for any future decorator (`store_capability.go:60-100`). Two packages embed `Store` in a struct: `internal/reconcile/reconcile.go:99`, `internal/reconcile/elect_primaries.go:48`. |

- **SQLite: no implementation.** `InitializeStore` returns an error for `sqlite`/`sqlite3` and for any non-pebble type (`internal/database/store.go:1839-1851`). The CLI flag text is stale: `--db-type` help still says "pebble (default) or sqlite" (`cmd/root.go:489`) while the removed `--enable-sqlite3-i-know-the-risks` flag is a no-op (`cmd/root.go:62-70,497-508`). `backup.go` still has SQLite branches (`internal/backup/backup.go:959,1028,1103`), dead for the live store.
- A package-level `globalStore` is also still set by `InitializeStore` (`store.go:1854`), with a code comment saying new code should pass the store explicitly.
- Anything new that wants to be a second backend must implement 445 methods plus the 115 off-Store capability interfaces the runtime reaches by assertion; a missing capability degrades silently (see the `ChapterReader` comment, `store.go:80-93`).

## 6. Capability interfaces that are NOT on `Store` (115 exported, 283 methods)

These are what `database.AsCapability[T]` (`internal/database/store_capability.go:89`) or `*PebbleStore` assertions resolve. Listed name, method count, declaration.

| Interface | Methods | Declared at |
|---|---|---|
| `OpsV3Store` | 28 | `internal/database/iface_ops_v3.go:488` |
| `ActivityStorer` | 9 | `internal/database/activity_storer.go:100` |
| `FileProvenanceStore` | 8 | `internal/database/file_provenance.go:308` |
| `OpsV3AuxStore` | 7 | `internal/database/iface_ops_v3.go:455` |
| `OpsV3ChunkStore` | 7 | `internal/database/iface_ops_v3.go:438` |
| `OpsV3RunReader` | 7 | `internal/database/iface_ops_v3.go:413` |
| `RetiredBookFileReader` | 6 | `internal/database/iface_retired.go:55` |
| `ReviewItemApplyClaimer` | 6 | `internal/database/review_claim.go:46` |
| `SyncFileStore` | 6 | `internal/database/pebble_store_syncfile.go:53` |
| `SyncIdentityStore` | 6 | `internal/database/pebble_store_syncid.go:63` |
| `VectorANNStore` | 6 | `internal/database/vector_ann_store.go:24` |
| `ActivityFilterIndexBackfiller` | 5 | `internal/database/pebble_activity_filter_index.go:628` |
| `BookmarkStore` | 5 | `internal/database/pebble_store_bookmarks.go:48` |
| `ChangeObserver` | 5 | `internal/database/change_observer.go:28` |
| `ActivityRetention` | 4 | `internal/database/activity_storer.go:41` |
| `BookFileIntroStore` | 4 | `internal/database/bookfile_intro_store.go:60` |
| `DBCensusExactRunner` | 4 | `internal/database/census_exact.go:96` |
| `GuestServiceTokenStore` | 4 | `internal/database/pebble_store_guest_tokens.go:80` |
| `LegacyMetadataStateStore` | 4 | `internal/database/metadata_field_lock_source.go:96` |
| `MetricsStorer` | 4 | `internal/database/activity_storer.go:109` |
| `RepairLockClaimStore` | 4 | `internal/database/metadata_field_lock_source.go:178` |
| `SearchIndexDirtyStore` | 4 | `internal/database/pebble_store_search_dirty.go:46` |
| `SyncAliasUseStore` | 4 | `internal/database/pebble_store_sync_alias_use.go:42` |
| `UserMerger` | 4 | `internal/database/user_merge.go:384` |
| `UserOverrideRecorder` | 4 | `internal/database/metadata_field_locks.go:157` |
| `ActivityReader` | 3 | `internal/database/activity_storer.go:19` |
| `BookEditHistoryStore` | 3 | `internal/database/book_edit_history.go:68` |
| `BookTagSingletonStore` | 3 | `internal/database/tag_helpers.go:34` |
| `CollectionMembershipStore` | 3 | `internal/database/iface_catalog.go:56` |
| `InviteDeliveryStore` | 3 | `internal/database/pebble_store_invite_delivery.go:122` |
| `InviteEmailGrantStore` | 3 | `internal/database/pebble_store_invite_delivery.go:137` |
| `NarratorRefStore` | 3 | `internal/database/narrator_bookref.go:80` |
| `OpRowStore` | 3 | `internal/database/iface_op_rows.go:39` |
| `OpsV3RunWriter` | 3 | `internal/database/iface_ops_v3.go:397` |
| `RawSnapshot` | 3 | `internal/database/raw_snapshot.go:21` |
| `RetiredBookFileWriter` | 3 | `internal/database/iface_retired.go:83` |
| `UserCredentialWriter` | 3 | `internal/database/pebble_store_auth.go:801` |
| `BookAuthorReader` | 2 | `internal/database/live_book_authors.go:15` |
| `BookCreditsModifier` | 2 | `internal/database/credits_store.go:37` |
| `BookFilePathLookup` | 2 | `internal/database/book_file_owners.go:12` |
| `BookNameReader` | 2 | `internal/database/book_names.go:11` |
| `ChangeObservable` | 2 | `internal/database/change_observer.go:51` |
| `FileIntroStatsStore` | 2 | `internal/database/file_intro_stats.go:123` |
| `IdentIndexReader` | 2 | `internal/database/ident_state.go:338` |
| `MetadataFieldStateReader` | 2 | `internal/database/metadata_field_locks.go:123` |
| `OpsV3CheckpointStore` | 2 | `internal/database/iface_ops_v3.go:428` |
| `OpsV3SchedStore` | 2 | `internal/database/iface_ops_v3.go:472` |
| `PCacheStore` | 2 | `internal/database/metadata_fetch_cache.go:394` |
| `SearchResultStore` | 2 | `internal/database/pebble_store_search_hydrate.go:29` |
| `TranscribeStatsStore` | 2 | `internal/database/transcribe_stats.go:121` |
| `UserPlaylistReadWriter` | 2 | `internal/database/user_playlist_retry.go:17` |
| `ActivityCounter` | 1 | `internal/database/activity_storer.go:36` |
| `ActivityLifecycle` | 1 | `internal/database/activity_storer.go:79` |
| `ActivityPartialQuerier` | 1 | `internal/database/pebble_activity_filter_index.go:829` |
| `ActivityWriter` | 1 | `internal/database/activity_storer.go:14` |
| `AdminEmailSetter` | 1 | `internal/database/pebble_store_auth.go:649` |
| `AuthorBookRefBucketStore` | 1 | `internal/database/author_bookref.go:316` |
| `AuthorBookRefStore` | 1 | `internal/database/author_bookref.go:75` |
| `AuthorCheckedRenamer` | 1 | `internal/database/pebble_store_authors.go:539` |
| `AuthorFileRefStore` | 1 | `internal/database/author_file_refs.go:83` |
| `AuthorMergeDeleter` | 1 | `internal/database/iface_author.go:108` |
| `AuthorNameHolderReader` | 1 | `internal/database/pebble_store_author_restore.go:237` |
| `AuthorRelinkLister` | 1 | `internal/database/author_relink_guard.go:19` |
| `AuthorRestorer` | 1 | `internal/database/pebble_store_author_restore.go:29` |
| `BookChangeLogProvider` | 1 | `internal/database/book_change_log.go:108` |
| `BookCreditIndexStore` | 1 | `internal/database/book_credit_index.go:27` |
| `BookCreditsReader` | 1 | `internal/database/credits_store.go:30` |
| `BookFileByFileIDReader` | 1 | `internal/database/book_file_by_file_id.go:23` |
| `BookFileHashUpdater` | 1 | `internal/database/iface_bookfile.go:303` |
| `BookFilesGetter` | 1 | `internal/database/book_runtime.go:375` |
| `BookListingFieldsReader` | 1 | `internal/database/book_listing_fields.go:31` |
| `BookRowPathReader` | 1 | `internal/database/iface_book.go:124` |
| `BookSigMigrateStore` | 1 | `internal/database/pebble_store_booksig_migrate.go:99` |
| `BookmarkCopier` | 1 | `internal/database/pebble_store_bookmarks.go:241` |
| `CollectionMembershipMarkStore` | 1 | `internal/database/iface_catalog.go:75` |
| `DBCensusProvider` | 1 | `internal/database/census.go:218` |
| `FileHashHolderLookup` | 1 | `internal/database/file_hash_holders.go:23` |
| `FileProvenanceRecorder` | 1 | `internal/database/file_provenance.go:361` |
| `GuardedBookVersionDeleter` | 1 | `internal/database/iface_retired.go:124` |
| `IdentDirtyMarker` | 1 | `internal/database/ident_state.go:349` |
| `InviteAcceptChecker` | 1 | `internal/database/pebble_store_auth.go:1529` |
| `InviteIDLookup` | 1 | `internal/database/invite_id.go:35` |
| `LegacyMetadataStateDeleter` | 1 | `internal/database/metadata_field_locks.go:129` |
| `LibraryGenerationProvider` | 1 | `internal/database/library_generation.go:14` |
| `LiveBookFileSampler` | 1 | `internal/database/book_file_live_sample.go:24` |
| `MetadataCacheChangeLogProvider` | 1 | `internal/database/book_change_log.go:134` |
| `OperationChangeCreator` | 1 | `internal/database/change_type_class.go:187` |
| `OperationChangesCreator` | 1 | `internal/database/change_type_class.go:193` |
| `OperationsV2SinceLister` | 1 | `internal/database/ops_v2_recent.go:12` |
| `PebbleMetricsSampler` | 1 | `internal/database/pebble_metrics_export.go:16` |
| `PrefixPageScanner` | 1 | `internal/database/pcache_rows.go:111` |
| `RawBatchApplier` | 1 | `internal/database/raw_batch.go:21` |
| `RawSnapshotter` | 1 | `internal/database/raw_snapshot.go:30` |
| `RetiredPathKeyClaimer` | 1 | `internal/database/iface_retired.go:105` |
| `RetiredVersionHolderRestorer` | 1 | `internal/database/iface_retired.go:112` |
| `ReviewItemPendingDeleter` | 1 | `internal/database/review_pending_delete.go:20` |
| `ReviewStatusIndexRepairer` | 1 | `internal/database/iface_review.go:49` |
| `SeriesBookRefCounter` | 1 | `internal/database/series_bookref.go:239` |
| `SeriesBookRefStore` | 1 | `internal/database/series_bookref.go:49` |
| `SeriesHolder` | 1 | `internal/database/pebble_store_series.go:185` |
| `SeriesMembershipStore` | 1 | `internal/database/series_membership.go:44` |
| `SoftDeletedCountStore` | 1 | `internal/database/soft_deleted_count.go:27` |
| `StampedTombstoneWriter` | 1 | `internal/database/tombstone_file_stamp.go:43` |
| `StoreUnwrapper` | 1 | `internal/database/store_capability.go:71` |
| `StrictUserLister` | 1 | `internal/database/pebble_store_auth.go:33` |
| `TombstoneStampReader` | 1 | `internal/database/tombstone_file_stamp.go:52` |
| `UnverifiedEmailDisplacer` | 1 | `internal/database/pebble_store_auth.go:192` |
| `UserBookStateDeleter` | 1 | `internal/database/pebble_store_playback.go:349` |
| `UserBookStateLister` | 1 | `internal/database/pebble_store_playback.go:380` |
| `UserPasswordRehasher` | 1 | `internal/database/pebble_store_auth.go:849` |
| `UserPlaylistMarkStore` | 1 | `internal/database/iface_user.go:97` |
| `UserPositionReplacer` | 1 | `internal/database/pebble_store_playback.go:201` |
| `UserPositionTimestampWriter` | 1 | `internal/database/pebble_store_playback.go:45` |
| `VerifiedEmailIdentityLinker` | 1 | `internal/database/oauth_identity.go:84` |
| `VerifiedEmailMarker` | 1 | `internal/database/pebble_store_auth.go:378` |

## 7. "Must be this way" comments touching Store, and whether they hold

| Claim (location) | Verdict at HEAD |
|---|---|
| "Store ... 40 sub-interfaces ... The six composites below are deliberately UNEXPORTED" (`store.go:18-45`) | HOLDS. Six composites are lowercase (`store.go:58-134`). The count "40 sub-interfaces" is the count before regrouping; a flattened method count (445) is not stated. |
| "250 [consumer-side interfaces] exist outside this package, median 2 methods" (`store.go:38`) | STALE. 591 interfaces qualify as Store-subsets by my rule (median 3). Different counting rules may explain part of it. |
| `migrationStore`: "The parameter was database.Store - all 398 methods" and "Of the 61 migration functions, 60 touch the store not at all" (`migrations.go:17-28`) | Both STALE. Store is 445 now; 66 migrations are registered (section 7 of 05-storage). The narrowing itself (6-method `migrationStore`, `migrations.go:30-37`) holds. |
| `ChapterReader` is read-only and on Store so a decorator that forgets to forward it fails the BUILD (`store.go:80-93`) | HOLDS as a design rule. The motivating decorator (`indexedStore`) no longer exists, so the rule is now insurance. |
| `StoreUnwrapper`: do not implement on access-control or read-only wrappers (`store_capability.go:60-70`) | HOLDS (no implementers at HEAD to violate it). |
| MockStore kept hand-written because converting ~22 test files to strict mockery expectations is not clearly better (`mock_store.go:15-30`) | Not verifiable cheaply; the two-mocks situation persists (4,189 + 31,408 LOC). |
| CLAUDE.md "`handlers.OrganizeStore` was `= database.Store` (398 methods)" | HOLDS as history: `OrganizeStore` at `internal/server/handlers/organize.go:68` is now an 8-method interface; no `= database.Store` alias remains (grep found none). |

## 8. Appendix: every leaf interface and its methods

| Leaf interface | Declared at | Methods |
|---|---|---|
| `BookByIDReader` (3) | `internal/database/iface_book.go:26` | GetBookByID, GetBooksByIDs, ListBookIDs |
| `BookBulkReader` (4) | `internal/database/iface_book.go:40` | GetAllBooksCore, GetAllBooksFullFrom, GetAllBookSummaries, ListBookSearchRows |
| `BookNaturalKeyReader` (8) | `internal/database/iface_book.go:85` | GetBookByFilePath, GetBookByITunesPersistentID, ListBooksByITunesPID, GetBookByFileHash, GetBookByOriginalHash, GetBookByOrganizedHash, GetBookIDsByISBNASIN, GetBooksByMetadataSourceHash |
| `BookPathSetReader` (1) | `internal/database/iface_book.go:105` | LiveBookIDsAtPath |
| `BookDuplicateReader` (4) | `internal/database/iface_book.go:179` | GetDuplicateBooks, GetFolderDuplicatesCore, GetDuplicateBooksByMetadataCore, GetBooksByTitleInDir |
| `BookRelationReader` (4) | `internal/database/iface_book.go:194` | GetBooksBySeriesIDCore, GetBooksBySeriesIDAllVersions, GetBooksByAuthorIDCore, GetBooksByVersionGroup |
| `BookSearchReader` (5) | `internal/database/iface_book.go:227` | SearchBooks, GetDistinctGenres, GetGenreCounts, GetDistinctLanguages, GetDistinctPublishedYears |
| `BookCountReader` (3) | `internal/database/iface_book.go:250` | CountPrimaryBooks, CountAllBooks, CountQuarantinedBooks |
| `BookSnapshotReader` (2) | `internal/database/iface_book.go:260` | GetBookSnapshots, GetBookAtVersion |
| `BookLifecycleReader` (4) | `internal/database/iface_book.go:266` | ListSoftDeletedBooks, GetBookTombstone, ListBookTombstones, GetQuarantinedBooks |
| `BookITunesReader` (2) | `internal/database/iface_book.go:274` | GetITunesDirtyBooks, GetITunesPurgePendingBooks |
| `BookMutator` (7) | `internal/database/iface_book.go:306` | CreateBook, UpdateBook, ModifyBook, ClearBookSignature, UpdateBookRating, FillBookMediaInfo, DeleteBook |
| `BookSyncMarker` (2) | `internal/database/iface_book.go:335` | SetLastWrittenAt, MarkITunesSynced |
| `BookVersionMutator` (3) | `internal/database/iface_book.go:341` | RevertBookToVersion, PruneBookSnapshots, CountBookSnapshots |
| `BookTombstoneWriter` (2) | `internal/database/iface_book.go:350` | CreateBookTombstone, DeleteBookTombstone |
| `BookScanFailStore` (3) | `internal/database/iface_book.go:356` | GetScanFailCount, IncrScanFailCount, ResetScanFailCount |
| `BookAggregateWriter` (2) | `internal/database/iface_book.go:364` | FlagMetadataHashDuplicate, RecomputeBookAggregates |
| `BookCompletenessReader` (2) | `internal/database/iface_book.go:407` | GetAllBooksCoreComplete, GetAllBookFilesCoreComplete |
| `BookPathIndexStatus` (1) | `internal/database/iface_book.go:161` | BookAtPathIndexBuilt |
| `BookDirLister` (1) | `internal/database/iface_book.go:143` | LiveBookPathsUnderDir |
| `AuthorLookupReader` (5) | `internal/database/iface_author.go:11` | GetAllAuthors, GetAuthorByID, GetAuthorByName, GetAuthorsByIDs, GetAuthorTombstone |
| `AuthorAliasReader` (3) | `internal/database/iface_author.go:22` | GetAuthorAliases, GetAllAuthorAliases, FindAuthorByAlias |
| `AuthorBookReader` (5) | `internal/database/iface_author.go:29` | GetBookAuthors, GetBooksByAuthorIDWithRoleCore, GetBooksByAuthorIDForRelinkCore, GetBookIDsCreditingAuthorDurable, GetAuthorsByBookIDs |
| `AuthorCountReader` (2) | `internal/database/iface_author.go:52` | GetAllAuthorBookCounts, GetAllAuthorFileCounts |
| `AuthorWriter` (8) | `internal/database/iface_author.go:72` | CreateAuthor, DeleteAuthor, UpdateAuthorName, CreateAuthorAlias, DeleteAuthorAlias, SetBookAuthors, CreateAuthorTombstone, ResolveTombstoneChains |
| `BookAuthorModifier` (1) | `internal/database/iface_author.go:87` | ModifyBookAuthors |
| `AuthorMinter` (1) | `internal/database/iface_author.go:98` | MintAuthor |
| `SeriesReader` (6) | `internal/database/iface_series.go:9` | GetAllSeries, GetSeriesByID, GetSeriesByName, GetAllSeriesBookCounts, GetAllSeriesFileCounts, GetSeriesByIDs |
| `SeriesWriter` (5) | `internal/database/iface_series.go:21` | CreateSeries, DeleteSeries, UpdateSeriesName, RenameSeriesIf, RenameSeriesIfCurrent |
| `NarratorStore` (8) | `internal/database/iface_catalog.go:107` | CreateNarrator, DeleteNarrator, GetNarratorByID, GetNarratorByName, ListNarrators, GetBookNarrators, SetBookNarrators, GetNarratorsByBookIDs |
| `WorkStore` (7) | `internal/database/iface_catalog.go:95` | GetAllWorks, GetWorkByID, CreateWork, UpdateWork, DeleteWork, GetBooksByWorkID, GetAllWorkBookCounts |
| `WorkScanStore` (2) | `internal/database/iface_catalog.go:83` | ForEachWork, WorksGeneration |
| `BookVersionReader` (4) | `internal/database/iface_catalog.go:124` | GetBookVersion, GetBookVersionsByBookID, GetActiveVersionForBook, GetBookVersionByTorrentHash |
| `BookVersionWriter` (3) | `internal/database/iface_catalog.go:132` | CreateBookVersion, UpdateBookVersion, DeleteBookVersion |
| `BookVersionDispositionReader` (3) | `internal/database/iface_catalog.go:139` | ListTrashedBookVersions, ListPurgedBookVersions, ListSwappingBookVersions |
| `CollectionStore` (6) | `internal/database/iface_catalog.go:24` | CreateCollection, GetCollection, GetCollectionByName, ListCollections, UpdateCollection, DeleteCollection |
| `BookFileReader` (7) | `internal/database/iface_bookfile.go:19` | GetBookFiles, GetAllBookFilesCore, GetBookFilesForIDsCore, GetBookFileByID, GetBookFileByPath, BookFilesAtPath, BookFilesWithHash |
| `BookFileCreator` (2) | `internal/database/iface_bookfile.go:44` | CreateBookFile, BatchCreateBookFiles |
| `BookFileUpserter` (6) | `internal/database/iface_bookfile.go:73` | UpdateBookFile, UpdateBookFiles, UpsertBookFile, ClaimBookFilePathKey, PatchBookFileFields, ModifyBookFile |
| `BookFileBatchUpserter` (3) | `internal/database/iface_bookfile.go:56` | BatchUpsertBookFiles, BatchUpsertBookFilesKeepPaths, BatchUpsertScannedBookFiles |
| `BookFileMover` (2) | `internal/database/iface_bookfile.go:99` | MoveBookFilesToBook, MoveBookFilesToBookBulk |
| `BookFileDeleter` (3) | `internal/database/iface_bookfile.go:129` | DeleteBookFile, DeleteBookFilesByIDs, DeleteBookFilesForBook |
| `BookFileHashStore` (4) | `internal/database/iface_bookfile.go:165` | UpdateBookFileHashes, SetBookFileHash, GetDuplicateFilesByHash, GetBookBySegmentFileHash |
| `BookFileFingerprintStore` (4) | `internal/database/iface_bookfile.go:195` | GetBookFileByAcoustID, GetBookFileByAcoustIDFuzzy, GetFilesWithFingerprintFailures, GetFilesWithZeroDurationFingerprint |
| `FingerprintWindowStore` (8) | `internal/database/iface_bookfile.go:219` | PutFingerprintWindow, GetFingerprintWindows, WindowsForFile, DeleteFingerprintWindows, CarryOverFingerprintWindows, ReplaceFingerprintWindows, RecordFingerprintWindowFailure, GetFingerprintWindowFailure |
| `BookFileITunesStore` (1) | `internal/database/iface_bookfile.go:250` | GetBookFileByPID |
| `BookFileDelugeStore` (1) | `internal/database/iface_bookfile.go:255` | GetBookFilesNeedingDelugeImportCore |
| `BookFileStatsStore` (3) | `internal/database/iface_bookfile.go:270` | GetBookFileHashStats, GetBookMetadataHashStats, GetAcoustIDStats |
| `BookSegmentStore` (6) | `internal/database/iface_bookfile.go:315` | CreateBookSegment, UpdateBookSegment, ListBookSegments, MergeBookSegments, GetBookSegmentByID, MoveSegmentsToBook |
| `ChapterReader` (1) | `internal/database/store.go:94` | GetChaptersForBook |
| `PlaylistStore` (5) | `internal/database/iface_user.go:102` | CreatePlaylist, GetPlaylistByID, GetPlaylistBySeriesID, AddPlaylistItem, GetPlaylistItems |
| `UserPlaylistReader` (6) | `internal/database/iface_user.go:65` | GetUserPlaylist, GetUserPlaylistByName, GetUserPlaylistByITunesPID, ListUserPlaylists, ListUserPlaylistsForUser, ListDirtyUserPlaylists |
| `UserPlaylistWriter` (3) | `internal/database/iface_user.go:78` | CreateUserPlaylist, UpdateUserPlaylist, DeleteUserPlaylist |
| `PathHistoryStore` (2) | `internal/database/iface_itunes.go:57` | RecordPathChange, GetBookPathHistory |
| `PlaybackStore` (8) | `internal/database/iface_user.go:111` | AddPlaybackEvent, ListPlaybackEvents, UpdatePlaybackProgress, GetPlaybackProgress, IncrementBookPlayStats, GetBookStats, IncrementUserListenStats, GetUserStats |
| `UserReader` (5) | `internal/database/iface_user.go:11` | GetUserByID, GetUserByUsername, GetUserByVerifiedEmail, ListUsers, CountUsers |
| `UserWriter` (2) | `internal/database/iface_user.go:23` | CreateUser, UpdateUser |
| `SessionStore` (7) | `internal/database/iface_auth.go:70` | CreateSession, CreateSessionWithOrigin, GetSession, RevokeSession, ListUserSessions, DeleteExpiredABSSessions, DeleteExpiredSessions |
| `OAuthIdentityStore` (3) | `internal/database/iface_oauth.go:9` | CreateOAuthIdentity, GetOAuthIdentityByProviderSubject, GetOAuthIdentitiesForUser |
| `RoleStore` (6) | `internal/database/iface_auth.go:60` | GetRoleByID, GetRoleByName, ListRoles, CreateRole, UpdateRole, DeleteRole |
| `APIKeyReader` (4) | `internal/database/iface_auth.go:19` | GetAPIKey, GetAPIKeyByHash, ListAPIKeysForUser, ListAllAPIKeys |
| `APIKeyWriter` (1) | `internal/database/iface_auth.go:27` | CreateAPIKey |
| `APIKeyLifecycleStore` (3) | `internal/database/iface_auth.go:32` | RevokeAPIKey, SetAPIKeyStatus, SetAPIKeyExpiry |
| `APIKeyUsageRecorder` (1) | `internal/database/iface_auth.go:43` | TouchAPIKeyLastUsed |
| `InviteStore` (5) | `internal/database/iface_auth.go:83` | CreateInvite, GetInvite, ListActiveInvites, DeleteInvite, ConsumeInvite |
| `UserPreferenceStore` (7) | `internal/database/iface_user.go:51` | GetUserPreference, SetUserPreference, DeleteUserPreference, GetAllUserPreferences, SetUserPreferenceForUser, GetUserPreferenceForUser, GetAllPreferencesForUser |
| `MetadataFieldStateStore` (3) | `internal/database/iface_metadata.go:221` | GetMetadataFieldStates, UpsertMetadataFieldState, DeleteMetadataFieldState |
| `MetadataChangeStore` (3) | `internal/database/iface_metadata.go:228` | RecordMetadataChange, GetMetadataChangeHistory, GetBookChangeHistory |
| `AlternativeTitleStore` (1) | `internal/database/iface_metadata.go:237` | GetBookAlternativeTitles |
| `MetadataCacheStore` (4) | `internal/database/iface_metadata.go:200` | GetMetadataCache, PutMetadataCache, DeleteMetadataCache, ListMetadataCacheKeys |
| `RejectedMetadataStore` (3) | `internal/database/iface_metadata.go:257` | AddMetadataRejection, GetMetadataRejections, DeleteMetadataRejections |
| `ExternalIDReader` (3) | `internal/database/iface_itunes.go:21` | GetBookByExternalID, GetExternalIDsForBook, GetRemovedExternalIDs |
| `ExternalIDWriter` (4) | `internal/database/iface_itunes.go:28` | CreateExternalIDMapping, BulkCreateExternalIDMappings, SetExternalIDProvenance, MarkExternalIDRemoved |
| `ExternalIDLifecycle` (4) | `internal/database/iface_itunes.go:36` | IsExternalIDTombstoned, TombstoneExternalID, ReassignExternalIDs, ReassignExternalID |
| `BookTagReader` (5) | `internal/database/iface_tags.go:9` | GetBookTags, GetBookTagsByBookIDs, GetBookTagsDetailed, ListAllTags, GetBooksByTag |
| `BookTagWriter` (5) | `internal/database/iface_tags.go:23` | AddBookTag, AddBookTagWithSource, RemoveBookTag, RemoveBookTagsByPrefix, SetBookTags |
| `AuthorTagReader` (4) | `internal/database/iface_tags.go:33` | GetAuthorTags, GetAuthorTagsDetailed, ListAllAuthorTags, GetAuthorsByTag |
| `AuthorTagWriter` (4) | `internal/database/iface_tags.go:41` | AddAuthorTagWithSource, RemoveAuthorTag, RemoveAuthorTagsByPrefix, SetAuthorTags |
| `SeriesTagReader` (4) | `internal/database/iface_tags.go:50` | GetSeriesTags, GetSeriesTagsDetailed, ListAllSeriesTags, GetSeriesByTag |
| `SeriesTagWriter` (4) | `internal/database/iface_tags.go:58` | AddSeriesTagWithSource, RemoveSeriesTag, RemoveSeriesTagsByPrefix, SetSeriesTags |
| `UserTagStore` (4) | `internal/database/iface_tags.go:87` | GetBookUserTags, SetBookUserTags, AddBookUserTag, RemoveBookUserTag |
| `HashBlocklistStore` (5) | `internal/database/iface_bookfile.go:325` | IsHashBlocked, AddBlockedHash, RemoveBlockedHash, GetAllBlockedHashes, GetBlockedHashByHash |
| `OperationLifecycleStore` (4) | `internal/database/iface_ops.go:14` | CreateOperation, UpdateOperationStatus, UpdateOperationError, UpdateOperationResultData |
| `OperationReader` (5) | `internal/database/iface_ops.go:23` | GetOperationByID, GetRecentOperations, ListOperations, GetRecentCompletedOperations, GetInterruptedOperations |
| `OperationStateStore` (3) | `internal/database/iface_ops.go:32` | SaveOperationState, GetOperationState, DeleteOperationState |
| `OperationChangeStore` (4) | `internal/database/iface_ops.go:40` | CreateOperationChange, GetOperationChanges, GetBookChanges, MarkOperationChangesReverted |
| `OperationLogStore` (5) | `internal/database/iface_ops.go:52` | AddOperationLog, GetOperationLogs, SaveOperationSummaryLog, GetOperationSummaryLog, ListOperationSummaryLogs |
| `OperationResultStore` (3) | `internal/database/iface_ops.go:63` | CreateOperationResult, GetOperationResults, GetOperationResultsPage |
| `OperationPruner` (3) | `internal/database/iface_ops.go:75` | PruneOperationLogs, PruneOperationChanges, DeleteOperationWithLogs |
| `OpDefV2Store` (2) | `internal/database/iface_ops_v2.go:178` | UpsertOpDefinitionV2, DeleteOrphanOpDefsV2 |
| `OpV2RunStore` (7) | `internal/database/iface_ops_v2.go:199` | InsertOperationV2, GetOperationV2, UpdateOperationV2Params, IncrementResumeCountV2, MarkOperationV2ManualRetry, SetOperationV2Result, DeleteOperationV2 |
| `OpV2StatusStore` (4) | `internal/database/iface_ops_v2.go:254` | UpdateOperationV2Status, UpdateOperationV2StatusIf, ResetOperationV2ForResume, RepairOpsV2MissingCompletedAt |
| `OpV2QueueStore` (7) | `internal/database/iface_ops_v2.go:281` | ListQueuedOperationsV2, ListActiveOperationsV2, ListResumableOperationsV2, ListOperationsV2Since, CountRunningByPluginV2, PromoteToQueued, ListWaitingDepsOps |
| `OpV2StateStore` (4) | `internal/database/iface_ops_v2.go:314` | GetOpStateV2, DeleteOpStateV2, UpsertOpStateV2, UpdateOpCheckpointV2 |
| `OpV2ObservabilityStore` (7) | `internal/database/iface_ops_v2.go:327` | UpdateOpProgressV2, SetOpQueuedProgressV2, UpdateOpPhaseV2, AppendOpLogsV2, GetOpLogsV2, InsertOpErrorV2, InsertOpStrikeV2 |
| `OpV2DepStore` (2) | `internal/database/iface_ops_v2.go:360` | GetDepRev, BumpDepRev |
| `OpV2CompletionStore` (3) | `internal/database/iface_ops_v2.go:370` | RecordOpCompletion, GetOpCompletion, ListFileCompletions |
| `OpV2BatchStore` (3) | `internal/database/iface_ops_v2.go:385` | AddToBatchBucket, ListBatchBucket, ClearBatchBucket |
| `MaintenanceStore` (8) | `internal/database/iface_system.go:53` | Optimize, CompactionStats, GetScanCacheMap, GetScanCacheMapContext, UpdateScanCache, MarkNeedsRescan, GetDirtyBookFolders, GetDirtyBookFoldersContext |
| `SystemActivityStore` (3) | `internal/database/iface_system.go:44` | AddSystemActivityLog, GetSystemActivityLogs, PruneSystemActivityLogs |
| `AIJobReader` (4) | `internal/database/iface_metadata.go:271` | GetAIJob, GetAIJobByBatchID, GetAIJobPayload, ListAIJobs |
| `AIJobWriter` (6) | `internal/database/iface_metadata.go:279` | CreateAIJob, MarkAIJobSubmitted, MarkAIJobCompleted, MarkAIJobFailed, MarkAIJobApplyFailed, MarkAIJobApplied |
| `ReviewStore` (5) | `internal/database/iface_review.go:18` | UpsertReviewItem, GetReviewItem, ListReviewItems, CountReviewItems, ReviewStatsByKind |
| `LifecycleStore` (2) | `internal/database/iface_system.go:96` | Close, Reset |
| `ImportPathStore` (7) | `internal/database/iface_system.go:102` | GetAllImportPaths, GetImportPathByID, GetImportPathByPath, CreateImportPath, UpdateImportPath, DeleteImportPath, CountBooksByPathPrefix |
| `ITunesStateStore` (6) | `internal/database/iface_itunes.go:11` | SaveLibraryFingerprint, GetLibraryFingerprint, CreateDeferredITunesUpdate, GetPendingDeferredITunesUpdates, MarkDeferredITunesUpdateApplied, GetDeferredITunesUpdatesByBookID |
| `RawKVStore` (7) | `internal/database/iface_system.go:73` | SetRaw, GetRaw, DeleteRaw, ScanPrefix, ScanPrefixPage, CountPrefix, DeleteRawBatch |
| `SettingsStore` (4) | `internal/database/iface_system.go:20` | GetSetting, SetSetting, GetAllSettings, DeleteSetting |
| `StatsStore` (8) | `internal/database/iface_system.go:28` | CountFiles, CountAuthors, CountSeries, GetBookCountsByLocation, GetBookSizesByLocation, GetDashboardStats, SetRootDir, InvalidateLibraryStats |
| `UserPositionStore` (8) | `internal/database/iface_user.go:39` | SetUserPosition, GetUserPosition, ListUserPositionsForBook, ClearUserPositions, SetUserBookState, GetUserBookState, ListUserBookStatesByStatus, ListUserPositionsSince |

