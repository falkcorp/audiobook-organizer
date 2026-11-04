<!-- file: docs/specs/platform-core/05-storage.md -->
<!-- version: 1.0.0 -->
<!-- guid: 940ce8be-6f86-4450-b43a-77bf78827b12 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Storage (`storage`, `storage/index`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) (engines, 258 key families,
migrations, backup), [`raw/04-database-store.md`](../../plans/platform-core/raw/04-database-store.md)
(`database.Store`), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md),
[`raw/02-import-graph-hotspots.md`](../../plans/platform-core/raw/02-import-graph-hotspots.md),
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md). Names, layers and rules
(R1–R8) are from [03-architecture](03-architecture.md); decisions from
[02-decision-record](02-decision-record.md). Task-system names (`tasks.Definition`,
`tasks.Parallelism`, checkpoints, preview) are normative in [05-tasks](05-tasks.md).

## 5S.0 Decisions in one table

| # | Decision | Section |
|---|---|---|
| S1 | **One system-of-record Pebble DB per install**, opened only by the kernel. Modules and core subsystems get a `storage.Keyspace` (`m/<module>/`, `c/<subsystem>/`); holding the handle *is* the permission. | §5S.3.1 |
| S2 | A write whose key is outside the handle's keyspace or legacy claims is **refused** (`ErrOutsideKeyspace`), never logged-and-allowed — except a one-release shadow mode for the books module's *legacy* ranges only. | §5S.3.4, §5S.9.4 |
| S3 | ABO's 258 key families become **legacy claims**, owned by module `books` unless core adopts them. Claims are prefix *or exact-key*, with explicit carve-outs; any ambiguous overlap across owners fails boot. Static check at HEAD: no family is, contains, or is contained by `m/` or `c/`. | §5S.3.4, §5S.9.1 |
| S4 | Core **adopts** the small generic families (users, roles, sessions, API keys, invites, OAuth identities, settings, global and per-user preferences — 11 families + 6 `idx:` sub-ranges) by **copy + dual-write for the compatibility window**, so the previous release can still read them (G5). If a family turns out too large for one batch, the *same* adoption runs as a resumable data migration automatically. | §5S.9.2 |
| S5 | Core **reads in place** the large families (ops v1/v2/v3: 42 families; activity log and system log: 14 families) behind read adapters; new rows go to `c/tasks/…`, `c/log/…`, `c/audit/…`; legacy rows age out by retention, never by copy. | §5S.9.2 |
| S6 | Versioned migrations are **per keyspace, forward-only, one atomic batch per step** (the step's writes, the version bump and the applied record commit together). One Pebble checkpoint is taken before the first pending step of a boot. Failure halts boot with nothing of the failed step committed (G2). | §5S.3.5 |
| S7 | Anything too large for one batch is a **data migration**: a core task kind (`core.storage.migrate`) that is sharded, checkpointed in the same batch as its writes, resumable, dry-run-able, verified by an invariant gate, and either boot-blocking or online. | §5S.3.6 |
| S8 | The storage-format guard stays (stamp + sidecar, same keys and filename, so `make rollback` keeps working) and gets the converter it references: `RunCutover` is realised as the boot-blocking data-migration runner with `RaisesFormat`. The dangling scaffolding is deleted from ABO. | §5S.3.7 |
| S9 | Full-text search is a core contract (`storage/index`, bleve in `internal/bleveindex`) with a **durable dirty set in the same batch as the entity write** and a reconciler. hnsw, chromem-go and go-memdb stay **app-level** (books); core only provides the derived-index lifecycle they plug into. | §5S.3.8, §5S.3.9 |
| S10 | Auxiliary, re-creatable Pebble DBs (the Open Library dump mirror) are allowed only through `storage.OpenAuxiliary`, with the same format pin; never system of record. The dead standalone AI-scan DB is deleted. | §5S.3.10 |

## 5S.1 Purpose & scope

`storage` is the only persistence API in core: an ordered byte-key/byte-value store over Pebble with
namespaced keyspaces, batches, snapshots, iterators, sequences, per-keyspace migrations, data
migrations, legacy claims and a census. `storage/index` is the full-text index contract.

In scope: the main DB lifecycle; keyspace issue and enforcement (R8); legacy-claim registry (the
successor of `internal/database/keyfamilies.go`); migrations (versioned and data); the storage-format
guard; derived-index lifecycle; full-text index contract; auxiliary DBs; value/key codec guidance;
the adoption plan for ABO's generic families.

Out of scope: entity schemas (modules own them; core owns no media table — 02 decision 6); backup
and restore ([05-backup](05-backup.md), which consumes `storage.Admin.Checkpoint`); filesystem roots
([05-library](05-library.md)); the representation of users/sessions/roles/settings after adoption
([05-authn](05-authn.md), [05-rbac](05-rbac.md), [05-config](05-config.md) decide the row shapes;
this file specifies how the bytes move); task execution semantics ([05-tasks](05-tasks.md)).

## 5S.2 Current state at HEAD

| Fact | Evidence |
|---|---|
| Pebble v2.1.7 is the only system-of-record engine; SQLite removed (`InitializeStore` rejects it), NutsDB retired; bbolt indirect only. | `go.mod:7`; `internal/database/store.go:1840-1851`; `raw/05-storage.md` §1 |
| One main DB, directory `audiobooks.pebble`, plus sidecar file `<db>.storage-format`; siblings `library.bleve`, `hnsw/`, `certs/`, bootstrap-token file, `backups/`. | `cmd/root.go:488`; `internal/database/storage_format.go:111-123`; `internal/appdirs/appdirs.go:52-62`; `internal/backup/backup.go:134-146` |
| Every "store" (activity, metrics, embeddings, AI scan, catalog, master list) is a key-prefix namespace over the one DB; one WAL, one backup unit, one migration clock. | `internal/database/store_from_store.go:12-63`; `internal/server/registry_wire.go:270-310` |
| All engine access goes through `GatedDB`, a lease gate so `Close` drains in-flight calls; borrowers get it from `PebbleStore.DB()`. | `internal/database/pebble_gate.go:327-340,363`; `internal/database/pebble_store.go:957-962` |
| Key-family registry: 258 families, feeds `GET /api/v1/diagnostics/db-census`; an unregistered prefix shows as "(unregistered)" and is **not** refused; uniqueness and partition coverage are tested, writer registration is not. | `internal/database/keyfamilies.go:14-36,37-345`; `internal/server/wire_media_routes.go:68`; `raw/05-storage.md` §3 |
| 256 families are `name:`-prefixed; 2 are bare keys: `narrator_counter` and `sync_alias_use_seed_cutoff`. Both are deliberate (the first keeps legacy narrator numbering, the second must not fall inside the `sync_alias_use:` scans). | `keyfamilies.go:84,316`; `pebble_store_authors.go:1220-1229`; `pebble_store_sync_alias_use.go:209-213` |
| `idx:` is one family shared by auth, users, sessions, API keys, OAuth, invites, playback, playlists, book versions, collections, ABS sessions and the search dirty set (sub-ranges `idx:user:`, `idx:sess:user:`, `idx:apikey:hash:`, `idx:ubs:status:`, `idx:bv:book:`, `idx:sidx:dirty:` …). | `keyfamilies.go:64`; `pebble_store_auth.go:1131`; `pebble_store_search_dirty.go:39` |
| `counter:` holds ASCII-decimal ID counters for unrelated entities (preference, operationlog, author, series, playlist, scan …); `nextID` commits each increment with its own `pebble.Sync` write, outside the caller's batch. | `pebble_store.go:96-103,966-990` |
| Values are `encoding/json` by default; binary exceptions: ASCII counters, float16+zstd vectors in a JSON envelope, 8-byte LE `dedup:seq`, big-endian fingerprint windows and clock values. No gob or protobuf in `internal/database`. | `raw/05-storage.md` §3 "Value encoding" |
| Versioned migrations: one hand-ordered slice of 66 (`Version`, `Up`, optional `Down`; 2 have `Down`); version in `preference:db_version`, records in `preference:migration_<n>`; record + version bump are one batch, **but `Up`'s own writes are not in that batch** (they go through store methods); `Up` must be idempotent (enforced by a test). | `internal/database/migrations.go:69-112,512-575`; `migration_bookkeeping.go:14-80` |
| An unreadable `db_version` is read as version 0 (fail-open), so every migration would re-run. | `migrations.go:579-585` |
| Migrations run after the store's init phase has already written (counters, `library:`→`import_path:` conversion, stamp), and before `ReconcileOpsV3WithV2`, which runs every boot and only logs on failure. No checkpoint is taken before migrations. | `store.go:1846-1872`; `pebble_store.go:1089-1105` |
| Storage-format guard: `SupportedStorageFormat = 1`; stamp at `preference:storage_format`, marker `preference:storage_migration`, sidecar `<db>.storage-format`; open refuses too-new and (serve mode) older/marked stores; Pebble format pinned to `FormatValueSeparation` and ratcheted only after checks. | `storage_format.go:44-80,82-87,111-123`; `pebble_store.go:620-687,689-699` |
| `RunCutover` is referenced but **does not exist**; `openForCutover` and `StorageMigrationCheckpointSuffix` have no caller. The design is in the storage-efficiency plan (task B6a: stamp-driven serve-only cut-over, checkpoint, marker, status listener B6b, held list B6c) and was never built. | `storage_format.go:125-129`; `pebble_store.go:597-619`; `docs/plans/2026-10-03-storage-efficiency-plan.md:168-170` |
| `make rollback` reads the sidecar and the `.prev` binary's `--print-storage-format` and refuses a swap across a format change. | `Makefile:871-903`; `scripts/storage_format_guard.py:17-22` |
| Derived engines: go-memdb (`MemStore`, rebuilt every boot, reads fall back to Pebble until published) **live**; bleve scorch `library.bleve` **live but degradable** (open failure → no search; mapping change → delete + recreate + dirty-set drain); coder/hnsw **live by default** (`resolveVectorBackend("")` = hnsw), snapshot in `hnsw/` validated against a Pebble truth count; chromem-go **optional** brute-force alternative, in-memory only. | `pebble_store.go:792-845`; `internal/search/register.go:42-60`; `internal/search/bleve_index.go:186-262`; `internal/server/registry_wire.go:65-80,136-159`; `internal/server/server.go:838-870`; `internal/database/hnsw_embedding_store.go:346-380` |
| Second Pebble DBs: the Open Library dump mirror (`ol:` keys, optional, opened with `pebble.FormatNewest`) and a standalone AI-scan DB constructor with no non-test caller. | `internal/openlibrary/store.go:41-43,97-104`; `internal/database/ai_scan_store.go:156-181`; `storage_format.go:62-73` |
| A package-level `globalStore` is still set by `InitializeStore`. | `store.go:1807-1825,1854` |

Coupling that the extraction must cut (`raw/02-import-graph-hotspots.md` findings 1–2):
`internal/database` imports 8 audiobook packages and *is* the book model (`store.go:202`), so
nothing that imports it can be core. Core storage therefore starts from zero code shared with
`internal/database`, and books keeps `internal/database` as its repository layer over a core handle
(§5S.9.3).

## 5S.3 Go API

Package `github.com/falkcorp/mediacore/storage` (layer 2, R4). The engine lives in
`core/internal/pebble`; the only constructor of a `*DB` is `kernel`. Apps never open a DB (R2).

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
// "core/<subsystem>" ("core/tasks").
type Owner string

// DB is the install's single system-of-record store. Only the kernel holds a
// *DB; everyone else holds Keyspaces cut from it.
type DB struct{ /* engine, claim registry, gate, metrics; unexported */ }

// Keyspace is a capability: every key passed to its methods is relative and
// is stored as Prefix()+key, so a holder cannot address anything else.
// The zero Keyspace is invalid; every method on it returns ErrInvalidKeyspace.
type Keyspace struct{ /* unexported */ }

func (k Keyspace) Prefix() string      // "m/tv/" or "c/tasks/"
func (k Keyspace) Owner() Owner
func (k Keyspace) Sub(segment string) Keyspace // "m/tv/" + "series" -> "m/tv/series/"; segment validated by keys.ValidSegment

// Reads. Get returns a copy owned by the caller; ErrNotFound when absent.
func (k Keyspace) Get(ctx context.Context, key []byte) ([]byte, error)
func (k Keyspace) Has(ctx context.Context, key []byte) (bool, error)

// Single-key writes are a one-entry batch committed with the keyspace's
// default Durability.
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

// Sequence returns a named monotonic uint64 allocator stored in the keyspace.
func (k Keyspace) Sequence(name string) *Sequence

// EstimateBytes is Pebble's on-disk estimate for the keyspace (cheap).
func (k Keyspace) EstimateBytes(ctx context.Context) (uint64, error)

type ScanOptions struct {
	Start, End []byte // relative; nil = keyspace bound
	Reverse    bool
	KeysOnly   bool  // skips value fetch for value-separated blobs
	Limit      int   // 0 = unlimited
}

type KV struct{ Key, Value []byte } // Key is relative to the keyspace
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
	ErrNotFound         = errors.New("storage: not found")
	ErrOutsideKeyspace  = errors.New("storage: key outside the handle's keyspace or claims")
	ErrInvalidKeyspace  = errors.New("storage: zero or closed keyspace")
	ErrClosed           = errors.New("storage: db closed")
	ErrBatchTooLarge    = errors.New("storage: batch exceeds the configured limit")
	ErrReadOnlyClaim    = errors.New("storage: claim is read-only")
)
```

### 5S.3.2 Batches, snapshots, sequences

```go
// Batch is atomic across every keyspace it is bound to: one Pebble batch.
// A batch created from keyspace K can write K; Bind adds another keyspace the
// caller *holds*. This is how a module writes its row and an outbox entry
// (c/outbox/) or an index-dirty mark (c/index/) in one commit (05-events, §5S.3.8).
type Batch struct{ /* unexported */ }

func (b *Batch) In(k Keyspace) BatchWriter // panics if k is not bound; use Bind first
func (b *Batch) Bind(k Keyspace) *Batch
func (b *Batch) Set(key, value []byte) error // shorthand for b.In(origin).Set
func (b *Batch) Delete(key []byte) error
func (b *Batch) DeleteRange(start, end []byte) error
func (b *Batch) Get(ctx context.Context, key []byte) ([]byte, error) // indexed batches only
func (b *Batch) Len() int      // encoded bytes, for callers that chunk
func (b *Batch) Count() int    // entries
func (b *Batch) Commit(ctx context.Context, d Durability) error
func (b *Batch) Close() error  // discards an uncommitted batch; idempotent

type BatchWriter interface {
	Set(key, value []byte) error
	Delete(key []byte) error
	DeleteRange(start, end []byte) error
}

// Snapshot pins a Pebble version. Snapshots are for bounded reads (a page, a
// verification pass); long passes must refresh (see Concurrency contract).
type Snapshot struct{ /* unexported */ }
func (s *Snapshot) Get(ctx context.Context, key []byte) ([]byte, error)
func (s *Snapshot) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (s *Snapshot) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
func (s *Snapshot) Age() time.Duration
func (s *Snapshot) Close() error

// Sequence leases ranges of IDs (default 64) so Next is usually lock-free and
// never costs its own fsync — unlike ABO's nextID (pebble_store.go:966-990).
// IDs lost to a crash inside a lease are burned, never reused.
type Sequence struct{ /* unexported */ }
func (s *Sequence) Next(ctx context.Context) (uint64, error)
func (s *Sequence) NextIn(b *Batch) (uint64, error) // allocates and persists the high-water mark in b
```

### 5S.3.3 Key and value codecs (guidance + helpers)

`storage/keys` (pure functions) and `storage/codec` (generic value codecs).

```go
package keys

// Segments are joined with '/', which sorts below every letter and digit, so a
// parent's keys stay contiguous. A segment may not be empty, contain '/', or
// contain 0x00; arbitrary user strings go through Escape.
func Join(segments ...string) []byte
func ValidSegment(s string) error
func Escape(s string) string           // order-preserving: 0x00 -> 0x00 0x01, '/' -> 0x00 0x02
func Uint64(n uint64) []byte           // 8-byte big-endian (sorts numerically)
func Time(t time.Time) []byte          // 8-byte big-endian UnixNano, sign bit flipped
func PrefixEnd(prefix []byte) []byte   // smallest key greater than every key with prefix; nil if none
```

```go
package codec

// Codec[T] encodes one value type. Every codec writes a 1-byte format tag first
// so a value can change encoding without a key-format change.
type Codec[T any] interface {
	Encode(v T) ([]byte, error)
	Decode(b []byte) (T, error)
}

func JSON[T any](version uint8) Codec[T] // tag 'j', then {"v":version,...}; Decode rejects unknown versions with ErrUnknownVersion
func Raw() Codec[[]byte]                 // tag 'r'
func Zstd[T any](inner Codec[T], minSize int) Codec[T] // tag 'z' when compressed (klauspost/compress)

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
| Keys are ASCII path segments joined by `/`; IDs are ULIDs (sortable) or `keys.Uint64`; times in keys are `keys.Time`. Never decimal numbers without padding. | Order-preserving range scans; ABO mixes `:` separators, zero-padded and unpadded nanos (`raw/05-storage.md` §3), which the census has to special-case. |
| Secondary index keys: `<index>/<term>/<primary-id>` with an empty value, written in the **same batch** as the primary row. | ABO's single-writer + ratchet discipline (`raw/08-prior-art.md` §7 `opv2-waiting-deps-index`) generalised. |
| Values: `codec.JSON[T]` with an explicit version; binary only for hot or large values (vectors, bitsets), always behind a tag byte. No gob. | Today's JSON default is readable in the census and diagnostics; the tag byte is what ABO lacks when it must change an encoding. |
| Values over ~64 KiB: rely on Pebble value separation (`FormatValueSeparation`, already pinned, `storage_format.go:80`) and `KeysOnly` scans; split above 4 MiB. | Large values in a scan path dominate read amplification. |
| A keyspace never stores another keyspace's key as a foreign key except through `core.EntityRef{Module, Kind, ID}` (02 decision 6). | Module isolation (R3). |

### 5S.3.4 Claims, keyspace issue, enforcement (R8)

```go
// Claim reserves legacy keys (written before core existed) for one owner.
// Exactly one of Prefix or Key is set.
type Claim struct {
	Prefix      string   // e.g. "book:"; must end in a separator byte (':' '/' '_') or be listed in BareAllow
	Key         string   // exact single key, e.g. "narrator_counter"
	Except      []string // sub-prefixes or exact keys this claim does NOT cover; each must be claimed by another owner
	Description string
	Mode        ClaimMode
}

type ClaimMode uint8
const (
	ClaimInvalid  ClaimMode = iota
	ClaimReadWrite          // owner reads and writes in place (books' 182 families)
	ClaimReadOnly           // read adapter; writes refused (ops v1, retired families)
	ClaimMigrating          // a core adoption is copying it; owner dual-writes (§5S.9.2)
	ClaimRetired            // no reads expected; census still reports it; a cleanup migration may delete it
)

// LegacyRange is the handle for an owner's claims. Keys are ABSOLUTE (legacy
// keys have no m/ prefix). Every write is checked against the claim set with
// longest-match resolution; a key that resolves to another owner, or to no
// claim, is refused with ErrOutsideKeyspace.
type LegacyRange struct{ /* unexported */ }
func (r LegacyRange) Get(ctx context.Context, key []byte) ([]byte, error)
func (r LegacyRange) Scan(ctx context.Context, opts ScanOptions) iter.Seq2[KV, error]
func (r LegacyRange) NewIterator(ctx context.Context, opts ScanOptions) (*Iterator, error)
func (r LegacyRange) NewBatch() *Batch
func (r LegacyRange) NewIndexedBatch() *Batch
func (r LegacyRange) Snapshot() (*Snapshot, error)
func (r LegacyRange) Set(ctx context.Context, key, value []byte) error
func (r LegacyRange) Delete(ctx context.Context, key []byte) error
func (r LegacyRange) DeleteRange(ctx context.Context, start, end []byte) error // whole range must resolve to this owner
func (r LegacyRange) Barrier(ctx context.Context) error // WAL sync barrier; replaces LogData(nil, Sync) (ident_state.go:1360)

// Spec is what a module (module.Manifest.Storage) or core subsystem declares.
type Spec struct {
	Keyspace     bool          // wants m/<id>/ (or c/<sub>/)
	Claims       []Claim       // legacy claims, books only in practice
	Migrations   []Migration   // versioned, §5S.3.5
	Data         []DataMigration // §5S.3.6
	Derived      []DerivedIndex  // §5S.3.9
	Compat       []Compat        // read adapters for adopted families, §5S.9.2
}

// What the module receives in module.Deps.
type Handles struct {
	Keyspace Keyspace
	Legacy   LegacyRange // zero unless Claims were declared
	Indexes  index.Opener
}
```

Kernel validation at boot (before any listener; any failure is a boot error, `ErrClaimConflict`
lists every conflict, not only the first):

1. Module IDs and core subsystem names are unique; keyspace prefixes derive from them, so they
   cannot collide.
2. No claim prefix or key starts with `m/` or `c/`, and none is a prefix of `m/` or `c/`
   (verified statically at HEAD for all 258 families: no family has `/`, and the only families
   starting with `m`/`c` — `met:`, `mlist:…`, `metadata_…`, `merge…`, `cat…`, `counter:`, `cursor:`,
   `col:`, `chapters:`, `candfb:`, `cover_text…` — diverge at the second byte).
3. Two claims overlap if one's prefix is a prefix of the other's (or equals an exact key under
   it). Overlap is legal **only** when the shorter claim lists the longer in `Except` and the longer
   is claimed by exactly one *other* owner. Otherwise boot fails. This is the multi-app safety rule:
   two modules (possibly from two apps in one binary) can never both own a key.
4. Runtime proof for the static check: on the first boot that creates `c/storage/install`, the
   kernel verifies that `[c/, c0)` and `[m/, m0)` are empty; a non-empty range halts boot with
   `ErrForeignKeys` and a sample of keys (an unregistered ABO writer is the only way this can
   happen; `keyfamilies_test.go` never proved every writer is registered, `raw/05-storage.md` §3).

### 5S.3.5 Versioned migrations (G2)

```go
// Migration is one forward-only step of one keyspace. There is no Down.
type Migration struct {
	Version int    // 1..n, contiguous per keyspace
	Name    string
	Compat  Compat // Additive or Breaking; zero value rejected at registration
	Format  int    // required when Compat == Breaking: the storage format this step needs (§5S.3.7)
	Up      func(ctx context.Context, tx *MigrationTx) error
}

type Compat uint8
const (
	CompatInvalid Compat = iota
	Additive // an older build that does not know this step can still read and write the keyspace
	Breaking // an older build must refuse the keyspace; only allowed in a release that raises the storage format
)

// MigrationTx is an indexed batch over the keyspace (plus its legacy claims),
// with a hard size cap. Reads see the step's own writes. The runner commits
// tx + version bump + applied record as ONE Pebble batch.
type MigrationTx struct{ /* unexported */ }
func (tx *MigrationTx) Keyspace() Keyspace       // reads go through the tx
func (tx *MigrationTx) Legacy() LegacyRange      // reads/writes go through the tx
func (tx *MigrationTx) Set(key, value []byte) error
func (tx *MigrationTx) Delete(key []byte) error
func (tx *MigrationTx) Log() *slog.Logger

var ErrMigrationTooLarge = errors.New("storage: migration step exceeds core.storage.migration.max_batch_bytes; make it a DataMigration")
var ErrKeyspaceTooNew    = errors.New("storage: keyspace has a Breaking step this build does not know")
```

Runner rules:

| Rule | Detail |
|---|---|
| Order | Core keyspaces in R4 layer order (`c/storage`, `c/config`, `c/auth`, `c/rbac`, `c/tasks`, …), then modules in manifest `DependsOn` topological order, then by module ID. Within a keyspace, by `Version`. |
| Bookkeeping | `c/storage/ks/<prefix>/version` (uint64) and `c/storage/ks/<prefix>/applied/<v>` (`{name, compat, applied_at, build}`). The books keyspace uses a **version-source adapter** over `preference:db_version` / `preference:migration_<n>` (§5S.9.3), so the previous ABO release keeps seeing the numbers it wrote. |
| Atomicity | Step writes + version + record in one batch → no replay window, so idempotency is no longer what makes a crash safe (it remains a test, belt and braces). |
| Size cap | `MigrationTx` refuses past `core.storage.migration.max_batch_bytes` (default 64 MiB) or `max_batch_ops` (default 250 000) with `ErrMigrationTooLarge`; the author moves the work to a `DataMigration`. |
| Pre-migration checkpoint | If any step is pending, one Pebble checkpoint is taken first into `<data-dir>/.checkpoints/premigrate-<utc>/` (same device enforced, hard links, `WithFlushedWAL`); kept for `core.storage.migration.checkpoint_keep` boots. If the checkpoint fails, boot halts before any step runs. |
| Failure | A failed `Up` discards its batch, logs the step, and `app.New` returns an error wrapping `ErrMigrationFailed{Keyspace, Version}`; the process exits non-zero. Earlier steps stay committed (each is atomic and forward-only); the checkpoint is the rollback. No partial step is ever visible. |
| Unreadable version | Fail closed (`ErrBookkeepingUnreadable`), never "assume 0" (contrast `migrations.go:582`). |
| Newer version on disk | Unknown applied steps all `Additive` → boot continues with a warning (G5: an older build reads a newer DB). Any unknown `Breaking` step → `ErrKeyspaceTooNew`. |
| Systemd budget | Each step extends the start timeout (kept from `migrations.go:559`) through `health`'s notifier. |

### 5S.3.6 Data migrations (large, resumable)

```go
// DataMigration rewrites a large key range in many batches. It is executed by
// the core task kind "core.storage.migrate" (runner in core/internal/storagemigrate,
// layer 3, which imports both storage and tasks; storage itself does not import tasks).
type DataMigration struct {
	ID          string // "<keyspace>.<name>", unique, stable forever
	Description string
	Source      []Range        // absolute ranges in the owner's keyspace/claims to visit
	Shards      Shards         // required; zero value rejected (mirror of tasks.Parallelism, R7)
	Mode        Mode           // BootBlocking or Online; zero rejected
	RaisesFormat int           // >0: this migration is a storage-format cut-over (§5S.3.7)
	Step        func(ctx context.Context, kv KV, w *StepWriter) error
	Verify      func(ctx context.Context, r Reader) (VerifyReport, error) // invariant gate, run after the last shard
	Barrier     int            // keyspace Version that may not run before this completes (0 = none)
}

type Range struct{ Start, End []byte }

// Shards partitions Source into disjoint key ranges processed concurrently.
// Disjointness is what makes parallel workers safe (CLAUDE.md concurrency rule):
// no two workers ever touch the same key.
type Shards struct{ n int; reason string }
func ShardsCPU() Shards                         // runtime.NumCPU()
func ShardsFixed(n int) Shards                  // n >= 1
func Sequential(reason string) Shards           // 1, with a mandatory reason string

type Mode uint8
const (
	ModeInvalid Mode = iota
	BootBlocking // runs before any module starts serving; a status listener reports progress (05-health)
	Online       // runs as a background task while serving; owner must declare a Compat read adapter
)

// StepWriter accumulates a shard's writes; the runner commits them together
// with the shard's checkpoint (last key processed) in ONE batch, every
// core.storage.data_migration.chunk_bytes (default 4 MiB) or chunk_items (default 2 000).
type StepWriter struct{ /* unexported */ }
func (w *StepWriter) Set(key, value []byte) error
func (w *StepWriter) Delete(key []byte) error
func (w *StepWriter) Hold(key []byte, reason string) // record and skip; held items listed, never silently dropped
func (w *StepWriter) Count(outcome string)           // report counters ("converted", "already-current", ...)

type VerifyReport struct {
	Read, Written, Held int64
	Failures []string // bounded sample
}
```

Execution contract (built on [05-tasks](05-tasks.md)): one task run per `DataMigration`; task
`Resource` = `{Module: owner, Kind: "storage-range", ID: migration.ID}` (G8); per-shard checkpoint at
`c/storage/dm/<id>/shard/<n>` written in the same batch as the shard's writes, so a resume restarts
exactly after the last committed key; state `c/storage/dm/<id>` ∈ {pending, running, verifying,
done, failed, held}. **Dry run** (`tasks` preview): each chunk is written into an indexed batch that
is discarded; the report lists counts per outcome, the first N keys per outcome and the bytes that
would be written; nothing is persisted except the preview report. Cancellation stops at a chunk
boundary. A failed `Verify` leaves state `failed` with the report; `Barrier` versions stay blocked,
so later versioned migrations cannot run on a half-converted keyspace. `Hold` mirrors the
storage-efficiency plan's held list (B6c, `2026-10-03-storage-efficiency-plan.md:170`).

### 5S.3.7 Storage format and cut-over (resolves the `RunCutover` reference)

The install-wide storage format is for physical changes an older build cannot read. Core keeps the
ABO mechanism and its on-disk names exactly, because the previous ABO binary and `make rollback`
read them (`Makefile:871-903`):

| Item | Location (unchanged) | Core owner |
|---|---|---|
| Stamp | `preference:storage_format` (exact-key claim, owner `core/storage`) | `storage` |
| Marker | `preference:storage_migration` (exact-key claim) | `storage` |
| Sidecar | `<db path>.storage-format` | `internal/pebble` |
| Checkpoint record | `<db path>.migration-checkpoint` | written by the cut-over runner (finally has a writer) |
| Pebble format pin | `FormatValueSeparation`, ratchet after checks | `internal/pebble` (one constant for main and auxiliary DBs) |

`RunCutover` is realised as: *the kernel runs every pending `DataMigration` with
`RaisesFormat > current` in `BootBlocking` mode, before module services start*, following the B6a
sequence: open in cut-over mode (refuses only too-new) → checkpoint beside the store (same device)
→ write the `.migration-checkpoint` record → raise stamp + sidecar + marker **before the first legacy
delete** → convert → `Verify` → clear the marker. A `Breaking` versioned migration must name the
format it needs; registration fails if the build's `SupportedStorageFormat` is lower. In ABO the
comments at `storage_format.go:125-129` and `pebble_store.go:597-619` are deleted together with
`openForCutover` when the books module moves onto core; the storage-efficiency release B becomes a
books `DataMigration{RaisesFormat: 2}`.

### 5S.3.8 Full-text index contract (`storage/index`)

```go
package index

// Spec declares one named index owned by a module.
type Spec struct {
	Name           string  // "books"; on disk <data-dir>/index/<module>/<name>.bleve
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
	// the entity write; the reconciler indexes it later. This is the only write
	// path modules use (G4: the index never depends on an in-memory event).
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
marker and rebuilding marker as **siblings** of the index directory (`bleve_index.go:176-184,252-260`),
open failure = `Degraded` not boot failure (`register.go:42-60`), mapping change = drop + rebuild.
The dirty set lives at `c/index/<module>/<name>/dirty/<id>` (generalising `idx:sidx:dirty:`,
`pebble_store_search_dirty.go:39`) and is drained by a core reconciler (generalising
`internal/server/search_reconciler.go`) as a `tasks` definition with `Parallelism` bounded by
`core.index.reconcile.workers`. Backups use bleve's online `CopyTo` (`bleve/v2 index.go:350-354`) —
[05-backup](05-backup.md). ABO's query parser and per-user field routing (`internal/search/query_*.go`,
`per_user_*.go`) are books code that compiles to `index.Query` or to `QueryString`.

### 5S.3.9 Derived indexes (memdb, hnsw, chromem stay in books)

Decision with evidence: go-memdb (`MemStore`, tables are book tables, `memdb_schema.go:12-21`),
coder/hnsw (dedup layer 2 over `emb:` rows, `hnsw_embedding_store.go:98`) and chromem-go (opt-in
brute-force alternative, `registry_wire.go:71-76`) each have exactly **one** consumer, the
audiobook dedup/query paths. Under the promotion rule (03 §3.4) they stay app-level. What *is*
generic is their lifecycle, which core owns:

```go
// DerivedIndex is anything rebuilt from storage: in-memory tables, ANN graphs,
// caches with an on-disk warm-start snapshot.
type DerivedIndex struct {
	Name     string                                   // "books.memdb", "books.hnsw"
	Version  string                                   // a change forces a rebuild
	Build    func(ctx context.Context) error          // full rebuild; run as a tasks definition
	Snapshot func(ctx context.Context, dir string) error             // optional warm-start export (hnsw ExportWithTruth)
	Load     func(ctx context.Context, dir string) (accepted bool, err error) // must verify against storage truth (hnsw ARCH-1, server.go:845-866)
	Blocking bool                                     // true: readiness waits for it (05-health)
}
```

The kernel records `c/storage/derived/<name>` = `{version, state, built_at}`, triggers `Build` on
version change, on `Load` refusal and after every restore ([05-backup](05-backup.md) §5B.3.4).
Snapshots go to `<data-dir>/derived/<name>/` (ABO keeps `hnsw/` via a legacy path) and are backup
class `Cache`.

### 5S.3.10 Auxiliary databases

```go
// OpenAuxiliary opens a separate Pebble DB for re-creatable bulk data a module
// owns (the Open Library dump mirror). Same format pin and close gate as the
// main DB; never system of record; backup class Recreatable by default.
// Exposed to modules through module.Deps; path <data-dir>/aux/<module>/<name>
// unless the module passes a legacy path.
func (h AuxOpener) OpenAuxiliary(ctx context.Context, name string, opts AuxOptions) (*Aux, error)

type AuxOptions struct{ LegacyPath string; CacheBytes int64 }
type Aux struct{ /* Keyspace-shaped API over the whole aux DB */ }
func (a *Aux) Root() Keyspace
func (a *Aux) Close() error
```

This closes TASK-A7's open item for `internal/openlibrary/store.go:43` (`FormatNewest`) by
construction. `NewAIScanStore` (`ai_scan_store.go:156`, no non-test caller) is deleted, not ported.

## 5S.4 Lifecycle

```
kernel.Open(storage)
  1 resolve path (core.storage.path; ABO: database_path)            fail → boot error
  2 pebble.Open with no format version → read stamp, marker, sidecar  too new → refuse (StorageFormatTooNewError)
  3 pending RaisesFormat data migrations? → cut-over mode (§5S.3.7) else serve mode refuses older/marked
  4 ratchet to the pinned Pebble format
  5 validate specs: keyspaces, claims, migrations, data migrations     conflict → boot error (all conflicts listed)
  6 first boot on this DB? create c/storage/install {install_id, created, build}; check c/ and m/ empty
  7 any pending versioned step? → pre-migration checkpoint
  8 core migrations → module migrations (§5S.3.5)                       failure → boot error, step discarded
  9 BootBlocking data migrations (status listener up, 05-health)       failure → boot error, state kept for resume
 10 derived indexes: versions checked; Build tasks queued; Blocking ones gate readiness
 11 issue Handles to core subsystems and modules (module.Deps)
 12 Online data migrations and the index reconciler start with the task executor
App.Run ends → executor drained (05-tasks) → handles invalidated → gate drain (core.storage.close_timeout)
             → WAL sync → engine close (GatedDB semantics, pebble_gate.go:600-680)
```

A disabled module (G1) still has its claims validated and its keyspace reserved; it gets no handle,
runs no migrations, and its data is retained. Its pending migrations run the next time it is enabled.

## 5S.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Stamp or sidecar newer than build | closed (refuse open) | Older build would misread newer format (kept from `pebble_store.go:670-680`). |
| Store older / marker present in serve mode | closed | A half-converted store must not serve; the cut-over runner resumes it. |
| Sidecar unreadable | open (rewrite from stamp) + error log | Stamp is authoritative; kept from `pebble_store.go:651-660`. |
| Claim conflict, keyspace collision, invalid zero-valued `Compat`/`Mode`/`Shards` | closed (boot) | Ownership ambiguity is a data-corruption risk; registration-time is the cheapest place to catch it. |
| Keys found in `c/` or `m/` on first boot | closed (boot), with sample | Proves the static collision check; an unknown writer must be investigated, not overwritten. |
| Write outside keyspace/claims | closed (`ErrOutsideKeyspace`) | R8. Shadow mode (count + log, allow) exists only for books' `LegacyRange` for one release (§5S.9.4). |
| Write to `ClaimReadOnly`/`ClaimRetired` | closed (`ErrReadOnlyClaim`) | Adapters must not resurrect retired data. |
| Migration bookkeeping unreadable | closed (boot) | Replaces the fail-open at `migrations.go:582`. |
| Migration step error / `ErrMigrationTooLarge` | closed (boot), batch discarded | G2. |
| Pre-migration checkpoint fails (incl. cross-device) | closed (boot) before any step | Without the checkpoint there is no rollback; `ensureSameDevice` rationale `backup.go:557-598`. |
| Unknown `Breaking` step on disk | closed (`ErrKeyspaceTooNew`) | Older build must not write a keyspace whose meaning changed. |
| Unknown `Additive` step on disk | open + warning | G5: previous release keeps working. |
| BootBlocking data migration fails `Verify` | closed (boot), state `failed`, held list kept | Same as B6a "stop states". |
| Online data migration fails | open (app serves through the read adapter); task `failed`; health `degraded` | The old representation is still authoritative while `ClaimMigrating`. |
| Full-text index open fails | open (`Degraded`; callers fall back) | Kept from `search/register.go:38-60`. |
| Index dirty mark fails | closed (the whole entity batch fails) | It is in the same batch; partial commit impossible. |
| Derived index `Load` rejects snapshot | open (rebuild) | Snapshot is an accelerator only (hnsw ARCH-1). |
| `Get` on missing key | `ErrNotFound` (not an error log) | Normal control flow. |
| Iterator/scan I/O error | yielded once, iteration stops | Callers must check; `iter.Seq2` makes it visible at the call site. |
| Close timeout with leases outstanding | closed (engine not closed; error returned; process exits) | Closing under a lease is use-after-free in Pebble; kept from `pebble_gate.go:600-680`. |

## 5S.6 Concurrency contract

- `DB`, `Keyspace`, `LegacyRange`, `Snapshot`, `Sequence`, `index.Index` are goroutine-safe.
  `Batch` and `Iterator` are **not**: one goroutine each (as in Pebble).
- Every call takes `context.Context`; a cancelled context fails the call before engine work starts.
  Engine calls themselves are not interruptible; scans check `ctx.Err()` every step.
- Every engine call holds a gate lease (the `GatedDB` design moves into `internal/pebble`); `Close`
  waits up to `core.storage.close_timeout`.
- Snapshot and iterator lifetimes pin memtables/SSTs. Rule: a pass longer than
  `core.storage.snapshot_max_age` (default 30 s, the same refresh interval as the exact census,
  `census_exact.go:40-45`) must re-open its iterator from its last key; the runner of data
  migrations and the census do so; `mediacore_storage_snapshot_oldest_seconds` alarms on violations.
- Data migrations and index rebuilds are bounded worker pools (`Shards`, `tasks.Parallelism`) over
  **disjoint key ranges**; there is no shared mutable state between shards except counters
  (atomics). Online data migrations take the task `Resource` lock; the owner's dual-write path
  takes the same per-key stripe lock as its normal writes, so a row is never converted while being
  rewritten (the converter re-reads inside the indexed batch).
- `Sequence.Next` is lock-free inside a lease; a lease refill holds a per-sequence mutex.
- Batch size: `Commit` refuses batches over `core.storage.max_batch_bytes` (default 256 MiB) with
  `ErrBatchTooLarge`, so a runaway writer cannot stall the WAL.
- Tests use `testing/synctest` for snapshot-age and lease-drain timing, and `vfs.NewCrashableMem`
  + `CrashClone` for crash points (pebble v2 `vfs/mem_fs.go:61,140`).

## 5S.7 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.storage.path` | string (path) | `<data-dir>/storage/main.pebble`; ABO maps `database_path` | no |
| `core.storage.cache_bytes` | int64 | 512 MiB | no |
| `core.storage.memtable_bytes` | int64 | 64 MiB | no |
| `core.storage.max_batch_bytes` | int64 | 256 MiB | yes |
| `core.storage.close_timeout` | duration | 30s | yes |
| `core.storage.snapshot_max_age` | duration | 30s | yes |
| `core.storage.claims.mode` | enum `strict`/`shadow` | `strict` (books' first release on core: `shadow`) | no |
| `core.storage.migration.max_batch_bytes` | int64 | 64 MiB | no |
| `core.storage.migration.max_batch_ops` | int | 250000 | no |
| `core.storage.migration.checkpoint_keep` | int (boots) | 3 | yes |
| `core.storage.data_migration.chunk_bytes` | int64 | 4 MiB | yes |
| `core.storage.data_migration.chunk_items` | int | 2000 | yes |
| `core.storage.adopt.max_atomic_bytes` | int64 | 64 MiB | no |
| `core.storage.adopt.max_atomic_keys` | int | 200000 | no |
| `core.index.reconcile.workers` | int | `runtime.NumCPU()` | yes |
| `core.index.reconcile.interval` | duration | 5s | yes |

Environment overrides for Pebble open settings (TASK-A7's intent) are the generic env layer of
[05-config](05-config.md); none of these keys is DB-persisted (they are read before the DB opens).

## 5S.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_storage_ops_total` | counter | `keyspace`, `op` (get, set, delete, scan, commit) |
| `mediacore_storage_op_duration_seconds` | histogram | `op` |
| `mediacore_storage_batch_bytes` | histogram | `keyspace` |
| `mediacore_storage_keyspace_bytes` | gauge (estimate, refreshed 5 min) | `keyspace` |
| `mediacore_storage_claim_violations_total` | counter | `owner`, `mode` (refused, shadow) |
| `mediacore_storage_snapshots_open` | gauge | — |
| `mediacore_storage_snapshot_oldest_seconds` | gauge | — |
| `mediacore_storage_migrations_applied_total` | counter | `keyspace` |
| `mediacore_storage_migration_duration_seconds` | histogram | `keyspace` |
| `mediacore_storage_data_migration_items_total` | counter | `migration`, `outcome` |
| `mediacore_storage_data_migration_state` | gauge (enum as 0/1 per state) | `migration`, `state` |
| `mediacore_storage_format` | gauge | — |
| `mediacore_storage_pebble_*` | (the existing Pebble collector's series, renamed) | as today (`internal/metrics/pebble_collector.go`) |
| `mediacore_index_docs` | gauge | `module`, `index` |
| `mediacore_index_dirty` | gauge | `module`, `index` |
| `mediacore_index_reconcile_lag_seconds` | gauge | `module`, `index` |
| `mediacore_storage_derived_state` | gauge | `name`, `state` |

`keyspace` cardinality is bounded by the number of modules + core subsystems (< 40).

## 5S.9 Migration from ABO

### 5S.9.1 Ownership of the 258 families

| Group | Families | Owner after books-on-core | Mechanism |
|---|---|---|---|
| Auth/identity: `u:`, `role:`, `sess:`, `apikey:`, `invite:`, `inviteallow:`, `oauthid:`, `guestcftoken:` + `idx:` sub-ranges `idx:user:`, `idx:invite:`, `idx:role:`, `idx:sess:`, `idx:apikey:`, `idx:oauth:` | 8 + 6 sub-ranges | `core/auth`, `core/rbac` | adopt (copy + dual-write) |
| Config: `setting:`, `preference:`, `pref:` | 3 | `core/config` | adopt; carve-outs below |
| Carve-outs inside adopted families: `setting:repairs_last_apply_op:`, `setting:repairs_last_plan_op:`, `pref:_system:` and its 6 children; exact keys `preference:db_version`, `preference:migration_` (prefix) | 9 families + 2 | `books` (repairs, organizer, write-back outbox, iTunes, migration bookkeeping) | in place |
| Storage format: `preference:storage_format`, `preference:storage_migration` | 2 exact keys | `core/storage` | in place forever (rollback guard reads them) |
| Ops v1/v2/v3: `operation:`, `operationlog:`, `opstate:`, `opsummary:`, `op:` (+3), `opv2:` (+14), `opv3:` (+19) | 42 | `core/tasks` | read in place; new runs in `c/tasks/` |
| Activity/system log: `act:` (+12), `syslog:` | 14 | `core/log` (non-audit tiers), `core/audit` (audit tier) — row shapes in [05-logging](05-logging.md), [05-rbac](05-rbac.md) | read in place; new rows in `c/log/`, `c/audit/` |
| Everything else (books, authors, files, fingerprints, dedup, catalog, metadata caches, playback, playlists, decisions, `counter:`, `idx:` remainder, `met:`, `aijob*`, `provider_*`, `system:*`, `cursor:` …) incl. the bare keys `narrator_counter`, `sync_alias_use_seed_cutoff` (exact-key claims) | 182 | `books` | `ClaimReadWrite` (the 10 + 3 the registry marks legacy/retired: `ClaimReadOnly` or `ClaimRetired`) |

Total: 8 + 3 + 9 + 42 + 14 + 182 = 258. `met:` (metrics snapshots), `aijob*`/`aijournal:`,
`provider_daily_budget:`/`provider_throttle:` and `system:census:` are generic-looking but stay
books claims until their owning spec ([05-metrics](05-metrics.md), [05-ai](05-ai.md),
[05-metadata](05-metadata.md)) specifies its own adoption; `system:census:` is a recomputable cache and
is simply not migrated (core's census writes `c/storage/census/`). `opstate:<opID>:params` is live
(backfill chunk journal, `keyfamilies.go` comment at line 269) and must be in the read-write part of
the tasks adapter, not `ClaimReadOnly`.

The books claim list is **generated** from `keyfamilies.go` into
`internal/books/storage_claims.go` (ABO side) by a `go generate` tool and checked by a test that
the two agree; `keyfamilies.go` is retired once the generator owns the list. The core census
(`storage.Admin.Census`) partitions by claims + keyspaces (successor of
`census_families.go`/`census_exact.go`), so `GET /api/core/v1/storage/census` replaces the books
endpoint and keeps its shape.

### 5S.9.2 Adopt vs read in place, per family

Production counts are unknown to this spec. The design does not depend on them: sizing decides
only *how* an adoption runs, never *whether* it is correct.

| Family group | Expected size (reasoning from code) | Decision | Why |
|---|---|---|---|
| users `u:` + `idx:user:` | one row per user; single-tenant home install (D7) → tens to hundreds | adopt | tiny; core auth must own it (05-authn) |
| roles `role:` + `idx:role:` | seed roles (`internal/auth/seed.go:86`) + custom → < 100 | adopt | tiny |
| sessions `sess:` + `idx:sess:user:` | one per live login; a 10-minute cleanup ticker removes expired/revoked (`server_lifecycle.go:529-530`) → bounded by active sessions | adopt (transform per 05-authn: sessions are stored raw today, `pebble_store_auth.go:1657`) | tiny; credentials belong to core |
| API keys `apikey:` + `idx:apikey:`; invites `invite:`, `inviteallow:`, `idx:invite:`; OAuth `oauthid:`, `idx:oauth:`; `guestcftoken:` | handful each | adopt | tiny |
| settings `setting:` | ~251 config keys (`raw/12-cross-cutting.md` §2) + one `config_blob` JSON + secret rows (ciphertext moves byte-identical) | adopt | small; 05-config owns the layered loader |
| preferences `preference:` (global) and `pref:` (per user, minus `pref:_system:`) | global: tens (incl. 66 `migration_<n>`); per user: tens × users | adopt (reserved keys stay, §5S.9.1) | small |
| ops v1 (`operation*`, `opstate:`, `opsummary:`) | one row per historical op + logs; legacy since v2 | read in place, `ClaimReadOnly` except `opstate:*:params` | write-dead; copying history buys nothing |
| ops v2/v3 (`opv2:*`, `opv3:*`, `op:*`) | runs × (logs + per-item outcomes + frozen item-key pages, `opv3:snap:`, `opv3:item:`) — library-scale per run | read in place | potentially GBs; copying would be a long migration of data that only ages out |
| activity `act:*`, `syslog:` | tiered log incl. debug tier, retention-pruned | read in place | append-only, large, retention already deletes it |

**Adoption procedure** (one per group; core migration in the adopting subsystem's keyspace):

1. *Measure* inside the migration: iterate the source keys **keys-only**, summing key+value bytes,
   stopping as soon as `core.storage.adopt.max_atomic_bytes` or `max_atomic_keys` is exceeded.
2. *Under the cap*: one versioned migration step copies (and, if the owning spec says so,
   transforms) every row into `c/<sub>/…` in one atomic batch with the version bump. The legacy
   claim flips to `ClaimMigrating`.
3. *Over the cap*: the same copy function is registered as an `Online` `DataMigration`
   (`Shards: ShardsCPU()`); until it is `done`, the subsystem reads **new-first, legacy-fallback**
   through its `Compat` adapter. No code change is needed for either outcome.
4. *Compatibility window* (two minor releases, 02 Q5): every write goes to the new row **and** to
   the legacy row (same batch, legacy encoding), so the previous release — which reads only legacy
   keys — sees current users, passwords, settings (G5). Reads use only the new rows.
5. *Window end*: a `Breaking` migration deletes the legacy rows and flips the claim to
   `ClaimRetired`; it ships in a release that raises `SupportedStorageFormat`, so `make rollback`
   refuses to go back across it.

Cost surfaced: step 4 means dual-write code in `auth` and `config` for two releases (same shape as
ABO's opv2→opv3 mirror, `pebble_store_ops_v3.go:28-45`).

**Read-in-place procedure** (tasks, log, audit): the subsystem receives a `LegacyRange` over the
families and a read adapter that decodes legacy rows into the new types for list/detail/census
views; new rows go only to `c/…`. Retention runs over both. Rollback to the previous release
loses visibility of runs/activity created after the upgrade; 05-tasks specifies draining
non-terminal runs before a rollback.

**Read-only command for the owner** (gives per-family key counts and bytes; the estimated census
reads sstable properties and an in-memory cache only, `census.go:317-372`):

```bash
curl -sS -H "Authorization: Bearer <redacted-token>" \
  "https://<host-a>/api/v1/diagnostics/db-census?fresh=true" \
| jq '.data.families[]
      | select(.prefix | test("^(u:|role:|sess:|apikey:|invite|oauthid:|guestcftoken:|idx:|setting:|preference:|pref:|act:|syslog:|operation|opstate:|opsummary:|op:|opv2:|opv3:)"))
      | {prefix, keys, raw_key_bytes, raw_value_bytes, disk_bytes}'
```

`idx:` is one family in the census, so its sub-ranges are not split; they are bounded by their
parent rows. Do **not** use `diagnostics query --raw` for this: it prints values, and session rows
hold bearer credentials.

### 5S.9.3 Code moves

| ABO code | Becomes |
|---|---|
| `internal/database/pebble_gate.go` (`GatedDB`, leases, drain/seal) | `core/internal/pebble` gate |
| `storage_format.go`, `pebble_store.go:597-700` (open/init split, format pin, sidecar) | `core/internal/pebble` + kernel; ABO copies deleted |
| `keyfamilies.go`, `census*.go` | books claim list (generated) + `storage.Admin.Census` |
| `migrations.go` runner + `migration_bookkeeping.go` | core runner; books' 66 migrations stay ABO code registered as books `Migration`s (versions 1..66, `Compat: Additive`) over the version-source adapter |
| `ReconcileOpsV3WithV2` boot step (`store.go:1862-1870`) | 05-tasks legacy adapter (a boot hook of the tasks subsystem) |
| `NewPebbleStore(path)` / `InitializeStore` / `globalStore` | books module constructor `database.NewPebbleStore(h storage.Handles)`; `globalStore` deleted |
| `internal/database` call sites of `p.db.*` (`GatedDB` method set) | a thin ABO adapter `internal/database/legacyrange.go` mapping the used subset (Get/Set/Delete/DeleteRange/NewIter/NewBatch/NewIndexedBatch/NewSnapshot/LogData) onto `storage.LegacyRange`; admin calls (Checkpoint, Compact, Metrics, SSTables) move to core `storage.Admin` users (backup, health, metrics) |
| `nextID` / `counter:` | ABO keeps `counter:<name>` (claimed) for existing entity IDs; new core rows use `storage.Sequence` |
| `internal/search` (bleve index, reconciler, dirty set) | `core/internal/bleveindex` + `storage/index`; books keeps document building and query parsing |
| `MemStore`, `HNSWEmbeddingStore`, `ChromemEmbeddingStore` | stay in books; registered as `DerivedIndex` |
| `internal/openlibrary` OL store | books, opened via `OpenAuxiliary(…, LegacyPath: <openlibrary-dumps>/oldb)` |
| `ai_scan_store.go` standalone constructor | deleted |

### 5S.9.4 Rollout order (summary; 09 owns the waves)

1. Core `storage` lands with zero modules (G7 harness); `coretest` opens an in-memory DB.
2. Books moves onto `LegacyRange` with claims in **shadow** mode for one release; every
   `mediacore_storage_claim_violations_total` hit is fixed by registering or correcting the writer.
   ABO's full test suite also runs in strict mode in CI from the start.
3. Strict mode. Then adoptions (auth → rbac → config), one release each, each with its window.

## 5S.10 Tests

| Test | Proves |
|---|---|
| `TestKeyspaceWritesCannotEscapePrefix` | relative keys always land under `Prefix()` incl. `Sub`, `DeleteRange` bounds |
| `TestLegacyRangeRefusesUnclaimedAndForeignKeys` | R8 for absolute keys, longest-match resolution, carve-outs |
| `TestClaimOverlapFailsBoot` / `TestClaimCarveOutAccepted` | overlap rules, multi-owner conflicts listed together |
| `TestABOClaimsDoNotTouchCoreOrModulePrefixes` | all 258 generated claims vs `m/`, `c/` (static) |
| `TestFirstBootRefusesKeysUnderCoreAndModulePrefixes` | runtime `ErrForeignKeys` |
| `TestBatchAtomicAcrossBoundKeyspaces` | outbox/dirty-mark in same commit; crash via `CrashClone` shows all or nothing |
| `TestMigrationStepAtomicWithBookkeeping` | crash at every point of a step: either nothing or step+version+record |
| `TestMigrationFailureHaltsBootAndLeavesStepUncommitted` | G2 |
| `TestPremigrationCheckpointTakenOnceAndOpenable` | checkpoint opens read-only and matches pre-step data |
| `TestMigrationTooLargeRefused` | cap enforcement |
| `TestUnreadableVersionFailsClosed` | contrast with ABO fail-open |
| `TestOlderBuildAcceptsNewerAdditiveSteps` / `TestOlderBuildRefusesNewerBreakingStep` | G5 vs `ErrKeyspaceTooNew` |
| `TestBreakingMigrationRequiresFormat` | registration rule |
| `TestDataMigrationResumesExactlyAfterLastCommittedKey` | crash between chunks; no item processed twice or skipped |
| `TestDataMigrationShardsDisjoint` (`-race`) | parallel shards never touch the same key |
| `TestDataMigrationDryRunWritesNothing` | preview leaves the DB byte-identical except the report |
| `TestDataMigrationVerifyFailureBlocksBarrier` | later versions cannot run |
| `TestCutoverRaisesStampBeforeFirstDelete` | B6a ordering; previous build refuses the half-converted store |
| `TestAdoptionUnderCapIsAtomic` / `TestAdoptionOverCapBecomesOnlineDataMigration` | either production answer works |
| `TestAdoptionDualWriteKeepsLegacyReadable` | previous release's reads see current data during the window |
| `TestSequenceNeverReusesAfterCrash` | lease burn semantics |
| `TestSnapshotAgeMetricAndRefresh` (`synctest`) | long-pass rule |
| `TestCloseWaitsForLeases` (`synctest`) | gate drain/seal |
| `TestIndexMarkDirtyInSameBatch` / `TestIndexReconcilerDrainsAfterCrash` | G4 for search |
| `TestIndexOpenFailureDegrades` / `TestIndexMappingChangeRebuilds` | kept ABO behaviours |
| `TestDerivedIndexRebuiltAfterVersionChangeAndRestore` | lifecycle |
| `TestAuxiliaryDBUsesPinnedFormat` | no `FormatNewest` |
| `BenchmarkKeyspaceScan`, `BenchmarkBatchCommit` | overhead of the prefixing layer vs raw Pebble (< 5 % target) |

## 5S.11 Open items

1. **Claim generator source of truth**: the generator reads `keyfamilies.go`; once retired, the
   books claim list is hand-maintained Go. Decide in 09 whether the db-census "unregistered" view
   stays as a CI check over a production census snapshot.
2. **Session representation** at adoption (hash vs raw) — 05-authn; storage supports both
   (transform step).
3. `met:` (metrics snapshots) size and owner — 05-metrics; default stays a books claim.
4. Whether `codec.JSON` uses `encoding/json/v2` depends on its status in the toolchain at
   implementation time (record in [13-toolchain](13-toolchain.md)); the tag byte makes a later
   switch a per-value change.
5. 02 §2.4 G2 says "checkpoint before each module". This spec takes **one** checkpoint per boot
   before the first pending step: it captures every keyspace at once and is the only restore point
   anyone would use. If the decision record means per-module restore points, that needs a
   per-keyspace restore tool, which does not exist; flagged for 02's author.
6. The exact `storage.Admin` surface (Checkpoint, Compact, Metrics, Census, EstimateDiskUsage)
   is shared with [05-backup](05-backup.md) and [05-health](05-health.md); listed there as consumers.

## 5S.12 Objections to 03

None that change a name or rule. One clarification: R4 puts `storage` (layer 2) below `tasks`
(layer 3), so the data-migration *runner* cannot live in `storage`; it lives in
`core/internal/storagemigrate` (layer 3), and `storage.Shards` mirrors `tasks.Parallelism` to keep
R7's "no usable zero value" property without an upward import.

## 5S.13 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| `getCurrentVersion` treats a read *error* on `db_version` as version 0, so a transient read failure re-runs all 66 migrations. | `internal/database/migrations.go:579-585` |
| `diagnostics query --raw` is documented as read-only but calls `pebble.Open` without `ReadOnly: true` (WAL replay can write), and it prints value previews, which for `sess:` are bearer credentials. | `cmd/diagnostics.go:262-300`; `internal/database/pebble_store_auth.go:1657` |
| `migrations.go` comments still say 398 methods / 61 migrations (now 445 / 66). | `migrations.go:17-24`; `raw/04-database-store.md` §7 |
