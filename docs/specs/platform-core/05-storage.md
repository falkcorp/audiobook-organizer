<!-- file: docs/specs/platform-core/05-storage.md -->
<!-- version: 1.4.1 -->
<!-- guid: 940ce8be-6f86-4450-b43a-77bf78827b12 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Storage (`storage`, `storage/index`, `storage/datamigrate`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) (engines, 258 key families,
migrations, backup), [`raw/04-database-store.md`](../../plans/platform-core/raw/04-database-store.md)
(`database.Store`), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md),
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md). Names, layers and rules
(R1–R8) are from [03-architecture](03-architecture.md): `storage` and `storage/index` are **L1**;
`storage/keys` (pure tuple-key functions) and `storage/codec` (generic value codecs) are **L0**
leaves that import nothing from core (03 §3.3, MOD3-9; the typed keyspace helper that names both a
codec and a `Keyspace` is therefore `storage.Typed`, not a `codec` type, §5S.3.3); `storage/datamigrate` and its runner `core/internal/storagemigrate` are **L4** (they import
`tasks`; R4 bridge rule). Task-system names (`tasks.Definition`, `tasks.Parallelism`, checkpoints,
preview) are normative in [05-tasks](05-tasks.md). This file owns storage **mechanisms**: claims, the
explicit, logged **storage grants** through which a core subsystem reaches a family another owner
holds (R3-1), the one ownership table of ABO's families (§5S.9.1), migrations, the one store guard
and its sidecars, checkpoints, and the contract of the deferred per-family **cutover** (§5S.3.13).
Boot **order** is [06](06-app-sdk.md) §6.5.1's one phase table (R2-8); §5S.4 describes storage's own
steps and names the phase each runs in. Stage sequencing, the stage in which each grant lands and
the release that would run a cutover are **policy** owned by [09](09-abo-on-core.md), which
references these mechanisms.

**One stored format per family during the extraction (R3-1, 02 decision 13).** From S0 to S10 every
ABO family keeps exactly the stored format HEAD writes. A core service that needs ABO's existing data
runs a **legacy-format adapter** over the existing keys, through a grant the owning module declares
(§5S.3.11); native-only data is additive in the legacy row where the legacy format tolerates it, or
lives in a **core-only family** (`c/<subsystem>/…`) that no older build reads. There is no adoption
copy, no mirror, no side record, no rebuild at boot, no claim hand-over, no claim-mode level step and
no floor build, so a rollback is a binary swap to the previous release; its stated costs (one
re-login, re-entering secrets moved to the encrypted store) are 05-authn's and 05-config's. The
per-owner level guard (§5S.3.7) stays for real format changes: books' own `Breaking` migrations and,
outside the extraction, each cutover.

## 5S.0 Decisions in one table

Decision IDs carry a `D-S` prefix so they never read as 09's stages S0–S10 (COR2-19).

| # | Decision | Section |
|---|---|---|
| D-S1 | **One system-of-record Pebble DB per install**, opened only by the kernel at the absolute path `core.storage.path` (never under the state directory by construction). Holders get a `storage.Keyspace` (`m/<module>/`, `c/<subsystem>/`) and, for data written before core existed, a `LegacyRange` over their claims — or, for a core subsystem named in a grant, over exactly the granted ranges. Holding the handle *is* the permission — for reads as well as writes. | §5S.3.1, §5S.3.4, §5S.3.11, §5S.4 |
| D-S2 | Access outside the handle's keyspace, claims or grants is **refused** (`ErrOutsideKeyspace`), never logged-and-allowed — except a one-release shadow mode for the books module's legacy ranges only. | §5S.3.4, §5S.9.4 |
| D-S3 | ABO's 258 key families are **books' legacy claims for the whole extraction**, in one normative table (§5S.9.1) that is the same at every stage: no claim changes owner or mode on a store (R3-1). Claims are prefix or exact key; same-owner nesting is legal and the longest match selects the claim; cross-owner nesting needs an explicit `Except`; any other overlap fails boot. | §5S.3.4, §5S.9.1 |
| D-S4 | **Storage grants** (R3-1): the owner of a legacy family declares in its `storage.Spec` which core subsystem may read (`GrantRead`), read and delete (`GrantReadDelete`) or read and write (`GrantReadWrite`) which of its ranges, optionally `Exclusive` (the grantee becomes the range's only runtime writer). Storage enforces the grant on the grantee's handle, logs every grant at boot, records it in the claim ledger and counts every operation through it. A grant never changes ownership, and a module is never a grantee (R3). | §5S.3.11 |
| D-S5 | **One stored format per family.** A grantee writes correct legacy rows (`codec.LegacyJSON`) that every build able to open the store reads, and never a row that gives an older build more than the native value (an API key it writes always has explicit, non-empty scopes, `internal/server/middleware/auth.go:405-408`). Native-only data is additive in the legacy row where tolerated, or lives in a core-only family (hashed sessions and native one-time tokens: a rollback costs one re-login). | §5S.3.11, §5S.9.2 |
| D-S6 | Nothing is copied. Ops runs, queues and pages, the `Requires` stamps, AI job rows, provider holds, `syslog:` and the search dirty set are read and written (or read and retention-deleted) **in place** through grants; `act:` and the live v1 ops families keep their books writers. | §5S.9.1–§5S.9.2 |
| D-S7 | Versioned migrations are **per keyspace, forward-only, one atomic batch per step** — except books' legacy steps 1..66, which keep their idempotent, non-atomic form and run **first**, under the logged legacy-migration grant (R2-4). All pending steps of all owners run in **one DAG**. **One checkpoint per upgrade attempt**, taken before anything is written, reused only by later boots of the same uncompleted attempt on the same store (install marker + store generation; DATA3-1), beside the DB, restorable offline. Code a pending step needs may be deleted only behind a declared **upgrade floor** (`ErrUpgradeFloor`), which the store's **applied-steps sidecar** `<db>.storage-applied` lets the updater and `make rollback` check before a binary is applied (02 N14). | §5S.3.5, §5S.3.7, §5S.3.12, §5S.4 |
| D-S8 | Anything too large for one batch is a **data migration** (`storage/datamigrate`): a `tasks.Parallelism`, a persisted shard plan (exact resume), a compatibility class, preconditions against concurrent writers, and a dry run that is the real runner on a read-only copy of the store. A module's `Online` migration runs as a **module** task kind with its declared `Writes` as effects (COR3-5). | §5S.3.6 |
| D-S9 | **One store guard** (SIMP-18): a per-owner minimum-reader **level** — stamp, fsynced sidecar, fast-exit flag, one reader-check function used by `make rollback` and the updater — raised (always as `max`) only by level steps: a physical re-encoding of an owner's data (books' storage-efficiency release B) or a cutover (§5S.3.13). During the extraction nothing raises a core level, so the guard admits every earlier release. ABO's storage-format names are kept as a projection for builds older than the guard. A level step never runs on the first boot of an auto-applied release (`RequiresSelfServed`, OPS2-2). | §5S.3.5, §5S.3.7 |
| D-S10 | Concurrency: **preconditions evaluated at commit** under commit-time stripe locks for `Serialized` ranges (legacy claims and core or module keyspaces alike); **multi-owner atomic batches** by participant binding (each owner binds only the handles it holds; participant APIs take `*storage.Batch`). Storage never holds a lock, lease or reservation for an uncommitted batch, so a leaked batch stalls nothing. | §5S.3.2, §5S.6 |
| D-S11 | Full-text search is a core contract (`storage/index`, which imports `storage`, never the reverse) with a durable dirty set in the entity's batch. Derived indexes record a **storage generation** and are accepted only when it matches and every boot since maintained it; their hooks and rebuild task are registrar data, and the loader runs at Run start step (3a), never inside `app.New` (COR3-7). hnsw, chromem-go and go-memdb stay **app-level** (books). | §5S.3.8, §5S.3.9 |
| D-S12 | Auxiliary, re-creatable Pebble DBs (the Open Library dump mirror) only through `OpenAuxiliary`, with the same format pin; never system of record. The dead standalone AI-scan DB is deleted. | §5S.3.10 |
| D-S13 | **Cutover is deferred** and outside the extraction: moving a family to a native core keyspace and deleting its legacy rows is a later, per-family, one-way, checkpointed `Breaking` data migration behind the per-owner level guard, decided on measured data; its floor is the cutover release itself (02 N15). | §5S.3.13 |

## 5S.1 Purpose & scope

`storage` is the only persistence API in core: an ordered byte-key/byte-value store over Pebble with
namespaced keyspaces, batches with preconditions, snapshots, iterators, sequences, per-keyspace
migrations, legacy claims, storage grants, the store guard and a census. `storage/index` is the
full-text index contract. `storage/datamigrate` declares large resumable rewrites.

In scope: the main DB lifecycle; keyspace issue and enforcement (R8); the legacy-claim registry (the
successor of `internal/database/keyfamilies.go`, which stays ABO code) and the **one ownership table**
of ABO's families that 04, 05-tasks, 05-logging, 05-authn, 05-config and 09 reference instead of
restating (DATA-2); storage grants; migrations (versioned, books legacy, data); the store guard and
its sidecars; checkpoints; the cutover contract; derived-index lifecycle; full-text index contract;
auxiliary DBs; key and value codecs.

Out of scope: entity schemas (modules own them; core owns no media table — 02 decision 6); backup
and restore ([05-backup](05-backup.md), which consumes `storage.Admin.Checkpoint` and performs every
directory swap, including checkpoint restores); filesystem roots ([05-library](05-library.md)); the
legacy-format adapters and the row shapes they read and write ([05-authn](05-authn.md),
[05-rbac](05-rbac.md), [05-config](05-config.md), [05-tasks](05-tasks.md), [05-logging](05-logging.md),
[05-library](05-library.md), [05-ai](05-ai.md), [05-metadata](05-metadata.md)); task execution
([05-tasks](05-tasks.md)); stage order and window lengths ([09](09-abo-on-core.md)).

## 5S.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| Pebble v2.1.7 is the only system-of-record engine; SQLite removed (`InitializeStore` rejects it), NutsDB retired; bbolt indirect only. | `go.mod:7`; `internal/database/store.go:1840-1851`; `raw/05-storage.md` §1 |
| One main DB, directory `audiobooks.pebble`, plus sidecar file `<db>.storage-format`; siblings `library.bleve`, `hnsw/`, `certs/`, bootstrap-token file, `backups/`. | `cmd/root.go:488`; `internal/database/storage_format.go:111-123`; `internal/appdirs/appdirs.go:52-62`; `internal/backup/backup.go:134-146` |
| Every "store" (activity, metrics, embeddings, AI scan, catalog, master list) is a key-prefix namespace over the one DB; one WAL, one backup unit, one migration clock. | `internal/database/store_from_store.go:12-63`; `internal/server/registry_wire.go:270-310` |
| All engine access goes through `GatedDB`, a lease gate so `Close` drains in-flight calls; borrowers get it from `PebbleStore.DB()`. | `internal/database/pebble_gate.go:327-340,363`; `internal/database/pebble_store.go:957-962` |
| Key-family registry: 258 families, feeds `GET /api/v1/diagnostics/db-census`; an unregistered prefix shows as "(unregistered)" and is **not** refused; uniqueness and partition coverage are tested, writer registration is not. | `internal/database/keyfamilies.go:14-36,37-345`; `internal/server/wire_media_routes.go:68`; `raw/05-storage.md` §3 |
| **89 nested pairs** among the 258: a family whose prefix starts with another family's prefix is that family's child (`book:` ⊃ `book:asin:`, `act:` ⊃ 12 children, `opv2:` ⊃ 13, `opv3:` ⊃ 19, `pref:_system:` ⊃ 6), all written by the same owner; some children have a different lifecycle than their parent (`book:hash:`, `book:organizedhash:`, `book:originalhash:` retired; `book:author:`, `book:series:` dead and swept by retention). | `keyfamilies.go:29-36` (nesting rule), `:37-345` (counted at this commit) |
| 256 families are `name:`-prefixed; 2 are bare keys: `narrator_counter` and `sync_alias_use_seed_cutoff`. Both are deliberate (the first keeps legacy narrator numbering, the second must not fall inside the `sync_alias_use:` scans). | `keyfamilies.go:84,316`; `pebble_store_authors.go:1220-1229`; `pebble_store_sync_alias_use.go:209-213` |
| `idx:` is one family shared by auth, users, sessions, API keys, OAuth, invites, playback, playlists, book versions, collections, ABS sessions and the search dirty set. The identity sub-prefixes in code are exactly `idx:user:` (username, email), `idx:role:name:`, `idx:sess:user:`, `idx:apikey:` (hash, user), `idx:invite:username:`, `idx:oauth:` (user, sub). | `keyfamilies.go:64`; `pebble_store_auth.go:67,142,173,178,1126,1131,1670`; `pebble_store_search_dirty.go:39` |
| `setting:` is a general runtime-marker store, not the config store: 56 non-test `SetSetting` call sites write scheduler clocks, the pause gate, index-built and backfill-done flags, the bootstrap token hash and one `transcode_skip_<hash>` row per file; `preference:` holds per-book state (`metadata_state_<bookID>`); `pref:` holds per-user books data moved by the user merge. | `internal/scheduler/interval_clock.go:50`; `internal/operations/registry/pause_gate.go:52`; `internal/server/bootstrap.go:38-39`; `internal/remux/transcode.go:113-116`; `internal/metastate/metastate.go:34`; `internal/database/user_merge.go:241,425` |
| `counter:` holds ASCII-decimal ID counters for unrelated entities; `nextID` commits each increment with its own `pebble.Sync` write, outside the caller's batch. | `pebble_store.go:96-103,966-990` |
| Values are `encoding/json` (v1) by default; binary exceptions: ASCII counters, float16+zstd vectors in a JSON envelope, 8-byte LE `dedup:seq`, big-endian fingerprint windows and clock values. No gob or protobuf in `internal/database`. | `raw/05-storage.md` §3 "Value encoding" |
| Versioned migrations: one hand-ordered slice of 66 (`Version`, `Up`, optional `Down`; 2 have `Down`); `type MigrationFunc func(store migrationStore) error`; version in `preference:db_version`, records `{version, description, applied_at}` in `preference:migration_<n>`; record + version bump are one batch, **but `Up`'s own writes are not in that batch** (they go through store methods that commit their own batches and allocate IDs through `nextID`); several are library-scale repairs (063–066); `Up` must be idempotent (enforced by a test). A version whose record exists is never re-run (replay guard DB-02). | `internal/database/migrations.go:69-112,87,96-100,487-506,512-575,536-548`; `migration_bookkeeping.go:14-80,128-138` |
| An unreadable `db_version` is read as version 0 (fail-open); only versions without a `migration_<n>` record would then re-run. | `migrations.go:579-585,542` |
| Migrations run after the store's init phase has already written (counters, `library:`→`import_path:` conversion, stamp) and after the Pebble format ratchet, and before `ReconcileOpsV3WithV2`, which runs every boot and only logs on failure. No checkpoint is taken before migrations. | `store.go:1846-1872`; `pebble_store.go:689-699,1089-1105` |
| Storage-format guard: `SupportedStorageFormat = 1`; stamp at `preference:storage_format`, marker `preference:storage_migration`, sidecar `<db>.storage-format`; open refuses too-new (stamp or sidecar) and, in serve mode, older or marked stores; Pebble format pinned to `FormatValueSeparation` and ratcheted in the init phase. | `storage_format.go:44-80,82-87,111-123`; `pebble_store.go:620-687,689-699` |
| `RunCutover` is referenced but **does not exist**; `openForCutover` and `StorageMigrationCheckpointSuffix` have no caller. The design is in the storage-efficiency plan (task B6a) and was never built. | `storage_format.go:125-129`; `pebble_store.go:597-619`; `docs/plans/2026-10-03-storage-efficiency-plan.md:168-170` |
| `make rollback` reads the sidecar and the `.prev` binary's `--print-storage-format` and refuses a swap when the sidecar is above `.prev`'s format; the sidecar is read with `sudo -n cat` over ssh. | `Makefile:871-903`; `scripts/storage_format_guard.py:17-22` |
| The shipped unit restarts on failure every 5 s, burst 5 per 60 s. | `deploy/audiobook-organizer.service:81-82,111-112` |
| Derived engines: go-memdb **live**; bleve scorch `library.bleve` **live but degradable**; coder/hnsw **live by default**, snapshot in `hnsw/` validated against a Pebble truth *count*; chromem-go **optional**, in-memory only. | `pebble_store.go:792-845`; `internal/search/register.go:42-60`; `internal/search/bleve_index.go:186-262`; `internal/server/registry_wire.go:65-80,136-159`; `internal/server/server.go:838-870`; `internal/database/hnsw_embedding_store.go:346-380` |
| Second Pebble DBs: the Open Library dump mirror (`ol:` keys, opened with `pebble.FormatNewest`) and a standalone AI-scan DB constructor with no non-test caller. | `internal/openlibrary/store.go:41-43,97-104`; `internal/database/ai_scan_store.go:156-181` |
| Owner locks for identity writes are ad-hoc mutexes inside the store (`inviteMu`, `apiKeyMu`, `sessionWriteMu`); the admin user merge commits one batch across books and auth families after a compare-and-set under them. | `pebble_store_auth.go:136,760,1285,1697`; `user_merge.go:241-242,421-486,507-531,719-722` |
| A package-level `globalStore` is still set by `InitializeStore`. | `store.go:1807-1825,1854` |

Coupling that the extraction must cut (`raw/02-import-graph-hotspots.md` findings 1–2):
`internal/database` imports 8 audiobook packages and *is* the book model (`store.go:202`), so
nothing that imports it can be core. Core storage starts from zero code shared with
`internal/database`, and books keeps `internal/database` as its repository layer over core handles
(§5S.9.3).

## 5S.3 Go API

Package `github.com/falkcorp/mediacore/storage` (L1, R4). The engine lives in
`core/internal/pebble`; the only constructor of a `*DB` is `kernel`. Apps never open a DB (R2).
`storage` imports neither `storage/index` nor `tasks` (COR-2, SIMP-17).

### 5S.3.1 DB, Keyspace, keys and values

```go
package storage

import (
	"context"
	"errors"
	"iter"
	"time"
)

// Owner identifies who holds a keyspace or a claim: a module ID ("books"), or
// "core/<subsystem>" ("core/tasks"). Levels (§5S.3.7) use "core" for all of core.
type Owner string

// DB is the install's single system-of-record store. Only the kernel holds a
// *DB; everyone else holds Keyspaces and LegacyRanges cut from it.
type DB struct{ /* engine, claim registry, gate, stripe table, metrics; unexported */ }

// Handle is implemented by Keyspace and LegacyRange only (sealed).
type Handle interface{ handle() }

// Reader is the read surface shared by Keyspace, LegacyRange and Snapshot.
type Reader interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
	NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
}

// Keyspace is a capability: every key passed to its methods is relative and
// is stored as Prefix()+key, so a holder cannot address anything else.
// The zero Keyspace is invalid; every method on it returns ErrInvalidKeyspace.
type Keyspace struct{ /* unexported */ }

func (k Keyspace) Prefix() string      // "m/tv/" or "c/tasks/"
func (k Keyspace) Owner() Owner
func (k Keyspace) Sub(name string) Keyspace // "m/tv/" + "series" -> "m/tv/series/"; name checked by keys.ValidName

// Reads. Get returns a copy owned by the caller; ErrNotFound when absent.
func (k Keyspace) Get(ctx context.Context, key []byte) ([]byte, error)
func (k Keyspace) Has(ctx context.Context, key []byte) (bool, error)

// Single-key writes are a one-entry batch committed with Sync.
func (k Keyspace) Set(ctx context.Context, key, value []byte) error
func (k Keyspace) Delete(ctx context.Context, key []byte) error
// DeleteRange deletes [start, end) inside the keyspace; nil end = to the end of the keyspace.
func (k Keyspace) DeleteRange(ctx context.Context, start, end []byte) error

// Scan yields keys in [opts.Start, opts.End) (relative), forward or reverse.
// Key and Value are valid only until the next iteration step; use KV.Clone to
// retain. A non-nil error is yielded at most once, as the last pair.
func (k Keyspace) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (k Keyspace) ScanPrefix(ctx context.Context, prefix []byte) iter.Seq2[KV, error]

// NewIterator is the cursor form for callers that seek, step backwards or
// interleave scans (the books repository layer needs SeekGE/Prev/Last).
func (k Keyspace) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)

func (k Keyspace) NewBatch() *Batch            // write-only
func (k Keyspace) NewIndexedBatch() *Batch     // reads see the batch's own writes
func (k Keyspace) Snapshot() (*Snapshot, error) // point-in-time, read-only, scoped to k

// Sequence returns a named monotonic uint64 allocator stored in the keyspace (§5S.3.2).
func (k Keyspace) Sequence(name string) *Sequence

// EstimateBytes is Pebble's on-disk estimate for the keyspace (cheap).
func (k Keyspace) EstimateBytes(ctx context.Context) (uint64, error)

type ScanOptions struct {
	Start, End []byte // relative; nil = keyspace bound
	Reverse    bool
	KeysOnly   bool  // skips value fetch for value-separated blobs
	Limit      int   // 0 = unlimited
}

type Range struct{ Start, End []byte } // absolute keys; End exclusive

type KV struct{ Key, Value []byte } // Key is relative to the handle
func (kv KV) Clone() KV

type Iterator struct{ /* unexported */ }
func (it *Iterator) First() bool
func (it *Iterator) Last() bool
func (it *Iterator) SeekGE(key []byte) bool
func (it *Iterator) SeekLT(key []byte) bool
func (it *Iterator) Next() bool
func (it *Iterator) Prev() bool
func (it *Iterator) Valid() bool
func (it *Iterator) Key() []byte   // relative
func (it *Iterator) Value() []byte
func (it *Iterator) Err() error
func (it *Iterator) Close() error

type Durability uint8
const (
	DurabilityInvalid Durability = iota // zero value is rejected
	Sync                                  // fsync the WAL before Commit returns (default for every keyspace)
	NoSync                                // caches, census results, metrics snapshots: may be lost on crash
)

var (
	ErrNotFound        = errors.New("storage: not found")
	ErrOutsideKeyspace = errors.New("storage: key outside the handle's keyspace or claims")
	ErrInvalidKeyspace = errors.New("storage: zero or closed keyspace")
	ErrClosed          = errors.New("storage: db closed")
	ErrBatchTooLarge   = errors.New("storage: batch exceeds the configured limit")
	ErrClaimMode       = errors.New("storage: the claim's or grant's mode forbids this operation")
	ErrNotSerialized   = errors.New("storage: preconditions are allowed only on Serialized ranges")
	ErrConflict        = errors.New("storage: a precondition failed at commit; nothing was applied")
)
```

### 5S.3.2 Batches, preconditions, multi-owner commits, sequences

```go
// Batch is atomic across every handle bound to it: one Pebble batch.
// A batch created from handle H can write H. Bind adds another handle; the
// caller must HOLD that handle — the handle value is the grant, so a
// participant can only touch what it bound. This is how a module writes its
// row and an outbox entry (c/outbox/) or an index-dirty mark in one commit,
// and how a core subsystem joins a module's batch (participant pattern below).
// Batch embeds a noCopy marker: it is only ever passed as *Batch (COR2-12).
type Batch struct{ /* noCopy; unexported */ }

func (b *Batch) Bind(h Handle) *Batch
func (b *Batch) In(h Handle) BatchWriter // panics if h is not bound
func (b *Batch) Set(key, value []byte) error // shorthand for b.In(origin).Set
func (b *Batch) Delete(key []byte) error
func (b *Batch) DeleteRange(start, end []byte) error
func (b *Batch) Get(ctx context.Context, key []byte) ([]byte, error) // indexed batches only

// Expect adds a precondition evaluated at Commit: the key's committed value
// must equal value byte for byte (nil = the key must be absent). Allowed only
// for keys in Serialized ranges (§5S.3.4) of a bound handle; else ErrNotSerialized.
func (b *Batch) Expect(h Handle, key, value []byte) error
// GetForUpdate reads key through h (the batch's own writes first) and records
// Expect(h, key, <value read>): optimistic read-modify-write.
func (b *Batch) GetForUpdate(ctx context.Context, h Handle, key []byte) ([]byte, error)

func (b *Batch) Participants() []Owner // owners of the bound handles, for logs and metrics
func (b *Batch) Len() int              // encoded bytes, for callers that chunk
func (b *Batch) Count() int            // entries
// Commit applies the batch atomically or not at all. ErrConflict: a
// precondition failed; ErrOutsideKeyspace / ErrClaimMode: a write resolves
// outside the bound handles or against the claim's mode.
func (b *Batch) Commit(ctx context.Context, d Durability) error
func (b *Batch) Close() error // discards an uncommitted batch; idempotent

type BatchWriter interface {
	Set(key, value []byte) error
	Delete(key []byte) error
	DeleteRange(start, end []byte) error
}

// RetryOnConflict re-runs fn (which builds and commits a fresh batch) while it
// returns ErrConflict, up to attempts, with jittered backoff; ctx-aware.
func RetryOnConflict(ctx context.Context, attempts int, fn func(ctx context.Context) error) error

// Snapshot pins a Pebble version. Snapshots are for bounded reads (a page, a
// verification pass); long passes must refresh (§5S.6).
type Snapshot struct{ /* unexported */ }
func (s *Snapshot) Get(ctx context.Context, key []byte) ([]byte, error)
func (s *Snapshot) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (s *Snapshot) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
func (s *Snapshot) Age() time.Duration
func (s *Snapshot) Close() error

// Sequence hands out unique, increasing uint64 IDs from leases of
// core.storage.sequence.lease IDs (default 64). A refill is its OWN Sync
// commit of hw = max(hw, start+lease), made under the sequence's mutex BEFORE
// any ID of the new lease is returned. A crash can therefore only skip IDs,
// never reissue one; the cost is one fsync per lease (ABO's nextID pays one
// per ID, pebble_store.go:966-990). IDs increase in allocation order, not in
// commit order.
type Sequence struct{ /* unexported */ }
func (s *Sequence) Next(ctx context.Context) (uint64, error)
// NextIn returns an ID from a durable lease for use in b's keys or values.
// It writes nothing into b; if a refill is needed it commits on its own first.
func (s *Sequence) NextIn(ctx context.Context, b *Batch) (uint64, error)
// AdvancePast makes every later ID greater than n: under the sequence's mutex
// it commits hw = max(hw, n+1) on its own (Sync) and drops the in-memory lease.
// Used after a restore, before records carrying IDs from another copy of the
// store are written back (05-rbac audit copy-forward; DATA2-16, SEC2-12). Like
// a refill, a crash after it can only skip IDs.
func (s *Sequence) AdvancePast(ctx context.Context, n uint64) error
```

**Commit procedure.**

1. Every write and every precondition must resolve, by longest match, to a bound handle's keyspace or
   claims, and the claim's mode must allow the operation (§5S.3.4); otherwise the commit fails and
   nothing is applied.
2. If the batch writes or expects any key in a `Serialized` range, Commit acquires the **commit
   stripes** of those keys (a fixed table of 4 096 mutexes indexed by a hash of the absolute key) in
   ascending stripe order, evaluates every `Expect` against committed state, applies the batch with
   its durability, and releases the stripes. A failed precondition releases and returns `ErrConflict`.
   Ascending acquisition by every committer makes deadlock impossible.
3. Stripes exist only inside `Commit`. Storage holds **no lock, lease or reservation on behalf of an
   open batch**: an uncommitted batch that is never closed costs only its own memory and blocks no
   other writer, reader, relay or migration.

Because every writer of a `Serialized` key commits under its stripe, a precondition cannot be
overtaken between its evaluation and the apply. Writers that read-modify-write such a key use
`GetForUpdate` + `RetryOnConflict`; they may keep an in-process mutex for their own fairness, but
correctness no longer depends on one (DATA-13). The same API serves core keyspaces (for example a
`c/tasks/` run record that two executor paths may update) and module keyspaces: the owner lists the
sub-prefixes in `Spec.Serialized`.

**Multi-owner atomic commits (DATA-18).** A commit spanning owners is built by **participant
binding**: the initiating owner creates the batch and writes its rows; each other owner's
subsystem exposes a staging function that binds *its own* handles to that batch, writes its rows and
adds its preconditions; the initiator commits. Nobody receives another owner's handle. The admin
user merge is the motivating case: books builds the batch for `ubs:`, `upos:`, `bookmark:`,
`sync_alias_use:`, `pref:`, `playp:`, `playe:` and `usermerge:journal:`, and calls the auth
participant (`auth.Admin.StageUserMerge(ctx, b, plan)`, specified in [05-authn](05-authn.md)), which
binds `c/auth/` and its grant over `u:`, `apikey:`, `idx:apikey:user:`, `oauthid:`, `idx:oauth:user:`,
`invite:`, `inviteallow:` (§5S.3.11), writes or deletes those rows in their legacy format, as its
grant's mode allows, and adds the
compare-and-set checks that `ApplyUserMergeOps` performs today under `userMergeApplyMu`/`inviteMu`/
`apiKeyMu` (`user_merge.go:507-531`) as `Expect` preconditions. Books commits once; all of it lands or
none does. The same shape serves `outbox.Stage`, `index.MarkDirty` and `library.Manager.TransferIn`.

**Participant APIs take `*storage.Batch` (COR2-12).** `Bind` records the grant on the batch the
initiator commits; a participant that received a copy would bind and write into the copy, and the
initiator's `Commit` would then refuse those rows (`ErrOutsideKeyspace`) or apply them unchecked.
Every core API that stages into a caller's batch therefore takes `b *storage.Batch`; `Batch`'s
`noCopy` marker makes `go vet` (copylocks) flag a value copy, and `archtest.NoBatchByValue` rejects a
`storage.Batch` value parameter or field anywhere in core and in the spec's extracted Go blocks
(`TestSpecGoBlocksCompile`).

**No resolution hook.** `Batch` has no `OnResolve`. Its one requested consumer, the outbox gap-safe
horizon, is replaced in [05-events](05-events.md) by commit-ordered intake (the relay observes
committed intake rows and assigns sequence numbers itself, COR-10). A hook could not have made a
horizon safe anyway: a leaked batch is never closed, so its hook never fires. Storage states the
general rule instead: **key order says nothing about commit order**; a consumer that needs commit
order observes commits (scan, then delete what it has handled), it does not infer them from keys or
IDs reserved earlier.

### 5S.3.3 Key and value codecs (helpers + guidance)

`storage/keys` (pure functions) and `storage/codec` (generic value codecs) are L0 leaves that import
nothing from core (03 R4, MOD3-9); `storage.Typed` binds a codec to a keyspace.

```go
package keys

// Seg is one key segment. Join encodes each segment with 0x00 -> 0x00 0xFF and
// ends it with the terminator 0x00 0x01 (FoundationDB tuple-layer style), so
// for any two segment lists the byte order of the joined keys equals the
// lexicographic order of the lists, and every key that extends a list lies in
// [Prefix(list), PrefixEnd(Prefix(list))). Any byte may appear in a segment.
type Seg []byte

func Str(s string) Seg
func Bytes(b []byte) Seg
func U64(n uint64) Seg       // 8-byte big-endian (sorts numerically)
func Time(t time.Time) Seg   // 8-byte big-endian UnixNano, sign bit flipped

func Join(segs ...Seg) []byte
func Prefix(segs ...Seg) []byte         // same bytes as Join; named for intent
func Split(key []byte) ([]Seg, error)   // inverse of Join (census and diagnostics rendering)
func PrefixEnd(prefix []byte) []byte    // smallest key greater than every key with prefix; nil if none

// ValidName checks keyspace and Sub names: [a-z0-9_]{1,32}. '/' (0x2F) sorts
// below every allowed byte, so "m/tv/series/" keys stay contiguous and ordered.
func ValidName(s string) error
```

Why the tuple encoding (DATA-26): a `/`-joined key with escaping of only 0x00 and `/` is not
order-preserving — `"a b"` sorts before `"a/b"` (0x20 < 0x2F) but after it once `/` is escaped to
0x00 0x02, and a segment `"a b"` sorts before its sibling `"a"` because 0x20 < 0x2F. With the
terminator 0x00 0x01 and the escape 0x00 0xFF, a segment that is a prefix of another sorts first
(0x01 < 0xFF and 0x01 < every non-zero content byte), and an embedded zero sorts below every other
content byte, as it should. `TestJoinOrderMatchesSegmentOrder` is a property test over random segment
lists.

**Notation for core keys.** In this spec set a core key written `c/storage/served/<owner>/<level>`
means the keyspace prefix bytes `c/storage/` followed by `keys.Join(keys.Str("served"),
keys.Str(<owner>), keys.U64(<level>))`: after the keyspace prefix, `/` marks a segment boundary, not a
byte. Owner IDs, including `core/auth`, are always one segment. A build that is not yet on core
storage (09 S0) writes the `c/storage/` records it needs with the same `storage/keys` package, so a
later core build reads them unchanged (DATA2-6).

```go
package codec

// Codec[T] encodes one value type. Native codecs write a 1-byte format tag
// first so a value can change encoding without a key-format change.
type Codec[T any] interface {
	Encode(v T) ([]byte, error)
	Decode(b []byte) (T, error)
}

func JSON[T any](version uint8) Codec[T] // native: tag 'j', then {"v":version,...}; Decode rejects unknown versions with ErrUnknownVersion
func Raw() Codec[[]byte]                 // native: tag 'r'
func Zstd[T any](inner Codec[T], minSize int) Codec[T] // native: tag 'z' when compressed (klauspost/compress)

// LegacyJSON is the codec for every legacy row a legacy-format adapter
// (§5S.3.11) or a read adapter writes or decodes. It is encoding/json (v1)
// with no tag byte and no envelope, byte-compatible with what the previous ABO build decodes —
// permanently, whatever native codecs use (DATA-5). encoding/json/v2 defaults
// (case-sensitive names, omitzero, nil-slice encoding) would silently change
// what the old build reads.
func LegacyJSON[T any]() Codec[T]

var ErrUnknownVersion = errors.New("codec: unknown value version")
```

```go
package storage

// Typed binds a codec to a keyspace for the common Get/Set/Scan shapes. It
// lives in storage, not in codec, so codec stays an L0 leaf (MOD3-9).
type Typed[T any] struct{ KS Keyspace; C codec.Codec[T] }
func (t Typed[T]) Get(ctx context.Context, key []byte) (T, error)
func (t Typed[T]) Put(b *Batch, key []byte, v T) error
func (t Typed[T]) All(ctx context.Context, opts ScanOptions) iter.Seq2[T, error]
```

Guidance (normative for core keyspaces, recommended for module keyspaces):

| Rule | Why |
|---|---|
| Inside a keyspace, keys are built with `keys.Join`; IDs are ULIDs (as `keys.Str`) or `keys.U64`; times are `keys.Time`. Never decimal numbers without padding. Keyspace and `Sub` names are `keys.ValidName`. | Order-preserving range scans; ABO mixes `:` separators, zero-padded and unpadded nanos (`raw/05-storage.md` §3), which the census has to special-case. |
| Secondary index keys: `Join(<index>, <term>, <primary-id>)` with an empty value, written in the **same batch** as the primary row. | ABO's single-writer + ratchet discipline (`raw/08-prior-art.md` §7 `opv2-waiting-deps-index`) generalised. |
| Values: `codec.JSON[T]` with an explicit version; binary only for hot or large values (vectors, bitsets), always behind a tag byte. No gob. Legacy rows: `codec.LegacyJSON[T]` only. | Today's JSON default is readable in the census and diagnostics; the tag byte is what ABO lacks when it must change an encoding; legacy bytes must stay what the old build reads. |
| Values over ~64 KiB: rely on Pebble value separation (`FormatValueSeparation`, already pinned, `storage_format.go:80`) and `KeysOnly` scans; split above 4 MiB. | Large values in a scan path dominate read amplification. |
| A keyspace never stores another owner's key as a foreign key except through `entity.Ref{Module, Kind, ID}` (03 §3.3, L0). | Module isolation (R3); one reference type (COR-17). |

### 5S.3.4 Claims, keyspace issue, enforcement (R8)

```go
// Claim reserves legacy keys (written before core existed) for one owner.
// Exactly one of Prefix or Key is set. A claim's owner and mode are a function
// of the build alone: no claim changes owner or mode on a store (R3-1).
type Claim struct {
	Prefix      string    // e.g. "book:"; ends in a separator byte (':' '/' '_' '.') or is listed in BareAllow
	Key         string    // exact single key, e.g. "narrator_counter"
	Except      []string  // CROSS-OWNER carve-outs only: sub-prefixes or exact keys claimed by exactly one other owner
	Mode        ClaimMode // zero rejected
	Serialized  bool      // commits touching it take commit stripes; Expect allowed (§5S.3.2)
	TakesOver   string    // optional "<former-owner>": explicit takeover of an orphaned claim (rule 5)
	Description string
}

type ClaimMode uint8
const (
	ClaimInvalid   ClaimMode = iota
	ClaimReadWrite           // owner reads and writes in place
	ClaimReadOnly            // frozen: reads only; every write refused
	ClaimAgingOut            // reads + Delete/DeleteRange (retention, read-through cleanup); Set refused (DATA-15)
	ClaimRetired             // like AgingOut, and expected to drain to zero: census and health report a non-empty retired range
)

// LegacyRange is the handle for an owner's claims, or for a grantee's grants
// (§5S.3.11). Keys are ABSOLUTE (legacy keys have no m/ or c/ prefix). Every
// read and every write is checked against the handle's claim set (or grant
// set) with longest-match resolution; a key that resolves to another owner,
// or to no claim or grant of this handle, is refused with ErrOutsideKeyspace,
// and an operation the mode forbids with ErrClaimMode. Scans and iterators
// are clamped: keys outside the handle are skipped (the iterator seeks past
// carve-outs), and c/ and m/ are never reachable.
type LegacyRange struct{ /* unexported */ }
func (r LegacyRange) Get(ctx context.Context, key []byte) ([]byte, error)
func (r LegacyRange) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (r LegacyRange) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
func (r LegacyRange) NewBatch() *Batch
func (r LegacyRange) NewIndexedBatch() *Batch
func (r LegacyRange) Snapshot() (*Snapshot, error)
func (r LegacyRange) Set(ctx context.Context, key, value []byte) error
func (r LegacyRange) Delete(ctx context.Context, key []byte) error
func (r LegacyRange) DeleteRange(ctx context.Context, start, end []byte) error // the whole range must resolve to this handle
func (r LegacyRange) Barrier(ctx context.Context) error // WAL sync barrier; replaces LogData(nil, Sync) (ident_state.go:1360)

// MigrationCompat classifies a step (renamed from Compat, COR-22).
type MigrationCompat uint8
const (
	CompatInvalid MigrationCompat = iota
	Additive // a build that does not know the step can still read and write the owner's data (G5)
	Breaking // a build that does not know the step must refuse the store: requires RaisesLevel (§5S.3.7)
)

// ReadAdapter decodes rows of one family written in an older shape into the
// current encoding, for an Online data migration's transition and for
// read-in-place legacy families (COR-22).
type ReadAdapter struct {
	Family string // legacy family prefix, or a relative sub-prefix of the owner's keyspace
	Read   func(ctx context.Context, key, raw []byte) ([]byte, error)
}

// Spec is what a module (module.Manifest.Storage) or core subsystem declares.
// Data migrations are declared separately (storage/datamigrate, L4) because
// storage (L1) cannot import tasks (SIMP-17).
type Spec struct {
	Keyspace          bool                   // wants m/<id>/ (or c/<sub>/)
	Level             int                    // highest minimum-reader level of this owner the build serves (§5S.3.7)
	MinVersionToServe int                    // 0 = every registered versioned step must be applied before the owner starts
	Claims            []Claim                // legacy claims; books in practice
	Grants            []Grant                // which core subsystem may reach which of these claims, in the legacy format (§5S.3.11, R3-1)
	Serialized        []string               // relative sub-prefixes of the keyspace that are Serialized; "" = the whole keyspace
	Migrations        []Migration            // versioned, §5S.3.5
	Legacy            []LegacyMigration      // books only, versions 1..66, §5S.3.5
	Derived           []DerivedIndex         // declarative part only; hooks and rebuild task via Registrar.DerivedIndex (§5S.3.9)
	ReadAdapters      []ReadAdapter
	LegacyGuard       *LegacyGuardProjection // books only, §5S.3.7
	UpgradeFloor      *UpgradeFloor          // §5S.3.5 (R2-4)
}
```

**Handles.** What a module receives is `module.StorageHandles{Keyspace storage.Keyspace; Legacy
storage.LegacyRange; Indexes index.Opener; Aux storage.AuxOpener; Derived map[string]storage.Derived}`,
defined in [06](06-app-sdk.md) and carried by `module.Deps.Storage`. It lives in `module` (L7), not in
`storage`, because it names `storage/index`, which imports `storage` (COR-2). Core subsystems receive
the same values from the kernel's constructors. `Legacy` is the zero value unless claims were declared.
A core subsystem named in some manifest's grant additionally receives, at lifecycle step 10 (06 P4c), a
`LegacyRange` over exactly its granted ranges in the grant's mode (§5S.3.11); a module never does.

**Claims do not change hands (R3-1).** Every claim's owner and mode are a function of the build alone,
and ABO's claims are the same at every stage of the extraction: books holds all 258 families
(§5S.9.1). One build therefore serves any store of any stage — after a skipped release, a rollback or a
roll-forward — with the same resolution. There is no adopt step, no claim hand-over, no contract and
no claim-mode level step: a core subsystem reaches a books family only through a grant (§5S.3.11), and
a family moves to a native keyspace only by a cutover (§5S.3.13), outside the extraction. This
supersedes round 2's per-store claim resolution (R2-3, DATA2-1) and the hand-over rule of the claim
ledger that DATA3-2 attacked. `TestS2bBooksWritesEveryFamily` boots the S2b claim set with books
writing every family in shadow and strict mode and expects zero violations; from the stage in which a
family is granted `Exclusive`, the same test runs with that grant in force and expects books'
remaining runtime writers of it to be zero.

**Legacy-migration grant (R2-4, DATA2-2).** Books' legacy steps 1..66 run on books' own claims, which
books holds for the whole extraction (06 P4b). Steps 62, 65 and 66 write ops families
(`internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`) that books grants
`Exclusive` to `core/tasks` from S5 (§5S.9.1), so the `LegacyRange` passed to a `LegacyMigration.Up`
differs from books' runtime handle in one way, and that difference is the **legacy-migration grant**:
exclusive grants do not restrict it. It exists only while that step runs, in P4b, before any grantee
holds a handle (P4c); each write through it into an exclusively granted range is logged once per (step,
family) at warning level with its count and counted in
`mediacore_storage_legacy_grant_writes_total{step,family}`; no other code path gets it. A core step
that reads a granted range (only a cutover's fold, §5S.3.13) declares `After` the highest step the
granting owner registers, so on a store that skipped releases books is brought to its latest version
first (§5S.3.5).

**Kernel validation at boot** (before any listener; any failure is a boot error; `ErrClaimConflict`
lists every conflict, not only the first; 06 reports grant problems as `ProblemGrant`):

1. Module IDs and core subsystem names are unique; keyspace prefixes derive from them, so they
   cannot collide.
2. No claim prefix or key starts with `m/` or `c/`, and none is a prefix of `m/` or `c/` (verified
   statically at HEAD for all 258 families: no family has `/`, and the only families starting with
   `m`/`c` — `met:`, `mlist:…`, `metadata_…`, `merge…`, `cat…`, `counter:`, `cursor:`, `col:`,
   `chapters:`, `candfb:`, `cover_text…` — diverge at the second byte). The books claim generator
   skips the reserved families `c/` and `m/` that 09 S0 registers in `keyfamilies.go` for the census
   (DATA-28).
3. **Overlap** (DATA-16). Two claims overlap when one's prefix is a prefix of the other's, or an exact
   key lies under a prefix. Resolution is always longest match, which selects both the owner and the
   `Mode`. Then:
   - **same owner** — legal, no `Except` needed (books' 89 nested pairs; a retired or dead child
     gets its own mode under a read-write parent);
   - **different owners** — legal only when the shorter claim lists the longer in `Except` and the
     longer is claimed by exactly one other owner; otherwise boot fails. This is the multi-app safety
     rule: two modules (possibly from two apps in one binary) can never both own a key;
   - two claims with the same prefix or key fail boot, whoever holds them;
   - grants take no part in ownership resolution: every granted range must resolve to a claim of the
     declaring owner (§5S.3.11).
4. Runtime proof for the static check: `c/storage/install` is **the first `c/` key ever written** to a
   store, by whichever build first writes under `c/` — 09 S0's guard (before its first served-ledger
   key) or, on a store that never ran S0, the first core-storage boot (§5S.4 step 5). The boot that finds
   the marker absent verifies that `[c/, c0)` and `[m/, m0)` are empty before anything else is written;
   a non-empty range halts boot with `ErrForeignKeys` and a sample of keys (an unregistered ABO writer
   is the only way this can happen; `keyfamilies_test.go` never proved every writer is registered,
   `raw/05-storage.md` §3). The marker is then the first write after the checkpoint, with a new
   `install_id` — except on a restore boot, where it takes the `install_id` the restore journal hands
   over (the pre-swap store's, or on a host with no store the revocation journal header's,
   [05-backup](05-backup.md) §5B.3.4 step 8), so a pre-S0 archive restored onto its own install keeps its
   lineage and its journal and archives stay `Own` (DATA3-12). Because S0 writes the marker first, a
   store that ran S0–S2 opens under core storage at S2b with its S0-era served ledger intact (DATA2-6,
   `TestS2bOpensStoreWrittenByS0`).
5. **Claim ledger** (DATA-28). After validation, and after the checkpoint (§5S.4 step 9, R2-8), the
   kernel writes `c/storage/claims/<owner>` = `{claims, modes (by name), grants, backends, build, at}`
   for every owner in the build, enabled or not: `grants` is what the owner grants, and for a core
   subsystem `backends` records, per granted family group, whether it served from its legacy-format
   adapter (`legacy`) or from its native keyspace (`native`, only after a cutover, §5S.3.13). A claim on
   disk whose owner is absent from the build (a module removed, not just disabled) is **orphaned**: the
   census shows it, no write path reaches it, and a build may claim the same range only with
   `TakesOver: "<former-owner>"` on that claim — an explicit, reviewed takeover — otherwise boot fails.
   Since no claim changes hands during the extraction, this is the only orphan case (DATA3-2 is resolved
   by R3-1).

**Reads are checked too** (DATA-17). Before S4, books reads and writes the identity families directly
(the user merge scans `idx:apikey:user:`, `apikey:`, `idx:oauth:user:`, `oauthid:`, `invite:`,
`user_merge.go:241-242,454-486`). From the stage in which books grants them `Exclusive` to `core/auth`
(§5S.9.1), books' runtime handle keeps its reads — books is still the owner — but its writes to them are
refused, so the identity half of the user merge goes through the participant API
(`auth.Admin.StageUserMerge`, §5S.3.2), which binds auth's granted handle. A core subsystem never reads a
family it holds no grant on. The census and backup use `storage.Admin` (kernel-only), never a
`LegacyRange`.

### 5S.3.5 Versioned migrations (G2)

```go
// StepRef names one migration step of any owner, for cross-owner ordering.
// Its one printed spelling is "<owner>#<version>" ("core/integrations#1", "books#66")
// or "<owner>#<data-migration-id>"; bookkeeping is keyed by the same owner ID
// (c/storage/ks/core/auth/...), never by "core" alone (DATA2-29).
type StepRef struct {
	Owner         Owner
	Version       int    // a versioned or books legacy step; or
	DataMigration string // a storage/datamigrate ID
}

// Served is a precondition on the store's served ledger (§5S.3.7). It is met
// when the latest UNINTERRUPTED run of boots by builds whose supported level
// for Owner is >= Level has lasted at least For (wall clock from the first
// boot of that run). A boot by a build supporting a lower level, or a detected
// foreign open (a pre-guard build opened the store, §5S.3.7), ends the run, so
// a rollback restarts the clock (DATA2-9). An unmet precondition DEFERS the
// step (it never fails boot): see "Skipped releases" below. 09 decides which
// steps use it (a cutover, §5S.3.13).
type Served struct {
	Owner Owner
	Level int
	For   time.Duration
}

// Migration is one forward-only step of one keyspace. There is no Down.
type Migration struct {
	Version        int             // 1..n, contiguous per owner
	Name           string
	Compat         MigrationCompat // zero rejected at registration
	RaisesLevel    int             // required when Compat == Breaking: the owner's new level (§5S.3.7)
	After          []StepRef       // must run after these steps (DATA-9)
	RequiresServed *Served
	Up             func(ctx context.Context, tx *MigrationTx) error
}

// UpgradeFloor lets a build delete step code that an older store would still
// need (books' legacy steps 1..66 after the minimum-upgrade-from release, R2-4;
// a family's legacy-format adapter once its cutover ran, §5S.3.13). A store
// whose applied version for Owner is below Version, or on which a step listed
// in Requires is not applied or done, is refused before anything is written.
type UpgradeFloor struct {
	Owner        Owner
	Version      int       // 0 = no version floor
	Requires     []StepRef // versioned steps or data migrations (a cutover) that must be applied/done (DATA3-10, OPS3-7)
	InstallFirst string    // the release to install first (09's minimum-upgrade-from)
}

// LegacyMigration is books' existing step form, kept for versions 1..66 only
// (DATA-8). Up runs ABO's MigrationFunc through the books repository layer
// over r, which carries the legacy-migration grant (§5S.3.4); its store
// methods commit their own batches and allocate IDs with their own Sync
// writes, so the step is NOT atomic.
type LegacyMigration struct {
	Version int
	Name    string
	Up      func(ctx context.Context, r LegacyRange) error
}

// MigrationTx is an indexed batch over the keyspace (plus its legacy claims),
// with a hard size cap. Reads see the step's own writes. The runner commits
// tx + version bump + applied record (+ level stamp) as ONE Pebble batch.
type MigrationTx struct{ /* unexported */ }
func (tx *MigrationTx) Keyspace() Keyspace       // reads go through the tx
func (tx *MigrationTx) Legacy() LegacyRange      // reads/writes go through the tx
func (tx *MigrationTx) Set(key, value []byte) error
func (tx *MigrationTx) Delete(key []byte) error
func (tx *MigrationTx) Log() *slog.Logger

var (
	ErrMigrationTooLarge     = errors.New("storage: migration step exceeds core.storage.migration.max_batch_bytes; make it a DataMigration")
	ErrKeyspaceTooNew        = errors.New("storage: owner has a Breaking step above this build's supported level")
	ErrBookkeepingUnreadable = errors.New("storage: migration bookkeeping unreadable")
	ErrMigrationCycle        = errors.New("storage: migration DAG has a cycle, an unknown reference or an unordered owner")
)

// ErrUpgradeFloor: the store has not applied what a declared UpgradeFloor
// requires (R2-4, DATA2-2, DATA3-10); the message names InstallFirst and the
// missing steps. Nothing was written.
type ErrUpgradeFloor struct {
	Owner        Owner
	Have, Floor  int
	Missing      []StepRef
	InstallFirst string
}

// ErrMigrationFailed wraps the failing step.
type ErrMigrationFailed struct{ Step StepRef; Err error }
```

Runner rules:

| Rule | Detail |
|---|---|
| Order (DATA-9, R2-4, COR3-14) | One **DAG** over every pending step of every enabled owner: versioned steps, books legacy steps and `BootBlocking` data migrations (§5S.3.6). Edges: an owner's steps by `Version`; every `After`; data-migration `After`; and an implicit edge from every core owner's steps to the highest registered books `LegacyMigration`, so the legacy-migration phase completes first (§5S.3.4). Ties are broken by the kernel's fixed core subsystem order — `storage`, `secrets`, `config`, `compat`, `log`, `auth`, `rbac`, `audit`, `tasks`, `outbox`, `index`, `library`, `providerhttp`, `integrations`, `ai`, `download`, `indexer`, `notify`, `acquire`, `backup`, `updater`, `health`, `arrfacade` — then modules by ID, then version. A core owner that declares a step or a data migration and is absent from that list fails registration with `ErrMigrationCycle` ("unordered owner"), so boot order is always deterministic. A cycle, or a reference to an unknown step of a registered owner, is `ErrMigrationCycle` (boot error). An edge to a step that is already **applied** in this store's bookkeeping is satisfied even if its owner is disabled; an edge to an unapplied step of a disabled or absent owner defers the dependent step, and its owner is not started (rule "Barrier and serving" below). There is no "R4 layer order" rule: layer order is not an order inside L4. |
| Bookkeeping (DATA-28, DATA2-29) | `c/storage/ks/<owner>/version` (uint64) and `c/storage/ks/<owner>/applied/<v>` = `{name, compat, raises_level, applied_at, build}` (notation §5S.3.3; the owner ID — `core/tasks`, `core/auth`, `books` — is one segment); data migrations in `c/storage/dm/<id>` (§5S.3.6). Books uses a **version-source adapter** over `preference:db_version` / `preference:migration_<n>` so every older ABO build keeps seeing the numbers it wrote; the adapter adds `compat` and `raises_level` fields to new `migration_<n>` records (older builds ignore unknown JSON fields). An applied step this build does not know and whose record has **no** `compat` field is treated as `Breaking`. Every guard-aware boot writes the applied-steps sidecar from this bookkeeping (§5S.3.7). |
| Atomicity | Versioned steps: step writes + version + record (+ level stamp) in one batch, so there is no replay window; idempotency remains a test, belt and braces. Books legacy steps 1..66: **not atomic** — crash safety is ABO's idempotency test (`TestMigrationUpFunctionsAreIdempotent`) plus the kept DB-02 replay guard (`migrations.go:536-548`); bookkeeping goes through the adapter after `Up` returns. `TestNoNewLegacyMigrations` pins the highest `LegacyMigration` version at 66, so every new books step is a `Migration` or a `DataMigration`. |
| Size cap | `MigrationTx` refuses past `core.storage.migration.max_batch_bytes` (default 64 MiB) or `max_batch_ops` (default 250 000) with `ErrMigrationTooLarge`; the author moves the work to a `DataMigration`. |
| Checkpoint (R2-8) | One per upgrade attempt, before **anything** is written to the store — ratchet, step, install marker, ledgers, every-boot writers (§5S.3.12, 06 §6.5.1 P4a) — and only when a step will actually run: a deferred step does not count (OPS3-14). If it cannot be taken, boot halts before anything is written. |
| Failure (DATA2-19) | A failed versioned `Up` discards its batch; a failed legacy step may leave idempotent partial writes. Either way `app.New` returns an error wrapping `ErrMigrationFailed`, the process exits non-zero, earlier steps stay committed, and the attempt's checkpoint stays **open**, named by `.migration-checkpoint`, for the offline `storage restore-checkpoint` (§5S.3.12). There is **no automatic restore**: it would fight the crash-loop reuse rule and the non-atomic legacy steps. No partial versioned step is ever visible. |
| Unreadable version | Fail closed (`ErrBookkeepingUnreadable`), never "assume 0" (contrast `migrations.go:582`). |
| Newer version on disk | Unknown applied steps all `Additive` → boot continues with a warning (G5: an older build reads a newer DB). An unknown applied `Breaking` step (or compat-less books record) whose recorded `raises_level` is above this build's supported level for the owner → `ErrKeyspaceTooNew`. Levels are reader capabilities, so an unknown `Breaking` step at or below the supported level is readable by definition. This is a consistency check on top of the guard, which refuses the same store first (§5S.3.7). |
| Upgrade floor (R2-4, OPS2-11, DATA3-10, OPS3-7) | A build may delete code that a pending step of an older store would need — books' legacy steps 1..66 after 09's minimum-upgrade-from release, or a family's legacy-format adapter once its cutover ran — **only** by declaring an `UpgradeFloor` for that owner: a minimum applied `Version` and/or `Requires`, the steps that must be applied or done (a cutover is a data migration, `<owner>#<id>`). A level cannot stand in for this: it proves a reader capability, not that a given step ran. A store that falls short is refused at lifecycle step 3, before the checkpoint, with `ErrUpgradeFloor` naming `InstallFirst` and the missing steps, so a skipped-release install passes through the named release instead of failing half-way. The same comparison runs **before** a binary is applied, from the store's applied-steps sidecar and the candidate's `--print-upgrade-floor` (§5S.3.7, `CheckReader` reason `upgrade_floor`; the updater's `StoreState`, 05-updater), so neither the updater nor `make rollback` installs a build that the next boot would refuse (`TestUpgradeFloorRefusesBeforeAnyWrite`, `TestUpgradeFloorRequiresDoneStep`, R-test cell "store at books v64 → cur"). |
| Skipped releases (OPS-2) | Releases may be skipped as long as the build still registers every step the store has not applied; a build that no longer does declares an `UpgradeFloor` (above). `RequiresServed`, when set, **defers** the step while the served ledger does not yet show an uninterrupted run at ≥ the named level for `For` (09 policy): the step and every step that depends on it (`After`, and later versions of the same owner, which stay contiguous) are skipped for this boot, the owner serves at its last applied version (a deferred step does not count toward `MinVersionToServe`), the boot log and health detail name the step and the earliest time it can run (token `storage_step_deferred`, status Up), and every later boot re-checks it. Deferral never deletes, never fails boot, and never raises a level. Only `ErrUpgradeFloor` refuses to start. |
| `RequiresSelfServed` (OPS2-2) | Implicit on every step with `RaisesLevel` (books' physical re-encodings and cutovers). Such a step is deferred, exactly like an unmet `RequiresServed`, on a boot where the kernel reports an unconfirmed update (05-updater `BootResult.Pending`, i.e. `<state_dir>/updates/pending.json` exists), and until **this build** has been continuously ready for `max(core.updater.confirm_after, core.storage.self_served_min)` (default 1 h), recorded in its self-served record (§5S.3.7). A level step therefore never runs on the first boot of an auto-applied release, so the updater's `Boot` and `make rollback` can still undo a broken release; health reports `storage_step_pending` with the earliest run time (R-test cell d': "release with a level step fails readiness on first boot → `Boot` rollback allowed"). |
| Barrier and serving | An owner starts only when its applied version ≥ `MinVersionToServe` (default: its latest registered version). When an `Online` data migration's `Barrier` or a deferred edge holds the owner below that, the owner is not started in this process, health is `degraded` with the reason, and when the blocking migration completes the kernel requests a graceful restart through the lifecycle supervisor. |
| Systemd budget (OPS2-17) | The kernel passes storage `ExtendStart func(step string)` (05-health's `Supervisor.ExtendStart`; storage does not import `health`). It is called right before each step that makes real progress — store open (WAL replay), the pre-migration checkpoint, each migration step, each group of `BootBlocking` chunks, each cutover phase — and extends the start deadline by `StartupStepBudget` (10 min, HEAD's value, `internal/sdnotify/sdnotify.go:46-53`, called per step at `internal/database/startup_notify.go:15-27`). Never from a timer, so a hung step is still killed. Each step's duration is also reported to the kernel's boot metric with its step name (06, OPS2-24). |

### 5S.3.6 Data migrations (`storage/datamigrate`, large, resumable)

Package `github.com/falkcorp/mediacore/storage/datamigrate` (L4: imports `storage`, `entity` and `tasks`,
so the parallelism declaration is a real `tasks.Parallelism` and there is no storage-local mirror type,
SIMP-17). Modules declare data migrations in `module.Manifest.DataMigrations` ([06](06-app-sdk.md));
core subsystems hand them to the kernel with their `storage.Spec`. The runner is
`core/internal/storagemigrate` (L4 by the R4 bridge rule). **`BootBlocking` data migrations run in
process** inside the P4 DAG (06 §6.5.1 P4b), through `tasks.ForEach` over the persisted shard plan
with the persisted shard cursors below — the tasks service and executor do not exist yet in P4, so they
are not executor runs. **`Online`** data migrations are executor runs started at 06 §6.5.2 step 5
(COR2-7): a **core** owner's is a run of the core kind `core.storage.migrate`; a **module**'s is a run
of the module kind `<module>.datamigrate.<name>`, which the kernel registers on the module's behalf with
the migration's `Writes` as its `Effects` (plus one `Subjects` entry per shard), so the dispatch
conflict rule serialises the rewrite against the module's own writers and no core run invokes a module
callback that writes module entities (COR3-5; 05-tasks 5T.3.12 and 5T.4 step 0; 06 §6.5.2 step 5).

```go
package datamigrate

type Mode uint8
const (
	ModeInvalid  Mode = iota
	BootBlocking // a DAG node of the boot (§5S.3.5); a status listener reports progress (05-health)
	Online       // a background task while serving; must be Additive and covered by read adapters
)

type DataMigration struct {
	ID              string // "<owner>.<name>", unique, stable forever; a module's kind is "<module>.datamigrate.<name>"
	Owner           storage.Owner
	Description     string
	Source          []storage.Range           // absolute ranges in the owner's keyspace/claims (a cutover: its grants) to visit
	Parallelism     tasks.Parallelism         // required (R7): tasks.CPU(), tasks.Fixed(n, reason), tasks.Sequential(reason)
	Mode            Mode                      // zero rejected
	Compat          storage.MigrationCompat   // zero rejected; Breaking ⇒ BootBlocking and RaisesLevel > 0
	RaisesLevel     int                       // stamped (sidecar first) before the first chunk writes (§5S.3.7); a cutover: in its raised phase (§5S.3.13)
	Writes          []entity.Ref              // the resource kinds Step writes (Module and Kind set, ID empty); required for an Online module migration, whose kind's Effects they become (COR3-5)
	RewritesInPlace bool                      // Step writes keys inside Source
	After           []storage.StepRef
	Barrier         int                       // owner Version that may not run before this completes (0 = none)
	Step            func(ctx context.Context, kv storage.KV, w *StepWriter) error
	Verify          func(ctx context.Context, r storage.Reader) (VerifyReport, error) // invariant gate after the last shard
}

// StepWriter accumulates a chunk's writes; the runner commits them with the
// shard's cursor in ONE batch every core.storage.data_migration.chunk_bytes
// (default 4 MiB) or chunk_items (default 2 000).
type StepWriter struct{ /* unexported */ }
func (w *StepWriter) Set(key, value []byte) error
func (w *StepWriter) Delete(key []byte) error
func (w *StepWriter) SetIfAbsent(key, value []byte) error             // Expect(key, nil) at chunk commit
func (w *StepWriter) SetIfUnchanged(key, expect, value []byte) error // Expect(key, expect) at chunk commit
func (w *StepWriter) Hold(key []byte, reason string) // record and skip; held items listed, never silently dropped
func (w *StepWriter) Count(outcome string)           // "converted", "already-current", ...

type VerifyReport struct {
	Read, Written, Held int64
	Failures []string // bounded sample
}

// DerivedBuildSpec and DerivedBuildTask build the rebuild task of a derived
// index (§5S.3.9). They live here, not in storage, because storage (L1)
// cannot name a tasks type (COR3-7); a module may equally build the same
// tasks.Definition with tasks.Task.
type DerivedBuildSpec struct {
	Title       string
	Reads       []entity.Ref      // module resources the rebuild reads (Effects: ReadOnly over them)
	PermStart   string            // the module permission that may start a rebuild by hand
	Timeout     time.Duration     // 0 = 24h (library-scale, as Batch)
	Run         func(rc *tasks.RC) error // its library-scale loops declare their tasks.Parallelism (R7)
}

// DerivedBuildTask returns the tasks.Definition of kind
// "<module>.derived.<name>.build" (name module-relative, e.g. "hnsw"), with
// Resume set to restart from scratch; Registrar.DerivedIndex takes it (06).
func DerivedBuildTask(module, name string, s DerivedBuildSpec) tasks.Definition
```

Execution contract (built on [05-tasks](05-tasks.md)):

- **One run per data migration ID.** An `Online` run's task resource is, for a core owner,
  `entity.Ref{Module: "core", Kind: "storage-range", ID: <migration ID>}` (G8); for a module, its
  declared `Writes` plus a per-shard subject. State `c/storage/dm/<id>` ∈ {pending, running, verifying,
  done, failed, held}; a cutover adds its phases (§5S.3.13). An ID is never reused, so a `done` state is
  never re-entered (DATA2-15).
- **Exact resume** (DATA-12). At first start the runner computes the shard plan `{n, boundaries}` from
  `Parallelism` and the key distribution and persists it at `c/storage/dm/<id>/plan` before any shard
  runs. Every resume reuses that plan, whatever the CPU count, container limit, host or key
  distribution is now. Each shard's cursor `c/storage/dm/<id>/shard/<n>` (last key processed) is
  written in the same batch as the chunk's writes, so a resume restarts exactly after the last
  committed key. Shards are disjoint ranges, which is what makes parallel workers safe (CLAUDE.md
  concurrency rule): no two workers touch the same key.
- **Own writes are never revisited.** `Step` must be idempotent for its own outputs and count
  `already-current`. A write into the not-yet-visited part of any shard's range is refused unless the
  migration declares `RewritesInPlace`; then the runner materialises each chunk's key list before
  stepping it and skips keys the migration wrote itself, so the mandatory 30 s iterator re-open
  (§5S.6) cannot see its own commits as new input.
- **Concurrent owner writes** (DATA-13). An `Online` migration over a `Serialized` range uses
  `SetIfAbsent` / `SetIfUnchanged` and per-item chunks; a failed precondition re-reads that item. A
  module's `Online` migration over a range that is not `Serialized` relies on its kind's `Effects`
  for exclusion from the module's own writers (above). A copy (a cutover's fold) never overwrites a
  destination row the owner wrote (owner writes win).
- **Compatibility class** (DATA-12, DATA-7). `Online` ⇒ `Additive`, and the owner declares
  `ReadAdapters` covering every family the migration rewrites, so old and new rows are both readable
  throughout. `Breaking` ⇒ `BootBlocking` with `RaisesLevel`; the level stamp is committed (sidecar
  first) before the first chunk, so a half-converted store is already too new for older builds. A
  cutover is a `Breaking` `BootBlocking` data migration with its own phases (§5S.3.13).
- **Dry run** (DATA-14, DATA2-26) is the real runner on a throw-away copy: offline
  `<app> storage migrate --dry-run [--id <id>] [--from-archive <file>]` refuses while another process
  holds the DB lock, opens the live store **`ReadOnly`** (no WAL flush, no ratchet, no write of any
  kind to the live directory) and hard-links its SSTs and copies its WAL, MANIFEST and OPTIONS into a
  scratch directory beside the DB (same filesystem, free-space precheck, §5S.3.12), or extracts the
  archive there. WAL replay happens in the scratch copy, which is then opened as a full DB; the runner
  runs **every** pending step in DAG order — versioned, legacy, `BootBlocking` and `Online` data
  migrations including `Verify` — and reports per step: counts per outcome, held items, bytes written,
  the first N keys per outcome, duration; then deletes the scratch. Cross-chunk state is faithful and
  stray writes outside `StepWriter` land in scratch. While a service holds the DB, use
  `--from-archive` (the newest verified archive) or the preview of a pending `Online` migration (its
  run's preview executes the same scratch procedure inside the process via `storage.Admin.Checkpoint`).
  The old in-batch discard mode remains only as `--estimate` and is labelled an estimate in its output.
- Cancellation stops at a chunk boundary. A failed `Verify` leaves state `failed` with the report;
  `Barrier` versions stay blocked, so later versioned steps cannot run on a half-converted keyspace.
  `Hold` mirrors the storage-efficiency plan's held list (B6c, `2026-10-03-storage-efficiency-plan.md:170`).

### 5S.3.7 The store guard: per-owner reader levels (storage format + compat level, merged)

One mechanism (SIMP-18), owned here; the policy of when each level rises, and which build supports
which level, is [09](09-abo-on-core.md) §9.2.2. It replaces both the storage-format guard and 09's
separate compat-level guard, which were the same four parts twice (stamp, sidecar, print flag,
rollback-script check; `storage_format.go:30-50,114-182`).

**Semantics.** Every owner has a monotone integer **level**: the minimum reader level a build needs to
serve that owner's data. Absent stamp = 0. A build declares, per owner it contains, the highest level
it serves (`Spec.Level`; core subsystems share one `core` level). Open refuses a store when, for any
owner the build contains, the store's level is above the build's (`LevelTooNewError`, fail-closed).
Owners present in the store but not in the build are reported and left dormant. A level changes only in
a **level step**, always as `level := max(stored, new)` so no step ever lowers it (DATA2-22): a
`Breaking` step's `RaisesLevel` — a physical re-encoding (the storage-efficiency release B) or a
cutover (§5S.3.13). There is no separate "storage format" number in core (DATA-7). There is **no
"older store" refusal**: a build brings an older level forward with its own pending level steps, and a
raise in progress (stamp raised, migration not done) is resumed by the `BootBlocking` runner before any
service starts, so a half-converted store is never served (DATA-20). Levels are **per owner**, so a
books change never stops a TV app on the same core release, and books can raise its level without a
core release (DATA-20). Levels defined by reader capability (OPS-3) are a policy choice 09 makes with
this mechanism: a build that can already read a new shape declares the higher level before any store is
raised to it.

**Rollback during the extraction (R3-1).** No step of the extraction raises a level for `core` or for
any core subsystem: there are no adopt floors, no window-end contracts and no claim-mode level steps.
Books' level rises only through books' own `Breaking` steps (storage-efficiency release B). So while
every level is 0 the legacy projection stays 1 and the guard admits every earlier release, pre-guard
ones included: `make rollback` and the updater's `Boot` may return to the previous release at any stage,
and that release reads every row because every family keeps one stored format (§5S.3.11). The guard's
job is the real format changes — books' own raises and, outside the extraction, each cutover, whose
floor is the cutover release itself (§5S.3.13). What a rollback costs is stated by the owners of the
core-only families (one re-login, 05-authn; re-entering secrets moved to the encrypted store,
05-config). The live security hotfixes of `todo.d/2026-11-04_platform_core_followups.md` (revoked-key
re-activation, unscoped child keys, the `RequireAdmin` scope check, session-token exposure) ship
independently and early, so every rollback target from then on carries them; a rollback to a release
older than those hotfixes reopens what they fix, which [14](14-risks-open-questions.md) records.

```go
type Levels map[Owner]int

type LevelTooNewError struct {
	Owner           Owner
	Store, Supported int
	Source          string // "stamp", "sidecar" or "legacy-stamp"
}

// LegacyGuardProjection is declared by books only, so that builds older than
// the guard — which read only ABO's storage-format names — refuse a store
// whose levels were raised. Core never writes these names into another app's DB.
type LegacyGuardProjection struct {
	StampKey      string // "preference:storage_format"
	MarkerKey     string // "preference:storage_migration"
	SidecarSuffix string // ".storage-format"
	// LegacyMaxFormat is the highest SupportedStorageFormat any pre-guard
	// release shipped (1 at 748fd787a; TestLegacyMaxFormatMatchesReleases
	// checks the release history). The projection of a raised store is
	// LegacyMaxFormat+1, which every pre-guard build refuses (OPS2-10).
	LegacyMaxFormat int
	// Stage writes the stamp and marker rows into b through books' own
	// claim: it rewrites the existing preference record keeping its ID and,
	// for an absent stamp, allocates the ID from books' counter:preference,
	// as storage_format.go:338-356 does today (DATA2-22). The kernel binds
	// books' LegacyRange to the level step's batch for this call only.
	Stage func(ctx context.Context, r LegacyRange, b *Batch, value int, marker bool) error
}
```

| Item | Location | Notes |
|---|---|---|
| Install marker | `c/storage/install` = `{install_id, created, build}` (codec `JSON` v1) | the **first** `c/` key on every store (§5S.3.4 rule 4): 09 S0's guard writes it before its first served-ledger key, with `storage/keys` and this exact shape; a core build writes it at §5S.4 step 5 on a store that has none, inheriting the restored install's `install_id` on a restore boot (DATA2-6, DATA3-12) |
| Store generation (DATA3-1) | `c/storage/epoch` = `{epoch, dir_dev, dir_ino, at}` | the store's identity across swaps (distinct from a derived index's storage generation, §5S.3.9). Every guard-aware boot stats the DB directory at §5S.4 step 2; when the record is absent or names another directory identity — the directory was swapped by a restore, revert or checkpoint restore, or copied or replaced by hand — this boot's generation is `epoch + 1`, written at step 9 with the new identity. A swap-back returns the original directory with its own record, so it is the same store again; a store restored **in place** by a pre-core build carries its archive's older record. Input to checkpoint reuse (§5S.3.12) |
| Stamp | `c/storage/level/<owner>` = `{level, by_build, at}` | written only by a level step |
| Level sidecar | `<db path>.storage-levels` | one line per owner, `<owner> <level>`, sorted; written temp file + fsync + rename + directory fsync; read by `make rollback` over ssh and by the updater without opening Pebble; every guard-aware boot rewrites it from the stamps, so after the first such boot it always exists |
| Applied-steps sidecar (02 N14, DATA3-10, OPS3-7) | `<db path>.storage-applied` | what the store has applied, readable **before open**: one line per owner `<owner> version <n>` (books from its version-source adapter) and one per finished data migration `<owner> done <id>`, sorted, plus a header line `install <install_id> epoch <n>`; written like the level sidecar by every guard-aware boot at §5S.4 step 9 (09 S0's guard included) and after every step that adds to it — always **after** the commit that applied the step, so it can lag the bookkeeping but never lists a step that did not run (a lag only makes a pre-apply check refuse more). Read by `CheckReader` (reason `upgrade_floor`), by `make rollback`, and by the updater's `StoreState` (05-updater), which compares it with a release manifest's `min_upgrade_from` (copied from the candidate binary's `--print-upgrade-floor`); missing or unreadable → the reader fails closed with `sidecar_unreadable`, and the message tells the operator to boot the current build once, which rewrites it |
| Served ledger (DATA2-9) | `c/storage/served/<owner>/<level>` = `{run_start, last_boot, boots, build}`; `c/storage/served/<owner>/last` = `{build, supported, duties, at}` | updated once per boot per owner, after the checkpoint. A boot of a build supporting S keeps the run of every level ≤ S if the previous boot (`last`) also supported it and no foreign open was detected, else starts it (`run_start = now`); it ends the run of every level > S. `duties` lists the store-maintenance duties the build performs on every boot and write — `journal` (it writes the revocation journal, 05-backup §5B.3.6) and `gen` (it touches derived-index generations, §5S.3.9); an absent field means none. A consumer that needs a duty to have held since some point checks that the previous boot listed it and no foreign open was detected, else falls back (05-backup records a coverage gap; the derived loader rebuilds). Input to `Served` preconditions (OPS-2) |
| Foreign-open record (DATA2-9, DATA3-20) | `c/storage/sidecar_identity` = `{device, inode, at}` of `<db>.storage-format` | every pre-guard open rewrites that sidecar by temp file + rename (`internal/database/pebble_store.go:776-786`, `storage_format.go:285-300`), so its inode changes. Each guard-aware boot — 09 S0's guard included — compares the recorded identity with the file's before rewriting it; a difference means another binary opened the store in between: the boot logs it, ends every level's run, makes the previous boot's `duties` count as none, then rewrites the sidecar and records the new identity. Every level step records the identity of the `.storage-format` it just rewrote in its stamp batch (step 3 below), so a crash between the sidecar write and the end of the boot never reads as a foreign open |
| Self-served record (OPS2-2) | `c/storage/selfserved/<build id>` = `{first_boot, first_ready, confirmed_at}` | `first_ready` ends the current upgrade attempt (§5S.3.12); `confirmed_at` is written once this build's process has been continuously ready for `max(core.updater.confirm_after, core.storage.self_served_min)`; input to `RequiresSelfServed` (§5S.3.5) |
| Fast-exit flags | `--print-storage-levels`, `--print-upgrade-floor` | print, and exit before opening anything, the build's supported vector (`<owner> <level>` per line) and its declared upgrade floors as an `UpgradeRequirement` (`{release, owners: {<owner>: <version>}, steps: [<owner>#<id>]}`, 05-updater); computed from manifests only |
| Legacy projection (books) | stamp `preference:storage_format`, marker `preference:storage_migration`, sidecar `<db>.storage-format`, flag `--print-storage-format` | value **1** while every owner's level is 0, **`LegacyMaxFormat + 1`** once any level is above 0 — during the extraction only after a books physical raise; later from a cutover's raise — which every pre-guard build refuses with `StorageFormatTooNewError` (`storage_format.go:44-49`); the marker is set while a books `Breaking` raise is in progress and cleared when it is done. Written only through `LegacyGuardProjection.Stage` (books owns the rows, §5S.9.1 row 10). A core build prints, for `--print-storage-format`, the projection of its own supported vector. The projection is derived and never decides anything for a guard-aware build, except that a legacy stamp above the value this build would write is treated as too new. A store converted by a pre-guard storage-efficiency release B (legacy stamp 2 at a time `LegacyMaxFormat` is 2) is mapped at its first guard-aware boot to books level ≥ 1 by books' first versioned step; 09 §9.0 orders release B behind this guard (OPS2-10) |
| Checkpoint record | `<db path>.migration-checkpoint` | names the checkpoint of the latest upgrade attempt and whether it is open or completed (§5S.3.12); absent when there is none; read by the rollback script to print the restore steps |
| Pebble format pin | `FormatValueSeparation` | one constant for main and auxiliary DBs; ratcheted only after the checkpoint (§5S.4) |

**One implementation.** `core/internal/pebble/guard.go` has one `guard{stampKey, sidecarPath,
printFlag, compare}` type instantiated twice: the per-owner vector (`compare: minReader`) and, for
books, the legacy projection (`compare: legacyExact`).

**Level step procedure** (shared by versioned steps and data migrations, cutovers included):

1. The boot's checkpoint exists (§5S.3.12).
2. Compute `new = max(stored, RaisesLevel)`; if it equals `stored`, the step stamps nothing and the
   procedure ends here. Otherwise write the sidecar(s) with the new level — `.storage-levels`, and for
   books also `.storage-format` = `LegacyMaxFormat + 1` and, for a books `Breaking` raise, the marker —
   fsynced.
3. Commit the batch that holds the stamp (and, when the projection changes, `LegacyGuardProjection.Stage`)
   together with the foreign-open record of the `.storage-format` file just written (DATA3-20): for a
   versioned step, together with the step's writes; for a data migration, alone, before the first chunk;
   for a cutover, in its `raised` phase (§5S.3.13).
4. If that commit returns an error (disk full, not a crash), rewrite the sidecars from the committed
   stamps before returning, so a failed raise does not block rollback (OPS-26b).
5. When the raising step or migration completes, clear the books marker.

A crash between 2 and 3 leaves a sidecar ahead of its stamp: older builds refuse (safe), and the next
boot of this build re-runs the pending step. At open the guard uses `max(stamp, sidecar)` per owner
for the too-new check; an unreadable sidecar is rewritten from the stamps with an error log (kept from
`pebble_store.go:651-660`); when stamp and sidecar disagree the boot log prints both.

**Reader check (one decision, three callers).** `core/internal/pebble.CheckReader(ctx, stateDir,
dbPath, exe string, opt ReaderCheckOptions) error` decides whether the binary at `exe` may open the
store as it is now, without opening Pebble. It reads `.storage-levels` (or, if absent, the legacy
`.storage-format`) and runs `exe --print-storage-levels` (falling back to `--print-storage-format` for
a pre-guard binary), comparing per owner; it reads `.storage-applied` and `exe --print-upgrade-floor`
and refuses when the store has not applied what a floor of that binary requires (a pre-guard binary
declares none); and it refuses while a restore is pending or in flight — `<state_dir>/restore/pending.json`
exists, or a restore journal `restore/<id>/` holds an `inflight.json` (between `previous_moved` and
`applied`, [05-backup](05-backup.md) §5B.3.4, DATA3-6). The one exception: an intent whose
`staged_by_build` equals `opt.IgnoreRestoreStagedBy` and whose journal shows no swap step yet does not
refuse — the updater's `Boot` passes the failing build there, and the restored `.prev` then rejects that
intent as foreign (OPS2-3). A swap that has started always refuses, because only a core build can finish
or undo it. Reason tokens: `level_newer`, `format_newer` (pre-guard binary), `upgrade_floor`,
`restore_pending`, `restore_inflight`, `sidecar_unreadable`, `cannot_determine`. The kernel binds it as
[05-updater](05-updater.md)'s `ReaderCheck`; `scripts/storage_format_guard.py` (name kept) implements
the same table for `make rollback` and the operator `deploy` recipe. Over ssh the script reads the
sidecars and probes the restore state with literal sudoers command lines under a `DEPLOY_STATE_DIR`
variable (`test -e <state_dir>/restore/pending.json`;
`find <state_dir>/restore -mindepth 2 -maxdepth 2 -name inflight.json -print -quit`); a probe that cannot
run (no sudoers line, unreadable directory) is `cannot_determine`, which **refuses**, naming the missing
line, and `ROLLBACK_IGNORE_RESTORE=1` overrides only the restore part (OPS3-13). Both are tested against
one fixture set (`TestReaderCheckMatchesRollbackScript`). The sudoers lines for `.storage-levels`,
`.storage-applied` and the two restore probes are owner actions in 09 S0 and S1 (OPS-26a, OPS3-13).

**ABO's `RunCutover` is resolved** by this mechanism (the name is the storage-efficiency plan's and is
unrelated to the per-family cutover of §5S.3.13): the storage-efficiency release B becomes a books
`DataMigration{Compat: Breaking, Mode: BootBlocking, RaisesLevel: N}` following the B6a order
(checkpoint, record, raise before the first legacy delete, convert, `Verify`, clear marker). ABO's
`openForCutover`, `StorageMigrationCheckpointSuffix` and the comments at `storage_format.go:125-129`
and `pebble_store.go:597-619` are deleted when books moves onto core.

### 5S.3.8 Full-text index contract (`storage/index`)

Package `storage/index` (L1) imports `storage`; `storage` never imports it, and the kernel wires
`index.Opener` into `module.StorageHandles` (COR-2).

```go
package index

// Spec declares one named index owned by a module.
type Spec struct {
	Name           string  // "books"; on disk <db parent>/index/<module>/<name>.bleve
	MappingVersion string  // a change triggers drop + rebuild (today's bookMappingVersion, bleve_index.go:104)
	Fields         []Field
	LegacyPath     string  // books only: "library.bleve" beside the DB, kept so the existing index is reused
	LegacyDirty    string  // books only: "idx:sidx:dirty:" — the dirty set in HEAD's format, through books' grant to core/index
	Rebuild        func(ctx context.Context, w RebuildWriter) error // full rebuild from the module's storage
	Document       func(ctx context.Context, id string) (Doc, bool, error) // reconciler: load one doc, false = delete
}

type Field struct {
	Name     string
	Kind     FieldKind // Text, Keyword, Number, Date, Bool
	Analyzer string    // "standard", "keyword", or a module-registered analyzer name
	Store    bool
	Facet    bool
}

type Doc map[string]any

type Opener interface {
	Open(ctx context.Context, s Spec) (Index, error)
}

type Index interface {
	Search(ctx context.Context, q Query, page Page) (Result, error)
	Count(ctx context.Context, q Query) (uint64, error)
	Facets(ctx context.Context, q Query, fields []string, size int) (map[string][]FacetValue, error)
	// MarkDirty records id in the durable dirty set inside b, the same batch as
	// the entity write, and touches the index's storage generation (§5S.3.9).
	// It is the only write path modules use (G4: the index never depends on an
	// in-memory event).
	MarkDirty(b *storage.Batch, id string) error
	State() State // Ready, Rebuilding, Degraded (open failed; callers fall back)
	Close() error
}

// Query is engine-neutral; bleve types never appear in the SDK.
type Query interface{ isQuery() }
func Match(field, text string) Query
func Term(field, value string) Query
func Prefix(field, prefix string) Query
func Range[T int64 | float64 | time.Time](field string, min, max *T) Query
func And(qs ...Query) Query
func Or(qs ...Query) Query
func Not(q Query) Query
func QueryString(s string) Query // user query syntax, parsed by the engine

type Page struct{ From, Size int; SortBy []string }
type Result struct{ Total uint64; Hits []Hit }
type Hit struct{ ID string; Score float64; Fields map[string]any }
type FacetValue struct{ Term string; Count int }
type RebuildWriter interface{ Put(id string, d Doc) error }
type State uint8
const ( StateInvalid State = iota; Ready; Rebuilding; Degraded )
```

Implementation `core/internal/bleveindex` generalises `internal/search/bleve_index.go`: scorch, mapping
marker and rebuilding marker as **siblings** of the index directory (`bleve_index.go:177-184,257-264`),
open failure = `Degraded` not boot failure (`register.go:42-60`), mapping change = drop + rebuild.
The dirty set lives at `c/index/<module>/<name>/dirty/<id>` (generalising `idx:sidx:dirty:`,
`pebble_store_search_dirty.go:39`) — except for an index with `LegacyDirty`: books' index keeps HEAD's
dirty set `idx:sidx:dirty:<bookID>` (value `"1"`) and reads, writes and deletes it through books'
`Exclusive` grant to `core/index` (§5S.9.1 row 18). One format, so after a rollback the previous build
drains the marks this build left in the shared `library.bleve`, and after the roll-forward this build
drains the previous build's (R3-1). The reconciler is a `lifecycle.Component` (SIMP-6 rule: an
in-process loop shorter than the task scheduler's one-minute poll is not a `tasks.Schedule`): it wakes
every `core.index.reconcile.interval` and after any commit that carried a dirty mark, and drains with a
bounded pool of `core.index.reconcile.workers`. A full rebuild is a `tasks` definition with a declared
`tasks.Parallelism`. Backups capture the index with bleve's online `CopyTo` (`bleve/v2 index.go:350-354`)
under the generation rule of §5S.3.9 ([05-backup](05-backup.md)). ABO's query parser and per-user field
routing (`internal/search/query_*.go`, `per_user_*.go`) are books code that compiles to `index.Query` or
to `QueryString`.

### 5S.3.9 Derived indexes and storage generations (memdb, hnsw, chromem stay in books)

Decision with evidence: go-memdb (`MemStore`, tables are book tables, `memdb_schema.go:12-21`),
coder/hnsw (dedup layer 2 over `emb:` rows, `hnsw_embedding_store.go:98`) and chromem-go (opt-in
brute-force alternative, `registry_wire.go:71-76`) each have exactly **one** consumer, the audiobook
dedup/query paths. Under the promotion rule (03 §3.4) they stay app-level. What *is* generic is their
lifecycle and the check that a warm-start artifact still describes storage, which core owns.

```go
// Generation is an opaque token that changes in every batch that touches a
// derived index's source.
type Generation [16]byte

// Derived is the per-index handle (module.StorageHandles.Derived[name]); it
// carries a grant for exactly one key, c/storage/gen/<module>/<name>.
type Derived struct{ /* unexported */ }
// Touch blind-writes a fresh Sequence value into the generation key in b.
// Every batch that changes the index's source must Touch (index.MarkDirty does
// it for full-text indexes).
func (d Derived) Touch(ctx context.Context, b *Batch) error
func (d Derived) Current(ctx context.Context) (Generation, error)

// DerivedIndex is the DECLARATIVE part of anything rebuilt from storage
// (in-memory tables, ANN graphs, caches with an on-disk warm-start snapshot).
// It is manifest data (Spec.Derived), so the claim ledger and readiness gating
// know the index while its module is disabled.
type DerivedIndex struct {
	Name     string // module-relative: "memdb", "hnsw" (COR3-7)
	Version  string // a change forces a rebuild
	Blocking bool   // true: readiness waits for it (05-health)
}

// DerivedHooks are the closures, supplied through
// Registrar.DerivedIndex(name, hooks, build) (06) because they close over
// services built in Register (COR2-15). The rebuild is not a hook: build is an
// ordinary tasks.Definition of the MODULE kind "<module>.derived.<name>.build"
// (datamigrate.DerivedBuildTask builds one, §5S.3.6), which P7 validates like
// every definition (Title, Effects, Perms.Start, Resume, a Timeout sized for a
// library-scale rebuild). storage (L1) cannot name a tasks type (COR3-7).
type DerivedHooks struct {
	Snapshot func(ctx context.Context, dir string, gen Generation) error // optional warm-start export; records gen in the artifact
	// Load must refuse unless the artifact's recorded generation equals current
	// AND the owner's own check passes (hnsw truth count, server.go:845-866).
	Load func(ctx context.Context, dir string, current Generation) (accepted bool, err error)
}
```

Why a generation and not a count (DATA-25): a truth *count* passes when an add and a delete cancel,
and an artifact captured after a checkpoint can hold documents the checkpoint lacks. The generation
key is blind-written with a fresh, unique `Sequence` value by every touching batch. After an artifact
records value X, any later committed touch leaves a value ≠ X (values are unique, and X's own batch
committed before the snapshot), so an artifact can be falsely *rejected* (two touches committed out of
order) but never falsely *accepted* — **provided every write to the source touched the key**. A build
that predates the mechanism (any rollback target before the index's stage) changes the source without
touching it, so the loader accepts an artifact only when every boot since the artifact's generation was
recorded listed the `gen` duty in the served ledger and no foreign open was detected (§5S.3.7); after a
rollback to such a build, the next boot rebuilds. Rejection means rebuild — the artifact is an
accelerator only.

**When the hooks run (COR3-7).** At 06 P7 the kernel validates the declarations against the registered
hooks and build kinds: a declared index with no hooks, hooks for an undeclared index, or a build whose
kind is not `<module>.derived.<name>.build` fails boot (06 `ProblemDerivedIndex`). The version and
generation check, `Load` and the record `c/storage/derived/<module>/<name>` = `{version, state, built_at}`
run at Run start step (3a) (06 §6.5.2): after the module's hooks have started, so `Load` may use objects
a start hook builds (books' hnsw store and the truth count of `server.go:845-866`), and before
`Bus.Open`; never inside `app.New`, so `Describe` never calls them. The kernel starts the build kind on a
version change, on a `Load` refusal and after every restore ([05-backup](05-backup.md) §5B.3.4); a
`Blocking` index keeps readiness false until its build completes. Snapshots go to
`<db parent>/derived/<module>/<name>/` (ABO keeps `hnsw/` via a legacy path) and are backup class
`Cache`. Core components that invoke these module callbacks (the derived loader, the index reconciler)
start after module hooks and stop before them (06 §6.5.2).

### 5S.3.10 Auxiliary databases

```go
// OpenAuxiliary opens a separate Pebble DB for re-creatable bulk data a module
// owns (the Open Library dump mirror). Same format pin and close gate as the
// main DB; never system of record; backup class Recreatable by default.
// Exposed to modules as module.StorageHandles.Aux; path <db parent>/aux/<module>/<name>
// unless the module passes a legacy path.
func (h AuxOpener) OpenAuxiliary(ctx context.Context, name string, opts AuxOptions) (*Aux, error)

type AuxOptions struct{ LegacyPath string; CacheBytes int64 }
type Aux struct{ /* Keyspace-shaped API over the whole aux DB */ }
func (a *Aux) Root() Keyspace
func (a *Aux) Close() error
```

This closes TASK-A7's open item for `internal/openlibrary/store.go:43` (`FormatNewest`) by
construction. `NewAIScanStore` (`ai_scan_store.go:156`, no non-test caller) is deleted, not ported.

### 5S.3.11 Legacy-format storage grants (R3-1)

The access model for every core service that needs ABO's existing data (resolution R3-1, which
supersedes cross-cutting resolution 5 and R2-1/R2-2/R2-3 here). The family stays where and how HEAD
stores it; the owning module grants a named core subsystem access to it; the subsystem runs a
**legacy-format adapter** over the granted keys. One stored format means the previous build reads
every row the new build wrote, and the new build reads every row the previous build wrote after a
rollback, with nothing to reconcile.

```go
// Grant lets one core subsystem reach ranges inside the DECLARING owner's
// legacy claims, in the legacy format (R3-1). It is declared in the owner's
// storage.Spec (books: module.Manifest.Storage) and never changes ownership.
type Grant struct {
	To        Owner     // "core/auth"; a module ID is refused (R3)
	Prefixes  []string  // each resolves inside a claim of the declaring owner (a family or a sub-prefix of one)
	Keys      []string  // exact keys inside the declaring owner's claims
	Mode      GrantMode // zero rejected
	Exclusive bool      // the grantee is the ranges' only runtime writer; the owner's own runtime handle keeps reads only
	Reason    string    // required; printed in the boot log, the ledger and the census
}

type GrantMode uint8
const (
	GrantInvalid    GrantMode = iota
	GrantRead                 // Get, Scan, iterators
	GrantReadDelete           // + Delete/DeleteRange (retention, upgrade-on-use cleanup, secret blanking); Set refused
	GrantReadWrite            // + Set; Expect on the claim's Serialized ranges
)

var (
	ErrGrantNotOwned      = errors.New("storage: a granted range is not a claim of the declaring owner")
	ErrGrantConflict      = errors.New("storage: a range is granted exclusively to one subsystem and also to another")
	ErrGrantNotSerialized = errors.New("storage: a shared read-write grant covers a range that is not Serialized")
	ErrGrantMissing       = errors.New("storage: the store records a legacy-format backend this build has no grant for")
)
```

| Rule | Specification |
|---|---|
| Declaration | Only the owner of a claim grants it. Every prefix and key must resolve, by longest match, to a claim of the declaring owner (`ErrGrantNotOwned`); `To` must be a core subsystem with a legacy-format adapter for those families (a module grantee is refused at P2, R3; a grantee absent from the build makes the grant inert, logged). One range may be granted to several subsystems only when none of those grants is `Exclusive` (`ErrGrantConflict`). The list is reviewed: `storage_grants.golden` (the successor of round 1's read-share list, DATA-17) records every grant with its reason, and a change needs the owner's sign-off. Grants are manifest data, validated and honoured while the granting module is **disabled** (G1), so a core service never silently loses the rows it serves. 06 reports a failing grant as `ProblemGrant`. |
| Enforcement | At lifecycle step 10 (06 P4c) the grantee receives a `LegacyRange` over exactly its granted ranges: a read or write outside them is `ErrOutsideKeyspace`, an operation beyond the mode `ErrClaimMode`, and scans and iterators are clamped to them. `Exclusive` makes the owner's own runtime handle read-only on those ranges (`ErrClaimMode` on write), so the grantee is their one writer and its invariants (for example one staging function per family, 05-tasks §5T.6 rule 5) hold by construction; books' legacy migration steps are the only exception (§5S.3.4). A range's `Serialized` flag is the owner's claim's; a non-exclusive `GrantReadWrite` over a range that two writers read-modify-write must be `Serialized`, so both commit with `Expect` (`ErrGrantNotSerialized` at registration). |
| Logged and counted | Every grant is logged once per boot at info level (owner, grantee, ranges, mode, exclusive, reason), recorded in the claim ledger (§5S.3.4 rule 5) after the checkpoint, and listed by the census and `GET /api/core/v1/storage/claims` (`core:system.manage`). Every operation through a granted handle is counted in `mediacore_storage_grant_ops_total{grantee, family, op}` (`family` = the grant's prefix or key). |
| What a grantee writes | Rows in the owner's legacy format with `codec.LegacyJSON` (byte-compatible with what every build able to open the store decodes, DATA-5), together with every legacy index row the previous build resolves through (`idx:user:username:` beside `u:`, `idx:apikey:hash:` beside `apikey:`, `import_path:path:` beside `import_path:`) and the deletes of stale ones, in **one** batch with the primary row; IDs the legacy format allocates from a counter come from that counter (an exact-key grant on it, e.g. `counter:import_path`). A grantee never writes a row that would give an older build more access than the native value: it never writes an API key with an empty scope list, which older builds read as "all role permissions" (`internal/server/middleware/auth.go:405-408`; 05-authn §4.4), and every restriction it makes (revoke, disable, epoch bump, role removal) is a field older builds read. Revocations, post-restore hook writes and journal re-application are such ordinary writes ([05-backup](05-backup.md) §5B.3.4, §5B.3.6). Property tests per family live with the grantee ([05-authn](05-authn.md), [05-rbac](05-rbac.md), [05-config](05-config.md), [05-tasks](05-tasks.md)). |
| Native-only data | Two homes, nothing else. (1) **Additively in the legacy row**, where the legacy format tolerates it: values inside a field older builds preserve and ignore — a namespaced permission or scope string in a role's or key's list grants nothing to an older build, which matches by exact string (`internal/server/middleware/auth.go:430-455`) — or an extra JSON member with a stated default when absent, because pre-core builds decode rows into typed structs without a catch-all field and drop unknown members on their next rewrite (`internal/database/pebble_store_ops_v2.go:336-380`, `internal/database/iface_ops_v2.go:39`); no rule may depend on such a member surviving a rollback, and a restriction is never expressed only in one. (2) **A core-only family** `c/<subsystem>/…`, which no pre-core build reads: hashed sessions, native one-time tokens and the bootstrap hash (05-authn: a rollback costs one sign-in and voids what they carry), config keys with no legacy name and secrets the new build writes, blanked in the blob (05-config: rollback = re-enter them, stated in the release note), definition snapshots and native-only clock fields (05-tasks), AI batch fields (`c/ai/batch/`, 05-ai), integration extension records (`c/integrations/ext/`, 05-metadata), the journal head and gap records (05-backup). A core-only family is invisible to an older build and current again on roll-forward. |
| Adapter selection | A core subsystem that has a legacy-format adapter uses it exactly when the build declares a grant to it; without a grant it uses its native keyspace (an app with no pre-core history, [10](10-tv-movies-proof.md)). The claim ledger records the choice per subsystem (`backends`); a build whose choice differs from the store's record fails boot with `ErrGrantMissing` naming the subsystem — a build that lost the grant would otherwise serve an empty native keyspace. The only legitimate change is a cutover (§5S.3.13), which records `native` in its `raised` batch. |
| Format changes of a granted range | A granted range changes encoding only by a `Breaking` step of its owner with `RaisesLevel`, shipped with the grantee's adapter updated in the same build. `TestGrantedFamilyGolden` pins HEAD fixtures for every granted family: the grantee decodes and re-encodes each byte-identically. |
| No copy, no rebuild | No subsystem copies a granted family into its keyspace, mirrors its writes into a second format, keeps side records keyed by legacy IDs, or rebuilds anything at boot. Round 2's mirror machinery — `Mirror`, `MirrorView`, `SideRecord`, `Restriction`, `LegacyProjection`, native-row generations, the over-cap read-through, the verification rebuild, `ClaimMigrating`, adopt steps, contracts and claim hand-overs — does not exist (R3-1). |

Cost surfaced: the adapters and their golden fixtures belong to the grantees (05-authn, 05-rbac,
05-config, 05-tasks, 05-logging, 05-library, 05-ai, 05-metadata, `storage/index`); storage adds the
grant check, a ledger entry and one counter. What the extraction gives up is native shapes for these
families until each one is cut over (§5S.3.13).

### 5S.3.12 The pre-migration checkpoint

| Rule | Specification |
|---|---|
| When (R2-8, OPS3-14) | At lifecycle step 6 (06 §6.5.1 P4a), when this boot will write something a restore point must cover: a Pebble format ratchet, a versioned or legacy step, a `BootBlocking` data migration or a level step that will actually **run** — `RequiresServed`/`RequiresSelfServed` are evaluated read-only first, and a deferred step does not count — or the install marker on a non-empty store. It is then the boot's **first store write**: it precedes the ratchet (DATA-11; a ratchet ahead of the checkpoint makes it unopenable by the previous build, `internal/database/storage_format.go:61-80`), the DAG, the ledgers and every every-boot writer (auth's boot writes, journal re-apply, post-restore hooks, compat records), so it holds no post-upgrade row (COR2-7). A boot with nothing to run takes none; its every-boot writes are routine operation. An empty store needs none (§5S.4 step 5). |
| Where (DATA-10) | Beside the DB: `<db parent>/.mediacore-checkpoints/<name>/` (as ABO's backup staging sits beside the DB, `internal/backup/backup.go:569-571`), overridable by `core.storage.checkpoint_dir`. Hard links need the DB's filesystem, so the device is checked; a mismatch halts boot before any write with a message naming the override. Never the state directory, which may be another filesystem when the DB path is set by flag or env. |
| How (DATA3-22) | In this order: `storage.Admin.Checkpoint` with `WithFlushedWAL` into `<name>.tmp/`, fsync, rename to `<name>/`, directory fsync; then the record `<name>.json` = `{install_id, epoch, from_vector, journal_seq, taken_at, completed_at, consumed_at, status}` by temp file + fsync + rename + directory fsync; then `<db path>.migration-checkpoint` the same way. `epoch` is the store generation this boot runs at (§5S.3.7); `journal_seq` is the store's revocation-journal head read before the checkpoint, the boundary of a checkpoint restore (05-backup §5B.3.6). A checkpoint directory without a record (a crash before the record) is garbage and is swept at boot before the free-space check; a record without its directory is dropped. |
| Upgrade attempt and reuse (DATA3-1, OPS3-14) | An **upgrade attempt** begins with the boot that takes a checkpoint and ends at the first later boot that finishes every step it runs or reaches readiness (the self-served record's `first_ready`). A boot reuses an existing checkpoint instead of taking one only when **all** hold: its record's `install_id` and `epoch` equal the live store's (the store generation differs after any swap, copy or in-place restore); it has no `completed_at` and its `status` is not `restore_left`; no boot has reached readiness since its `taken_at` (a real crash loop); and its from-vector (every owner's applied version, every level, the Pebble format) is an ancestor of the current vector. So a crash loop — also when a fixed binary retries the failed upgrade — keeps the one pristine pre-upgrade checkpoint, and nothing else ever reuses one: a second upgrade takes its own (`TestSecondUpgradeTakesFreshCheckpoint`), a checkpoint taken weeks before a deferred step finally runs is not reused (`TestDeferredStepTakesFreshCheckpoint`), and a checkpoint of another store is never reused (`TestSwapBackCheckpointNeverReused`; 05-backup step x also moves the checkpoints a failed restore boot took into `restore/<id>/failed/` and marks any it cannot move `restore_left`). |
| Completed, consumed, pruned (DATA2-18) | When a boot finishes every step it ran, it writes `completed_at` into the attempt's record (a deferred step does not keep an attempt open: the later boot that runs it begins a new attempt with a new checkpoint). **Any** later boot marks a completed checkpoint consumed once `completed_at` is at least `core.storage.migration.checkpoint_min_age` (default 7 d) old and the served ledger shows at least one boot after it. When a boot takes a new checkpoint, completed-but-unconsumed predecessors beyond `core.storage.migration.checkpoint_keep` are pruned; consumed ones are pruned keeping the newest `checkpoint_keep` (default 1). An open checkpoint of the live store is never pruned automatically. `.migration-checkpoint` names the checkpoint of the latest attempt and says whether it is open or completed; it is removed with that checkpoint. |
| Free space (OPS-25) | Before taking one: available space on the DB's filesystem ≥ the live store's on-disk size (`EstimateDiskUsage`) + `core.storage.checkpoint.free_space_margin` (default 2 GiB), because hard-linked files diverge as compaction rewrites them. Otherwise boot halts with the numbers. `mediacore_storage_checkpoint_bytes` reports retained checkpoints. |
| Restore (DATA-10, DATA2-19, DATA3-1) | Offline `<app> storage restore-checkpoint <name>` restores only the checkpoint of the **latest** upgrade attempt of this store (its record's `install_id` and `epoch` equal the live store's): an older checkpoint would also discard a later upgrade's work and is refused, naming the latest one. Restoring a completed attempt's checkpoint discards every write since its `taken_at` and needs `--accept-loss`, which prints that time. It performs the journaled directory swap of [05-backup](05-backup.md) §5B.3.4 directly with kind `KindCheckpoint` (sidecars rewritten from the restored stamps, derived indexes invalidated, the live directory kept as the undo point), refusing while another process holds the DB lock. It runs without booting the app, because a crash-looping new build must not be required, and its result is then usable by `make rollback` to the previous binary. Nothing restores a checkpoint automatically. |

### 5S.3.13 Deferred: per-family cutover (out of the extraction)

Moving a family from its legacy keys to a native core keyspace, and deleting the legacy rows, is **not
part of the extraction** (R3-1, 02 N15): no build of S0–S10 registers a cutover, and every core
subsystem serves its granted families through its legacy-format adapter until one does. A cutover is a
later, per-family, **one-way** step, decided on measured data (key counts, row sizes, the native
features that need a different shape; 09 decides when), run behind the boot's pre-migration checkpoint
and the single per-owner level guard. Its floor is the cutover release itself: once it ran, every older
build is refused, and only a restore of the pre-cutover checkpoint or of a pre-cutover backup goes back.
This section is its whole contract.

| Rule | Specification |
|---|---|
| Shape | A `datamigrate.DataMigration` of the **grantee** (`core/<subsystem>`), `Breaking`, `BootBlocking`, `RaisesLevel` = the cutover build's supported level for that owner, `Source` = the granted ranges it moves, `After` the highest step the granting owner registers (§5S.3.4). |
| Phases (DATA3-11, DATA3-15) | Persisted in `c/storage/dm/<id>` under the phase names 09 uses: **`folding`/`folded`** — the legacy rows and the grantee's additive and core-only data are folded into the native keyspace (one batch under `core.storage.migration.max_batch_bytes`; above it, per-item commits with a cursor in `folding`); until `raised`, legacy is authoritative and a resumed fold restarts from the legacy rows, overwriting its own native rows; **`raised`** — the sidecars first (§5S.3.7 level-step step 2), then one batch that stamps the level, records the grantee's backend as `native` in the claim ledger and records `raised`; from here the native rows are authoritative and older builds are refused; **`deleting`** — legacy rows deleted in chunks with a cursor; **`done`**. The level is always raised before the first legacy delete, so a crash mid-delete can never let an older build open a half-deleted family; a resume skips completed phases. |
| Preconditions | `RequiresSelfServed` (implicit: never on the first boot of an auto-applied release); `RequiresServed` when 09 sets one; the boot takes its checkpoint, because a cutover that will run always counts as pending (§5S.3.12). |
| After it | The granting owner drops the grant and marks the moved claims `ClaimRetired` (they drain to zero). A build that deletes the grantee's legacy-format adapter declares `UpgradeFloor{Owner: <grantee>, Requires: [<cutover>], InstallFirst: <cutover release>}`, so a store that skipped the cutover release is refused before any write and the updater refuses to apply that build (§5S.3.5). An archive taken before the cutover restores only through a build that still registers the cutover (05-backup §5B.3.4 step 4 checks the floor at staging). |
| Tests (when built) | `TestCutoverCrashAtEveryPhase` (`CrashClone` at every chunk: no row lost, no delete before `raised`), `TestCutoverRefusesOlderBuild`, `TestCutoverFloorRefusesSkippedStore`. |

## 5S.4 Lifecycle

Boot order is [06](06-app-sdk.md) §6.5.1's one phase table (R2-8); where this list and 06 differ, 06
wins. This list names storage's own steps and the 06 phase each belongs to. **Write rule:** steps 1–5
write nothing to a non-empty store; the checkpoint (step 6) is the first store write of a boot that
takes one.

```
06 P−1  updater.Boot (05-updater): may restore .prev before anything below runs; its Pending result
        and 05-health's ExtendStart callback are passed to kernel.Open
06 P0a  pre-boot restore (05-backup §5B.3.4): the swap of <state_dir>/restore/pending.json using its
        absolute targets, or the resumption of a restore journal left in flight (DATA3-6)
kernel.Open(storage)                                                   06 P2 — read only
  1 resolve path: core.storage.path from 05-config's BootView (absolute; ABO maps database_path /
    DATABASE_PATH, permanent aliases). Refuse to CREATE a store at the resolved path when none exists
    there and 05-config reports a retired or unread database-path setting (OPS-5), or when
    <state_dir>/restore/ records a failed restore that had no previous store (05-backup step x): a
    failed host-loss restore never leaves an empty fresh store behind (DATA3-9). An alias conflict on
    this ProtectOperator key has already failed P0 (05-config §1.3.5, OPS2-12). After a P0a swap the
    resolved path must equal the recorded target, else 05-backup swaps back (DATA2-25)
                                                                       fail → boot error
  2 pebble.Open without a format version (os.OpenRoot on the DB's parent directory, which need not be
    under the state directory, DATA2-19); read level stamps, the sidecars, legacy projection, marker,
    store generation and the DB directory's identity, the foreign-open identity, the served ledger's
    last record (§5S.3.7)
  3 guard: any owner's store level > build level → LevelTooNewError; the store short of a declared
    UpgradeFloor (version or Requires) → ErrUpgradeFloor             fail → boot error, nothing written
  4 validate specs: keyspaces, claims (overlap, orphans vs c/storage/claims/), grants (ownership,
    mode, exclusivity, Serialized, adapter selection vs the ledger's backends), migration DAG
    (unordered owners), data migrations                                conflict → boot error (all listed)
  5 install marker:
      empty store → FRESH INSTALL (DATA2-28, DATA3-21): ONE batch writes c/storage/install,
        c/storage/epoch, every owner's stamp at the build's supported level, an applied record for
        every registered versioned step, `done` for every registered data migration, and the claim
        ledger; the sidecars follow. A crash before that batch leaves an empty store and the next boot
        starts over; no checkpoint
      non-empty, marker absent → verify [c/, c0) and [m/, m0) empty (read only); the marker is written
        as the first write after step 6, with the install_id a restore boot hands over (05-backup
        step 8, DATA3-12) or a new one
      marker present → nothing
                                                                       06 P4a
  6 evaluate RequiresServed / RequiresSelfServed read-only; a ratchet, a step or data migration that
    will RUN, or the marker → take the attempt's checkpoint, or reuse it (§5S.3.12)
                                                                       fail → boot error, nothing written
  7 ratchet to the pinned Pebble format
                                                                       06 P4b
  8 run the DAG (§5S.3.5): books legacy steps first under the legacy-migration grant, then versioned
    steps and BootBlocking data migrations (in process, tasks.ForEach); deferrals (status listener up,
    05-health; ExtendStart per step)                                   fail → boot error; checkpoint open
  9 write c/storage/claims/<owner> (grants, backends), the served ledger (with this build's duties),
    the foreign-open identity, the store generation; rewrite .storage-levels and .storage-applied;
    clear the books marker; write completed_at on the attempt's checkpoint when every step that ran
    finished
                                                                       06 P4c
 10 issue handles to core subsystems: Keyspace, and a LegacyRange over each subsystem's grants
                                                                       06 P4d
 11 every-boot reconcilers, in 06's fixed order, each a function of its owning package over its
    handles, no service built yet (DATA2-24, COR3-2):
      (a) secrets.Open (the keyring only: no import, no derive-key retirement, which P5 decides), then
          05-config's AttachOverrides over its legacy-format adapter: DB overrides become visible, so
          (b) finds a backup directory an admin set in Settings
      (b) revocation journal (05-backup §5B.3.6): locate it from the effective core.backup.dir (moving
          it first when its recorded location differs), append the restore line of a restore boot,
          record a coverage gap after a boot without the journal duty, then re-apply the verified tail
          the store lacks through auth's and rbac's adapters (the one stored format)
      (c) post-restore hooks (05-backup §5B.3.4 step h) on a restore boot as P0a decides it, or on a
          boot that finds a replaced store (journal head below the journal's end), in the order auth,
          rbac, audit
      (d) auth.BootWrites (05-authn §5): the _system principal and expiry stamps on legacy API keys,
          written in the legacy rows through auth's grant (DATA3-18)
      (e) compat usage records buffered in P0 (09 §9.2.9)
    There is no mirror rebuild, no verification rebuild and no tasks reconcile (R3-1; 05-tasks rebuilds
    stale run records inside Executor.Run)
                                                                       06 P6
 12 issue handles to started modules (module.StorageHandles)
                                                                       06 P7
 13 derived indexes: declarations, hooks and build kinds validated (§5S.3.9)
                                                                       06 §6.5.2
 14 Run start step (3a): derived version/generation check, Load, c/storage/derived/<module>/<name>;
    build runs started where needed. Run step 5 (task executor): Online data migrations; index
    reconciler component after module hooks
 15 after readiness: self-served record (first_ready ends the upgrade attempt; confirmed_at);
    checkpoint consumption (§5S.3.12)
App.Run ends → executor drained (05-tasks) → handles invalidated → gate drain (core.storage.close_timeout)
             → WAL sync → engine close (GatedDB semantics, pebble_gate.go:600-680)
```

Until 09 S2b the kernel runs without `storage` (06 §6.5.1 "Stages"): books opens its store and runs its
own migrations as HEAD does, and only 09 S0's guard writes `c/storage/install`, the served ledger, the
foreign-open identity and the two sidecars, with the encodings of §5S.3.3 and §5S.3.7.

A disabled module (G1) still has its claims validated and recorded in the claim ledger, its grants in
force and its keyspace reserved; it gets no handle, runs no migrations, and its data is retained. Its
pending migrations run the next time it is enabled; steps of other owners that declared `After` on them
wait (§5S.3.5).

## 5S.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Any owner's level (stamp or sidecar) above the build's | closed (`LevelTooNewError`) | An older build would misread newer data (kept from `pebble_store.go:670-680`, generalised per owner). |
| Store level below the build's, or books marker present | neither: the build's pending level step runs before any service starts; a build without it has a lower supported level and is refused by the row above | A half-converted store must never serve (DATA-20). |
| Store short of a declared `UpgradeFloor` (version or `Requires`) | closed (`ErrUpgradeFloor`, names the release to install first and the missing steps), before the checkpoint; the same check refuses the binary before it is applied (`CheckReader` `upgrade_floor`, updater `StoreState`) | The code its pending steps need is gone; failing half-way, or applying a build the next boot refuses, would be worse (R2-4, DATA2-2, DATA3-10, OPS3-7). |
| Level step (books' physical raise, a cutover) whose `RequiresServed` or `RequiresSelfServed` does not hold | neither: deferred, boot continues, health `storage_step_deferred` / `storage_step_pending` | A one-way step must not run on an unconfirmed or too-young build (OPS-2, OPS2-2). |
| Foreign open detected (sidecar identity changed) | open (logged; every level's served run restarts; the previous boot's duties count as none) | An older binary served the store; clocks that measure "served since" and duties that must hold continuously restart (DATA2-9). |
| A sidecar unreadable or missing at open | open (rewrite from stamps and bookkeeping) + error log | Stamps and bookkeeping are authoritative; kept from `pebble_store.go:651-660`. Before open (`CheckReader`, `StoreState`) the same condition is closed (`sidecar_unreadable`). |
| Level-step batch commit returns an error | closed (step fails); sidecars rewritten from stamps first | A failed raise must not block rollback (OPS-26b). |
| Claim conflict, cross-owner overlap without `Except`, orphan claimed without `TakesOver`, keyspace collision, zero-valued `MigrationCompat`/`Mode`/`Parallelism`/`GrantMode`, migration cycle or unordered owner | closed (boot) | Ownership ambiguity is a data-corruption risk; registration time is the cheapest place to catch it. |
| Grant on a range the declarer does not own, grant to a module, a range granted exclusively and also elsewhere, a shared read-write grant on a range that is not `Serialized` | closed (boot; `ErrGrantNotOwned`, `ErrGrantConflict`, `ErrGrantNotSerialized`; 06 `ProblemGrant`) | A grant is the only way a core service reaches a books family; it must be exact and reviewable (R3-1). |
| A subsystem's adapter selection differs from the store's recorded backend | closed (`ErrGrantMissing`) | A subsystem must never silently switch between its legacy rows and an empty native keyspace. |
| Keys found in `c/` or `m/` on first boot | closed (boot), with sample | Proves the static collision check; an unknown writer must be investigated, not overwritten. |
| Read or write outside keyspace, claims or grants | closed (`ErrOutsideKeyspace`) | R8, reads included (DATA-17). Shadow mode (count + log, allow) exists only for books' `LegacyRange` for one release (§5S.9.4). |
| Write against the claim's or grant's mode (`ReadOnly`; `Set` on `AgingOut`/`Retired` or through `GrantReadDelete`; any write through `GrantRead`; an owner's runtime write to an `Exclusive` range) | closed (`ErrClaimMode`) | Adapters must not resurrect retired data; an exclusive grantee's invariants hold only if it is the one writer. |
| Books legacy step writes an exclusively granted range | open (legacy-migration grant): logged once per step and family, counted | Books' steps 1..66 predate core and run before any grantee holds a handle (R2-4). |
| Precondition fails at commit | closed (`ErrConflict`, nothing applied) | Optimistic concurrency; the caller re-reads and retries. |
| Migration bookkeeping unreadable | closed (boot) | Replaces the fail-open at `migrations.go:582`. |
| Versioned step error / `ErrMigrationTooLarge` | closed (boot), batch discarded, earlier steps committed, checkpoint open; no automatic restore | G2; the offline `storage restore-checkpoint` is the restore path (DATA2-19). |
| Books legacy step error | closed (boot); partial writes are idempotent and replayed next boot | Existing ABO contract (`TestMigrationUpFunctionsAreIdempotent`). |
| Checkpoint fails (cross-device, too little space) | closed (boot) before anything is written | Without the checkpoint there is no restore point; `ensureSameDevice` rationale `backup.go:557-598`. |
| A checkpoint of another store, another attempt, or one a failed restore left | never reused; `restore-checkpoint` refuses all but the latest attempt's | Reusing or restoring it silently rolls the store back by days (DATA3-1, OPS3-14). |
| Unknown `Breaking` step on disk | closed (`ErrKeyspaceTooNew`) | Secondary to the level check. |
| Unknown `Additive` step on disk | open + warning | G5: previous release keeps working. |
| BootBlocking data migration fails `Verify` | closed (boot), state `failed`, held list kept | Same as B6a "stop states". |
| Online data migration fails | open (app serves through the read adapters); task `failed`; health `degraded` | Old and new rows are both readable while it is incomplete. |
| Cutover crashes mid-way | resume from its persisted phase; nothing is deleted before `raised` | A delete ahead of the raise would let an older build open a half-deleted family (§5S.3.13). |
| Full-text index open fails | open (`Degraded`; callers fall back) | Kept from `search/register.go:38-60`. |
| Index dirty mark fails | closed (the whole entity batch fails) | It is in the same batch; partial commit impossible. |
| Derived index `Load` rejects (generation, own check, or a boot without the `gen` duty since the artifact) | open (rebuild) | Snapshot is an accelerator only. |
| `Get` on missing key | `ErrNotFound` (not an error log) | Normal control flow. |
| Iterator/scan I/O error | yielded once, iteration stops | Callers must check; `iter.Seq2` makes it visible at the call site. |
| Close timeout with leases outstanding | closed (engine not closed; error returned; process exits) | Closing under a lease is use-after-free in Pebble; kept from `pebble_gate.go:600-680`. |

## 5S.6 Concurrency contract

- `DB`, `Keyspace`, `LegacyRange`, `Snapshot`, `Sequence`, `Derived`, `index.Index` are goroutine-safe.
  `Batch` and `Iterator` are **not**: one goroutine each (as in Pebble).
- Every call takes `context.Context`; a cancelled context fails the call before engine work starts.
  Engine calls themselves are not interruptible; scans check `ctx.Err()` every step.
- Every engine call holds a gate lease (the `GatedDB` design moves into `internal/pebble`); `Close`
  waits up to `core.storage.close_timeout`.
- **Commit stripes** are taken only inside `Commit`, in ascending order, for keys in `Serialized`
  ranges; preconditions are evaluated under them. No lock, lease or reservation is held for an open
  batch (§5S.3.2). Pebble gives no snapshot isolation to indexed batches; correctness for
  read-modify-write comes from `Expect`, not from reading inside a batch.
- **Commit order is not key order.** Two batches may commit in either order regardless of the keys or
  `Sequence` values they carry. Consumers that need commit order observe commits (05-events intake).
- `Sequence` refills are serialised by a per-sequence mutex and made durable before use; `Next` is
  lock-free inside a lease.
- Snapshot and iterator lifetimes pin memtables/SSTs. Rule: a pass longer than
  `core.storage.snapshot_max_age` (default 30 s, the same refresh interval as the exact census,
  `census_exact.go:40-45`) re-opens its iterator from its last key; the data-migration runner (a
  cutover's fold included) and the census do so; `mediacore_storage_snapshot_oldest_seconds` alarms on violations.
- Data migrations and index rebuilds are bounded worker pools sized by a declared `tasks.Parallelism`
  over **disjoint key ranges** from a persisted plan; nothing is shared between shards except counters
  (atomics). An `Online` migration over a `Serialized` range commits per item with preconditions, so
  it never overwrites a concurrent owner write (DATA-13).
- Batch size: `Commit` refuses batches over `core.storage.max_batch_bytes` (default 256 MiB) with
  `ErrBatchTooLarge`, so a runaway writer cannot stall the WAL.
- Tests use `testing/synctest` for snapshot-age and lease-drain timing, and `vfs.NewCrashableMem`
  + `CrashClone` for crash points (pebble v2 `vfs/mem_fs.go:61,140`).

## 5S.7 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.storage.path` | string (absolute path) | `<state_dir>/storage/main.pebble` (compiled default); ABO maps `database_path` / `DATABASE_PATH` (permanent aliases, 05-config); the one source of the DB location (COR2-8) | no |
| `core.storage.cache_bytes` | int64 | 512 MiB | no |
| `core.storage.memtable_bytes` | int64 | 64 MiB | no |
| `core.storage.max_batch_bytes` | int64 | 256 MiB | yes |
| `core.storage.close_timeout` | duration | 30s | yes |
| `core.storage.snapshot_max_age` | duration | 30s | yes |
| `core.storage.sequence.lease` | int | 64 | no |
| `core.storage.claims.mode` | enum `strict`/`shadow` | `strict` (books' first release on core: `shadow`, 09) | no |
| `core.storage.claims.violation_flush` | duration | 1m | yes |
| `core.storage.checkpoint_dir` | string (path) | `<db parent>/.mediacore-checkpoints` (must be on the DB's filesystem) | no |
| `core.storage.checkpoint.free_space_margin` | int64 | 2 GiB | no |
| `core.storage.migration.max_batch_bytes` | int64 | 64 MiB | no |
| `core.storage.migration.max_batch_ops` | int | 250000 | no |
| `core.storage.migration.checkpoint_min_age` | duration | 168h | yes |
| `core.storage.migration.checkpoint_keep` | int (completed or consumed checkpoints kept) | 1 | yes |
| `core.storage.self_served_min` | duration | 1h (the floor is `max(core.updater.confirm_after, this)`) | no |
| `core.storage.data_migration.chunk_bytes` | int64 | 4 MiB | yes |
| `core.storage.data_migration.chunk_items` | int | 2000 | yes |
| `core.index.reconcile.workers` | int | `runtime.NumCPU()` | yes |
| `core.index.reconcile.interval` | duration | 5s (in-process component, not a task schedule) | yes |

Environment overrides for Pebble open settings (TASK-A7's intent) are the generic env layer of
[05-config](05-config.md); none of these keys is DB-persisted (they are read before the DB opens), so
every one is `ProtectOperator` (file, env or flag only). The round-2 adoption caps
(`core.storage.adopt.max_atomic_bytes`/`_keys`) are gone with the mirrors (R3-1); a cutover's fold uses
`core.storage.migration.max_batch_bytes` and the data-migration chunk keys (§5S.3.13).

## 5S.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_storage_ops_total` | counter | `keyspace`, `op` (get, set, delete, scan, commit) |
| `mediacore_storage_op_duration_seconds` | histogram | `op` |
| `mediacore_storage_batch_bytes` | histogram | `keyspace` |
| `mediacore_storage_keyspace_bytes` | gauge (estimate, refreshed 5 min) | `keyspace` |
| `mediacore_storage_claim_violations_total` | counter | `owner`, `op` (read, write), `mode` (refused, shadow) |
| `mediacore_storage_grant_ops_total` | counter | `grantee`, `family`, `op` (get, scan, set, delete) |
| `mediacore_storage_conflicts_total` | counter | `keyspace` |
| `mediacore_storage_snapshots_open` | gauge | — |
| `mediacore_storage_snapshot_oldest_seconds` | gauge | — |
| `mediacore_storage_migrations_applied_total` | counter | `owner` |
| `mediacore_storage_migration_duration_seconds` | histogram | `owner` |
| `mediacore_storage_data_migration_items_total` | counter | `migration`, `outcome` |
| `mediacore_storage_data_migration_state` | gauge (enum as 0/1 per state) | `migration`, `state` |
| `mediacore_storage_level` | gauge (the one level metric; 09's `mediacore_compat_level` is not a second name, OPS2-24) | `owner`, `kind` (store, supported) |
| `mediacore_storage_legacy_grant_writes_total` | counter | `step`, `family` |
| `mediacore_storage_checkpoint_bytes` | gauge | `state` (open, completed, consumed) |
| `mediacore_storage_pebble_*` | (the existing Pebble collector's series, renamed) | as today (`internal/metrics/pebble_collector.go`) |
| `mediacore_index_docs` | gauge | `module`, `index` |
| `mediacore_index_dirty` | gauge | `module`, `index` |
| `mediacore_index_reconcile_lag_seconds` | gauge | `module`, `index` |
| `mediacore_storage_derived_state` | gauge | `name`, `state` |

`keyspace` and `owner` cardinality is bounded by the number of modules + core subsystems (< 40);
`grantee` and `family` by the grant declarations (< 40).

## 5S.9 Migration from ABO

### 5S.9.1 Ownership of the 258 families and the grants (normative, single table)

This is the **only** statement of who owns each legacy family, in which mode, and which core subsystem
reaches it through which grant (DATA-2). 04, [05-tasks](05-tasks.md) §5T.6, [05-logging](05-logging.md),
[05-authn](05-authn.md), [05-config](05-config.md), [05-ai](05-ai.md), [05-metadata](05-metadata.md),
[05-library](05-library.md) and [09](09-abo-on-core.md) reference it instead of restating. Counts are
from `keyfamilies.go:37-345`.

The table is **the same at every stage of the extraction** (R3-1): books holds every family in the
mode shown, no claim changes owner or mode, and nothing is copied. A row's grant is in force from the
stage in which the grantee subsystem lands on core storage (the stage shown is 09's; 09 is
authoritative) until that family's cutover (§5S.3.13), which is outside the extraction. "Excl." marks
an `Exclusive` grant: from that stage books' runtime handle only reads the range. Exact keys and
sub-prefixes inside a books family that a grant names are books' own same-owner nested claims where
they need their own mode or `Serialized` flag; there are no cross-owner carve-outs in ABO's claim set.

| # | Families / exact keys | Families | Owner and mode | Grant (grantee, mode, stage) | Native-only data and notes |
|---|---|---|---|---|---|
| 1 | `u:` + `idx:user:` | 1 | books RW, Serialized | `core/auth` ReadWrite, Excl. (S4) | legacy `User` rows and their index rows; additive members (`restriction`, `password_reset_required`) with defaults, every restriction also in a field older builds read (05-authn §11) |
| 2 | `role:` + `idx:role:name:` | 1 | books RW, Serialized | `core/rbac` ReadWrite, Excl. (S4) | native permissions as namespaced strings in the role's list; an older build matches by exact string, so they grant nothing there (`internal/server/middleware/auth.go:430-455`; 05-rbac) |
| 3 | `apikey:` + `idx:apikey:` | 1 | books RW, Serialized | `core/auth` ReadWrite, Excl. (S4) | namespaced scopes; a key this build writes never has an empty scope list (empty = all role permissions in older builds, `auth.go:405-408`; 05-authn §4.4) |
| 4a | `invite:` + `idx:invite:username:` | 1 | books RW | `core/auth` ReadDelete, Excl. (S4) | legacy invites read in place by presented token, deleted on accept, expiry or restore; never written; new invites are hash-only in the core-only `c/auth/` and void on an older build (`internal/database/store.go:1004-1012`, DATA2-8) |
| 4b | `inviteallow:` | 1 | books RW, Serialized | `core/auth` ReadWrite, Excl. (S4) | — |
| 5 | `oauthid:` + `idx:oauth:` | 1 | books RW, Serialized | `core/auth` ReadWrite, Excl. (S4) | — |
| 6 | `sess:` + `idx:sess:user:` | 1 | books RW | `core/auth` ReadDelete, Excl. (S4) | read and deleted on upgrade-on-use, expiry or restore, never written (SEC-19); new sessions are hashed in the core-only `c/auth/`: a rollback costs one sign-in (05-authn §11) |
| 7 | exact keys `setting:config_blob` (Serialized) and the four legacy secret rows `setting:openai_api_key`, `setting:google_books_api_key`, `setting:hardcover_api_token`, `setting:basic_auth_password` | — | books RW (inside row 16; same-owner exact-key claims) | `core/config` (the kernel's `configstore`): ReadWrite on the blob, ReadDelete on the four secret rows (S2b reads, S3 writes) | keys with no legacy name live in the core-only `c/config/`; a secret the new build writes goes to `c/secrets/` and is blanked in the blob or deleted from its legacy row (rollback = re-enter it, stated in the release note); nothing deletes the blob during the extraction (05-config C4, §1.12.2; DATA3-3) |
| 8 | exact keys `setting:bootstrap_token_hash`, `setting:bootstrap_token_expires_at` | — | books RW (inside row 16) | none | core's bootstrap hash is the core-only `c/auth/bootstrap`, regenerated at every start; an older build regenerates its own rows (05-authn §4.1) |
| 9 | sub-prefix `setting:scheduled_task_last_tick.`; exact keys `setting:library_scan_full_last_run`, `setting:maintenance_window_last_run`, `setting:registry.operations_paused` | — | books RW (inside row 16) | `core/tasks` ReadWrite, Excl. (S5) | clocks and the pause marker in place; native-only clock fields in the core-only `c/tasks/sched/` (05-tasks §5T.6) |
| 10 | exact keys `preference:storage_format`, `preference:storage_migration` | — | books RW (inside row 16), written only through `LegacyGuardProjection.Stage` (§5S.3.7, DATA2-22) | none | — |
| 11 | ops run families: `opv2:act:`, `opv2:op:`, `opv2:state:`, `opv3:run:`, `opv3:prog:`, `opv3:ckpt:`, `op:batch:`, `opv3:sched:` | 8 | books RW | `core/tasks` ReadWrite, Excl. (S5) | runs stored in place in HEAD's format (the `opv3:` record and its paired `opv2:` rows in one batch), one row per run, never copied; native-only fields additive with defaults or in `c/tasks/` (05-tasks §5T.6) |
| 12 | v1 `opstate:`, `op_result:`, `opsummary:`, `operation:` | 4 | books RW: live books writers — `opstate:` (`internal/operations/state.go:59-74`), `op_result:` (`CreateOperationResult`, `internal/database/pebble_store_operations.go:438`, 15 call sites, e.g. `internal/server/metadata_ops.go:391`), `opsummary:` (`SaveOperationSummaryLog`, `:256`, e.g. `internal/maintenance/jobs/backfill_book_files.go:197`), `operation:` (`UpdateOperationStatus`, `:138`, `internal/operations/registry/legacy_op_status.go:244`) | `core/tasks` Read (S5) | read by the v1 history reader and merged into `GET /runs`; AgingOut once 05-tasks' `TestLegacyOpsWriters` ratchet reaches zero writers (DATA-15, DATA2-12) |
| 13a | `op:completion:`, `op:deprev:` (`Requires` stamps) | 2 | books RW | `core/tasks` ReadWrite, Excl. (S5) | read and written in place (library-scale, never copied); stamps of subjects with no legacy type in `c/tasks/req/` (05-tasks §5T.6; OPS2-9) |
| 13 | all other ops families: v1 `operationlog:`; `op:`; `opv2:` and its other 10 children; `opv3:` and its other 15 children | 29 | books RW | `core/tasks`: ReadWrite, Excl. on the `op:`, `opv2:`, `opv3:` families (queues, indexes, trails, pages, Fixer rows); ReadDelete on `operationlog:` (S5) | written in place by the same adapter as row 11; terminal history deleted only by retention (05-tasks §5T.6) |
| 14 | `syslog:` | 1 | books RW (HEAD's `StandardLogger` writes it until the journal is the only writer, 05-logging) | `core/log` ReadDelete (S5: its only reader, the journal adapter in `core/internal/logjournal`, imports `tasks` and lands at S5, and P2's `ProblemGrant` refuses a grant whose grantee has no legacy-format adapter in the build) | read in place by the journal query adapter, retention-deleted in chunks; new log records in the core-only `c/log/journal/`; no `core/log` step and no level stamp (05-logging; DATA3-14 is resolved by R3-1) |
| 15 | `act:` (+12 children) | 13 | books RW: books' activity feed (05-logging) | none | core audit writes only new rows in the core-only `c/audit/` |
| 16 | `setting:` (+`repairs_last_apply_op:`, `repairs_last_plan_op:`), `preference:`, `pref:` (+`pref:_system:` and its 6 children) | 12 | books RW: runtime markers, per-book and per-user state, migration bookkeeping (`preference:db_version`, `preference:migration_<n>`; resolution 7) | only the exact keys and sub-prefix of rows 7 and 9 | core config overrides with no legacy name are rows in `c/config/` (DATA-1) |
| 17 | `guestcftoken:` | 1 | books RW: guest tokens stay in ABO (09 S4) | none | — |
| 18 | `idx:` (remainder) | 1 | books RW | sub-prefix `idx:sidx:dirty:` → `core/index` ReadWrite, Excl. (S7) | the full-text dirty set stays in HEAD's format, so both builds drain one set (§5S.3.8) |
| 19 | everything else, incl. `counter:` and the bare keys `narrator_counter`, `sync_alias_use_seed_cutoff` (exact-key claims) | 180 | books RW; same-owner children `book:hash:`, `book:organizedhash:`, `book:originalhash:`, `library:` Retired; `book:author:`, `book:series:` AgingOut | `import_path:` (Serialized: books' scan statistics and library's root edits both read-modify-write the row) and exact key `counter:import_path` → `core/library` ReadWrite (S7); `aijob:`, `aijob_batch:`, `aijob_payload:` → `core/ai` ReadWrite, Excl. (S7); `provider_throttle:`, `provider_daily_budget:` → `core/providerhttp` ReadWrite, Excl. (S7) | library: native-only root fields in `c/library/` (05-library); ai: native-only job fields in `c/ai/batch/` (05-ai); providerhttp: holds and counters of non-metadata scopes in `c/providerhttp/` (05-metadata) |

Total: 7 (rows 1–6) + 8 + 4 + 2 + 29 + 1 + 13 + 12 + 1 + 1 + 180 = **258**. The ops group is 43
families: v1 `operation:`, `operationlog:`, `op_result:`, `opstate:`, `opsummary:` (5), `op:` and 3
children (4), `opv2:` and 13 children (14), `opv3:` and 19 children (20) = rows 11 (8) + 12 (4) + 13a (2)
+ 13 (29). `met:`, `aijournal:` and `system:census:` are generic-looking but stay books claims with no
grant until their owning spec ([05-metrics](05-metrics.md), [05-ai](05-ai.md)) changes this table;
`system:census:` is a recomputable cache (core's census writes `c/storage/census/`).

The books claim list is **generated** from `keyfamilies.go` into `internal/books/storage_claims.go`
(ABO side) by a `go generate` tool that skips the reserved `c/` and `m/` entries and emits every row as
books' claim in the mode above — the same for every build of the extraction; the grants are declared
beside it by hand in `internal/books/storage_grants.go` and pinned by `storage_grants.golden`
(§5S.3.11). A test checks that the generated claims and the declared grants agree with this table,
counting by group rather than against a literal 258 (DATA-28), and 09's `TestBooksClaimsGenerated`
asserts which grants each stage's build declares. `keyfamilies.go` stays ABO code: core holds no
audiobook key name (R1, 03 §3.4). The census **mechanism** moves into core as `storage.Admin.Census`,
which partitions by claims + keyspaces and shows each claim's grants: `census.go` and
`census_families.go` take the family list as input and hold no key name, and so does `census_exact.go`
except its `book_ver:` special case (`census_exact.go:169,545`), which stays books code behind a census
hook. So `GET /api/core/v1/storage/census` replaces the books endpoint and keeps its shape (DATA2-29, 04).

### 5S.9.2 Adapter, read in place, or core-only

Production counts are unknown to this spec. The design does not depend on them: nothing is copied, so
size decides only how a grantee pages its reads, never whether a step fits in a batch.

| Group | Expected size (reasoning from code) | Decision | Why |
|---|---|---|---|
| users, roles, API keys, the invite allow-list, OAuth identities (+ their `idx:` rows) | one row per user/key/role/entry/identity on a single-tenant home install (D7): tens to hundreds | **legacy-format adapter**, read and written in place (`core/auth`, `core/rbac`) | one format: the previous build reads every identity row this build wrote; native-only data additive or core-only (05-authn §11, 05-rbac) |
| sessions `sess:`, legacy invites `invite:` | bounded by active logins and open invites (72 h TTL); a 10-minute cleanup removes expired sessions (`server_lifecycle.go:529-530`) | **read and delete in place**; new ones hashed in the core-only `c/auth/` | raw bearer tokens and raw invite tokens are never copied or written again (SEC-19, DATA2-8); rollback = re-login, voids invites issued by this build |
| `setting:config_blob`, the four legacy secret rows | one blob row + four secret rows | **legacy-format adapter** (`core/config`); keys with no legacy name in `c/config/`; secrets this build writes in `c/secrets/` | 05-config C4; rollback = re-enter the secrets this build moved |
| the rest of `setting:`, `preference:`, `pref:` | `setting:` holds per-file and per-job markers (one `transcode_skip_<hash>` per file); `preference:` holds per-book state; `pref:` per-user books data | **books family**, no grant beyond rows 7 and 9 | they are not config; a broad grant would hand library-scale book state to core (DATA-1) |
| ops runs, queues, trails, pages, clocks, `Requires` stamps | small per run; pages and stamps library-scale | **in place** through the `core/tasks` adapter | one row per run, never in two stores, so no build can double-execute it (05-tasks §5T.6) |
| v1 ops history and live v1 families | bounded by retention | **read in place** (v1 reader); live books writers ratcheted to zero | the v1 writers move job by job (05-tasks) |
| activity `act:*` | tiered, retention-pruned, includes the debug tier | **books family** | books' domain feed (05-logging) |
| `syslog:` | retention-pruned (`log_retention_days`) | **read and retention-deleted in place** by core log; new records in `c/log/journal/` | append-only; a rollback does not show journal records made after the upgrade, which 05-logging states |
| `import_path:` | tens | **legacy-format adapter** (`core/library`) | the previous build keeps every import root this build added (05-library) |
| AI job rows, provider holds and daily counters | open jobs; one row per provider | **legacy-format adapters** (`core/ai`, `core/providerhttp`) | an older build finishes an open job and honours every hold after a rollback (05-ai, 05-metadata) |
| search dirty set `idx:sidx:dirty:` | bounded by pending re-index work | **in place** (`core/index`) | both builds drain one set (§5S.3.8) |

**Read-in-place procedure** (history families, `syslog:`): the grantee receives a `LegacyRange` over
its `GrantRead`/`GrantReadDelete` ranges and a `ReadAdapter` that decodes legacy rows into its types for
list, detail and census views; retention deletes legacy rows through the grant. Where the grantee keeps
new data in a core-only family instead (the log journal), a rollback does not show that data, which the
grantee's spec states.

**Read-only command for the owner** (gives per-family key counts and bytes; the estimated census
reads sstable properties and an in-memory cache only, `census.go:317-372`):

```bash
curl -sS -H "Authorization: Bearer <redacted-token>" \
  "https://<host-a>/api/v1/diagnostics/db-census?fresh=true" \
| jq '.data.families[]
      | select(.prefix | test("^(u:|role:|sess:|apikey:|invite|oauthid:|guestcftoken:|idx:|setting:|preference:|pref:|act:|syslog:|operation|op_result:|opstate:|opsummary:|op:|opv2:|opv3:|import_path:|aijob|provider_)"))
      | {prefix, keys, raw_key_bytes, raw_value_bytes, disk_bytes}'
```

`idx:` is one family in the census, so its sub-ranges are not split; they are bounded by their parent
rows. Do **not** use `diagnostics query --raw` for this: it prints values, and session rows hold bearer
credentials.

### 5S.9.3 Code moves

| ABO code | Becomes |
|---|---|
| `internal/database/pebble_gate.go` (`GatedDB`, leases, drain/seal) | `core/internal/pebble` gate |
| `storage_format.go`, `pebble_store.go:597-700` (open/init split, format pin, sidecar) | `core/internal/pebble` guard (§5S.3.7) + kernel; ABO copies deleted |
| `keyfamilies.go` | stays ABO: the source of the generated books claim list (§5S.9.1) |
| `census.go`, `census_families.go`, `census_exact.go` | core `storage.Admin.Census` (the mechanism holds no key name); the `book_ver:` special case stays books behind a census hook (DATA2-29) |
| `migrations.go` runner + `migration_bookkeeping.go` | core DAG runner; books' 66 migrations become `LegacyMigration`s 1..66 over the version-source adapter (non-atomic, idempotent, DATA-8), run first under the legacy-migration grant; every later books step is a `Migration` or `DataMigration`; the 66 steps and the store methods they call (incl. `MigrateOpsV2ToV3`) are deleted only by a build that declares books' `UpgradeFloor{Version: 66, InstallFirst: <09's minimum-upgrade-from release>}` (R2-4) |
| identity, ops, config-blob, import-path, AI-job, provider-hold and search-dirty store code (`pebble_store_auth.go`, `pebble_store_ops_v2.go`/`_v3*.go`, `persistence.go`'s blob path, `pebble_store_importpaths.go`, `pebble_store_aijobs.go`, `provider_throttle.go`/`dailyquota.go`, `pebble_store_search_dirty.go`) | the grantees' legacy-format adapters over their granted `LegacyRange` (05-authn, 05-rbac, 05-tasks, 05-config, 05-library, 05-ai, 05-metadata, `storage/index`), keeping HEAD's encodings |
| `ReconcileOpsV3WithV2` boot step (`store.go:1862-1870`) | 05-tasks' legacy store, inside `Executor.Run` (no P4d step) |
| `NewPebbleStore(path)` / `InitializeStore` / `globalStore` | books module constructor `database.NewPebbleStore(h module.StorageHandles)`; `globalStore` deleted |
| `internal/database` call sites of `p.db.*` (`GatedDB` method set) | a thin ABO adapter `internal/database/legacyrange.go` mapping the used subset (Get/Set/Delete/DeleteRange/NewIter/NewBatch/NewIndexedBatch/NewSnapshot/LogData) onto `storage.LegacyRange`; admin calls (Checkpoint, Compact, Metrics, SSTables) move to core `storage.Admin` users (backup, health, metrics) |
| ad-hoc identity mutexes (`inviteMu`, `apiKeyMu`, `sessionWriteMu`, `userMergeApplyMu`) | `Serialized` claims + `Expect` preconditions; the user merge's auth half becomes the `auth.Admin.StageUserMerge` participant (DATA-18) |
| `nextID` / `counter:` | ABO keeps `counter:<name>` (claimed) for existing entity IDs; a grantee that creates a legacy row allocates its ID from the same counter through an exact-key grant; new core rows use `storage.Sequence` |
| `internal/search` (bleve index, reconciler, dirty set) | `core/internal/bleveindex` + `storage/index` (the books index keeps its legacy dirty set, §5S.3.8); books keeps document building and query parsing |
| `MemStore`, `HNSWEmbeddingStore`, `ChromemEmbeddingStore` | stay in books; registered as `DerivedIndex` with a `Derived` generation handle |
| `internal/openlibrary` OL store | books, opened via `OpenAuxiliary(…, LegacyPath: <openlibrary-dumps>/oldb)` |
| `ai_scan_store.go` standalone constructor | deleted |

### 5S.9.4 Rollout order (summary; 09 owns the stages)

1. Core `storage` lands with zero modules (G7 harness); `coretest` opens an in-memory DB.
2. Books moves onto `LegacyRange` with claims in **shadow** mode for one release; every
   `mediacore_storage_claim_violations_total` hit (reads and writes) is fixed by registering or
   correcting the access. ABO's full test suite also runs in strict mode in CI from the start.
   Because the counter resets on every restart, shadow and strict violations are also **persisted**:
   `c/storage/claimviol/<owner>/<op>/<family>` = `{first_at, last_at, count, mode, build}`, where
   `<family>` is the key's longest registered family prefix (or the first 16 bytes, hex, for an
   unregistered key — never a value), written by one coalescing writer at most once per
   `core.storage.claims.violation_flush` per entry and never inside the caller's batch. The census
   (`c/storage/census`) and `GET /api/core/v1/storage/claim-violations` (`core:system.manage`) list
   them; 09 S2b's done-check ("zero persisted violations ≥ 14 days") reads `last_at`. Entries are
   kept until a strict-mode release has run 14 days with none newer.
3. Strict mode. Then each grant lands with the stage that moves its grantee (§5S.9.1, 09); no stage
   hands a claim over, copies a family or raises a core level. Cutovers are outside the extraction
   (§5S.3.13).

## 5S.10 Tests

| Test | Proves |
|---|---|
| `TestKeyspaceWritesCannotEscapePrefix` | relative keys always land under `Prefix()` incl. `Sub`, `DeleteRange` bounds |
| `TestLegacyRangeRefusesForeignKeys` | R8 for absolute keys on reads and writes, longest-match resolution, carve-outs, scan clamping |
| `TestSameOwnerNestedClaimsSelectMode` | books' nested pairs boot; the longest match selects the child's mode |
| `TestClaimOverlapFailsBoot` / `TestClaimCarveOutAccepted` | cross-owner rules, multi-owner conflicts listed together |
| `TestOrphanedClaimNeedsTakeover` | claim ledger; removed module's claims not silently re-owned |
| `TestABOClaimsMatchOwnershipTable` | generated books claims and declared grants equal §5S.9.1 by group; none touches `m/`, `c/`; the claims are identical for every stage's build |
| `TestS2bBooksWritesEveryFamily` | the S2b claim set with books writing every family: zero violations in shadow and strict mode; with each stage's exclusive grants in force, zero books runtime writers of those ranges |
| `TestGrantEnforced` | a grantee's handle reaches exactly its granted ranges in its mode (`ErrOutsideKeyspace`, `ErrClaimMode`); scans clamped; an `Exclusive` grant makes the owner's runtime handle read-only on the range; the legacy-migration grant still writes it and is logged and counted |
| `TestGrantValidation` | grant outside the declarer's claims, grant to a module, conflicting exclusive grants and a shared read-write grant on a non-`Serialized` range each fail boot; a grant of a disabled module stays in force |
| `TestGrantsLoggedAndRecorded` | one boot-log line per grant; grants and backends in the claim ledger and the census; `mediacore_storage_grant_ops_total` counts operations |
| `TestAdapterSelectionMatchesLedger` | a build whose subsystem would switch between legacy adapter and native keyspace against the store's recorded backend fails with `ErrGrantMissing` |
| `TestGrantedFamilyGolden` | HEAD fixtures of every granted family decode and re-encode byte-identically through the grantee's adapter (`codec.LegacyJSON`) |
| `TestOneFormat_PrevBuildReadsGranteeWrites` | rows a grantee wrote (a user, a key with namespaced scopes, a role, a config value, a run, an import root) are read correctly by the previous release's store code; a key's stored scopes are never empty |
| `TestFirstBootRefusesForeignKeys` | runtime `ErrForeignKeys` |
| `TestS2bOpensStoreWrittenByS0` | a store with S0's install marker, served ledger and sidecars opens under core storage, ledger history readable (DATA2-6) |
| `TestFreshInstallStampsLevelsAtomically` | an empty store gets marker, store generation, every owner at the build's level, every registered step applied and data migration done, in one batch; `CrashClone` before it leaves an empty store (DATA2-28, DATA3-21) |
| `TestRestoreBootInheritsInstallID` | a pre-S0 archive restored onto its install gets the pre-swap `install_id`, not a new one (DATA3-12) |
| `TestCheckpointPrecedesEveryWrite` | on an upgrade boot the checkpoint holds no `c/` row written by that boot (marker, ledgers, reconcilers) (R2-8) |
| `TestPostDAGReconcilerOrder` | reconcilers (a)–(e) run in 06's order after the DAG and handle issue, before any listener; the journal re-apply sees overrides and the keyring (DATA2-24, COR3-2) |
| `TestBatchAtomicAcrossBoundKeyspaces` | outbox/dirty-mark/participant writes in one commit; `CrashClone` shows all or nothing |
| `archtest.NoBatchByValue` | no core API or spec Go block takes or holds a `storage.Batch` by value (COR2-12) |
| `TestExpectConflictAppliesNothing` (`-race`) | concurrent writers on a `Serialized` key: one wins, the other gets `ErrConflict`, no lost update |
| `TestLeakedBatchBlocksNobody` | an unclosed batch with preconditions holds no stripe; other commits proceed |
| `TestUserMergeAtomicAcrossOwners` | books + auth participant (auth's granted handle) in one batch; `CrashClone` at every point |
| `TestSequenceNeverReusesAfterCrash` | crash at every point between refill and consuming commits; no ID reissued |
| `TestSequenceAdvancePast` | after `AdvancePast(n)` every ID is > n, across a crash (DATA2-16) |
| `TestJoinOrderMatchesSegmentOrder` | tuple-encoding order property over random segment lists |
| `TestMigrationStepAtomicWithBookkeeping` | crash at every point of a versioned step: nothing, or step+version+record |
| `TestLegacyMigrationsReplayIdempotent` / `TestNoNewLegacyMigrations` | books 1..66 semantics; ratchet at 66 |
| `TestLegacySteps59to66RunFirst` | on a store at books v58, steps 59–66 run before every core step; their writes into exclusively granted ops families go through the legacy-migration grant, logged and counted; boot succeeds (R2-4, DATA2-2) |
| `TestUpgradeFloorRefusesBeforeAnyWrite` / `TestUpgradeFloorRequiresDoneStep` | a store below a declared version floor, or missing a `Requires` step, is refused with `ErrUpgradeFloor` naming the release and the steps; the store is byte-identical (DATA3-10) |
| `TestAppliedSidecarMatchesBookkeeping` | `.storage-applied` lists exactly the committed applied versions and done data migrations after every step and boot; a crash leaves it lagging, never ahead (02 N14, OPS3-7) |
| `TestPrintUpgradeFloor` | `--print-upgrade-floor` prints the build's floors as an `UpgradeRequirement` without opening anything |
| `TestMigrationDAGOrdersAcrossOwners` | `After` edges, ties by the fixed core list, an unordered core owner refused, cycle detection, deferral on a disabled owner (COR3-14) |
| `TestMigrationFailureHaltsBoot` | G2 |
| `TestCheckpointPrecedesRatchet` | the checkpoint opens at the previous pinned Pebble format |
| `TestCheckpointSurvivesCrashLoop` | five failing boots, some committing steps, one with a replaced binary, reuse one pristine checkpoint; `.migration-checkpoint` keeps naming it; nothing pruned |
| `TestSecondUpgradeTakesFreshCheckpoint` | a completed attempt's checkpoint is never reused; the next upgrade takes its own and prunes predecessors beyond `checkpoint_keep` (DATA3-1) |
| `TestSwapBackCheckpointNeverReused` | after a failed restore swaps back, no checkpoint the restored store took is reused or named (DATA3-1) |
| `TestDeferredStepTakesFreshCheckpoint` | a boot whose only pending step is deferred takes no checkpoint; the boot that finally runs it takes a fresh one (OPS3-14) |
| `TestPartialCheckpointSwept` | a checkpoint directory without a record is swept at boot and never reused (DATA3-22) |
| `TestCheckpointConsumedByLaterBoot` | a completed checkpoint is consumed by a later boot after `checkpoint_min_age`, not only by the upgrade boot's own process (DATA2-18) |
| `TestRestoreCheckpointOffline` | offline swap of the latest attempt's checkpoint; an older one refused; a completed one needs `--accept-loss`; sidecars rewritten; previous binary boots on it |
| `TestCheckpointFreeSpaceRefused` | too little space → boot halts before any write |
| `TestMigrationTooLargeRefused` | cap enforcement |
| `TestUnreadableVersionFailsClosed` | contrast with ABO fail-open |
| `TestOlderBuildAcceptsNewerAdditiveSteps` / `TestOlderBuildRefusesNewerBreakingStep` / `TestCompatlessBooksRecordIsBreaking` | G5 vs `ErrKeyspaceTooNew` |
| `TestBreakingStepRequiresRaisesLevel` | registration rule |
| `TestLevelsArePerOwner` | a books raise does not stop a build without books; a core raise does |
| `TestRaiseWritesSidecarBeforeStamp` / `TestRaiseCommitErrorRewritesSidecar` | raise ordering; OPS-26b |
| `TestRaiseNeverLowersLevel` | two level steps in any order leave `max` (DATA2-22) |
| `TestNoExtractionStepRaisesCoreLevel` | every build of the extraction declares `core` level 0 and registers no core step with `RaisesLevel`; the previous release opens a store this build wrote (R3-1) |
| `TestLegacyProjectionRefusesPreGuardBuild` / `TestLegacyMaxFormatMatchesReleases` | after any level step the projection is `LegacyMaxFormat + 1` and a HEAD-era open refuses with `StorageFormatTooNewError`; the constant matches the release history (OPS2-10) |
| `TestProjectionKeepsRecordID` | the projection rewrite keeps the preference record's `ID`; an absent stamp gets one from `counter:preference` (DATA2-22) |
| `TestReaderCheckMatchesRollbackScript` | `CheckReader` and `storage_format_guard.py` agree on one fixture table: guard-aware and pre-guard `exe`, a store short of the candidate's upgrade floor, a pending restore, an in-flight restore journal, a restore staged by the failing build with and without a started swap, and an unrunnable probe, which refuses (OPS2-3, OPS3-13, DATA3-6) |
| `TestStepClockResetsAfterRollback` | a boot by a lower-level build, or a detected foreign open, restarts the served run; `Served` uses ≥-level runs (DATA2-9) |
| `TestForeignOpenDetected` / `TestLevelStepNoFalseForeignOpen` | a pre-guard open between two guard-aware boots is detected from the sidecar identity; a crash after a level step's sidecar write is not (DATA3-20) |
| `TestServedDutiesFallBack` | after a boot without the `journal` or `gen` duty, or a foreign open, the duty's consumer falls back (coverage gap; derived rebuild) |
| `TestStoreGenerationBumpsOnSwap` | a directory swap, a copy or an in-place restore changes the store generation; a swap-back returns the original one |
| `TestLevelStepDeferredOnUnconfirmedUpdate` | with `Pending` set, or before the build's `confirmed_at`, a `RaisesLevel` step is deferred and health says `storage_step_pending` (OPS2-2) |
| `TestStartupStepsExtendTimeout` | `ExtendStart` is called once per progressing step and never from a timer (OPS2-17) |
| `TestDataMigrationResumesExactly` | crash between chunks and a changed CPU count on resume; no item processed twice or skipped |
| `TestDataMigrationRewritesInPlaceNotRevisited` | own writes not re-stepped after the iterator re-open |
| `TestDataMigrationShardsDisjoint` (`-race`) | parallel shards never touch the same key |
| `TestOnlineCopyNeverOverwritesOwnerWrite` | DATA-13 interleaving: owner write during the copy wins |
| `TestModuleOnlineMigrationRunsAsModuleKind` | a module's `Online` migration runs as `<module>.datamigrate.<name>` with its `Writes` as effects and is serialised against the module's own writers; `core.storage.migrate` refuses a module owner (COR3-5) |
| `TestDryRunMatchesLiveRun` | counts from `--dry-run` equal a live run's; the live directory's SST and MANIFEST file names and sizes are unchanged and no ratchet ran (DATA2-26) |
| `TestDataMigrationVerifyBlocksBarrier` | later versions cannot run |
| `TestSnapshotAgeMetricAndRefresh` (`synctest`) | long-pass rule |
| `TestCloseWaitsForLeases` (`synctest`) | gate drain/seal |
| `TestIndexMarkDirtyInSameBatch` / `TestIndexReconcilerDrainsAfterCrash` | G4 for search |
| `TestBooksDirtySetSharedWithPrevBuild` | marks this build leaves in `idx:sidx:dirty:` are drained by the previous release, and vice versa |
| `TestIndexOpenFailureDegrades` / `TestIndexMappingChangeRebuilds` | kept ABO behaviours |
| `TestDerivedGenerationRejectsStaleArtifact` | add+delete that cancel in a count are still rejected; an artifact older than a boot without the `gen` duty is rejected |
| `TestDerivedIndexRebuildTriggers` | lifecycle; hooks and the build definition come from `Registrar.DerivedIndex`; a wrong build kind name fails P7; `Load` runs at Run start step (3a), never in `app.New` or `Describe` (COR2-15, COR3-7) |
| `TestAuxiliaryDBUsesPinnedFormat` | no `FormatNewest` |
| `BenchmarkKeyspaceScan`, `BenchmarkBatchCommit`, `BenchmarkSerializedCommit` | overhead of prefixing and stripes vs raw Pebble (< 5 % target for unserialized commits) |

## 5S.11 Open items

1. **Claim generator source of truth**: the generator reads `keyfamilies.go`; once retired, the books
   claim list is hand-maintained Go. 09 decides whether the db-census "unregistered" view stays as a
   CI check over a production census snapshot.
2. `met:` (metrics snapshots) size and owner — 05-metrics; default stays a books claim with no grant.
3. Whether `codec.JSON` (native rows) uses `encoding/json/v2` depends on its status in the toolchain
   at implementation time (record in [13-toolchain](13-toolchain.md)); the tag byte makes a later
   switch a per-value change. Legacy rows never change codec (`codec.LegacyJSON`).
4. 02 §2.4 G2 says "checkpoint before each module". This spec takes **one** checkpoint per upgrade
   attempt before the first step that runs and the ratchet: it captures every keyspace at once and is
   the only restore point anyone would use; per-keyspace restore would need a tool that does not exist.
   06 P4 already follows this; 02 to align.
5. The exact `storage.Admin` surface (Checkpoint, Compact, Metrics, Census, EstimateDiskUsage,
   read-only open of a directory for 05-backup's restore hooks) is shared with
   [05-backup](05-backup.md) and [05-health](05-health.md); listed there as consumers.
6. Multi-app store merge (DATA-30) is not "copy keyspaces": two installs each hold `c/auth/`,
   `c/rbac/`, `c/tasks/`, `c/config/`, `c/secrets/` under different keyrings, their own
   `c/storage/install`, bookkeeping, levels and `Sequence` high-water marks — and an ABO install keeps
   its identity data in books' legacy families until their cutover. Merging needs a core
   export/import capability (per keyspace, version and level check, ID-remap hooks for users, roles
   and keys, keyring merge, sequence rebase). It changes a core contract and is not built until
   multi-app exists (lowest priority); per-owner levels already keep one app's raise from blocking
   another's rollback.
7. Which families are cut over first, and when, is decided on measured data after the extraction
   (§5S.3.13, 09); nothing in S0–S10 depends on it.

## 5S.12 Objections to 03

None open. 03 R8 (v1.6.1) lists exactly this file's claim modes (read-write, read-only, aging-out,
retired) and reaches another owner's range only through a `storage.Grant` (§5S.3.11).

## 5S.13 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| `getCurrentVersion` treats a read *error* on `db_version` as version 0 (fail-open). The DB-02 replay guard limits the damage: only versions without a `migration_<n>` record would re-run. | `internal/database/migrations.go:579-585`; `:536-548`; `migration_bookkeeping.go:132-138` |
| `diagnostics query --raw` is documented as read-only but calls `pebble.Open` without `ReadOnly: true` (WAL replay can write), and it prints value previews, which for `sess:` are bearer credentials. | `cmd/diagnostics.go:262-300`; `internal/database/pebble_store_auth.go:1657` |
| `migrations.go` comments still say 398 methods / 61 migrations (now 445 / 66). | `migrations.go:17-24`; `raw/04-database-store.md` §7 |
| `SetAPIKeyStatus` sets `Status` from any state, `revoked` included, and leaves `RevokedAt`; the key-auth path checks only `Status`, so `PATCH /auth/api-keys/:id {"status":"active"}` re-activates a revoked key. Filed as a live hotfix in `todo.d/2026-11-04_platform_core_followups.md`, shipped independently and early; a rollback to a release older than the hotfix reopens it (§5S.3.7). | `internal/database/pebble_store_auth.go:1222-1247`; `internal/server/handlers/apikeys.go:336-372`; `internal/server/middleware/auth.go:335-344` |

## Review history

Round-1 findings applied to this file (`docs/plans/platform-core/reviews/spec-round-1-*.md`):

- COR-2 — `storage.Handles` removed; `module.StorageHandles` (06) holds `Indexes`; `storage/index` imports `storage`, never the reverse.
- COR-4 — participant binding lets `library.Manager.TransferIn` and other owners join a caller's batch (§5S.3.2).
- COR-10 — no `OnResolve`; storage holds nothing for an open batch, so a leaked batch stalls nothing; commit order ≠ key order rule; outbox uses commit-ordered intake.
- COR-17 / SIMP-16 — `core.EntityRef` replaced by `entity.Ref`; data-migration resource is an `entity.Ref`.
- COR-19 — layers: `storage`, `storage/index` L1; `storage/datamigrate`, `internal/storagemigrate` L4.
- COR-22 — enum renamed `MigrationCompat`; `ReadAdapter` type defined; `Spec.ReadAdapters`.
- COR-26 — task kinds and metric names checked against 03 §3.6 (`core.storage.migrate`, `mediacore_*`).
- DATA-1 — `setting:`/`preference:`/`pref:` stay books; core holds exact-key carve-outs only; counts recomputed.
- DATA-2 — one normative ownership table (§5S.9.1); ops model = 05-tasks' core claims with books `opstate:` carve-out; `act:`, `guestcftoken:` books; single `idx:` list; `keyfamilies.go` stays ABO. (Ops model and carve-outs superseded in round 3 by R3-1: books holds every family, core reaches them through grants.)
- DATA-3 / DATA-4 / OPS-19 (resolution 5) — mirrors: legacy authoritative, same-batch dual write, rebuild every boot, side records, per-principal fail-closed, over-cap read-through path. (Superseded in round 3 by R3-1.)
- DATA-5 — projections include every legacy index row and deletes; `codec.LegacyJSON` (v1) for legacy bytes. (Mirror projections superseded in round 3 by R3-1; `codec.LegacyJSON` and the index-row rule kept for grantee writes.)
- DATA-6 — return paths: mirrors rebuild every boot; config re-import and tasks reconcile carry the claims they need (rows 7, 9, 11). (Superseded in round 3 by R3-1.)
- DATA-7 / SIMP-18 — one guard: per-owner levels raised only by `Breaking` steps; `RaisesLevel` on steps and data migrations; legacy projection for pre-guard builds; one `CheckReader` for rollback script and updater.
- DATA-8 — `LegacyMigration` kind for books 1..66, non-atomic, ratcheted; S6 reworded.
- DATA-9 — cross-owner migration DAG with `After`; deferral and restart rule for barriers; one checkpoint per boot.
- DATA-10 / OPS-25 — checkpoint beside the DB, reused by from-vector, pruned only when consumed and aged, free-space precheck, offline restore.
- DATA-11 — checkpoint before the Pebble ratchet; lifecycle reordered.
- DATA-12 — persisted shard plan, idempotent steps, `RewritesInPlace`, compatibility class on data migrations.
- DATA-13 — `Serialized` ranges (claims and keyspaces), commit-time stripes, `Expect`/`GetForUpdate`, `SetIfAbsent`/`SetIfUnchanged`, per-item online copies.
- DATA-14 — dry run is the real runner on a throw-away checkpoint (offline, from archive, or preview); in-batch mode labelled estimate.
- DATA-15 — `ClaimAgingOut` mode; `opstate:` read-write for its live books writers.
- DATA-16 — same-owner nesting legal; longest match selects mode; `Except` only across owners.
- DATA-17 — reads claim-checked, scans clamped, `ClaimReadShare`; census/backup via `storage.Admin`. (`ClaimReadShare` superseded in round 3 by R3-1's grants.)
- DATA-18 — multi-owner atomic batches by participant binding with preconditions; user-merge example.
- DATA-19 — `Sequence` refill durable before use; `NextIn` writes nothing.
- DATA-20 — per-owner levels; no install-wide format; no "older store" refusal; books-only legacy names.
- DATA-25 — storage generations for derived indexes; `Load` accepts only a matching generation.
- DATA-26 — tuple key encoding; `ValidName` for keyspace names; order property test.
- DATA-27 — no all-runs adoption batch: ops runs adopted per run under caps (05-tasks), table row 11. (Superseded in round 3 by R3-1: runs stay in place.)
- DATA-28 — claim ledger and orphan takeover; generator skips reserved families; bookkeeping keyed by owner; compat field on books records.
- DATA-29 — factual slips fixed (db_version defect, ops counts incl. `op_result:`, layers); `OnResolve` declined with reason.
- DATA-30 — store merge recorded as a future core contract change (open item 6).
- SEC-1 — never-widen rule and mirror-disabled rows in the mirror contract. (Mirror-disabled rows superseded in round 3 by R3-1; never-widen kept for grantee writes.)
- OPS-2 (resolution 6) — `Adopts`, adopt steps registered forever, `ErrAdoptMissing`, served ledger and `RequiresServed`. (`Adopts`, adopt steps and `ErrAdoptMissing` superseded in round 3 by R3-1; served ledger and `RequiresServed` kept.)
- OPS-5 — storage refuses to create a store when a retired or unread database-path setting is present (lifecycle step 1).
- OPS-26 — raise rewrites sidecars after a commit error; sudoers note for the new sidecar.
- SIMP-6 — index reconciler is an in-process component, not a task schedule.
- SIMP-17 — `storage.Shards` deleted; data migrations declare `tasks.Parallelism` in L4 `storage/datamigrate`.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-H request (OPS-2) — an unmet `RequiresServed` defers the step and its dependants (never fails boot); `Served` comment and the skipped-releases row say so, matching 09 §9.2.2.
- integration pass: R-H request (09 S2b done-check) — claim violations persisted in `c/storage/claimviol/…` with a coalescing writer, a listing route and `core.storage.claims.violation_flush` (§5S.9.4).
- integration pass: Objections to 03 removed: both adopted by 03 v1.4.0 (`storage/datamigrate` at L4; R8 reads and writes).

Round 2:

- R2-1 — adopt steps declare the Teach build's level as `Migration.Floor`; the guard refuses every older build once adoption ran; the legacy projection becomes `LegacyMaxFormat + 1` at the first adopt step (D-S4, §5S.3.7 "Rollback floor"). (Superseded in round 3 by R3-1.)
- R2-2 — revocations reach legacy rows: every restriction (request, journal re-apply, post-restore hook) goes through `Mirror.Stage`; `Native`/`Side` writable only by `Stage` and the rebuild in phase mirror (§5S.3.11). (Superseded in round 3 by R3-1: one stored format, so every revocation is a legacy-row write.)
- R2-3 / DATA2-1 — per-stage ownership table (*Before* / *Hand-over* / *After* / *Contract*); `Claim.From`, `Adopt`, `Contract`; claims resolved per store and re-resolved after each adopt/contract; claim-mode changes are stamped level steps; generator per build (§5S.3.4, §5S.9.1). (Superseded in round 3 by R3-1.)
- R2-4 / DATA2-2 — books legacy steps run first under a logged legacy-migration grant; implicit edge from every adopt step; `UpgradeFloor` and `ErrUpgradeFloor` for deleting code a pending step needs. (Implicit edge now from every core step; the grant now matters only for exclusively granted ranges, round 3.)
- R2-8 / COR2-7 — §5S.4 follows 06 §6.5.1 (P2 read only, P4a checkpoint first write, P4b DAG, P4c handles, P4d reconcilers); BootBlocking data migrations in process via `tasks.ForEach`.
- DATA2-3 / SEC2-1 — side-record `Restriction` (replaces `MirrorDisabled`), most-restrictive rebuild, restricted keys projected revoked with a non-empty scope set, `LegacyFullScope` only without a side record; the reactivation path is closed by the Teach build's guards (resolution R2-1, so the reviewers' decoy/poisoned hash is not added). (Superseded in round 3 by R3-1.)
- DATA2-4 / SEC2-3 — post-restore writes via `Stage`, legacy session and invite deletes, verification rebuild after reconcilers (b) and (e). (Superseded in round 3 by R3-1.)
- DATA2-5 — reconciler (b) re-applies the revocation-journal tail through the mirror (mechanism 05-backup). (Re-apply now writes through the grantees' adapters, round 3.)
- DATA2-6 — `c/storage/install` is the first `c/` key on every store (09 S0 writes it first); exact encodings via `storage/keys`; `TestS2bOpensStoreWrittenByS0`.
- DATA2-8 — row 4 split: `invite:` + `idx:invite:username:` aging out, `inviteallow:` mirrored; native invites outside every `Native` range. (Mirror part superseded in round 3 by R3-1.)
- DATA2-9 — `Served` met by an uninterrupted ≥-level run; foreign-open detection from the `.storage-format` sidecar identity; served ledger records `run_start` and `last`.
- DATA2-12 — `op_result:`, `opsummary:`, `operation:` join `opstate:` as books read-write carve-outs (row 12, 4 families); counts recomputed.
- DATA2-14 — contracts run as persisted phases (`folded` with the level stamp, `deleting`, `done`); resume never rebuilds after `folded`. (Superseded in round 3 by R3-1; the cutover keeps persisted phases, §5S.3.13.)
- DATA2-15 — native rows in generations `Native/<gen>/`; over-cap rebuild writes a new generation and drops the old one; ID carries the generation; the adopt step does not rebuild. (Superseded in round 3 by R3-1.)
- DATA2-16 / SEC2-12 — `Sequence.AdvancePast`.
- DATA2-18 — checkpoint record with `completed_at`; consumed by any later boot; ancestor reuse; `.migration-checkpoint` names the oldest unconsumed. (Ancestor reuse narrowed by DATA3-1 in round 3.)
- DATA2-19 — DB opened at `core.storage.path` (never under the state directory); no automatic restore after a failed step.
- DATA2-21 — `Mirror.UniqueIndexes` and `Expect` on each unique legacy index row. (Superseded in round 3 by R3-1; unique legacy index rows are `Serialized` claims written with `Expect` by the grantee.)
- DATA2-22 — level steps use `max`; projection rows books-owned and written by `LegacyGuardProjection.Stage` keeping the record ID.
- DATA2-23 — row 7 adds the sub-prefix `setting:metadata_sources.`. (Superseded in round 3 by R3-1: no sub-prefix grant.)
- DATA2-24 — one ordered list of post-DAG reconcilers (a)–(f) at §5S.4 step 11.
- DATA2-26 — offline dry run opens the live store read-only and copies into scratch; test asserts unchanged SST/MANIFEST names and sizes.
- DATA2-28 — fresh-install rule at §5S.4 step 5 (levels at the build's level, adopt and contract steps recorded applied). (Adopt/contract records superseded in round 3 by R3-1; one batch per DATA3-21.)
- DATA2-29 — row 8 (bootstrap) `AgingOut` for `core/auth`; census mechanism moves to core; row 12 "a books claim"; one step spelling `core/auth#1` and bookkeeping `c/storage/ks/<owner>/…`. (Row 8 is books with no grant since round 3, R3-1.)
- OPS2-2 — implicit `RequiresSelfServed` on every `RaisesLevel` step: no contract on an unconfirmed update or before the build's confirmed readiness; `core.storage.contract.self_served_min`.
- OPS2-3 — `CheckReader` takes `ReaderCheckOptions.IgnoreRestoreStagedBy`; a started swap always refuses; state directory, not a data dir.
- OPS2-9 — `op:completion:`/`op:deprev:` (row 13a) read-write for the stamp mirror; `req-migrate` copies only; legacy delete moves to C5. (Superseded in round 3 by R3-1: stamps read and written in place, no copy.)
- OPS2-10 — legacy projection value `LegacyMaxFormat + 1`; mapping of a pre-guard B-converted store.
- OPS2-11 — adopt steps registered forever; deleting code behind an `UpgradeFloor` only. (Adopt steps superseded in round 3 by R3-1; `UpgradeFloor` kept and extended.)
- OPS2-12 — step 1 notes that a DB-path alias conflict fails P0 (05-config).
- OPS2-17 — `ExtendStart` callback per progressing step (HEAD's 10-minute budget), never on a timer.
- OPS2-24 — `mediacore_storage_level` is the one level metric; step durations reported to the kernel boot metric.
- COR2-8 — `core.storage.path` is the one DB-location source, default under the state directory; index, derived and aux directories beside the DB.
- COR2-12 — participant APIs take `*storage.Batch`; `Batch` has a `noCopy` marker; `archtest.NoBatchByValue`.
- COR2-15 — `DerivedIndex` is declarative; hooks are `DerivedHooks` via `Registrar.DerivedIndex`, run after `Register`, `Build` as a module kind. (`Build` hook and P7 `Load` superseded by COR3-7 in round 3.)
- COR2-19 — decision IDs renamed `D-S1`…`D-S12`.

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): Claims table: identity adopt steps at S4 r2, config at S3 r2; mirror procedure names S4 r1/r2/r3 levels 3/4 and `Floor` 3 (09 v1.2.0); Teach-build guard list includes the integration-URL bound. (Superseded in round 3 by R3-1.)

Round 3:

- R3-1 — one stored format per family: claims never change hands (`Claim.From`/`FromMode`/`Adopt`/`Contract`/`ContractMode`, `ClaimMigrating`, `ClaimReadShare` and per-store claim resolution removed); storage grants (`Spec.Grants`, `Grant`, `GrantMode`, `Exclusive`, ledger `grants`/`backends`, `ErrGrant*`, `mediacore_storage_grant_ops_total`, §5S.3.11) and core-only families replace the mirrored families (`Mirror`, `MirrorView`, `SideRecord`, `Restriction`, `LegacyProjection`, rebuild every boot, verification rebuild and the adoption caps removed); `Migration.Floor`, `Adopts`, `ErrAdoptMissing`, window contracts and the Teach-build rollback floor removed — no extraction step raises a core level (§5S.3.7); §5S.9.1 rewritten (every family books', one grant column, incl. `core/ai`, `core/providerhttp`, `core/library`, `core/index`); deferred cutover contract in one section (§5S.3.13); the live hotfixes are independent early fixes (§5S.3.7, §5S.13).
- DATA3-1 — checkpoint reuse only by the same uncompleted upgrade attempt on the same store (install marker + store generation `c/storage/epoch`); completed or restore-left checkpoints never reused; `restore-checkpoint` restores only the latest attempt's (§5S.3.7, §5S.3.12).
- DATA3-2 — superseded by R3-1: no claim hand-overs; the ledger's only orphan case is a removed module (§5S.3.4 rule 5).
- DATA3-3 — superseded by R3-1: nothing deletes the config blob during the extraction; row 7 is a `core/config` grant (§5S.9.1).
- DATA3-6 — `CheckReader` refuses while a restore journal holds `inflight.json` (reason `restore_inflight`, §5S.3.7).
- DATA3-9 — storage refuses to create a store while a failed restore with no previous store is recorded (§5S.4 step 1); the staging floor check is 05-backup's.
- DATA3-10 — `UpgradeFloor.Requires` and `ErrUpgradeFloor.Missing`; a level is never taken as proof that a step ran (§5S.3.5); the S10 contract floors are superseded by R3-1.
- DATA3-11 / DATA3-15 — the cutover's phases `folding`/`folded` → `raised` → `deleting` → `done`, level raised before the first delete, 09's names (§5S.3.13); C5 itself is superseded by R3-1.
- DATA3-12 — a restore boot writes the restored install's `install_id` into a marker-less store (§5S.3.4 rule 4, §5S.4 step 5), at boot rather than into the staged checkpoint, which 05-backup step b re-hashes against the manifest.
- DATA3-14 — superseded by R3-1: `syslog:` is a `core/log` read-delete grant; no `core/log#1` (§5S.9.1 row 14).
- DATA3-20 — 09 S0's guard records the sidecar identity; every level step records the identity of the `.storage-format` it rewrote in its stamp batch (§5S.3.7).
- DATA3-21 — fresh install in one batch; the "no mirror" sentence removed (§5S.4 step 5).
- DATA3-22 — checkpoint write order (directory, record, `.migration-checkpoint`) and the boot sweep of record-less directories (§5S.3.12).
- DATA3-23 — superseded by R3-1: no contracts during the extraction.
- COR3-2 — P4d: keyring and DB overrides before the journal re-apply; 06's order (§5S.4 step 11).
- COR3-5 — a module's `Online` data migration runs as `<module>.datamigrate.<name>` with `DataMigration.Writes` as its effects; `core.storage.migrate` for core owners only (§5S.3.6).
- COR3-7 — derived check and `Load` at Run start step (3a); `DerivedHooks` without `Build`; the rebuild is a module `tasks.Definition` passed to `Registrar.DerivedIndex`, built by `datamigrate.DerivedBuildTask` (L4) rather than a `storage` helper (L1 cannot import `tasks`); module-relative names (§5S.3.6, §5S.3.9).
- COR3-14 — complete tie-break list (incl. `compat`, `log`, `index`, `providerhttp`, `integrations`, `ai`, `download`, `indexer`, `notify`, `acquire`); an unordered core owner fails registration (§5S.3.5).
- OPS3-7 — applied-steps sidecar `<db>.storage-applied`, `--print-upgrade-floor`, `CheckReader` reason `upgrade_floor`, read by the updater's `StoreState` (02 N14; §5S.3.5, §5S.3.7).
- OPS3-13 — the rollback/deploy script probes the restore state with literal sudoers commands under `DEPLOY_STATE_DIR`; `cannot_determine` refuses; `ROLLBACK_IGNORE_RESTORE=1` (§5S.3.7).
- OPS3-14 — a deferred step does not trigger a checkpoint; reuse only in a real crash loop (§5S.3.12).
- OPS3-20 — superseded by R3-1: no C5 `Adopts` list.
- SEC3-2 — item (4), the rebuild rule, superseded by R3-1 (no rebuild exists).
- SEC3-11 — superseded by R3-1: no `Restriction`, decoy hash or `MirrorDisabled` in this file.
- MOD3-9 — `storage/keys` and `storage/codec` are L0 leaves; `Typed` moved into `storage` so `codec` imports nothing from core (§5S.3.3).
- (Rollback-safety follow-on of R3-1) derived-index artifacts are accepted only when every boot since maintained generations (served-ledger `duties`, §5S.3.9); the books full-text dirty set stays in its legacy family (§5S.3.8).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): objection 1 closed (03 R8 now lists this file's claim modes and grants); `StepRef` example names `core/integrations#1`, not the removed `core/config#1`; §5S.9.1 row 14: the `core/log` `syslog:` grant comes into force at S5 with `logjournal` (it imports `tasks`; no S2b reader exists and P2 would refuse an adapter-less grant).
