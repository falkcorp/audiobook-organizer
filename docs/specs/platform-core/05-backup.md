<!-- file: docs/specs/platform-core/05-backup.md -->
<!-- version: 1.12.9 -->
<!-- guid: 604febb0-a09e-4cf9-99ee-5f3722534da6 -->
<!-- last-edited: 2026-11-05 -->

# 05 — Backup and restore (`backup`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) §5 (backup and restore) and §1
(engines), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7 (secret storage).
Storage terms (`storage.Admin`, keyspaces, levels, sidecars, checkpoints, the cutover's migration steps,
the store generation, derived indexes and their generations, auxiliary DBs) are defined in [05-storage](05-storage.md); the secrets keyring, its derivation key and
the boot record in [05-config](05-config.md); the post-restore credential hook in
[05-authn](05-authn.md) §4.14; role re-application and audit copy-forward in [05-rbac](05-rbac.md) §7.1.
`backup` is **L4** (R4) and lands in R2, at 09's backup flip. The revocation-journal and restore-hook types live
in the leaf package `backup/journal` (L4; imports `entity` and `storage` only), which lands in R2 with `auth`
and `rbac`, its first writers (COR2-9); it exports the `c/backup/` `storage.Spec` the kernel registers
from then on (COR5-6). Boot order is [06](06-app-sdk.md) §6.5.1's one phase table
(R2-8): this file's pre-boot swap is P0a, its journal re-apply and restore hooks are P4d steps 2 and 3,
after the keyring that `secrets.Open` opened at 05-storage §5S.4 step 8 (the start of 06 P4b: after the checkpoint, before the migration list) is in hand and P4d step 1 has attached the DB overrides (COR3-2). This file owns the **one
archive format** (cross-cutting resolution 8); [09](09-abo-on-core.md) references it and decides only
which release each step ships in.

**Releases (DATA3-7, DATA4-6, COR4-10; D11).** `backup` lands in R2; two pieces land earlier or with another flip.
- **The journaled swap** (the one intent slot, P0a's swap and resumption, step x's swap-back, step h's
  bookkeeping up to `applied` and the removal of `inflight.json`, the `KindCheckpoint` swap of 05-storage's
  `storage restore-offline --checkpoint`, and the `KindReset` swap of 05-storage's factory reset, §5S.3.15) is core
  code from **R1** (M8's `KindRewrite` swap of 05-storage §5S.3.13 runs on the same code from F3, where M8 lands), `core/internal/restoreswap`, held and run by the kernel at P0a (06) and from its reset
  staging. `restoreswap` is **L1** and imports `storage` and its L0 leaves; `storage` never imports it, and
  `storage.Admin` has no swap or staging method, so there is no cycle (COR8-5). The swap journal's record
  types (`restorejournal.Record` and the step names) live in the L0 leaf `storage/restorejournal` (05-storage
  §5S.3.15), which this package uses directly under those names (SIMP8-8). The slot's sentinel
  `storage.ErrSwapPending` and the pre-check's `storage.ErrRenameRefused` are `storage`'s, so R1 and ABO's
  staging return them before this package exists (COR8-6). From R1 until R2's backup flip, ABO's own
  `internal/backup` creates archives in HEAD's format (`legacy-abo/0`, B7) and **stages** a restore (steps 1–9
  below, creating `pending.json` in the one slot through `restorejournal.CreateIntent` with the shared
  `restorejournal.Intent` type, so its intent and the kernel's cannot drift; COR9-13); it takes the build's
  upgrade floor for step 4 from books' own manifest (`Manifest.Storage.UpgradeFloor`), so it needs no kernel
  handle (SIMP8-4). R1 has no revocation journal and no restore hooks, so a restore R1 applies keeps every
  restored key and session as archived, which is HEAD's behaviour, and reaches `applied` through the hook-less
  edge of step h, recording its hooks as owed (§5B.3.6 "Restores without hooks"). R0 restores as HEAD does,
  live over the running store (5B.2), which is why every rollback restore runs offline (§5B.3.3).
- **The journal** (`backup/journal`) lands in R2 with `auth` and `rbac`, its first writers, at the identity
  flip. From that flip the kernel registers two things for it, so neither waits for `backup` (COR5-6): core
  keyspace `c/backup/` (owner `backup`, already in the fixed core order, 05-storage §5S.3.5) through
  `journal.StorageSpec()` — `Serialized: ["journal_head", "served_boot"]` plus the `journal_gap/` and
  `restore_hooked/` prefixes (every write to either key goes through `GetForUpdate`, R6-1; that is the only
  reason `served_boot` is `Serialized`, COR6-6) — so `Append`'s `GetForUpdate` and `Gap`'s record commit in
  the revoking batch instead of failing with `ErrNotSerialized` or `ErrOutsideKeyspace`; and the restart-bound
  key `core.backup.dir` (`ProtectAdmin`, default §5B.7), which the kernel's **core-section list** declares from
  F2, the foundation PRs, with every other `core.backup.*` key (dark until `backup` adopts the section; IMPL-1), so
  `backup/journal` keeps importing only `entity` and `storage` and P4d step 2 can locate the journal under
  the admin's directory before `backup` exists. `backup` adopts both at its own flip
  (`TestRevocationStagesJournalHead`). P4d step 2 re-applies the journal and step 3 runs the hooks on every
  boot that applied a restore or finds a replaced store (§5B.3.6). No release ships between the identity and
  backup flips (D12 (a)), and core auth decides every request from the identity flip on, so no released build
  revokes without journaling.

From R2's backup flip the steps below are this package's.

**One stored format (R3-1, R4-1).** Every family keeps one native format: the core-service families are
migrated forward in R2 (05-storage's cutover), so a revocation, a hook write and a journal re-apply are each an
ordinary write through the owning subsystem's native store. No older build can open a migrated store (the level
guard refuses it, 05-storage), so this file's journal never accounts for a build that served the store without
journaling; the one hook-less path is a restore applied by R1 or offline, whose hooks the next journaling boot
runs (§5B.3.6).

## 5B.0 Decisions in one table

| # | Decision |
|---|---|
| B1 | **One archive format, `mediacore-backup/1`**: one tar file per archive (manifest first, then the Pebble checkpoint under `db/`, the config file and boot record, module artifacts and the optional derived and re-creatable items) with `.sha256` and manifest sidecars (§5B.3.3). The layout is core's own; nothing in it is shaped for an older build's restore code. **No archive ever contains the secrets keyring** (it comes back from 05-config's keyring export, §5B.3.3), and an unencrypted archive never contains a secret: the operator config file goes in as class `Secret` when it holds one, and only a redacted copy goes into a plaintext archive (DATA2-17). An archive is **credential-equivalent**: it holds every password hash, OIDC identity link, the invite allow-list, the audit trail and the stored secrets (ciphertext under the keyring; a pre-R2 `legacy-abo/0` archive holds HEAD's plaintext blob secrets and raw session tokens), so downloading one is gated like a restore (§5B.3.8, SEC3-7). **Old archives restore, then migrate**: a `legacy-abo/0` archive, or any archive older than the build's migrations, restores through the same staged or offline path, and the boot that follows migrates the restored store forward behind its own pre-migration checkpoint (B7). |
| B2 | **Restore is staged and applied in the kernel pre-boot phase** (06 P0a), before config is read and storage opens (and, once the deferred P−1 is built, after the updater's own pre-boot check; A-6). The API verifies, pre-validates every rename and extracts into a staging directory on the DB's filesystem; the kernel swaps by rename using the absolute targets recorded at staging and keeps the previous data as an undo point. **Any failure after the swap swaps back automatically.** No code path writes into a live store. |
| B3 | **Archive encryption is a deferred-build member** (SIMP4-5): no app needs it during the extraction and hello is not a backup product, so R2 builds **plaintext** archives with B5 provenance only. Its contract (age, an archive identity derived from the one keyring, activation rules, operator recipients, the plaintext-floor rule) is kept at proof depth in §5B.3.9 and lands with the first app or owner request that needs it. A plaintext archive never carries a secret-class item (the config file goes in redacted); the secrets inside the DB are ciphertext under the keyring from R2 on. |
| B4 | Verification is part of creation (per-entry SHA-256, open the checkpoint read-only, compare key counts) and runs again on a schedule. Retention never deletes the newest **verified** archive. |
| B5 | **Provenance.** The manifest carries an HMAC bound to the install (keyring-derived key named by its key ID, install ID in the MAC'd bytes). An archive without a valid MAC — imported, planted, from another install, or from before provenance existed — is **foreign**: restoring it needs an explicit `Foreign` request, `core:admin` and an interactive owner proof, and is audited. |
| B6 | **What must outlive a restore lives outside the restored DB**: the archive catalogue is derived from sidecar files in the backup directory; the restore journal and the **revocation journal** are files. The revocation journal is hash-chained with a keyring-derived MAC and anchored by a head record inside the store (SEC2-9); every build from R2 journals its revocations, and no older build can open a store the journal covers; every boot re-applies the journal tail the store lacks, a restore applied without hooks gets them at the next journaling boot, and a store replaced outside the staged and offline restore gets the post-restore hooks too (DATA3-8). |
| B7 | Old ABO archives (`audiobooks_<dbtype>_<ts>.tar[.gz|.zst]` + `.sha256`: every archive made before R2's backup flip, R1's included) stay listable, verifiable and restorable through the same staged and offline paths as format `legacy-abo/0`. They are pre-provenance, so restoring one is a foreign restore. The restored store is then migrated forward by the boot that follows (05-storage's cutover steps behind the pre-migration checkpoint), so a restored pre-R2 store is converted exactly as a live one was (`TestLegacyArchiveRestoreThenMigrate`). The migration reads the gap-fill values HEAD filled from `<db parent>/config.yaml`, which M2b strips on the live host (05-config §1.12.2), so `storage restore-offline --archive` of a `legacy-abo/0` archive takes `--gap-fill <file>` (the off-host copy kept with the archive, 09's R2 checklist) and puts it in place before the migrating boot; without it, and on the staged path, the restore warns that every stored-empty field HEAD filled from that file migrates empty (DATA-15). Its coverage by the revocation journal follows §5B.3.6: a pre-R2 archive has no journal head, so every verified line is re-applied. |
| B8 | Create/verify/prune/stage-restore are core **tasks** (`core.backup.*`) with progress from real work, cancellation, one exclusive `Resource`, and schedules declared under backup's own namespace with type `tasks.ScheduleConfig` and bound through `tasks.ScheduleFromConfig` (SIMP-6, R2-7). |

## 5B.1 Purpose & scope

In scope: what is backed up, the archive format, encryption (a deferred-build member, §5B.3.9), provenance, creation (scheduled, on
demand, "ensure fresh before a risky operation", pre-update), verification, retention, listing,
import of an uploaded archive, staged restore and its automatic and manual undo, offline restore for
host loss and as the rollback path (D11), the revocation journal, and restoring ABO's pre-core archives.

Out of scope: media files under library roots (core does not back up media; operators use filesystem
snapshots — [05-library](05-library.md) owns roots); off-site replication (an archive is a set of files;
copying the backup directory elsewhere is the operator's tooling, and this spec makes that copy
sufficient for host-loss recovery when the keyring export is kept with it); the secrets keyring
([05-config](05-config.md)); what the post-restore hooks do to credentials and audit records
([05-authn](05-authn.md), [05-rbac](05-rbac.md) implement the hooks this file calls).

## 5B.2 Current state at HEAD

| Aspect | Today | Evidence |
|---|---|---|
| Scope | The Pebble directory only. bleve, `hnsw/`, the Open Library store, the `.storage-format` sidecar, the config file and the encryption key file are not archived. | `internal/backup/backup.go:283-428`; `raw/05-storage.md` §5 |
| Consistency | Pebble native checkpoint (hard links) staged in `.backup-staging` **on the DB's filesystem**; cross-device staging is refused because Pebble silently byte-copies on EXDEV (a 14 GB silent copy once cancelled a library scan). Live-directory walk is the fallback when the store is not `Checkpointable`. Checkpoint is called without `WithFlushedWAL`. | `backup.go:428-541,544-598`; `internal/database/pebble_store.go:953-955` |
| Format | tar + codec (gzip default at best compression, zstd, none) chosen by name, **sniffed by magic bytes** on restore; `<archive>.sha256` sidecar in `sha256sum` format; names `audiobooks_<dbtype>_<YYYYMMDD_HHMMSS><ext>`. | `backup.go:148-156,333,643-700`; `internal/backup/codec.go:48-80,204-245` |
| What an older build lists and restores | Only names with prefix `audiobooks_` **and** an extension in `.tar.gz`, `.tar.zst`, `.tar` are listed or pruned. Restore extracts **every** tar entry under the target directory by its name (DB dir basename first). | `codec.go:82-84,225-245`; `backup.go:777-870` |
| Retention | `MaxBackups` 10 and `MaxTotalBytes` 40 GiB default; runs **before** the space check (an order bug once left a full disk permanently stuck); the last archive is a floor (never deleted), except `MaxBackups == 0` = keep none. | `backup.go:85-100,188,437-458,1183-1290` |
| Space guard | `diskstats` Bavail with a 2 GiB margin, before checkpointing. | `backup.go:198-268` |
| Progress | phase callback with counters from the real walk; callback error aborts and removes the partial archive. | `backup.go:51-82` |
| Callers | `POST /api/v1/backup/create`, `GET /backup/list`, `POST /backup/restore` (credential-change route, owner-gated), `DELETE /backup/:filename`, all `settings:manage`; organizer auto-backup before organize, skipped if the newest archive is younger than 6 h. | `internal/server/wire_system_routes.go:59-66`; `internal/organizer/service.go:552,697,735-760` |
| **Restore** | `RestoreBackupIn` verifies (only if the request sets `verify`; the JSON default is false) and **extracts straight into the target directory** — by default the DB's parent, i.e. over the live `audiobooks.pebble` — with `os.Create` per entry. The handler never closes, swaps or reopens the open store and never asks for a restart. Files that exist live but not in the archive stay, so the result is a mix of two databases under a running engine. | `backup.go:732-775,777-870`; `internal/server/handlers/system/handler.go:823-905`; `raw/05-storage.md` §5 |
| Format interplay | The stamp is inside the checkpoint, the sidecar outside: restoring an older archive leaves a newer sidecar (latent while the format is 1 everywhere). | `raw/05-storage.md` §5; `pebble_store.go:670-680` |
| Secrets in archives | Session IDs are stored raw and are bearer credentials; several provider/OAuth/download-client secrets sit in the plaintext `config_blob`; only 4 secrets are AES-GCM rows whose key file (`.encryption_key`) is outside the DB and outside the archive. Archives are never encrypted. Restoring onto a host without that key file makes boot refuse (correctly) rather than lose secrets. | `raw/10-auth-rbac.md` §7; `internal/database/pebble_store_auth.go:1656-1663`; `internal/database/settings.go:42-52`; `cmd/root.go:760-790` |
| Plaintext config mirror | `SaveConfigToFile` writes `config.yaml` beside the DB directory (`filepath.Dir(DatabasePath)`), outside the Pebble directory, so today's archives never contain it; operators also keep secrets (`smtp_password`, `abs_jwt_secret`, …) in the viper config file. | `internal/config/persistence.go:32`; `internal/config/config.go:2289-2292`; 05-config §2.2 |
| Upgrade path | ABO upgrades are headless (`make rollback` is the committed path back; deploys copy the binary); a restart policy restarts on failure every 5 s. | `Makefile:892-917`; `deploy/audiobook-organizer.service:81-82,111-112` |
| SQLite remnants | Live-copy path still has SQLite single-file branches (dead). | `backup.go:959,1028,1103` |

## 5B.3 Go API

Package `github.com/falkcorp/mediacore/backup` (L4).

### 5B.3.1 What is in an archive

| Store | Class | Default | Where | How captured | How restored |
|---|---|---|---|---|---|
| Main Pebble DB | `SystemOfRecord` | always | archive `db/…` | `storage.Admin.Checkpoint(dir, WithFlushedWAL)` on the DB's filesystem | staged dir renamed into place |
| Manifest | metadata | always | the archive's first entry `mediacore-manifest.json` + sidecar `<archive>.manifest.json` | built after the hash pass | read first; drives verification and the swap |
| Level stamps and legacy projection | metadata | always (as manifest values) | manifest | read from the checkpoint | sidecars `.storage-levels` and `.storage-format` rewritten from the staged store |
| Operator config file (05-config) | `Secret` when 05-config's `ConfigFileHasSecrets()` is true, else `SystemOfRecord` (DATA2-17) | always | `config/<file>` — the file itself when it holds no secret; otherwise 05-config's `RedactedConfigFile()` and manifest flag `config_redacted` (archives are plaintext until encryption is built, §5B.3.9) | file copy | staged; swapped only when `ReplaceConfig` and the rename pre-check pass, else left in `restore/<id>/config.restored` for the operator; a redacted copy is never swapped in, and restore warns that its secret values must come from env or be re-entered |
| Boot record `<state_dir>/config.boot.json` (05-config §1.3.5) | `SystemOfRecord` | always | `config/config.boot.json` | file copy | as the config file |
| ABO's plaintext mirror `<db parent>/config.yaml` | — | **never** | — | — | — (it duplicates secrets in clear, `persistence.go:32`) |
| Secrets keyring | — | **never** | — | — | recovered from 05-config's keyring export, which the offline restore puts back (`--keyring`, §5B.3.3); restore checks that the keyring covers the archive's secret records (§5B.3.4 step 5) |
| TLS material (`certs/` beside the DB, ABO `appdirs`) | `Secret` | never while archives are plaintext (lands with encryption, §5B.3.9) | `certs/…` | dir copy | staged |
| Module artifacts declared `SystemOfRecord` | `SystemOfRecord` | always | `artifacts/<owner>/<name>/…` | module `Snapshot` hook | module `Restore` hook into staging |
| Full-text indexes (`storage/index`) | `Derived` | off (`core.backup.include_derived`) | `index/…` | bleve online `CopyTo` (`bleve/v2 index.go:350-354`) | offered to the index only if its recorded generation matches the restored store's; else rebuilt |
| Derived snapshots (hnsw) | `Cache` | off | `derived/<name>/…` | `DerivedIndex.Snapshot` | `DerivedIndex.Load` with the generation rule; else rebuilt |
| Auxiliary DBs (Open Library mirror) | `Recreatable` | off (`core.backup.include_recreatable`) | `aux/…` | `Aux` checkpoint | staged |
| Revocation journal, its location record, restore journal | — | never inside an archive | backup directory (`<prefix>revocations.journal`) / `<state_dir>/backup.location.json` / `<state_dir>/restore/` | — | read by the restore, never replaced by it |
| Bootstrap token, `.backup-staging`, temp files | — | never | — | — | — |

Consistency rule: the Pebble checkpoint is the point in time of the archive. Every other
system-of-record artifact must be either (a) fully described by Pebble, or (b) append-only /
content-addressed, so capturing it *after* the checkpoint yields a superset that Pebble's view
tolerates; module hooks state which (`Artifact.Consistency`). Derived and cache items follow the
**generation rule** of 05-storage §5S.3.9 (DATA-25): the generation recorded in the checkpoint is
written into the manifest, the item is captured after the checkpoint, and the item is marked `stale` in
the manifest — never offered on restore — **unless the source's generation read at the end of the
capture equals the checkpoint's** (DATA2-27: a touch between the checkpoint and the start of capture
also makes it stale). On restore every derived index is rebuilt unless its artifact's generation equals
the restored store's.

### 5B.3.2 Types

```go
package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
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

// Artifact is declared by a module through Registrar.Backup (06) or by a core
// subsystem. Hooks close over the declaring owner's services.
type Artifact struct {
	Name        string // "books.playlists"; unique; archive path artifacts/<owner>/<name>/
	Owner       storage.Owner
	Class       Class
	Consistency Consistency // required for SystemOfRecord
	// Dir: core copies this directory (confined via os.Root). Or Snapshot/Restore
	// hooks for anything that needs the owner's cooperation.
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
	Format              string                 `json:"format"`         // "mediacore-backup/1"
	ArchiveID           ArchiveID              `json:"archive_id"`
	App                 string                 `json:"app"`            // app.Options.AppID
	AppVersion          string                 `json:"app_version"`
	CoreVersion         string                 `json:"core_version"`   // version.Version (03 §3.3)
	InstallID           string                 `json:"install_id"`     // c/storage/install
	CreatedAt           time.Time              `json:"created_at"`     // informational only; no restore rule reads it (DATA2-7)
	Reason              string                 `json:"reason"`         // "schedule", "manual", "pre:books.organize", "pre-update", "pre-migration"
	// Read BEFORE the checkpoint call, so every revocation or audit record the
	// checkpoint lacks lies above them (clock-free boundaries, DATA2-7).
	JournalSeq          uint64                 `json:"journal_seq"`      // revocation journal head seq (§5B.3.6)
	AuditHighWater      uint64                 `json:"audit_high_water"` // c/audit/ Sequence high-water mark (05-rbac §7.1)
	// ServedBoot is the content marker c/backup/served_boot read before the
	// checkpoint; Create then writes a newer pair to the live store, so the
	// archive's pair is always older than the file's (§5B.3.6, SEC6-6).
	ServedBoot          *journal.ServedBoot    `json:"served_boot,omitempty"`
	Levels              map[string]int         `json:"levels"`         // per-owner reader levels in the checkpoint (05-storage §5S.3.7)
	LegacyStorageFormat int                    `json:"legacy_storage_format,omitempty"` // books' projection, 1 or LegacyMaxFormat+1
	PebbleFormat        uint64                 `json:"pebble_format"`
	Keyspaces           map[string]KSInfo      `json:"keyspaces"`      // owner -> version, approx keys/bytes
	Modules             map[string]string      `json:"modules"`        // module ID -> module version
	Entries             []Entry                `json:"entries"`        // archive files: path, size, sha256, class
	Derived             map[string]DerivedInfo `json:"derived,omitempty"`
	ConfigRedacted      bool                   `json:"config_redacted,omitempty"` // the archive holds 05-config's RedactedConfigFile (DATA2-17)
	// Encrypted and Recipients are deferred-build members (§5B.3.9); absent = plaintext.
	Provenance          Provenance             `json:"provenance"`     // computed last; MAC over the manifest with this field's MAC empty
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

type DerivedInfo struct {
	Generation string `json:"generation"` // 05-storage Generation, hex
	Stale      bool   `json:"stale"`      // source generation at the end of capture != checkpoint's: never offered on restore
}

type Provenance struct {
	Alg   string `json:"alg"`    // "hmac-sha256"
	KeyID string `json:"key_id"` // 05-config ActiveDeriveID() when made; the archive identity derives from the same key (SEC2-24)
	MAC   string `json:"mac"`    // key: DeriveKeyFor(KeyID, "core.backup/manifest/v1") of this install
}

type ProvenanceClass uint8
const (
	ProvenanceInvalid ProvenanceClass = iota
	Own           // MAC verifies with this install's derivation key KeyID and InstallID matches (or there is no live store)
	Foreign       // MAC missing or wrong, KeyID retired or unknown, or another install's ID
	PreProvenance // legacy-abo/0: made before provenance existed; restored only as Foreign
)

type Archive struct {
	ID         ArchiveID
	Name       string // archive file name
	Size       int64
	CreatedAt  time.Time
	Format     string // "mediacore-backup/1" or "legacy-abo/0"
	Provenance ProvenanceClass
	Manifest   *Manifest     // nil for legacy archives until first verification synthesises one
	Verified   *VerifyResult // last verification, from <main>.verify.json
}

type VerifyLevel uint8
const (
	VerifyInvalid VerifyLevel = iota
	VerifyChecksum // outer SHA-256 sidecars only (fast)
	VerifyEntries  // every entry hash vs manifest, both archives
	VerifyOpen     // VerifyEntries + open the checkpoint read-only in a temp dir and count keys per owner
)

type VerifyResult struct {
	At     time.Time
	Level  VerifyLevel
	OK     bool
	Detail string
}

type CreateOptions struct {
	Reason             string
	IncludeDerived     bool
	IncludeRecreatable bool
}

type RestoreOptions struct {
	Verify                  VerifyLevel // default VerifyOpen; lower requires AllowWeakVerify
	AllowWeakVerify         bool        // needed for legacy archives without a sidecar
	Foreign                 bool        // required unless provenance is Own; the route also requires core:admin + interactive owner proof
	ReplaceConfig           bool        // swap the config file and boot record too (default: stage them for the operator)
	AcceptUnreadableSecrets bool        // the keyring lacks keys the archive's secret records need: they must be re-entered
	Restart                 bool        // once staged, request a restart (06 Lifecycle.RequestRestart: drain, then the restart exit code; OPS8-12)
}

type PendingRestore struct {
	ArchiveID     ArchiveID
	Kind          journal.RestoreKind
	StagedAt      time.Time
	StagedByBuild string    // the binary ID (app.BinaryID; R5-7, FE5-3); another binary rejects the intent
	ExpiresAt     time.Time // StagedAt + core.storage.swap.intent_ttl (05-storage §5S.7; SIMP8-1)
	RequestedBy   string
	Warnings      []string  // e.g. "module tv in backup is not in this build: its data is restored and dormant"
}

// Ensurer is the handle modules receive (module.Deps.Backup): "archive unless
// one verified within maxAge" (organizer's pre-organize backup, service.go:697).
type Ensurer interface {
	Ensure(ctx context.Context, maxAge time.Duration, reason string) (a Archive, created bool, err error)
}

// Runner is what the updater's pre-update backup would receive. That consumer
// is a deferred-build member of 05-updater §5U.3 (R10-8, OPS10-6): the R2
// updater is HEAD's moved pipeline and takes no backup, so no R0–R3 build
// passes a Runner to it. Service embeds it.
type Runner interface {
	Ensurer
	Create(ctx context.Context, o CreateOptions) (Archive, error)
}

// Service is core's backup API, exposed to the HTTP layer (§5B.3.8) and the CLI.
type Service interface {
	Runner
	List(ctx context.Context) ([]Archive, error) // the catalogue derived from the backup directory (§5B.3.5); never hashes files
	Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error)
	Import(ctx context.Context, name string, r io.Reader) (Archive, error) // provenance decides Own vs Foreign
	Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error)    // the archive as stored
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
	Delete(ctx context.Context, id ArchiveID) error // refused for archives protected by a retention floor
	// Rewrap re-creates an Own archive under the ACTIVE derivation key (re-MAC;
	// re-encryption is a deferred-build member, §5B.3.9). The original is
	// deleted only if no floor protects it.
	Rewrap(ctx context.Context, id ArchiveID) (Archive, error)
}

var (
	ErrBackupInProgress    = errors.New("backup: another backup or restore task holds the backup resource")
	ErrInsufficientSpace   = errors.New("backup: insufficient disk space")
	ErrCrossDevice         = errors.New("backup: checkpoint or restore staging is not on the database's filesystem")
	ErrChecksumMismatch    = errors.New("backup: archive does not match its recorded checksum")
	ErrNoChecksum          = errors.New("backup: archive has no checksum sidecar")
	ErrManifest            = errors.New("backup: manifest missing, invalid or inconsistent with entries")
	ErrForeignArchive      = errors.New("backup: archive provenance is not this install; restore requires Foreign")
	ErrBackupTooNew        = errors.New("backup: archive needs a newer build (reader level, Pebble format or Breaking step)")
	ErrWrongApp            = errors.New("backup: archive belongs to a different app")
	ErrRestoreStale        = errors.New("backup: the staged restore expired, was staged by another build, or the store was opened since")
	ErrUnreadableSecrets   = errors.New("backup: the keyring lacks keys the archive's secret records need")
	ErrUnencryptedSecrets  = errors.New("backup: refusing to write secret material into an unencrypted archive")
	ErrProtectedByFloor    = errors.New("backup: archive is protected by a retention floor")
)

// The slot and pre-check refusals are storage's, shared with the factory
// reset and with ABO's staging before R2's backup flip (R8-1, COR8-6): storage.ErrSwapPending
// (an intent of any kind is staged, or a swap is in flight) and
// storage.ErrRenameRefused (a target cannot be swapped by rename); this
// package returns them as they are and declares no sentinel of its own for
// the slot, and no swap kind has one (R8-1).
```

```go
// Package journal is the leaf of backup that auth and rbac import (COR2-9):
// github.com/falkcorp/mediacore/backup/journal, L4, imports entity and storage
// (with its L0 leaf storage/restorejournal) only, lands in R2 with auth and rbac. It holds the out-of-DB revocation journal (§5B.3.6),
// its file-backed implementation, the restore-hook contract and StorageSpec,
// the c/backup/ storage.Spec the kernel registers from the identity flip (COR5-6).
package journal

import (
	"context"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

type Kind string

const (
	APIKeyRevoked         Kind = "apikey.revoked"
	APIKeyDisabled        Kind = "apikey.disabled"         // any status change away from active, needs_confirmation included
	UserDisabled          Kind = "user.disabled"
	UserDeleted           Kind = "user.deleted"
	CredentialEpochBumped Kind = "credential_epoch.bumped"
	PasswordChanged       Kind = "password.changed"        // change or reset (SEC2-11; DATA2-7's credential.changed)
	IdentityUnlinked      Kind = "identity.unlinked"       // Detail = [provider]
	SessionRevoked        Kind = "session.revoked"
	RoleUnassigned        Kind = "role.unassigned"         // Subject user, Object role
	RoleNarrowed          Kind = "role.narrowed"           // Subject role, Detail = removed permissions
	RoleDeleted           Kind = "role.deleted"            // Subject role
	Restore               Kind = "restore"                 // the store was replaced; Detail = [kind, archive or source id, boundary seq]
	Gap                   Kind = "gap"                     // an interval with revocations the journal may lack; Detail = [since, reason: "append_failed"]
	RestoreAborted        Kind = "restore_aborted"         // a restore whose boot swapped back; Detail = [restore line seq]: that restore line is neither resumed nor evidence (DATA6-3)
)

// Revocation is one line of the revocation journal (§5B.3.6). SEC2-10: one
// Subject cannot say what a role removal removed, hence Object and Detail.
type Revocation struct {
	Seq     uint64     // assigned by Append: header line is 0, then 1, 2, ... without gaps
	At      time.Time  // informational; no coverage rule reads it
	Kind    Kind
	Subject entity.Ref // e.g. {core, user, <id>}, {core, apikey, <id>}, {core, role, <id>}
	Object  entity.Ref // role.unassigned: the role taken away
	Detail  []string
	Epoch   int64      // the new credential epoch, when Kind bumps it
	// RestoreSeq is set on a line a post-restore hook appends: the seq of the
	// restore line whose hooks wrote it (0 otherwise; JSON "restore_seq"). Boot
	// re-apply skips such a line when that restore was aborted (DATA8-7).
	RestoreSeq uint64 `json:"restore_seq,omitempty"`
	Source  string     // writing binary: its binary ID (app.BinaryID; R5-7, FE5-3)
}

// Head identifies the last line a store has seen: c/backup/journal_head.
type Head struct {
	Seq uint64
	MAC string
}

// ServedBoot is the content marker c/backup/served_boot = {n, nonce}
// (§5B.3.6 row "Content marker").
type ServedBoot struct {
	N     uint64 `json:"n"`
	Nonce string `json:"nonce"`
}

// RevocationJournal is what core/auth and core/rbac receive from the kernel.
type RevocationJournal interface {
	// Append writes r as one fsynced line BEFORE the caller commits the
	// revocation, and stages c/backup/journal_head = max(head, r.Seq) into b by
	// participant binding (05-storage §5S.3.2), reading the head with
	// GetForUpdate (c/backup/journal_head is Serialized), so two revocations
	// committing out of order never move the head backwards; a conflict
	// retries the revoking batch, and a duplicate line is harmless (DATA4-7).
	// On error nothing was recorded and nothing staged: the caller still
	// commits the revocation and calls Gap(b, at) — a disk problem never
	// blocks a security action.
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}

// RestoreKind is the hooks' view of a swap. The swap's own kind is
// restorejournal.Kind ("archive", "revert", "checkpoint", "reset", "rewrite";
// KindReset and KindRewrite, 05-storage §5S.3.15 and §5S.3.13), the intent's
// "kind" field; a reset and M8's rewrite run no restore hook, so RestoreKind
// has no reset or rewrite value (R8-1; §5B.3.4 "M8's store rewrite").
type RestoreKind uint8
const (
	RestoreKindInvalid RestoreKind = iota
	KindArchive    // an archive
	KindRevert     // the undo point of the last restore
	KindCheckpoint // a pre-migration checkpoint (05-storage §5S.3.12)
	KindExternal   // replaced outside the staged and offline restore (a file copy, a hand-copied checkpoint), detected at boot (§5B.3.6)
)

// RestoreContext is what the kernel hands every restore hook at 06 P4d step 3.
// Hooks are idempotent: a crash before the journal reaches "applied" re-runs
// them at the next boot with the same context (DATA3-6).
type RestoreContext struct {
	ArchiveID string // backup.ArchiveID (journal cannot import backup); "" for revert and checkpoint restores
	Kind      RestoreKind
	Foreign   bool
	// Boundary is the journal seq the swapped-in store had seen (§5B.3.6):
	// min(the source's recorded journal seq, the swapped-in store's
	// journal_head); for KindExternal, the store's journal_head. AuditHighWater
	// is the manifest's (0 for revert, checkpoint and external).
	Boundary       uint64
	AuditHighWater uint64
	// Previous opens a read-only view of the pre-swap store (restore/<id>/previous/),
	// limited to the calling owner's own keyspace (audit's copy-forward is its
	// consumer); nil when there was no live store and for KindExternal. The
	// kernel opens the pre-swap directory with storage.Admin.OpenReadOnly
	// (05-storage §5S.3.10; COR7-8). For auth and rbac the kernel also builds
	// the owner's native store over the read-only DB, whose handles fail every
	// write with storage.ErrReadOnly, and hands it to the owner's hook function (auth:
	// RestoreDeps.Previous, a read-only auth.Store; rbac: a read-only
	// rbac.Store), so the nil-coverage fallback keeps every restriction the
	// pre-swap store holds (SEC5-10).
	Previous func(owner storage.Owner) (storage.Reader, error)
	// Revocations yields the verified journal lines after Boundary; nil when
	// coverage fails (§5B.3.6): foreign, host-loss, external, broken chain,
	// gap, other install.
	Revocations iter.Seq[Revocation]
	// Journal is the out-of-DB restore journal (staged, swapped, ... records).
	Journal []restorejournal.Record
	// Counts carries what earlier hooks returned (auth runs first), so audit's
	// hook can record core.restore.revoke with them.
	Counts map[string]int
}

// RestoreHook is the kernel's adapter over an owner's package function (auth's
// PostRestore, rbac's, audit's), bound to that owner's P4c handles. Hooks run
// in the order auth, rbac, audit; each returns per-family counts.
type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)

// The out-of-DB restore journal restore/<id>/journal.json holds
// restorejournal.Record lines (05-storage §5S.3.15, used under its own names, SIMP8-8).
// A restore's steps: "staged", "rechecked", "previous_moved",
// "staged_moved", "sidecars", "swapped", "opened", "restore_line",
// "hooks_done", "marker_written", "applied", "failed", "swapped_back",
// "restore_aborted", "rejected", "applied_offline" (DATA7-12). Seq (staged)
// is the outgoing store's journal head seq, the recorded boundary of a later
// revert (DATA2-25); HooksOwed (step "marker_written") marks a restore a
// build without hooks (R1) applied, whose hooks the next journaling boot runs
// (§5B.3.6); the Note of step "failed" names the
// binary whose boot failed (DATA8-6).
```

### 5B.3.3 Archive format `mediacore-backup/1`

```
Archive
  <prefix><YYYYMMDD_HHMMSS><ext>              ext = .tar.zst | .tar.gz | .tar   (+ ".sha256", sha256sum format)
    mediacore-manifest.json                    first entry
    db/...                                     the Pebble checkpoint
    config/<file>, config/config.boot.json
    artifacts/<owner>/<name>/...
    index/<module>/<name>.bleve/...            if IncludeDerived
    derived/<name>/...                         if IncludeDerived
    aux/<module>/<name>/...                    if IncludeRecreatable
Sidecars
  <archive>.manifest.json                      the manifest (MAC'd); source of the catalogue
  <archive>.verify.json                        last verification result {at, level, ok, detail}
```

(The encrypted variant, `<archive>.age`, is a deferred-build member, §5B.3.9.)

- **One file** (DATA-21, OPS-14). Core's layout serves core's own restore code only (D10): one tar stream,
  manifest first, so verification can read the manifest before anything else; the optional items are in the
  same file, so an archive and its catalogue entry are one unit.
- **Names.** ABO (the books module registers it) keeps HEAD's pattern
  `audiobooks_pebble_<YYYYMMDD_HHMMSS><ext>` (`backup.go:333`): keeping it is cheaper than renaming, and the
  manifest, not the name, tells a `mediacore-backup/1` archive from a `legacy-abo/0` one. Other apps use
  `<AppID>_<YYYYMMDD_HHMMSS><ext>`. The archive ID lives in the manifest. A name collision within one second
  waits for the next second. Only files with the app's registered prefix are ever listed or pruned (the prefix
  rule protects foreign files in a shared backup dir, `codec.go:225-245`).
- **Codecs** are sniffed by magic bytes on read (zstd, gzip, plain tar), as ABO does for
  compression (`codec.go:204-222`); `core.backup.compression` picks the writer (default `zstd`; ABO keeps
  its current `gzip` until the operator changes it, so an upgrade never changes the codec silently).
- **No key material in any archive.** The secrets keyring stays in the state directory and is recovered
  from 05-config's `secrets export-keyring`; the archive's secrets stay as the store holds them (ciphertext under the keyring; HEAD's representation in a `legacy-abo/0` archive). 05-config's
  `KeyIDsInUse`/manifest coupling is not used: the manifest records no key IDs, and restore computes the
  key IDs the restored records reference from the restored data (§5B.3.4 step 5).

**Derivation-key rotation** (SEC2-24; 05-config `secrets rotate-derive`). Every archive records the
derivation key ID it was made under (`Provenance.KeyID`); its MAC verifies with that key through
`DeriveKeyFor(KeyID, …)` while the key is `active` or `verify`. The scheduled `core.backup.verify` task rewraps (`Service.Rewrap`) every Own archive whose
key is in `verify` state, so no retained archive depends on a key once 05-config retires it; 05-config
keeps a `verify` key past `retire_after` while anything this file retains still needs it: a retained
archive, undo point or unconsumed checkpoint that records it, **or a line of the retained revocation
journal (or its header) written under it** (DATA3-13, SEC3-10), so retiring a key can never make the
journal's chain unverifiable. Retirement is decided in 06 P5, not inside `secrets.Open` (which runs at
the start of 06 P4b, 05-storage §5S.4 step 8, after the checkpoint and before the migration list, so before the backup directory an admin set in
Settings is known): the backup service calls
05-config's `secrets.RetireUnused` with that in-use set, computed from the manifests and the journal of
the **effective** `core.backup.dir` (COR3-2). An archive whose key is already retired (an imported one,
or a key retired before this rule) verifies as foreign and is restorable only as a foreign restore.

**Offline restore — the rollback path (D11).** 05-storage's `<app> storage restore-offline` (§5S.3.12, core
code from R1) is the one restore that runs with the service **stopped**, and the one `make rollback-restore` uses:
stop the unit, restore the latest pre-migration checkpoint (`--checkpoint <name> --accept-loss`) or an archive
(`--archive <file> [--keyring <file>] --accept-loss`) offline with the current binary, reinstall `.prev`, start (09). Before it touches anything,
`make rollback-restore` runs `.prev --print-storage-levels` and compares the result with the source's levels.
The source's levels are the checkpoint record's `from_vector`; for an archive (`ROLLBACK_ARCHIVE`) they are the
manifest's `Levels` when the archive is `mediacore-backup/1`, and for a `legacy-abo/0` archive (B7: every archive
made before R2's backup flip, which is the archive this path serves once the checkpoint's 7 days have passed, and
which carries no level stamps) the levels read from the extracted store itself, HEAD's legacy level key and its
`LegacyCap` stamp, as `VerifyOpen` reads them (§5B.3.4 step 4), never manifest stamps (05-storage §5S.3.12, row
`make rollback-restore`). When the levels differ (after an R2 hotfix `.prev` is an R2 build, not the pre-R2 binary)
the recipe refuses, printing the source's vector and naming the release to install by its tag, so it never restores
a store that `.prev` would migrate again (DATA-9): for a checkpoint, the pre-freeze release (the last production
release before F3 merged) by the record's `from_release`, the pre-freeze tag the cutover build embeds (05-storage
§5S.3.12 "How"); for an archive, whose checkpoint record may already be deleted, the release that produced it, the
manifest's `AppVersion` for `mediacore-backup/1` and, for `legacy-abo/0`, the version string the archive carries.
`make rollback-restore RELEASE=<that tag>` is the way past the refusal and is not an override of the check: it
first fetches that exact published release beside `.prev` (05-updater §5U.4, "Fetching a named release": the
release's raw asset, size-capped and self-tested as an apply does; the fetch has apply's trust level, HTTPS from the
configured release source plus the checksum check apply performs when the source publishes one, and is not a signed
release), runs the same comparison on the fetched binary,
and only when its levels equal the source's installs it as `.prev` and goes on; a release whose levels differ is
refused, the fetched file is deleted and the existing `.prev` is kept. Without `RELEASE` it refuses and names the
tag; no other binary or path is accepted. It never writes into an open
store, and it performs this file's journaled swap (§5B.3.4) in process. For `--archive` this package supplies the
offline extractor: the manifest check and `VerifyOpen` (§5B.3.4 steps 2–5, §5B.3.5), then extraction into staging.
What comes back is **one unit**:
- the DB directory and its sidecars;
- the **state files** the source holds: from a checkpoint, its `state/` copies (HEAD's `.encryption_key`, the
  gap-fill `config.yaml`, and the keyring only when the checkpoint holds one: R1's first-boot checkpoint and R2's
  are taken before any keyring exists and hold none; 05-storage §5S.3.12), each checked against its recorded SHA-256; from an
  archive, the config file and boot record when they are not redacted copies (else they are extracted to
  `restore/<id>/config.restored/`, as step 6 does);
- the **keyring**: from the checkpoint's copy when it holds one, or for an archive from the keyring export `--keyring` names
  (05-config's export, which carries the derivation key that verifies the manifest), else the state directory's
  current keyring when it covers the archive's secret records (step 5). A `legacy-abo/0` archive's four encrypted
  rows need HEAD's `.encryption_key` instead, which the migration's secrets step M2 reads from the state directory
  (05-config §1.12.2), so a host-loss restore of one also copies that file.

A restore replaces only the state files its source holds and **never removes or replaces a keyring the source
lacks**: the live keyring stays, because the revocation journal's MACs and a later re-upgrade need it (DATA-11).

Everything written after the source was taken is lost (imports, edits, reading progress the phone apps synced),
so restoring a completed checkpoint or an archive needs `--accept-loss`; the R2 release note and the operator
runbook say so (14 E9). A rollback across R2 also **brings back every credential R2 revoked**: a key revoked, a
user disabled or a password changed on R2 is live again on the pre-R2 binary, which has no journal and no hooks,
until R2 is deployed again and its owed hooks re-apply the journal (§5B.3.6 "Restores without hooks"). The
operator runbook warns of this before the recipe runs and tells the owner to repeat those revocations by hand
after the rollback, and the R2 release note says it in one sentence (SEC-10). When no post-restore hook is due the command finishes the journal itself
(`applied_offline`); otherwise it leaves it at `swapped` for the next boot (05-storage §5S.3.12), which on R1 is a
boot without hooks that records them as owed (§5B.3.4 step h; §5B.3.6 "Restores without hooks").

**Host-loss restore** (the same command, documented in the operator runbook): copy to the new host the backup
directory (archives, sidecars and the revocation journal) and the keyring export (05-config), then run
`<app> storage restore-offline --archive <file> --keyring <export> [--foreign]` before the first start, with a build at or
above the archive's upgrade floor: an archive older than the build's `UpgradeFloor` (R3 declares `InstallFirst:
<R2 tag>`) is refused at staging (step 4) naming the release to install first by its tag (`install <R2 tag> first`: install the R2 release, by its tag, first) — install that release, restore with it,
start it once so it migrates the store, then upgrade (DATA3-9). With the keyring in place and no live store, the
archive's MAC verifies and the restore is `Own`; otherwise it is foreign and needs `--foreign`. A `legacy-abo/0`
archive has no install marker; the restore hands the restore boot the revocation journal header's `install_id`
(else the keyring export's, 05-config) so the restored store keeps this install's identity (05-storage §5S.3.4
rule 4, DATA3-12). With no pre-swap store, nothing anchors the end of the revocation journal, so the restore treats
it as uncovered and every API key needs confirmation (§5B.3.6, SEC2-9).

**A production archive elsewhere is not inert (R6-7, OPS6-4).** An archive carries every password hash, invite and
stored secret (a pre-R2 archive also raw session tokens and the blob's plaintext secrets), and booting a restored
copy starts its schedules and outbound integrations with production URLs and credentials. The optional owner
rehearsal of R2 (09) therefore restores a production archive with `storage restore-offline --into` an empty path
and boots it only in a container with `--network none` and `core.integrations.disabled=true` in the operator layer
(05-metadata M1.4), schedules running. There is no scrub, no staging marker and no binary-identity sidecar, and
this file adds no "staging mode" that would make a plaintext archive safe to boot as it is.

### 5B.3.4 Staged restore

**Why not swap in place:** every subsystem holds the one store (`GatedDB` borrowers,
`store_from_store.go:12-63`; in core, every `Keyspace` handle), and derived state (memdb, bleve, hnsw,
caches, in-flight tasks) would describe the old data. A correct hot swap means quiescing and
re-initialising the whole process — which a restart already does. Restart-bound restore matches
restart-bound module enablement (02 decision 10).

API phase — task `core.backup.stage-restore` (`POST /api/core/v1/backups/{id}/restore`, permission
`core:backup.restore` (`Sensitive`, `AuthRequired`, 05-rbac), credential-change route with owner proof,
as today's `wire_system_routes.go:61-65`; a foreign restore also requires `core:admin` and an
interactive owner proof; §5B.3.8). The same steps run offline in `<app> restore --archive`:

1. Refuse with `storage.ErrSwapPending` while the one intent slot is taken — `restore/pending.json` exists,
   whatever its `kind` (archive, revert, checkpoint or a factory reset's `reset`) — or a swap of any kind is
   in flight (an `inflight.json` under `restore/`). This is the early refusal;
   step 8's atomic create is the one that decides, so two stagers — in one process or two, the kernel's or,
   before R2's backup flip, ABO's `internal/backup` — never both succeed, with no mutex (R8-1, SIMP8-1, COR8-6;
   `TestSwapSlotExclusive`).
2. Verify at `RestoreOptions.Verify` (default `VerifyOpen`). Legacy archives without a sidecar need
   `AllowWeakVerify` (today's `ErrVerificationUnsupported` choice, made explicit).
3. Provenance: `Own`, or `Foreign: true` (`ErrForeignArchive` otherwise). Audit `core.restore.stage`
   with `{foreign}` (05-rbac §7.1 action names).
4. Compatibility: `App` must match (`ErrWrongApp`); every owner level in the manifest ≤ the build's
   supported level (the guard's own comparison, 05-storage §5S.3.7); `PebbleFormat` ≤ the pin; every
   owner's version known, or newer with only `Additive` steps; else `ErrBackupTooNew`. Then every
   `UpgradeFloor` of the build against the archive's applied versions and done steps (the manifest's
   `Keyspaces`, or for a `legacy-abo/0` archive the versions read at `VerifyOpen`): an archive older
   than a floor is refused **before extraction** with `ErrUpgradeFloor` naming the release to install
   first, so it is never swapped in only to be refused at P2 (DATA3-9). Modules present in the archive
   but not in the build: restored, dormant, warning.
5. Secrets: open the staged checkpoint read-only and compute the key IDs its secret records reference
   with 05-config's `KeyIDsReferenced(ctx, r storage.Reader)` over that staged view. Any key missing
   from the keyring → `ErrUnreadableSecrets` unless `AcceptUnreadableSecrets` (warning: those secrets must be
   re-entered). A `legacy-abo/0` archive references no keyring key: HEAD's four encrypted rows carry no key ID
   and are decrypted after the restore by the migration's secrets step M2 with HEAD's `.encryption_key` from the
   state directory (05-config §1.12.2; DATA3-12). Core holds no HEAD row name, so staging cannot tell an archive
   whose rows were encrypted under another key file: M2 keeps HEAD's missing-key guard (boot stops before any
   write, naming the file and the override) and quarantines a row the key does not decrypt, which then reads
   "re-enter" in health, never fatal and never dropped (05-config; 14 E4).
6. **Pre-validate every rename** the boot phase will do (DATA-23): staging and each target on the same
   filesystem, parent writable, target not a mount point and not a single-file bind mount (device of the
   file ≠ device of its directory). The DB directory and its sidecars must pass (`storage.ErrRenameRefused`
   otherwise, the factory reset's pre-check sentinel too, 05-storage §5S.3.15). The config file and boot record are swapped only when `ReplaceConfig` is set, the archive
   holds the file itself (not a redacted copy) **and** they pass; otherwise they are extracted to
   `restore/<id>/config.restored/` for the operator to merge.
7. Extract into `<db parent>/.mediacore-restore/<id>/staging/` — the DB's filesystem, device-checked as
   `ensureSameDevice` does (`backup.go:577-598`), because the swap is a rename. Every file fsynced, then
   each directory.
8. Create `<state_dir>/restore/pending.json` = `{id, archive_id, kind, staging, targets[{from,to}],
   db_path, install_id, manifest_sha256, staged_by_build, expires_at, requested_by,
   requested_at}` (the shared `restorejournal.Intent`, 05-storage §5S.3.15; COR9-13) (`kind` `archive` or `revert`) —
   every path **absolute**, resolved now from 05-config's `BootView` (`core.storage.path`,
   `core.paths.state_dir`; COR2-8); `install_id` is the live store's marker or, on a host with no store,
   the revocation journal header's (else the keyring export's), which the restore boot writes into a
   marker-less restored store (05-storage §5S.3.4 rule 4, DATA3-12) — atomically, through
   `restorejournal.CreateIntent`, the one create both stagers call: temp file + fsync,
   `link(2)` to `pending.json`, which fails with `EEXIST` when the slot is taken (`storage.ErrSwapPending`;
   the staging directory is then removed), remove the temp, directory fsync; then the journal record
   `staged` to `<state_dir>/restore/<id>/journal.json`. The `staged`
   record carries the live store's `c/backup/journal_head` seq: the store it describes can only see more
   lines before the swap, so it is a safe boundary for a later revert of this restore (DATA2-25).
9. Return `202` with `restart_required: true`; with `Restart`, request a restart through 06's
   `Lifecycle.RequestRestart`, which drains and exits with the restart exit code, so the committed unit's
   `Restart=on-failure` brings the service back without an operator (OPS8-12). While a swap journal holds an
   `inflight.json` (created at the end of step b, before the first rename; R9-7), `make rollback`,
   `make rollback-restore` and the operator `deploy` recipe refuse (05-storage §5S.3.7, §5S.3.12), with no
   override and no exception for any kind: a swap that has started is finished by booting this build once
   (OPS8-7, SIMP8-1). The one recipe admitted while `inflight.json` exists is the escape from a `failed`
   `KindCheckpoint` journal (step x's exception): the previous binary's `storage restore-offline --checkpoint`. A staged intent that has not started does not block them: the binary they install rejects
   it at step a (`staged_by_build`, health `restore_rejected`), the offline restore rejects it itself
   (05-storage §5S.3.12), and an admin cancels it through `DELETE /restore/pending`. The updater refuses in both
   cases (05-updater's `ReaderCheck` tokens `restore_pending`, `restore_inflight`, whose message names the
   intent's `kind`, OPS-14). Over ssh the recipes make the one probe of 05-storage §5S.3.7, one literal sudoers
   command, which passes on a store that never staged anything, and a probe that cannot run refuses (OPS3-13,
   OPS4-3).

Boot phase — kernel **pre-boot** step P0a ([06](06-app-sdk.md) §6.5.1), before any config file or
storage is read. Once the deferred P−1 is built (A-6; no R0–R3 build runs it), the updater's pre-boot
check runs first (OPS2-3): it decides a rollback to `.prev` before any swap, and its deferred
`ReaderCheckOptions.IgnoreRestoreStagedBy` (05-updater §5U.3) ignores a pending intent staged by the failing build
as long as no swap step has started; the restored `.prev` then
rejects that intent at step a. P0a resolves only
the state directory, by 05-config's pre-boot rule (flags, env, compiled default, and next to the
database when that default cannot be created — HEAD's fallback, R3-4/OPS3-1; never the config file or
the DB) and trusts the **absolute targets recorded in `pending.json`**; 06 P2 then verifies that storage
opened at the recorded `db_path`, and a mismatch is a failure after the swap (step x) (DATA2-25). P0a
also **resumes** any restore journal whose in-flight file exists (from the end of step b until `applied`), and a
`KindCheckpoint` journal at `failed` unless this binary is the one its `failed` record names (DATA8-6), and
the kernel decides "this is a restore boot" from that journal, never from `pending.json` alone (DATA3-6).
The binary a `failed` record names refuses to boot while that journal is in flight, naming the operator
runbook's binary replacement (step x). **One slot, no precedence rule (R8-1).** There is one intent slot for every kind, so at
most one intent and one swap exist; a factory reset is the slot's `KindReset` (05-storage §5S.3.15), and
the former reset-supersedes-restore rule and its test are deleted:

| Step | Action (journaled in `restore/<id>/journal.json`; each step idempotent: rename only if source exists and destination does not) |
|---|---|
| a | Reject a **stale or foreign intent** (OPS-14), by the one rule for every kind (R8-1, SIMP8-1): `expires_at` passed or `staged_by_build` ≠ this build (another binary was deployed since staging, SEC7-5) → move `pending.json` to `restore/<id>/rejected.json`, boot on the **untouched** live data, health `degraded` (`restore_rejected`, naming the intent's kind). This rule stops a later binary from applying an intent by surprise. **The rule reads only `pending.json`** (R9-7, DATA9-4): once the intent has been renamed to its in-flight file (end of step b for a restore; before `set_aside_started` for a reset), the journal is **never rejected**, whichever core build boots it and however late — a restore is resumed or swapped back (step x), a reset is resumed forward to `set_aside` and reaches `rejected` only through its failure rule: before `set_aside_started` with nothing moved, after it once every moved target is back (05-storage §5S.3.15; R10-6). |
| b | Re-check staging against `manifest_sha256` and entry sizes (full re-hash if `core.backup.restore.rehash_on_boot`). Failure → reject as in a. Success → journal `rechecked`, then **rename** `pending.json` to `restore/<id>/inflight.json`, the one in-flight file of every kind, **before step c renames anything** (R9-7, DATA9-3). From here the in-flight probe (05-storage §5S.3.7's guard, 05-updater's `ReaderCheck`) reads `restore_inflight`, which no override passes, so a crash anywhere from step c on can never be followed by an older build on a half-swapped directory; a journal at `rechecked` with the in-flight file and no `pending.json` is a crash right after the rename and resumes at c. The file is not deleted until the journal reaches `applied` or `swapped_back`, so a crash anywhere between here and the hooks still resumes as a restore boot (DATA3-6). |
| c | Copy `<state_dir>/backup.location.json` to `restore/<id>/` **only if it exists** (R10-6, DATA10-4: it is written only from R2, and not on a host-loss restore; a missing source is not an error), temp file + fsync + rename (the original stays, because the restore boot locates the journal through it; R7-4, DATA7-5). Rename live items to `<db parent>/.mediacore-restore/<id>/previous/`: the DB dir, its sidecars (`.storage-levels`, `.storage-format`, `.migration-checkpoint`), and — only when being replaced — config file and boot record; index and derived dirs. |
| d | Rename staged items into place. |
| e | Rewrite the sidecars from the staged store: `.storage-levels` from its stamps and, for books, `.storage-format` (removes the "older archive, newer sidecar" case; DATA-23d, DATA4-17, OPS4-15), so no reader of a sidecar ever sees the pre-restore store between the swap and the restore boot's step 9 (journal step `sidecars`). |
| f | Mark every derived index for rebuild; included `Derived`/`Cache` items are offered to `Load` under the generation rule. |
| g | Journal `swapped` (the journal step only; the in-flight file has existed since step b, R9-7). The `restore` line of the revocation journal is **not** appended here — the keyring that MACs it is not open at P0a — but at P4d step 2 (§5B.3.6; journal `restore_line`). Boot continues: P0 reads the restored config (if replaced), storage opens at the recorded `db_path` (journal `opened`), a marker-less store gets `install_id` from the intent (DATA3-12), and the pre-migration checkpoint and the migration DAG bring an older archive forward (G2 rules): every versioned step the archive's store lacks runs again on it, one-shot steps included (a `legacy-abo/0` archive gets the whole R2 cutover, its preflight first: a refusal there is a step-x failure, below), so such a step must not trust values it did not vet (05-metadata M1.10's migrated `Local`, DATA5-9). |
| h | At 06 P4d step 3 (05-storage §5S.4 step 11 (c)) — **after** the keyring was opened at 05-storage §5S.4 step 8, step 1 attached the DB overrides and step 2 re-applied the journal tail and appended the `restore` line, before `auth.BootWrites` and any listener — the kernel runs the restore hooks in the order auth, rbac, audit with a `journal.RestoreContext` (`Previous` = read-only view of `previous/`, `Revocations` per §5B.3.6 or nil, the restore journal, the counts of earlier hooks). 05-authn re-applies the later revocations, deletes every session, invite and one-time token in auth's store, bumps epochs, marks `password.changed` users reset-required, and sets keys `needs_confirmation` when `Revocations` is nil (05-authn §4.14); 05-rbac re-applies role kinds; audit raises its `Sequence` past and copies forward the records above `AuditHighWater` from `previous/` and appends the restore records (`core.restore.revoke`, `core.restore.apply`). Every hook write is an ordinary write through the owning subsystem's native store (R3-1, R4-1), so the next boot reads the result as it is; there is no second copy to reconcile. Hooks are idempotent; the last hook batch records `c/backup/restore_hooked/<seq>` for the `restore` line (journal `hooks_done`). Only **then**, on a restore boot, is `c/backup/served_boot` written (`n = max(store n, file n) + 1`, a new nonce), and after it commits `backup.location.json` (the restored store's generation, content marker and committed head; journal `marker_written`), so a restore that fails before its hooks commit leaves the file describing the pre-swap store (DATA6-3); then journal `applied`, and `inflight.json` is deleted. **A build without P4d steps 2–3** (R1: no journal re-apply, no hooks, no `c/backup/` keyspace) takes its one legal edge, `opened --> marker_written` (05-storage §5S.3.16 machine 1): right after the DAG it journals `marker_written` with `HooksOwed` and then `applied`, writing no `c/backup/` record and no `backup.location.json`; the next journaling boot runs the owed hooks (§5B.3.6 "Restores without hooks"; DATA9-5, `TestRestoreBootWithoutHooksReachesApplied`). |
| x | **Automatic swap-back** (DATA-23): any failure after the in-flight rename that ends step b, **failures inside step c included** (a failed state-file copy, a live rename that fails although step 6 pre-validated it; machine 1's `rechecked --> swapped_back`, R10-6, DATA10-4), and any later one — a rename, storage opening at a path other than the recorded `db_path`, the guard refusing the restored levels, a cutover preflight refusal, a failing migration, a failing hook — closes storage (the journal component, stopped with it, has not rewritten the committed-head anchor since the swap, §5B.3.6 row "Head in the store"); when the boot already appended the `restore` line (a failure at or after P4d step 2, so the keyring is open), appends a `restore_aborted {seq}` line first (a failed append leaves the restore journal's `swapped_back` record as the evidence), so the line is neither resumed nor counted as evidence that the original store was replaced; `served_boot` and `backup.location.json` were not yet rewritten for the restored store (step h), and step x renames the copy step c saved back over `backup.location.json` (only when step c saved one; one that was absent stays absent), so the next boot reads the original store as the same store, its head equal to the anchor, and runs no hook (DATA6-3, R7-4, DATA7-5); the hook lines between the `restore` line and its `restore_aborted` line carry that line's seq (`RestoreSeq`) and are never re-applied (§5B.3.6 (3), DATA8-7 (a)); then it moves the restored items that are in place to `restore/<id>/failed/` (rename-if-exists: a failure inside c has moved none yet) together with every pre-migration checkpoint the restore boot took (their records name the restored store; one that cannot be moved is marked `restore_left`, so it is never reused or restored, 05-storage §5S.3.12, DATA3-1; both start their 7-day deletion clock at the swap-back, "Checkpoint retention" below; a `KindRewrite` swap-back is the exception and moves no checkpoint, rule (5) of the M8 paragraph below), renames `previous/` back, rewrites the sidecars from the original stamps, journals `swapped_back`, deletes `inflight.json` and exits non-zero. The automatic restart boots the original data with health `degraded` (`restore_failed`) and the reason. **A cutover preflight refusal on a restore boot is such a failure.** A staged `legacy-abo/0` archive that holds a row M3 cannot convert, or an account merge still `applying` or `undoing`, makes 05-storage's preflight (§5S.4 step 5a, §5S.3.13 "Cutover blockers") refuse; on a restore boot the refusal does not stop the boot with exit 78 and wait, because every tool that could then be run refuses while `inflight.json` exists. It swaps back at once: the pre-restore store returns, the refusal and its report (keys, families and reasons, never values) are logged, the restore is recorded failed (journal `swapped_back`) and the restart boots the original store `degraded` (`restore_failed`, reason `cutover_blocked`, naming the rows). The preflight runs before the checkpoint and before `secrets.Open`, so this boot took no checkpoint and appended no `restore` line, and no `restore_aborted` is written. On a host with **no** previous store, the journal records `no_previous` and storage refuses to create a fresh store at that path until the operator resolves it, so a failed host-loss restore never leaves an empty store behind (05-storage §5S.4 step 1, DATA3-9). If the swap-back itself fails, boot halts with the journal naming every path, and the offline `<app> restore --revert <id>` completes it. **Exception (DATA7-12, DATA8-6):** a `KindCheckpoint` restore boot whose migration DAG fails does **not** swap back, because the swap-back would only bring back the store whose migration failed: it keeps the restored checkpoint, journals `failed` with its own binary ID in the record's `Note`, keeps `inflight.json` and exits non-zero. The recipes and the updater keep refusing while the journal is in flight (step 9). The escape is `storage restore-offline --checkpoint <name>` run by the **previous** binary (any release from R1) with the service stopped, the one recipe admitted while `inflight.json` exists: it restores the checkpoint again, finishes the journal to `applied_offline` with `HooksOwed` and the boundary the checkpoint record's `hooks_owed` carries (the record copies both from the restore journal that produced the store it copied: here the failed restore boot's journal, whose hooks never ran), and deletes `inflight.json` on success (05-storage §5S.3.12 "In place", §5S.3.16 machine 1, `failed --> applied_offline (HooksOwed)`; DATA-7, `TestCheckpointEscapeKeepsOwedHooks`), so the next journaling boot runs the hooks the original restore owed, after which the operator starts the previous binary as the runbook says (09). A fixed build put in place instead resumes the journal at its P0a as a restore boot (`failed --> opened`) — its own DAG, the hooks, then `applied`. |

**A factory reset is `KindReset` of this swap (R8-1, SIMP8-1).** Its intent is `restore/pending.json` with `kind: "reset"`, created in the same slot by the same atomic create (step 8), so a reset and a restore exclude each other by the slot alone. It is rejected by step a's one rule only while its `pending.json` exists; its in-flight file `restore/<id>/inflight.json` is created by renaming the intent **before** the first target moves, and from then on the reset is never rejected but resumed forward by any core build (R9-6, R9-7, DATA9-4); its set-aside renames are journaled in `restore/<id>/journal.json` with the reset's step names, and the in-flight file is deleted at `hook_done` instead of `applied`; the tokens are `restore_pending`/`restore_inflight`, with no override (step 9). It differs from a restore in three places, all specified in 05-storage §5S.3.15: every target is pre-validated at staging and set aside beside itself, on its own filesystem; a failed set-aside always moves back what it moved **before** it journals `rejected`, booting the untouched store; and there are no restore hooks, because the next boot is a fresh install, while HEAD's in-request side effects run in the app's post-reset hook after the journal reads `applied`, are not undoable, and never fail boot. The hook's arguments carry the library `RootDir` only when the intent's `delete_library_files` is set: the native route sets it only on request and only after the client confirmed the preflight's root and count (`storage.ResetRequest`, 05-storage §5S.3.15; A-17, R9-6); A reset sets aside no checkpoint: the checkpoint directories stay under `.mediacore-checkpoints/` and keep the sweeper's 7-day clock, and none goes into `previous/`, whose lifetime is `core.storage.swap.keep_previous` (05-storage §5S.3.15, "Stage"; §5S.3.12, "Retention"). HEAD's reset routes are deleted in R2.

**M8's store rewrite is `KindRewrite` of this swap (05-storage §5S.3.13, §5S.3.16 machine 1; SEC-5).** Cutover
step M8 copies the compacted store into a fresh directory and swaps it in with kind `restorejournal.KindRewrite`,
which this section accepts as a swap kind under these rules: (1) it takes the one intent slot like every kind,
so it fails with `storage.ErrSwapPending` while another intent or swap exists, and its in-flight file exists
before its first rename (step b); (2) it runs **in process** during M8, not at P0a: storage closes, steps c–e set
the old directory aside, move the copy into place and rewrite the sidecars, and the store reopens at `db_path`
(`swapped --> opened`); step a's staleness rule and step b's archive re-check do not apply, and there is no step
f, because derived artifacts stay accepted; (3) it **keeps the store generation**: the kernel rewrites
`c/storage/epoch` with the new directory identity and the same `epoch`, so the attempt's checkpoint stays
restorable and §5B.3.6 reads the result as the same store (no `restore` line, no hooks, no `served_boot` or
`backup.location.json` write; `RestoreKind` has no rewrite value); (4) the set-aside old directory is **deleted at
once**, never kept as `previous/` for `core.storage.swap.keep_previous`, so `RevertLastRestore` and
`restore --revert` cannot undo it, and the journal then reaches `applied` with M8 `done` (`opened --> applied`);
(5) a failure from step c to that point swaps back as step x does, except that a `KindRewrite` swap-back **moves no
checkpoint and starts no clock**: the checkpoint directories stay in place under `.mediacore-checkpoints/`, never
moved to `restore/<id>/failed/` and never marked `restore_left`, because the store generation is unchanged and the
cutover's open checkpoint is the rollback once M7 has made the store unopenable by older builds (05-storage
§5S.3.12, "Retention"); and a kill is resumed forward or swapped back by P0a, after which M8 **derives its phase
from this journal, not from the copy it now runs on**, whose state still reads `copied` (05-storage §5S.3.13, M8):
M8 considers only the `KindRewrite` journals whose step ID (`StepID`, written by M8 at staging) is its own, so an
`applied` journal of an earlier M8 is ignored; when P0a resumed its swap forward (journal `swapped` or `opened`), M8
stages no second swap, deletes the set-aside old directory the journal names, records itself `done` and the journal
`applied`; when P0a swapped back, M8 starts again from its first phase **once**, and a second consecutive swap-back
of the same M8 fails the boot with `storage.ErrRewriteSwappedBack` (exit 78), naming `storage restore-offline` and
`make rollback-restore`, instead of looping (05-storage §5S.3.13, M8).

Undo: `previous/` is kept for `core.storage.swap.keep_previous` (05-storage §5S.7, a key of the swap that lands in R1; SIMP8-1) and then swept by the kernel after a boot reaches readiness. `RevertLastRestore` (API) and
`<app> restore --revert` (offline, for an app that no longer boots) stage it through the same mechanism
with kind `KindRevert`. 05-storage's offline `storage restore-offline --checkpoint` uses the same swap with kind
`KindCheckpoint`; all three are core code from R1 ("Releases"). A crash at any step resumes from the journal at the next boot: forward to `applied`
(re-running the idempotent hooks, `TestCrashBetweenSwapAndHooksResumesHooks`), or into the swap-back.

**Checkpoint restore (DATA7-12, R7-4).** Three rules make `storage restore-offline --checkpoint` followed by
`make rollback-restore`'s reinstall of `.prev` work (05-storage §5S.3.12): (1) for an **open** checkpoint (no
boot reached readiness since its `taken_at`, the failed-attempt case) the offline tool finishes the journal to
`applied_offline` itself, with no hooks, because nothing was served since the checkpoint (with `HooksOwed` when the
record carries `hooks_owed`, DATA-7), and deletes `inflight.json`, so nothing refuses `.prev`; (2) for `--accept-loss` on a completed checkpoint (the rollback
across R2 after R2 served) it leaves the journal at `swapped`, and `make rollback-restore` reinstalls `.prev`
itself after the restore, without a guard check, so `.prev`'s P0a resumes it as a restore boot — with its hooks
on R2 or later, through the hook-less edge with `HooksOwed` on R1 (step h); (3) a `KindCheckpoint` restore boot
whose DAG fails keeps the restored store, journals `failed` with its binary ID and exits non-zero (step x's
exception); the previous binary's `storage restore-offline --checkpoint` escapes it, clearing `inflight.json`, or a fixed build put in place resumes it as a restore boot (`TestRestoreCheckpointThenRollback`,
`TestCheckpointRestoreBootFailingStepKeepsCheckpoint`). In every case the checkpoint record turns `consumed` only
after the swap reaches `swapped` or `applied_offline`, through 05-storage §5S.3.16 machine 3's `open --> consumed`,
`completed --> consumed` and `served --> consumed` edges; a kill before that leaves the record in its earlier
state, and the restore is resumed or swapped back from the journal (DATA-6).

**Checkpoint retention (D12 (d); CONFORM-2, DATA-8).** Every pre-migration checkpoint is deleted by 05-storage's
checkpoint sweeper 7 days after it stops being needed (05-storage §5S.3.12): a completed checkpoint 7 days after
completion; an open checkpoint is kept while the live store is still at its pre-migration levels, because it is
the rollback, and its 7-day clock starts once the store has completed a migration or been restored by any path
(a checkpoint, an archive, a swap-back that moved it to `restore/<id>/failed/`, or one marked `restore_left`).
The sweeper walks `restore/<id>/failed/` too, audits each deletion as `core.storage.checkpoint_deleted` (from F2,
when the audit store exists), and health raises `storage_checkpoint_open` while any checkpoint waits and
`storage_checkpoint_mode` when a checkpoint directory or file is group- or world-accessible (05-storage
§5S.3.12; SEC-9). A factory reset and a `KindRewrite` swap-back move no checkpoint (the paragraphs above): each stays where it is, under the same clock. The sweeper and `storage prune-checkpoints` exist from R1, whose first boot on a non-empty store takes a
checkpoint, and R1's release note says so (CONFORM-6).

### 5B.3.5 Creation, verification, retention, catalogue

Create (task `core.backup.create`, `Resource` = `entity.Ref{Module: "core", Kind: "backup", ID: "main"}`,
exclusive with stage-restore, prune and rewrap):

1. Resolve the codec first (configuration errors before any work, the ABO ordering at
   `backup.go:428-440`). Secret-class items are skipped (manifest notes it), since archives are
   plaintext.
2. Retention **before** the space check (kept from `backup.go:445-458`).
3. Space check (`Avail` vs estimated size + margin).
4. Read the revocation-journal head seq (`JournalSeq`), the audit `Sequence` high-water mark
   (`AuditHighWater`) and the content marker `c/backup/served_boot` (`ServedBoot`, pair P) **before** the
   checkpoint call (DATA2-7), then checkpoint on the DB's filesystem
   with `WithFlushedWAL` (cross-device → `ErrCrossDevice`, never a silent copy); read the level stamps,
   the books legacy projection and every derived generation from it. A revocation appended before step 4
   is either in the checkpoint or above `JournalSeq`; the same holds for audit records. Right after the
   checkpoint, write a fresh pair P′ (`n` = max(P's `n`, the file's `n`) + 1, a new nonce) into the live
   store with `GetForUpdate` and then hand it to the journal component, the file's one writer, which writes
   it into `backup.location.json` (DATA7-14): every archive core makes therefore
   carries a pair older than the file, so an in-place restore of any of them — last night's included —
   reads **replaced** (row "Content marker"; SEC6-6, DATA6-4). A crash between the two writes leaves the
   store ahead of the file, which reads as the same store and is rewritten.
5. Capture artifacts into the run directory beside the checkpoint (hooks write there), so every input is
   an immutable local file; the config file is captured as 05-config's `RedactedConfigFile()` when
   `ConfigFileHasSecrets()` (`ConfigRedacted`), else as itself. Capture
   included derived items and mark stale every item whose source generation at the end of its capture
   differs from the checkpoint's (B1). Hash pass over all files (bounded pool), build the manifest,
   compute the provenance MAC under the active derivation key (`Provenance.KeyID` = 05-config
   `ActiveDeriveID()`), then stream the archive (manifest first) through tar → codec into a `.partial`
   file, re-hashing while streaming and aborting on any mismatch.
6. fsync, rename to the final names, write the `.sha256` and `.manifest.json` sidecars, fsync the
   directory, and refresh the catalogue entry.
7. If `core.backup.verify_after_create`, run `VerifyOpen`; write `<archive>.verify.json`.

Progress uses the existing phase model (checkpoint / archive / checksum / verify) with counters from
real work (`backup.go:51-82`) mapped onto `tasks` progress.

Verification task `core.backup.verify` (`Schedule: tasks.ScheduleFromConfig("core.backup.schedules.verify")`)
re-verifies the newest `core.backup.verify_keep` archives at `VerifyEntries` and the newest at
`VerifyOpen`, and rewraps every Own archive whose `Provenance.KeyID` is a derivation key in `verify`
state (§5B.3.3, SEC2-24). Create runs on `tasks.ScheduleFromConfig("core.backup.schedules.create")`. Both keys
are declared in backup's own namespace with type `tasks.ScheduleConfig` (R2-7, COR2-6, SIMP2-1). No
bespoke timer.

**Catalogue** (DATA-24). The archive list is **derived from the backup directory**: every
`<archive>.manifest.json` (MAC checked: unverifiable ones are listed as foreign) and `<archive>.verify.json`,
plus HEAD's `audiobooks_*` archives as `legacy-abo/0`. `c/backup/archive/<id>` is only a cache, reconciled from
the directory at boot and before every list and prune; it is never authoritative. After restoring archive X,
newer archives (including the safety archive taken just before the restore) therefore stay listed, stay
in the byte budget and keep protecting the floor. **Orphans** (DATA2-27): reconciliation drops every entry whose
archive is missing and deletes its orphaned sidecars (logged), so the catalogue never lists, counts or protects an
archive that no longer exists.

Retention (task `core.backup.prune`, also run inside create):

| Bound | Key | Default | Note |
|---|---|---|---|
| count | `core.backup.retention.max_count` | 10 | must be ≥ 1 (ABO's `0 = keep none` maps to 1) |
| bytes | `core.backup.retention.max_total_bytes` | 40 GiB | 0 = unlimited, as `backup.go:174-188`; an archive and its sidecars counted together |
| age | `core.backup.retention.max_age` | 0 (off) | |
| floor 1 | — | the newest **verified** archive | an unverified newer archive does not protect the last good one |
| floor 2 | — | any archive referenced by a pending or undo-able restore | (the plaintext floor of DATA-22 lands with encryption, §5B.3.9) |

Deletion order oldest first; an archive and its sidecars are deleted together; a failed
delete is not counted as freed (kept from `backup.go:1255-1262`). Prune also trims the revocation journal
(§5B.3.6) without breaking its chain.

Pre-operation backups: modules call `Deps.Backup.Ensure(ctx, maxAge, "pre:books.organize")` (organizer
today: 6 h, `organizer/service.go:552,697`); the updater's pre-update backup, which would call `Runner.Create` with reason
`pre-update`, is a deferred-build member of 05-updater §5U.3 (R10-8, OPS10-6: the R2 updater moves HEAD's
pipeline unchanged and takes no archive); the kernel calls `Ensure` with `core.backup.before_breaking_migration` before any boot whose
DAG holds a `Breaking` step.

### 5B.3.6 Revocation journal (out of DB)

Cross-cutting resolution 8 and SEC-16: a restore must not bring back a credential revoked after the
archive was taken. The record of those revocations therefore lives outside the DB that the restore
replaces, and — because the backup directory is untrusted storage (a network share, an off-host copy)
— it carries its own integrity (SEC2-9).

| Rule | Specification |
|---|---|
| Where (DATA2-27, DATA3-5) | `<backup dir>/<prefix>revocations.journal` (0600; JSON lines), where `<prefix>` is the app's archive prefix (ABO: `audiobooks_pebble_revocations.journal`), so two apps sharing a backup directory never share a journal. It sits beside the archives so an off-host copy of the backup directory carries it. `core.backup.dir` is **restart-bound** (§5B.7; declared from R2's identity flip in the kernel's core-section list, COR4-10, COR5-6), and `<state_dir>/backup.location.json` = `{backup_dir, journal, db_path, install_id, store: {epoch, dir_dev, dir_ino}, served_boot: {n, nonce}, head: {seq, mac}}` (paths absolute) records where the journal is and which store it describes (`db_path`, `install_id`), and — rewritten at the end of every journaling boot's P4d step 2 (on a restore boot only after the hooks, §5B.3.4 step h), after every `Create` (§5B.3.5 step 4) and by the committed-head anchor (row "Head in the store") — which store generation (05-storage `c/storage/epoch`), which content marker and which committed journal head it last served (DATA4-7, SEC5-6, DATA6-4). **One writer (DATA7-14):** in a running process only the journal component writes the file; it holds one mutex across every read-modify-write of it, and `Create` hands its new pair to the component instead of writing the file, so the anchor can never rewrite `{P, H′}` over Create's `{P′, H}` (`TestLocationFileWritersSerialized`). The only writer outside that process lifetime is restoreswap: step c saves a copy into `restore/<id>/` and step x renames it back (R7-4). Its `store`, `served_boot` and `head` are compared only when `db_path` equals the resolved `core.storage.path` and `install_id` equals the store's marker; otherwise the file describes another store sharing this state directory, the boot reads "no marker" and rewrites the file for this store (SEC6-8). At P4d step 2, when the effective directory (DB overrides attached at step 1) differs from the recorded one — an admin moved backups in Settings, or a restore brought back another override — the kernel first **moves** the journal: copy, verify the whole chain, fsync, rename, update the record; a move that fails verification keeps the old file and the boot reports `revocation_journal_broken`. **Not on a restore boot (DATA8-7 (b)):** while a restore journal is between `swapped` and `applied` the journal stays at the recorded location — the `restore` line and the hook lines go there, and a swap-back leaves it where it was — and the first boot after `applied` moves it. So a live directory change can never split the journal. **Colocation (SEC6-10):** with the default directory (`backups` beside the DB, `internal/backup/backup.go:145`) and the state directory's fallback to the DB's parent (`internal/config/state_dir.go:143-154`), one volume holds the store, the journal, `backup.location.json` and every marker, so a whole-volume or VM snapshot rollback reverts them together and every check reads "same store": revocations made after the snapshot come back undetected. Health reports `info` `revocation_journal_colocated` when the journal's filesystem device equals the store's, the operator runbook recommends a `core.backup.dir` on another volume and says to go back with the staged restore, never a volume snapshot, and 14 §14.5 states the residual. |
| Format and integrity (SEC2-9) | Line 0 is the header `{seq: 0, install_id, app, started_at, kid, base_seq, base_mac, mac}`; every later line is a `journal.Revocation` plus `kid` and `mac`, with `seq` = previous + 1 and `mac = HMAC-SHA256(DeriveKeyFor(kid, "core.backup/revocations/v1"), prev_mac ‖ canonical line without mac)`, `kid` being the derivation key ID the line was written under (05-config; a derive-key rotation changes `kid` mid-journal without breaking the chain). Deleting, reordering, editing or forging a line breaks the chain. |
| Head in the store (SEC2-9, DATA2-5, DATA4-7) | Every revoking batch also writes `c/backup/journal_head` = `{seq, mac}` (`RevocationJournal.Append` stages it by participant binding). `c/backup/journal_head` is `Serialized` (the `c/backup/` `storage.Spec`, registered by the kernel from R2's identity flip through `journal.StorageSpec()`, COR5-6), and `Append` stages `max(head, seq)` through `GetForUpdate`, so two revocations that commit out of append order never move the head backwards. The store — and so every archive and checkpoint made from it — knows the last line it committed; truncating the file below a store's head is detected. The head can still lag the journal on an unreplaced store (a crash between a line's fsync and its batch commit, a failed commit): that is a re-apply case, never a replaced store (row "Boot re-apply"). **Committed-head anchor (DATA6-4):** the journal component reads `c/backup/journal_head` (it holds the `c/backup/` handle) after its callers commit — at its next `Append`, on an in-process one-minute tick (SIMP-6's in-process exception) and at shutdown — and rewrites `backup.location.json`'s `head` (temp file + fsync + rename) whenever the committed head moved; P4d step 2 rewrites it after the re-apply. It reads only committed state, so it may lag the store but is never ahead of it, and a store whose head is below it was replaced (row "Boot re-apply" (2)). **Frozen during a restore (R7-4, DATA7-5):** on a restore boot, and whenever a restore journal is between `swapped` and `applied`, the component does not rewrite the anchor — not after the re-apply, not on the `restore` line's `Append`, not on a tick and not at shutdown; step h writes it with the marker, after the hooks, and step x never rewrites it but restores the copy step c saved, so a swap-back leaves the original store's head equal to the anchor (`TestRestoreFailureSwapsBack`). |
| Content marker (SEC5-6, SEC6-6, SEC6-7, DATA6-4) | `c/backup/served_boot` = `{n, nonce}` (`Serialized`; every write is a `GetForUpdate`, COR6-6), written by every journaling boot at the end of P4d step 2 (on a restore boot after the hooks, §5B.3.4 step h) and by every `Create` right after its checkpoint (§5B.3.5 step 4), each time with `n` = max(the store's `n`, `backup.location.json`'s `n`) + 1 and a fresh random nonce, and the same pair then written into `backup.location.json`. It is content, so it travels with the store: a DB directory copied **in place** from an archive or a checkpoint keeps the directory's identity and usually its `c/storage/epoch`, but not this pair. Because `Create` advances the live pair after its checkpoint, **every archive core made carries a pair older than the file**, whichever boot it was taken in, last night's included (SEC6-6). A store whose pair is older than the file's — a lower `n`, the same `n` with another nonce, or no pair while the file has one — was therefore **replaced**, whatever its directory identity (row "Boot re-apply" (2)); a store whose pair is ahead of the file's (a crash between the store write and the file write) is the same store, and the file is rewritten. The pair is compared only when the file describes this store (row "Where", SEC6-8). A store copied in place from a pre-R2 archive has no pair while the file has one, so it reads replaced by this rule ((2)(a)), unless a restore journal with `HooksOwed` names it (row "Restores without hooks"); the former "user rows but no session cut-off" signal of 05-authn is deleted, because it added no detection (R7-4, DATA7-10). **Residual:** a whole-volume or VM snapshot rollback reverts the store, the journal and the file together, so nothing detects it (row "Where"; 14 §14.5) |
| Writers (DATA2-5, COR4-4) | Every build from R2, in which every revocation path is core's (the identity and backup flips ship in the one R2 release, D12 (a)). The writers: `core/auth` (key revoke and disable, user disable and delete, credential-epoch bump, password change, identity unlink, session revocation) and `core/rbac` (`role.unassigned`, `role.narrowed`, `role.deleted`) — the kinds of §5B.3.2. `Append` is called **before** the revocation's batch commits; each line is fsynced. A recorded revocation whose commit then fails is harmless: re-applying it revokes more, never less. A restore boot (P0a swap, revert, checkpoint restore) and a boot that finds a replaced store append a `restore` line `{kind, source, boundary}` at P4d step 2, once the keyring is open and after the re-apply; every line a post-restore hook then appends carries that line's seq in `RestoreSeq` (DATA8-7). No earlier build writes the journal: lines need the keyring's derive key, which R2 creates (DATA3-19). **Offline revoking writes** (05-storage §5S.4 "Offline open" item 4): an offline command that commits a revoking write through `kernel.OpenOffline` (`users reset-password` bumping an epoch) appends exactly as the serving path does — the open opens the keyring as 05-storage §5S.4 step 8 does and resolves the effective `core.backup.dir` as P4d step 1 does, and `Append` runs before the batch commits with the head staged in it; a failed append commits the revocation with its `journal_gap/` record (row "Append failure"). The offline open re-applies nothing, runs no hook and appends no `restore` line (`TestOfflineRevocationJournaled`). |
| Restores without hooks (D11) | R1 has the journaled swap but no journal and no hooks, so a restore R1 finishes — its own staged restore, or one the offline restore left at `swapped` (§5B.3.3) — journals `HooksOwed` with its source's boundary (§5B.3.4 step h). An offline restore of a checkpoint whose record carries `hooks_owed` journals `applied_offline` with `HooksOwed` the same way (DATA-7). The first journaling boot that finds the latest restore journal for its `db_path` at `applied` or `applied_offline` with `HooksOwed`, and no `restore` line for it, treats the store as **restored by that journal**, not replaced: at P4d step 2 it appends the `restore` line with that boundary (a pre-R2 source has no journal head: 0), re-applies every verified line after it and runs the hooks with `Revocations` from it, so a revocation R2 made before the rollback stays revoked, and no key needs confirmation unless a `gap` lies after the boundary (`TestRollbackRestoreThenReupgradeKeepsCoverage`). Revocations R1 made while it served the restored store are in the store itself. `Previous` is the set-aside pre-swap store while `core.storage.swap.keep_previous` still keeps it, else nil. |
| Append failure | The revocation still commits — a disk problem never blocks a security action — and the same batch records `c/backup/journal_gap/<since>` through `Gap`; health is `degraded` (`revocation_journal_unwritable`) until the next successful append writes a `gap` line closing the interval. |
| Boot re-apply (DATA2-5, COR3-2, DATA3-5, DATA3-8, SEC3-10, DATA4-7, DATA4-15, SEC4-5) | At every boot, 06 P4d step 2 (05-storage §5S.4 step 11 (b)), after `secrets.Open` opened the keyring at 05-storage §5S.4 step 8 (every line's MAC needs `DeriveKeyFor`) and P4d step 1 attached the DB overrides (so the journal is found under the effective `core.backup.dir`, moved first if needed, row "Where"), in this order: **(0) install check** — the journal header's `install_id` is compared with `c/storage/install` **before** any line is used; on a mismatch (a core build's factory reset, 05-storage §5S.3.15, or a deleted DB directory gave a new install while the backup directory kept the old install's journal, whose MACs still verify under the same keyring) the file is renamed aside (`<name>.other-install-<ts>`), a new journal is started with a `gap` line, nothing is re-applied, and the boot reports `revocation_journal_reset` (DATA4-15). A restored `legacy-abo/0` store is not a mismatch: the marker-less store inherits the install's `install_id` from the restore intent (05-storage §5S.3.4 rule 4), so steps (2)–(4) re-apply the verified tail before the hooks (DATA5-13). **(1) head before** — read `head_before`, the store's `journal_head`, before anything is appended this boot; every comparison below uses `head_before` and the seq of the journal's last **revocation** line (`gap` and `restore` lines are bookkeeping and never count) (SEC4-5). **(2) classify** by store identity and content, never by head lag alone (DATA4-7, DATA5-7, SEC5-6, DATA6-4, DATA6-5). This is **the one classifier**: 06 P4d step 2, 05-storage §5S.4 step 11 (b) and 05-authn §4.14 cite it, and no other file adds a signal (R7-4, DATA7-10): a P0a restore boot, or a store a `HooksOwed` restore journal names (row "Restores without hooks"), is a restore boot, and none of the evidence below applies to it (R6-4); otherwise, with `backup.location.json` describing this store (row "Where"), the store was **replaced** outside the staged and offline restore (a file copy of an old store, a hand-copied checkpoint) when **any** of these holds: (a) its `c/backup/served_boot` pair is older than the file's (row "Content marker"); (b) `head_before` is below the file's committed-head anchor (row "Head in the store"), which catches an in-place copy that kept the pair; (c) `c/storage/epoch` names another generation or directory identity than the one recorded **and** the journal shows the store is not the current one — `head_before` is below the journal's last revocation line, or the journal holds a `gap` or `restore` line after `head_before` (a `restore` line with a `restore_aborted` line, or whose restore journal reads `swapped_back`, does not count, DATA6-3), or the store has no head while the journal has revocation lines; a new generation or directory identity **without** such evidence is the current store **relocated** (a cross-device move, an rsync to a new disk, a volume migration, which `cmd/root.go:324-327` expects): the boot records the new identity in `backup.location.json` and runs no hooks; when generation and marker match, it is the same store, whatever its head. **(3) re-apply** — every verified line after `head_before` is re-applied through auth's and rbac's stores in batches that also write the new head, **except a hook line of an aborted restore**: a line whose `RestoreSeq` names a `restore` line that a `restore_aborted` line closes, or whose restore journal reads `swapped_back`, is verified (it is part of the chain) but never re-applied, because the store it narrowed was swapped away (DATA8-7 (a)); a store with no head never committed a journaled revocation (it predates the journal, or is an older archive), so every verified line is re-applied. On the **same store** this is all: a lagging head (crash after `Append`, failed commit) re-applies the tail, narrowing and idempotent, and runs no hooks. **(4) replaced store (DATA3-8)** — step 3 runs the hooks with `KindExternal` (`Previous` nil, so no audit copy-forward; `Revocations` **nil**, since nothing anchors the journal's end once the store that held the head is gone: every key needs confirmation, SEC4-6) and a `restore` line `{kind: external, boundary: head_before}` is appended. **(5) bookkeeping lines** — only now is, on a restore boot, the `restore` line (unless the journal already holds one for this restore) appended, each with its head update in its own batch; then, **except on a restore boot**, the boot writes `c/backup/served_boot` and then `backup.location.json` with this store's generation, that pair and the committed head; a restore boot writes them only after its hooks commit (§5B.3.4 step h, DATA6-3). A `restore` line without its `c/backup/restore_hooked/<seq>` record is resumed by the next boot, never duplicated — unless a `restore_aborted` line names it or its restore journal reads `swapped_back`: then it is neither resumed nor evidence (DATA6-3). A store replaced by a file copy, with no revocation after it, is detected by its content marker although its head equals the journal's end (DATA4-7). **Chain break** (a missing, edited or unverifiable line): when `head_before` is below the last revocation line, re-application cannot know what it skipped, so the boot fails closed through auth's restore fallback — every API key `needs_confirmation`, every credential epoch bumped — and reports `revocation_journal_broken`; with the head at the end, only the health report (SEC3-10). |
| Coverage at restore | `RestoreContext.Boundary` = min(the source's recorded seq — the manifest's `JournalSeq`, the `staged` record of the restore being reverted, or the checkpoint record's `journal_seq` (05-storage §5S.3.12) — and the swapped-in store's own `journal_head.seq`). `Revocations` yields the lines after `Boundary` only when: the journal's `install_id` equals the archive's (a restore boot inherits the install's ID, 05-storage §5S.3.4 rule 4, so an old archive of this install still matches, DATA3-12); the chain verifies from `Boundary` to the **pre-swap store's** `journal_head` (the journal is not truncated below what the live store had committed); and no gap — a `gap` line in the journal (an append failure) or an open `c/backup/journal_gap/` record in the pre-swap store — lies after `Boundary`. Otherwise it is nil and 05-authn sets every API key `needs_confirmation` (§4.14 step 3). Foreign, host-loss and external restores (no pre-swap store, so nothing anchors the journal's end) always get nil. **Pre-swap restrictions win (SEC5-10, SEC6-4):** a demotion or revocation whose `Append` failed, or one in an interval that detection missed, is in no journal line but is in the pre-swap store; so **whenever `Previous` is non-nil** — not only when `Revocations` is nil; with full coverage the result equals the journal re-apply for journaled kinds — each owner's hook keeps the **most restrictive** of the restored and the pre-swap state through the read-only pre-swap store the kernel builds for it — rbac removes assignments absent there, intersects permissions and deletes roles deleted there (05-rbac §7.1), auth keeps a key revoked there revoked instead of merely `needs_confirmation` (05-authn §4.14) (`TestRestore_UnjournaledDemotionHeldByPreSwapStore`). A replaced store has no pre-swap store; that residual is listed in 14 §14.5. |
| Retention | Prune drops lines whose `seq` is at or below the smallest boundary any retained archive, undo point or unconsumed checkpoint records, by rewriting the journal (temp file + rename) with a new header whose `base_seq`/`base_mac` are the last dropped line's, so the chain continues. Every `kid` that a retained line or the header carries stays in the derive-key in-use set (§5B.3.3), so retirement never breaks verification (DATA3-13). |

### 5B.3.7 Provenance (SEC-16)

Provenance, not encryption, decides whether an archive is trusted (and once encryption is built, age
gives confidentiality, not authenticity: recipients are public, so anyone could make an archive that
"decrypts" here). The manifest's `Provenance.MAC` is HMAC-SHA256 over the canonical manifest bytes (with
the MAC field empty) under `DeriveKeyFor(Provenance.KeyID, "core.backup/manifest/v1")` of this install's
keyring (05-config; the derivation key survives data-key rotation, and its own rotation leaves older
keys verify-only until retired, SEC2-24). The MAC'd bytes include `install_id`, `key_id`, every entry
hash of the archive, the levels and the journal and audit boundaries, so no entry can be swapped or
edited. An archive is `Own` when the MAC verifies **and** `install_id` equals the live
store's `c/storage/install` (or no live store exists, the host-loss case once the exported keyring is in
place). Restoring a `legacy-abo/0` archive does not change the install's ID (the restore boot writes the
inherited one, DATA3-12), so this install's later `Own` archives stay `Own`. Everything else — including every `legacy-abo/0` archive and every archive whose `key_id` is
retired — is restored only as a foreign restore (B5).

### 5B.3.8 Routes (SEC2-8)

All under `/api/core/v1`, registered by `core/internal/backuphttp`. HEAD's `/api/v1/backup/*` paths are
deleted in R2; the SPA switches in the same release (§5B.10).

| Route | Permission and guards | Audited |
|---|---|---|
| `GET /backups`, `GET /backups/{id}` | `core:system.view` | no |
| `POST /backups/{id}/verify` | `core:system.manage` (it starts `VerifyOpen` work: a checkpoint open and a full hash, which a view-class scope may not start, 05-rbac §4.10, SEC3-12) | no |
| `POST /backups` (create) | `core:system.manage` | `core.backup.create` |
| `DELETE /backups/{id}` | `core:system.manage` + `Guards.Interactive` | no (floors refuse protected archives) |
| `POST /backups/import` | `core:system.manage` + `Guards.Interactive` | `core.backup.import` |
| `GET /backups/{id}/download` | `core:admin` + `Guards.Interactive` + `Guards.Owner` (with 05-authn §4.8's fallback): an archive is credential-equivalent (B1), so taking one offline is gated at least like a restore (SEC3-7); streams the archive as stored | `core.backup.download` |
| `POST /backups/{id}/restore`, `POST /restore/revert`, `DELETE /restore/pending`, `GET /restore/pending` | `core:backup.restore` (`Sensitive`, `AuthRequired`) + `Guards.Interactive` + `Guards.Owner`; a foreign restore also `core:admin`; `GET` needs only `core:backup.restore` | `core.restore.stage` (`{foreign}`), then `core.restore.apply` at boot |
| `POST /backups/{id}/rewrap` | `core:system.manage` + `Guards.Interactive` | no |

(`ExportPlain`, the CLI-only decryption of an encrypted archive, lands with encryption, §5B.3.9.)

### 5B.3.9 Deferred-build members

Archive encryption lands when an app or the owner asks for it (03 §3.4; SIMP4-1, SIMP4-5); no consumer
exists during the extraction. The first column holds package-qualified Go identifiers and the
second every other item (COR5-16, SIMP5-6); the table is planning information, which nothing checks in CI
(A-4). The PR that builds one deletes its entry. It lands additively: an absent `encrypted` field
reads as plaintext.

| Member | Also deferred (not checked) |
|---|---|
| `backup.Manifest.Encrypted`, `backup.Manifest.Recipients`, `backup.Archive.Encrypted`, `backup.Service.ExportPlain`, `backup.ErrDecrypt` | B3; the CLI `<app> backup export-plain`, the offline restore's `--identity`, and the re-encryption behaviour of the built `backup.Service.Rewrap` (a restriction on a built member, which an absence test cannot check; SIMP5-6) |
| — | the age writer and reader in `core/internal/backuparchive`, the `.age` variant and the age-header sniff; `filippo.io/age` stays out of `go.mod` until then |
| — | config keys `core.backup.encryption`, `core.backup.recipients`; health token `backup_encryption_pending`; metric `mediacore_backup_encryption_active` and the `encrypted` label of `mediacore_backup_archives` |
| — | the plaintext-archive retention floor (DATA-22's floor 2) and TLS material (`certs/`) in archives: both only mean something once encrypted archives exist |
| — | tests `TestEncryptionInactiveUntilExportAndBound`, `TestEncryptedArchiveNeedsIdentity`, `TestUpgradeDoesNotSwitchToUnrecoverableArchives` |

**Contract at proof depth** (B3; DATA-22, SIMP-9, SIMP2-2). `filippo.io/age` (BSD-3-Clause; latest stable recorded in
[13-toolchain](13-toolchain.md)). Recipients: (1) the **install archive identity**, an X25519 identity
whose scalar is the keyring's derivation output for info `core.backup/age-identity/v1` (05-config §2.4,
through the `core.backup.` secrets scope) — **not a file**: there is no identity file, no identity
export and no second "not exported" health reason, because the keyring export recovers it; (2) zero or
more **operator recipients** (`core.backup.recipients`, public keys only), whose identities never touch
the server and which an operator adds to separate archive decryption from secret access. Encryption is
**active** only when all hold:

1. `core.backup.encryption = age`. Default: `none` on an install whose store existed before its first
   core boot (an upgrade), so an upgrade never switches an operator to archives they may not yet be able to
   open; `age` otherwise.
2. 05-config's export record `<state_dir>/secrets.keyring.exported` covers the **active derivation key**
   (`secrets_keyring_not_exported` is not raised for it), **or** at least one operator recipient is
   configured.

While `age` is configured but not active, archives are written unencrypted, secret-class items are
omitted (the config file as its redacted copy), and health is `degraded` with token
`backup_encryption_pending` and the reason (`keyring_not_exported`). An encrypted archive is never created
before the keyring export or an operator recipient can open it.

## 5B.4 Lifecycle

Phases are 06 §6.5.1's (R2-8).

- Construction: `backup/journal` is opened by the kernel from R2's identity flip at P4d step 2 — after `secrets.Open` opened
  the keyring at 05-storage §5S.4 step 8 (after the checkpoint, before the DAG) and P4d step 1 attached the DB
  overrides, so its file is resolved from the effective
  `core.backup.dir`, which the kernel's core-section list declares from then on (COR4-10, COR5-6), and every line verifies (COR3-2,
  DATA3-5) — and handed to `auth` and `rbac`;
  `backup` itself is built in P5 after storage and secrets, before modules, and then decides derive-key
  retirement by calling `secrets.RetireUnused` with the in-use set of §5B.3.3 (archives, undo points,
  checkpoints and journal lines of the effective directory; COR3-2, DATA3-13). Module artifacts
  registered through `Registrar.Backup` ([06](06-app-sdk.md)) are validated in P7 (unique names,
  required `Consistency`).
- Boot: the pending-restore swap, or the resumption of an in-flight one, is P0a (§5B.3.4; core code
  from R1; after the updater's P−1 once that deferred phase is built, A-6); storage then opens at the recorded DB path. At P4d step 2 the
  journal is located (and moved if needed), its header's `install_id` checked against
  `c/storage/install`, `head_before` read, the boot classified by content marker, committed-head anchor and
  store generation with journal evidence (replaced, relocated or the same store; a restore boot, owed hooks included, is none of these), the tail re-applied, and only then the `restore` line appended (§5B.3.6); at step 3 the restore hooks
  run on a restore boot or a replaced-store boot. After services are built the catalogue is reconciled
  from the backup directory (orphans removed), and interrupted `.partial` archives and staging dirs
  older than 24 h are swept (ABO `sweepStaleCheckpoints`, `backup.go:600-640`).
- Schedules: `core.backup.create` and `core.backup.verify` through `tasks.ScheduleFromConfig`.
- Shutdown: a running create is cancelled at an entry boundary; its partial files are removed.

## 5B.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Codec/recipient/config invalid | closed before any work | ABO lesson: discover config errors before pruning and checkpointing (`backup.go:428-440`). |
| Insufficient space | closed (`ErrInsufficientSpace`) after retention ran | Full disk kills Pebble's WAL (`backup.go:85-100`). |
| Checkpoint or restore staging cross-device | closed (`ErrCrossDevice`) | A silent multi-GB copy is worse than a fast failure (`backup.go:477-498`). |
| Secret-class item | closed for that item (skipped), noted | Never write secret material in clear; archives are plaintext until encryption is built (§5B.3.9). |
| Artifact hook fails | closed (archive aborted, partial removed) | An archive missing a system-of-record artifact is not a backup. |
| Derived/recreatable capture fails or goes stale | open (item omitted or marked `stale`) | Rebuildable on restore. |
| Retention delete fails | open (logged; budget not credited) | Kept from `backup.go:1255-1262`. |
| Delete of a floor-protected archive | closed (`ErrProtectedByFloor`) | The floors are the recovery guarantee. |
| Post-create verification fails | closed: archive marked failed, not counted as a floor, alert | An unverifiable archive must not protect itself from pruning. |
| Restore verify fails / checksum mismatch / no identity | closed (`409` / `400`), nothing staged | Kept fail-closed semantics of `handler.go:870-895`. |
| Foreign provenance without `Foreign` | closed (`ErrForeignArchive`) | Account takeover through a planted archive (SEC-16). |
| Archive too new / wrong app | closed | Older build would misread. |
| Keyring lacks the archive's keys | closed (`ErrUnreadableSecrets`) unless `AcceptUnreadableSecrets` | Silent loss of secrets is worse than a refusal. |
| A rename target fails the pre-check | closed for the DB (`storage.ErrRenameRefused`); for config/boot record: not swapped, staged for the operator | A swap must not move the DB and then fail on a bind-mounted config file (DATA-23a). |
| Stale or foreign pending intent at boot (no swap started): expired or staged by another binary | open for the app (boot on live data), closed for the restore (rejected) | OPS-14: no surprise restore after another binary was deployed (SIMP8-1); after the updater's rollback, `.prev` rejects the failing build's intent (OPS2-3). |
| A second staging (any kind) while the slot is taken or a swap is in flight | closed (`storage.ErrSwapPending`), nothing staged | One slot, created atomically, across processes (R8-1). |
| Storage opens at a path other than the recorded `db_path` | closed for the restore: automatic swap-back | The swap must never be applied to one path and served from another (DATA2-25). |
| Boot-time staging re-check fails | open for the app (boot on live data), closed for the restore (rejected) | Live data is untouched; refusing to boot would turn a bad archive into an outage. |
| Archive below the build's upgrade floor | closed at staging (`ErrUpgradeFloor`, names the release to install first), nothing extracted | It would be swapped in and refused at P2; on a fresh host it would leave an empty store (DATA3-9). |
| Any failure after the swap (guard, cutover preflight refusal, migration, hook) | closed for the restore: automatic swap-back (checkpoints of the restore boot, if it took any, moved aside and deleted 7 days later; a preflight refusal comes before the boot's checkpoint, so none), exit, restart on the original data with health `degraded`; with no previous store, storage refuses to create one | DATA-23b: never leave the app down with live data parked in `previous/`; never reuse a checkpoint of the restored store (DATA3-1, DATA3-9). |
| Crash between the swap and the hooks | resumed: `inflight.json` keeps it a restore boot, hooks re-run (idempotent); rollback tooling keeps refusing | Dropping the hooks would serve revoked credentials (DATA3-6). |
| Swap-back fails | closed (boot halts; journal names every path; offline `restore --revert`) | Manual recovery with exact instructions beats guessing. |
| Crash during the swap | resume from journal | Each step idempotent. |
| M8's `KindRewrite` swap fails or is killed between step c and `applied` | closed for the rewrite: swapped back as step x does, with no checkpoint moved, the old directory back in place and the store generation unchanged, and M8 runs again once at the next boot (a second consecutive swap-back of the same M8 fails the boot with `ErrRewriteSwappedBack`, exit 78, naming `storage restore-offline` and `make rollback-restore`); or, after a kill past `swapped`, resumed forward by P0a, and M8 then deletes the old directory and finishes without staging a second swap | The live store is never left half-swapped, no restorable checkpoint is orphaned, and M8 never re-stages a swap into its own occupied slot (05-storage §5S.3.13). |
| Post-restore hook fails | handled by the swap-back row | Resurrecting revoked credentials is a security failure (05-authn §4.14). |
| A `KindCheckpoint` restore boot's migration DAG fails | closed: no swap-back; the restored checkpoint is kept, the journal reads `failed` with the failing binary's ID, exit non-zero; the recipes keep refusing while `inflight.json` exists, the operator puts the previous binary or a fixed build in place by hand (runbook), and its P0a resumes the journal; the failing binary refuses while the journal is in flight | Swapping back would only bring back the store whose migration failed (DATA7-12); the failing binary resuming its own journal would loop (DATA8-6). |
| Revocation journal append fails | open (revocation commits; gap recorded; health `degraded`) | Never block a revocation; the gap makes a later restore fail safe (keys need confirmation). |
| Revocation journal chain broken or truncated below the live store's head | open for the app (health `degraded`, `revocation_journal_broken`); closed for coverage (`Revocations` nil → keys need confirmation); when the store's head is below the journal's end, closed for credentials too (every key `needs_confirmation`, every epoch bumped) | A tampered journal must never make a restore look covered, nor leave skipped revocations unapplied (SEC2-9, SEC3-10). |
| A restore applied without hooks (R1) | open: the next journaling boot runs the owed hooks with the source's boundary, and no key needs confirmation unless a gap lies after it (§5B.3.6 "Restores without hooks") | A rollback across R2 and a later re-upgrade must keep R2's revocations without locking out every key (D11). |
| Store replaced outside the staged and offline restore (no restore journal; an older content marker, a head below the committed-head anchor, or another store generation with journal evidence that it is not the current store) | open: tail re-applied, hooks run with `KindExternal`, `Revocations` nil, keys need confirmation | A file copy must not keep revoked credentials or old sessions (DATA3-8, DATA4-7, SEC5-6). |
| Current store relocated (another generation or directory identity, same content marker, no journal evidence) | open: the new identity recorded, no hooks | A routine move of the DB to another disk or pool lost nothing; running the replaced-store hooks would lock out every key and sign everyone out (DATA5-7). |
| Same store, head behind the journal's last revocation line (crash after `Append`, a failed commit, out-of-order commits) | open: tail re-applied, no hooks | Re-applying narrows and is idempotent; treating it as a replaced store would lock out every key (DATA4-7). |
| Journal header names another install (a core build's factory reset, 05-storage §5S.3.15, or a deleted DB directory) | open: journal set aside, new journal with a `gap` line, nothing re-applied; health `revocation_journal_reset` | Another install's revocations must never be replayed onto this one (DATA4-15, DATA6-10). |
| A restore whose hooks fail and swap back | open for the app: the original store boots as the same store, no hooks, no key needs confirmation, no epoch bumped; the restore line is closed by `restore_aborted`, the hook lines it wrote are never re-applied, and `backup.location.json` and the journal's location are as before | The failed restore's marker, location record, journal location and hook lines must never make the original data read as replaced, as an unfinished restore, or as revoked (DATA6-3, DATA8-7). |
| Journal on the store's filesystem device | open, health `info` `revocation_journal_colocated` | A whole-volume snapshot rollback would revert journal and markers with the store (SEC6-10). |
| Archive's derivation key retired | closed for `Own` (verifies as foreign) | The verify task rewraps before retirement (SEC2-24); after it, only a deliberate foreign restore remains. |

## 5B.6 Concurrency contract

- One backup-resource holder at a time (create, prune, stage-restore, rewrap): task `Resource`
  exclusivity from [05-tasks](05-tasks.md); `List` and `Verify` of other archives run concurrently.
- The checkpoint does not block writers (Pebble hard-links SSTs; the WAL copy is bounded by
  `WithFlushedWAL`).
- Archive writing is a single sequential stream per archive (tar is sequential); hashing for the
  manifest uses a bounded pool (`core.backup.hash_workers`, default `min(4, NumCPU)`) over the
  immutable run-directory files, partitioned by file.
- All long steps check `ctx` between entries and between 1 MiB chunks; cancellation removes `.partial`
  files and staging dirs.
- Revocation journal appends are serialised by one mutex per process; each line is written and fsynced
  before `Append` returns.
- `Service` is goroutine-safe; the HTTP layer never runs a backup on the request goroutine (task
  enqueue + `202`).

## 5B.7 Config keys

Protection levels are 05-config's (SEC-6, SEC2-8): a key that decides who can read future archives, or
whether they are protected at all, is `ProtectAdmin`; one that can delete archives sooner is
`ProtectInteractive`. 05-config's classifier test (`TestEveryURLPathOrSignInKeyClassified`) adds the
"key / recipient / encryption" shapes so a new key of that kind cannot ship unclassified.

| Key | Type | Default | Hot-reload? | Protection |
|---|---|---|---|---|
| `core.backup.enabled` | bool | true | yes | admin |
| `core.backup.dir` | path | `backups` beside the DB (relative values resolve against the DB's parent, `backup.ResolveDir` semantics, `backup.go:120-146`; ABO's `backup_dir` is renamed to it by the R2 config cutover) | no: restart-bound; the next boot moves the revocation journal (§5B.3.6, DATA3-5); declared from F2 (the foundation PRs, dark) in the kernel's core-section list with every `core.backup.*` key, adopted by `backup` at its flip (COR4-10, COR5-6, IMPL-1) | admin |
| `core.backup.schedules.create` | `tasks.ScheduleConfig` | `{enabled: true, every: 24h}` (the durable clock's first-sight seed spreads installs, 05-tasks §5T.3.8) | yes | none |
| `core.backup.compression` | enum `zstd`/`gzip`/`none` | `zstd` (ABO keeps `gzip` until changed) | yes | none |
| `core.backup.compression_level` | int | 0 = codec default | yes | none |
| `core.backup.include_derived` | bool | false | yes | admin |
| `core.backup.include_recreatable` | bool | false | yes | admin |
| `core.backup.verify_after_create` | bool | true | yes | interactive |
| `core.backup.schedules.verify` | `tasks.ScheduleConfig` | `{enabled: true, every: 168h}` | yes | none |
| `core.backup.verify_keep` | int | 3 | yes | none |
| `core.backup.retention.max_count` | int ≥ 1 | 10 | yes | interactive |
| `core.backup.retention.max_total_bytes` | int64 | 40 GiB | yes | interactive |
| `core.backup.retention.max_age` | duration | 0 | yes | interactive |
| `core.backup.before_breaking_migration` | bool | true | no | admin |
| `core.backup.hash_workers` | int | `min(4, NumCPU)` | yes | none |
| `core.backup.restore.rehash_on_boot` | bool | false | yes | none |

The swap's intent lifetime and `previous/` retention are 05-storage's `core.storage.swap.intent_ttl` and
`core.storage.swap.keep_previous` (R1), which this package reuses for every kind; the former
`core.backup.restore.intent_ttl` and `core.backup.restore.keep_previous` are deleted (SIMP8-1).
ABO's `backup_*` keys are renamed to these by the R2 config cutover (05-config's rename table; §5B.10). HEAD's
`create_backups` is not a backup key: it is the tag writer's switch for a `.bak-*` copy before a tag write, and
becomes `modules.books.tags.create_backups`. There is no switch to skip
the post-restore credential hook: 05-authn always runs it.

## 5B.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_backup_runs_total` | counter | `reason`, `outcome` |
| `mediacore_backup_duration_seconds` | histogram | `phase` (checkpoint, archive, checksum, verify) |
| `mediacore_backup_bytes` | gauge | — (size of newest archive) |
| `mediacore_backup_last_success_timestamp_seconds` | gauge | — |
| `mediacore_backup_last_verified_timestamp_seconds` | gauge | — |
| `mediacore_backup_archives` | gauge | `format`, `provenance` |
| `mediacore_backup_retained_bytes` | gauge | — |
| `mediacore_backup_verify_total` | counter | `level`, `outcome` |
| `mediacore_backup_restore_total` | counter | `stage` (staged, applied, rejected, swapped_back, reverted), `foreign` |
| `mediacore_backup_revocation_journal_gaps` | gauge | — |
| `mediacore_backup_revocation_reapplied_total` | counter | `kind`, `cause` (boot, restore) |

Names are shown with the default namespace `mediacore`; core takes the namespace as an `app.Options` value
and ABO passes its own (05-metrics), so these series appear under ABO's namespace. HEAD's backup code emits no
series.

Health ([05-health](05-health.md)) turns `last_verified` older than 2 × the verify schedule into
`degraded`, and reports `restore_rejected`, `restore_failed`, `revocation_journal_unwritable`,
`revocation_journal_broken`, `revocation_journal_reset`, and `info` `revocation_journal_colocated` (SEC6-10).

## 5B.9 Tests

| Test | Proves |
|---|---|
| `TestRestoreNeverWritesLiveStore` | the API phase touches only staging and `restore/`; live DB bytes unchanged |
| `TestStagedRestoreAppliedBeforeConfigAndStorage` | pre-boot order; a replaced config file is in effect for the applying boot |
| `TestRestoreCrashAtEveryJournalStep` | each crash point resumes to applied, to swapped-back, or to untouched live data |
| `TestRestoreRejectedKeepsLiveData` | bad staging → boot on live data, health degraded |
| `TestStaleOrForeignIntentRejected` | expired intent and an intent staged by another binary (SIMP8-1); a journal whose in-flight file exists is resumed or swapped back, never rejected (R9-7) |
| `TestInflightBeforeFirstRename` | the in-flight file exists before step c's first rename; a kill between c and g: the in-flight probe refuses `.prev` (`restore_inflight`, 05-storage §5S.3.7), and the next `cur` boot resumes or swaps back (R9-7, DATA9-3; a case of 05-storage's crash matrix) |
| `TestRestoreBootWithoutHooksReachesApplied` (R1) | a staged restore booted by a build without P4d steps 2–3 reaches `applied` through `opened --> marker_written` with `HooksOwed`, writes no `c/backup/` record, and deletes `inflight.json` (DATA9-5) |
| `TestRestoreRejectsMismatchedDBPath` | P0a swaps the recorded absolute targets; storage opening elsewhere swaps back (DATA2-25) |
| `TestUpdaterRollbackRejectsFailingBuildsIntent` | **deferred-build** (A-6): listed in 05-updater's deferred-build table with P−1, which no R0–R3 build runs; once built, P−1 rolls back with a pending intent staged by the failing build and `.prev` rejects it at step a (OPS2-3; COR9-7, DATA9-12) |
| `TestRestoreWithoutLocationFile` (05-storage owns it) | a staged restore with no `backup.location.json` (R1, a host-loss restore) completes; a fault inside step c after the in-flight rename swaps back through step x with only the saved copy restored, and never wedges the journal at `rechecked` (R10-6, DATA10-4) |
| `TestRenamePrecheckRefusesBindMountedConfig` | config not swapped and staged for the operator; DB still restorable |
| `TestRestoreFailureSwapsBack` | guard refusal, failing migration and failing hook each swap back and boot the original data; the next boot reads the original as the same store, runs no hook, no key needs confirmation, the restore line has a `restore_aborted` line and is not resumed, `served_boot`/`backup.location.json` still describe the original, and the committed-head anchor equals the original store's head; with rbac's hook failing after auth's committed, no epoch is bumped, no key needs confirmation and the journal is where it was (DATA6-3, DATA7-5, DATA8-7) |
| `TestRestoreBootFailingAfterHooksIsNotReplaced` | a restore boot that fails at P6, after journal `applied`: the next boot reads the same store, runs no hook and asks no key confirmation (R7-4, DATA7-4) |
| `TestLocationFileWritersSerialized` | `Create` and the anchor's tick, `Append` and shutdown rewrite `backup.location.json` through the journal component's one mutex: no lost update reverts Create's pair (DATA7-14) |
| `TestRestoreCheckpointThenRollback` / `TestCheckpointRestoreBootFailingStepKeepsCheckpoint` | an open checkpoint restored offline is finished to `applied_offline` without hooks and `.prev` then boots; a completed one (`--accept-loss`) is left at `swapped`, and the `.prev` that `make rollback-restore` reinstalls (R1) finishes it with `HooksOwed`; a checkpoint restore boot whose DAG fails keeps the checkpoint, journals `failed` with its binary ID and exits non-zero, the failing build then refuses to boot, the previous binary's `storage restore-offline --checkpoint` (admitted while `inflight.json` exists) finishes it to `applied_offline` and deletes `inflight.json`, and a fixed core build put in place instead resumes it (DATA7-12, DATA8-6) |
| `TestRestoreOfflineBringsBackKeyringAndStateFiles` (with 05-storage) | `storage restore-offline --archive` with the service stopped restores the DB, its sidecars, the config file and boot record and, with `--keyring`, the keyring; `--checkpoint` restores the checkpoint's `.encryption_key`, keyring and gap-fill copies, each matching its recorded SHA-256; a source with hooks due is left at `swapped`; both refuse while an in-flight file exists; a checkpoint that holds no keyring leaves the live keyring byte-identical (DATA-11) |
| `TestRollbackRestoreThenReupgradeKeepsCoverage` | R2 revokes a key, `make rollback-restore` brings back the pre-migration checkpoint and R1 serves it, then R2 is deployed again: the migration runs, the owed hooks run with boundary 0, the key stays revoked, no other key needs confirmation |
| `TestLegacyArchiveRestoreThenMigrate` | a `legacy-abo/0` archive restored on R2 (staged and offline) is migrated by the next boot behind its own pre-migration checkpoint, with the same per-family counts as a live migration of the same store (14 E1); a gap-filled field migrates with the file's value when `--gap-fill` is given, and the restore warns without it (DATA-15) |
| `TestCheckpointEscapeKeepsOwedHooks` | a `KindCheckpoint` restore boot of a served attempt fails its DAG and journals `failed`; the previous binary's `storage restore-offline --checkpoint` finishes it to `applied_offline` with `HooksOwed` and the record's boundary; the next R2 boot reads the store as restored, not replaced, runs the owed hooks and asks no key confirmation (DATA-7) |
| `TestRollbackRestoreRefusesHotfixPrev` | after an R2 hotfix, `.prev` is the earlier R2 build: `make rollback-restore` refuses before touching anything, naming the checkpoint's `from_vector` and the pre-freeze release's tag, and nothing is restored or re-migrated (DATA-9); re-run with `RELEASE=<that tag>` (a fake source serving the pre-freeze binary) it fetches beside `.prev`, finds the levels equal to `from_vector`, installs it as `.prev` and restores; with `RELEASE` naming a release whose levels differ it refuses and keeps `.prev`; an archive cell checks a `mediacore-backup/1` archive against its manifest `Levels` and a `legacy-abo/0` archive against the levels read from its extracted store, with no manifest stamps involved, and its refusal names the archive's producing release (manifest `AppVersion`, or the `legacy-abo/0` archive's version string) |
| `TestStagedLegacyRestoreWithBlockerSwapsBack` | a staged `legacy-abo/0` restore whose archive holds an unconvertible user row (and one with an account merge `applying`) is refused by the preflight on the restore boot: step x swaps back, the report is logged, the journal reads `swapped_back`, no checkpoint, `restore` line or `restore_aborted` is written, `inflight.json` is gone, and the original store boots `degraded` (`restore_failed`, reason `cutover_blocked`) |
| `TestRewriteSwapBackMovesNoCheckpoint` | a `KindRewrite` swap that fails between step c and `applied` leaves every checkpoint directory under `.mediacore-checkpoints/` (none in `failed/`, none `restore_left`, no `delete_after` written), the open cutover checkpoint stays restorable, and a kill after `swapped` resumes forward with M8 deleting the old directory and no second `ErrSwapPending`; a second consecutive swap-back of the same M8 fails the boot with `ErrRewriteSwappedBack` and stages nothing |
| `TestOrphanedOpenCheckpointDeletedAfterSevenDays` (`synctest`, with 05-storage) | checkpoints moved to `restore/<id>/failed/` or marked `restore_left` by a swap-back, and an open checkpoint whose store was then restored from an archive, are deleted 7 days later, with `storage_checkpoint_open` raised until then (CONFORM-2, DATA-8) |
| `TestSwapSlotExclusive` | two concurrent stagers of any kinds (archive, revert, reset), in one process and in two: exactly one creates `restore/pending.json`, the other gets `storage.ErrSwapPending`; staging while a swap is in flight gets it too (R8-1, COR8-6) |
| `TestStagedRestoreKeepsCoverage` / `TestCheckpointRestoreAppendsNoGap` | an archive restore over an intact journal: no `gap` line for the swap, `Revocations` non-nil, only keys revoked after the archive are revoked, no key needs confirmation; the same for a revert and a `storage restore-offline --checkpoint` (R6-4, DATA6-2) |
| `TestRevertLastRestore` / `TestOfflineRevert` | undo point round trip, with and without a booting app |
| `TestRestoreOlderArchiveRunsMigrations` | older versions migrate forward after the swap |
| `TestRestoreRefusesTooNewArchive` | level, Pebble format, Breaking step, wrong app |
| `TestRestoreRewritesSidecarsFromStamps` | `.storage-levels` and `.storage-format` match the restored store right after the swap (DATA4-17) |
| `TestPostRestoreReappliesRevocations` | key revoked after the archive stays revoked; journal gap → keys need confirmation |
| `TestRestoreHooksWriteNativeRows` | hook writes run after the journal re-apply as writes through the subsystems' native stores; after a restart the re-revoked key, a restored session and a restored invite stay refused (R3-1, R4-1, SEC2-3, DATA2-4) |
| `TestCrashBetweenSwapAndHooksResumesHooks` | `CrashClone` at P2, P4a, P4b and each P4d step after step g: the next boot is still a restore boot (`inflight.json`), the hooks run once to completion, the in-flight probe refuses until `applied` (DATA3-6) |
| `TestJournalReapplyVerifiesWithKeyring` / `TestJournalReapplyUsesOverriddenBackupDir` / `TestJournalReapplyWithUISetBackupDir` | with `core.backup.dir` set only in Settings, P4d step 2 reads (or first moves) the journal from that directory and verifies every line with the keyring opened at step 1 (COR3-2, DATA3-5) |
| `TestRestoreBelowUpgradeFloorRefusedAtStaging` | an archive below the build's upgrade floor is refused before extraction; a failed host-loss restore leaves no fresh store (DATA3-9) |
| `TestHostLossRestoreLegacyArchive` | a `legacy-abo/0` archive on a fresh host keeps the install's `install_id`; `KeyIDsReferenced` reports no keyring key; with HEAD's `.encryption_key` copied, M2 decrypts HEAD's four rows, and without it the boot stops before any write naming the file and the override (DATA3-12) |
| `TestRotateDerive_JournalStaysVerifiable` (05-config §2.9 owns it; this file adds the journal-chain cell) | after `rotate-derive`, rewrap and `retire_after`, a key that signs a retained journal line is not retired and the chain still verifies (DATA3-13, SEC3-10) |
| `TestBootReapply_ChainBreakFailsClosed` | a broken chain with the store's head below the journal's end sets every key `needs_confirmation` and bumps every epoch (SEC3-10) |
| `TestReplacedStoreRunsHooks` | an older archive's DB directory copied into place is detected at boot by its content marker or its store generation with journal evidence, also when its head equals the journal's end: tail re-applied, hooks run once with `KindExternal` and `Revocations` nil, keys need confirmation (DATA3-8, DATA4-7, SEC4-6) |
| `TestRelocatedCurrentStoreRunsNoHooks` | the current store moved to another directory or device: new identity recorded, no hooks, no key needs confirmation (DATA5-7) |
| `TestInPlaceRestoreOfLastNightsCoreArchive_Replaced` | `Create` writes P′ after its checkpoint; the archive carries P; an in-place restore of it reads replaced; a crash between the store write and the file write reads the same store (SEC6-6) |
| `TestCommittedHeadAnchorDetectsInPlaceCopy` | a DB directory copied file by file over the live one (same directory identity, same marker) after a later revocation reads replaced because `head_before` is below the anchor; the anchor never leads the store (a revocation whose commit failed does not move it) (DATA6-4) |
| `TestTwoStoresShareStateDir` | two stores with one state directory: neither reads the other's marker, anchor or generation; each boot rewrites the file for its own store and runs no hook (SEC6-8) |
| `TestRevocationStagesJournalHead` | with the identity flip and before the backup flip, a key revoke commits with `c/backup/journal_head` staged through `GetForUpdate`, and an append failure commits with its `journal_gap/` record; the kernel registers `journal.StorageSpec()` and declares `core.backup.dir` (COR5-6) |
| `TestRestore_UnjournaledDemotionHeldByPreSwapStore` | with a pre-swap store, whether coverage is nil or not (SEC6-4), a demotion and a key revocation with no journal line (an append failure) survive a staged restore of an earlier archive (with 05-rbac and 05-authn, SEC5-10) |
| `TestStagedByBinaryID` | an intent staged by one API-only binary is rejected by another binary of the same commit (`StagedByBuild` is the binary ID, FE5-3) |
| `TestConcurrentRevocationsHeadMonotone` (`-race`) | two revocations committing out of append order leave the head at the larger seq (DATA4-7) |
| `TestCrashAfterAppendNoExternalHooks` | a crash between a line's fsync and its commit: the next boot re-applies the line and runs no hooks (DATA4-7) |
| `TestJournalOfOtherInstallNotReplayed` | after a core build's factory reset (05-storage §5S.3.15) or a deleted DB directory, the old install's journal is set aside, a new one starts with a `gap` line, nothing is re-applied (DATA4-15, DATA6-10) |
| `TestRestoreSwapInCoreFromR1` | at R1: a restore whose boot fails at P4b swaps back; a crash after the swap resumes the journal once; `inflight.json` is gone after `applied`; `storage restore-offline --checkpoint` works; ABO's staging takes its floor from books' manifest and creates the intent in the one slot (DATA4-6, COR7-1, SIMP8-4) |
| `TestSwapBackMovesRestoreCheckpoints` | after a failed restore swaps back, no checkpoint the restored store took remains reusable (DATA3-1) |
| `TestRevocationCommittedAfterCheckpointReapplied` | appended before, committed after the checkpoint: re-applied by seq, whatever the clocks say (DATA2-7) |
| `TestRevocationJournal_TruncationDetected` | deleted, edited, reordered or truncated lines break coverage → keys need confirmation (SEC2-9) |
| `TestRevertBoundaryFromStagedRecord` | a revert and a checkpoint restore re-apply the lines after their recorded boundary (DATA2-25) |
| `TestRestore_DemotedAdminStaysDemoted` | `role.unassigned` / `role.narrowed` / `role.deleted` re-applied (with 05-rbac, SEC2-10) |
| `TestRestore_PostArchivePasswordChangeNotReverted` | `password.changed` → reset required; `identity.unlinked` link removed (with 05-authn, SEC2-11) |
| `TestRestoreKeepsAuditTrail` | audit records above the manifest's high-water mark and the restore records survive, no seq collision (with 05-rbac, DATA2-16) |
| `TestForeignArchiveNeedsFlag` | a planted archive (valid format, no valid MAC) is refused without `Foreign` |
| `TestProvenanceBindsInstallAndEntries` | edited entry, swapped entry, other install ID → foreign |
| `TestRewrapBeforeDeriveKeyRetired` | after `rotate-derive`, the verify task re-MACs archives under the `verify` key; a retired key's archive verifies as foreign (SEC2-24) |
| `TestNoKeyMaterialInArchives` | keyring never written; no TLS material or other secret-class item in a plaintext archive |
| `TestPlainArchiveHasNoConfigSecrets` | with `ConfigFileHasSecrets()`, a plaintext archive carries the redacted file and `config_redacted`; `config.yaml` beside the DB is never archived (DATA2-17) |
| `TestUnreadableSecretsRefused` | keyring missing a referenced key → refused unless accepted |
| `TestCatalogueSurvivesRestore` | archives newer than the restored one stay listed and protected |
| `TestCatalogueDropsOrphans` / `TestJournalNamePerApp` | sidecars of an archive deleted outside the service are removed; two apps in one directory keep separate journals (DATA2-27) |
| `TestBackupRoutesPermissions` / `TestBackupDownload_SystemManageOnly403` | the §5B.3.8 table: permission, guards and audit per route; a `core:system.manage` holder without `core:admin` gets 403 on download, and a view-class scope 403 on verify (SEC2-8, SEC3-7, SEC3-12) |
| `TestManifestMatchesEntries` | tampered entry → `ErrManifest` |
| `TestVerifyOpenCountsKeys` | read-only open of the checkpoint matches manifest counts |
| `TestCheckpointCrossDeviceRefused` | no silent copy |
| `TestRetentionFloors` | newest verified; restore-referenced |
| `TestRetentionRunsBeforeSpaceCheck` | stuck-disk regression from 2026-08-29 |
| `TestLegacyABOArchiveRestores` | gzip/zstd/none, with and without sidecar, entry root `audiobooks.pebble/`, foreign confirmation; the restored store is then migrated (B7) |
| `TestDerivedItemsGenerationChecked` | stale bleve/hnsw items marked and rebuilt; an item whose source was touched between the checkpoint and the capture is stale (DATA2-27) |
| `TestHostLossRestore` | backup dir + keyring export on an empty host → own restore of a plaintext archive; journal unanchored → keys need confirmation |
| `TestBackupTaskExclusive` | create vs stage-restore vs prune |
| `TestCreateCancelRemovesPartial` (`synctest`) | cancellation cleanliness |

## 5B.10 Migration from ABO

| ABO code | Becomes |
|---|---|
| `internal/backup/codec.go` (gzip/zstd/none, magic sniffing, level validation) | `core/internal/backuparchive` codecs (the age layer lands with §5B.3.9) |
| `CreateBackupWithCheckpoint`, `ensureSameDevice`, `sweepStaleCheckpoints`, space guard, retention | `backup.Service.Create` / prune internals (rules kept; floors as §5B.3.5) |
| `CreateBackup` live-walk fallback and SQLite branches (`backup.go:283,959,1028,1103`) | deleted: core always checkpoints |
| `RestoreBackupIn` / `extractArchive` | the reader for format `legacy-abo/0`, used only by `StageRestore` and `storage restore-offline --archive` into staging; HEAD's live extraction is deleted |
| `ListBackups`, `newestBackupAge` (`organizer/service.go:567`) | `Service.List` (directory-derived catalogue) and `Ensurer.Ensure` |
| `handlers/system` backup routes (`/api/v1/backup/*`) | core routes `/api/core/v1/backups[/{id}[/verify|/restore]]`; HEAD's paths deleted in R2 with the SPA switched in the same release, so `POST /api/v1/backup/restore`'s extraction into an arbitrary `target_path` is gone (§5B.13) |
| organizer auto-backup | `Deps.Backup.Ensure(ctx, 6h, "pre:books.organize")` |
| config `backup_dir`, `backup_max_total_bytes`, `backup_retention_days`, `backup_compression`, `backup_compression_level` | `core.backup.*` (`backup_retention_days` → `core.backup.retention.max_age`), renamed once by the R2 config cutover (05-config's rename table); `create_backups` is the tag writer's switch and becomes `modules.books.tags.create_backups` |

First run of R2 on an upgraded ABO install: existing `audiobooks_*` archives appear as `legacy-abo/0`
(pre-provenance); the first `core.backup.create` writes a plaintext `mediacore-backup/1` archive under the same
name pattern; nothing is deleted except by retention. R1 ships the journaled swap and ABO's staged restore, so the
restore that rolls back R2 (`make rollback-restore`) exists before R2 does. The owner takes the off-host archive
(with `.encryption_key` and the gap-fill file) before the deploy, and the keyring export after R2's first boot,
when the keyring exists (09's R2 checklist; DATA-12).

## 5B.11 Open items

1. Whether `Import` (uploading an archive over HTTP) needs a size cap and streaming verification
   before it is stored — default cap = `retention.max_total_bytes`.
2. TLS material under `certs/` may be ACME-managed (re-creatable) or operator-provided; class decided
   with [05-http-api](05-http-api.md).
3. Multi-app in one binary: one archive per install (one DB); `Manifest.App` becomes a list then. A
   merge of two installs' stores is not an archive operation (05-storage open item 6, DATA-30). Not
   built until that mode exists (lowest priority).
4. (Closed; SIMP4-5.) Encrypted archives: not built during the extraction (§5B.3.9). They land when an app or
   the owner asks for them. Until then the stated cost is plaintext archives, whose config file is redacted and
   whose DB secrets are ciphertext under the keyring from R2 on (a pre-R2 archive holds HEAD's representation).

## 5B.12 Objections to 03

None.

## 5B.13 Out-of-scope defects found

| Defect | Evidence |
|---|---|
| `POST /api/v1/backup/restore` accepts any absolute `target_path` that is not a "dangerous root" and extracts an archive there, an arbitrary-directory write for any holder of `settings:manage` while no owner is set (`ownerGateWhenOwnerSet`). | `internal/server/handlers/system/handler.go:842-856`; `internal/server/wire_system_routes.go:61-65` |
| Restore `verify` defaults to false (Go zero value of the request field), so the documented fail-closed verification only runs when a client opts in. | `handler.go:826-829,862` |

## Review history

Round-1 findings applied to this file (`docs/plans/platform-core/reviews/spec-round-1-*.md`):

- DATA-21 / OPS-14 (resolution 8) — one format: legacy-layout main archive + companion, restorable by older builds; `ExportPlain` for encrypted archives; stale or foreign intents rejected; rollback and updater refuse while a restore is staged.
- DATA-22 / SIMP-9 — encryption opt-in on upgrade, active only after identity export or a recipient and past the legacy restore bound; floor 2; no keyring in archives; host-loss procedure.
- DATA-23 — pre-boot restore before config; rename pre-validation; config staged for the operator unless replaced; level sidecars rewritten; automatic swap-back; offline revert.
- DATA-24 — catalogue derived from backup-directory sidecars; restore journal outside the DB; audit copy-forward via hooks.
- DATA-25 — generation rule for derived and cache items; stale items never offered.
- DATA-30 — multi-app merge pointed at 05-storage open item 6.
- SEC-16 — manifest HMAC bound to install and entries; foreign restores need flag, `core:admin`, owner proof; out-of-DB revocation journal; `RestoreHook` contract for 05-authn §4.14.
- SEC-17 — restore journal and copy-forward hook context so restore keeps its audit trail.
- SIMP-6 — schedules through `tasks.ScheduleFromConfig`; no bespoke timer.
- COR-1 / COR-21 — `Ensurer` (module handle) and `Runner` (updater) interfaces; artifacts registered through `Registrar.Backup`.
- COR-17 — revocation subjects and the backup resource are `entity.Ref`.
- COR-19 — `backup` is L4.
- MOD-12 — `Manifest.CoreVersion` is `version.Version`.
- DATA-7 / SIMP-18 — compatibility check and sidecar rewrite use the one per-owner level guard.

Round 2:

- R2-1 / DATA2-5 — the Teach build journals its revocations and restores (§5B.3.6 Writers, "What a pre-core writer writes"); every boot re-applies the journal tail the store lacks; builds older than the Teach build cannot open an adopted store. (Teach-build part superseded in round 3 by R3-1: boots without the journal duty are recorded as gaps.)
- R2-2 / DATA2-4 / SEC2-3 — restore hooks run at P4d step 5 after the rebuild and the journal re-apply, write mirrored families only through `Mirror.Stage`, delete legacy `sess:`/`invite:` rows, and are checked by a verification rebuild (§5B.3.4 step h, step x). (Mirror and verification-rebuild part superseded in round 3 by R3-1; hooks now run at P4d step 3.)
- R2-8 — boot steps reference 06 §6.5.1 (P−1, P0a, P2, P4d); §5B.4 rewritten.
- SIMP2-2 — the archive identity is derived from the keyring (`core.backup/age-identity/v1`); identity file, identity export and `identity_not_exported` removed; activation and floor 2 key on the keyring export; host-loss procedure (B3, B4, §5B.3.3, §5B.3.5).
- DATA2-7 — manifest `JournalSeq` and `AuditHighWater` read before the checkpoint; `CreatedAt` informational only; `password.changed` kind (SEC2-11's name for DATA2-7's `credential.changed`).
- DATA2-17 — operator config file archived as class `Secret` when `ConfigFileHasSecrets()`, redacted copy and `config_redacted` in plaintext archives; `config.yaml` beside the DB never archived.
- DATA2-25 — `pending.json` records absolute targets and `db_path` from the `BootView`; P0a trusts them and P2 verifies, mismatch swaps back; revert and checkpoint boundaries from the `staged` record and the checkpoint record.
- DATA2-27 — orphaned companions and sidecars removed at reconciliation; journal named `<prefix>revocations.journal`; derived items stale unless the end-of-capture generation equals the checkpoint's.
- DATA2-28 — a fresh store is stamped at the build's level, so the legacy restore bound does not hold encryption back (§5B.3.3 rule 3).
- DATA2-16 / SEC2-12 — restore context carries `AuditHighWater`; audit's hook advances its `Sequence` (05-storage `Sequence.AdvancePast`) before copying.
- SEC2-8 — Protection column in §5B.7; route table §5B.3.8 (list/verify view, create manage, delete/import/download manage + interactive, restore as specified); classifier shapes.
- SEC2-9 — journal lines hash-chained with a keyring-derived MAC, head `c/backup/journal_head` in every revoking batch, coverage requires an unbroken chain to the pre-swap head; host-loss restores uncovered; chain-preserving prune.
- SEC2-10 — `journal.Revocation` gains `Object` and `Detail`; role kinds `role.unassigned`, `role.narrowed`, `role.deleted`.
- SEC2-11 — kinds `password.changed` and `identity.unlinked` (plus `apikey.disabled`, `user.deleted` from 05-authn).
- SEC2-24 — `Provenance.KeyID`; verification under `DeriveKeyFor`; the verify task rewraps archives under a `verify` derivation key before retirement.
- COR2-6 / SIMP2-1 (R2-7) — `core.backup.schedule` and `core.backup.verify_schedule` typed `tasks.ScheduleConfig` in backup's namespace.
- COR2-8 — no data-dir: restore journal under `<state_dir>/restore/`, backup dir beside the DB (`backup.ResolveDir`), paths from the `BootView`.
- COR2-9 — journal and restore-hook types moved to the leaf package `backup/journal` (lands at S4); hooks are kernel adapters over package functions returning counts.
- COR2-19 — audit actions use 05-rbac §7.1's present-tense names (`core.backup.create`, `core.restore.stage`, `core.restore.apply`, …).
- OPS2-3 — P−1 runs before P0a; an intent staged by the failing build is rejected by `.prev`; a started swap is always resumed or swapped back.
- OPS2-26 — the operator `deploy` recipe refuses while a restore is staged (09 S1).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): B3: books' legacy restore bound is `{Owner: core, Level: 5}`; C8 raises the store to 6 (09 v1.2.0). (Superseded in round 3 by R3-1: bound `{core, 0}`.)
- integration pass (round 2): Schedule keys `core.backup.schedules.create` / `core.backup.schedules.verify` (R2-7 form).
- integration pass (round 2): Rewrap paragraph: 05-config keeps a `verify` derive key while a retained archive records it (`DeriveKeyInUse`).

Round 3:

- R3-1 — one stored format: hook writes and journal re-application are legacy-row writes through the grantees' adapters (no `Mirror.Stage`, no verification rebuild); no Teach-build writer; boots without the journal duty become `gap` lines; legacy restore bound `{core, 0}` (B3, B6, §5B.3.4 h/x, §5B.3.6, Stages paragraph).
- COR3-2 — journal re-apply at P4d step 2 after the keyring and DB overrides (step 1); derive-key retirement moved to P5 through `secrets.RetireUnused` (§5B.3.3, §5B.3.6, §5B.4).
- DATA3-1 — swap-back moves the restore boot's checkpoints into `restore/<id>/failed/` or marks them `restore_left` (§5B.3.4 step x).
- DATA3-5 — `core.backup.dir` restart-bound; journal location record `<state_dir>/backup.location.json`; the journal is moved and verified before re-apply when the directory changes (§5B.3.6, §5B.7).
- DATA3-6 — `pending.json` renamed to `restore/<id>/inflight.json` at step g and kept until `applied`/`swapped_back`; P0a resumes in-flight journals; hooks idempotent; rollback tooling refuses meanwhile (§5B.3.4).
- DATA3-7 — Stages paragraph: ABO's swap through `app.Options.PreBootRestore` from S2b, hooks from S4, this package from S8.
- DATA3-8 — replaced-store detection (head below the journal's end without a P0a restore) runs the hooks with `KindExternal`; unacknowledged `restore` lines resumed (§5B.3.6); the Teach-build part is superseded by R3-1.
- DATA3-9 — staging step 4 checks every `UpgradeFloor`; host-loss procedure names the install-first route; a failed restore with no previous store leaves no fresh store (§5B.3.3, §5B.3.4).
- DATA3-12 — `pending.json` carries `install_id` for a marker-less restored store (from the journal header or keyring export on a fresh host); `KeyIDsReferenced` takes the staged reader and reports `k0-legacy` (§5B.3.4 steps 5, 8; §5B.3.7).
- DATA3-13 — derive keys referenced by retained journal lines stay in the in-use set (§5B.3.3, §5B.3.6 Retention).
- DATA3-19 — the journal starts with the S4 build; no S0 hotfix writes it (§5B.3.6 Writers).
- SEC3-3 — superseded by R3-1 (no Teach build or floor to restore past); its residual — an older build's in-place restore after a rollback — is covered by the replaced-store rule.
- SEC3-7 — download requires `core:admin` + `Guards.Interactive` + `Guards.Owner`; B1 says an archive is credential-equivalent (§5B.3.8).
- SEC3-10 — journal chain stays verifiable across key retirement; a chain break with the head below the journal's end fails closed (§5B.3.6).
- SEC3-12 — `POST /backups/{id}/verify` requires `core:system.manage` (§5B.3.8).
- OPS3-1 — P0a resolves the state directory by 05-config's pre-boot rule with HEAD's fallback next to the database (R3-4) (§5B.3.4).
- OPS3-13 — step 9 names the ssh probes of both restore files; a probe that cannot run refuses (§5B.3.4).

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): B3 rule 3 and open item 4 (closed): ABO archives stay unencrypted for the whole extraction; there is no dedicated encryption level step, the first cutover passes the bound (09 §9.13).

Round 4:

- R4-1 — hook writes and journal re-application go through the subsystems' stores (books' legacy implementations for ABO); "storage grants" removed from the terms; `RestoreContext.Previous` limited to the caller's own keyspace (Stages paragraph, "One stored format", §5B.3.4 h, §5B.3.6).
- COR4-4 — the `journal` duty is listed only from S4 r2; S4 r1's interval becomes a `gap` line; `TestDarkReleaseRecordsJournalGap`.
- COR4-10 — `core.backup.dir` declared from S4 by `backup/journal` (Stages, §5B.3.6, §5B.4, §5B.7).
- DATA4-6 — the journaled swap (P0a, step x, step h bookkeeping, `KindCheckpoint`) is core code from S2b; ABO only stages, with the kernel's floors; `PreBootRestore` gone; `TestRestoreSwapInCoreFromS2b`.
- DATA4-7 — `c/backup/journal_head` `Serialized` and monotone (`max` via `GetForUpdate`); replaced store decided by store generation recorded in `backup.location.json`; a lagging head on the same store re-applies without hooks; three tests.
- DATA4-15 — the journal header's `install_id` is checked first; another install's journal is set aside and not replayed.
- DATA4-17 / OPS4-15 — step c moves and step e rewrites all three sidecars, `.storage-applied` included.
- DATA4-22 — staging trial-decrypts one legacy secret copy per name with `k0-legacy` through 05-config's `LegacyStore.Check` (the app's code; core holds no row name).
- SEC4-5 — `head_before` read before any append; gap and `restore` lines appended after the re-apply.
- SEC4-6 — a replaced store's hooks get `Revocations` nil while the verified tail is still re-applied (stated in row "Boot re-apply").
- SIMP4-5 / SIMP4-1 — archive encryption moved to `### 5B.3.9 Deferred-build members` (B3, B4, §5B.3.1, §5B.3.2, §5B.3.3, §5B.3.5, §5B.5, §5B.7–§5B.9, §5B.11 item 4); S8 builds plaintext archives with provenance.
- OPS4-3 — step 9 refers to the one restore probe of 05-storage §5S.3.7.

Round 5:

- COR5-6 — `backup/journal` exports `journal.StorageSpec()` (`c/backup/`, `Serialized` `journal_head` and `served_boot`, prefixes `journal_gap/`, `restore_hooked/`), registered by the kernel from S4; `core.backup.dir` declared in the kernel's core-section list from S4; `backup` adopts both at S8; `TestRevocationStagesJournalHeadAtS4`.
- COR5-3 / DATA5-6 / R5-5 — "Boots without the journal duty": the served ledger's previous record survives a failed boot (06 Run step (3b)); the duty is owed; the `gap` line is appended unless one already covers `since`, and its batch clears the owed entry; `TestFailedBootKeepsGapOnce`.
- DATA5-7 — classifier: a new generation is "replaced" only with journal evidence; otherwise "relocated" (identity recorded, no hooks); error row and tests.
- SEC5-6 — content marker `c/backup/served_boot` mirrored in `backup.location.json`; an older pair means replaced whatever the directory identity; residual (archive taken by the older build after the last journaling boot) stated; `TestOlderBuildInPlaceRestore_SameDirDetected` (its R-test cell reworded to an archive of the current build: the reviewer's "back up on prev, revoke, restore" case is undetectable by any marker).
- SEC5-5 — the gap detection cites the full-stat foreign-open rule and the boot lineage.
- DATA5-13 — install check: an older build's in-place restore of a pre-S0 archive inherits the `install_id`, so the tail is re-applied before the hooks.
- SEC5-10 — "Coverage at restore": with nil coverage and a pre-swap store, hooks keep the most restrictive state, through the owner's store built read-only over the pre-swap store (`RestoreContext.Previous` comment); test.
- DATA5-9 — step g: steps an older archive lacks re-run after the swap, one-shot steps included.
- DATA5-19 — B1: an older build restoring an S3+/S4+ archive pays rows 1–5's rollback costs.
- FE5-3 / R5-7 — `StagedByBuild` and `Revocation.Source` hold the binary ID; `TestStagedByBinaryID`.
- COR5-16 / SIMP5-6 — §5B.3.9 table: package-qualified identifiers only in the first column; the re-encryption behaviour of `Rewrap` and the non-identifier items moved to "Also deferred (not checked)".
- integration pass (round 5): history completed for the R-D requests already in the body — `RestoreContext.Previous` hands auth a read-only `auth.Store` as `auth.RestoreDeps.Previous` and rbac a read-only `rbac.Store` (SEC5-10, built by the kernel at 06 P4d step 3); the `c/backup/served_boot` content marker (SEC5-6, row "Content marker") is the replaced-store signal 14 K22 cites.

Round 6:

- R6-4 / DATA6-2 — step e records the `.storage-format` stat identity and the staged directory's highest `OPTIONS-<n>` (journal step `sidecars`, `JournalRecord.Sidecar`/`Options`); a restore boot is no foreign open, owes no duty for its own swap and appends no `non_journaling_build` gap; `TestStagedRestoreKeepsCoverage`, `TestCheckpointRestoreAppendsNoGap`.
- R6-7 / OPS6-4 — §5B.3.3: a production archive restored on a staging host is made inert offline before its first boot by 09's procedure (all secrets, sessions, invites, tokens, integration URLs, updater and schedules); no "staging mode".
- COR6-2 — the gap consumer reads `storage.Duties.Owed(DutyJournal)` and clears it with `StageDone` in the gap line's batch; owed journal duty counts as classifier evidence.
- COR6-6 — Stages and row "Content marker": every write of `served_boot` and `journal_head` goes through `GetForUpdate`, the reason `served_boot` is `Serialized`.
- DATA6-3 — on a restore boot `served_boot` and `backup.location.json` are written only after the hooks commit (journal `marker_written`); step x appends `restore_aborted` (new `journal.Kind`); an aborted or swapped-back restore line is neither resumed nor evidence; error row; `TestRestoreFailureSwapsBack` extended.
- DATA6-4 / SEC6-6 — `Create` reads the pair into `Manifest.ServedBoot` and writes a fresh pair to the store and the file after its checkpoint; `backup.location.json` gains the committed-head anchor `head` (never ahead); classifier (2) gains (a) older marker, (b) head below the anchor, (c) behind lineage, (d) generation with journal evidence including this boot's owed journal duty; residual narrowed to archives the older build itself made; `TestOlderBuildInPlaceRestore_SameDirDetected` restated, `TestInPlaceRestoreOfLastNightsCoreArchive_Replaced`, `TestCommittedHeadAnchorDetectsInPlaceCopy`.
- DATA6-5 — classifier (2) lists the lineage verdict 05-storage feeds it (never on a restore boot).
- DATA6-6 — the journaled swap also performs 05-storage's factory reset set-aside; the other-install row names a core build's reset.
- DATA6-10 — the other-install row and `TestJournalOfOtherInstallNotReplayed` narrowed to a core build's reset or a deleted DB directory; residual row for an older build's reset (05-storage §5S.11 item 8).
- SEC6-4 — "pre-swap restrictions win" whenever `Previous` is non-nil, not only with nil coverage; test row updated.
- SEC6-7 — the cut-off evidence is a `served_boot` pair for this store in `backup.location.json` (journaling builds only).
- SEC6-8 — `backup.location.json` records `db_path` and `install_id`; marker, anchor and generation are compared only for the store the file describes; `TestTwoStoresShareStateDir`.
- SEC6-10 — whole-volume snapshot residual stated in row "Where"; health `info` `revocation_journal_colocated`; runbook advice.
- SIMP6-8 — checked: the §5B.3.9 rows' first cell is exactly `—`, which 03's amended grammar accepts; no change.

Round 7:

- R7-4 / DATA7-4 — step h: a restore boot writes `c/storage/boot` with `c/backup/served_boot` in its `marker_written` batch, then `storage.boot.json`, before journal `applied`; `TestRestoreBootFailingAfterHooksIsNotReplaced`.
- R7-4 / DATA7-5 — row "Head in the store": the committed-head anchor is frozen while a restore journal is between `swapped` and `applied` and on a restore boot; step c saves `backup.location.json` into `restore/<id>/` and step x restores it; `TestRestoreFailureSwapsBack` extended.
- R7-4 / DATA7-10 — row "Content marker": the "user rows but no cut-off" signal deleted ((2)(a) covers its case); (2) stated as the one classifier.
- R7-4 / DATA7-12 — "Checkpoint restore" paragraph: an open checkpoint is finished to `applied` offline without hooks (`applied_offline`), `CheckReader` passes a `KindCheckpoint` journal at `swapped` for an admitting candidate, and a failing checkpoint restore boot does not swap back (step 9, step x, error row, two tests).
- R7-4 / COR7-8 — `RestoreContext.Previous`: the pre-swap store is opened with `storage.Admin.OpenReadOnly` and the auth and rbac constructors are called once more over read-only `legacy.Handles` (`storage.ErrReadOnly`).
- R7-4 / COR7-10 — `JournalRecord` and `SidecarIdentity` are aliases of `storage/restorejournal` types; `restoreswap` is L1; the kernel passes the recorded identity to storage's open (Stages, Go block).
- R7-2 / COR7-1 / COR7-9 / DATA7-17 / OPS7-11 / SEC7-5 — ABO's staging takes its floors from `legacy.Maintenance.UpgradeFloors()`; step 1 refuses while a factory reset is pending or in flight (`ErrResetPending`); step 9 names `reset_pending`/`reset_inflight` and `ROLLBACK_IGNORE_RESET`; P0a precedence (the reset supersedes); the factory-reset paragraph (open-stamp rejection, `inflight.json`, aux DBs, post-reset hook not undoable); `TestRestoreAndResetBothStaged`.
- R7-6 / OPS7-2 / OPS7-3 / OPS7-4 / OPS7-7 / SEC7-6 / SEC7-10 — the staging paragraph cites 09 §9.3 only and restates none of its steps.
- DATA7-14 — `backup.location.json` has one writer in a running process (the journal component, one mutex); `Create` hands its pair to it; `TestLocationFileWritersSerialized`.
- DATA7-16 — Stages and B7: archives are ABO's `legacy-abo/0` until S8, so restores in S2b–S7 are foreign with nil coverage (every API key re-confirmed); `TestOlderBuildInPlaceRestore_SameDirDetected` reworded to "the head is below the anchor (from S8 also the marker)".
- integration pass (round 7): step g and step x: a `KindCheckpoint` swap's in-flight file is `restore/<id>/inflight-checkpoint.json`; the reset set-aside keeps `reset/<id>/inflight.json` until `hook_done` (`reset_pending`/`reset_inflight`); B-staging cites 09 §9.3's five-step procedure (R-H request); `TestRotateDerive_JournalStaysVerifiable` is 05-config's name.

Round 8:

- R8-1 / SIMP8-1 — one slot for every kind (step 1, step 8's atomic `link(2)` create, `storage.ErrSwapPending`), one rejection rule with `pebble_options` (step a), one token pair and one override, pending only (step 9); the precedence paragraph, error row and `TestRestoreAndResetBothStaged` replaced by "one slot" and `TestSwapSlotExclusive`; `ErrResetPending` deleted, `ErrRestorePending` an alias of `storage.ErrSwapPending`; the factory-reset paragraph states `KindReset`; `DEPLOY_DISCARD_RESTORE` deleted; `core.backup.restore.intent_ttl`/`keep_previous` replaced by `core.storage.swap.*`.
- COR8-5 / COR8-6 — Stages: `restoreswap` is kernel-held and imports `storage`, never the reverse; `storage.ErrSwapPending` and `storage.ErrRenameRefused` are storage's (step 6, error row).
- SIMP8-4 — ABO's pre-S8 staging takes books' manifest floor, not `legacy.Maintenance.UpgradeFloors()`; `TestRestoreSwapInCoreFromS2b`.
- SIMP8-8 — `JournalRecord`/`SidecarIdentity` aliases deleted; `RestoreContext.Journal` is `[]restorejournal.Record`.
- R8-3 / OPS8-2 / SIMP8-2 / SEC8-11 — the staging paragraph: never the staged restore; `staging-restore` and `staging-scrub` with the archive's release binary; cites 09 §9.3.
- OPS8-7 — `ROLLBACK_IGNORE_RESTORE=1` overrides `restore_pending` only.
- OPS8-12 — `Restart` requests `Lifecycle.RequestRestart` (restart exit code).
- DATA8-5 — "Checkpoint restore" rule (1): the offline close batch with the step-e identities before `applied_offline`; `TestOpenCheckpointRestoreThenRollbackIsNotForeign`.
- DATA8-6 — step 9, P0a, step x's exception, error row, rule (3): a `KindCheckpoint` journal at `swapped` or `failed` admits only a candidate with the journaled swap (`--print-restore-journal`), not the failed binary (`Build`), and that candidate resumes it; test row extended.
- DATA8-7 — `Revocation.RestoreSeq` (`restore_seq`) on hook lines, never re-applied after an aborted restore (§5B.3.6 (3), step x; R-D request); the journal is not moved on a restore boot (row "Where"); step c saves and step x restores `storage.boot.json`; `TestRestoreFailureSwapsBack` and the swap-back error row extended.
- integration pass (round 8): sentinel comment and step 9 no longer name the deleted reset sentinel and deploy override (sweep (a)); the S8 updater check has no fake-server text here (SIMP8-10, already applied); `ErrRestorePending` is the alias of `storage.ErrSwapPending` and the journal is not moved before `applied` on a restore boot (DATA8-7), both already applied.

Advisor (`docs/plans/platform-core/reviews/spec-advisor-dispositions.md`, 2026-11-04):

- A-2 / ADV-3 — no extension record or sentinel handling: header, "One stored format", B1 (an older build restores any extraction archive with no representation cost), B3 and the key-material bullet (a legacy-backed store's secrets stay in HEAD's representation), step 5 (no sentinelled copy; the legacy key covers HEAD's rows), step h (HEAD's session, invite and token rows deleted; nil coverage sets a legacy key to HEAD's `inactive` status, R-A request option (b)), open item 4.
- A-10 / A-12 / ADV-6 / ADV-S6 — staging paragraph: the staging marker and `--confirm-destroy-credentials`; staging is the offline tools plus a `--network none` container; no ssh-reachable host, jump container or deploy-user rehearsal.
- A-17 — the reset hook's arguments carry `RootDir` only when `delete_library_files` is set (R-G request).
- A-4 / ADV-S3 — §5B.3.9 no longer cites `archtest.DeferredMembersAbsent`.
- A-5 / ADV-S4 — §5B.8 namespace note.
- integration pass (advisor): a staged restore of an ABO archive before S8 sets legacy keys `inactive` and deletes sessions (05-authn §4.14 choice (b)); stated as a restore cost, no longer as a 09 §9.2.1 row; `auth` and `usage` duties confirmed absent.

Round 9 (`docs/plans/platform-core/reviews/spec-round-9-*.md`, dispositions binding):

- DATA9-3 / DATA9-5 / COR9-5 (R9-7) — step b renames `pending.json` to the in-flight file before step c's first rename; step g only journals `swapped`; step 9 and P0a cite the new timing; step h: a build without P4d steps 2–3 reaches `applied` through `opened --> marker_written`, writing only `c/storage/boot` and `storage.boot.json`; tests `TestInflightBeforeFirstRename`, `TestRestoreBootWithoutHooksReachesApplied`.
- DATA9-4 (R9-6) — step a reads only `pending.json`; an accepted swap is never rejected; a reset resumes forward and always moves back before `rejected` (factory-reset paragraph).
- R9-6 — the native route's wipe only after the confirmed preflight (`storage.ResetRequest`).
- R9-9 / OPS8-8 — `Manifest.BinaryID`; from S0 ABO's `internal/backup` writes a `legacy-abo/0` manifest sidecar with `binary_id` (B7); `staging-restore` refuses another binary; `TestStagingRestoreRefusesOtherBinary`.
- OPS9-9 / DATA9-14 (lead decision over SEC9-12) — a pre-S0 archive cannot be staged; the operator takes a fresh archive on a release ≥ S0.
- COR9-6 — the staging marker is written through `app.Offline.WriteStagingMarker`.
- COR9-7 / DATA9-12 — P−1 marked deferred-build in B2, the P0a paragraph and §5B.4; `TestUpdaterRollbackRejectsFailingBuildsIntent` points at 05-updater's deferred-build table.
- COR9-13 — step 8 and "Stages": both stagers create the intent through `restorejournal.CreateIntent` with `restorejournal.Intent` (`id` added to the field list).
- COR9-15 — the open-checkpoint finish commits through `kernel.offlineCloseBatch`, not the public `OpenOffline`.
- DATA9-15 / OPS9-5 — "Stages": the key and session cost of a staged restore applies from S4 r2 to S7 (S2b–S4 r1 keep the archived key status, HEAD's behaviour); 09's S4 r2 release note carries it.
- integration pass (round 9): "Stages": the S4 r2 key and session restore cost is 09 §9.2.1 row 11 (09 v1.10.0), no longer "no row".

Round 10 (`docs/plans/platform-core/reviews/spec-round-10-*.md`, dispositions binding):

- DATA10-4 (R10-6) — step c copies `backup.location.json` and `storage.boot.json` only if present; step x covers failures inside step c after the in-flight rename and restores only the saved copies; step a: a reset reaches `rejected` before `set_aside_started` with nothing moved; `TestRestoreWithoutLocationFileS2b`.
- DATA10-5 (R10-11) — §5B.3.3 staging: `staging-restore` records the writer's `binary_id` in the staging marker, `staging-scrub` reads and checks it; test row extended.
- DATA10-7 — §5B.3.9 rule 3 and open item 4: the bound is passed by the first cutover that raises `core`, not the first per-family cutover.
- DATA10-8 — "Stages": the S1–S2 r3 ABO-local swap follows §5B.3.4 exactly (journal shape, in-flight timing, `applied` with no lineage write); S2b's P0a resumes it (`TestS2bResumesS1Journal`).
- OPS10-6 (R10-8) — `Runner` comment and pre-operation backups: the updater's pre-update backup is a deferred-build member of 05-updater §5U.3.
- OPS10-9 — B7: from S0 `internal/backup` deletes the manifest sidecar with the `.sha256`; staging copies archive and sidecar together, and an archive without its sidecar is refused.
- OPS10-10 — B7: a pre-S8 archive stays `legacy-abo/0` with nil coverage when restored on S8 or later.

- D10–D12 revision (2026-11-04): "Stages" became "Releases" (journaled swap in R1, journal and `backup` in R2,
  no dark-release gap). B1/§5B.3.3: one archive file in core's own layout (the legacy-layout main archive, the
  companion `.extra` and every older-build listing and restore rule deleted); old archives restore, then
  migrate (B1, B7, step g). B3: the rollback rationale for deferring encryption and the legacy restore bound
  deleted. B6 and §5B.3.6: the served-ledger `journal` duty, `non_journaling_build` gaps, boot lineage and
  older-build in-place restore cases deleted; new row "Restores without hooks" (R1 and offline restores journal
  `HooksOwed`; the next journaling boot runs the hooks with the source's boundary). §5B.3.3: the staging
  procedure (`staging-restore`, `staging-scrub`, staging marker, `binary_id` sidecar and manifest field)
  replaced by the offline restore as the rollback path (service stopped; DB, sidecars, state files and keyring
  restored as one unit) and the optional `--network none` rehearsal. §5B.3.4: `pebble_options`,
  `storage.boot.json`, the `.storage-applied` sidecar, foreign-open identities, `ROLLBACK_IGNORE_RESTORE` and
  `LegacyStore.Check` removed; checkpoint-restore rules rewritten for `make rollback-restore`. Routes and
  config keys renamed in R2 with no aliases. Tests: 15 older-build, staging and dark-release tests deleted;
  `TestRestoreOfflineBringsBackKeyringAndStateFiles`, `TestRollbackRestoreThenReupgradeKeepsCoverage` and
  `TestLegacyArchiveRestoreThenMigrate` added. Consistency pass (lead decisions L9, L11, L15): `CheckReader`, `inflight-checkpoint.json`, `--print-restore-journal`, the `KindCheckpoint` probe exception, `Record.Build`, `ErrRestorePending` and `k0-legacy` are gone (a `failed` checkpoint journal is resumed by a binary the operator puts in place, a `legacy-abo/0` archive's rows are decrypted by M2 with `.encryption_key`), step 9 follows 05-storage's in-flight-only probe, `secrets.Open` is cited at 05-storage §5S.4 step 8, `create_backups` maps to `modules.books.tags.create_backups`, offline revoking writes are journaled (`TestOfflineRevocationJournaled`), and D12's answers are cited as D12 (a)–(d). Final consistency sweep: the previous binary's `storage restore-offline --checkpoint` is the one recipe admitted while a `failed` checkpoint journal holds `inflight.json` (steps 9 and x, rule (3), test row), and `secrets.Open` is placed at the start of P4b.

Round 13 (2026-11-04): CONFORM-2, CONFORM-6, DATA-6, DATA-7, DATA-8, DATA-9, DATA-11, DATA-12, DATA-15, SEC-10, IMPL-1, SEC-5 (M8's `KindRewrite` accepted as a swap kind: its rules and failure row), SEC-9 (the sweeper's `core.storage.checkpoint_deleted` audit and `storage_checkpoint_mode`) (lead rulings on checkpoint retention, the `make rollback-restore` level check and F2's core sections).

Round 14 (2026-11-05): V2 (a cutover preflight refusal on a restore boot is a step-x failure: swap-back, journal `swapped_back`, health `restore_failed` reason `cutover_blocked`; `TestStagedLegacyRestoreWithBlockerSwapsBack`), V3 (`KindRewrite` rule 5: M8 reads its phase from the journal), V4 (an archive's levels come from the manifest `Levels` or, for `legacy-abo/0`, from the extracted store), V8 (a `KindRewrite` swap-back moves no checkpoint), V9 (a factory reset sets aside no checkpoint), V10 (`make rollback-restore RELEASE=<tag>` and the fetch it cites in 05-updater), V23 (no bare round-13 ruling citation was present; the `(R4)` in the header is 03's layer rule).

Round 15 (2026-11-05): lead fix (W8: `from_release` is the embedded pre-freeze tag), W1 (`KindRewrite` rule (5) and the failure and test rows: M8 reads only journals with its own `StepID`; one swap-back restarts it once, a second consecutive one fails the boot with `storage.ErrRewriteSwappedBack`), W8 (the refusal names the checkpoint record's `from_release`, or on the archive branch the archive's producing release), W10 (the fetch has apply's trust level, not a signed release), W11 (`--accept-loss` on both forms of the rollback path's summary).

Round 16 (2026-11-05): review fixes X4 (§5B.3.4: the floor is `InstallFirst: <R2 tag>`; the refusal names the R2 release by its tag).
