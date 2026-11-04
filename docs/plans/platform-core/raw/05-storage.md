<!-- file: docs/plans/platform-core/raw/05-storage.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0022240f-ec5f-4aa2-bec2-24b85761febf -->
<!-- last-edited: 2026-11-03 -->

# 05 - Storage engines, Pebble keyspaces, migrations, backup (raw scout data)

Anchor commit: `748fd787a`. All anchors `path:line`. Paths are relative or default names only.

## 1. Storage engines actually in use

Engines in `go.mod` direct requires: Pebble v2.1.7 (`go.mod:7`), bleve v2.6.1 (`go.mod:6`), coder/hnsw v0.6.1 (`go.mod:8`), go-memdb v1.3.5 (`go.mod:18`), chromem-go v0.7.0 (`go.mod:23`). `bbolt` is only an indirect dependency (`go.mod:146`). **There is no SQLite and no NutsDB dependency in `go.mod`**; both survive only in comments, tests and a refusal path (below).

| # | Engine | Owning package | What it stores | On-disk default name | Live in default path? | Opened where |
|---|---|---|---|---|---|---|
| 1 | **Pebble (main store)** | `internal/database` (`PebbleStore`, `pebble_store.go:107`) | System of record: every entity, auth, ops, activity log, metrics snapshots (`met:`), AI scan (`aiscan:`), embeddings (`emb:`), dedup state, catalog, config blob/settings, migration bookkeeping | directory `audiobooks.pebble` (`cmd/root.go:488`, `cmd/child_mode.go:63`) + sibling sidecar file `audiobooks.pebble.storage-format` (`storage_format.go:117-123`) | **LIVE, mandatory** | `serveCmd` -> `initializeStore` (`cmd/root.go:280`, bound to `database.InitializeStore` at `cmd/root.go:74`) -> `NewPebbleStore` (`database/store.go:1846`) -> `openPebbleChecked` -> `pebble.Open` (`pebble_store.go:626`). Format ratchet to `pebble.FormatValueSeparation` in the init phase (`storage_format.go:79`). |
| 2 | **go-memdb** (`MemStore`) | `internal/database` (`memdb_store.go:189`, schema `memdb_schema.go:153`) | In-memory query/index layer over Pebble: tables `books`, `authors`, `series`, `book_files`, `narrators`, `book_authors`, `book_narrators`, `import_paths`, `author_aliases`, `blocked_hashes` (`memdb_schema.go:12-21`); optional per-sort-key indexes (`memdb_schema.go:88-95`). Derived, rebuilt every boot; write-through sync | none (RAM only) | **LIVE.** Warm-up runs async from `initPebbleStore`; reads fall back to Pebble until it publishes (`pebble_store.go:795-845`) | `NewMemStore()` at `pebble_store.go:803`; `memPtr` atomic at `pebble_store.go:158` |
| 3 | **bleve (scorch)** | `internal/search` (`bleve_index.go`, `register.go`) | Full-text/field search index of books (derived from Pebble; reconciled by a dirty-set + reconciler `internal/server/search_reconciler.go`) | directory `library.bleve` beside the DB dir, plus `library.bleve.mapping` marker and `library.bleve.rebuilding` marker (`search/register.go:55`, `bleve_index.go:182,262`) | **LIVE but degradable**: open failure is downgraded to a warning and the server runs without search (`register.go:46-60`); mapping-version change deletes and recreates the index (`bleve_index.go:186-215`) | service `searchindex` (`internal/search/register.go`, retrieved at `internal/server/server_lifecycle.go:159`); `bleve.NewUsing(..., "scorch", "scorch", ...)` at `bleve_index.go:240`; `bleve.OpenUsing` at `:197` |
| 4 | **coder/hnsw** (`HNSWEmbeddingStore`) | `internal/database` (`hnsw_embedding_store.go:98`) | In-memory ANN graph per entity type ("book", "author") for dedup layer 2; hydrated from the Pebble `emb:` rows; optional snapshot export on clean shutdown | directory `hnsw/` beside the DB dir with `hnsw-<entityType>.bin` + `hnsw-<entityType>.meta.json` (`hnsw_embedding_store.go:346-352`; dir at `internal/server/server.go:841,1025`; export at `server_lifecycle.go:717-731`; import at `server.go:865`) | **LIVE by default** - default backend resolves to `hnsw` (`internal/server/registry_wire.go:65-71`); the service is optional and returns an untyped nil when `DatabasePath` is empty (`registry_wire.go:136-139`) | service `chromemstore` -> `database.NewHNSWEmbeddingStore(dims)` (`registry_wire.go:154`) |
| 5 | **chromem-go** (`ChromemEmbeddingStore`) | `internal/database` (`chromem_embedding_store.go:65`) | In-memory brute-force cosine vector index; alternative to hnsw | none: persistence deliberately removed (`chromem.NewDB()`, in-memory only; `chromem_embedding_store.go:17-39,67`); the `dir` argument is accepted but unused | **OPTIONAL** - only when `embedding.vector_backend=chromem` (`registry_wire.go:71-76,156`); emits a warning that it is brute force | same `chromemstore` service (`registry_wire.go:156`) |
| 6 | **Pebble (Open Library dump store)** | `internal/openlibrary` (`OLStore`, `store.go:41`) | Local mirror of Open Library data-dump records; keys `ol:edition:`, `ol:edition:isbn10:`, `ol:edition:isbn13:`, `ol:work:`, `ol:work:title:`, `ol:author:`, `ol:author:name:`, `ol:meta:status` (`store.go:97-104`); JSON values (`store.go:515,554`) | directory `oldb` under `<openlibrary-dumps>/` (`internal/metafetch/openlibrary.go:73`; default dump dir `<root-dir>/openlibrary-dumps`, `openlibrary.go:60`) | **OPTIONAL / feature flag**: `openlibrary_dump_enabled` defaults false (`config/config.go:2692`) but the store is opened and the flag auto-enabled if an `oldb` directory already exists (`metafetch/openlibrary.go:73-81`) | `openlibrary.NewOLStore` (`metafetch/openlibrary.go:80,221`); opens with `pebble.FormatNewest` (`openlibrary/store.go:43`) - flagged in `storage_format.go:68-72` as pending TASK-A7 |
| 7 | **Pebble (standalone AI-scan DB)** | `internal/database` (`ai_scan_store.go:156`) | AI author-scan history, with its own counters, no key prefix | caller-chosen path | **DEAD in production**: production uses the shared form `NewAIScanStoreFromDB`/`FromStore` under the `aiscan:` prefix (`registry_wire.go:304`, `ai_scan_store.go:175-181`). The standalone constructor `NewAIScanStore` has no non-test caller (grep) | n/a |
| 8 | **Pebble (read-only/recovery opens)** | `cmd/diagnostics.go:262`, `cmd/pebble-inject-skip/main.go:35`, `internal/database/storage_format_repair.go:300` | raw inspection/repair of the main store, deliberately bypassing the format guard (`storage_format.go:33-36`) | same dir as #1 | Operator tools only | n/a |

Not in the live path (dead or removed), with evidence:
- **SQLite**: removed. `InitializeStore` rejects `sqlite`/`sqlite3` with an error (`database/store.go:1840-1843`); a removed flag is kept as a no-op (`cmd/root.go:62-70,497-508`). Stale remnants: `--db-type` help text "pebble (default) or sqlite" (`cmd/root.go:489`), SQLite branches in the backup package (`backup/backup.go:959,1028,1103`), comments in `chromem_embedding_store.go:12`.
- **NutsDB**: retired. Activity and metrics were migrated to Pebble keyspaces (`act:` and `met:`) that mirror the old NutsDB layout (`pebble_activity_store.go:9-24`, `pebble_metrics_store.go:9-13`, `internal/activity/register.go:9-14`). Only the backend-name strings `"nutsdb"`/`"dual"` remain, and tests assert they are rejected (`internal/activity/register_backend_test.go:76`, `internal/config/activity_backend_env_test.go:137`).
- **bbolt**: indirect only (`go.mod:146`); no import in the repo's non-test code (grep for `bbolt.Open` found nothing).
- Indexed-store decorator over bleve: removed (`internal/server/index_queue.go:10`).

Non-engine on-disk neighbours the code treats as siblings of the DB dir (`internal/appdirs/appdirs.go:55-66`): `certs/`, `library.bleve`, a bootstrap token file, `backups/` (default backup dir, `backup/backup.go:134-146`).

Pebble in-memory mode: `NewPebbleStoreInMemory` (`pebble_store.go:578`) runs the whole store on `vfs.NewMem()` for tests; documented 585s -> 41.8s on the server test package (`pebble_store.go:562-577`).

Gated access: every Pebble handle is wrapped in `GatedDB` (`internal/database/pebble_gate.go:330`) so `Close` can wait for leases; borrowers (embedding, metrics, activity, AI scan, catalog) get it from `PebbleStore.DB()` (`pebble_store.go:960`).

## 2. How borrowers share the one Pebble DB

All "stores" besides the OL store are key-prefix namespaces over the single main Pebble instance: `PebbleActivityStore`, `PebbleMetricsStore`, `EmbeddingStore`, `AIScanStore`, catalog-entry store, master-list store (`internal/database/store_from_store.go:12-63`; `registry_wire.go:270-310`). Consequence for a platform core: there is one DB handle, one WAL, one backup unit and one migration clock; keyspace ownership is by prefix convention enforced only by the key-family registry (next section) and a census test.

## 3. Pebble key prefixes / keyspaces

Source of truth: the `keyFamilies` registry, 258 registered families (`internal/database/keyfamilies.go:37-345`, header comment `:14-36`). The registry feeds the db-census endpoint; an unlisted prefix shows as "(unregistered)" rather than failing, and tests enforce uniqueness and partition coverage (`keyfamilies_test.go:29,48,73`) but NOT that every writer is registered (no test greps writers; UNKNOWN whether a lint does). Nesting rule: a prefix that starts with another is its child (`keyfamilies.go:25-26`).

Extra keyspaces outside this registry: the OL store's `ol:` namespace (separate Pebble DB, section 1 row 6); the standalone AI-scan DB used the bare prefix `""` (un-namespaced; dead in prod, `ai_scan_store.go:165`) versus `aiscan:` when shared (`ai_scan_store.go:176`). `provider_daily_budget:` is in the registry (`keyfamilies.go:246`) and written by `internal/metadata/dailyquota` (`dailyquota.go:85`). Reserved preference rows inside the `pref:`/`preference:` space gate whether the DB opens: `storage_format` and `storage_migration` (`storage_format.go:83-87`), plus `db_version` and `migration_<n>` (`migration_bookkeeping.go:21`).

Value encoding: the `internal/database` package contains no gob and no protobuf. Rows are `encoding/json` (`json.Marshal` appears in 84 non-test files of the package). Binary exceptions confirmed in code: counters are ASCII decimal (`pebble_store.go:970-983`); embedding vectors are float16 little-endian then zstd inside a JSON envelope (`embedding_store.go:86-100,2476,2513`); `dedup:seq` is an 8-byte little-endian int64 (`embedding_store.go:80-83`); fingerprint windows are custom big-endian binary (`fingerprint_window.go:104-139`); a few epoch/clock values are 8-byte big-endian (`ident_state.go:1714-1731`, `dirty_set.go:76`, `pebble_store_journal_clock.go:183`). The "Encoding" column below is `JSON (default; not verified per family)` unless a row says otherwise - I did not open all 258 writers. gob appears only in the operations registry's plugin-state codec (`internal/operations/registry/reporter_db.go:9`, comment). The HNSW snapshot files are coder/hnsw's own little-endian binary (`<gomodcache>/github.com/coder/hnsw@v0.6.1/encode.go:16`), not Pebble.

Namespacing: 256 of 258 families are `name:`-prefixed (namespaced). Two are bare keys: `narrator_counter` (`keyfamilies.go:84`) and `sync_alias_use_seed_cutoff` (`:316`). Families the registry itself labels legacy/retired/dead are marked LEGACY below. No family is a truly un-prefixed legacy row-key scheme at HEAD; the pre-prefix era is represented by `library:` (migrated to `import_path:`), `dedup_candidate:`, `op:`, `operation:`/`operationlog:`/`opstate:`/`opsummary:` (v1 ops, superseded by `opv2:`/`opv3:`), and the single-valued `book:hash:`/`book:originalhash:`/`book:organizedhash:` indexes.

Columns: registry line = where the literal is declared; owner = file the registry names; first literal in owner = first non-comment line in the owner containing the quoted prefix (blank = prefix is assembled at runtime, e.g. `act:<tier>:`, `book:isbn10:`; the registry header says so, `keyfamilies.go:28-36`).

| Prefix | Registry line | Owner file | First literal in owner | Namespaced? | Encoding | What it holds (registry description) |
|---|---|---|---|---|---|---|
| `act:` | 39 | `internal/database/pebble_activity_store.go` | `pebble_activity_store.go:231` | namespaced | index entry (empty or id string; not verified per family) | activity log: tier primaries plus op/book/filter indexes |
| `act:audit:` | 40 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, audit tier |
| `act:batch:` | 41 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, batch tier |
| `act:bk:` | 42 | `internal/database/pebble_activity_store.go` | `pebble_activity_store.go:442` | namespaced | index entry (empty or id string; not verified per family) | activity index by book id |
| `act:change:` | 43 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, change tier |
| `act:debug:` | 44 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, debug tier |
| `act:digest:` | 45 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, daily digest rollups |
| `act:info:` | 46 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, info tier |
| `act:lvl:` | 47 | `internal/database/pebble_activity_filter_index.go` | `pebble_activity_filter_index.go:125` | namespaced | index entry (empty or id string; not verified per family) | activity filter index by level |
| `act:op:` | 48 | `internal/database/pebble_activity_store.go` | `pebble_activity_store.go:442` | namespaced | index entry (empty or id string; not verified per family) | activity index by operation id |
| `act:src:` | 49 | `internal/database/pebble_activity_filter_index.go` | `pebble_activity_filter_index.go:115` | namespaced | index entry (empty or id string; not verified per family) | activity filter index by source |
| `act:system:` | 50 | `internal/database/pebble_activity_store.go` |  | namespaced | JSON (default; not verified per family) | activity primary rows, system tier |
| `act:typ:` | 51 | `internal/database/pebble_activity_filter_index.go` | `pebble_activity_filter_index.go:120` | namespaced | index entry (empty or id string; not verified per family) | activity filter index by type |
| `aijob:` | 54 | `internal/database/pebble_store_aijobs.go` | `pebble_store_aijobs.go:31` | namespaced | JSON (default; not verified per family) | AI job rows |
| `aijob_batch:` | 55 | `internal/database/pebble_store_aijobs.go` | `pebble_store_aijobs.go:62` | namespaced | index entry (empty or id string; not verified per family) | AI job index by batch id |
| `aijob_payload:` | 56 | `internal/database/pebble_store_aijobs.go` | `pebble_store_aijobs.go:35` | namespaced | JSON (default; not verified per family) | AI job request payloads |
| `aijournal:` | 57 | `internal/ai/resultjournal/journal.go` | `journal.go:51` | namespaced | JSON (default; not verified per family) | durable AI result journal (whisper, batch) |
| `aiscan:` | 58 | `internal/database/ai_scan_store.go` | `ai_scan_store.go:42` | namespaced | JSON (default; not verified per family) | AI scan history, phases, results and artifacts (shared-DB namespace) |
| `abs_sess:` | 61 | `internal/database/pebble_store_abssession.go` | `pebble_store_abssession.go:35` | namespaced | JSON (default; not verified per family) | Audiobookshelf-compat sessions |
| `admin_debug_edit:` | 62 | `internal/server/handlers/admindebug/handler.go` | `handler.go:406` | namespaced | JSON (default; not verified per family) | admin debug-edit undo images |
| `apikey:` | 63 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:1122` | namespaced | JSON (default; not verified per family) | API keys |
| `idx:` | 64 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:67` | namespaced | index entry (empty or id string; not verified per family) | secondary indexes for auth, users, sessions, playback, playlists and collections |
| `invite:` | 65 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:1314` | namespaced | JSON (default; not verified per family) | user invites |
| `guestcftoken:` | 66 | `internal/database/pebble_store_guest_tokens.go` | `pebble_store_guest_tokens.go:22` | namespaced | JSON (default; not verified per family) | per-guest Cloudflare service token records (token id + secret hash, never the secret) |
| `inviteallow:` | 67 | `internal/database/pebble_store_invite_delivery.go` | `pebble_store_invite_delivery.go:105` | namespaced | JSON (default; not verified per family) | SSO sign-in grants for emails of accepted invites |
| `oauthid:` | 68 | `internal/database/oauth_identity.go` | `oauth_identity.go:72` | namespaced | JSON (default; not verified per family) | OAuth identities |
| `role:` | 69 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:941` | namespaced | JSON (default; not verified per family) | roles |
| `sess:` | 70 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:787` | namespaced | JSON (default; not verified per family) | web sessions |
| `u:` | 71 | `internal/database/pebble_store_auth.go` | `pebble_store_auth.go:169` | namespaced | JSON (default; not verified per family) | users |
| `usermerge:journal:` | 72 | `internal/database/user_merge.go` | `user_merge.go:39` | namespaced | JSON (default; not verified per family) | admin user-merge journals: every key written, with its before and after image (never pruned; the only way back) |
| `author:` | 75 | `internal/database/pebble_store_authors.go` | `pebble_store_authors.go:31` | namespaced | JSON (default; not verified per family) | author rows |
| `author:name:` | 76 | `internal/database/pebble_store_name_index.go` | `pebble_store_name_index.go:111` | namespaced | index entry (empty or id string; not verified per family) | author index by normalized name |
| `author_books:` | 77 | `internal/database/pebble_store_author_books_index.go` | `pebble_store_author_books_index.go:76` | namespaced | index entry (empty or id string; not verified per family) | author to books index, credit-row source (one key per author and book) |
| `author_books_legacy:` | 78 | `internal/database/pebble_store_author_books_index.go` | `pebble_store_author_books_index.go:77` | namespaced | index entry (empty or id string; not verified per family) | author to books index, Book.AuthorID source (one key per author and book) |
| `author_alias:` | 79 | `internal/database/pebble_store_authors.go` | `pebble_store_authors.go:619` | namespaced | index entry (empty or id string; not verified per family) | author aliases and their indexes |
| `author_tag:` | 80 | `internal/database/pebble_store.go` | `pebble_store.go:6485` | namespaced | JSON (default; not verified per family) | author tags |
| `author_tag_idx:` | 81 | `internal/database/pebble_store.go` | `pebble_store.go:6486` | namespaced | index entry (empty or id string; not verified per family) | author tag reverse index |
| `author_tombstone:` | 82 | `internal/database/pebble_store_authors.go` | `pebble_store_authors.go:1528` | namespaced | JSON (default; not verified per family) | merged-away author id redirects |
| `narrator:` | 83 | `internal/database/pebble_store_authors.go` | `pebble_store_authors.go:1248` | namespaced | JSON (default; not verified per family) | narrator rows |
| `narrator_counter` | 84 | `internal/database/pebble_store_authors.go` | `pebble_store_authors.go:1229` | bare key (no namespace) | ASCII decimal | narrator id counter (single bare key) |
| `narrator_fold:` | 85 | `internal/database/pebble_store_narrator_fold_index.go` | `pebble_store_narrator_fold_index.go:71` | namespaced | index entry (empty or id string; not verified per family) | narrator index by fold key (case, accents, punctuation, spacing folded), one key per fold and narrator id |
| `narrator_name:` | 86 | `internal/database/pebble_store_name_index.go` | `pebble_store_name_index.go:113` | namespaced | index entry (empty or id string; not verified per family) | narrator index by normalized name |
| `series:` | 87 | `internal/database/pebble_store_series.go` | `pebble_store_series.go:31` | namespaced | JSON (default; not verified per family) | series rows |
| `series:name:` | 88 | `internal/database/pebble_store_name_index.go` | `pebble_store_name_index.go:118` | namespaced | index entry (empty or id string; not verified per family) | series index by normalized name and author |
| `series_tag:` | 89 | `internal/database/pebble_store.go` | `pebble_store.go:6497` | namespaced | JSON (default; not verified per family) | series tags |
| `series_tag_idx:` | 90 | `internal/database/pebble_store.go` | `pebble_store.go:6498` | namespaced | index entry (empty or id string; not verified per family) | series tag reverse index |
| `work:` | 91 | `internal/database/pebble_store_works.go` | `pebble_store_works.go:79` | namespaced | JSON (default; not verified per family) | work rows |
| `alt_titles:` | 94 | `internal/database/pebble_store.go` | `pebble_store.go:4533` | namespaced | JSON (default; not verified per family) | alternate titles per book |
| `b:duration_map:` | 95 | `internal/database/pebble_store.go` | `pebble_store.go:5963` | namespaced | JSON (default; not verified per family) | per-book segment duration maps |
| `book:` | 96 | `internal/database/pebble_store.go` | `pebble_store.go:1766` | namespaced | index entry (empty or id string; not verified per family) | book rows (book:<ULID>) and the book:<index>: sub-families |
| `book:asin:` | 97 | `internal/database/pebble_store_isbn_index.go` |  | namespaced | index entry (empty or id string; not verified per family) | book index by ASIN (multi-value) |
| `book:author:` | 98 | `internal/maintenance/jobs/retention_and_hygiene.go` | `retention_and_hygiene.go:221` | LEGACY / retired | index entry (empty or id string; not verified per family) | dead legacy index, swept by retention |
| `book:hash:` | 99 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:93` | LEGACY / retired | index entry (empty or id string; not verified per family) | retired single-valued book index by file hash (deleted by the book-hash-index repair) |
| `book:isbn10:` | 100 | `internal/database/pebble_store_isbn_index.go` |  | namespaced | index entry (empty or id string; not verified per family) | book index by ISBN-10 (multi-value) |
| `book:isbn13:` | 101 | `internal/database/pebble_store_isbn_index.go` |  | namespaced | index entry (empty or id string; not verified per family) | book index by ISBN-13 (multi-value) |
| `book:organizedhash:` | 102 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:99` | LEGACY / retired | index entry (empty or id string; not verified per family) | retired single-valued book index by organized file hash |
| `book:originalhash:` | 103 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:96` | LEGACY / retired | index entry (empty or id string; not verified per family) | retired single-valued book index by original file hash |
| `book:path:` | 104 | `internal/database/pebble_store.go` | `pebble_store.go:1852` | namespaced | index entry (empty or id string; not verified per family) | book index by file path |
| `book:series:` | 105 | `internal/maintenance/jobs/retention_and_hygiene.go` | `retention_and_hygiene.go:221` | LEGACY / retired | index entry (empty or id string; not verified per family) | dead legacy index, swept by retention |
| `book:versiongroup:` | 106 | `internal/database/pebble_store.go` | `pebble_store.go:3273` | namespaced | index entry (empty or id string; not verified per family) | book index by version group |
| `book:work:` | 107 | `internal/database/pebble_store.go` | `pebble_store.go:3282` | namespaced | index entry (empty or id string; not verified per family) | book index by work |
| `book_atpath:` | 108 | `internal/database/pebble_store_atpath_index.go` | `pebble_store_atpath_index.go:56` | namespaced | index entry (empty or id string; not verified per family) | books-at-path index |
| `book_atpath_undecodable:` | 109 | `internal/database/pebble_store_atpath_index.go` | `pebble_store_atpath_index.go:77` | namespaced | JSON (default; not verified per family) | undecodable books-at-path markers |
| `book_authors:` | 110 | `internal/database/pebble_store.go` | `pebble_store.go:2780` | namespaced | JSON (default; not verified per family) | book to author links |
| `book_hash:` | 111 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:93` | namespaced | index entry (empty or id string; not verified per family) | book index by file hash (multi-value, one key per hash and book) |
| `book_narrators:` | 112 | `internal/database/pebble_store.go` | `pebble_store.go:4531` | namespaced | JSON (default; not verified per family) | book to narrator links |
| `book_organizedhash:` | 113 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:99` | namespaced | index entry (empty or id string; not verified per family) | book index by organized file hash (multi-value) |
| `book_originalhash:` | 114 | `internal/database/pebble_store_book_hash_index.go` | `pebble_store_book_hash_index.go:96` | namespaced | index entry (empty or id string; not verified per family) | book index by original file hash (multi-value) |
| `book_sig:` | 115 | `internal/database/pebble_store_booksig.go` | `pebble_store_booksig.go:67` | namespaced | JSON (default; not verified per family) | book signatures |
| `book_tag:` | 116 | `internal/database/pebble_store.go` | `pebble_store.go:4551` | namespaced | JSON (default; not verified per family) | book tags |
| `book_ver:` | 117 | `internal/database/pebble_store.go` | `pebble_store.go:3559` | namespaced | JSON (default; not verified per family) | book version history snapshots (book_ver:<id>:<nanos>) |
| `blocked:hash:` | 118 | `internal/database/pebble_store_blocklist.go` | `pebble_store_blocklist.go:18` | namespaced | JSON (default; not verified per family) | blocked file hashes |
| `bv:` | 119 | `internal/database/pebble_store.go` | `pebble_store.go:5372` | namespaced | JSON (default; not verified per family) | book versions (torrent-backed) |
| `cover_text:` | 120 | `internal/covertext/covertext.go` | `covertext.go:41` | namespaced | JSON (default; not verified per family) | cover OCR text records |
| `cover_text_book:` | 121 | `internal/covertext/covertext.go` | `covertext.go:42` | namespaced | index entry (empty or id string; not verified per family) | cover OCR text index by book |
| `deferred_itunes:` | 122 | `internal/database/pebble_store_itunes.go` | `pebble_store_itunes.go:101` | namespaced | JSON (default; not verified per family) | deferred iTunes updates |
| `ext_id:` | 123 | `internal/database/pebble_store_externalids.go` | `pebble_store_externalids.go:33` | namespaced | JSON (default; not verified per family) | external id mappings |
| `import_path:` | 124 | `internal/database/pebble_store_importpaths.go` | `pebble_store_importpaths.go:32` | namespaced | JSON (default; not verified per family) | import paths |
| `itunes:` | 125 | `internal/database/pebble_store_stats.go` | `pebble_store_stats.go:480` | namespaced | JSON (default; not verified per family) | iTunes library fingerprints |
| `library:` | 126 | `internal/database/pebble_store.go` | `pebble_store.go:1062` | LEGACY / retired | JSON (default; not verified per family) | legacy library paths, migrated to import_path: |
| `merge_user_state_pending:` | 127 | `internal/merge/pending_repair.go` | `pending_repair.go:30` | namespaced | JSON (default; not verified per family) | pending user-state repairs after a merge |
| `merge:combine-journal:` | 128 | `internal/merge/combine_journal.go` | `combine_journal.go:55` | namespaced | JSON (default; not verified per family) | book combine journal |
| `merge:combine-retire:` | 129 | `internal/merge/retire_link.go` | `retire_link.go:37` | LEGACY / retired | JSON (default; not verified per family) | links a combine's absorbed shell to each retire op that retired it (one key per shell and op), so the combine's undo reverses the retire first |
| `merge:survivor-reconcile:` | 130 | `internal/merge/combine_journal.go` | `combine_journal.go:1970` | namespaced | JSON (default; not verified per family) | per-user markers that make a touched survivor's put-back safe to re-run |
| `scanner:ai_parse_single_fail:` | 131 | `internal/scanner/ai_parse_giveup.go` | `ai_parse_giveup.go:34` | namespaced | JSON (default; not verified per family) | single-file AI parse give-up markers by path hash |
| `apply_refusal:` | 132 | `internal/server/batch_apply_refusal.go` | `batch_apply_refusal.go:33` | namespaced | JSON (default; not verified per family) | structural refusals the cached-metadata batch apply remembers, one per book, with the fingerprint of the inputs that produced them |
| `metadata_cache:` | 133 | `internal/database/pebble_store_metadata_cache.go` | `pebble_store_metadata_cache.go:22` | namespaced | JSON (default; not verified per family) | metadata provider response cache |
| `ident:state:` | 134 | `internal/database/ident_state.go` | `ident_state.go:157` | namespaced | JSON (default; not verified per family) | book identification state and reason per book (derived, rebuilt on boot) |
| `ident:by:` | 135 | `internal/database/ident_state.go` | `ident_state.go:158` | namespaced | JSON (default; not verified per family) | book identification membership by state, keys only plus a primary-scope byte |
| `ident:dirty:` | 136 | `internal/database/ident_state.go` | `ident_state.go:159` | namespaced | JSON (default; not verified per family) | book identification dirty set (epoch per book) |
| `ident:meta:` | 137 | `internal/database/ident_state.go` | `ident_state.go:161` | namespaced | index entry (empty or id string; not verified per family) | book identification index format marker |
| `ident:reason:` | 138 | `internal/database/ident_state.go` | `ident_state.go:160` | namespaced | JSON (default; not verified per family) | identification driver's reason per book with the question fingerprint it answers |
| `metadata_change:` | 139 | `internal/database/pebble_store_metadata.go` | `pebble_store_metadata.go:77` | namespaced | JSON (default; not verified per family) | metadata change records |
| `metadata_fetch_cache:` | 140 | `internal/database/metadata_fetch_cache.go` | `metadata_fetch_cache.go:106` | namespaced | JSON (default; not verified per family) | metadata fetch cache per book and source |
| `metadata_rejection:` | 141 | `internal/database/pebble_store_metadata.go` | `pebble_store_metadata.go:197` | namespaced | JSON (default; not verified per family) | rejected metadata candidates |
| `metadata_state:` | 142 | `internal/database/pebble_store_metadata.go` | `pebble_store_metadata.go:18` | namespaced | JSON (default; not verified per family) | per-field metadata state |
| `path_history:` | 143 | `internal/database/pebble_store_scancache.go` | `pebble_store_scancache.go:538` | namespaced | JSON (default; not verified per family) | book path change history |
| `purge_pending_file:` | 144 | `internal/audiobooks/purge_pending_journal.go` | `purge_pending_journal.go:43` | namespaced | JSON (default; not verified per family) | file removals a purge pass decided on and has not yet carried out, one per purged book |
| `rejected_candidate:` | 145 | `internal/server/metadata_batch_candidates.go` |  | namespaced | JSON (default; not verified per family) | rejected batch metadata candidates |
| `revert_settle_owed:` | 146 | `internal/audiobooks/revert_settle.go` | `revert_settle.go:39` | namespaced | JSON (default; not verified per family) | operation reverts owed a settle pass (revert_settle_owed:#corrupt:<time>:<record> holds an undecodable record set aside) |
| `scan_fail:` | 147 | `internal/database/pebble_store_quarantine.go` | `pebble_store_quarantine.go:98` | namespaced | JSON (default; not verified per family) | scan quarantine records by path hash |
| `split_book_candidate:` | 148 | `internal/dedup/split_book_storage.go` | `split_book_storage.go:38` | namespaced | JSON (default; not verified per family) | split-book dedup candidates |
| `tombstone:` | 149 | `internal/database/pebble_store.go` | `pebble_store.go:5236` | namespaced | JSON (default; not verified per family) | deleted-book tombstones |
| `user_tag:` | 150 | `internal/database/pebble_store.go` | `pebble_store.go:4532` | namespaced | JSON (default; not verified per family) | user tags |
| `ref_asin:` | 153 | `internal/authority/authoritybuild/apply.go` |  | namespaced | JSON (default; not verified per family) | authority lists: contributor ASIN to every name fold it was credited under |
| `ref_ovr:` | 154 | `internal/authority/lookup.go` |  | namespaced | JSON (default; not verified per family) | authority lists: owner overrides for persons and publishers (never rebuilt) |
| `ref_person:` | 155 | `internal/authority/authoritybuild/apply.go` |  | namespaced | JSON (default; not verified per family) | authority lists: known persons by name fold, with roles, tiers and contributor ASINs |
| `ref_pub:` | 156 | `internal/authority/authoritybuild/apply.go` |  | namespaced | JSON (default; not verified per family) | authority lists: known publishers by name fold |
| `ref_src:` | 157 | `internal/authority/authoritybuild/apply.go` |  | namespaced | JSON (default; not verified per family) | authority lists: per-source ingest ledger (item digest) |
| `bf:` | 160 | `internal/database/pebble_store.go` | `pebble_store.go:5783` | namespaced | JSON (default; not verified per family) | book file segments |
| `bfs:` | 161 | `internal/database/pebble_store.go` | `pebble_store.go:5787` | namespaced | index entry (empty or id string; not verified per family) | book file segment index by book |
| `book_file:` | 162 | `internal/database/pebble_store.go` | `pebble_store.go:6562` | namespaced | JSON (default; not verified per family) | book file rows |
| `book_file_acoustid:` | 163 | `internal/database/pebble_store.go` | `pebble_store.go:6605` | namespaced | index entry (empty or id string; not verified per family) | book file index by AcoustID |
| `book_file_error:` | 164 | `internal/database/pebble_book_file_errors.go` | `pebble_book_file_errors.go:34` | namespaced | JSON (default; not verified per family) | book file errors by path |
| `book_file_errors_by_book:` | 165 | `internal/database/pebble_book_file_errors.go` | `pebble_book_file_errors.go:67` | namespaced | index entry (empty or id string; not verified per family) | book file error index by book |
| `book_file_gone:` | 166 | `internal/database/book_delete_owns_files.go` | `book_delete_owns_files.go:728` | namespaced | JSON (default; not verified per family) | deleted book file markers |
| `book_file_hash:` | 167 | `internal/database/pebble_store.go` | `pebble_store.go:1973` | namespaced | index entry (empty or id string; not verified per family) | book file index by hash |
| `book_file_id:` | 168 | `internal/database/pebble_store.go` | `pebble_store.go:6561` | namespaced | index entry (empty or id string; not verified per family) | book file index by id |
| `book_file_orig_hash:` | 169 | `internal/database/pebble_store.go` | `pebble_store.go:1973` | namespaced | index entry (empty or id string; not verified per family) | book file index by original hash |
| `book_file_path:` | 170 | `internal/database/pebble_store.go` | `pebble_store.go:6574` | namespaced | index entry (empty or id string; not verified per family) | book file index by path CRC |
| `book_file_pid:` | 171 | `internal/database/pebble_store.go` | `pebble_store.go:6567` | namespaced | index entry (empty or id string; not verified per family) | book file index by iTunes persistent id |
| `chapters:` | 172 | `internal/database/pebble_store_chapters.go` | `pebble_store_chapters.go:31` | namespaced | JSON (default; not verified per family) | per-book chapter lists |
| `file_prov:` | 173 | `internal/database/pebble_file_provenance.go` | `pebble_file_provenance.go:32` | namespaced | JSON (default; not verified per family) | file provenance records |
| `file_prov_hash:` | 174 | `internal/database/pebble_file_provenance.go` | `pebble_file_provenance.go:33` | namespaced | index entry (empty or id string; not verified per family) | file provenance index by hash |
| `file_prov_orphan:` | 175 | `internal/database/pebble_file_provenance.go` | `pebble_file_provenance.go:34` | namespaced | JSON (default; not verified per family) | orphaned file provenance records |
| `file_prov_seq:` | 176 | `internal/database/pebble_file_provenance.go` | `pebble_file_provenance.go:39` | namespaced | index entry (empty or id string; not verified per family) | file provenance sequence index |
| `pending_file_op:` | 177 | `internal/server/file_io_pool.go` | `file_io_pool.go:31` | namespaced | JSON (default; not verified per family) | pending file operations for crash recovery |
| `cat:` | 180 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:58` | namespaced | JSON (default; not verified per family) | catalog entries |
| `cat_author:` | 181 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:62` | namespaced | index entry (empty or id string; not verified per family) | catalog index by author |
| `cat_author_state:` | 182 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:68` | namespaced | JSON (default; not verified per family) | catalog per-author harvest state |
| `cat_eg:` | 183 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:64` | namespaced | JSON (default; not verified per family) | catalog entry groups |
| `cat_egkey:` | 184 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:65` | namespaced | JSON (default; not verified per family) | catalog entry group keys |
| `cat_hv:` | 185 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:66` | namespaced | JSON (default; not verified per family) | catalog harvest records |
| `cat_id:` | 186 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:59` | namespaced | index entry (empty or id string; not verified per family) | catalog index by id |
| `cat_pid:` | 187 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:61` | namespaced | index entry (empty or id string; not verified per family) | catalog index by provider id |
| `cat_raw:` | 188 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:60` | namespaced | JSON (default; not verified per family) | catalog raw provider payloads |
| `cat_series:` | 189 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:63` | namespaced | index entry (empty or id string; not verified per family) | catalog index by series |
| `cat_stale:` | 190 | `internal/database/catalog_entry_store.go` | `catalog_entry_store.go:67` | namespaced | index entry (empty or id string; not verified per family) | catalog staleness index |
| `mlist:list:` | 193 | `internal/database/master_list_store.go` | `master_list_store.go:50` | namespaced | JSON (default; not verified per family) | master-list uploads: one metadata row per list |
| `mlist:row:` | 194 | `internal/database/master_list_store.go` | `master_list_store.go:51` | namespaced | JSON (default; not verified per family) | master-list rows, one per input row, in input order |
| `mlist:elsewhere:` | 195 | `internal/database/master_list_elsewhere.go` | `master_list_elsewhere.go:38` | namespaced | JSON (default; not verified per family) | read-elsewhere records: books a master list says the owner has read or owns that the library does not hold |
| `decision:ent:` | 198 | `internal/decisions/store.go` |  | namespaced | index entry (empty or id string; not verified per family) | decision history index by entity (never pruned) |
| `decision:mb:` | 199 | `internal/decisions/store.go` |  | namespaced | JSON (default; not verified per family) | decision veto membership rows for rejected sets over 50 entities (never pruned) |
| `decision:op:` | 200 | `internal/decisions/store.go` |  | namespaced | index entry (empty or id string; not verified per family) | decision history index by op kind, export order (never pruned) |
| `decision:pe:` | 201 | `internal/decisions/store.go` |  | namespaced | index entry (empty or id string; not verified per family) | decision pair-edge veto index, one row per rejecting record (never pruned) |
| `decision:r:` | 202 | `internal/decisions/store.go` |  | namespaced | JSON (default; not verified per family) | decision log: one labelled example per owner decision (never pruned) |
| `decision:va:` | 203 | `internal/decisions/store.go` |  | namespaced | index entry (empty or id string; not verified per family) | decision live-reject index: one row per active or reopened reject, what the veto snapshot scans (never pruned) |
| `decision:v:` | 204 | `internal/decisions/store.go` |  | namespaced | JSON (default; not verified per family) | decision verdicts: current reject state of each proposal (never pruned) |
| `dedup:` | 207 | `internal/database/embedding_store.go` | `embedding_store.go:88` | namespaced | JSON (default; not verified per family) | dedup engine state |
| `dedup:automerge:` | 208 | `internal/database/dedup_automerge_journal.go` | `dedup_automerge_journal.go:35` | namespaced | JSON (default; not verified per family) | dedup auto-merge journal |
| `dedup:e:` | 209 | `internal/database/embedding_store.go` | `embedding_store.go:90` | namespaced | index entry (empty or id string; not verified per family) | dedup candidate index by entity |
| `dedup:label:` | 210 | `internal/database/dedup_label.go` | `dedup_label.go:20` | namespaced | JSON (default; not verified per family) | dedup labels |
| `dedup:lbe:` | 211 | `internal/database/dedup_label.go` | `dedup_label.go:39` | namespaced | index entry (empty or id string; not verified per family) | dedup label index by entity |
| `dedup:p:` | 212 | `internal/database/embedding_store.go` | `embedding_store.go:89` | namespaced | index entry (empty or id string; not verified per family) | dedup candidate pair index |
| `dedup:r:` | 213 | `internal/database/embedding_store.go` | `embedding_store.go:88` | namespaced | JSON (default; not verified per family) | dedup candidate records |
| `dedup:s:` | 214 | `internal/database/embedding_store.go` | `embedding_store.go:91` | namespaced | index entry (empty or id string; not verified per family) | dedup candidate index by status |
| `dedup_candidate:` | 215 | `internal/database/pebble_quick_queries.go` | `pebble_quick_queries.go:241` | LEGACY / retired | JSON (default; not verified per family) | legacy dedup candidates, read by quick queries |
| `emb:` | 216 | `internal/database/embedding_store.go` | `embedding_store.go:86` | namespaced | JSON (default; not verified per family) | embeddings |
| `emb:c:` | 217 | `internal/database/embedding_store.go` | `embedding_store.go:87` | namespaced | JSON envelope (vector blob as embedding) | embedding cache |
| `emb:v:` | 218 | `internal/database/embedding_store.go` | `embedding_store.go:86` | namespaced | JSON envelope, vector field = float16 LE + zstd | embedding vectors |
| `fpidx:` | 221 | `internal/database/pebble_store.go` | `pebble_store.go:6633` | namespaced | custom binary (BE uint64 bands) / empty | fingerprint LSH index |
| `fpidx_meta:` | 222 | `internal/database/pebble_store.go` | `pebble_store.go:6634` | namespaced | custom binary (BE uint64 bands) / empty | fingerprint LSH index metadata |
| `fpwin:` | 223 | `internal/database/fingerprint_window.go` | `fingerprint_window.go:88` | namespaced | custom binary (BE uint64 bands) / empty | windowed fingerprints |
| `fpwin_fail:` | 224 | `internal/database/fingerprint_window.go` | `fingerprint_window.go:89` | namespaced | custom binary (BE uint64 bands) / empty | windowed fingerprint failures |
| `fpwinidx:` | 225 | `internal/database/fingerprint_window.go` | `fingerprint_window.go:90` | namespaced | custom binary (BE uint64 bands) / empty | window-print band index (one row per band key and file) |
| `fpwinidx_meta:` | 226 | `internal/database/fingerprint_window.go` | `fingerprint_window.go:91` | namespaced | custom binary (BE uint64 bands) / empty | window-print band index member lists |
| `asinbackfill:` | 229 | `internal/plugins/metafetch/asin_backfill.go` | `asin_backfill.go:105` | namespaced | JSON (default; not verified per family) | ASIN/ISBN backfill per-book miss markers |
| `counter:` | 230 | `internal/database/pebble_store.go` | `pebble_store.go:730` | namespaced | ASCII decimal | id counters |
| `cursor:` | 231 | `internal/database/pebble_file_provenance.go` | `pebble_file_provenance.go:47` | namespaced | JSON (default; not verified per family) | export cursors |
| `met:` | 232 | `internal/database/pebble_metrics_store.go` | `pebble_metrics_store.go:43` | namespaced | JSON (default; not verified per family) | metrics store |
| `pcache:` | 233 | `internal/database/metadata_fetch_cache.go` | `metadata_fetch_cache.go:374` | namespaced | JSON (default; not verified per family) | shared metadata provider answers, keyed by question (P1) or product id (P2) |
| `pcache:asin:` | 234 | `internal/database/metadata_fetch_cache.go` | `metadata_fetch_cache.go:376` | namespaced | JSON (default; not verified per family) | shared provider product lookups by provider and ASIN (P2), positive and negative |
| `pcache:book:` | 235 | `internal/database/metadata_fetch_cache.go` | `metadata_fetch_cache.go:383` | namespaced | JSON (default; not verified per family) | per-book pointer rows: the shared P1 keys a book's questions map to, for the Search again bust |
| `pcache:q:` | 236 | `internal/database/metadata_fetch_cache.go` | `metadata_fetch_cache.go:375` | namespaced | JSON (default; not verified per family) | shared provider answers by provider, endpoint and normalized-question hash (P1), including empty answers |
| `pref:` | 237 | `internal/database/pebble_store_preferences.go` | `pebble_store_preferences.go:113` | namespaced | JSON (default; not verified per family) | per-user preferences |
| `pref:_system:` | 238 | `internal/database/pebble_store_preferences.go` |  | namespaced | JSON (default; not verified per family) | the _system user's records (other than the sub-families below) |
| `pref:_system:apply_rename_failure:` | 239 | `internal/organizer/apply_failure.go` |  | namespaced | JSON (default; not verified per family) | durable organizer apply-rename failure records |
| `pref:_system:itunes_clone_record:` | 240 | `internal/plugins/maintenance/itunes_clone_into_library.go` |  | namespaced | JSON (default; not verified per family) | iTunes clone-into-library records |
| `pref:_system:organize_collision_skip:` | 241 | `internal/organizer/apply_failure.go` |  | namespaced | JSON (default; not verified per family) | durable organize collision-skip records |
| `pref:_system:outbox:writeback:` | 242 | `internal/writeback/outbox.go` |  | namespaced | JSON (default; not verified per family) | tag write-back outbox |
| `pref:_system:pipeline_checkpoint:` | 243 | `internal/organizer/checkpoint.go` |  | namespaced | JSON (default; not verified per family) | organizer per-book phase checkpoints |
| `pref:_system:rename_path_write_failure:` | 244 | `internal/organizer/rename_path_failure.go` |  | namespaced | JSON (default; not verified per family) | rename path-write failure records |
| `preference:` | 245 | `internal/database/pebble_store_preferences.go` | `pebble_store_preferences.go:17` | namespaced | JSON (default; not verified per family) | global preferences |
| `provider_daily_budget:` | 246 | `internal/metadata/dailyquota/dailyquota.go` | `dailyquota.go:85` | namespaced | JSON (default; not verified per family) | metadata provider daily lookup counts (shared Google Books budget) |
| `provider_throttle:` | 247 | `internal/database/provider_throttle.go` | `provider_throttle.go:29` | namespaced | JSON (default; not verified per family) | metadata provider throttles |
| `quick_query_cache:` | 248 | `internal/database/pebble_quick_queries.go` | `pebble_quick_queries.go:19` | namespaced | JSON (default; not verified per family) | quick query result cache |
| `setting:` | 249 | `internal/database/settings.go` | `settings.go:299` | namespaced | JSON (default; not verified per family) | settings |
| `setting:repairs_last_apply_op:` | 250 | `internal/server/handlers/repairs/handler.go` |  | namespaced | JSON (default; not verified per family) | last repairs apply op per fixer |
| `setting:repairs_last_plan_op:` | 251 | `internal/server/handlers/repairs/handler.go` |  | namespaced | JSON (default; not verified per family) | last repairs plan op per fixer |
| `stats:` | 252 | `internal/database/pebble_store.go` | `pebble_store.go:432` | namespaced | JSON (default; not verified per family) | cached library, transcribe and playback stats |
| `system:` | 253 | `internal/database/pebble_store_atpath_index.go` | `pebble_store_atpath_index.go:67` | namespaced | JSON (default; not verified per family) | system flags and markers |
| `system:backfill:` | 254 | `internal/database/pebble_store_atpath_index.go` | `pebble_store_atpath_index.go:67` | namespaced | JSON (default; not verified per family) | backfill completion markers and cursors |
| `system:census:` | 255 | `internal/database/census_exact.go` | `census_exact.go:31` | namespaced | JSON (default; not verified per family) | last exact db census and its run progress |
| `op:` | 258 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:1182` | LEGACY / retired | JSON (default; not verified per family) | legacy op dedup and completion records |
| `op:batch:` | 259 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:1562` | namespaced | JSON (default; not verified per family) | op batch membership |
| `op:completion:` | 260 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:1188` | namespaced | JSON (default; not verified per family) | op completion records per subject |
| `op:deprev:` | 261 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:1182` | namespaced | JSON (default; not verified per family) | op dependency revisions per subject |
| `op_result:` | 262 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:444` | namespaced | JSON (default; not verified per family) | per-book operation results |
| `opchange:` | 263 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:652` | namespaced | JSON (default; not verified per family) | operation change journal |
| `opchange_by_book:` | 264 | `internal/database/pebble_store_opchange_index.go` | `pebble_store_opchange_index.go:134` | namespaced | index entry (empty or id string; not verified per family) | operation change index by book |
| `opchange_by_list:` | 265 | `internal/database/pebble_store_opchange_list_index.go` | `pebble_store_opchange_list_index.go:60` | namespaced | index entry (empty or id string; not verified per family) | operation change index by list (carry rows of collections and playlists) |
| `opchange_undecodable:` | 266 | `internal/database/pebble_store_opchange_index.go` | `pebble_store_opchange_index.go:135` | namespaced | JSON (default; not verified per family) | undecodable operation change markers |
| `operation:` | 267 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:37` | legacy v1 | JSON (default; not verified per family) | v1 operation rows |
| `operationlog:` | 268 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:228` | legacy v1 | JSON (default; not verified per family) | v1 operation logs |
| `opstate:` | 269 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:321` | LEGACY / retired | JSON (default; not verified per family) | v1 operation state (checkpoints); the opstate:<opID>:params suffix is LIVE, not legacy: it is the backfill-file-hashes chunk journal (a crash-recovery record, internal/maintenance/jobs/backfill_file_hashes.go), so a cleanup must not sweep it; DeleteOperationState removes it with the op |
| `opsummary:` | 270 | `internal/database/pebble_store_operations.go` | `pebble_store_operations.go:261` | legacy v1 | JSON (default; not verified per family) | v1 operation summaries |
| `opv2:` | 271 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:51` | namespaced | JSON (default; not verified per family) | operations v2 |
| `opv2:act:` | 272 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:67` | namespaced | JSON (default; not verified per family) | operations v2 active set |
| `opv2:def:` | 273 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:51` | namespaced | JSON (default; not verified per family) | operations v2 definitions |
| `opv2:done:` | 274 | `internal/database/pebble_store_ops_v2.go` |  | namespaced | index entry (empty or id string; not verified per family) | timeline index by completed_at nanos (TASK-A5) |
| `opv2:err:` | 275 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:79` | namespaced | JSON (default; not verified per family) | operations v2 errors |
| `opv2:log:` | 276 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:75` | namespaced | JSON (default; not verified per family) | operations v2 logs |
| `opv2:op:` | 277 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:55` | namespaced | JSON (default; not verified per family) | operations v2 rows |
| `opv2:open:` | 278 | `internal/database/pebble_store_ops_v2.go` |  | namespaced | index entry (empty or id string; not verified per family) | timeline index: op not completed (TASK-A5) |
| `opv2:q:` | 279 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:63` | namespaced | JSON (default; not verified per family) | operations v2 queue |
| `opv2:row:` | 280 | `internal/database/pebble_store_op_rows.go` | `pebble_store_op_rows.go:29` | namespaced | JSON (default; not verified per family) | blob rows owned by an operation (a repairs plan's rows), OpRowStore |
| `opv2:rowix:` | 281 | `internal/database/pebble_store_op_rows.go` | `pebble_store_op_rows.go:30` | namespaced | index entry (empty or id string; not verified per family) | per-row index of the opv2:row: set (class, skip kind), scanned without the rows |
| `opv2:state:` | 282 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:71` | namespaced | JSON (default; not verified per family) | operations v2 resume state |
| `opv2:strike:` | 283 | `internal/database/pebble_store_ops_v2.go` | `pebble_store_ops_v2.go:83` | namespaced | JSON (default; not verified per family) | operations v2 failure strikes |
| `opv2:wait:` | 284 | `internal/database/pebble_store_ops_v2_wait.go` | `pebble_store_ops_v2_wait.go:51` | namespaced | index entry (empty or id string; not verified per family) | waiting-deps index: op Status == waiting_deps (STARTUP-GC-WAIT-INDEX) |
| `opv3:` | 285 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:82` | namespaced | index entry (empty or id string; not verified per family) | operations v3 (05-PR4): runs, indexes, audit trail, chunk ledger |
| `opv3:ckpt:` | 286 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:91` | namespaced | JSON (default; not verified per family) | operations v3 run checkpoints (phase, task state, lease table, migrated opv2:state: bytes) |
| `opv3:def:` | 287 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:99` | namespaced | JSON (default; not verified per family) | operations v3 snapshots of removed defs |
| `opv3:evt:` | 288 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:90` | namespaced | JSON (default; not verified per family) | operations v3 state-machine audit trail |
| `opv3:intent:` | 289 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:94` | namespaced | JSON (default; not verified per family) | operations v3 write-ahead intents |
| `opv3:item:` | 290 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:95` | namespaced | JSON (default; not verified per family) | operations v3 failed/skipped item outcomes (capped per run) |
| `opv3:ix:def:` | 291 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:87` | namespaced | index entry (empty or id string; not verified per family) | operations v3 index: runs by def |
| `opv3:ix:done:` | 292 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:88` | namespaced | index entry (empty or id string; not verified per family) | operations v3 index: timeline by completed_at |
| `opv3:ix:parent:` | 293 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:89` | namespaced | index entry (empty or id string; not verified per family) | operations v3 index: pipeline children |
| `opv3:ix:state:` | 294 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:86` | namespaced | index entry (empty or id string; not verified per family) | operations v3 index: exact census per state |
| `opv3:ledger:` | 295 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:93` | namespaced | JSON (default; not verified per family) | operations v3 chunk ledgers |
| `opv3:log:` | 296 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:96` | namespaced | JSON (default; not verified per family) | operations v3 run logs |
| `opv3:mark:` | 297 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:97` | namespaced | JSON (default; not verified per family) | operations v3 keys an op marked with rc.Mark (a Fixer's applied rows), per run |
| `opv3:meta:` | 298 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:100` | namespaced | JSON (default; not verified per family) | operations v3 per-state/per-def counters |
| `opv3:prog:` | 299 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:84` | namespaced | JSON (default; not verified per family) | operations v3 run progress, checkpoint stamp and synced opv2 row hash (the small half of a run record) |
| `opv3:q:` | 300 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:85` | namespaced | JSON (default; not verified per family) | operations v3 dispatch queue |
| `opv3:rowmark:` | 301 | `internal/database/pebble_store_ops_v3_rowmarks.go` | `pebble_store_ops_v3_rowmarks.go:24` | namespaced | JSON (default; not verified per family) | operations v3 per-row Fixer apply markers, written as each row's Apply returns (05-PR5b) |
| `opv3:run:` | 302 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:83` | namespaced | JSON (default; not verified per family) | operations v3 run records |
| `opv3:sched:` | 303 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:98` | namespaced | JSON (default; not verified per family) | operations v3 schedule state (written by 05-PR8) |
| `opv3:snap:` | 304 | `internal/database/pebble_store_ops_v3.go` | `pebble_store_ops_v3.go:92` | namespaced | JSON (default; not verified per family) | operations v3 frozen item-key pages; opv3:snap:{op}:chunks (chunk offsets) sorts inside each run's range |
| `syslog:` | 305 | `internal/database/pebble_store_activity.go` | `pebble_store_activity.go:21` | namespaced | JSON (default; not verified per family) | system log entries |
| `bookmark:` | 308 | `internal/database/pebble_store_bookmarks.go` | `pebble_store_bookmarks.go:76` | namespaced | JSON (default; not verified per family) | bookmarks |
| `col:` | 309 | `internal/database/pebble_store_collections.go` | `pebble_store_collections.go:36` | namespaced | JSON (default; not verified per family) | collections |
| `playe:` | 310 | `internal/database/pebble_store_playback.go` | `pebble_store_playback.go:522` | namespaced | JSON (default; not verified per family) | playback events |
| `playlist:` | 311 | `internal/database/pebble_store_playlists.go` | `pebble_store_playlists.go:40` | namespaced | JSON (default; not verified per family) | playlists |
| `playlistitem:` | 312 | `internal/database/pebble_store_playlists.go` | `pebble_store_playlists.go:116` | namespaced | JSON (default; not verified per family) | playlist items |
| `playp:` | 313 | `internal/database/pebble_store_playback.go` | `pebble_store_playback.go:556` | namespaced | JSON (default; not verified per family) | playback progress |
| `sync_alias_use:` | 314 | `internal/database/pebble_store_sync_alias_use.go` | `pebble_store_sync_alias_use.go:82` | namespaced | JSON (default; not verified per family) | sync alias use records |
| `sync_alias_use_seeded:` | 315 | `internal/database/pebble_store_sync_alias_use.go` | `pebble_store_sync_alias_use.go:90` | namespaced | JSON (default; not verified per family) | sync alias use seeded markers |
| `sync_alias_use_seed_cutoff` | 316 | `internal/database/pebble_store_sync_alias_use.go` | `pebble_store_sync_alias_use.go:213` | bare key (no namespace) | JSON (default; not verified per family) | sync alias seed cutoff (single bare key) |
| `sync_file:` | 317 | `internal/database/pebble_store_syncfile.go` | `pebble_store_syncfile.go:85` | namespaced | JSON (default; not verified per family) | sync file lookups |
| `sync_item:` | 318 | `internal/database/pebble_store_syncid.go` | `pebble_store_syncid.go:124` | namespaced | JSON (default; not verified per family) | sync item ids |
| `ubs:` | 319 | `internal/database/pebble_store_playback.go` | `pebble_store_playback.go:311` | namespaced | JSON (default; not verified per family) | user book state |
| `upl:` | 320 | `internal/database/pebble_store_playlists.go` | `pebble_store_playlists.go:204` | namespaced | JSON (default; not verified per family) | user playlists |
| `upos:` | 321 | `internal/database/pebble_store_playback.go` | `pebble_store_playback.go:36` | namespaced | JSON (default; not verified per family) | user playback positions per segment |
| `candfb:` | 324 | `internal/database/candidate_feedback.go` | `candidate_feedback.go:37` | namespaced | JSON (default; not verified per family) | metadata-candidate thumbs-up/down labels (scoring training data) |
| `review_item:` | 325 | `internal/database/review_store.go` | `review_store.go:41` | namespaced | index entry (empty or id string; not verified per family) | review items and their indexes |
| `tag_idx:` | 326 | `internal/database/pebble_store.go` | `pebble_store.go:4554` | namespaced | index entry (empty or id string; not verified per family) | tag reverse index |

Status counts: namespaced: 243, bare key (no namespace): 2, LEGACY / retired: 10, legacy v1: 3.

## 4. Migration systems (there are four layers, not one)

### 4.1 Versioned schema migrations (`RunMigrations`)

| Aspect | Finding | Anchor |
|---|---|---|
| Location | `internal/database/migrations.go` (registry + bodies), `migration_bookkeeping.go` | `migrations.go:112`, `migration_bookkeeping.go:1` |
| Registration | one hand-ordered Go slice `var migrations = []Migration{...}`; each entry `{Version int, Description, Up, Down}`; no init-time self-registration | `migrations.go:112-509`, type at `:91` |
| Count | **66 migrations**, versions 1..66 contiguous | `migrations.go:504` (last entry, v66) |
| Down (rollback) | only 2 of 66 define `Down` (059, 065); all others nil | `migrations.go:465,501` |
| Version storage | user-preference row `db_version` (JSON `DatabaseVersion`) plus per-version `migration_<n>` preference records | `migration_bookkeeping.go:21-26`, `migrations.go:579-608` |
| Runner | `RunMigrations(store migrationStore)`: read version, select `Version > current`, apply in slice order; called right after store open | `migrations.go:512`, caller `database/store.go:1857` |
| Store dependency | narrowed 6-method `migrationStore`; special needs asserted at run time as capability interfaces (`hollowOpsSweeper`, `dedupIndexBackfiller`, `opsV3Migrator`, `junctionBookIDRepairer`, `legacyPlanStripper`) | `migrations.go:28-79` |
| Atomicity / resume | Per-migration, not per-row. Record + version bump committed as ONE Pebble batch (`setPreferencesAtomic`); if a record exists but the version did not advance (crash window) the runner advances the version without re-running Up. Up functions must be idempotent (enforced by `TestMigrationUpFunctionsAreIdempotent`). | `migrations.go:538-575,69-82`, `migration_bookkeeping.go:36-80` |
| Within-migration checkpointing | Not generic. Individual heavy migrations are resumable by being idempotent and batch-committed: migration 065 builds opv3 runs in 500-row Pebble batches with progress logging and skips rows whose `V2RowHash` is current; it can be re-run safely and is re-run every boot as `ReconcileOpsV3WithV2` | `pebble_store_ops_v3_migrate.go:25-31,150-217`, `store.go:1862-1870` |
| Start-timeout handling | each migration extends the systemd start budget | `migrations.go:559` (`extendStartTimeout`) |
| "Most are no-ops" | the file says the Pebble schema is created by store init so most migrations only log and return nil (stated as 60 of 61; there are now 66) | `migrations.go:20-24` |

### 4.2 Storage-format guard (release-gated format migration, not yet implemented)

- `SupportedStorageFormat = 1` (`storage_format.go:50`); a `storage_format` stamp lives in the DB as a preference row and is mirrored to the sidecar file `<db>.storage-format` (`storage_format.go:39-47,117-123`). Every open refuses a store newer than the build (`StorageFormatTooNewError`) or older/mid-migration (`StorageMigrationRequiredError`) (`pebble_store.go:670-680`).
- The converter that would raise the format does **not exist at HEAD**: `RunCutover` is referenced in comments (`storage_format.go:127-128`, `pebble_store.go:604`) but has no definition (grep for `func RunCutover` returns nothing) and the checkpoint-path constant `StorageMigrationCheckpointSuffix` "has no caller yet" (`storage_format.go:125-129`). Treat the guard as scaffolding for a planned "storage-efficiency program" (book_ver full copies, inline file signals).
- `make rollback` uses `main.go --print-storage-format` (`main.go:41`) to refuse a binary downgrade across a format change.

### 4.3 Other one-off converters and repairs

`pebble_store_booksig_migrate.go`, `pebble_store_legacy_seed.go`, `pebble_store_plan_legacy.go`, `legacy_migration.go` (activity-log tag enrichment, not a schema migration), `pebble_store_ops_v3_migrate.go` (opv2 -> opv3). Maintenance repairs run as ops after boot (`internal/plugins/maintenance`, `internal/repairs`), outside the migration list. Library-path to import-path conversion is done inline at store init (`pebble_store.go:1089-1101`, `library:` -> `import_path:`, `counter:library` -> `counter:import_path`).

### 4.4 Derived-index rebuilds

bleve rebuilds when its mapping version differs (delete + recreate, then dirty-set drain: `search/bleve_index.go:186-215`, `internal/server/server_lifecycle.go` "non-resumable bulk backfill" comment); memdb is rebuilt on every boot; the hnsw snapshot is discarded when its recorded Pebble truth-count disagrees (`hnsw_embedding_store.go:363-380`, `server.go:845-870`). None of these are listed in `migrations`.

## 5. Backup and restore

Package `internal/backup` (non-test: `backup.go` 1,295 LOC, `codec.go` 264, `device_unix.go`/`device_windows.go`).

| Aspect | Finding | Anchor |
|---|---|---|
| Scope | The Pebble DB directory only. The bleve index, the `hnsw/` snapshot, the OL store and the `.storage-format` sidecar are not archived (no reference in the package); they are derived or re-creatable, except the OL dump store and the sidecar. | `backup/backup.go:283-428`, `appdirs.go:55-66` |
| Consistent snapshot | `Checkpointable` interface (`Checkpoint(destDir)`), satisfied by `PebbleStore` via Pebble's native checkpoint (hard-links; staging dir must be on the same device) | `backup.go:28-33`, `pebble_store.go:953-955`, `backup.go:569-598` (`ensureSameDevice`) |
| Fallback | Live-file copy path (`CreateBackup`) when the store is not Checkpointable; has SQLite single-file branches | `backup.go:283`, `:1028-1103` |
| Format | tar + codec (gzip default, level best; codec chosen by name, sniffed by magic bytes on restore) + SHA-256 sidecar `<archive>.sha256` | `backup.go:148-156,643-700,777-800`, `codec.go` |
| Retention | `MaxBackups` default 10, `MaxTotalBytes` budget; retention runs BEFORE the write (and before the space check) | `backup.go:148-156,1183`, `:283-330,428-460` |
| Callers | HTTP `POST /backup`/restore in `internal/server/handlers/system/handler.go:769-776,862`; organizer auto-backup `internal/organizer/service.go:742-748` | as cited |
| Restore | `RestoreBackupIn(backupDir, filename, targetPath, verify)` extracts into `targetPath` (default: the DB's parent dir); verification fails closed. The handler does not close, swap or reopen the live `PebbleStore` (no such call in the handler; the word "restart" does not occur in `handler.go`), so safety of restoring over a running store relies on the operator. UNKNOWN whether the UI instructs a restart. | `handler.go:823-905`, `backup.go:732-775` |
| Interaction with storage format | The stamp is inside the checkpointed DB; the sidecar is outside the archive. Restoring an older archive therefore leaves a newer sidecar; the open guard compares both and refuses a store whose sidecar is newer than the build (`StorageFormatTooNewError` source "sidecar") and rewrites an unreadable sidecar from the stamp. Today the format is 1 everywhere, so the case is latent. | `pebble_store.go:670-680` |
| Interaction with migrations | Archives carry `db_version`/`migration_<n>`; restoring an older archive means `RunMigrations` re-applies pending migrations on the next boot, which is safe only because Up functions are idempotent. | `migrations.go:512-570` |
| Checkpoint staging cleanup | stale checkpoint staging dirs are swept | `backup.go:600-640` |

## 6. "Must be this way" comments and whether they hold

| Claim (location) | Verdict at HEAD |
|---|---|
| Pebble is "the only database backend"; SQLite flag removed (`cmd/root.go:508`, `store.go:1830-1843`) | HOLDS (no SQLite code path opens). Stale remnants listed in section 1. |
| "Never pass pebble.FormatNewest in new code" (`storage_format.go:67-73`) | PARTLY VIOLATED today by two existing callers the comment itself names as pending (TASK-A7): `ai_scan_store.go:157` and `openlibrary/store.go:43`. The main store honours it (`PebbleFormatMajorVersion`). |
| chromem "is a derived in-memory index, not a second source of truth"; persistence removed (`chromem_embedding_store.go:17-39`) | HOLDS (`chromem.NewDB()` at `:67`). The same comment says the "SQLite/Pebble EmbeddingStore stays the source of truth" - SQLite half is stale. |
| memdb is derived and Pebble is the source of truth; reads fall back until warm-up publishes (`memdb_store.go:15-24`, `pebble_store.go:795-799`) | HOLDS structurally (`memPtr` only set after a successful warm-up, `pebble_store.go:837-840`). |
| Migration Up functions MUST be idempotent (`migrations.go:74-87`) | HOLDS as a stated contract with a named enforcing test; I did not run it. |
| "Of the 61 migration functions, 60 touch the store not at all" (`migrations.go:20`) | STALE count (66 registered); the substance (most are no-ops) not re-verified function by function. |
| Reserved prefs `storage_format`/`storage_migration` only changeable by the stamp writer, cut-over and `diagnostics reserved-prefs` (`storage_format.go:89-104`) | HOLDS at the `SetUserPreference`/`DeleteUserPreference` boundary by code (`ErrReservedPreferenceKey`); the cut-over writer does not exist yet. |
| "The six composites ... deliberately UNEXPORTED" (`store.go:18-45`) | HOLDS. |
| Search index open failure must not abort startup (`search/register.go:38-47`) | HOLDS in code (`register.go:56-60` returns nil on open error). |
| `ChapterReader` is on Store so a decorator that does not forward it fails the BUILD (`store.go:80-93`) | HOLDS as a rule; the decorator it protects against is gone. |
| NutsDB "retired, no longer opened" (`internal/activity/register.go:9-14`) | HOLDS (no dependency, only rejected backend names in tests). |
| Config comment: root_dir is deliberately not environment-authoritative (`internal/config/config.go:2569-2578`) | Out of scope for storage; mentioned because it names a deployment-specific mount that I did not quote. |

