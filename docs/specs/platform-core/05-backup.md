<!-- file: docs/specs/platform-core/05-backup.md -->
<!-- version: 1.0.0 -->
<!-- guid: 604febb0-a09e-4cf9-99ee-5f3722534da6 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Backup and restore (`backup`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) §5 (backup and restore) and §1
(engines), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7 (secret storage).
Storage terms (`storage.Admin`, keyspaces, format stamp, derived indexes, auxiliary DBs) are defined
in [05-storage](05-storage.md); the secrets master key in [05-config](05-config.md). `backup` is
layer 3 (R4).

## 5B.0 Decisions in one table

| # | Decision |
|---|---|
| B1 | A backup is one **archive with a manifest** covering every core-owned store by class: the Pebble checkpoint (system of record), module-declared system-of-record artifacts, the config file, and — only inside an encrypted archive — the secrets master key. Derived stores (bleve, hnsw snapshots) and re-creatable stores (Open Library mirror) are excluded by default and rebuilt; they can be included as warm-start accelerators that are accepted only after verification. |
| B2 | **Restore is staged and applied at the next boot, before storage opens.** The API verifies and extracts into a staging directory on the database's filesystem and writes a pending-restore intent; the kernel swaps directories by rename, keeps the previous data as an undo point, then opens storage normally (migrations bring an older backup forward). No code path ever writes into a live store. |
| B3 | Archives are **encrypted by default** (age, X25519 recipients: the install's backup identity plus optional operator recovery recipients), because every ABO backup holds bearer credentials and plaintext secrets today. An unencrypted archive requires an explicit setting and never contains the master key. |
| B4 | Verification is part of creation (decrypt, per-entry SHA-256, open the checkpoint read-only, compare key counts) and runs again on a schedule; retention never deletes the newest **verified** archive. |
| B5 | Old ABO archives (`audiobooks_pebble_<ts>.tar[.gz|.zst]` + `.sha256`) stay listable, verifiable and restorable through the same staged path as format `legacy-abo/0`, and can be re-wrapped into encrypted archives. |
| B6 | Create/verify/prune/stage-restore are core **tasks** (`core.backup.*`) with progress from real work, cancellation, and a single exclusive `Resource`. |

## 5B.1 Purpose & scope

In scope: what is backed up, archive format, encryption, creation (scheduled, on demand,
"ensure fresh before a risky operation"), verification, retention, listing, import of an uploaded
archive, staged restore and its undo, compatibility with ABO archives.

Out of scope: media files under library roots (core does not back up media; operators use
filesystem snapshots — [05-library](05-library.md) owns roots); off-site replication (an archive is a
file; copying it elsewhere is the operator's tooling); the secrets store itself ([05-config](05-config.md));
session revocation semantics after restore ([05-authn](05-authn.md) implements the hook this file
calls).

## 5B.2 Current state at HEAD

| Aspect | Today | Evidence |
|---|---|---|
| Scope | The Pebble directory only. bleve, `hnsw/`, the Open Library store, the `.storage-format` sidecar, the config file and the encryption key file are not archived. | `internal/backup/backup.go:283-428`; `raw/05-storage.md` §5 |
| Consistency | Pebble native checkpoint (hard links) staged in `.backup-staging` **on the DB's filesystem**; cross-device staging is refused because Pebble silently byte-copies on EXDEV (a 14 GB silent copy once cancelled a library scan). Live-directory walk is the fallback when the store is not `Checkpointable`. Checkpoint is called without `WithFlushedWAL`. | `backup.go:428-541,544-598`; `internal/database/pebble_store.go:953-955` |
| Format | tar + codec (gzip default at best compression, zstd, none) chosen by name, **sniffed by magic bytes** on restore; `<archive>.sha256` sidecar in `sha256sum` format; names `audiobooks_<dbtype>_<YYYYMMDD_HHMMSS><ext>`; only files with that prefix are ever listed or pruned. | `backup.go:148-156,333,643-700`; `internal/backup/codec.go:48-80,204-245` |
| Retention | `MaxBackups` 10 and `MaxTotalBytes` 40 GiB default; runs **before** the space check (an order bug once left a full disk permanently stuck); the last archive is a floor (never deleted), except `MaxBackups == 0` = keep none. | `backup.go:85-100,188,437-458,1183-1290` |
| Space guard | `diskstats` Bavail with a 2 GiB margin, before checkpointing. | `backup.go:198-268` |
| Progress | phase callback with counters from the real walk (the ops watchdog would otherwise be fooled by a timer); callback error aborts and removes the partial archive. | `backup.go:51-82` |
| Callers | `POST /api/v1/backup/create`, `GET /backup/list`, `POST /backup/restore` (credential-change route, owner-gated), `DELETE /backup/:filename`, all `settings:manage`; organizer auto-backup before organize, skipped if the newest archive is younger than 6 h. | `internal/server/wire_system_routes.go:59-66`; `internal/organizer/service.go:552,697,735-760` |
| **Restore** | `RestoreBackupIn` verifies (only if the request sets `verify`; the JSON default is false) and **extracts straight into the target directory** — by default the DB's parent, i.e. over the live `audiobooks.pebble` — with `os.Create` per entry. The handler never closes, swaps or reopens the open store and never asks for a restart. Files that exist live but not in the archive (newer SSTs, `MANIFEST`, `OPTIONS`) stay, so the result is a mix of two databases under a running engine. | `backup.go:732-775,777-870`; `internal/server/handlers/system/handler.go:823-905`; `raw/05-storage.md` §5 |
| Format interplay | The stamp is inside the checkpoint, the sidecar outside: restoring an older archive leaves a newer sidecar (latent while the format is 1 everywhere). Restored `db_version` makes migrations re-run, safe only because `Up` is idempotent. | `raw/05-storage.md` §5; `pebble_store.go:670-680` |
| Secrets in archives | Session IDs are stored raw and are bearer credentials; several provider/OAuth/download-client secrets sit in the plaintext `config_blob`; only 4 secrets are AES-GCM rows whose key file (`.encryption_key`) is outside the DB and outside the archive. Archives are never encrypted. Restoring onto a host without that key file makes boot refuse (correctly) rather than lose secrets. | `raw/10-auth-rbac.md` §7; `internal/database/pebble_store_auth.go:1656-1663`; `internal/database/settings.go:42-52`; `cmd/root.go:760-790` |
| SQLite remnants | Live-copy path still has SQLite single-file branches (dead). | `backup.go:959,1028,1103` |

## 5B.3 Go API

Package `github.com/falkcorp/mediacore/backup`.

### 5B.3.1 What is in an archive

| Store | Class | Default | How captured | How restored |
|---|---|---|---|---|
| Main Pebble DB | `SystemOfRecord` | always | `storage.Admin.Checkpoint(dir, WithFlushedWAL)` on the DB's filesystem | staged dir renamed into place |
| Storage-format sidecar | metadata | always (as a manifest value) | read stamp | sidecar rewritten from the staged stamp |
| Config file layer (05-config) | `SystemOfRecord` | always | file copy | staged, swapped |
| Secrets master key (05-config) | `Secret` | encrypted archives only | read key file | staged; previous key kept in the undo dir |
| TLS material under `<data-dir>/certs` | `Secret` | encrypted archives only, if present | dir copy | staged |
| Module artifacts declared `SystemOfRecord` | `SystemOfRecord` | always | module `Snapshot` hook | module `Restore` hook into staging |
| Full-text indexes (`storage/index`) | `Derived` | off (`core.backup.include_derived`) | bleve online `CopyTo` (`bleve/v2 index.go:350-354`) | accepted only if mapping version matches and the index reconcile from storage is run; else rebuilt |
| Derived snapshots (hnsw) | `Cache` | off | `DerivedIndex.Snapshot` | `DerivedIndex.Load` must verify against storage truth, else rebuild |
| Auxiliary DBs (Open Library mirror) | `Recreatable` | off (`core.backup.include_recreatable`) | `Aux` checkpoint | staged |
| Bootstrap token, `.backup-staging`, temp files | — | never | — | — |

Consistency rule: the Pebble checkpoint is the point in time of the archive. Every other
system-of-record artifact must be either (a) fully described by Pebble, or (b) append-only /
content-addressed, so capturing it *after* the checkpoint yields a superset that Pebble's view
tolerates; module hooks must state which (`Artifact.Consistency`). Derived and cache classes are
never trusted on restore without verification against the restored Pebble (the hnsw truth-count
rule, `internal/server/server.go:845-866`, generalised).

### 5B.3.2 Types

```go
package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

type ArchiveID string // ULID

type Class uint8
const (
	ClassInvalid Class = iota
	SystemOfRecord
	Secret
	Derived
	Cache
	Recreatable
)

type Consistency uint8
const (
	ConsistencyInvalid Consistency = iota
	DescribedByStorage // artifact content is derivable from Pebble rows
	AppendOnly         // files are immutable once named; superset after checkpoint is safe
)

// Artifact is declared by a module (module.Manifest.Backup) or a core subsystem.
type Artifact struct {
	Name        string // "books.playlists"; unique; archive path artifacts/<owner>/<name>/
	Owner       storage.Owner
	Class       Class
	Consistency Consistency // required for SystemOfRecord
	// Dir: core copies this directory (confined via os.Root). Or Snapshot/Restore
	// hooks for anything that needs the module's cooperation.
	Dir      string
	Snapshot func(ctx context.Context, w ArtifactWriter) error
	Restore  func(ctx context.Context, r ArtifactReader, stagingDir string) error
}

type ArtifactWriter interface {
	Create(rel string, mode fs.FileMode) (io.WriteCloser, error) // writes into the run dir beside the checkpoint
}
type ArtifactReader interface {
	Open(rel string) (io.ReadCloser, error)
	Walk(func(rel string, size int64) error) error
}

type Manifest struct {
	Format         string            `json:"format"`          // "mediacore-backup/1"
	ArchiveID      ArchiveID         `json:"archive_id"`
	App            string            `json:"app"`             // "audiobook-organizer"
	AppVersion     string            `json:"app_version"`
	CoreVersion    string            `json:"core_version"`
	InstallID      string            `json:"install_id"`      // c/storage/install
	CreatedAt      time.Time         `json:"created_at"`
	Reason         string            `json:"reason"`          // "schedule", "manual", "pre:books.organize", "pre-migration"
	StorageFormat  int               `json:"storage_format"`
	PebbleFormat   uint64            `json:"pebble_format"`
	Keyspaces      map[string]KSInfo `json:"keyspaces"`       // prefix -> version, approx keys/bytes
	Modules        map[string]string `json:"modules"`         // module ID -> module version
	Entries        []Entry           `json:"entries"`         // every file: path, size, sha256, class
	Encrypted      bool              `json:"encrypted"`
	Recipients     []string          `json:"recipients"`      // age recipient fingerprints, never keys
	SecretsKeyID   string            `json:"secrets_key_id"`  // fingerprint of the master key in use
	ContainsSecret bool              `json:"contains_secret"` // always true for a Pebble checkpoint today
}

type KSInfo struct {
	Version int    `json:"version"`
	Keys    int64  `json:"keys"`  // exact, counted from the checkpoint during verification
	Bytes   uint64 `json:"bytes"`
}

type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Class  Class  `json:"class"`
}

type Archive struct {
	ID        ArchiveID
	Name      string
	Size      int64
	CreatedAt time.Time
	Format    string // "mediacore-backup/1" or "legacy-abo/0"
	Encrypted bool
	Manifest  *Manifest     // nil for legacy archives until first verification synthesises one
	Verified  *VerifyResult // last verification
}

type VerifyLevel uint8
const (
	VerifyInvalid VerifyLevel = iota
	VerifyChecksum // outer SHA-256 sidecar only (fast)
	VerifyEntries  // decrypt + every entry hash vs manifest
	VerifyOpen     // VerifyEntries + open the checkpoint read-only in a temp dir and count keys per keyspace
)

type VerifyResult struct {
	At     time.Time
	Level  VerifyLevel
	OK     bool
	Detail string
}

type CreateOptions struct {
	Reason            string
	IncludeDerived    bool
	IncludeRecreatable bool
}

type RestoreOptions struct {
	Verify        VerifyLevel // default VerifyOpen; lower requires AllowWeakVerify
	AllowWeakVerify bool      // needed for legacy archives without a sidecar
	Restart       bool        // request a graceful restart once staged
}

type PendingRestore struct {
	ArchiveID   ArchiveID
	StagedAt    time.Time
	RequestedBy string
	Warnings    []string // e.g. "module tv in backup is not in this build: its data is restored and dormant"
}

// Service is core's backup API, exposed to the HTTP layer and to modules
// (module.Deps.Backup, limited to Ensure).
type Service interface {
	Create(ctx context.Context, o CreateOptions) (Archive, error)
	// Ensure creates an archive unless the newest verified one is younger than
	// maxAge (organizer's pre-organize backup, service.go:697, generalised).
	Ensure(ctx context.Context, maxAge time.Duration, reason string) (a Archive, created bool, err error)
	List(ctx context.Context) ([]Archive, error) // from the c/backup index; never hashes files
	Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error)
	Import(ctx context.Context, name string, r io.Reader) (Archive, error)
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
	Delete(ctx context.Context, id ArchiveID) error
	Rewrap(ctx context.Context, id ArchiveID) (Archive, error) // legacy/plain -> encrypted, verify, then delete the original
}

var (
	ErrBackupInProgress   = errors.New("backup: another backup or restore task holds the backup resource")
	ErrInsufficientSpace  = errors.New("backup: insufficient disk space")
	ErrCrossDevice        = errors.New("backup: checkpoint staging is not on the database's filesystem")
	ErrChecksumMismatch   = errors.New("backup: archive does not match its recorded checksum")
	ErrNoChecksum         = errors.New("backup: archive has no checksum sidecar")
	ErrDecrypt            = errors.New("backup: no configured identity can decrypt this archive")
	ErrManifest           = errors.New("backup: manifest missing, invalid or inconsistent with entries")
	ErrBackupTooNew       = errors.New("backup: archive needs a newer build (storage format, Pebble format or Breaking keyspace step)")
	ErrWrongApp           = errors.New("backup: archive belongs to a different app")
	ErrRestorePending     = errors.New("backup: a restore is already staged")
	ErrUnencryptedSecrets = errors.New("backup: refusing to write secret material into an unencrypted archive")
)
```

### 5B.3.3 Archive format

```
<app>_<YYYYMMDDTHHMMSSZ>_<archive-id>.tar.zst.age     (+ ".sha256" sidecar, sha256sum format)
  tar stream, zstd (klauspost/compress, already a dependency), age-encrypted:
    manifest.json                         first entry; also stored in c/backup/archive/<id>
    storage/main.pebble/...               checkpoint
    config/<file>                         config file layer
    secrets/master.key                    encrypted archives only
    certs/...                             encrypted archives only, if present
    artifacts/<owner>/<name>/...
    index/<module>/<name>.bleve/...       if IncludeDerived
    derived/<name>/...                    if IncludeDerived
    aux/<module>/<name>/...               if IncludeRecreatable
```

- Codec layers are sniffed by magic bytes on read (age header, zstd, gzip, plain tar), as ABO does
  for compression (`codec.go:204-222`); `core.backup.compression` picks the writer (default `zstd`;
  `gzip` and `none` remain).
- Names start with the app's own prefix; the books module registers the legacy prefix
  `audiobooks_` so old archives are listed and pruned (the prefix rule protects foreign files in a
  shared backup dir, `codec.go:225-245`).
- Encryption: `filippo.io/age` (BSD-3-Clause; latest stable recorded in [13-toolchain](13-toolchain.md),
  D8/D9). Recipients: (1) the **install backup identity**, an X25519 identity generated on first
  use and stored as a 0600 file beside the secrets master key in the credential state directory —
  *not* in the DB, because the DB is inside the archive it must decrypt; its recovery string is shown
  once in the UI with an acknowledgement step; (2) zero or more **operator recovery recipients**
  (`core.backup.recipients`, public keys only), whose identities never touch the server. Restoring on
  a new host needs either the state directory or an operator identity uploaded for that one restore
  (held in memory only).
- `secrets/master.key` inside an encrypted archive makes a restore self-contained: after the swap
  the restored DB's encrypted settings decrypt, instead of tripping the key-regeneration guard
  (`cmd/root.go:760-790`).

### 5B.3.4 Staged restore (the fix for `raw/05-storage.md` §5)

**Why not swap in place:** every subsystem holds the one store (`GatedDB` borrowers,
`store_from_store.go:12-63`; in core, every `Keyspace` handle), and derived state (memdb, bleve,
hnsw, caches, in-flight tasks) would describe the old data. A correct hot swap means quiescing and
re-initialising the whole process — which a restart already does. Restart-bound restore matches
restart-bound module enablement (02 decision 10).

API phase — task `core.backup.stage-restore` (`POST /api/core/v1/backups/{id}/restore`,
permission `core:backup.restore`, credential-change route with owner proof, as today's
`wire_system_routes.go:61-65`):

1. Refuse if a restore is already pending (`ErrRestorePending`).
2. Verify at `RestoreOptions.Verify` (default `VerifyOpen`). Legacy archives without a sidecar need
   `AllowWeakVerify` (today's `ErrVerificationUnsupported` choice, made explicit).
3. Compatibility: `App` must match (`ErrWrongApp`); `StorageFormat` ≤ build; `PebbleFormat` ≤ the
   pin; every keyspace version known, or newer with only `Additive` steps; else `ErrBackupTooNew`.
   Modules present in the archive but not in the build: restored, dormant, warning.
4. Extract into `<data-dir>/restore/<id>/staging/` — must be on the DB's filesystem (device check,
   as `ensureSameDevice`, `backup.go:577-598`), because the swap is a rename. Every file fsynced,
   then each directory.
5. Write `<data-dir>/restore/pending.json` (`{archive_id, staging, manifest_sha256, requested_by,
   requested_at}`) by temp file + rename + directory fsync. Audit event `core.backup.restore.staged`.
6. Return `202` with `restart_required: true`; with `Restart`, request a graceful restart through
   the lifecycle supervisor (ABO precedent `updater.RestartSelf`, `internal/updater/updater.go:331`).

Boot phase — kernel step **before** `storage` opens (05-storage §5S.4 step 0):

| Step | Action (journaled in `restore/<id>/journal.json`; each step idempotent: rename only if source exists and destination does not) |
|---|---|
| a | Re-check staging against `manifest_sha256` and entry sizes (full re-hash if `core.backup.restore.rehash_on_boot`). Failure → move `pending.json` to `restore/<id>/rejected.json`, boot continues on the **untouched** live data, health `degraded`, audit event. |
| b | Rename live items to `restore/<id>/previous/`: DB dir, its sidecar, config file, master key (if the archive carries one), index and derived dirs. |
| c | Rename staged items into place. |
| d | Rewrite the storage-format sidecar from the staged stamp (removes the "older archive, newer sidecar" case). |
| e | Mark every derived index for rebuild; included `Derived`/`Cache` items are offered to `Load` and kept only if accepted. |
| f | Remove `pending.json` (last). Journal = done. |
| g | Storage opens normally; the pre-migration checkpoint and migrations bring an older archive forward (G2 rules apply). Post-open hooks: `auth.OnRestore` revokes every session (a session revoked after the archive was taken must not come back — 05-authn), audit `core.backup.restore.applied`. |

Undo: `restore/<id>/previous/` is kept for `core.backup.restore.keep_previous`;
`RevertLastRestore` stages it through the same mechanism. A crash at any step resumes from the
journal at the next boot.

### 5B.3.5 Creation, verification, retention

Create (task `core.backup.create`, `Resource{Module: "core", Kind: "backup", ID: "main"}`,
exclusive with stage-restore and prune):

1. Resolve codec, recipients, encryption policy first (configuration errors before any work, the
   ABO ordering at `backup.go:428-440`). Encryption off + secret-class item → item skipped; the
   Pebble checkpoint is still written with `ContainsSecret: true` and a UI warning.
2. Retention **before** the space check (kept from `backup.go:445-458`).
3. Space check (`Avail` vs estimated size + margin).
4. Checkpoint on the DB's filesystem with `WithFlushedWAL` (cross-device → `ErrCrossDevice`, never a
   silent copy).
5. Capture artifacts into the run directory beside the checkpoint (hooks write there), so every
   input is an immutable local file; hash pass over them (bounded pool) to build the manifest, then stream `manifest.json` first and every entry after it through
   tar → zstd → age into `<name>.partial`, re-hashing while streaming and aborting on any mismatch.
6. fsync, rename to the final name, write the sidecar, fsync the directory, record
   `c/backup/archive/<id>`.
7. If `core.backup.verify_after_create`, run `VerifyOpen`; record the result.

Progress uses the existing phase model (checkpoint / archive / checksum / verify) with counters
from real work (`backup.go:51-82`) mapped onto `tasks` progress.

Verification task `core.backup.verify` (scheduled, default weekly) re-verifies the newest
`core.backup.verify_keep` archives at `VerifyEntries`, the newest at `VerifyOpen`.

Retention (task `core.backup.prune`, also run inside create):

| Bound | Key | Default | Note |
|---|---|---|---|
| count | `core.backup.retention.max_count` | 10 | must be ≥ 1 (ABO's `0 = keep none` maps to 1) |
| bytes | `core.backup.retention.max_total_bytes` | 40 GiB | 0 = unlimited, as `backup.go:174-188` |
| age | `core.backup.retention.max_age` | 0 (off) | |
| floor | — | newest **verified** archive, plus any archive referenced by a pending or undo-able restore | an unverified newer archive does not protect the last good one |

Deletion order oldest first; a failed delete is not counted as freed (kept from `backup.go:1255-1262`).

Pre-operation backups: modules call `Backup.Ensure(ctx, maxAge, "pre:books.organize")`
(organizer today: 6 h, `organizer/service.go:552,697`); the kernel itself calls `Ensure` with
`core.backup.before_breaking_migration` before any `Breaking` migration or `RaisesFormat` cut-over.

## 5B.4 Lifecycle

- Construction: after storage and secrets, before modules (module artifacts register during
  module registration and are validated: unique names, required `Consistency`).
- Boot: the pending-restore step runs before storage (§5B.3.4); after storage opens, interrupted
  `.partial` archives and staging dirs older than 24 h are swept (ABO `sweepStaleCheckpoints`,
  `backup.go:600-640`).
- Schedule: `core.backup.schedule` (cron; default daily at a jittered minute) registered as a
  `tasks` schedule.
- Shutdown: a running create is cancelled at an entry boundary; its partial archive is removed.

## 5B.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Codec/recipient/config invalid | closed before any work | ABO lesson: discover config errors before pruning and checkpointing (`backup.go:428-440`). |
| Insufficient space | closed (`ErrInsufficientSpace`) after retention ran | Full disk kills Pebble's WAL (`backup.go:85-100`). |
| Checkpoint staging cross-device | closed (`ErrCrossDevice`) | A silent multi-GB copy is worse than a fast failure (`backup.go:477-498`). |
| Secret item with encryption off | closed for that item (skipped), warning | Never write secret material in clear. |
| Artifact hook fails | closed (archive aborted, partial removed) | An archive missing a system-of-record artifact is not a backup. |
| Derived/recreatable capture fails | open (item omitted, manifest notes it) | Rebuildable on restore. |
| Retention delete fails | open (logged; budget not credited) | Kept from `backup.go:1255-1262`. |
| Post-create verification fails | closed: archive marked failed, not counted as the floor, alert | An unverifiable archive must not protect itself from pruning. |
| Restore verify fails / checksum mismatch / no identity | closed (`409` / `400`), nothing staged | Kept fail-closed semantics of `handler.go:870-895`. |
| Archive too new / wrong app | closed | Older build would misread. |
| Boot-time staging re-check fails | open for the app (boot on live data), closed for the restore (rejected) | Live data is untouched; refusing to boot would turn a bad archive into an outage. |
| Crash during the swap | resume from journal | Each step idempotent. |
| Session revocation hook fails after restore | closed (boot error) | Resurrecting revoked credentials is a security failure; operator can disable via `core.backup.restore.revoke_sessions=false` only explicitly. |

## 5B.6 Concurrency contract

- One backup-resource holder at a time (create, prune, stage-restore, rewrap): task `Resource`
  exclusivity from [05-tasks](05-tasks.md); `List` and `Verify` of other archives run concurrently.
- The checkpoint does not block writers (Pebble hard-links SSTs; the WAL copy is bounded by
  `WithFlushedWAL`).
- Archive writing is a single sequential stream (tar is sequential); hashing for the manifest
  pre-pass uses a bounded pool (`core.backup.hash_workers`, default `min(4, NumCPU)`) over the
  immutable checkpoint files, partitioned by file.
- All long steps check `ctx` between entries and between 1 MiB chunks; cancellation removes
  `.partial` files and staging dirs.
- `Service` is goroutine-safe; the HTTP layer never runs a backup on the request goroutine (task
  enqueue + `202`).

## 5B.7 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.backup.enabled` | bool | true | yes |
| `core.backup.dir` | path | `<data-dir>/backups` (ABO: `backup_dir`, resolved as `backup.ResolveDir`, `backup.go:120-146`) | yes (next run) |
| `core.backup.schedule` | cron | daily, jittered | yes |
| `core.backup.compression` | enum `zstd`/`gzip`/`none` | `zstd` (ABO keeps `gzip` until changed) | yes |
| `core.backup.compression_level` | int | 0 = codec default | yes |
| `core.backup.encryption` | enum `age`/`none` | `age` | yes |
| `core.backup.recipients` | list of age public keys | empty | yes |
| `core.backup.include_derived` | bool | false | yes |
| `core.backup.include_recreatable` | bool | false | yes |
| `core.backup.verify_after_create` | bool | true | yes |
| `core.backup.verify_schedule` | cron | weekly | yes |
| `core.backup.verify_keep` | int | 3 | yes |
| `core.backup.retention.max_count` | int ≥ 1 | 10 | yes |
| `core.backup.retention.max_total_bytes` | int64 | 40 GiB | yes |
| `core.backup.retention.max_age` | duration | 0 | yes |
| `core.backup.before_breaking_migration` | bool | true | no |
| `core.backup.hash_workers` | int | `min(4, NumCPU)` | yes |
| `core.backup.restore.keep_previous` | duration | 168h | yes |
| `core.backup.restore.rehash_on_boot` | bool | false | yes |
| `core.backup.restore.revoke_sessions` | bool | true | yes |

## 5B.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_backup_runs_total` | counter | `reason`, `outcome` |
| `mediacore_backup_duration_seconds` | histogram | `phase` (checkpoint, archive, checksum, verify) |
| `mediacore_backup_bytes` | gauge | — (size of newest archive) |
| `mediacore_backup_last_success_timestamp_seconds` | gauge | — |
| `mediacore_backup_last_verified_timestamp_seconds` | gauge | — |
| `mediacore_backup_archives` | gauge | `format`, `encrypted` |
| `mediacore_backup_retained_bytes` | gauge | — |
| `mediacore_backup_verify_total` | counter | `level`, `outcome` |
| `mediacore_backup_restore_total` | counter | `stage` (staged, applied, rejected, reverted) |

Health ([05-health](05-health.md)) turns `last_verified` older than 2 × schedule into `degraded`.

## 5B.9 Tests

| Test | Proves |
|---|---|
| `TestRestoreNeverWritesLiveStore` | the API phase touches only `restore/`; live DB bytes unchanged |
| `TestStagedRestoreAppliedBeforeStorageOpens` | kernel order; restored data visible after boot |
| `TestRestoreCrashAtEveryJournalStep` | each crash point resumes to done or to untouched live data |
| `TestRestoreRejectedKeepsLiveData` | bad staging → boot on live data, health degraded |
| `TestRevertLastRestore` | undo point round trip |
| `TestRestoreOlderArchiveRunsMigrations` | older keyspace versions migrate forward after swap |
| `TestRestoreRefusesTooNewArchive` | format, Pebble format, Breaking step, wrong app |
| `TestRestoreRewritesSidecarFromStamp` | latent sidecar mismatch removed |
| `TestRestoreRevokesSessions` | post-restore hook |
| `TestEncryptedArchiveNeedsIdentity` | install identity and operator recipient both decrypt; neither present → `ErrDecrypt` |
| `TestUnencryptedArchiveOmitsSecrets` | master key and certs never written in clear |
| `TestManifestMatchesEntries` | tampered entry → `ErrManifest` |
| `TestVerifyOpenCountsKeys` | read-only open of the checkpoint matches manifest counts |
| `TestCheckpointCrossDeviceRefused` | no silent copy |
| `TestRetentionKeepsNewestVerified` | unverified newer archives do not protect themselves |
| `TestRetentionRunsBeforeSpaceCheck` | stuck-disk regression from 2026-08-29 |
| `TestLegacyABOArchiveRestores` | gzip/zstd/none, with and without sidecar, entry root `audiobooks.pebble/` |
| `TestRewrapLegacyArchive` | plain → encrypted, verified, original deleted |
| `TestDerivedItemsVerifiedOnLoad` | stale bleve/hnsw rejected and rebuilt |
| `TestBackupTaskExclusive` | create vs stage-restore vs prune |
| `TestCreateCancelRemovesPartial` (`synctest`) | cancellation cleanliness |

## 5B.10 Migration from ABO

| ABO code | Becomes |
|---|---|
| `internal/backup/codec.go` (gzip/zstd/none, magic sniffing, level validation) | `core/internal/backuparchive` codecs (+ age layer) |
| `CreateBackupWithCheckpoint`, `ensureSameDevice`, `sweepStaleCheckpoints`, space guard, retention | `backup.Service.Create` / prune internals (rules kept, floor changed to newest verified) |
| `CreateBackup` live-walk fallback and SQLite branches (`backup.go:283,959,1028,1103`) | deleted: core always checkpoints |
| `RestoreBackupIn` / `extractArchive` | legacy reader for format `legacy-abo/0`, used only by `StageRestore` into staging |
| `ListBackups`, `newestBackupAge` (`organizer/service.go:567`) | `Service.List` (index-backed) and `Service.Ensure` |
| `handlers/system` backup routes (`/api/v1/backup/*`) | core routes `/api/core/v1/backups[/{id}[/verify|/restore]]`; legacy paths aliased for the 09 compatibility window, `POST /api/v1/backup/restore` re-implemented as stage-restore (it can no longer extract into an arbitrary `target_path`) |
| organizer auto-backup | `Backup.Ensure(ctx, 6h, "pre:books.organize")` |
| config `backup_dir`, `backup_max_total_bytes`, `backup_compression`, `backup_compression_level`, `create_backups` | `core.backup.*` aliases (09) |

First run on an upgraded ABO install: existing `audiobooks_*` archives appear as `legacy-abo/0`
(unencrypted, `ContainsSecret: true`, flagged in the UI with a "re-wrap" action); the first
`core.backup.create` produces an encrypted archive; nothing is deleted except by retention.

## 5B.11 Open items

1. Where the install backup identity file lives is fixed by [05-config](05-config.md)'s credential
   state directory; this spec requires it to be the same protection domain as the secrets master
   key and outside the DB.
2. Whether `Import` (uploading an archive over HTTP) needs a size cap and streaming verification
   before it is stored — default cap = `retention.max_total_bytes`.
3. TLS material under `certs/` may be ACME-managed (re-creatable) or operator-provided; class
   decided with [05-http-api](05-http-api.md).
4. Multi-app in one binary: one archive per install (one DB); `Manifest.App` becomes a list then.
   Not built until that mode exists (lowest priority).

## 5B.12 Objections to 03

None.

## 5B.13 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| `POST /api/v1/backup/restore` accepts any absolute `target_path` that is not a "dangerous root" and extracts an archive there, an arbitrary-directory write for any holder of `settings:manage` while no owner is set (`ownerGateWhenOwnerSet`). | `internal/server/handlers/system/handler.go:842-856`; `internal/server/wire_system_routes.go:61-65` |
| Restore `verify` defaults to false (Go zero value of the request field), so the documented fail-closed verification only runs when a client opts in. | `handler.go:826-829,862` |
