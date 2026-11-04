<!-- file: docs/specs/platform-core/05-storage.md -->
<!-- version: 1.3.0 -->
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
`storage/datamigrate` and its runner `core/internal/storagemigrate` are **L4** (they import `tasks`;
R4 bridge rule). Task-system names (`tasks.Definition`, `tasks.Parallelism`, checkpoints, preview) are
normative in [05-tasks](05-tasks.md). This file owns storage **mechanisms**: claims and their
per-stage hand-over, the one ownership table of ABO's families (§5S.9.1), migrations, the one store
guard, mirrors, checkpoints. Boot **order** is [06](06-app-sdk.md) §6.5.1's one phase table (R2-8);
§5S.4 describes storage's own steps and names the phase each runs in. Stage sequencing, compatibility
windows, which build is a stage's Teach build and which level each stage reaches are **policy** owned
by [09](09-abo-on-core.md), which references these mechanisms.

## 5S.0 Decisions in one table

Decision IDs carry a `D-S` prefix so they never read as 09's stages S0–S10 (COR2-19).

| # | Decision | Section |
|---|---|---|
| D-S1 | **One system-of-record Pebble DB per install**, opened only by the kernel at the absolute path `core.storage.path` (never under the state directory by construction). Holders get a `storage.Keyspace` (`m/<module>/`, `c/<subsystem>/`) and, for data written before core existed, a `LegacyRange` over their claims. Holding the handle *is* the permission — for reads as well as writes. | §5S.3.1, §5S.3.4, §5S.4 |
| D-S2 | Access outside the handle's keyspace or claims is **refused** (`ErrOutsideKeyspace`), never logged-and-allowed — except a one-release shadow mode for the books module's legacy ranges only. | §5S.3.4, §5S.9.4 |
| D-S3 | ABO's 258 key families become **legacy claims** in one normative table (§5S.9.1) that is **per stage**: each row names the owner and mode **before** its hand-over, the adopt step that hands it over, the owner and mode **after**, and the contract that retires it. A claim's owner and mode are resolved from the build **and this store's bookkeeping**, so one build serves a store before and after adoption correctly (R2-3). Claims are prefix or exact key; same-owner nesting is legal and the longest match selects the claim; cross-owner nesting needs an explicit `Except`; any other overlap fails boot. | §5S.3.4, §5S.9.1 |
| D-S4 | **Every claim-mode change is a stamped level step** of the one guard: an adopt step stamps the owner's **Teach floor** (the level of the stage's Teach build), a contract raises the level. Once adoption has run, no build older than the Teach build can open the store; the Teach build is the rollback floor (R2-1, R2-3). | §5S.3.4, §5S.3.7 |
| D-S5 | The small identity families (users, roles, API keys, the invite allow-list, OAuth identities, plus the `idx:` rows the previous build resolves through) are **mirrored** for the compatibility window: legacy rows stay **authoritative**, the adopting subsystem writes legacy and native rows in one batch, and the native copy is **rebuilt from legacy at every boot**; native-only data and native restrictions live in side records. **Every revocation — including post-restore and journal re-application — writes the legacy row in the same batch as the native one** (R2-2). Sessions and legacy invites are not mirrored (read in place, aging out). The settings families `setting:`/`preference:`/`pref:` stay books families. | §5S.3.11, §5S.9.2 |
| D-S6 | Nothing large is copied wholesale. Ops history and `syslog:` are read in place and age out by retention (`ClaimAgingOut`); `act:` and the four v1 ops families that still have live books writers stay books families. The ops run families are adopted **per run** by 05-tasks under the atomic caps. | §5S.9.1–§5S.9.2 |
| D-S7 | Versioned migrations are **per keyspace, forward-only, one atomic batch per step** — except books' legacy steps 1..66, which keep their idempotent, non-atomic form and run **first**, under a logged legacy-migration grant, before any core claim takes effect (R2-4). All pending steps of all owners run in **one DAG**. **One checkpoint per boot**, taken before anything is written to the store, reused across a crash loop, beside the DB, restorable offline (R2-8). Code a pending step needs may be deleted only behind a declared **upgrade floor** (`ErrUpgradeFloor`). | §5S.3.5, §5S.3.12, §5S.4 |
| D-S8 | Anything too large for one batch is a **data migration** (`storage/datamigrate`): a `tasks.Parallelism`, a persisted shard plan (exact resume), a compatibility class, preconditions against concurrent writers, and a dry run that is the real runner on a read-only copy of the store. | §5S.3.6 |
| D-S9 | **One store guard** (SIMP-18): a per-owner minimum-reader **level** — stamp, fsynced sidecar, fast-exit flag, one reader-check function used by `make rollback` and the updater — raised (always as `max`) only by level steps, whether the change is physical (the old "storage format"), a window-end contract or an adopt step's Teach floor. ABO's storage-format names are kept as a projection for builds older than the guard. A contract never runs on the first boot of an auto-applied release (`RequiresSelfServed`, OPS2-2). | §5S.3.5, §5S.3.7 |
| D-S10 | Concurrency: **preconditions evaluated at commit** under commit-time stripe locks for `Serialized` ranges (legacy claims and core or module keyspaces alike); **multi-owner atomic batches** by participant binding (each owner binds only the handles it holds; participant APIs take `*storage.Batch`). Storage never holds a lock, lease or reservation for an uncommitted batch, so a leaked batch stalls nothing. | §5S.3.2, §5S.6 |
| D-S11 | Full-text search is a core contract (`storage/index`, which imports `storage`, never the reverse) with a durable dirty set in the entity's batch. Derived indexes record a **storage generation** and are accepted only when it matches; their hooks are registrar data, run after `Register`. hnsw, chromem-go and go-memdb stay **app-level** (books). | §5S.3.8, §5S.3.9 |
| D-S12 | Auxiliary, re-creatable Pebble DBs (the Open Library dump mirror) only through `OpenAuxiliary`, with the same format pin; never system of record. The dead standalone AI-scan DB is deleted. | §5S.3.10 |

## 5S.1 Purpose & scope

`storage` is the only persistence API in core: an ordered byte-key/byte-value store over Pebble with
namespaced keyspaces, batches with preconditions, snapshots, iterators, sequences, per-keyspace
migrations, legacy claims, mirrors, the store guard and a census. `storage/index` is the full-text
index contract. `storage/datamigrate` declares large resumable rewrites.

In scope: the main DB lifecycle; keyspace issue and enforcement (R8); the legacy-claim registry (the
successor of `internal/database/keyfamilies.go`, which stays ABO code) and the **one ownership table**
of ABO's families that 04, 05-tasks, 05-logging, 05-authn, 05-config and 09 reference instead of
restating (DATA-2); migrations (versioned, books legacy, data); the store guard; mirrors; checkpoints;
derived-index lifecycle; full-text index contract; auxiliary DBs; key and value codecs.

Out of scope: entity schemas (modules own them; core owns no media table — 02 decision 6); backup
and restore ([05-backup](05-backup.md), which consumes `storage.Admin.Checkpoint` and performs every
directory swap, including checkpoint restores); filesystem roots ([05-library](05-library.md)); the
row shapes of users, keys, roles and config after adoption ([05-authn](05-authn.md),
[05-rbac](05-rbac.md), [05-config](05-config.md)); task execution ([05-tasks](05-tasks.md)); stage
order and window lengths ([09](09-abo-on-core.md)).

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
	ErrClaimMode       = errors.New("storage: the claim's mode forbids this operation")
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
binds `c/auth/` and its claims on `u:`, `apikey:`, `idx:apikey:user:`, `oauthid:`, `idx:oauth:user:`,
`invite:`, `inviteallow:`, writes native rows and their mirrors (§5S.3.11), and adds the
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

`storage/keys` (pure functions) and `storage/codec` (generic value codecs).

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

// LegacyJSON is the codec for every legacy row: mirror projections (§5S.3.11)
// and read adapters. It is encoding/json (v1) with no tag byte and no
// envelope, byte-compatible with what the previous ABO build decodes —
// permanently, whatever native codecs use (DATA-5). encoding/json/v2 defaults
// (case-sensitive names, omitzero, nil-slice encoding) would silently change
// what the old build reads.
func LegacyJSON[T any]() Codec[T]

var ErrUnknownVersion = errors.New("codec: unknown value version")

// Typed binds a codec to a keyspace for the common Get/Set/Scan shapes.
type Typed[T any] struct{ KS storage.Keyspace; C Codec[T] }
func (t Typed[T]) Get(ctx context.Context, key []byte) (T, error)
func (t Typed[T]) Put(b *storage.Batch, key []byte, v T) error
func (t Typed[T]) All(ctx context.Context, opts storage.ScanOptions) iter.Seq2[T, error]
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
// Exactly one of Prefix or Key is set.
type Claim struct {
	Prefix      string   // e.g. "book:"; ends in a separator byte (':' '/' '_' '.') or is listed in BareAllow
	Key         string   // exact single key, e.g. "narrator_counter"
	Except      []string // CROSS-OWNER carve-outs only: sub-prefixes or exact keys claimed by exactly one other owner
	Mode        ClaimMode // mode once Adopt is applied on this store (or from the start when From is "")
	Serialized  bool     // commits touching it take commit stripes; Expect allowed (§5S.3.2)
	TakesOver   string   // optional "<former-owner>": explicit takeover of an orphaned claim (rule 5)

	// Hand-over (R2-3, DATA2-1). From names the owner that holds the range on a
	// store where Adopt has NOT been applied yet; Adopt is the declaring
	// owner's adopt step that hands it over; Contract, when set, is the step
	// after which the range has ContractMode (default ClaimRetired).
	From         Owner     // "" = the declaring owner holds it on every store
	FromMode     ClaimMode // mode under From (books: ClaimReadWrite); required with From
	Adopt        *StepRef  // required with From; must declare Floor >= 1 (§5S.3.7)
	Contract     *StepRef  // optional; must be Breaking with RaisesLevel
	ContractMode ClaimMode // zero = ClaimRetired
	Description  string
}

type ClaimMode uint8
const (
	ClaimInvalid   ClaimMode = iota
	ClaimReadWrite           // owner reads and writes in place
	ClaimReadOnly            // frozen: reads only; every write refused
	ClaimAgingOut            // reads + Delete/DeleteRange (retention, upgrade-on-use, read-through cleanup); Set refused (DATA-15)
	ClaimMigrating           // mirrored during the compatibility window: writes only through Mirror.Stage (§5S.3.11)
	ClaimRetired             // like AgingOut, and expected to drain to zero: census and health report a non-empty retired range
	ClaimReadShare           // a reviewed, window-only read grant on ANOTHER owner's range; never ownership (DATA-17)
)

// LegacyRange is the handle for an owner's claims. Keys are ABSOLUTE (legacy
// keys have no m/ or c/ prefix). Every read and every write is checked
// against the claim set with longest-match resolution; a key that resolves to
// another owner, or to no claim, is refused with ErrOutsideKeyspace. Scans and
// iterators are clamped: keys resolving to another owner are skipped (the
// iterator seeks past carve-outs), and c/ and m/ are never reachable.
type LegacyRange struct{ /* unexported */ }
func (r LegacyRange) Get(ctx context.Context, key []byte) ([]byte, error)
func (r LegacyRange) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (r LegacyRange) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
func (r LegacyRange) NewBatch() *Batch
func (r LegacyRange) NewIndexedBatch() *Batch
func (r LegacyRange) Snapshot() (*Snapshot, error)
func (r LegacyRange) Set(ctx context.Context, key, value []byte) error
func (r LegacyRange) Delete(ctx context.Context, key []byte) error
func (r LegacyRange) DeleteRange(ctx context.Context, start, end []byte) error // the whole range must resolve to this owner
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
	Keyspace          bool               // wants m/<id>/ (or c/<sub>/)
	Level             int                // highest minimum-reader level of this owner the build serves (§5S.3.7)
	MinVersionToServe int                // 0 = every registered versioned step must be applied before the owner starts
	Claims            []Claim            // legacy claims; books and adopting core subsystems in practice
	Serialized        []string           // relative sub-prefixes of the keyspace that are Serialized; "" = the whole keyspace
	Migrations        []Migration        // versioned, §5S.3.5
	Legacy            []LegacyMigration  // books only, versions 1..66, §5S.3.5
	Mirrors           []Mirror           // core subsystems only, §5S.3.11
	Derived           []DerivedIndex     // declarative part only; hooks via Registrar.DerivedIndex (§5S.3.9, COR2-15)
	ReadAdapters      []ReadAdapter
	LegacyGuard       *LegacyGuardProjection // books only, §5S.3.7
	UpgradeFloor      *UpgradeFloor      // §5S.3.5 (R2-4)
}
```

**Handles.** What a module receives is `module.StorageHandles{Keyspace storage.Keyspace; Legacy
storage.LegacyRange; Indexes index.Opener; Aux storage.AuxOpener; Derived map[string]storage.Derived}`,
defined in [06](06-app-sdk.md) and carried by `module.Deps.Storage`. It lives in `module` (L7), not in
`storage`, because it names `storage/index`, which imports `storage` (COR-2). Core subsystems receive
the same values from the kernel's constructors. `Legacy` is the zero value unless claims were declared.

**Claim resolution is per store (R2-3, DATA2-1).** A family changes hands during the extraction
(identity at S4, config at S3, tasks at S5), and one build must serve a store on either side of that
hand-over (a skipped release, a rollback and roll-forward). So the effective owner and mode of a claim
are a function of the build **and** this store's migration bookkeeping, never of the build alone:

| Store state (bookkeeping, §5S.3.5) | Effective owner | Effective mode |
|---|---|---|
| `Adopt` not applied (claim has `From`) | `From` | `FromMode` |
| `Adopt` applied (or no `From`), `Contract` absent or not applied | declaring owner | `Mode` |
| `Contract` applied, or in its phase `folded` or later (§5S.3.11) | declaring owner | `ContractMode` |

The kernel resolves every claim at lifecycle step 4 (06 P2) and re-resolves after each adopt or contract
step inside the DAG (step 8); the handles issued at step 10 (06 P4c) use the final view, and a handle
issued earlier is never widened in place. A carve-out that a claim with `From` creates inside a parent
claim of `From` (for example `setting:config_blob` inside books' `setting:`) resolves back to the
parent while the claim is in its pre-adoption view, so the generated books `Except` lists are the same
for every store. **Every claim-mode change is a stamped level step** (R2-3): registration refuses an
`Adopt` that does not declare `Floor` and a `Contract` that is not `Breaking` with `RaisesLevel`
(§5S.3.7), so a build that resolves claims the old way can never open a store resolved the new way.
`TestClaimPhaseFollowsStoreBookkeeping` boots one build on a pre-adoption store, an adopted store and a
contracted store; `TestS2bBooksWritesEveryFamily` boots the S2b claim set (no hand-over in the build)
with books writing every family in shadow and strict mode and expects zero violations.

**Legacy-migration grant (R2-4, DATA2-2).** Books' legacy steps 1..66 were written against the whole
pre-core store, and steps 62, 65 and 66 write ops families that the S5 build hands to `core/tasks`
(`internal/database/migrations.go:479-506,1238-1250,1340-1352,1412-1424`). They therefore run with an
explicit grant: the `LegacyRange` passed to a `LegacyMigration.Up` resolves every claim in its
**pre-adoption view** (books `ReadWrite` on everything books held before any hand-over), whatever this
store's adoption state, and exists only while that step runs. Each write through the grant that lands
in a range whose current owner is not books is logged once per (step, family) at warning level with its
count and counted in `mediacore_storage_legacy_grant_writes_total{step,family}`; no other code path gets
the grant. The DAG adds an edge from **every** adopt step to the highest books legacy step the build
registers (§5S.3.5), so on a store that skipped releases books is brought to 66 before any core claim
takes effect; 05-tasks' `core/tasks#1` and 05-authn's `core/auth#1` also declare `After: books#66`
explicitly.

**Kernel validation at boot** (before any listener; any failure is a boot error; `ErrClaimConflict`
lists every conflict, not only the first):

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
   - `ClaimReadShare` claims take no part in ownership resolution: each must name a range owned by
     another owner and appear in the reviewed share list (`storage_readshares.golden`, a test fixture
     the owner of the shared range signs off).
4. Runtime proof for the static check: `c/storage/install` is **the first `c/` key ever written** to a
   store, by whichever build first writes under `c/` — 09 S0's guard (before its first served-ledger
   key) or, on a store that never ran S0, the first core-storage boot (§5S.4 step 5). The boot that finds
   the marker absent verifies that `[c/, c0)` and `[m/, m0)` are empty before anything else is written;
   a non-empty range halts boot with `ErrForeignKeys` and a sample of keys (an unregistered ABO writer
   is the only way this can happen; `keyfamilies_test.go` never proved every writer is registered,
   `raw/05-storage.md` §3). Because S0 writes the marker first, a store that ran S0–S2 opens under core
   storage at S2b with its S0-era served ledger intact (DATA2-6, `TestS2bOpensStoreWrittenByS0`).
5. **Claim ledger** (DATA-28). After validation, and after the checkpoint (§5S.4 step 9, R2-8), the kernel writes `c/storage/claims/<owner>` =
   `{claims, modes, build, at}` for every owner in the build, enabled or not. A claim on disk whose
   owner is absent from the build (a module removed, not just disabled) is **orphaned**: the census
   shows it, no write path reaches it, and a build may claim the same range only with
   `TakesOver: "<former-owner>"` on that claim — an explicit, reviewed takeover — otherwise boot fails.

**Reads are checked too** (DATA-17). Before adoption, books reads identity families directly (the
user merge scans `idx:apikey:user:`, `apikey:`, `idx:oauth:user:`, `oauthid:`, `invite:`,
`user_merge.go:241-242,454-486`). After adoption those rows are owned by `core/auth`, so such reads go
through the participant API (§5S.3.2) or an explicit `ClaimReadShare`; the share list starts empty and
grows only by review. The census and backup use `storage.Admin` (kernel-only), never a `LegacyRange`.

### 5S.3.5 Versioned migrations (G2)

```go
// StepRef names one migration step of any owner, for cross-owner ordering.
// Its one printed spelling is "<owner>#<version>" ("core/auth#1", "books#66")
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
// steps use it (OPS-2 policy).
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
	// Floor is set on an ADOPT step (one that hands a claim over, §5S.3.4):
	// the level of the stage's Teach build, which 09 names. The step stamps
	// level = max(level, Floor) in its own batch, so no build older than the
	// Teach build can open the store once adoption ran (R2-1). The step stays
	// Additive: the Teach build and every later build read the store.
	Floor          int
	After          []StepRef       // must run after these steps (DATA-9)
	Adopts         []StepRef       // a contract names the adopt steps whose data it deletes (resolution 6)
	RequiresServed *Served
	Up             func(ctx context.Context, tx *MigrationTx) error
}

// UpgradeFloor lets a build delete step code that an older store would still
// need (books' legacy steps 1..66 after the minimum-upgrade-from release, R2-4;
// mirror code after the contracts, OPS2-11). A store whose applied version for
// Owner is below Version is refused before anything is written.
type UpgradeFloor struct {
	Owner        Owner
	Version      int
	InstallFirst string // the release to install first (09's minimum-upgrade-from)
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
	ErrMigrationCycle        = errors.New("storage: migration DAG has a cycle or an unknown reference")
)

// ErrAdoptMissing: a contract would delete legacy data whose adopt step never
// ran on this store and is not registered in this build (resolution 6).
type ErrAdoptMissing struct{ Contract, Adopt StepRef; InstallFirst string }

// ErrUpgradeFloor: the store is older than a declared UpgradeFloor (R2-4,
// DATA2-2); the message names InstallFirst. Nothing was written.
type ErrUpgradeFloor struct {
	Owner        Owner
	Have, Floor  int
	InstallFirst string
}

// ErrMigrationFailed wraps the failing step.
type ErrMigrationFailed struct{ Step StepRef; Err error }
```

Runner rules:

| Rule | Detail |
|---|---|
| Order (DATA-9, R2-4) | One **DAG** over every pending step of every enabled owner: versioned steps, books legacy steps and `BootBlocking` data migrations (§5S.3.6). Edges: an owner's steps by `Version`; every `After`; every `Adopts`; data-migration `After`; and an implicit edge from **every adopt step** (a step named in some claim's `Adopt`, or a `Mirror.AdoptStep`) to the highest registered books `LegacyMigration`, so the legacy-migration phase completes before any core claim takes effect (§5S.3.4 grant). Ties broken by the kernel's fixed core subsystem list (`storage`, `secrets`, `config`, `auth`, `rbac`, `audit`, `tasks`, `outbox`, `library`, `backup`, `updater`, `health`, `log`), then modules by ID, then version. Example: `core/auth#1` declares `After: books#66`. A cycle, or a reference to an unknown step of a registered owner, is `ErrMigrationCycle` (boot error). An edge to a step that is already **applied** in this store's bookkeeping is satisfied even if its owner is disabled; an edge to an unapplied step of a disabled or absent owner defers the dependent step, and its owner is not started (rule "Barrier and serving" below). There is no "R4 layer order" rule: layer order is not an order inside L4. |
| Bookkeeping (DATA-28, DATA2-29) | `c/storage/ks/<owner>/version` (uint64) and `c/storage/ks/<owner>/applied/<v>` = `{name, compat, raises_level, floor, applied_at, build}` (notation §5S.3.3; the owner ID — `core/tasks`, `core/auth`, `books` — is one segment). Books uses a **version-source adapter** over `preference:db_version` / `preference:migration_<n>` so every older ABO build keeps seeing the numbers it wrote; the adapter adds `compat`, `raises_level` and `floor` fields to new `migration_<n>` records (older builds ignore unknown JSON fields). An applied step this build does not know and whose record has **no** `compat` field is treated as `Breaking`. |
| Atomicity | Versioned steps: step writes + version + record (+ level stamp) in one batch, so there is no replay window; idempotency remains a test, belt and braces. Books legacy steps 1..66: **not atomic** — crash safety is ABO's idempotency test (`TestMigrationUpFunctionsAreIdempotent`) plus the kept DB-02 replay guard (`migrations.go:536-548`); bookkeeping goes through the adapter after `Up` returns. `TestNoNewLegacyMigrations` pins the highest `LegacyMigration` version at 66, so every new books step is a `Migration` or a `DataMigration`. |
| Size cap | `MigrationTx` refuses past `core.storage.migration.max_batch_bytes` (default 64 MiB) or `max_batch_ops` (default 250 000) with `ErrMigrationTooLarge`; the author moves the work to a `DataMigration`. |
| Checkpoint (R2-8) | One per boot, before **anything** is written to the store — ratchet, step, install marker, ledgers, reconcilers (§5S.3.12, 06 §6.5.1 P4a). If it cannot be taken, boot halts before anything is written. |
| Failure (DATA2-19) | A failed versioned `Up` discards its batch; a failed legacy step may leave idempotent partial writes. Either way `app.New` returns an error wrapping `ErrMigrationFailed`, the process exits non-zero, earlier steps stay committed, and the boot's checkpoint stays **unconsumed** for the offline `storage restore-checkpoint` (§5S.3.12). There is **no automatic restore**: it would fight the crash-loop reuse rule and the non-atomic legacy steps. No partial versioned step is ever visible. |
| Unreadable version | Fail closed (`ErrBookkeepingUnreadable`), never "assume 0" (contrast `migrations.go:582`). |
| Newer version on disk | Unknown applied steps all `Additive` → boot continues with a warning (G5: an older build reads a newer DB). An unknown applied `Breaking` step (or compat-less books record) whose recorded `raises_level` is above this build's supported level for the owner → `ErrKeyspaceTooNew`. Levels are reader capabilities, so an unknown `Breaking` step at or below the supported level is readable by definition. This is a consistency check on top of the guard, which refuses the same store first (§5S.3.7). |
| Upgrade floor (R2-4, OPS2-11) | Adopt steps stay registered in every later build (resolution 6). A build may delete code that a pending step of an older store would need — books' legacy steps 1..66 after 09's minimum-upgrade-from release, or the mirror and rebuild code once its contracts have run — **only** by declaring an `UpgradeFloor` for that owner. A store whose applied version is below the floor is refused at lifecycle step 3, before the checkpoint, with `ErrUpgradeFloor` naming `InstallFirst`; a skipped-release install therefore passes through the named release instead of failing half-way (`TestUpgradeFloorRefusesBeforeAnyWrite`, R-test cell "store at books v64 → cur"). |
| Skipped releases (resolution 6, OPS-2) | A contract — any step that deletes legacy data an adoption copied — lists its adopt steps in `Adopts`. Before it runs, the store's bookkeeping must hold an applied record for each. If the adopt step is registered in this build, the DAG simply runs it first (a skipped expand is replayed); if the build no longer registers it, boot refuses with `ErrAdoptMissing` naming the release to install first. A contract never deletes legacy data on an unadopted store. `RequiresServed`, when set, **defers** the step while the served ledger does not yet show an uninterrupted run at ≥ the named level for `For` (09 policy): the step and every step that depends on it (`After`, and later versions of the same owner, which stay contiguous) are skipped for this boot, the owner serves at its last applied version (a deferred contract does not count toward `MinVersionToServe`), the boot log and health detail name the step and the earliest time it can run (token `storage_contract_deferred`, status Up), and every later boot re-checks it. Deferral never deletes, never fails boot, and never raises a level. Only `ErrAdoptMissing` and `ErrUpgradeFloor` refuse to start. |
| `RequiresSelfServed` (OPS2-2) | Implicit on every step with `RaisesLevel` (contracts and physical re-encodings; not adopt steps, whose `Floor` keeps the Teach build openable and which an owner needs in order to start). Such a step is deferred, exactly like an unmet `RequiresServed`, on a boot where the kernel reports an unconfirmed update (05-updater `BootResult.Pending`, i.e. `<state_dir>/updates/pending.json` exists), and until **this build** has been continuously ready for `max(core.updater.confirm_after, core.storage.contract.self_served_min)` (default 1 h), recorded in its self-served record (§5S.3.7). A contract therefore never runs on the first boot of an auto-applied release, so the updater's `Boot` and `make rollback` can still undo a broken contract release; health reports `storage_contract_pending` with the earliest run time (R-test cell d': "contract release fails readiness on first boot → `Boot` rollback allowed"). |
| Barrier and serving | An owner starts only when its applied version ≥ `MinVersionToServe` (default: its latest registered version). When an `Online` data migration's `Barrier` or a deferred edge holds the owner below that, the owner is not started in this process, health is `degraded` with the reason, and when the blocking migration completes the kernel requests a graceful restart through the lifecycle supervisor. |
| Systemd budget (OPS2-17) | The kernel passes storage `ExtendStart func(step string)` (05-health's `Supervisor.ExtendStart`; storage does not import `health`). It is called right before each step that makes real progress — store open (WAL replay), the pre-migration checkpoint, each migration step, each group of `BootBlocking` chunks, each mirror rebuild and contract phase — and extends the start deadline by `StartupStepBudget` (10 min, HEAD's value, `internal/sdnotify/sdnotify.go:46-53`, called per step at `internal/database/startup_notify.go:15-27`). Never from a timer, so a hung step is still killed. Each step's duration is also reported to the kernel's boot metric with its step name (06, OPS2-24). |

### 5S.3.6 Data migrations (`storage/datamigrate`, large, resumable)

Package `github.com/falkcorp/mediacore/storage/datamigrate` (L4: imports `storage` and `tasks`, so
the parallelism declaration is a real `tasks.Parallelism` and there is no storage-local mirror type,
SIMP-17). Modules declare data migrations in `module.Manifest.DataMigrations` ([06](06-app-sdk.md));
core subsystems hand them to the kernel with their `storage.Spec`. The runner is
`core/internal/storagemigrate` (L4 by the R4 bridge rule). **`BootBlocking` data migrations run in
process** inside the P4 DAG (06 §6.5.1 P4b), through `tasks.ForEach` over the persisted shard plan
with the persisted shard cursors below — the tasks service and executor do not exist yet in P4, so they
are not executor runs. Only **`Online`** data migrations are runs of the core task kind
`core.storage.migrate`, started with the executor (06 §6.5.2 step 5) (COR2-7).

```go
package datamigrate

type Mode uint8
const (
	ModeInvalid  Mode = iota
	BootBlocking // a DAG node of the boot (§5S.3.5); a status listener reports progress (05-health)
	Online       // a background task while serving; must be Additive and covered by read adapters
)

type DataMigration struct {
	ID              string // "<owner>.<name>", unique, stable forever
	Owner           storage.Owner
	Description     string
	Source          []storage.Range           // absolute ranges in the owner's keyspace/claims to visit
	Parallelism     tasks.Parallelism         // required (R7): tasks.CPU(), tasks.Fixed(n, reason), tasks.Sequential(reason)
	Mode            Mode                      // zero rejected
	Compat          storage.MigrationCompat   // zero rejected; Breaking ⇒ BootBlocking and RaisesLevel > 0
	RaisesLevel     int                       // stamped (sidecar first) before the first chunk writes (§5S.3.7)
	RewritesInPlace bool                      // Step writes keys inside Source
	After           []storage.StepRef
	Adopts          []storage.StepRef         // contracts: adopt steps whose data this deletes (§5S.3.5)
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
```

Execution contract (built on [05-tasks](05-tasks.md)):

- **One run per data migration ID**; an `Online` run's task resource is `entity.Ref{Module: "core",
  Kind: "storage-range", ID: <migration ID>}` (G8). State `c/storage/dm/<id>` ∈ {pending, running,
  verifying, done, failed, held}; a contract adds its phases (`folded`, `deleting`, §5S.3.11). A
  migration that must run again on later boots (the over-cap mirror rebuild) carries a generation in its
  ID, so a `done` state is never re-entered (DATA2-15).
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
  `SetIfAbsent` / `SetIfUnchanged` and per-item chunks; a failed precondition re-reads that item.
  The copy rule for adoption-style migrations is: never overwrite an existing destination row that the
  owner wrote (owner writes win).
- **Compatibility class** (DATA-12, DATA-7). `Online` ⇒ `Additive`, and the owner declares
  `ReadAdapters` covering every family the migration rewrites, so old and new rows are both readable
  throughout. `Breaking` ⇒ `BootBlocking` with `RaisesLevel`; the level stamp is committed (sidecar
  first) before the first chunk, so a half-converted store is already too new for older builds. A
  window-end contract is a `Breaking` `BootBlocking` data migration with `Adopts`, deleting in chunks.
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
  `--from-archive` (the newest verified archive) or the preview of a pending `Online` migration
  (`core.storage.migrate` preview runs the same scratch procedure inside the process via
  `storage.Admin.Checkpoint`). The old in-batch discard mode remains only as `--estimate` and is
  labelled an estimate in its output.
- Cancellation stops at a chunk boundary. A failed `Verify` leaves state `failed` with the report;
  `Barrier` versions stay blocked, so later versioned steps cannot run on a half-converted keyspace.
  `Hold` mirrors the storage-efficiency plan's held list (B6c, `2026-10-03-storage-efficiency-plan.md:170`).

### 5S.3.7 The store guard: per-owner reader levels (storage format + compat level, merged)

One mechanism (SIMP-18), owned here; the policy of when each level rises, and which stage's build
supports which level, is [09](09-abo-on-core.md) §9.2.2. It replaces both the storage-format guard
and 09's separate compat-level guard, which were the same four parts twice (stamp, sidecar, print
flag, rollback-script check; `storage_format.go:30-50,114-182`).

**Semantics.** Every owner has a monotone integer **level**: the minimum reader level a build needs to
serve that owner's data. Absent stamp = 0. A build declares, per owner it contains, the highest level
it serves (`Spec.Level`; core subsystems share one `core` level). Open refuses a store when, for any
owner the build contains, the store's level is above the build's (`LevelTooNewError`, fail-closed).
Owners present in the store but not in the build are reported and left dormant. A level changes only in
a **level step**, always as `level := max(stored, new)` so no step ever lowers it (DATA2-22): a
`Breaking` step's `RaisesLevel` — a physical re-encoding (the storage-efficiency release B) or a
window-end contract — or an adopt step's `Floor`. There is no separate "storage format" number in core
(DATA-7). There is **no "older store" refusal**: a build brings an older level forward with its own
pending level steps, and a raise in progress (stamp raised, migration not done) is resumed by the
`BootBlocking` runner before any service starts, so a half-converted store is never served (DATA-20).
Levels are **per owner**, so a books change never stops a TV app on the same core release, and books can
raise its level without a core release (DATA-20). Levels defined by reader capability (OPS-3) are a
policy choice 09 makes with this mechanism: a build that can already read a new shape declares the
higher level before any store is raised to it.

**Rollback floor = the Teach build (R2-1).** The first build of every adopt/expand sequence is that
sequence's **Teach build** (09 names it and its level): it ships the legacy-side guards a rollback
target needs — it refuses status changes on revoked or mirror-disabled API keys, bounds
`users.manage`/`settings.manage` by dominance in the legacy handlers, and journals every revocation and
restore it performs to the out-of-DB revocation journal (05-backup §5B.3.6). Every adopt step declares
that build's level as its `Floor`, so the store's level reaches it in the adopt step's own batch. From
then on the guard refuses every older build (a guard-aware one by its supported vector, a pre-guard one
by the legacy projection), so no build that could re-activate a disabled key, turn a bounded custom
role into admin power, or revoke without journaling can ever serve the store again; the Teach build and
every later build still can. Because every claim-mode change is a level step (§5S.3.4), the same rule
keeps a build that resolves ownership the old way off a store resolved the new way (R2-3).

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
| Install marker | `c/storage/install` = `{install_id, created, build}` (codec `JSON` v1) | the **first** `c/` key on every store (§5S.3.4 rule 4): 09 S0's guard writes it before its first served-ledger key, with `storage/keys` and this exact shape; a core build writes it at §5S.4 step 5 on a store that has none (DATA2-6) |
| Stamp | `c/storage/level/<owner>` = `{level, by_build, at}` | written only by a level step |
| Sidecar | `<db path>.storage-levels` | one line per owner, `<owner> <level>`, sorted; written temp file + fsync + rename + directory fsync; read by `make rollback` over ssh and by the updater without opening Pebble; every guard-aware boot rewrites it from the stamps, so after the first such boot it always exists |
| Served ledger (DATA2-9) | `c/storage/served/<owner>/<level>` = `{run_start, last_boot, boots, build}`; `c/storage/served/<owner>/last` = `{build, supported, at}` | updated once per boot per owner, after the checkpoint. A boot of a build supporting S keeps the run of every level ≤ S if the previous boot (`last`) also supported it and no foreign open was detected, else starts it (`run_start = now`); it ends the run of every level > S. Input to `Served` preconditions and 09's skipped-release rules (OPS-2) |
| Foreign-open record (DATA2-9) | `c/storage/sidecar_identity` = `{device, inode, at}` of `<db>.storage-format` | every pre-guard open rewrites that sidecar by temp file + rename (`internal/database/pebble_store.go:776-786`, `storage_format.go:285-300`), so its inode changes. Each guard-aware boot compares the recorded identity with the file's before rewriting it; a difference means another binary opened the store in between: the boot logs it, ends every level's run, and reports it to 05-config's blanking rule; it then rewrites the sidecar and records the new identity |
| Self-served record (OPS2-2) | `c/storage/selfserved/<build id>` = `{first_boot, first_ready, confirmed_at}` | `confirmed_at` is written once this build's process has been continuously ready for `max(core.updater.confirm_after, core.storage.contract.self_served_min)`; input to `RequiresSelfServed` (§5S.3.5) |
| Fast-exit flag | `--print-storage-levels` | prints the build's supported vector (`<owner> <level>` per line) and exits before opening anything; computed from manifests only |
| Legacy projection (books) | stamp `preference:storage_format`, marker `preference:storage_migration`, sidecar `<db>.storage-format`, flag `--print-storage-format` | value **1** while every owner's level is 0, **`LegacyMaxFormat + 1`** once any level is above 0 — i.e. from the first adopt step's Teach floor on (R2-1) — which every pre-guard build refuses with `StorageFormatTooNewError` (`storage_format.go:44-49`); the marker is set while a books `Breaking` raise is in progress and cleared when it is done. Written only through `LegacyGuardProjection.Stage` (books owns the rows, §5S.9.1 row 10). A core build prints, for `--print-storage-format`, the projection of its own supported vector. The projection is derived and never decides anything for a guard-aware build, except that a legacy stamp above the value this build would write is treated as too new. A store converted by a pre-guard storage-efficiency release B (legacy stamp 2 at a time `LegacyMaxFormat` is 2) is mapped at its first guard-aware boot to books level ≥ 1 by books' first versioned step; 09 §9.0 orders release B behind this guard (OPS2-10) |
| Checkpoint record | `<db path>.migration-checkpoint` | names the **oldest unconsumed** checkpoint (§5S.3.12); read by the rollback script to print the restore steps |
| Pebble format pin | `FormatValueSeparation` | one constant for main and auxiliary DBs; ratcheted only after the checkpoint (§5S.4) |

**One implementation.** `core/internal/pebble/guard.go` has one `guard{stampKey, sidecarPath,
printFlag, compare}` type instantiated twice: the per-owner vector (`compare: minReader`) and, for
books, the legacy projection (`compare: legacyExact`).

**Level step procedure** (shared by versioned steps, adopt floors and data migrations):

1. The boot's checkpoint exists (§5S.3.12).
2. Compute `new = max(stored, RaisesLevel or Floor)`; if it equals `stored`, the step stamps nothing
   and the procedure ends here. Otherwise write the sidecar(s) with the new level — `.storage-levels`,
   and for books also `.storage-format` = `LegacyMaxFormat + 1` and, for a books `Breaking` raise, the
   marker — fsynced.
3. Commit the batch that holds the stamp (and, when the projection changes, `LegacyGuardProjection.Stage`):
   for a versioned step, together with the step's writes; for a data migration, alone, before the first
   chunk; for a contract, in its `folded` batch (§5S.3.11).
4. If that commit returns an error (disk full, not a crash), rewrite the sidecars from the committed
   stamps before returning, so a failed raise does not block rollback (OPS-26b).
5. When the raising step or migration completes, clear the books marker.

A crash between 2 and 3 leaves a sidecar ahead of its stamp: older builds refuse (safe), and the next
boot of this build re-runs the pending step. At open the guard uses `max(stamp, sidecar)` per owner
for the too-new check; an unreadable sidecar is rewritten from the stamps with an error log (kept from
`pebble_store.go:651-660`); when stamp and sidecar disagree the boot log prints both.

**Reader check (one decision, three callers).** `core/internal/pebble.CheckReader(ctx, stateDir,
dbPath, exe string, opt ReaderCheckOptions) error` decides whether the binary at `exe` may open the
store as it is now, without opening Pebble: it reads `.storage-levels` (or, if absent, the legacy
`.storage-format`), runs `exe --print-storage-levels` (falling back to `--print-storage-format` for a
pre-guard binary), compares per owner, and refuses while `<state_dir>/restore/pending.json` exists —
except that an intent whose `staged_by_build` equals `opt.IgnoreRestoreStagedBy` and whose journal
shows no swap step yet (`previous_moved` or later, 05-backup §5B.3.4) does not refuse: the updater's
`Boot` passes the failing build there, and the restored `.prev` then rejects that intent as foreign
(OPS2-3). A swap that has started always refuses, because only a core build can finish or undo it.
Reason tokens: `level_newer`, `format_newer` (pre-guard binary), `restore_pending`,
`sidecar_unreadable`. The kernel binds it as [05-updater](05-updater.md)'s `ReaderCheck`;
`scripts/storage_format_guard.py` (name kept) implements the same table for `make rollback`, and both
are tested against one fixture set (`TestReaderCheckMatchesRollbackScript`). Reading `.storage-levels`
over ssh needs one more sudoers line beside the existing ones (owner action in 09 S0, OPS-26a).

**`RunCutover` is resolved** by this mechanism: the storage-efficiency release B becomes a books
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
`pebble_store_search_dirty.go:39`). The reconciler is a `lifecycle.Component` (SIMP-6 rule: an
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
// carries a grant for exactly one key, c/storage/gen/<name>.
type Derived struct{ /* unexported */ }
// Touch blind-writes a fresh Sequence value into c/storage/gen/<name> in b.
// Every batch that changes the index's source must Touch (index.MarkDirty does
// it for full-text indexes).
func (d Derived) Touch(ctx context.Context, b *Batch) error
func (d Derived) Current(ctx context.Context) (Generation, error)

// DerivedIndex is the DECLARATIVE part of anything rebuilt from storage
// (in-memory tables, ANN graphs, caches with an on-disk warm-start snapshot).
// It is manifest data (Spec.Derived), so the claim ledger and readiness gating
// know the index while its module is disabled.
type DerivedIndex struct {
	Name     string // "books.memdb", "books.hnsw"
	Version  string // a change forces a rebuild
	Blocking bool   // true: readiness waits for it (05-health)
}

// DerivedHooks are the closures, supplied through Registrar.DerivedIndex (06)
// because they close over services built in Register (COR2-15). The kernel
// calls Load only after every module registered (06 P7); Build runs as the
// MODULE task kind "<module>.derived.<name>.build", which the registrar
// registers with the module's own Effects (never a core kind, COR2-11).
type DerivedHooks struct {
	Build    func(ctx context.Context) error                             // full rebuild
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
order) but never falsely *accepted*. Rejection means rebuild — the artifact is an accelerator only.

The kernel records `c/storage/derived/<name>` = `{version, state, built_at}` at the end of 06 P7 and
triggers `Build` on a version change, on `Load` refusal and after every restore
([05-backup](05-backup.md) §5B.3.4). A declared index with no hooks, or hooks for an undeclared index,
fails boot (06 `ProblemDerivedIndex`). Snapshots go to `<db parent>/derived/<name>/` (ABO keeps `hnsw/`
via a legacy path) and are backup class `Cache`. Core components that invoke these module callbacks
(the derived loader, the index reconciler) start after module hooks and stop before them (06 §6.5.2).

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

### 5S.3.11 Mirrored families (adoption during the compatibility window)

The adoption model for the identity families (cross-cutting resolution 5; DATA-3, DATA-5, SEC-1,
OPS-19; R2-1, R2-2). The previous build writes only legacy rows (`pebble_store_auth.go:132-190` users,
`:1100-1131` API keys) and resolves through legacy index rows. So, for the whole window: **legacy rows
are authoritative**; the native copy is derived from them, and nothing an older build changed after a
rollback — a deactivation, a password reset that bumps `CredentialEpoch`, a revoked key, a deleted and
recreated user — can be lost or overridden. Conversely, nothing the new build restricts can exist only
in the native copy (R2-2). There is no field merge and no "legacy wins" heuristic.

```go
// Mirror declares one legacy family group a core subsystem adopts.
type Mirror struct {
	Name          string   // "auth.user"; metric label
	Owner         Owner    // holds both the legacy claims (ClaimMigrating, Serialized) and the native keyspace
	Legacy        []Range  // primary legacy rows, e.g. u:
	LegacyIndexes []Range  // legacy index rows the previous build resolves through, e.g. idx:user:
	UniqueIndexes []Range  // the subset of LegacyIndexes that must stay unique (DATA2-21)
	Native        []byte   // relative sub-prefix of Owner's keyspace; native rows live in Native/<gen>/ (DATA2-15)
	Side          []byte   // relative sub-prefix for side records keyed by legacy ID; never wiped by a rebuild
	// Rebuild derives the native rows of one legacy primary row. side is nil
	// when none exists. keepSide=false deletes the side record.
	Rebuild func(ctx context.Context, legacy KV, side *SideRecord, w BatchWriter) (keepSide bool, err error)
	AdoptStep StepRef  // the versioned step that establishes the mirror; registered forever (resolution 6); declares Floor (§5S.3.7)
}

// Restriction is a native restriction the legacy shape cannot express exactly.
// Order is significant: a larger value is more restrictive.
type Restriction uint8
const (
	RestrictionNone              Restriction = iota
	RestrictionDisabled                          // no exact legacy equivalent (a narrow native key, SEC-1); formerly MirrorDisabled
	RestrictionNeedsConfirmation                 // set after a restore without journal coverage (05-authn §4.14)
	RestrictionRevoked                           // revoked natively; monotone
)

// SideRecord holds native-only data and native restrictions.
type SideRecord struct {
	Payload      []byte
	LegacySHA256 [32]byte    // hash of the legacy primary bytes written in the same batch
	Fresh        bool        // computed at rebuild: the current legacy bytes still hash to LegacySHA256
	Restriction  Restriction // projected to legacy as status "revoked" whenever not None (R2-1, R2-2)
}

// LegacyProjection is what one native change looks like to the previous build.
type LegacyProjection struct {
	Rows     []KV        // the primary row AND every legacy index row (absolute legacy keys), codec.LegacyJSON
	Deletes  [][]byte    // legacy rows to remove: stale index entries, the old row on a rename, the row on delete
	Restrict Restriction // != None: the legacy row is written revoked, with a non-empty scope set (SEC-1, R2-1)
	Reason   string      // stored in the legacy row's mirror_reason field, which older builds ignore
}

// Stage puts one change into the caller's batch: the projection's rows and
// deletes, the native rows (into the current generation, and into the
// generation an online rebuild is building), the side record (with
// LegacySHA256 of the primary row just written), an Expect on the legacy
// primary row's prior value, and an Expect on every UniqueIndexes row it
// writes (its prior value; absent on create) (DATA2-21).
func (m Mirror) Stage(b *Batch, p LegacyProjection, native []KV, side []byte) error

// MirrorView is the kernel-issued runtime handle of one mirror. Native returns
// the keyspace of the current native generation, or readThrough=true while
// the native copy is not yet usable (an over-cap rebuild in progress): the
// owner then reads the legacy row + side record and decodes on demand.
type MirrorView struct{ /* unexported */ }
func (v MirrorView) Native(ctx context.Context) (ks Keyspace, readThrough bool, err error)
```

| Rule | Specification |
|---|---|
| Write path (DATA-5) | Every write by the new build goes through `Stage`: legacy primary row, **every** legacy index row (`idx:user:username:`, `idx:user:email:`, `idx:apikey:hash:`, `idx:apikey:user:`, `idx:oauth:user:`, `idx:oauth:sub:`, `idx:role:name:`), deletes of stale legacy rows, native rows and side record, in **one** batch. A key or user created on the new build is therefore found by the previous build's `GetAPIKeyByHash` and username lookup, and a delete leaves no dangling index row. Legacy bytes use `codec.LegacyJSON` permanently. Legacy invites are not mirrored (§5S.9.1 row 4a); native invites live outside every mirror's `Native` range (DATA2-8). |
| Unique legacy indexes (DATA2-21) | `Stage` adds an `Expect` on every `UniqueIndexes` row it writes (`idx:user:username:`, `idx:user:email:`, `idx:apikey:hash:`, `idx:role:name:`, `idx:oauth:sub:`), so two concurrent creates of one username both expect the index row absent and exactly one commits; HEAD serialises the same check with `inviteMu`/`userWriteMu` (`internal/database/pebble_store.go:232`). |
| Never widen (SEC-1, R2-1) | The owning subsystem's projection must never give the previous build more access than the native value. A native value with no exact legacy equivalent (in particular an API key whose projected legacy scope set would be empty — empty means "all role permissions" in the legacy build, `internal/server/middleware/auth.go:405-408`) is projected with `Restrict`: the legacy row is written with status `revoked`, a non-empty scope set, and the reason; the native scopes live in the side record. The rollback floor makes that projection safe: only builds at or after the Teach build can open an adopted store, and the Teach build refuses every status change on a revoked or mirror-restricted key (§5S.3.7), so no legacy API action can re-activate it. A legacy row adopted as `LegacyFullScope` (05-rbac) must have no side record. Property tests for each projection live with its owner ([05-authn](05-authn.md), [05-rbac](05-rbac.md)). |
| Revocations reach legacy (R2-2) | Every restriction the new build makes — revoke, disable, delete, credential-epoch bump, role removal, `needs_confirmation` — is staged through `Stage` like any other write: legacy row and native row in **one** batch. This includes re-application of the revocation journal at boot and every post-restore hook write (05-backup §5B.3.4 step h, §5B.3.6). During phase *mirror* storage refuses any write to a mirror's `Native` or `Side` range that does not come from `Stage` or the rebuild (`ErrClaimMode`), so no code path can restrict natively only, and the next boot's rebuild therefore keeps every restriction. |
| Rebuild at every boot (DATA-3) | At reconciler (a) (§5S.4 step 11, 06 P4d), for each mirror in phase *mirror*: measure the legacy primary rows keys-only against `core.storage.adopt.max_atomic_bytes` / `max_atomic_keys`; under the caps, in **one** atomic batch, `DeleteRange` the current generation, call `Rebuild` for every legacy primary row with its side record into generation `g+1`, delete side records whose legacy row is absent, and set `c/storage/mirror/<name>` = `{current: g+1, state: native}`. Deletes precede adds and generations are disjoint, so native unique indexes cannot collide (a user deleted and recreated on the previous build with a new ID). |
| Side records and restrictions (DATA2-3, DATA2-4) | `Fresh` tells the owner whether the previous build rewrote the row since the side record was written (it rewrites whole rows on routine traffic, e.g. `TouchAPIKeyLastUsed`, `pebble_store_auth.go:1284-1297`). The owner's `Rebuild` takes legacy-changeable fields (status, password hash, credential epoch, last used, mapped scopes) from the legacy row and native-only fields from the side record, and sets the native status to the **most restrictive** of the legacy status and the side record's `Restriction`: a side restriction is kept whether or not the row is `Fresh`, a legacy revocation always wins, and a `Restriction` never relaxes because the legacy row changed (revocation is monotone). A side record whose `Restriction` is `Disabled` stays a native-only key while `Fresh`; once the previous build rewrote the row it rebuilds as revoked, never "follows legacy". A legacy row that is deleted drops its side record; a revoked one rebuilds as revoked whatever the side record says. |
| Undecodable legacy row (OPS-19) | Fail closed **per principal**, open for the app: the row gets no native copy (the principal cannot authenticate or authorise), `mediacore_storage_mirror_rows_total{outcome="undecodable"}` counts it, health is `degraded` naming the key, boot continues. One bad row never crash-loops the unit. |
| Over the caps (DATA2-15) | The rebuild cannot be one batch. `c/storage/mirror/<name>` says `state: read_through`, and reads are served through the read-through view (legacy row + side record, decoded on demand) — always correct, because legacy is authoritative. The rebuild runs as an `Online` data migration `core.storage.mirror-rebuild.<name>.<gen>` writing generation `gen` with per-item commits and `SetIfUnchanged` on the legacy primary row (DATA-13), so an owner write that lands mid-rebuild wins (`Stage` writes the building generation too). When it completes, one small batch sets `current = gen`, `state: native` and `DeleteRange`s the previous generation, which removes the natives of rows deleted on the previous build; reads switch to the native copy. Every boot starts a new generation, and the generation in the ID means a `done` migration is never re-entered. |
| Adopt step (DATA2-15, R2-1) | `AdoptStep` is a versioned migration of the owner that records adoption (the claims' `Adopt` is applied, so they resolve to the owner in `ClaimMigrating`, §5S.3.4), creates the side range and stamps the Teach `Floor`. It does **not** rebuild: the first rebuild is reconciler (a) of the same boot, under the same cap rules, so an over-cap family cannot fail the adopt step with `ErrMigrationTooLarge`. It stays registered in every later build, so a store that skipped the expand release is adopted before any contract (resolution 6). |
| Contract (DATA2-14) | A `Breaking`, `BootBlocking` data migration with `RaisesLevel` and `Adopts: [AdoptStep]`, run as persisted phases in `c/storage/dm/<id>`: (1) **`folded`** — sidecars first (§5S.3.7 step 2), then one batch that runs the last rebuild (so rollback-era changes are honoured), folds side records into the native rows, stamps the level and records `folded` (over the caps: sidecars first, per-item fold commits with a cursor in state `folding`, then the stamp batch records `folded`; the sidecar already refuses older builds throughout); (2) **`deleting`** — legacy primary and index rows deleted in chunks (09 sets the chunk size) with a cursor; (3) **`done`**. From `folding` on the mirror is out of phase *mirror*: reconciler (a) skips it and the claims resolve to `ContractMode` (`ClaimRetired`). A resume skips completed phases and never rebuilds over half-deleted legacy rows (`TestContractCrashMidDeleteKeepsIdentities`, `CrashClone` at every chunk). It refuses on an unadopted store (`ErrAdoptMissing`) and is subject to `RequiresSelfServed` (§5S.3.5). |
| Sessions and legacy invites | Not mirrored (SEC-19, DATA2-8). New sessions and invites are hashed and native only; `sess:` + `idx:sess:user:` and `invite:` + `idx:invite:username:` are `ClaimAgingOut` for `core/auth`: a presented legacy token is looked up in place and the legacy row is deleted on upgrade-on-use, accept or expiry ([05-authn](05-authn.md)); a post-restore hook deletes them all (the claim permits deletes). A rollback costs one re-login and voids invites issued by the new build. |
| Verification after restrictions (SEC2-3) | After reconciler (b) or (e) wrote to a mirror, reconciler (e) ends with a verification rebuild of that mirror into a scratch generation under the caps and compares it with the current native copy; any difference means a restriction bypassed the legacy row, which `Stage`'s enforcement makes a code defect: the boot fails (a restore boot swaps back, 05-backup §5B.3.4 step x) rather than serve a restriction the next boot would undo. Over the caps the check is skipped with a log line. |

Cost surfaced: projection, side-record, restriction and rebuild code in `auth` and `rbac` for the window,
one rebuild per boot (families are small: one row per user, key, role, allow-list entry, OAuth identity on
a single-tenant install, D7), and the Teach build's legacy-side guards (09).

### 5S.3.12 The pre-migration checkpoint

| Rule | Specification |
|---|---|
| When (R2-8) | Once per boot, at lifecycle step 6 (06 §6.5.1 P4a), if any of these is pending: a Pebble format ratchet, a versioned or legacy step, a `BootBlocking` data migration, a level step, the install marker on a non-empty store. It is then the boot's **first store write**: it precedes the ratchet (DATA-11; a ratchet ahead of the checkpoint makes it unopenable by the previous build, `internal/database/storage_format.go:61-80`), the DAG, the ledgers and every every-boot writer (mirror rebuilds, revocation re-apply, config import, tasks reconcile, post-restore hooks), so it holds no post-upgrade row (COR2-7). A boot with nothing pending takes none; its every-boot writes are routine operation. An empty store needs none (§5S.4 step 5). |
| Where (DATA-10) | Beside the DB: `<db parent>/.mediacore-checkpoints/<name>/` (as ABO's backup staging sits beside the DB, `internal/backup/backup.go:569-571`), overridable by `core.storage.checkpoint_dir`. Hard links need the DB's filesystem, so the device is checked; a mismatch halts boot before any write with a message naming the override. Never the state directory, which may be another filesystem when the DB path is set by flag or env. |
| How | `storage.Admin.Checkpoint` with `WithFlushedWAL`; a record file `<name>.json` beside it = `{from_vector, journal_seq, taken_at, completed_at, consumed_at}` (`journal_seq`: the store's revocation-journal head read before the checkpoint, the boundary of a checkpoint restore, 05-backup §5B.3.6); `<db path>.migration-checkpoint` names the oldest unconsumed checkpoint. |
| Crash loops (DATA2-18) | The **from-vector** is every owner's applied version, every level and the Pebble format. A boot reuses the oldest unconsumed checkpoint whose from-vector is an **ancestor** of the current vector (same Pebble format; every version and level ≤ the current one) instead of taking another. A crash loop that commits some steps between restarts therefore keeps the one pristine pre-upgrade checkpoint, and `.migration-checkpoint` keeps naming it. |
| Consumed and pruned (DATA2-18) | When a boot finishes every pending step, it writes `completed_at` into the record of the checkpoint it used. **Any** later boot marks that checkpoint consumed once `completed_at` is at least `core.storage.migration.checkpoint_min_age` (default 7 d) old and the served ledger shows at least one boot after `completed_at` — so routine restarts (RC builds, auto-update, reboots) no longer keep checkpoints unconsumed for ever. Unconsumed checkpoints whose from-vector is an ancestor of a consumed one are pruned with it. Consumed checkpoints are pruned keeping the newest `core.storage.migration.checkpoint_keep` (default 1). Any other unconsumed checkpoint is never pruned automatically. |
| Free space (OPS-25) | Before taking one: available space on the DB's filesystem ≥ the live store's on-disk size (`EstimateDiskUsage`) + `core.storage.checkpoint.free_space_margin` (default 2 GiB), because hard-linked files diverge as compaction rewrites them. Otherwise boot halts with the numbers. `mediacore_storage_checkpoint_bytes` reports retained checkpoints. |
| Restore (DATA-10, DATA2-19) | Offline `<app> storage restore-checkpoint <name>` performs the journaled directory swap of [05-backup](05-backup.md) §5B.3.4 directly with kind `KindCheckpoint` (sidecars rewritten from the restored stamps, derived indexes invalidated, the live directory kept as the undo point), refusing while another process holds the DB lock. It runs without booting the app, because a crash-looping new build must not be required, and its result is then usable by `make rollback` to the previous binary. Nothing restores a checkpoint automatically. |

## 5S.4 Lifecycle

Boot order is [06](06-app-sdk.md) §6.5.1's one phase table (R2-8); where this list and 06 differ, 06
wins. This list names storage's own steps and the 06 phase each belongs to. **Write rule:** steps 1–5
write nothing to a non-empty store; the checkpoint (step 6) is the first store write of a boot that
takes one.

```
06 P−1  updater.Boot (05-updater): may restore .prev before anything below runs; its Pending result
        and 05-health's ExtendStart callback are passed to kernel.Open
06 P0a  pre-boot restore swap (05-backup §5B.3.4) using the absolute targets recorded in
        <state_dir>/restore/pending.json
kernel.Open(storage)                                                   06 P2 — read only
  1 resolve path: core.storage.path from 05-config's BootView (absolute; ABO maps database_path /
    DATABASE_PATH, permanent aliases). Refuse to CREATE a store at the resolved path when none exists
    there and 05-config reports a retired or unread database-path setting (OPS-5); an alias conflict on
    this ProtectOperator key has already failed P0 (05-config §1.3.5, OPS2-12). After a P0a swap the
    resolved path must equal the recorded target, else 05-backup swaps back (DATA2-25)
                                                                       fail → boot error
  2 pebble.Open without a format version (os.OpenRoot on the DB's parent directory, which need not be
    under the state directory, DATA2-19); read level stamps, sidecars, legacy projection, marker, the
    foreign-open identity (§5S.3.7)
  3 guard: any owner's store level > build level → LevelTooNewError; applied version below a declared
    UpgradeFloor → ErrUpgradeFloor                                      fail → boot error, nothing written
  4 validate specs: keyspaces, claims (overlap, orphans vs c/storage/claims/), migration DAG, data
    migrations, mirrors; resolve claims for this store (§5S.3.4)        conflict → boot error (all listed)
  5 install marker:
      empty store → FRESH INSTALL (DATA2-28): write c/storage/install, stamp every owner at the build's
        supported level, record every adopt and contract step as applied (no mirror is ever active on
        a fresh store; backup encryption is not held back by the legacy restore bound); no checkpoint
      non-empty, marker absent → verify [c/, c0) and [m/, m0) empty (read only); the marker is written
        as the first write after step 6
      marker present → nothing
                                                                       06 P4a
  6 pending ratchet ∨ step ∨ BootBlocking data migration ∨ level step ∨ marker?
      → take or reuse the one checkpoint (§5S.3.12)                    fail → boot error, nothing written
  7 ratchet to the pinned Pebble format
                                                                       06 P4b
  8 run the DAG (§5S.3.5): books legacy steps first under the legacy-migration grant, then versioned
    steps and BootBlocking data migrations (in process, tasks.ForEach); re-resolve claims after each
    adopt or contract step; RequiresServed / RequiresSelfServed deferrals (status listener up,
    05-health; ExtendStart per step)                                   fail → boot error; checkpoint unconsumed
  9 write c/storage/claims/<owner>, the served ledger, the foreign-open identity; rewrite sidecars from
    stamps; clear books marker; write completed_at on the checkpoint record when nothing is deferred
                                                                       06 P4c
 10 issue handles to core subsystems (Keyspace, LegacyRange, MirrorView)
                                                                       06 P4d
 11 post-DAG reconcilers, in this fixed order, each a function of its owning package over its handle,
    no service built yet (DATA2-24):
      (a) mirror rebuilds (§5S.3.11), run by the kernel; over-cap ones switch to read-through and queue
          an online rebuild
      (b) revocation-journal tail re-apply (05-backup §5B.3.6) through Mirror.Stage (R2-2)
      (c) secrets.Open, configstore import / re-import, AttachOverrides (05-config §1.12.2)
      (d) tasks reconcile and clock adoption (05-tasks §5T.6)
      (e) post-restore hooks (05-backup §5B.3.4 step h), then the verification rebuild of every mirror
          (b) or (e) wrote (§5S.3.11, SEC2-3)
      (f) compat usage records buffered in P0 (09 §9.2.9)
                                                                       06 P6
 12 issue handles to started modules (module.StorageHandles)
                                                                       06 P7
 13 derived indexes: declared versions and generations checked, DerivedHooks.Load after every Register;
    Build runs as <module>.derived.<name>.build; Blocking ones gate readiness
                                                                       06 §6.5.2
 14 with the task executor (Run step 5): Online data migrations, over-cap mirror rebuilds; index
    reconciler component after module hooks
 15 after readiness: self-served record (confirmed_at); checkpoint consumption (§5S.3.12)
App.Run ends → executor drained (05-tasks) → handles invalidated → gate drain (core.storage.close_timeout)
             → WAL sync → engine close (GatedDB semantics, pebble_gate.go:600-680)
```

Until 09 S2b the kernel runs without `storage` (06 §6.5.1 "Stages"): books opens its store and runs its
own migrations in a start hook as HEAD does, and only 09 S0's guard writes `c/storage/install` and the
served ledger, with the encodings of §5S.3.3 and §5S.3.7.

A disabled module (G1) still has its claims validated, recorded in the claim ledger and its keyspace
reserved; it gets no handle, runs no migrations, and its data is retained. Its pending migrations run
the next time it is enabled; steps of other owners that declared `After` on them wait (§5S.3.5).

## 5S.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Any owner's level (stamp or sidecar) above the build's | closed (`LevelTooNewError`) | An older build would misread newer data (kept from `pebble_store.go:670-680`, generalised per owner). |
| Store level below the build's, or books marker present | neither: the build's pending level step runs before any service starts; a build without it has a lower supported level and is refused by the row above | A half-converted store must never serve (DATA-20). |
| Store older than a declared `UpgradeFloor` | closed (`ErrUpgradeFloor`, names the release to install first), before the checkpoint | The code its pending steps need is gone; failing half-way would be worse (R2-4, DATA2-2). |
| Build older than the store's Teach floor (any adopt step applied) | closed (`LevelTooNewError`; pre-guard builds: `StorageFormatTooNewError` from the projection) | The rollback floor is the Teach build: older builds lack the legacy-side guards (R2-1). |
| Contract whose `RequiresServed` or `RequiresSelfServed` does not hold | neither: deferred, boot continues, health `storage_contract_deferred` / `storage_contract_pending` | A contract must not remove the rollback path on an unconfirmed or too-young build (OPS-2, OPS2-2). |
| Foreign open detected (sidecar identity changed) | open (logged; every level's served run restarts; reported to 05-config) | An older binary served the store; clocks that measure "served since" must restart (DATA2-9). |
| Sidecar unreadable or missing | open (rewrite from stamps) + error log | Stamps are authoritative; kept from `pebble_store.go:651-660`. |
| Level-step batch commit returns an error | closed (step fails); sidecars rewritten from stamps first | A failed raise must not block rollback (OPS-26b). |
| Claim conflict, cross-owner overlap without `Except`, orphan claimed without `TakesOver`, keyspace collision, zero-valued `MigrationCompat`/`Mode`/`Parallelism`, migration cycle | closed (boot) | Ownership ambiguity is a data-corruption risk; registration time is the cheapest place to catch it. |
| Keys found in `c/` or `m/` on first boot | closed (boot), with sample | Proves the static collision check; an unknown writer must be investigated, not overwritten. |
| Read or write outside keyspace/claims | closed (`ErrOutsideKeyspace`) | R8, reads included (DATA-17). Shadow mode (count + log, allow) exists only for books' `LegacyRange` for one release (§5S.9.4). |
| Write against the claim's mode (`ReadOnly`; `Set` on `AgingOut`/`Retired`; direct write to `Migrating`; a write to a mirror's `Native`/`Side` range not made by `Stage` or the rebuild) | closed (`ErrClaimMode`) | Adapters must not resurrect retired data; mirrored rows, and every restriction, change only through `Mirror.Stage` (R2-2). |
| Legacy step writes a range now owned by core | open (legacy-migration grant): logged once per step and family, counted | Books' steps 1..66 predate core claims and run before any of them takes effect (R2-4). |
| Precondition fails at commit | closed (`ErrConflict`, nothing applied) | Optimistic concurrency; the caller re-reads and retries. |
| Migration bookkeeping unreadable | closed (boot) | Replaces the fail-open at `migrations.go:582`. |
| Versioned step error / `ErrMigrationTooLarge` | closed (boot), batch discarded, earlier steps committed, checkpoint unconsumed; no automatic restore | G2; the offline `storage restore-checkpoint` is the restore path (DATA2-19). |
| Books legacy step error | closed (boot); partial writes are idempotent and replayed next boot | Existing ABO contract (`TestMigrationUpFunctionsAreIdempotent`). |
| Checkpoint fails (cross-device, too little space) | closed (boot) before anything is written | Without the checkpoint there is no restore point; `ensureSameDevice` rationale `backup.go:557-598`. |
| Contract on an unadopted store whose adopt step is gone | closed (`ErrAdoptMissing`, names the release to install) | Never delete legacy data that was never adopted (resolution 6). |
| Unknown `Breaking` step on disk | closed (`ErrKeyspaceTooNew`) | Secondary to the level check. |
| Unknown `Additive` step on disk | open + warning | G5: previous release keeps working. |
| BootBlocking data migration fails `Verify` | closed (boot), state `failed`, held list kept | Same as B6a "stop states". |
| Online data migration fails | open (app serves through the read adapters); task `failed`; health `degraded` | Old and new rows are both readable while it is incomplete. |
| Mirror rebuild meets an undecodable legacy row | closed for that principal (no native copy), open for the app; counted; health `degraded` | One bad row must not crash-loop the unit (OPS-19). |
| Mirror rebuild batch fails | closed (boot) | Serving a stale native copy is the DATA-3 hazard. |
| Verification rebuild after a restriction differs from the native copy | closed (boot; a restore boot swaps back) | A restriction that did not reach the legacy row would be undone by the next boot (R2-2, SEC2-3). |
| Contract crashes mid-way | resume from its persisted phase; never re-runs the rebuild after `folded` | A rebuild over half-deleted legacy rows deletes identities (DATA2-14). |
| Full-text index open fails | open (`Degraded`; callers fall back) | Kept from `search/register.go:38-60`. |
| Index dirty mark fails | closed (the whole entity batch fails) | It is in the same batch; partial commit impossible. |
| Derived index `Load` rejects (generation or own check) | open (rebuild) | Snapshot is an accelerator only. |
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
  `census_exact.go:40-45`) re-opens its iterator from its last key; the data-migration runner, the
  mirror rebuild and the census do so; `mediacore_storage_snapshot_oldest_seconds` alarms on violations.
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
| `core.storage.migration.checkpoint_keep` | int (consumed checkpoints) | 1 | yes |
| `core.storage.contract.self_served_min` | duration | 1h (the floor is `max(core.updater.confirm_after, this)`) | no |
| `core.storage.data_migration.chunk_bytes` | int64 | 4 MiB | yes |
| `core.storage.data_migration.chunk_items` | int | 2000 | yes |
| `core.storage.adopt.max_atomic_bytes` | int64 | 64 MiB | no |
| `core.storage.adopt.max_atomic_keys` | int | 200000 | no |
| `core.index.reconcile.workers` | int | `runtime.NumCPU()` | yes |
| `core.index.reconcile.interval` | duration | 5s (in-process component, not a task schedule) | yes |

Environment overrides for Pebble open settings (TASK-A7's intent) are the generic env layer of
[05-config](05-config.md); none of these keys is DB-persisted (they are read before the DB opens), so
every one is `ProtectOperator` (file, env or flag only).
09 references `core.storage.adopt.max_atomic_keys` for its cap instead of stating its own value.

## 5S.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_storage_ops_total` | counter | `keyspace`, `op` (get, set, delete, scan, commit) |
| `mediacore_storage_op_duration_seconds` | histogram | `op` |
| `mediacore_storage_batch_bytes` | histogram | `keyspace` |
| `mediacore_storage_keyspace_bytes` | gauge (estimate, refreshed 5 min) | `keyspace` |
| `mediacore_storage_claim_violations_total` | counter | `owner`, `op` (read, write), `mode` (refused, shadow) |
| `mediacore_storage_conflicts_total` | counter | `keyspace` |
| `mediacore_storage_snapshots_open` | gauge | — |
| `mediacore_storage_snapshot_oldest_seconds` | gauge | — |
| `mediacore_storage_migrations_applied_total` | counter | `owner` |
| `mediacore_storage_migration_duration_seconds` | histogram | `owner` |
| `mediacore_storage_data_migration_items_total` | counter | `migration`, `outcome` |
| `mediacore_storage_data_migration_state` | gauge (enum as 0/1 per state) | `migration`, `state` |
| `mediacore_storage_level` | gauge (the one level metric; 09's `mediacore_compat_level` is not a second name, OPS2-24) | `owner`, `kind` (store, supported) |
| `mediacore_storage_legacy_grant_writes_total` | counter | `step`, `family` |
| `mediacore_storage_mirror_rows_total` | counter | `mirror`, `outcome` (rebuilt, side_kept, side_dropped, restricted, undecodable) |
| `mediacore_storage_checkpoint_bytes` | gauge | `state` (unconsumed, consumed) |
| `mediacore_storage_pebble_*` | (the existing Pebble collector's series, renamed) | as today (`internal/metrics/pebble_collector.go`) |
| `mediacore_index_docs` | gauge | `module`, `index` |
| `mediacore_index_dirty` | gauge | `module`, `index` |
| `mediacore_index_reconcile_lag_seconds` | gauge | `module`, `index` |
| `mediacore_storage_derived_state` | gauge | `name`, `state` |

`keyspace` and `owner` cardinality is bounded by the number of modules + core subsystems (< 40);
`mirror` by the mirror declarations (< 10).

## 5S.9 Migration from ABO

### 5S.9.1 Ownership of the 258 families (normative, single table, per stage)

This is the **only** statement of who owns each legacy family and in which mode (DATA-2). 04,
[05-tasks](05-tasks.md) §5T.6, [05-logging](05-logging.md), [05-authn](05-authn.md),
[05-config](05-config.md) and [09](09-abo-on-core.md) reference it instead of restating. Exact keys
and sub-prefixes owned by core inside a books family are cross-owner carve-outs (books' claim lists
them in `Except`); they are not families of their own. Counts are from `keyfamilies.go:37-345`.

The table is **per stage** (R2-3, DATA2-1). *Before* is the owner and mode on a store where the row's
hand-over step has not been applied — books `ReadWrite` for every handed-over row, because books code
writes all of them until then (login and key touch rewrite `u:`/`apikey:`,
`internal/database/pebble_store_auth.go:1284-1297`; the scheduler writes
`setting:scheduled_task_last_tick.*`, `internal/scheduler/interval_clock.go:50`). *Hand-over* is the
adopt step that is the claim's `Adopt` and stamps its stage's Teach floor; *After* is the claim's
`Mode`; *Contract* is the claim's `Contract` and `ContractMode` (§5S.3.4). A build that does not contain
a row's hand-over declares no core claim for it, and books' generated claims keep the row as *Before*.
Step names for `core/config#1` and `core/log#1` are this table's; 05-config and 05-logging own the steps.

| # | Families / carve-outs | Families | Before | Hand-over (stage) | After | Contract → |
|---|---|---|---|---|---|---|
| 1 | `u:` + `idx:user:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` Migrating, Serialized; mirror `auth.user` (§5S.3.11) | C4 → Retired |
| 2 | `role:` + `idx:role:name:` | 1 | books RW | `core/rbac#1` (S4) | `core/rbac` Migrating, Serialized; mirror `rbac.role` | C4 → Retired |
| 3 | `apikey:` + `idx:apikey:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` Migrating, Serialized; mirror `auth.apikey`; never widen (SEC-1) | C4 → Retired |
| 4a | `invite:` + `idx:invite:username:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` AgingOut: legacy invites read in place by presented token, deleted on accept or expiry; not mirrored, they hold raw tokens (`internal/database/store.go:1004-1012`, DATA2-8) | C4 → Retired |
| 4b | `inviteallow:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` Migrating, Serialized; mirror `auth.inviteallow` | C4 → Retired |
| 5 | `oauthid:` + `idx:oauth:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` Migrating, Serialized; mirror `auth.oauth` | C4 → Retired |
| 6 | `sess:` + `idx:sess:user:` | 1 | books RW | `core/auth#1` (S4) | `core/auth` AgingOut: not mirrored; legacy-token fallback, delete on upgrade-on-use or expiry (05-authn, SEC-19) | C4 → Retired |
| 7 | exact keys `setting:config_blob` and every legacy secret row `setting:<legacy-name>` listed in 05-config §2.2 (today's encrypted rows `openai_api_key`, `google_books_api_key`, `hardcover_api_token`, `basic_auth_password`, plus the rows the mirror writes for the other secret fields); sub-prefix `setting:metadata_sources.` for the data-dependent `metadata_sources.<id>.credentials.<k>` rows (DATA2-23; no books key starts with it — books' own row is `setting:metadata_sources`, no dot, `internal/config/persistence.go:1414`) | — | books RW (inside row 16) | `core/config#1` (S3, 05-config phase X) | `core/config` (the kernel's `configstore`; it also stages into `c/secrets/`) RW, Serialized: import / re-import every boot and same-batch mirror (05-config §1.12.2, DATA-6, SEC-20) | C3 → Retired |
| 8 | exact keys `setting:bootstrap_token_hash`, `setting:bootstrap_token_expires_at` | — | books RW (inside row 16) | `core/auth#1` (S4) | `core/auth` AgingOut: the native form is `c/auth/bootstrap`, regenerated at every start (05-authn §4.1); the legacy rows are deleted at the first start (DATA2-29) | C4 → Retired |
| 9 | sub-prefix `setting:scheduled_task_last_tick.`; exact keys `setting:library_scan_full_last_run`, `setting:maintenance_window_last_run`, `setting:registry.operations_paused` | — | books RW (inside row 16) | `core/tasks#1` (S5) | `core/tasks` RW: clock and pause mirror (05-tasks §5T.6) | C5 → Retired |
| 10 | exact keys `preference:storage_format`, `preference:storage_migration` | — | books RW (inside row 16) | — | books RW, written only through `LegacyGuardProjection.Stage` (§5S.3.7, DATA2-22) | — |
| 11 | ops run families: `opv2:act:`, `opv2:op:`, `opv2:state:`, `opv3:run:`, `opv3:prog:`, `opv3:ckpt:`, `op:batch:`, `opv3:sched:` | 8 | books RW | `core/tasks#1` (S5) | `core/tasks` RW: per-run copy-and-mark, boot reconcile, clock mirror (05-tasks §5T.6, DATA-27, DATA-6) | C5 → AgingOut |
| 12 | v1 `opstate:`, `op_result:`, `opsummary:`, `operation:` | 4 | books RW | — | books RW: live books writers — `opstate:` (`internal/operations/state.go:59-74`, the `opstate:<opID>:params` chunk journal), `op_result:` (`CreateOperationResult`, `internal/database/pebble_store_operations.go:438`, 15 call sites, e.g. `internal/server/metadata_ops.go:391`), `opsummary:` (`SaveOperationSummaryLog`, `:256`, e.g. `internal/maintenance/jobs/backfill_book_files.go:197`), `operation:` (`UpdateOperationStatus`, `:138`, `internal/operations/registry/legacy_op_status.go:244`). A books claim (no tasks claim covers them); AgingOut once 05-tasks' `TestLegacyOpsWriters` ratchet reaches zero writers (DATA-15, DATA2-12) | — |
| 13a | `op:completion:`, `op:deprev:` (`Requires` stamps) | 2 | books RW | `core/tasks#1` (S5) | `core/tasks` RW: read in place as the fallback; `core.tasks.req-migrate` **copies** them into `c/tasks/req/` and never deletes; new stamps are mirrored to the legacy key in the same batch (OPS2-9) | C5 → AgingOut (C5 deletes the legacy stamps, `Adopts: [req-migrate]`) |
| 13 | all other ops families: v1 `operationlog:`; `op:`; `opv2:` and its other 10 children; `opv3:` and its other 15 children | 29 | books RW | `core/tasks#1` (S5) | `core/tasks` AgingOut: read in place (history, read-through pages); deleted by retention and `core.tasks.legacy-prune` | — |
| 14 | `syslog:` | 1 | books RW | `core/log#1` (the first build at or after S2b in which the journal is the only writer, 05-logging) | `core/log` AgingOut: read in place by the journal query adapter, retention deletes (05-logging) | — |
| 15 | `act:` (+12 children) | 13 | books RW | — | books RW: books' activity feed (05-logging); core audit writes only new rows under `c/audit/` | — |
| 16 | `setting:` (+`repairs_last_apply_op:`, `repairs_last_plan_op:`), `preference:`, `pref:` (+`pref:_system:` and its 6 children) | 12 | books RW | — | books RW; `Except` rows 7–9 once their hand-over is in the build: resolution 7 — runtime markers, per-book and per-user state, migration bookkeeping (`preference:db_version`, `preference:migration_<n>`); core config overrides are new rows in `c/config/` (DATA-1) | — |
| 17 | `guestcftoken:` | 1 | books RW | — | books RW: guest tokens stay in ABO (09 S4) | — |
| 18 | `idx:` (remainder) | 1 | books RW | — | books RW; `Except` the identity sub-prefixes of rows 1–6 once `core/auth#1`/`core/rbac#1` is in the build | — |
| 19 | everything else, incl. `counter:` and the bare keys `narrator_counter`, `sync_alias_use_seed_cutoff` (exact-key claims) | 180 | books RW | — | books RW; same-owner children `book:hash:`, `book:organizedhash:`, `book:originalhash:`, `library:` Retired; `book:author:`, `book:series:` AgingOut | — |

Total: 7 (rows 1–6) + 8 + 4 + 2 + 29 + 1 + 13 + 12 + 1 + 1 + 180 = **258**. The ops group is 43
families: v1 `operation:`, `operationlog:`, `op_result:`, `opstate:`, `opsummary:` (5), `op:` and 3
children (4), `opv2:` and 13 children (14), `opv3:` and 19 children (20) = rows 11 (8) + 12 (4) + 13a (2)
+ 13 (29). `met:`, `aijob*`/`aijournal:`, `provider_daily_budget:`/`provider_throttle:` and
`system:census:` are generic-looking but stay books claims until their owning spec
([05-metrics](05-metrics.md), [05-ai](05-ai.md), [05-metadata](05-metadata.md)) changes this table;
`system:census:` is a recomputable cache and is not migrated (core's census writes `c/storage/census/`).

The books claim list is **generated per build** from `keyfamilies.go` into
`internal/books/storage_claims.go` (ABO side) by a `go generate` tool that skips the reserved `c/` and
`m/` entries and emits this table's rows for the build's stage: a row whose hand-over the build
contains is left to the core claim (which names books as `From`), every other row is books' in its
*Before* mode, and the `Except` lists follow. A test checks that the generated list and the core claims
of the build agree with this table, counting by group rather than against a literal 258 (DATA-28), and
09's `TestBooksClaimsGenerated` asserts the same per stage. `keyfamilies.go` stays ABO code: core holds
no audiobook key name (R1, 03 §3.4). The census **mechanism** moves into core as
`storage.Admin.Census`, which partitions by claims + keyspaces: `census.go` and `census_families.go`
take the family list as input and hold no key name, and so does `census_exact.go` except its
`book_ver:` special case (`census_exact.go:169,545`), which stays books code behind a census hook. So
`GET /api/core/v1/storage/census` replaces the books endpoint and keeps its shape (DATA2-29, 04).

### 5S.9.2 Mirror, adopt per item, or read in place

Production counts are unknown to this spec. The design does not depend on them: sizing decides only
*how* a mirror rebuild runs (one batch or online), never *whether* it is correct.

| Group | Expected size (reasoning from code) | Decision | Why |
|---|---|---|---|
| users, roles, API keys, the invite allow-list, OAuth identities (+ their `idx:` rows) | one row per user/key/role/entry/identity on a single-tenant home install (D7): tens to hundreds | **mirror** (§5S.3.11), one batch per boot | small; core auth/rbac must own them; legacy authoritative for the window |
| sessions `sess:`, legacy invites `invite:` | bounded by active logins and open invites (72 h TTL); a 10-minute cleanup removes expired sessions (`server_lifecycle.go:529-530`) | **no mirror**; read in place, aging out | raw bearer tokens and raw invite tokens must not be copied or mirrored (SEC-19, DATA2-8) |
| `setting:config_blob`, legacy secret rows | one blob row + a handful of secret rows | **import / re-import + mirror** by core config (05-config §1.12.2) | config overrides are new `c/config/` rows; the older build's changes are re-imported on the next boot |
| the rest of `setting:`, `preference:`, `pref:` | `setting:` holds per-file and per-job markers (one `transcode_skip_<hash>` per file); `preference:` holds per-book state; `pref:` per-user books data | **books family**, not adopted | they are not config; strict claims would refuse books' writers, and shadow mode would copy library-scale book state (DATA-1) |
| ops run records | small per run | **adopted per run** by 05-tasks (copy-and-mark, under the atomic caps; over the cap a run is dropped with a notice, never a boot failure) | one executor owns each run; runs are independent, so there is no all-runs batch (DATA-27) |
| ops history, per-run pages, `Requires` stamps | library-scale per run (`opv3:snap:`, `opv3:item:`, `opv2:row:`) | **read in place** / read-through; aging out | copying history buys nothing; it ages out |
| activity `act:*` | tiered, retention-pruned, includes the debug tier | **books family** | books' domain feed (05-logging) |
| `syslog:` | retention-pruned (`log_retention_days`) | **read in place** by core log; aging out | append-only; retention already deletes it |

**Mirror procedure** (09 schedules the stages; identity in S4, Teach build before it): the adopt step
hands the claims over and stamps the Teach floor; every boot rebuilds (reconciler (a)); every write and
every restriction is staged legacy + native + side; the contract (≥ 09's window, `RaisesLevel`,
`Adopts`, `RequiresSelfServed`) rebuilds once more and folds side records in its `folded` batch,
deletes the legacy rows in chunks and leaves the claims `Retired`.

**Read-in-place procedure** (ops history, `syslog:`): the subsystem receives a `LegacyRange` over its
aging-out claims and a `ReadAdapter` that decodes legacy rows into the new types for list, detail and
census views; new rows go only to `c/…`. Retention deletes legacy rows (the reason for `ClaimAgingOut`).
Rollback to the previous release loses visibility of rows created after the upgrade, which 05-tasks
and 05-logging state per family.

**Read-only command for the owner** (gives per-family key counts and bytes; the estimated census
reads sstable properties and an in-memory cache only, `census.go:317-372`):

```bash
curl -sS -H "Authorization: Bearer <redacted-token>" \
  "https://<host-a>/api/v1/diagnostics/db-census?fresh=true" \
| jq '.data.families[]
      | select(.prefix | test("^(u:|role:|sess:|apikey:|invite|oauthid:|guestcftoken:|idx:|setting:|preference:|pref:|act:|syslog:|operation|op_result:|opstate:|opsummary:|op:|opv2:|opv3:)"))
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
| `keyfamilies.go` | stays ABO: the source of the generated books claim list (per stage, §5S.9.1) |
| `census.go`, `census_families.go`, `census_exact.go` | core `storage.Admin.Census` (the mechanism holds no key name); the `book_ver:` special case stays books behind a census hook (DATA2-29) |
| `migrations.go` runner + `migration_bookkeeping.go` | core DAG runner; books' 66 migrations become `LegacyMigration`s 1..66 over the version-source adapter (non-atomic, idempotent, DATA-8), run first under the legacy-migration grant; every later books step is a `Migration` or `DataMigration`; the 66 steps and the store methods they call (incl. `MigrateOpsV2ToV3`) are deleted only by a build that declares books' `UpgradeFloor{Version: 66, InstallFirst: <09's minimum-upgrade-from release>}` (R2-4) |
| `ReconcileOpsV3WithV2` boot step (`store.go:1862-1870`) | 05-tasks legacy adapter (a boot hook of the tasks subsystem) |
| `NewPebbleStore(path)` / `InitializeStore` / `globalStore` | books module constructor `database.NewPebbleStore(h module.StorageHandles)`; `globalStore` deleted |
| `internal/database` call sites of `p.db.*` (`GatedDB` method set) | a thin ABO adapter `internal/database/legacyrange.go` mapping the used subset (Get/Set/Delete/DeleteRange/NewIter/NewBatch/NewIndexedBatch/NewSnapshot/LogData) onto `storage.LegacyRange`; admin calls (Checkpoint, Compact, Metrics, SSTables) move to core `storage.Admin` users (backup, health, metrics) |
| ad-hoc identity mutexes (`inviteMu`, `apiKeyMu`, `sessionWriteMu`, `userMergeApplyMu`) | `Serialized` claims + `Expect` preconditions; the user merge's auth half becomes the `auth.Admin.StageUserMerge` participant (DATA-18) |
| `nextID` / `counter:` | ABO keeps `counter:<name>` (claimed) for existing entity IDs; new core rows use `storage.Sequence` |
| `internal/search` (bleve index, reconciler, dirty set) | `core/internal/bleveindex` + `storage/index`; books keeps document building and query parsing |
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
3. Strict mode. Then the hand-overs of 09's stages (§5S.9.1), each preceded by its Teach build, each
   with its window and its contract.

## 5S.10 Tests

| Test | Proves |
|---|---|
| `TestKeyspaceWritesCannotEscapePrefix` | relative keys always land under `Prefix()` incl. `Sub`, `DeleteRange` bounds |
| `TestLegacyRangeRefusesForeignKeys` | R8 for absolute keys on reads and writes, longest-match resolution, carve-outs, scan clamping |
| `TestSameOwnerNestedClaimsSelectMode` | books' nested pairs boot; the longest match selects the child's mode |
| `TestClaimOverlapFailsBoot` / `TestClaimCarveOutAccepted` | cross-owner rules, multi-owner conflicts listed together |
| `TestOrphanedClaimNeedsTakeover` | claim ledger; removed module's claims not silently re-owned |
| `TestABOClaimsMatchOwnershipTable` | generated books claims plus the build's core claims equal §5S.9.1 by group for every stage; none touches `m/`, `c/` |
| `TestClaimPhaseFollowsStoreBookkeeping` | one build on a pre-adoption, an adopted and a contracted store resolves each row to *Before*, *After*, *Contract* (R2-3, DATA2-1) |
| `TestS2bBooksWritesEveryFamily` | the S2b claim set with books writing every family: zero violations in shadow and strict mode |
| `TestClaimModeChangeIsLevelStep` | an `Adopt` without `Floor` or a `Contract` without `RaisesLevel` fails registration |
| `TestFirstBootRefusesForeignKeys` | runtime `ErrForeignKeys` |
| `TestS2bOpensStoreWrittenByS0` | a store with S0's install marker and served ledger opens under core storage, ledger history readable (DATA2-6) |
| `TestFreshInstallStampsLevels` | an empty store gets the marker, every owner at the build's level and adopt/contract steps recorded applied; no mirror runs (DATA2-28) |
| `TestCheckpointPrecedesEveryWrite` | on an upgrade boot the checkpoint holds no `c/` row written by that boot (marker, ledgers, reconcilers) (R2-8) |
| `TestPostDAGReconcilerOrder` | reconcilers (a)–(f) run in order after the DAG and handle issue, before any listener (DATA2-24) |
| `TestBatchAtomicAcrossBoundKeyspaces` | outbox/dirty-mark/participant writes in one commit; `CrashClone` shows all or nothing |
| `archtest.NoBatchByValue` | no core API or spec Go block takes or holds a `storage.Batch` by value (COR2-12) |
| `TestExpectConflictAppliesNothing` (`-race`) | concurrent writers on a `Serialized` key: one wins, the other gets `ErrConflict`, no lost update |
| `TestLeakedBatchBlocksNobody` | an unclosed batch with preconditions holds no stripe; other commits proceed |
| `TestUserMergeAtomicAcrossOwners` | books + auth participant in one batch; `CrashClone` at every point |
| `TestSequenceNeverReusesAfterCrash` | crash at every point between refill and consuming commits; no ID reissued |
| `TestSequenceAdvancePast` | after `AdvancePast(n)` every ID is > n, across a crash (DATA2-16) |
| `TestJoinOrderMatchesSegmentOrder` | tuple-encoding order property over random segment lists |
| `TestMigrationStepAtomicWithBookkeeping` | crash at every point of a versioned step: nothing, or step+version+record |
| `TestLegacyMigrationsReplayIdempotent` / `TestNoNewLegacyMigrations` | books 1..66 semantics; ratchet at 66 |
| `TestLegacySteps59to66RunUnderGrant` | on a store at books v58 whose tasks adopt is pending, steps 59–66 run before every adopt step, their writes into core-owned families are logged and counted, and boot succeeds (R2-4, DATA2-2) |
| `TestUpgradeFloorRefusesBeforeAnyWrite` | a store below a declared `UpgradeFloor` is refused with `ErrUpgradeFloor` naming the release; the store is byte-identical |
| `TestMigrationDAGOrdersAcrossOwners` | `After` edges, ties, cycle detection, deferral on a disabled owner |
| `TestMigrationFailureHaltsBoot` | G2 |
| `TestCheckpointPrecedesRatchet` | the checkpoint opens at the previous pinned Pebble format |
| `TestCheckpointSurvivesCrashLoop` | five failing boots, some committing steps, reuse one pristine checkpoint (ancestor rule); `.migration-checkpoint` keeps naming it; nothing pruned |
| `TestCheckpointConsumedByLaterBoot` | a checkpoint whose boot completed is consumed by a later boot after `checkpoint_min_age`, not only by the upgrade boot's own process (DATA2-18) |
| `TestRestoreCheckpointOffline` | offline swap; sidecars rewritten; previous binary boots on it |
| `TestCheckpointFreeSpaceRefused` | too little space → boot halts before any write |
| `TestMigrationTooLargeRefused` | cap enforcement |
| `TestUnreadableVersionFailsClosed` | contrast with ABO fail-open |
| `TestOlderBuildAcceptsNewerAdditiveSteps` / `TestOlderBuildRefusesNewerBreakingStep` / `TestCompatlessBooksRecordIsBreaking` | G5 vs `ErrKeyspaceTooNew` |
| `TestBreakingStepRequiresRaisesLevel` | registration rule |
| `TestLevelsArePerOwner` | a books raise does not stop a build without books; a core raise does |
| `TestRaiseWritesSidecarBeforeStamp` / `TestRaiseCommitErrorRewritesSidecar` | raise ordering; OPS-26b |
| `TestRaiseNeverLowersLevel` | two level steps in any order leave `max` (DATA2-22) |
| `TestAdoptFloorRefusesPreTeachBuild` | after an adopt step, a build older than the Teach build is refused, the Teach build opens the store (R2-1) |
| `TestLegacyProjectionRefusesPreGuardBuild` / `TestLegacyMaxFormatMatchesReleases` | after any level step the projection is `LegacyMaxFormat + 1` and a HEAD-era open refuses with `StorageFormatTooNewError`; the constant matches the release history (OPS2-10) |
| `TestProjectionKeepsRecordID` | the projection rewrite keeps the preference record's `ID`; an absent stamp gets one from `counter:preference` (DATA2-22) |
| `TestReaderCheckMatchesRollbackScript` | `CheckReader` and `storage_format_guard.py` agree on one fixture table (guard-aware and pre-guard `exe`, pending restore, a restore staged by the failing build with and without a started swap) (OPS2-3) |
| `TestContractAdoptsFirstOnSkippedRelease` / `TestContractRefusesUnadoptedStore` | resolution 6 |
| `TestContractClockResetsAfterRollback` | a boot by a lower-level build, or a detected foreign open, restarts the served run; `Served` uses ≥-level runs (DATA2-9) |
| `TestForeignOpenDetected` | a pre-guard open between two guard-aware boots is detected from the sidecar identity |
| `TestContractDeferredOnUnconfirmedUpdate` | with `Pending` set, or before the build's `confirmed_at`, a `RaisesLevel` step is deferred and health says `storage_contract_pending`; an adopt step is not (OPS2-2) |
| `TestStartupStepsExtendTimeout` | `ExtendStart` is called once per progressing step and never from a timer (OPS2-17) |
| `TestDataMigrationResumesExactly` | crash between chunks and a changed CPU count on resume; no item processed twice or skipped |
| `TestDataMigrationRewritesInPlaceNotRevisited` | own writes not re-stepped after the iterator re-open |
| `TestDataMigrationShardsDisjoint` (`-race`) | parallel shards never touch the same key |
| `TestOnlineCopyNeverOverwritesOwnerWrite` | DATA-13 interleaving: owner write during the copy wins |
| `TestDryRunMatchesLiveRun` | counts from `--dry-run` equal a live run's; the live directory's SST and MANIFEST file names and sizes are unchanged and no ratchet ran (DATA2-26) |
| `TestDataMigrationVerifyBlocksBarrier` | later versions cannot run |
| `TestMirrorLegacyIndexesResolve` | create on the new build, resolve by username / key hash with the previous build's store code |
| `TestMirrorUniqueIndexExpect` (`-race`) | two concurrent creates of one username: exactly one commits (DATA2-21) |
| `TestRestrictionSurvivesRebuild` | a revoke, epoch bump or `needs_confirmation` made by a request, a post-restore hook or a journal re-apply survives a restart and is visible to the previous build (R2-2) |
| `TestNativeOnlyRestrictionRefused` | a write to a mirror's `Native`/`Side` range outside `Stage` is refused with `ErrClaimMode` in phase mirror |
| `TestRollbackReactivationNeverWidens` | drive the Teach build's legacy handlers against a mirrored store: a mirror-restricted and a natively revoked key stay refused; roll-forward keeps them restricted; a rewritten restricted row is never adopted as `LegacyFullScope` (R2-1, DATA2-3, SEC2-1) |
| `TestMirrorRebuildHonoursRollbackWrites` | deactivate, reset password, revoke key on the previous build → next boot's native copy reflects it |
| `TestMirrorRebuildDeletesAndRecreates` | user deleted and recreated with a new ID on the previous build; no unique-index collision |
| `TestMirrorDisabledNeverWidens` | a `RestrictionDisabled` row stays a native-only key while fresh and rebuilds as revoked once rewritten |
| `TestMirrorUndecodableRowQuarantined` | one bad row: principal refused, boot continues, health degraded |
| `TestMirrorOverCapReadsThrough` | over the cap: reads correct from legacy; online rebuild with preconditions into a new generation; a user deleted on the previous build has no native row after the switch (DATA2-15) |
| `TestContractCrashMidDeleteKeepsIdentities` | `CrashClone` at every phase and chunk of a contract: no identity is lost, no rebuild runs after `folded` (DATA2-14) |
| `TestMirrorLegacyCodecGolden` | HEAD fixtures decode and re-encode byte-identically with `codec.LegacyJSON` |
| `TestSnapshotAgeMetricAndRefresh` (`synctest`) | long-pass rule |
| `TestCloseWaitsForLeases` (`synctest`) | gate drain/seal |
| `TestIndexMarkDirtyInSameBatch` / `TestIndexReconcilerDrainsAfterCrash` | G4 for search |
| `TestIndexOpenFailureDegrades` / `TestIndexMappingChangeRebuilds` | kept ABO behaviours |
| `TestDerivedGenerationRejectsStaleArtifact` | add+delete that cancel in a count are still rejected |
| `TestDerivedIndexRebuildTriggers` | lifecycle; hooks come from `Registrar.DerivedIndex` and `Load` runs after every `Register` (COR2-15) |
| `TestAuxiliaryDBUsesPinnedFormat` | no `FormatNewest` |
| `BenchmarkKeyspaceScan`, `BenchmarkBatchCommit`, `BenchmarkSerializedCommit` | overhead of prefixing and stripes vs raw Pebble (< 5 % target for unserialized commits) |

## 5S.11 Open items

1. **Claim generator source of truth**: the generator reads `keyfamilies.go`; once retired, the books
   claim list is hand-maintained Go. 09 decides whether the db-census "unregistered" view stays as a
   CI check over a production census snapshot.
2. `met:` (metrics snapshots) size and owner — 05-metrics; default stays a books claim.
3. Whether `codec.JSON` (native rows) uses `encoding/json/v2` depends on its status in the toolchain
   at implementation time (record in [13-toolchain](13-toolchain.md)); the tag byte makes a later
   switch a per-value change. Legacy rows never change codec (`codec.LegacyJSON`).
4. 02 §2.4 G2 says "checkpoint before each module". This spec takes **one** checkpoint per boot
   before the first pending step and ratchet: it captures every keyspace at once and is the only
   restore point anyone would use; per-keyspace restore would need a tool that does not exist.
   06 P4 already follows this; 02 to align.
5. The exact `storage.Admin` surface (Checkpoint, Compact, Metrics, Census, EstimateDiskUsage,
   read-only open of a directory for 05-backup's restore hooks) is shared with
   [05-backup](05-backup.md) and [05-health](05-health.md); listed there as consumers.
6. Multi-app store merge (DATA-30) is not "copy keyspaces": two installs each hold `c/auth/`,
   `c/rbac/`, `c/tasks/`, `c/config/`, `c/secrets/` under different keyrings, their own
   `c/storage/install`, bookkeeping, levels and `Sequence` high-water marks. Merging needs a core
   export/import capability (per keyspace, version and level check, ID-remap hooks for users, roles
   and keys, keyring merge, sequence rebase). It changes a core contract and is not built until
   multi-app exists (lowest priority); per-owner levels already keep one app's raise from blocking
   another's rollback.

## 5S.12 Objections to 03

None open. 03 v1.4.0 places `storage/datamigrate` at L4 (§3.3, R4) and R8 says "reads and writes"
and points here for claim modes and nesting.

## 5S.13 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| `getCurrentVersion` treats a read *error* on `db_version` as version 0 (fail-open). The DB-02 replay guard limits the damage: only versions without a `migration_<n>` record would re-run. | `internal/database/migrations.go:579-585`; `:536-548`; `migration_bookkeeping.go:132-138` |
| `diagnostics query --raw` is documented as read-only but calls `pebble.Open` without `ReadOnly: true` (WAL replay can write), and it prints value previews, which for `sess:` are bearer credentials. | `cmd/diagnostics.go:262-300`; `internal/database/pebble_store_auth.go:1657` |
| `migrations.go` comments still say 398 methods / 61 migrations (now 445 / 66). | `migrations.go:17-24`; `raw/04-database-store.md` §7 |
| `SetAPIKeyStatus` sets `Status` from any state, `revoked` included, and leaves `RevokedAt`; the key-auth path checks only `Status`, so `PATCH /auth/api-keys/:id {"status":"active"}` re-activates a revoked key. Filed as a live hotfix; the identity Teach build makes the guard a rollback-floor guarantee (05-authn §11.1, R2-1). | `internal/database/pebble_store_auth.go:1222-1247`; `internal/server/handlers/apikeys.go:336-372`; `internal/server/middleware/auth.go:335-344` |

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
- DATA-2 — one normative ownership table (§5S.9.1); ops model = 05-tasks' core claims with books `opstate:` carve-out; `act:`, `guestcftoken:` books; single `idx:` list; `keyfamilies.go` stays ABO.
- DATA-3 / DATA-4 / OPS-19 (resolution 5) — mirrors: legacy authoritative, same-batch dual write, rebuild every boot, side records, per-principal fail-closed, over-cap read-through path.
- DATA-5 — projections include every legacy index row and deletes; `codec.LegacyJSON` (v1) for legacy bytes.
- DATA-6 — return paths: mirrors rebuild every boot; config re-import and tasks reconcile carry the claims they need (rows 7, 9, 11).
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
- DATA-17 — reads claim-checked, scans clamped, `ClaimReadShare`; census/backup via `storage.Admin`.
- DATA-18 — multi-owner atomic batches by participant binding with preconditions; user-merge example.
- DATA-19 — `Sequence` refill durable before use; `NextIn` writes nothing.
- DATA-20 — per-owner levels; no install-wide format; no "older store" refusal; books-only legacy names.
- DATA-25 — storage generations for derived indexes; `Load` accepts only a matching generation.
- DATA-26 — tuple key encoding; `ValidName` for keyspace names; order property test.
- DATA-27 — no all-runs adoption batch: ops runs adopted per run under caps (05-tasks), table row 11.
- DATA-28 — claim ledger and orphan takeover; generator skips reserved families; bookkeeping keyed by owner; compat field on books records.
- DATA-29 — factual slips fixed (db_version defect, ops counts incl. `op_result:`, layers); `OnResolve` declined with reason.
- DATA-30 — store merge recorded as a future core contract change (open item 6).
- SEC-1 — never-widen rule and mirror-disabled rows in the mirror contract.
- OPS-2 (resolution 6) — `Adopts`, adopt steps registered forever, `ErrAdoptMissing`, served ledger and `RequiresServed`.
- OPS-5 — storage refuses to create a store when a retired or unread database-path setting is present (lifecycle step 1).
- OPS-26 — raise rewrites sidecars after a commit error; sudoers note for the new sidecar.
- SIMP-6 — index reconciler is an in-process component, not a task schedule.
- SIMP-17 — `storage.Shards` deleted; data migrations declare `tasks.Parallelism` in L4 `storage/datamigrate`.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-H request (OPS-2) — an unmet `RequiresServed` defers the step and its dependants (never fails boot); `Served` comment and the skipped-releases row say so, matching 09 §9.2.2.
- integration pass: R-H request (09 S2b done-check) — claim violations persisted in `c/storage/claimviol/…` with a coalescing writer, a listing route and `core.storage.claims.violation_flush` (§5S.9.4).
- integration pass: Objections to 03 removed: both adopted by 03 v1.4.0 (`storage/datamigrate` at L4; R8 reads and writes).

Round 2:

- R2-1 — adopt steps declare the Teach build's level as `Migration.Floor`; the guard refuses every older build once adoption ran; the legacy projection becomes `LegacyMaxFormat + 1` at the first adopt step (D-S4, §5S.3.7 "Rollback floor").
- R2-2 — revocations reach legacy rows: every restriction (request, journal re-apply, post-restore hook) goes through `Mirror.Stage`; `Native`/`Side` writable only by `Stage` and the rebuild in phase mirror (§5S.3.11).
- R2-3 / DATA2-1 — per-stage ownership table (*Before* / *Hand-over* / *After* / *Contract*); `Claim.From`, `Adopt`, `Contract`; claims resolved per store and re-resolved after each adopt/contract; claim-mode changes are stamped level steps; generator per build (§5S.3.4, §5S.9.1).
- R2-4 / DATA2-2 — books legacy steps run first under a logged legacy-migration grant; implicit edge from every adopt step; `UpgradeFloor` and `ErrUpgradeFloor` for deleting code a pending step needs.
- R2-8 / COR2-7 — §5S.4 follows 06 §6.5.1 (P2 read only, P4a checkpoint first write, P4b DAG, P4c handles, P4d reconcilers); BootBlocking data migrations in process via `tasks.ForEach`.
- DATA2-3 / SEC2-1 — side-record `Restriction` (replaces `MirrorDisabled`), most-restrictive rebuild, restricted keys projected revoked with a non-empty scope set, `LegacyFullScope` only without a side record; the reactivation path is closed by the Teach build's guards (resolution R2-1, so the reviewers' decoy/poisoned hash is not added).
- DATA2-4 / SEC2-3 — post-restore writes via `Stage`, legacy session and invite deletes, verification rebuild after reconcilers (b) and (e).
- DATA2-5 — reconciler (b) re-applies the revocation-journal tail through the mirror (mechanism 05-backup).
- DATA2-6 — `c/storage/install` is the first `c/` key on every store (09 S0 writes it first); exact encodings via `storage/keys`; `TestS2bOpensStoreWrittenByS0`.
- DATA2-8 — row 4 split: `invite:` + `idx:invite:username:` aging out, `inviteallow:` mirrored; native invites outside every `Native` range.
- DATA2-9 — `Served` met by an uninterrupted ≥-level run; foreign-open detection from the `.storage-format` sidecar identity; served ledger records `run_start` and `last`.
- DATA2-12 — `op_result:`, `opsummary:`, `operation:` join `opstate:` as books read-write carve-outs (row 12, 4 families); counts recomputed.
- DATA2-14 — contracts run as persisted phases (`folded` with the level stamp, `deleting`, `done`); resume never rebuilds after `folded`.
- DATA2-15 — native rows in generations `Native/<gen>/`; over-cap rebuild writes a new generation and drops the old one; ID carries the generation; the adopt step does not rebuild.
- DATA2-16 / SEC2-12 — `Sequence.AdvancePast`.
- DATA2-18 — checkpoint record with `completed_at`; consumed by any later boot; ancestor reuse; `.migration-checkpoint` names the oldest unconsumed.
- DATA2-19 — DB opened at `core.storage.path` (never under the state directory); no automatic restore after a failed step.
- DATA2-21 — `Mirror.UniqueIndexes` and `Expect` on each unique legacy index row.
- DATA2-22 — level steps use `max`; projection rows books-owned and written by `LegacyGuardProjection.Stage` keeping the record ID.
- DATA2-23 — row 7 adds the sub-prefix `setting:metadata_sources.`.
- DATA2-24 — one ordered list of post-DAG reconcilers (a)–(f) at §5S.4 step 11.
- DATA2-26 — offline dry run opens the live store read-only and copies into scratch; test asserts unchanged SST/MANIFEST names and sizes.
- DATA2-28 — fresh-install rule at §5S.4 step 5 (levels at the build's level, adopt and contract steps recorded applied).
- DATA2-29 — row 8 (bootstrap) `AgingOut` for `core/auth`; census mechanism moves to core; row 12 "a books claim"; one step spelling `core/auth#1` and bookkeeping `c/storage/ks/<owner>/…`.
- OPS2-2 — implicit `RequiresSelfServed` on every `RaisesLevel` step: no contract on an unconfirmed update or before the build's confirmed readiness; `core.storage.contract.self_served_min`.
- OPS2-3 — `CheckReader` takes `ReaderCheckOptions.IgnoreRestoreStagedBy`; a started swap always refuses; state directory, not a data dir.
- OPS2-9 — `op:completion:`/`op:deprev:` (row 13a) read-write for the stamp mirror; `req-migrate` copies only; legacy delete moves to C5.
- OPS2-10 — legacy projection value `LegacyMaxFormat + 1`; mapping of a pre-guard B-converted store.
- OPS2-11 — adopt steps registered forever; deleting code behind an `UpgradeFloor` only.
- OPS2-12 — step 1 notes that a DB-path alias conflict fails P0 (05-config).
- OPS2-17 — `ExtendStart` callback per progressing step (HEAD's 10-minute budget), never on a timer.
- OPS2-24 — `mediacore_storage_level` is the one level metric; step durations reported to the kernel boot metric.
- COR2-8 — `core.storage.path` is the one DB-location source, default under the state directory; index, derived and aux directories beside the DB.
- COR2-12 — participant APIs take `*storage.Batch`; `Batch` has a `noCopy` marker; `archtest.NoBatchByValue`.
- COR2-15 — `DerivedIndex` is declarative; hooks are `DerivedHooks` via `Registrar.DerivedIndex`, run after `Register`, `Build` as a module kind.
- COR2-19 — decision IDs renamed `D-S1`…`D-S12`.
