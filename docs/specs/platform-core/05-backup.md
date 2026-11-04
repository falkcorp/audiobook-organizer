<!-- file: docs/specs/platform-core/05-backup.md -->
<!-- version: 1.8.0 -->
<!-- guid: 604febb0-a09e-4cf9-99ee-5f3722534da6 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Backup and restore (`backup`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) §5 (backup and restore) and §1
(engines), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7 (secret storage).
Storage terms (`storage.Admin`, keyspaces, levels, sidecars, checkpoints, app-supplied legacy stores
and extension records, the store generation, derived indexes and their generations, auxiliary DBs) are defined in [05-storage](05-storage.md); the secrets keyring, its derivation key and
the boot record in [05-config](05-config.md); the post-restore credential hook in
[05-authn](05-authn.md) §4.14; role re-application and audit copy-forward in [05-rbac](05-rbac.md) §7.1.
`backup` is **L4** (R4) and lands at 09 S8. The revocation-journal and restore-hook types live in the
leaf package `backup/journal` (L4; imports `entity` and `storage` only), which lands at S4 with `auth`
and `rbac`, its first writers (COR2-9); it exports the `c/backup/` `storage.Spec` the kernel registers
from S4 (COR5-6). Boot order is [06](06-app-sdk.md) §6.5.1's one phase table
(R2-8): this file's pre-boot swap is P0a, its journal re-apply and restore hooks are P4d steps 2 and 3,
after step 1 has opened the keyring and attached the DB overrides (COR3-2). This file owns the **one
archive format** (cross-cutting resolution 8); [09](09-abo-on-core.md) S8 references it and decides only
when each step ships.

**Stages (DATA3-7, DATA4-6, COR4-10).** `backup` lands at S8; three pieces land earlier.
- **The journaled swap** (the one intent slot, P0a's swap and resumption, step x's swap-back, step h's
  bookkeeping up to `applied` and the removal of `inflight.json`, the `KindCheckpoint` swap of 05-storage's
  `storage restore-checkpoint`, and the `KindReset` swap of 05-storage's factory reset, §5S.3.15) is core
  code from **S2b**, `core/internal/restoreswap`, held and run by the kernel at P0a (06) and from its reset
  staging. `restoreswap` is **L1** and imports `storage` and its L0 leaves; `storage` never imports it, and
  `storage.Admin` has no swap or staging method, so there is no cycle (COR8-5). The swap journal's record
  types (`restorejournal.Record`, `restorejournal.SidecarIdentity` and the step names) live in the L0 leaf
  `storage/restorejournal` (05-storage §5S.3.15), which this package uses directly under those names
  (SIMP8-8), and the kernel passes the identity recorded at step e to storage's open, so storage never reads
  the journal file and never reaches an L4 type (COR7-10). The slot's sentinel `storage.ErrSwapPending` and
  the pre-check's `storage.ErrRenameRefused` are `storage`'s, so the S2b code and ABO's staging return them
  long before this package exists (COR8-6). From S2b to S7 ABO's own `internal/backup` only
  **stages** a restore (steps 1–9 below, creating `pending.json` in the one slot); it takes the build's
  upgrade floor for step 4 from books' own manifest (`Manifest.Storage.UpgradeFloor`, the floor books
  declares), so it needs no kernel handle (SIMP8-4). Until S8 every archive is ABO's
  (`legacy-abo/0`, B7), so every staged restore in S2b–S7 is a foreign restore with nil coverage: every API
  key needs re-confirmation after it, a cost 09 §9.2.1 lists (DATA7-16). So a boot that fails after the swap swaps back, a crash between swap and hooks resumes
  them once, `inflight.json` is gone after the hooks, and a failed migration has its checkpoint
  restore, from the first release on core storage. ABO's former `app.Options.PreBootRestore` does not
  exist.
- **The journal** (`backup/journal`) lands at S4 with `auth` and `rbac`, its first writers. From S4 the
  kernel registers two things for it, so neither waits for `backup` (COR5-6): core keyspace `c/backup/`
  (owner `backup`, already in the fixed core order, 05-storage §5S.3.5) through `journal.StorageSpec()` —
  `Serialized: ["journal_head", "served_boot"]` plus the `journal_gap/` and `restore_hooked/` prefixes (every
  write to either key goes through `GetForUpdate`, R6-1; that is the only reason `served_boot` is `Serialized`,
  COR6-6) — so
  `Append`'s `GetForUpdate` and `Gap`'s record commit in the revoking batch from S4 r2 instead of failing
  with `ErrNotSerialized` or `ErrOutsideKeyspace`; and the restart-bound key `core.backup.dir` (alias
  `backup_dir`, `ProtectAdmin`, default §5B.7), declared in the kernel's **core-section list**, so
  `backup/journal` keeps importing only `entity` and `storage` and P4d step 2 can locate the journal
  under the admin's directory before `backup` exists. `backup` adopts both at S8
  (`TestRevocationStagesJournalHeadAtS4`). P4d step 2 re-applies the journal and step 3 runs the hooks on
  every boot that applied a restore or finds a replaced store (§5B.3.6).
- **The `journal` duty** (05-storage §5S.3.7) is listed only by builds in which every revocation path
  is core's: from S4 r2, the release in which core auth decides requests. S4 r1 (dark: gin's handlers
  still revoke, without journaling) does not list it, so the next journaling boot records its interval
  as a `gap` line (COR4-4).

From S8 the steps below are this package's.

**One stored format (R3-1, R4-1).** There are no mirrors, side records or floor builds: every family
keeps the format HEAD writes (05-storage §5S.3.11), so a revocation, a hook write and a journal
re-apply are each an ordinary write through the owning subsystem's store — for ABO, books' legacy
implementation, which writes the legacy row every build reads (plus, where needed, an extension record
older builds never read) — and every earlier release stays a rollback target. This file's journal
therefore assumes nothing about which builds served the store in between; it detects boots that did not
journal and fails safe (§5B.3.6).

## 5B.0 Decisions in one table

| # | Decision |
|---|---|
| B1 | **One archive format, `mediacore-backup/1`.** A **main archive** in ABO's legacy layout — the Pebble checkpoint rooted at the DB directory's basename, plus one root entry `mediacore-manifest.json` — that every ABO build, pre-core ones included, can list and restore; and a **companion archive** (`.extra`) for everything else (config file, boot record, module artifacts, optional derived and re-creatable stores), which older builds never list. **No archive ever contains the secrets keyring**, and an unencrypted archive never contains a secret: the operator config file goes in as class `Secret` when it holds one, and only a redacted copy goes into a plaintext archive (DATA2-17). A main archive is **credential-equivalent**: it holds every password hash, OIDC identity link, the invite allow-list, the audit trail and the secret ciphertexts, so downloading one is gated like a restore (§5B.3.8, SEC3-7). An older build restores an archive written on S3+ (S4+) with the costs of a rollback (09 §9.2.1 rows 1–5): the moved secrets are sentinels, native keys and sessions are void, namespaced role scopes are absent; restore it with a build at or after the archive's stage to avoid them (DATA5-19). |
| B2 | **Restore is staged and applied in the kernel pre-boot phase** (06 P0a), after the updater's own pre-boot check (P−1) and before config is read and storage opens. The API verifies, pre-validates every rename and extracts into a staging directory on the DB's filesystem; the kernel swaps by rename using the absolute targets recorded at staging and keeps the previous data as an undo point. **Any failure after the swap swaps back automatically.** No code path writes into a live store. |
| B3 | **Archive encryption is a deferred-build member** (SIMP4-5): during the extraction it could never become active for ABO — every earlier release, which has only the plaintext restore code, can still open the store (R3-1) — and hello is not a backup product, so S8 builds **plaintext** archives with B5 provenance only. Its contract (age, an archive identity derived from the one keyring, activation rules, operator recipients, the legacy restore bound, the plaintext-floor rule) is kept at proof depth in §5B.3.9 and lands with the first app or cutover that can activate it. Secrets inside the DB are ciphertext either way, and a plaintext archive never carries a secret-class item (the config file goes in redacted). |
| B4 | Verification is part of creation (per-entry SHA-256, open the checkpoint read-only, compare key counts) and runs again on a schedule. Retention never deletes the newest **verified** archive. |
| B5 | **Provenance.** The manifest carries an HMAC bound to the install (keyring-derived key named by its key ID, install ID in the MAC'd bytes). An archive without a valid MAC — imported, planted, from another install, or from before provenance existed — is **foreign**: restoring it needs an explicit `Foreign` request, `core:admin` and an interactive owner proof, and is audited. |
| B6 | **What must outlive a restore lives outside the restored DB**: the archive catalogue is derived from sidecar files in the backup directory; the restore journal and the **revocation journal** are files. The revocation journal is hash-chained with a keyring-derived MAC and anchored by a head record inside the store (SEC2-9); every build from S4 journals its revocations, and a boot by a build that does not (a rollback target older than S4, a pre-guard build) is recorded as a coverage gap by the next journaling boot (R3-1); every boot re-applies the journal tail the store lacks, and a store replaced outside the staged restore gets the post-restore hooks too (DATA3-8). |
| B7 | Old ABO archives (`audiobooks_<dbtype>_<ts>.tar[.gz|.zst]` + `.sha256`) stay listable, verifiable and restorable through the same staged path as format `legacy-abo/0` (pre-provenance, so restoring one is a foreign restore with nil coverage: every API key needs re-confirmation). Every archive made before S8, when ABO's `internal/backup` still creates them, is in this format, also those taken from S4 r2 on (DATA7-16). |
| B8 | Create/verify/prune/stage-restore are core **tasks** (`core.backup.*`) with progress from real work, cancellation, one exclusive `Resource`, and schedules declared under backup's own namespace with type `tasks.ScheduleConfig` and bound through `tasks.ScheduleFromConfig` (SIMP-6, R2-7). |

## 5B.1 Purpose & scope

In scope: what is backed up, the archive format, encryption (a deferred-build member, §5B.3.9), provenance, creation (scheduled, on
demand, "ensure fresh before a risky operation", pre-update), verification, retention, listing,
import of an uploaded archive, staged restore and its automatic and manual undo, offline restore for
host loss, the revocation journal, compatibility with ABO archives and with every build that can roll
back into the store.

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
| Main Pebble DB | `SystemOfRecord` | always | main archive, `<db-basename>/…` | `storage.Admin.Checkpoint(dir, WithFlushedWAL)` on the DB's filesystem | staged dir renamed into place |
| Manifest | metadata | always | main archive root `mediacore-manifest.json` + sidecar `<main>.manifest.json` | built after the hash pass | read first; drives verification and the swap |
| Level stamps, applied steps and legacy projection | metadata | always (as manifest values) | manifest | read from the checkpoint | sidecars `.storage-levels`, `.storage-applied` and `.storage-format` rewritten from the staged store |
| Operator config file (05-config) | `Secret` when 05-config's `ConfigFileHasSecrets()` is true, else `SystemOfRecord` (DATA2-17) | always | companion `config/<file>` — the file itself when it holds no secret; otherwise 05-config's `RedactedConfigFile()` and manifest flag `config_redacted` (archives are plaintext until encryption is built, §5B.3.9) | file copy | staged; swapped only when `ReplaceConfig` and the rename pre-check pass, else left in `restore/<id>/config.restored` for the operator; a redacted copy is never swapped in, and restore warns that its secret values must come from env or be re-entered |
| Boot record `<state_dir>/config.boot.json` (05-config §1.3.5) | `SystemOfRecord` | always | companion `config/config.boot.json` | file copy | as the config file |
| ABO's plaintext mirror `<db parent>/config.yaml` | — | **never** | — | — | — (it duplicates secrets in clear, `persistence.go:32`) |
| Secrets keyring | — | **never** | — | — | recovered from 05-config's keyring export; restore checks that the keyring covers the archive's secret records (§5B.3.4) |
| TLS material (`certs/` beside the DB, ABO `appdirs`) | `Secret` | never while archives are plaintext (lands with encryption, §5B.3.9) | companion `certs/…` | dir copy | staged |
| Module artifacts declared `SystemOfRecord` | `SystemOfRecord` | always | companion `artifacts/<owner>/<name>/…` | module `Snapshot` hook | module `Restore` hook into staging |
| Full-text indexes (`storage/index`) | `Derived` | off (`core.backup.include_derived`) | companion `index/…` | bleve online `CopyTo` (`bleve/v2 index.go:350-354`) | offered to the index only if its recorded generation matches the restored store's; else rebuilt |
| Derived snapshots (hnsw) | `Cache` | off | companion `derived/<name>/…` | `DerivedIndex.Snapshot` | `DerivedIndex.Load` with the generation rule; else rebuilt |
| Auxiliary DBs (Open Library mirror) | `Recreatable` | off (`core.backup.include_recreatable`) | companion `aux/…` | `Aux` checkpoint | staged |
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
	Name        string // "books.playlists"; unique; companion path artifacts/<owner>/<name>/
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
	Entries             []Entry                `json:"entries"`        // main archive files: path, size, sha256, class
	Companion           []Entry                `json:"companion"`      // companion archive files
	Derived             map[string]DerivedInfo `json:"derived,omitempty"`
	ConfigRedacted      bool                   `json:"config_redacted,omitempty"` // the companion holds 05-config's RedactedConfigFile (DATA2-17)
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
	Name       string // main archive file name
	Companion  string // companion file name ("" for legacy-abo/0)
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

// Runner is what the updater receives (05-updater pre-update backup).
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
	// ErrRestorePending is storage's one slot sentinel under backup's name
	// (03 R4, COR8-6): the same value, so errors.Is matches either spelling.
	ErrRestorePending      = storage.ErrSwapPending
	ErrUnreadableSecrets   = errors.New("backup: the keyring lacks keys the archive's secret records need")
	ErrUnencryptedSecrets  = errors.New("backup: refusing to write secret material into an unencrypted archive")
	ErrProtectedByFloor    = errors.New("backup: archive is protected by a retention floor")
)

// The slot and pre-check refusals are storage's, shared with the factory
// reset and with ABO's pre-S8 staging (R8-1, COR8-6): storage.ErrSwapPending
// (an intent of any kind is staged, or a swap is in flight) and
// storage.ErrRenameRefused (a target cannot be swapped by rename);
// ErrRestorePending above is the first one's alias, and there is no reset
// sentinel (ErrResetPending is deleted, R8-1).
```

```go
// Package journal is the leaf of backup that auth and rbac import (COR2-9):
// github.com/falkcorp/mediacore/backup/journal, L4, imports entity and storage
// (with its L0 leaf storage/restorejournal) only, lands at 09 S4. It holds the out-of-DB revocation journal (§5B.3.6),
// its file-backed implementation, the restore-hook contract and StorageSpec,
// the c/backup/ storage.Spec the kernel registers from S4 (COR5-6).
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
	Gap                   Kind = "gap"                     // an interval with revocations the journal may lack; Detail = [since, reason: "append_failed" | "non_journaling_build"]
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
// restorejournal.Kind ("archive", "revert", "checkpoint", "reset"; KindReset,
// 05-storage §5S.3.15), the intent's "kind" field; a reset runs no restore
// hook, so RestoreKind has no reset value (R8-1).
type RestoreKind uint8
const (
	RestoreKindInvalid RestoreKind = iota
	KindArchive    // an archive
	KindRevert     // the undo point of the last restore
	KindCheckpoint // a pre-migration checkpoint (05-storage §5S.3.12)
	KindExternal   // replaced outside the staged restore (an older build's restore, a file copy), detected at boot (§5B.3.6)
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
	// (05-storage §5S.3.10; COR7-8). For an owner whose rows live in an app's
	// legacy families (auth and rbac for ABO) the kernel also builds that
	// owner's store — the same legacy or native implementation as the live
	// one, its legacy.Stores constructor called once more with legacy.Handles
	// cut from the read-only DB, whose handles fail every write with
	// storage.ErrReadOnly — and hands it to the owner's hook function (auth:
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
// restorejournal.Record lines (05-storage §5S.3.15; no alias here, SIMP8-8).
// A restore's steps: "staged", "rechecked", "previous_moved",
// "staged_moved", "sidecars", "swapped", "opened", "restore_line",
// "hooks_done", "marker_written", "applied", "failed", "swapped_back",
// "restore_aborted", "rejected", "applied_offline" (DATA7-12). Seq (staged)
// is the outgoing store's journal head seq, the recorded boundary of a later
// revert (DATA2-25); Sidecar and Options (step "sidecars" only) are the full
// stat identity of the .storage-format file restoreswap wrote and the highest
// OPTIONS-<n> of the staged DB directory, which the kernel hands storage's
// open so the restore boot's own swap is not a foreign open (R6-4, 05-storage
// §5S.3.7); Build (step "failed") is the binary whose boot failed (DATA8-6).
```

### 5B.3.3 Archive format `mediacore-backup/1`

```
Main archive  (restorable by every ABO build, pre-core included)
  <prefix><YYYYMMDD_HHMMSS><ext>              ext = .tar.zst | .tar.gz | .tar   (+ ".sha256", sha256sum format)
    mediacore-manifest.json                    first entry; an older build extracts it as a stray file beside the DB dir
    <db-basename>/...                          the Pebble checkpoint, rooted at the DB directory's basename (legacy layout)
Companion archive  (same stem; no extension an older build lists)
  <main name>.extra                            tar + zstd, sniffed by magic   (+ ".extra.sha256")
    config/<file>, config/config.boot.json
    artifacts/<owner>/<name>/...
    index/<module>/<name>.bleve/...            if IncludeDerived
    derived/<name>/...                         if IncludeDerived
    aux/<module>/<name>/...                    if IncludeRecreatable
Sidecars
  <main name>.manifest.json                    the manifest (MAC'd); source of the catalogue
  <main name>.verify.json                      last verification result {at, level, ok, detail}
```

(The encrypted variant, `<main name>.age` and `<main name>.extra.age`, is a deferred-build member,
§5B.3.9.)

- **Why this layout** (DATA-21, OPS-14). An older build restores by extracting every entry under the
  target by name and lists only `audiobooks_` names with a `.tar.gz`/`.tar.zst`/`.tar` extension
  (`backup.go:777-870`, `codec.go:82-84,225-245`). The main archive therefore restores the DB exactly
  as today, the manifest lands as a harmless stray file, and the companion and sidecars match no
  listing rule, so an older build neither restores nor prunes them. Because every earlier release stays a
  rollback target during the extraction (R3-1), an operator who rolls back can restore any plaintext
  archive made since the upgrade.
- **Names.** ABO (the books module registers it) keeps the legacy pattern
  `audiobooks_pebble_<YYYYMMDD_HHMMSS><ext>` (`backup.go:333`); other apps use `<AppID>_<YYYYMMDD_HHMMSS><ext>`.
  The archive ID lives in the manifest. A name collision within one second waits for the next second.
  Only files with the app's registered prefix are ever listed or pruned (the prefix rule protects
  foreign files in a shared backup dir, `codec.go:225-245`).
- **Codecs** are sniffed by magic bytes on read (zstd, gzip, plain tar), as ABO does for
  compression (`codec.go:204-222`); `core.backup.compression` picks the writer (default `zstd`; ABO keeps
  its current `gzip` until the operator changes it, so an upgrade never changes the codec silently).
- **No key material in any archive.** The secrets keyring stays in the state directory and is recovered
  from 05-config's `secrets export-keyring`; the archive's secrets stay ciphertext. 05-config's
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
P4d step 1, before the backup directory an admin set in Settings is known): the backup service calls
05-config's `secrets.RetireUnused` with that in-use set, computed from the manifests and the journal of
the **effective** `core.backup.dir` (COR3-2). An archive whose key is already retired (an imported one,
or a key retired before this rule) verifies as foreign and is restorable only as a foreign restore.

**Host-loss restore** (documented procedure, also in the operator runbook): copy to the new host the
backup directory (archives, companions, sidecars and the revocation journal) and the keyring export
(05-config). Put the exported keyring in the new state directory first (05-config §2.4 — it carries the
provenance key and the legacy secret key), then run the offline restore
`<app> restore --archive <file> [--foreign]` before the first start, with a build at or above the archive's upgrade floor: an archive
older than the build's `UpgradeFloor` is refused at staging (step 4) naming the release to install
first — install that release, restore with it, run it until it has applied the steps the newer build
requires, then upgrade (DATA3-9). With the keyring in place and no live store, the archive's MAC
verifies and the restore is `Own`; otherwise it is foreign and needs `--foreign`. A pre-S0 archive has
no install marker; the restore hands the restore boot the revocation journal header's `install_id`
(else the keyring export's, 05-config) so the restored store keeps this install's lineage (05-storage
§5S.3.4 rule 4, DATA3-12). With no pre-swap store, nothing anchors the end of the revocation journal, so the restore treats it as
uncovered and every API key needs confirmation (§5B.3.6, SEC2-9).

**A production archive on a staging host is not inert (R6-7, OPS6-4).** A main archive carries every
password hash, raw legacy session token (`sess:` rows are bearer credentials), invite, the plaintext
secrets of the config blob and the encrypted secret rows; booting a restored copy also starts its
schedules and outbound integrations with production URLs and credentials. Key regeneration is not a
scrub. An archive restored anywhere but its own install (staging copies, the boot-budget copy among them)
is therefore prepared by **09 §9.3's five-step staging procedure, the only one** (R7-6, R8-3), and **never
by the staged restore of §5B.3.4**: a staged intent puts nothing at the DB path before the first boot, so
nothing could be scrubbed before that boot. The copy is written by `staging-restore --archive <file> --db
<empty path>`, an ABO subcommand from S0 that reads and verifies the archive offline, refuses a non-empty
target and never writes `config.yaml`; it is scrubbed offline by `staging-scrub`; and both run with the
binary of the release that wrote the archive, which has no pending step on the copy (OPS8-2, SIMP8-2,
SEC8-11). What the procedure scrubs, how egress is denied, which test asserts that the first serve boot
finds everything already scrubbed, and what stays running are stated in 09 §9.3, not here (OPS7-2,
OPS7-3, OPS7-4, OPS7-7, SEC7-6, SEC7-10). This file adds no "staging mode" that would make a plaintext
archive safe to boot as it is.

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
   in flight (an `inflight.json` or `inflight-checkpoint.json` under `restore/`); `ErrRestorePending` is its
   alias. This is the early refusal;
   step 8's atomic create is the one that decides, so two stagers — in one process or two, the kernel's or
   ABO's pre-S8 `internal/backup` — never both succeed, with no mutex (R8-1, SIMP8-1, COR8-6;
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
   with 05-config's `KeyIDsReferenced(ctx, r storage.Reader)` over that staged view — which also reports
   `k0-legacy` for every legacy-format secret row it finds (a pre-S3 archive has no `c/secrets/` but
   does have rows that need the legacy key, DATA3-12); any key missing from the keyring →
   `ErrUnreadableSecrets` unless `AcceptUnreadableSecrets` (warning: those secrets must be re-entered;
   05-config treats an undecryptable legacy row as "must be re-entered" at runtime rather than failing
   boot). Because HEAD's legacy cipher has no key ID, `k0-legacy` "matches" every archive's legacy
   rows, including a foreign or host-loss archive's encrypted under another install's key file: staging
   therefore calls the app's legacy secret store, `LegacyStore.Check(ctx, r, key)` (05-config §2.3: the
   app's code, which knows its row format), over the staged reader with this keyring's `k0-legacy`
   material; it trial-decrypts one legacy copy per name (a sentinelled copy, R4-2, decrypts like any
   other), and every name it returns as failing is reported as `ErrUnreadableSecrets` too, needing
   `AcceptUnreadableSecrets` (05-config §2.4, DATA4-22). Core holds no legacy secret row name.
6. **Pre-validate every rename** the boot phase will do (DATA-23): staging and each target on the same
   filesystem, parent writable, target not a mount point and not a single-file bind mount (device of the
   file ≠ device of its directory). The DB directory and its sidecars must pass (`storage.ErrRenameRefused`
   otherwise, the factory reset's pre-check sentinel too, 05-storage §5S.3.15). The config file and boot record are swapped only when `ReplaceConfig` is set, the archive
   holds the file itself (not a redacted copy) **and** they pass; otherwise they are extracted to
   `restore/<id>/config.restored/` for the operator to merge.
7. Extract into `<db parent>/.mediacore-restore/<id>/staging/` — the DB's filesystem, device-checked as
   `ensureSameDevice` does (`backup.go:577-598`), because the swap is a rename. Every file fsynced, then
   each directory.
8. Create `<state_dir>/restore/pending.json` = `{archive_id, kind, staging, targets[{from,to}],
   db_path, install_id, manifest_sha256, staged_by_build, pebble_options, expires_at, requested_by,
   requested_at}` (`kind` `archive` or `revert`; `pebble_options` the live DB directory's highest
   `OPTIONS-<n>`, its open stamp, or none on a host with no store; SIMP8-1) —
   every path **absolute**, resolved now from 05-config's `BootView` (`core.storage.path`,
   `core.paths.state_dir`; COR2-8); `install_id` is the live store's marker or, on a host with no store,
   the revocation journal header's (else the keyring export's), which the restore boot writes into a
   marker-less restored store (05-storage §5S.3.4 rule 4, DATA3-12) — atomically: temp file + fsync,
   `link(2)` to `pending.json`, which fails with `EEXIST` when the slot is taken (`storage.ErrSwapPending`;
   the staging directory is then removed), remove the temp, directory fsync; then the journal record
   `staged` to `<state_dir>/restore/<id>/journal.json`. The `staged`
   record carries the live store's `c/backup/journal_head` seq: the store it describes can only see more
   lines before the swap, so it is a safe boundary for a later revert of this restore (DATA2-25).
9. Return `202` with `restart_required: true`; with `Restart`, request a restart through 06's
   `Lifecycle.RequestRestart`, which drains and exits with the restart exit code, so the committed unit's
   `Restart=on-failure` brings the service back without an operator (OPS8-12). While `pending.json` exists,
   or a swap journal holds an `inflight.json` (step g), `make rollback`, the updater (05-storage
   `CheckReader` tokens `restore_pending`, `restore_inflight`, whose message names the intent's `kind`,
   OPS-14) and the operator `deploy` recipe (09 S1, OPS2-26) refuse, so a deploy cannot silently discard
   the restore. The one override, `ROLLBACK_IGNORE_RESTORE=1` for both recipes, passes `restore_pending`
   only — an intent left behind is then rejected by step a, because the older build's open moves the open
   stamp — and never `restore_inflight`: a swap that has started is finished by booting this build once
   (OPS8-7; `DEPLOY_DISCARD_RESTORE` is deleted, SIMP8-1). Over ssh the recipes probe the three paths with
   the one literal sudoers command of 05-storage §5S.3.7, which passes on a store that never staged
   anything, and a probe that cannot run refuses (OPS3-13, OPS4-3). One exception (DATA7-12, DATA8-6): a
   `KindCheckpoint` journal at `swapped` or `failed` does not refuse a candidate whose levels and floors
   admit the restored store **and** which implements the journaled swap (it answers the fast-exit flag
   `--print-restore-journal`, which ships at S2b; a candidate without it reads `restore_inflight`) and,
   for `failed`, is not the build the journal names; its in-flight file is named
   `inflight-checkpoint.json` so the probe tells it apart by path (05-storage §5S.3.12).

Boot phase — kernel **pre-boot** step P0a ([06](06-app-sdk.md) §6.5.1), before any config file or
storage is read. The updater's pre-boot check P−1 runs first (OPS2-3): it decides a rollback to
`.prev` before any swap, and `CheckReader` ignores a pending intent staged by the failing build as long
as no swap step has started; the restored `.prev` then rejects that intent at step a. P0a resolves only
the state directory, by 05-config's pre-boot rule (flags, env, compiled default, and next to the
database when that default cannot be created — HEAD's fallback, R3-4/OPS3-1; never the config file or
the DB) and trusts the **absolute targets recorded in `pending.json`**; 06 P2 then verifies that storage
opened at the recorded `db_path`, and a mismatch is a failure after the swap (step x) (DATA2-25). P0a
also **resumes** any restore journal whose last step lies between `previous_moved` and `applied`, and a
`KindCheckpoint` journal at `failed` unless this binary is the one its `failed` record names (DATA8-6), and
the kernel decides "this is a restore boot" from that journal, never from `pending.json` alone (DATA3-6).
The binary a `failed` record names refuses to boot while that journal is in flight, naming the rollback
or a fixed build. **One slot, no precedence rule (R8-1).** There is one intent slot for every kind, so at
most one intent and one swap exist; a factory reset is the slot's `KindReset` (05-storage §5S.3.15), and
the former reset-supersedes-restore rule and its test are deleted:

| Step | Action (journaled in `restore/<id>/journal.json`; each step idempotent: rename only if source exists and destination does not) |
|---|---|
| a | Reject a **stale or foreign intent** (OPS-14), by the one rule for every kind (R8-1, SIMP8-1): `expires_at` passed, `staged_by_build` ≠ this build, or the live DB directory's highest `OPTIONS-<n>` differs from the intent's `pebble_options` (another binary opened the store since staging, SEC7-5) → move `pending.json` to `restore/<id>/rejected.json`, boot on the **untouched** live data, health `degraded` (`restore_rejected`, naming the intent's kind). An older build ignores `pending.json`; this rule stops a later roll-forward from applying it by surprise. A journal that already shows `previous_moved` or later is never rejected: the swap is resumed or swapped back (step x), whichever build is running. |
| b | Re-check staging against `manifest_sha256` and entry sizes (full re-hash if `core.backup.restore.rehash_on_boot`). Failure → reject as in a. |
| c | Copy `<state_dir>/backup.location.json` and `<state_dir>/storage.boot.json` to `restore/<id>/` (temp file + fsync + rename each; the originals stay, because the restore boot locates the journal through the first and reads its lineage from the second; R7-4, DATA7-5, DATA8-7). Rename live items to `<db parent>/.mediacore-restore/<id>/previous/`: the DB dir, its sidecars (`.storage-levels`, `.storage-applied`, `.storage-format`, `.migration-checkpoint`), and — only when being replaced — config file and boot record; index and derived dirs. |
| d | Rename staged items into place. |
| e | Rewrite the three sidecars from the staged store: `.storage-levels` from its stamps, `.storage-applied` from its bookkeeping (its versions and its `install`/`epoch` header), and for books `.storage-format` (removes the "older archive, newer sidecar" case; DATA-23d, DATA4-17, OPS4-15), so no reader of a sidecar ever sees the pre-restore store between the swap and the restore boot's step 9. The journal step `sidecars` records the full stat identity of the `.storage-format` restoreswap just wrote and the highest `OPTIONS-<n>` in the staged DB directory; the restore boot compares the sidecar and the directory with these instead of the restored store's foreign-open record and Pebble open stamp, so a staged restore, revert or checkpoint restore is never a foreign open and owes no duty for its own swap (05-storage §5S.3.7; R6-4, DATA6-2). |
| f | Mark every derived index for rebuild; included `Derived`/`Cache` items are offered to `Load` under the generation rule. |
| g | Journal `swapped`; **rename** `pending.json` to `restore/<id>/inflight.json` (a `KindCheckpoint` swap names it `restore/<id>/inflight-checkpoint.json`, so `CheckReader` and the rollback script apply the checkpoint exception from the path alone, 05-storage §5S.3.7; R7-4, DATA7-12) (not deleted: it is removed only when the journal reaches `applied` or `swapped_back`, so a crash anywhere between here and the hooks still resumes as a restore boot, and `CheckReader`, `make rollback` and `deploy` keep refusing, DATA3-6). The `restore` line of the revocation journal is **not** appended here — the keyring that MACs it is not open at P0a — but at P4d step 2 (§5B.3.6; journal `restore_line`). Boot continues: P0 reads the restored config (if replaced), storage opens at the recorded `db_path` (journal `opened`), a marker-less store gets `install_id` from the intent (DATA3-12), and the pre-migration checkpoint and the migration DAG bring an older archive forward (G2 rules): every versioned step the archive's store lacks runs again on it, one-shot steps included, so such a step must not trust values it did not vet (05-metadata's S7 `Local` step, DATA5-9). |
| h | At 06 P4d step 3 (05-storage §5S.4 step 11 (c)) — **after** step 1 opened the keyring and attached the DB overrides and step 2 re-applied the journal tail and appended the `restore` line, before `auth.BootWrites` and any listener — the kernel runs the restore hooks in the order auth, rbac, audit with a `journal.RestoreContext` (`Previous` = read-only view of `previous/`, `Revocations` per §5B.3.6 or nil, the restore journal, the counts of earlier hooks). 05-authn re-applies the later revocations, invalidates every hashed session and one-time token in auth's store **and deletes every legacy session and invite** (for ABO, books' legacy identity store deletes its legacy session and invite rows and their index rows, and its standalone session and token records), bumps epochs, marks `password.changed` users reset-required, and sets keys `needs_confirmation` when `Revocations` is nil; 05-rbac re-applies role kinds; audit raises its `Sequence` past and copies forward the records above `AuditHighWater` from `previous/` and appends the restore records (`core.restore.revoke`, `core.restore.apply`). Every hook write is an ordinary write through the owning subsystem's store — for ABO, of the legacy row by books' legacy implementation, the one stored format (R3-1, R4-1) — so the next boot and every rollback build read the result as it is; there is no second copy to reconcile. Hooks are idempotent; the last hook batch records `c/backup/restore_hooked/<seq>` for the `restore` line (journal `hooks_done`). Only **then**, on a restore boot, are `c/backup/served_boot` and storage's boot lineage `c/storage/boot` (`n = max(store n, file n) + 1`, a new nonce, 05-storage §5S.3.7) written in one batch, and after it commits `backup.location.json` (the restored store's generation, content marker and committed head) and `<state_dir>/storage.boot.json` (journal `marker_written`), so a restore that fails before its hooks commit leaves the files describing the pre-swap store (DATA6-3), and one that fails after `applied` but before Run step (3b) reads a **same** lineage at the next boot, never behind (R7-4, DATA7-4); then journal `applied`, and `inflight.json` is deleted. |
| x | **Automatic swap-back** (DATA-23): any failure after step c — a rename, storage opening at a path other than the recorded `db_path`, the guard refusing the restored levels, a failing migration, a failing hook — closes storage (the journal component, stopped with it, has not rewritten the committed-head anchor since the swap, §5B.3.6 row "Head in the store"); when the boot already appended the `restore` line (a failure at or after P4d step 2, so the keyring is open), appends a `restore_aborted {seq}` line first (a failed append leaves the restore journal's `swapped_back` record as the evidence), so the line is neither resumed nor counted as evidence that the original store was replaced; `served_boot`, the lineage and `backup.location.json` were not yet rewritten for the restored store (step h), and step x renames the copy step c saved back over `backup.location.json`, so the next boot reads the original store as the same store, its head equal to the anchor, and runs no hook (DATA6-3, R7-4, DATA7-5); it renames the saved `storage.boot.json` back too, so the original store never reads **behind** (DATA8-7 (c)); the hook lines between the `restore` line and its `restore_aborted` line carry that line's seq (`RestoreSeq`) and are never re-applied (§5B.3.6 (3), DATA8-7 (a)); then it moves the restored items to `restore/<id>/failed/` together with every pre-migration checkpoint the restore boot took (their records name the restored store; one that cannot be moved is marked `restore_left`, so it is never reused or restored, 05-storage §5S.3.12, DATA3-1), renames `previous/` back, rewrites the sidecars from the original stamps, journals `swapped_back`, deletes `inflight.json` and exits non-zero. The automatic restart boots the original data with health `degraded` (`restore_failed`) and the reason. On a host with **no** previous store, the journal records `no_previous` and storage refuses to create a fresh store at that path until the operator resolves it, so a failed host-loss restore never leaves an empty store behind (05-storage §5S.4 step 1, DATA3-9). If the swap-back itself fails, boot halts with the journal naming every path, and the offline `<app> restore --revert <id>` completes it. **Exception (DATA7-12, DATA8-6):** a `KindCheckpoint` restore boot whose migration DAG fails does **not** swap back, because the swap-back would only bring back the store whose migration failed: it keeps the restored checkpoint, journals `failed` with its own binary ID (`Build`), keeps `inflight-checkpoint.json` and exits non-zero; the operator rolls back to the previous binary or installs a fixed build, either of which `CheckReader` admits by step 9's exception and whose P0a resumes the journal as a restore boot — its own DAG, the hooks, then `applied`. |

**A factory reset is `KindReset` of this swap (R8-1, SIMP8-1).** Its intent is `restore/pending.json` with `kind: "reset"`, created in the same slot by the same atomic create (step 8), so a reset and a restore exclude each other by the slot alone. It is rejected by step a's one rule, its set-aside renames are journaled in `restore/<id>/journal.json` with the reset's step names and resumed the same way, and its in-flight file is `restore/<id>/inflight.json`, deleted at `hook_done` instead of `applied`; the tokens are `restore_pending`/`restore_inflight` and the override is `ROLLBACK_IGNORE_RESTORE=1`, pending only (step 9). It differs from a restore in three places, all specified in 05-storage §5S.3.15: every target is pre-validated at staging and set aside beside itself, on its own filesystem; a failed set-aside moves back what it moved and journals `rejected`, booting the untouched store; and there are no restore hooks, because the next boot is a fresh install, while HEAD's in-request side effects run in the app's post-reset hook after the journal reads `applied`, are not undoable, and never fail boot.

Undo: `previous/` is kept for `core.storage.swap.keep_previous` (05-storage §5S.7, a key of the swap that lands at S2b; SIMP8-1) and then swept by the kernel after a boot reaches readiness. `RevertLastRestore` (API) and
`<app> restore --revert` (offline, for an app that no longer boots) stage it through the same mechanism
with kind `KindRevert`. 05-storage's offline `storage restore-checkpoint` uses the same swap with kind
`KindCheckpoint`; all three are core code from S2b ("Stages"). A crash at any step resumes from the journal at the next boot: forward to `applied`
(re-running the idempotent hooks, `TestCrashBetweenSwapAndHooksResumesHooks`), or into the swap-back.

**Checkpoint restore (DATA7-12, R7-4).** Three rules make `storage restore-checkpoint` followed by
`make rollback` work (05-storage §5S.3.12): (1) for an **open** checkpoint (no boot reached readiness since
its `taken_at`, the failed-attempt case) the offline tool finishes the journal to `applied` itself, with no
hooks, because nothing was served since the checkpoint; **before** it journals `applied_offline` it commits
`kernel.OpenOffline`'s close batch over the restored store with the step-e identities standing in for the
store's records, as on a restore boot, promoting the directory's highest `OPTIONS-<n>` and the
`.storage-format` identity it wrote and keeping the served ledger's `last` and the lineage, so the next
boot (`.prev` after `make rollback`) reads no foreign open and owes no duty for the swap (DATA8-5; the same
rule binds every offline swap finished without a boot); then it deletes `inflight.json` and logs it, so
nothing refuses the rollback; (2) for `--accept-loss` on a completed checkpoint it leaves the restore boot
with its hooks, and `CheckReader` passes a `KindCheckpoint` journal at `swapped` for a candidate that
implements the journaled swap and whose levels and floors admit the restored store (step 9); (3) a
`KindCheckpoint` restore boot whose DAG fails keeps the restored store, journals `failed` with its binary ID
and exits non-zero (step x's exception); a candidate admitted at `failed` resumes it as a restore boot
(`TestRestoreCheckpointThenRollback`, `TestOpenCheckpointRestoreThenRollbackIsNotForeign`,
`TestCheckpointRestoreBootFailingStepKeepsCheckpoint`).

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
   `ActiveDeriveID()`), then stream the main archive (manifest first) and the companion through tar →
   codec into `.partial` files, re-hashing while streaming and aborting on any
   mismatch.
6. fsync, rename to the final names, write the `.sha256` and `.manifest.json` sidecars, fsync the
   directory, and refresh the catalogue entry.
7. If `core.backup.verify_after_create`, run `VerifyOpen`; write `<main>.verify.json`.

Progress uses the existing phase model (checkpoint / archive / checksum / verify) with counters from
real work (`backup.go:51-82`) mapped onto `tasks` progress.

Verification task `core.backup.verify` (`Schedule: tasks.ScheduleFromConfig("core.backup.schedules.verify")`)
re-verifies the newest `core.backup.verify_keep` archives at `VerifyEntries` and the newest at
`VerifyOpen`, and rewraps every Own archive whose `Provenance.KeyID` is a derivation key in `verify`
state (§5B.3.3, SEC2-24). Create runs on `tasks.ScheduleFromConfig("core.backup.schedules.create")`. Both keys
are declared in backup's own namespace with type `tasks.ScheduleConfig` (R2-7, COR2-6, SIMP2-1). No
bespoke timer.

**Catalogue** (DATA-24). The archive list is **derived from the backup directory**: every
`<main>.manifest.json` (MAC checked: unverifiable ones are listed as foreign) and `<main>.verify.json`,
plus legacy `audiobooks_*` archives. `c/backup/archive/<id>` is only a cache, reconciled from the
directory at boot and before every list and prune; it is never authoritative. After restoring archive X,
newer archives (including the safety archive taken just before the restore) therefore stay listed, stay
in the byte budget and keep protecting the floor. **Orphans** (DATA2-27): during a rollback the older
build's retention deletes main archives it lists (`internal/backup/codec.go:225-245`) but not their
`.extra`, `.manifest.json` or `.verify.json`. Reconciliation drops every entry whose main archive is
missing and deletes its orphaned companion and sidecars (logged), so the catalogue never lists, counts or
protects an archive that no longer exists.

Retention (task `core.backup.prune`, also run inside create):

| Bound | Key | Default | Note |
|---|---|---|---|
| count | `core.backup.retention.max_count` | 10 | must be ≥ 1 (ABO's `0 = keep none` maps to 1) |
| bytes | `core.backup.retention.max_total_bytes` | 40 GiB | 0 = unlimited, as `backup.go:174-188`; main + companion counted together |
| age | `core.backup.retention.max_age` | 0 (off) | |
| floor 1 | — | the newest **verified** archive | an unverified newer archive does not protect the last good one |
| floor 2 | — | any archive referenced by a pending or undo-able restore | (the plaintext floor of DATA-22 lands with encryption, §5B.3.9) |

Deletion order oldest first; a main archive and its companion and sidecars are deleted together; a failed
delete is not counted as freed (kept from `backup.go:1255-1262`). Prune also trims the revocation journal
(§5B.3.6) without breaking its chain.

Pre-operation backups: modules call `Deps.Backup.Ensure(ctx, maxAge, "pre:books.organize")` (organizer
today: 6 h, `organizer/service.go:552,697`); the updater calls `Runner.Create` with reason `pre-update`
(05-updater); the kernel calls `Ensure` with `core.backup.before_breaking_migration` before any boot whose
DAG holds a `Breaking` step.

### 5B.3.6 Revocation journal (out of DB)

Cross-cutting resolution 8 and SEC-16: a restore must not bring back a credential revoked after the
archive was taken. The record of those revocations therefore lives outside the DB that the restore
replaces, and — because the backup directory is untrusted storage (a network share, an off-host copy)
— it carries its own integrity (SEC2-9).

| Rule | Specification |
|---|---|
| Where (DATA2-27, DATA3-5) | `<backup dir>/<prefix>revocations.journal` (0600; JSON lines), where `<prefix>` is the app's archive prefix (ABO: `audiobooks_pebble_revocations.journal`), so two apps sharing a backup directory never share a journal; an older ABO build neither lists nor prunes it (no archive extension). It sits beside the archives so an off-host copy of the backup directory carries it. `core.backup.dir` is **restart-bound** (§5B.7; declared from S4 in the kernel's core-section list, COR4-10, COR5-6), and `<state_dir>/backup.location.json` = `{backup_dir, journal, db_path, install_id, store: {epoch, dir_dev, dir_ino}, served_boot: {n, nonce}, head: {seq, mac}}` (paths absolute) records where the journal is and which store it describes (`db_path`, `install_id`), and — rewritten at the end of every journaling boot's P4d step 2 (on a restore boot only after the hooks, §5B.3.4 step h), after every `Create` (§5B.3.5 step 4) and by the committed-head anchor (row "Head in the store") — which store generation (05-storage `c/storage/epoch`), which content marker and which committed journal head it last served (DATA4-7, SEC5-6, DATA6-4). **One writer (DATA7-14):** in a running process only the journal component writes the file; it holds one mutex across every read-modify-write of it, and `Create` hands its new pair to the component instead of writing the file, so the anchor can never rewrite `{P, H′}` over Create's `{P′, H}` (`TestLocationFileWritersSerialized`). The only writer outside that process lifetime is restoreswap: step c saves a copy into `restore/<id>/` and step x renames it back (R7-4). Its `store`, `served_boot` and `head` are compared only when `db_path` equals the resolved `core.storage.path` and `install_id` equals the store's marker; otherwise the file describes another store sharing this state directory, the boot reads "no marker" and rewrites the file for this store (SEC6-8). At P4d step 2, when the effective directory (DB overrides attached at step 1) differs from the recorded one — an admin moved backups in Settings, or a restore brought back another override — the kernel first **moves** the journal: copy, verify the whole chain, fsync, rename, update the record; a move that fails verification keeps the old file and the boot reports `revocation_journal_broken`. **Not on a restore boot (DATA8-7 (b)):** while a restore journal is between `swapped` and `applied` the journal stays at the recorded location — the `restore` line and the hook lines go there, and a swap-back leaves it where it was — and the first boot after `applied` moves it. So a live directory change can never split the journal. **Colocation (SEC6-10):** with the default directory (`backups` beside the DB, `internal/backup/backup.go:145`) and the state directory's fallback to the DB's parent (`internal/config/state_dir.go:143-154`), one volume holds the store, the journal, `backup.location.json`, `storage.boot.json` and every marker, so a whole-volume or VM snapshot rollback reverts them together and every check reads "same store": revocations made after the snapshot come back undetected. Health reports `info` `revocation_journal_colocated` when the journal's filesystem device equals the store's, the operator runbook recommends a `core.backup.dir` on another volume and says to go back with the staged restore, never a volume snapshot, and 14 K22 states the residual. |
| Format and integrity (SEC2-9) | Line 0 is the header `{seq: 0, install_id, app, started_at, kid, base_seq, base_mac, mac}`; every later line is a `journal.Revocation` plus `kid` and `mac`, with `seq` = previous + 1 and `mac = HMAC-SHA256(DeriveKeyFor(kid, "core.backup/revocations/v1"), prev_mac ‖ canonical line without mac)`, `kid` being the derivation key ID the line was written under (05-config; a derive-key rotation changes `kid` mid-journal without breaking the chain). Deleting, reordering, editing or forging a line breaks the chain. |
| Head in the store (SEC2-9, DATA2-5, DATA4-7) | Every revoking batch also writes `c/backup/journal_head` = `{seq, mac}` (`RevocationJournal.Append` stages it by participant binding). `c/backup/journal_head` is `Serialized` (the `c/backup/` `storage.Spec`, registered by the kernel from S4 through `journal.StorageSpec()`, COR5-6), and `Append` stages `max(head, seq)` through `GetForUpdate`, so two revocations that commit out of append order never move the head backwards. The store — and so every archive and checkpoint made from it — knows the last line it committed; truncating the file below a store's head is detected. The head can still lag the journal on an unreplaced store (a crash between a line's fsync and its batch commit, a failed commit): that is a re-apply case, never a replaced store (row "Boot re-apply"). **Committed-head anchor (DATA6-4):** the journal component reads `c/backup/journal_head` (it holds the `c/backup/` handle) after its callers commit — at its next `Append`, on an in-process one-minute tick (SIMP-6's in-process exception) and at shutdown — and rewrites `backup.location.json`'s `head` (temp file + fsync + rename) whenever the committed head moved; P4d step 2 rewrites it after the re-apply. It reads only committed state, so it may lag the store but is never ahead of it, and a store whose head is below it was replaced (row "Boot re-apply" (2)). **Frozen during a restore (R7-4, DATA7-5):** on a restore boot, and whenever a restore journal is between `swapped` and `applied`, the component does not rewrite the anchor — not after the re-apply, not on the `restore` line's `Append`, not on a tick and not at shutdown; step h writes it with the marker, after the hooks, and step x never rewrites it but restores the copy step c saved, so a swap-back leaves the original store's head equal to the anchor (`TestRestoreFailureSwapsBack`). |
| Content marker (SEC5-6, SEC6-6, SEC6-7, DATA6-4) | `c/backup/served_boot` = `{n, nonce}` (`Serialized`; every write is a `GetForUpdate`, COR6-6), written by every journaling boot at the end of P4d step 2 (on a restore boot after the hooks, §5B.3.4 step h) and by every `Create` right after its checkpoint (§5B.3.5 step 4), each time with `n` = max(the store's `n`, `backup.location.json`'s `n`) + 1 and a fresh random nonce, and the same pair then written into `backup.location.json`. It is content, so it travels with the store: an older build that restores an archive **in place** keeps the directory's identity and usually its `c/storage/epoch` (HEAD extracts into the live directory, `internal/backup/backup.go:777-850`), but not this pair. Because `Create` advances the live pair after its checkpoint, **every archive core made carries a pair older than the file**, whichever boot it was taken in, last night's included (SEC6-6). A store whose pair is older than the file's — a lower `n`, the same `n` with another nonce, or no pair while the file has one — was therefore **replaced**, whatever its directory identity (row "Boot re-apply" (2)); a store whose pair is ahead of the file's (a crash between the store write and the file write) is the same store, and the file is rewritten. The pair is compared only when the file describes this store (row "Where", SEC6-8). A store restored in place from an archive made before S4 r2 has no pair while the file has one, so it reads replaced by this rule ((2)(a)); the former "user rows but no session cut-off" signal of 05-authn is deleted, because it added no detection and had a crash window on the first S4 r2 boot (R7-4, DATA7-10). 05-storage's boot lineage (`c/storage/boot`, §5S.3.7) is the same signal at storage level for the served-ledger duties and the `install_id`; this marker is the journal's own, anchored with the head in the same keyspace. **Residual:** an archive the **older build itself** made after the last journaling boot and restored in place carries the current pair, the current head and the current lineage, so nothing detects it; its effect equals HEAD's own restore semantics, and the rollback before it is still a foreign open, so its revocation coverage already has a `gap` line (14 K22) |
| Writers (DATA2-5, COR4-4) | Every build in which every revocation path is core's — from S4 r2, the release in which core auth decides requests; its served-ledger record lists the `journal` duty (05-storage §5S.3.7). S4 r1 (dark: gin's handlers still revoke without journaling) does not list it, so its interval becomes a `gap` line. The writers: `core/auth` (key revoke and disable, user disable and delete, credential-epoch bump, password change, identity unlink, session revocation) and `core/rbac` (`role.unassigned`, `role.narrowed`, `role.deleted`) — the kinds of §5B.3.2. `Append` is called **before** the revocation's batch commits; each line is fsynced. A recorded revocation whose commit then fails is harmless: re-applying it revokes more, never less. A restore boot (P0a swap, revert, checkpoint restore) and a boot that finds a replaced store append a `restore` line `{kind, source, boundary}` at P4d step 2, once the keyring is open and after the re-apply; every line a post-restore hook then appends carries that line's seq in `RestoreSeq` (DATA8-7). No earlier build writes the journal: lines need the S3 keyring's derive key, so the S0 hotfixes do not include it (DATA3-19). |
| Boots without the journal duty (R3-1, R5-5, COR6-2, R6-4) | Every earlier release can open the store, so a rollback may run a build that does not journal: a release older than S4, a pre-guard build, or one whose own restore extracts an archive in place (HEAD, `internal/server/handlers/system/handler.go:824-862`). Its revocations are in the legacy rows — the one stored format — so nothing is lost while the store is not replaced. The interval is detected by 05-storage (§5S.3.7): the served ledger's previous record (kept unchanged until 06 Run step (3b), so a boot that fails in between leaves it for the next one) lacks the `journal` duty, or a foreign open — the sidecar's full stat changed, its inode number reused or not (SEC5-5) — or a behind boot lineage was seen; either way 05-storage records the duty as **owed** with its `since`, which the boot reads through `storage.Duties.Owed(DutyJournal)`. At P4d step 2, after `head_before` is read and the tail re-applied (row "Boot re-apply", SEC4-5), the journaling boot appends a `gap` line `{since, reason: non_journaling_build}` **unless a `gap` line already covers that `since`** (a retry after a failed boot appends nothing new), and the same batch stages `Duties.StageDone(b, DutyJournal)` (COR5-3, DATA5-6, COR6-2). **A restore boot appends no `non_journaling_build` gap for its own swap** (R6-4, DATA6-2): its swap is neither a foreign open nor a behind lineage (05-storage §5S.3.7 rows "Boot lineage", "Foreign-open record", "Pebble open stamp"), so the journal duty is owed on it only from the restored store's own evidence — an archive taken on a build without the duty, or one that already held an owed entry — and then the gap line is real. Every later restore whose boundary precedes it gets no coverage (keys need confirmation). |
| Append failure | The revocation still commits — a disk problem never blocks a security action — and the same batch records `c/backup/journal_gap/<since>` through `Gap`; health is `degraded` (`revocation_journal_unwritable`) until the next successful append writes a `gap` line closing the interval. |
| Boot re-apply (DATA2-5, COR3-2, DATA3-5, DATA3-8, SEC3-10, DATA4-7, DATA4-15, SEC4-5) | At every boot, 06 P4d step 2 (05-storage §5S.4 step 11 (b)), after step 1 opened the keyring (every line's MAC needs `DeriveKeyFor`) and attached the DB overrides (so the journal is found under the effective `core.backup.dir`, moved first if needed, row "Where"), in this order: **(0) install check** — the journal header's `install_id` is compared with `c/storage/install` **before** any line is used; on a mismatch (a core build's factory reset, 05-storage §5S.3.15, or a deleted DB directory gave a new install while the backup directory kept the old install's journal, whose MACs still verify under the same keyring) the file is renamed aside (`<name>.other-install-<ts>`), a new journal is started with a `gap` line, nothing is re-applied, and the boot reports `revocation_journal_reset` (DATA4-15). An older build's in-place restore of a pre-S0 archive is not a mismatch: the marker-less store inherits the install's `install_id` from 05-storage's lineage file (§5S.4 step 5), so steps (2)–(4) re-apply the verified tail before the hooks (DATA5-13). An **older** build's factory reset, re-populated by that build, inherits the same way and is the stated residual of 05-storage §5S.11 item 8 (DATA6-10). **(1) head before** — read `head_before`, the store's `journal_head`, before anything is appended this boot; every comparison below uses `head_before` and the seq of the journal's last **revocation** line (`gap` and `restore` lines are bookkeeping and never count) (SEC4-5). **(2) classify** by store identity and content, never by head lag alone (DATA4-7, DATA5-7, SEC5-6, DATA6-4, DATA6-5). This is **the one classifier**: 06 P4d step 2, 05-storage §5S.4 step 11 (b) and 05-authn §4.14 cite it, and no other file adds a signal (R7-4, DATA7-10): a P0a restore boot is a restore boot, and none of the evidence below applies to it (R6-4); otherwise, with `backup.location.json` describing this store (row "Where"), the store was **replaced** outside the staged restore (an older build's in-place restore, a file copy of an old store, a hand-copied checkpoint) when **any** of these holds: (a) its `c/backup/served_boot` pair is older than the file's (row "Content marker"); (b) `head_before` is below the file's committed-head anchor (row "Head in the store"), which catches an in-place copy that kept the pair; (c) 05-storage's lineage verdict is **behind** (§5S.3.7); (d) `c/storage/epoch` names another generation or directory identity than the one recorded **and** the journal shows the store is not the current one — `head_before` is below the journal's last revocation line, or the journal holds a `gap` or `restore` line after `head_before` (a `restore` line with a `restore_aborted` line, or whose restore journal reads `swapped_back`, does not count, DATA6-3), or this boot owes the `journal` duty (the `gap` line it is about to append, read through `storage.Duties`), or the store has no head while the journal has revocation lines; a new generation or directory identity **without** such evidence is the current store **relocated** (a cross-device move, an rsync to a new disk, a volume migration, which `cmd/root.go:324-327` expects): the boot records the new identity in `backup.location.json` and runs no hooks; when generation and marker match, it is the same store, whatever its head. A non-journaling build's restore always leaves an owed `journal` duty, which counts in (d) on the very boot that first sees it, before its `gap` line exists, so it cannot pass for a relocation (DATA6-4). **(3) re-apply** — every verified line after `head_before` is re-applied through auth's and rbac's stores (for ABO, books' legacy implementations writing the legacy rows: the one stored format) in batches that also write the new head, **except a hook line of an aborted restore**: a line whose `RestoreSeq` names a `restore` line that a `restore_aborted` line closes, or whose restore journal reads `swapped_back`, is verified (it is part of the chain) but never re-applied, because the store it narrowed was swapped away (DATA8-7 (a)); a store with no head never committed a journaled revocation (it predates the journal, or is an older archive), so every verified line is re-applied. On the **same store** this is all: a lagging head (crash after `Append`, failed commit) re-applies the tail, narrowing and idempotent, and runs no hooks. **(4) replaced store (DATA3-8)** — step 3 runs the hooks with `KindExternal` (`Previous` nil, so no audit copy-forward; `Revocations` **nil**, since nothing anchors the journal's end once the store that held the head is gone: every key needs confirmation, SEC4-6) and a `restore` line `{kind: external, boundary: head_before}` is appended. **(5) bookkeeping lines** — only now are the due `gap` line (row "Boots without the journal duty") and, on a restore boot, the `restore` line (unless the journal already holds one for this restore) appended, each with its head update in its own batch; then, **except on a restore boot**, the boot writes `c/backup/served_boot` and then `backup.location.json` with this store's generation, that pair and the committed head; a restore boot writes them only after its hooks commit (§5B.3.4 step h, DATA6-3). A `restore` line without its `c/backup/restore_hooked/<seq>` record is resumed by the next boot, never duplicated — unless a `restore_aborted` line names it or its restore journal reads `swapped_back`: then it is neither resumed nor evidence (DATA6-3). A store replaced by a build that does not journal, with no revocation after the archive, is detected by its generation although its head equals the journal's end (DATA4-7). **Chain break** (a missing, edited or unverifiable line): when `head_before` is below the last revocation line, re-application cannot know what it skipped, so the boot fails closed through auth's restore fallback — every API key `needs_confirmation`, every credential epoch bumped — and reports `revocation_journal_broken`; with the head at the end, only the health report (SEC3-10). |
| Coverage at restore | `RestoreContext.Boundary` = min(the source's recorded seq — the manifest's `JournalSeq`, the `staged` record of the restore being reverted, or the checkpoint record's `journal_seq` (05-storage §5S.3.12) — and the swapped-in store's own `journal_head.seq`). `Revocations` yields the lines after `Boundary` only when: the journal's `install_id` equals the archive's (a restore boot inherits the install's ID, 05-storage §5S.3.4 rule 4, so an old archive of this install still matches, DATA3-12); the chain verifies from `Boundary` to the **pre-swap store's** `journal_head` (the journal is not truncated below what the live store had committed); and no gap — a `gap` line in the journal (an append failure or a non-journaling build) or an open `c/backup/journal_gap/` record in the pre-swap store — lies after `Boundary`. Otherwise it is nil and 05-authn sets every API key `needs_confirmation` (§4.14 step 3). Foreign, host-loss and external restores (no pre-swap store, so nothing anchors the journal's end) always get nil. **Pre-swap restrictions win (SEC5-10, SEC6-4):** a demotion or revocation an older build made during a rollback, or one whose `Append` failed, or one in an interval that detection missed, is in no journal line but is in the pre-swap store; so **whenever `Previous` is non-nil** — not only when `Revocations` is nil; with full coverage the result equals the journal re-apply for journaled kinds — each owner's hook keeps the **most restrictive** of the restored and the pre-swap state through the read-only pre-swap store the kernel builds for it — rbac removes assignments absent there, intersects permissions and deletes roles deleted there (05-rbac §7.1), auth keeps a key revoked there revoked instead of merely `needs_confirmation` (05-authn §4.14) (`TestRestore_UnjournaledDemotionHeldByPreSwapStore`). A replaced store has no pre-swap store; that residual is 14 K22's. |
| Retention | Prune drops lines whose `seq` is at or below the smallest boundary any retained archive, undo point or unconsumed checkpoint records, by rewriting the journal (temp file + rename) with a new header whose `base_seq`/`base_mac` are the last dropped line's, so the chain continues. Every `kid` that a retained line or the header carries stays in the derive-key in-use set (§5B.3.3), so retirement never breaks verification (DATA3-13). |

### 5B.3.7 Provenance (SEC-16)

Provenance, not encryption, decides whether an archive is trusted (and once encryption is built, age
gives confidentiality, not authenticity: recipients are public, so anyone could make an archive that
"decrypts" here). The manifest's `Provenance.MAC` is HMAC-SHA256 over the canonical manifest bytes (with
the MAC field empty) under `DeriveKeyFor(Provenance.KeyID, "core.backup/manifest/v1")` of this install's
keyring (05-config; the derivation key survives data-key rotation, and its own rotation leaves older
keys verify-only until retired, SEC2-24). The MAC'd bytes include `install_id`, `key_id`, every entry
hash of both archives, the levels and the journal and audit boundaries, so neither archive can be
swapped or edited. An archive is `Own` when the MAC verifies **and** `install_id` equals the live
store's `c/storage/install` (or no live store exists, the host-loss case once the exported keyring is in
place). Restoring a pre-S0 archive does not change the install's ID (the restore boot writes the
inherited one, DATA3-12), so this install's later `Own` archives stay `Own`. Everything else — including every `legacy-abo/0` archive and every archive whose `key_id` is
retired — is restored only as a foreign restore (B5).

### 5B.3.8 Routes (SEC2-8)

All under `/api/core/v1`, registered by `core/internal/backuphttp`; the legacy `/api/v1/backup/*` paths
are aliases for the 09 window (§5B.10).

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

Archive encryption lands with the first app or cutover that can activate it (03 §3.4; SIMP4-1,
SIMP4-5): during the extraction books' legacy restore bound `{core, 0}` keeps it inactive for every ABO
store, and no other consumer exists. Per 03 §3.4 the first column holds only package-qualified Go
identifiers, which `archtest.DeferredMembersAbsent` checks are absent from non-test `core/` files; every
other item is in the second column, unchecked (COR5-16, SIMP5-6). The PR that builds one deletes its entry. It lands additively: an absent `encrypted` field
reads as plaintext.

| Member | Also deferred (not checked) |
|---|---|
| `backup.LegacyRestoreBound`, `backup.Manifest.Encrypted`, `backup.Manifest.Recipients`, `backup.Archive.Encrypted`, `backup.Service.ExportPlain`, `backup.ErrDecrypt` | B3; the CLI `<app> backup export-plain`, the offline restore's `--identity`, and the re-encryption behaviour of the built `backup.Service.Rewrap` (a restriction on a built member, which an absence test cannot check; SIMP5-6) |
| — | the age writer and reader in `core/internal/backuparchive`, the `.age`/`.extra.age` variant and the age-header sniff; `filippo.io/age` stays out of `go.mod` until then |
| — | config keys `core.backup.encryption`, `core.backup.recipients`; health token `backup_encryption_pending`; metric `mediacore_backup_encryption_active` and the `encrypted` label of `mediacore_backup_archives` |
| — | the plaintext-archive retention floor (DATA-22's floor 2) and TLS material (`certs/`) in archives: both only mean something once encrypted archives exist |
| — | tests `TestEncryptionInactiveUntilExportAndBound`, `TestEncryptedArchiveNeedsIdentity`, `TestUpgradeDoesNotSwitchToUnrecoverableArchives`, `TestRewrapLegacyArchive` |

**Contract at proof depth** (B3; DATA-22, SIMP-9, SIMP2-2). `filippo.io/age` (BSD-3-Clause; latest stable recorded in
[13-toolchain](13-toolchain.md)). Recipients: (1) the **install archive identity**, an X25519 identity
whose scalar is the keyring's derivation output for info `core.backup/age-identity/v1` (05-config §2.4,
through the `core.backup.` secrets scope) — **not a file**: there is no identity file, no identity
export and no second "not exported" health reason, because the keyring export recovers it; (2) zero or
more **operator recipients** (`core.backup.recipients`, public keys only), whose identities never touch
the server and which an operator adds to separate archive decryption from secret access. Encryption is
**active** only when all hold:

1. `core.backup.encryption = age`. Default: `none` on an install whose store existed before its first
   core boot (an upgrade) and on every install of an app that declares a legacy restore bound which the
   store has not passed (rule 3); `age` otherwise.
2. 05-config's export record `<state_dir>/secrets.keyring.exported` covers the **active derivation key**
   (`secrets_keyring_not_exported` is not raised for it), **or** at least one operator recipient is
   configured.
3. The **legacy restore bound** is passed: an app with a pre-core history declares
   `backup.LegacyRestoreBound{Owner, Level}`; while the store's level for that owner is ≤ the bound,
   some build that can still open the store has only ABO's plaintext restore code, so encryption stays
   inactive. Books declares `{Owner: core, Level: 0}`: no step of the extraction raises the `core`
   level (R3-1, 05-storage §5S.3.7), so every earlier ABO release, HEAD included, can open the store —
   upgraded and fresh alike, because a fresh ABO store keeps its families in the legacy format too — and
   ABO archives stay plaintext for the whole extraction and until the first level step past the bound,
   which is the first per-family cutover (05-storage §5S.3.13; 09 §9.13), since no extraction step may
   raise a level (§5B.11 item 4). Apps without a pre-core history
   declare none; their fresh stores are stamped at the build's level (05-storage §5S.4 step 5) and
   encryption is active as soon as rule 2 holds.

While `age` is configured but not active, archives are written unencrypted, secret-class items are
omitted (the config file as its redacted copy), and health is `degraded` with token
`backup_encryption_pending` and the reason (`keyring_not_exported`, `legacy_restore_bound`). Encrypted
archives are never created during a window in which a rollback target could not restore them.

## 5B.4 Lifecycle

Phases are 06 §6.5.1's (R2-8).

- Construction: `backup/journal` is opened by the kernel from S4 at P4d step 2 — after step 1 opened
  the keyring (`secrets.Open`) and attached the DB overrides, so its file is resolved from the effective
  `core.backup.dir`, which the kernel's core-section list declares from S4 (COR4-10, COR5-6), and every line verifies (COR3-2,
  DATA3-5) — and handed to `auth` and `rbac`;
  `backup` itself is built in P5 after storage and secrets, before modules, and then decides derive-key
  retirement by calling `secrets.RetireUnused` with the in-use set of §5B.3.3 (archives, undo points,
  checkpoints and journal lines of the effective directory; COR3-2, DATA3-13). Module artifacts
  registered through `Registrar.Backup` ([06](06-app-sdk.md)) are validated in P7 (unique names,
  required `Consistency`).
- Boot: the pending-restore swap, or the resumption of an in-flight one, is P0a (§5B.3.4; core code
  from S2b), after the updater's P−1; storage then opens at the recorded DB path. At P4d step 2 the
  journal is located (and moved if needed), its header's `install_id` checked against
  `c/storage/install`, `head_before` read, the boot classified by content marker, committed-head anchor, storage lineage and
  store generation with journal evidence (replaced, relocated or the same store; a restore boot is none of these), the tail re-applied, and only then the gap and `restore` lines appended (§5B.3.6); at step 3 the restore hooks
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
| Stale or foreign pending intent at boot (no swap started): expired, another binary, or the open stamp moved | open for the app (boot on live data), closed for the restore (rejected) | OPS-14: no surprise restore after a rollback and roll-forward, also when the rollback's override let an older build serve in between (SIMP8-1); after the updater's rollback, `.prev` rejects the failing build's intent (OPS2-3). |
| A second staging (any kind) while the slot is taken or a swap is in flight | closed (`storage.ErrSwapPending`), nothing staged | One slot, created atomically, across processes (R8-1). |
| Storage opens at a path other than the recorded `db_path` | closed for the restore: automatic swap-back | The swap must never be applied to one path and served from another (DATA2-25). |
| Boot-time staging re-check fails | open for the app (boot on live data), closed for the restore (rejected) | Live data is untouched; refusing to boot would turn a bad archive into an outage. |
| Archive below the build's upgrade floor | closed at staging (`ErrUpgradeFloor`, names the release to install first), nothing extracted | It would be swapped in and refused at P2; on a fresh host it would leave an empty store (DATA3-9). |
| Any failure after the swap (guard, migration, hook) | closed for the restore: automatic swap-back (checkpoints of the restore boot moved aside), exit, restart on the original data with health `degraded`; with no previous store, storage refuses to create one | DATA-23b: never leave the app down with live data parked in `previous/`; never reuse a checkpoint of the restored store (DATA3-1, DATA3-9). |
| Crash between the swap and the hooks | resumed: `inflight.json` keeps it a restore boot, hooks re-run (idempotent); rollback tooling keeps refusing | Dropping the hooks would serve revoked credentials (DATA3-6). |
| Swap-back fails | closed (boot halts; journal names every path; offline `restore --revert`) | Manual recovery with exact instructions beats guessing. |
| Crash during the swap | resume from journal | Each step idempotent. |
| Post-restore hook fails | handled by the swap-back row | Resurrecting revoked credentials is a security failure (05-authn §4.14). |
| A `KindCheckpoint` restore boot's migration DAG fails | closed: no swap-back; the restored checkpoint is kept, the journal reads `failed` with the failing binary's ID, exit non-zero; `CheckReader` admits another candidate that implements the journaled swap and whose levels and floors admit the store, and its P0a resumes the journal; the failing binary refuses while the journal is in flight | Swapping back would only bring back the store whose migration failed (DATA7-12); a candidate without the swap would ignore the journal and leave it to surprise a later build (DATA8-6). |
| Revocation journal append fails | open (revocation commits; gap recorded; health `degraded`) | Never block a revocation; the gap makes a later restore fail safe (keys need confirmation). |
| Revocation journal chain broken or truncated below the live store's head | open for the app (health `degraded`, `revocation_journal_broken`); closed for coverage (`Revocations` nil → keys need confirmation); when the store's head is below the journal's end, closed for credentials too (every key `needs_confirmation`, every epoch bumped) | A tampered journal must never make a restore look covered, nor leave skipped revocations unapplied (SEC2-9, SEC3-10). |
| A boot without the journal duty served the store (rollback, pre-guard open) | open (the next journaling boot appends a `gap` line) | Later restores across that interval fail safe (R3-1). |
| Store replaced outside the staged restore (no P0a restore; an older content marker, a head below the committed-head anchor, a behind storage lineage, or another store generation with journal evidence that it is not the current store) | open: tail re-applied, hooks run with `KindExternal`, `Revocations` nil, keys need confirmation | An older build's restore or a file copy must not keep revoked credentials or old sessions (DATA3-8, DATA4-7, SEC5-6). |
| Current store relocated (another generation or directory identity, same content marker, no journal evidence) | open: the new identity recorded, no hooks | A routine move of the DB to another disk or pool lost nothing; running the replaced-store hooks would lock out every key and sign everyone out (DATA5-7). |
| Same store, head behind the journal's last revocation line (crash after `Append`, a failed commit, out-of-order commits on an older build) | open: tail re-applied, no hooks | Re-applying narrows and is idempotent; treating it as a replaced store would lock out every key (DATA4-7). |
| Journal header names another install (a core build's factory reset, 05-storage §5S.3.15, or a deleted DB directory) | open: journal set aside, new journal with a `gap` line, nothing re-applied; health `revocation_journal_reset` | Another install's revocations must never be replayed onto this one (DATA4-15, DATA6-10). |
| An **older** build's factory reset, re-populated by that build (residual) | open: the marker-less store inherits the install's `install_id` from the lineage file, so the old journal is re-applied | Core holds no app key name that would tell it apart from an in-place restore of a pre-S0 archive; re-applied revocations only narrow rows (05-storage §5S.11 item 8, DATA6-10). |
| A restore whose hooks fail and swap back | open for the app: the original store boots as the same store, no hooks, no key needs confirmation, no epoch bumped; the restore line is closed by `restore_aborted`, the hook lines it wrote are never re-applied, and `backup.location.json`, `storage.boot.json` and the journal's location are as before | The failed restore's marker, location record, lineage file, journal location and hook lines must never make the original data read as replaced, as an unfinished restore, or as revoked (DATA6-3, DATA8-7). |
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
| `core.backup.dir` | path | `backups` beside the DB (relative values resolve against the DB's parent, `backup.ResolveDir` semantics, `backup.go:120-146`; ABO: `backup_dir`) | no: restart-bound; the next boot moves the revocation journal (§5B.3.6, DATA3-5); declared from S4 in the kernel's core-section list, adopted by `backup` at S8 (COR4-10, COR5-6) | admin |
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
`core.storage.swap.keep_previous` (S2b), which this package reuses for every kind; the former
`core.backup.restore.intent_ttl` and `core.backup.restore.keep_previous` are deleted (SIMP8-1).
ABO's aliases (`create_backups`, `backup_*`; §5B.10, 09) target these keys. There is no switch to skip
the post-restore credential hook: 05-authn always runs it.

## 5B.8 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_backup_runs_total` | counter | `reason`, `outcome` |
| `mediacore_backup_duration_seconds` | histogram | `phase` (checkpoint, archive, checksum, verify) |
| `mediacore_backup_bytes` | gauge | — (size of newest archive, main + companion) |
| `mediacore_backup_last_success_timestamp_seconds` | gauge | — |
| `mediacore_backup_last_verified_timestamp_seconds` | gauge | — |
| `mediacore_backup_archives` | gauge | `format`, `provenance` |
| `mediacore_backup_retained_bytes` | gauge | — |
| `mediacore_backup_verify_total` | counter | `level`, `outcome` |
| `mediacore_backup_restore_total` | counter | `stage` (staged, applied, rejected, swapped_back, reverted), `foreign` |
| `mediacore_backup_revocation_journal_gaps` | gauge | — |
| `mediacore_backup_revocation_reapplied_total` | counter | `kind`, `cause` (boot, restore) |

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
| `TestStaleOrForeignIntentRejected` | expired intent, other build, and a moved open stamp (an override let an older build open the store between staging and the roll-forward; SIMP8-1); a journal past `previous_moved` is resumed or swapped back, never rejected |
| `TestRestoreRejectsMismatchedDBPath` | P0a swaps the recorded absolute targets; storage opening elsewhere swaps back (DATA2-25) |
| `TestUpdaterRollbackRejectsFailingBuildsIntent` | P−1 rolls back with a pending intent staged by the failing build; `.prev` rejects it at step a (OPS2-3) |
| `TestRenamePrecheckRefusesBindMountedConfig` | config not swapped and staged for the operator; DB still restorable |
| `TestRestoreFailureSwapsBack` | guard refusal, failing migration and failing hook each swap back and boot the original data; the next boot reads the original as the same store (lineage **same**: `storage.boot.json` restored), runs no hook, no key needs confirmation, the restore line has a `restore_aborted` line and is not resumed, `served_boot`/`backup.location.json` still describe the original, and the committed-head anchor equals the original store's head; with rbac's hook failing after auth's committed, no epoch is bumped, no key needs confirmation and the journal is where it was (DATA6-3, DATA7-5, DATA8-7) |
| `TestRestoreBootFailingAfterHooksIsNotReplaced` | a restore boot that fails at P6, after journal `applied` and before Run step (3b): the next boot reads the lineage **same**, runs no hook and asks no key confirmation (R7-4, DATA7-4) |
| `TestLocationFileWritersSerialized` | `Create` and the anchor's tick, `Append` and shutdown rewrite `backup.location.json` through the journal component's one mutex: no lost update reverts Create's pair (DATA7-14) |
| `TestRestoreCheckpointThenRollback` / `TestCheckpointRestoreBootFailingStepKeepsCheckpoint` | an open checkpoint restored offline is finished to `applied` without hooks and `make rollback` then passes; a completed one (`--accept-loss`) passes `CheckReader` at `swapped` for an admitting candidate; a checkpoint restore boot whose DAG fails keeps the checkpoint, journals `failed` with its binary ID and exits non-zero; `CheckReader` and the script agree that a `failed` journal admits a fixed or previous core build (which resumes it) and refuses a pre-S2b candidate and the failing build (DATA7-12, DATA8-6) |
| `TestOpenCheckpointRestoreThenRollbackIsNotForeign` | an open checkpoint restored offline, then `make rollback`: `.prev`'s boot reads no foreign open and owes no duty (no `gap` line, no session revoked, no W restart), because the tool's close batch promoted the step-e identities (DATA8-5) |
| `TestSwapSlotExclusive` | two concurrent stagers of any kinds (archive, revert, reset), in one process and in two: exactly one creates `restore/pending.json`, the other gets `storage.ErrSwapPending`; staging while a swap is in flight gets it too (R8-1, COR8-6) |
| `TestStagedRestoreKeepsCoverage` / `TestCheckpointRestoreAppendsNoGap` | an archive restore over an intact journal: no `gap` line for the swap, `Revocations` non-nil, only keys revoked after the archive are revoked, no key needs confirmation; the same for a revert and a `storage restore-checkpoint` (R6-4, DATA6-2) |
| `TestRevertLastRestore` / `TestOfflineRevert` | undo point round trip, with and without a booting app |
| `TestRestoreOlderArchiveRunsMigrations` | older versions migrate forward after the swap |
| `TestRestoreRefusesTooNewArchive` | level, Pebble format, Breaking step, wrong app |
| `TestRestoreRewritesSidecarsFromStamps` | `.storage-levels`, `.storage-applied` and `.storage-format` match the restored store right after the swap (DATA4-17) |
| `TestPostRestoreReappliesRevocations` | key revoked after the archive stays revoked; journal gap → keys need confirmation |
| `TestRestoreHooksWriteLegacyRows` | hook writes run after the journal re-apply as writes through the subsystems' stores (for ABO, legacy rows by books' legacy implementations); after a restart and after a rollback to the previous release the re-revoked key, a restored legacy session and a restored legacy invite stay refused (R3-1, R4-1, SEC2-3, DATA2-4) |
| `TestCrashBetweenSwapAndHooksResumesHooks` | `CrashClone` at P2, P4a, P4b and each P4d step after step g: the next boot is still a restore boot (`inflight.json`), the hooks run once to completion, `CheckReader` refuses until `applied` (DATA3-6) |
| `TestJournalReapplyVerifiesWithKeyring` / `TestJournalReapplyUsesOverriddenBackupDir` / `TestJournalReapplyWithUISetBackupDir` | with `core.backup.dir` set only in Settings, P4d step 2 reads (or first moves) the journal from that directory and verifies every line with the keyring opened at step 1 (COR3-2, DATA3-5) |
| `TestRestoreBelowUpgradeFloorRefusedAtStaging` | an archive below the build's upgrade floor is refused before extraction; a failed host-loss restore leaves no fresh store (DATA3-9) |
| `TestHostLossRestorePreS3Archive` | a pre-S0/pre-S3 archive on a fresh host keeps the install's `install_id`; `KeyIDsReferenced` reports `k0-legacy`; a keyring without it is refused unless accepted (DATA3-12) |
| `TestRotateDerive_JournalStaysVerifiable` (05-config §2.9 owns it; this file adds the journal-chain cell) | after `rotate-derive`, rewrap and `retire_after`, a key that signs a retained journal line is not retired and the chain still verifies (DATA3-13, SEC3-10) |
| `TestBootReapply_ChainBreakFailsClosed` | a broken chain with the store's head below the journal's end sets every key `needs_confirmation` and bumps every epoch (SEC3-10) |
| `TestNonJournalingBootRecordsGap` | a boot by a build without the `journal` duty, or a foreign open, makes the next journaling boot append a `gap` line; a later restore across it is uncovered (R3-1) |
| `TestReplacedStoreRunsHooks` | an older archive copied into place (or restored in place by an older build) is detected at boot by its content marker or its store generation with journal evidence, also when its head equals the journal's end: tail re-applied, hooks run once with `KindExternal` and `Revocations` nil, keys need confirmation (DATA3-8, DATA4-7, SEC4-6) |
| `TestRelocatedCurrentStoreRunsNoHooks` / `TestRelocatedAfterRollbackIsReplaced` | the current store moved to another directory or device: new identity recorded, no hooks, no key needs confirmation; the same move after a rollback whose interval left a `gap` line is classified replaced (DATA5-7) |
| `TestOlderBuildInPlaceRestore_SameDirDetected` | archive on the current build, revoke on the current build, roll back, restore that archive in place with the previous build, roll forward: the head is below the anchor (from S8 also the content marker is older; before S8 the archive is ABO's, DATA7-16), the store is replaced, the journaled revocation is re-applied, keys need confirmation, legacy sessions are deleted — also when the archive was taken during the last journaling boot, with no boot in between (SEC5-6, DATA6-4; an archive the older build itself took after the last journaling boot is the stated residual) |
| `TestInPlaceRestoreOfLastNightsCoreArchive_Replaced` | `Create` writes P′ after its checkpoint; the archive carries P; an in-place restore of it reads replaced; a crash between the store write and the file write reads the same store (SEC6-6) |
| `TestCommittedHeadAnchorDetectsInPlaceCopy` | a DB directory copied file by file over the live one (same directory identity, same marker) after a later revocation reads replaced because `head_before` is below the anchor; the anchor never leads the store (a revocation whose commit failed does not move it) (DATA6-4) |
| `TestTwoStoresShareStateDir` | two stores with one state directory: neither reads the other's marker, anchor or generation; each boot rewrites the file for its own store and runs no hook (SEC6-8) |
| `TestRevocationStagesJournalHeadAtS4` | at S4 r2, before `backup` exists, a key revoke commits with `c/backup/journal_head` staged through `GetForUpdate`, and an append failure commits with its `journal_gap/` record; the kernel registers `journal.StorageSpec()` and declares `core.backup.dir` (COR5-6) |
| `TestRestore_UnjournaledDemotionHeldByPreSwapStore` | with a pre-swap store, whether coverage is nil or not (SEC6-4), a demotion and a key revocation made on an older build (no journal line) survive a staged restore of an earlier archive (with 05-rbac and 05-authn, SEC5-10) |
| `TestFailedBootKeepsGapOnce` | a roll-forward boot that fails after P4d step 2 and the next boot append one `gap` line for the interval, not two, and the owed entry is cleared only by the batch that appended it (COR5-3, DATA5-6) |
| `TestStagedByBinaryID` | an intent staged by one API-only binary is rejected by another binary of the same commit (`StagedByBuild` is the binary ID, FE5-3) |
| `TestConcurrentRevocationsHeadMonotone` (`-race`) | two revocations committing out of append order leave the head at the larger seq (DATA4-7) |
| `TestCrashAfterAppendNoExternalHooks` | a crash between a line's fsync and its commit: the next boot re-applies the line and runs no hooks (DATA4-7) |
| `TestNonJournalingRestoreDetectedByGeneration` | a restore by a non-journaling build with no later revocation is still detected (DATA4-7) |
| `TestRollbackWithoutRestore_NoKeyConfirmation` / `TestOlderBuildRestore_DetectedDespiteGapLine` | a rollback and return with no restore runs no hooks and confirms no key; an older build's restore is detected although the gap line is appended this boot (SEC4-5) |
| `TestJournalOfOtherInstallNotReplayed` | after a core build's factory reset (05-storage §5S.3.15) or a deleted DB directory, the old install's journal is set aside, a new one starts with a `gap` line, nothing is re-applied (DATA4-15, DATA6-10) |
| `TestDarkReleaseRecordsJournalGap` | an S4 r1 build lists no `journal` duty; the next r2 boot appends a `gap` line covering it (COR4-4) |
| `TestRestoreSwapInCoreFromS2b` | at S2b: a restore whose boot fails at P4b swaps back; a crash after the swap resumes the hooks once; `inflight.json` is gone after the hooks; `storage restore-checkpoint` works; ABO's staging takes its floor from books' manifest and creates the intent in the one slot (DATA4-6, COR7-1, SIMP8-4) |
| `TestStagingTrialDecryptsLegacySecrets` | through `LegacyStore.Check`, a foreign archive whose legacy secret copies were encrypted under another key file is refused at staging with `ErrUnreadableSecrets` unless accepted (DATA4-22; 05-config `TestLegacyCheck_TrialDecryptForeignArchive`) |
| `TestSwapBackMovesRestoreCheckpoints` | after a failed restore swaps back, no checkpoint the restored store took remains reusable (DATA3-1) |
| `TestRevocationCommittedAfterCheckpointReapplied` | appended before, committed after the checkpoint: re-applied by seq, whatever the clocks say (DATA2-7) |
| `TestRevocationJournal_TruncationDetected` | deleted, edited, reordered or truncated lines break coverage → keys need confirmation (SEC2-9) |
| `TestOlderBuildRestoreReappliedAtNextBoot` | a restore performed in place by the previous release after a rollback is detected by the next boot, which re-applies the journal tail (DATA2-5, DATA3-8) |
| `TestRevertBoundaryFromStagedRecord` | a revert and a checkpoint restore re-apply the lines after their recorded boundary (DATA2-25) |
| `TestRestore_DemotedAdminStaysDemoted` | `role.unassigned` / `role.narrowed` / `role.deleted` re-applied (with 05-rbac, SEC2-10) |
| `TestRestore_PostArchivePasswordChangeNotReverted` | `password.changed` → reset required; `identity.unlinked` link removed (with 05-authn, SEC2-11) |
| `TestRestoreKeepsAuditTrail` | audit records above the manifest's high-water mark and the restore records survive, no seq collision (with 05-rbac, DATA2-16) |
| `TestForeignArchiveNeedsFlag` | a planted archive (valid format, no valid MAC) is refused without `Foreign` |
| `TestProvenanceBindsInstallAndEntries` | edited entry, swapped companion, other install ID → foreign |
| `TestPreviousBuildRestoresMainArchive` | the HEAD build's restore code restores a `mediacore-backup/1` main archive and ignores companion and sidecars |
| `TestPreviousBuildNeverListsCompanion` | HEAD listing and pruning skip `.extra` and `.manifest.json` |
| `TestRewrapBeforeDeriveKeyRetired` | after `rotate-derive`, the verify task re-MACs archives under the `verify` key; a retired key's archive verifies as foreign (SEC2-24) |
| `TestNoKeyMaterialInArchives` | keyring never written; no TLS material or other secret-class item in a plaintext archive |
| `TestPlainArchiveHasNoConfigSecrets` | with `ConfigFileHasSecrets()`, a plaintext archive carries the redacted file and `config_redacted`; `config.yaml` beside the DB is never archived (DATA2-17) |
| `TestUnreadableSecretsRefused` | keyring missing a referenced key → refused unless accepted |
| `TestCatalogueSurvivesRestore` | archives newer than the restored one stay listed and protected |
| `TestCatalogueDropsOrphans` / `TestJournalNamePerApp` | sidecars of a main archive an older build pruned are removed; two apps in one directory keep separate journals (DATA2-27) |
| `TestBackupRoutesPermissions` / `TestBackupDownload_SystemManageOnly403` | the §5B.3.8 table: permission, guards and audit per route; a `core:system.manage` holder without `core:admin` gets 403 on download, and a view-class scope 403 on verify (SEC2-8, SEC3-7, SEC3-12) |
| `TestManifestMatchesEntries` | tampered entry → `ErrManifest` |
| `TestVerifyOpenCountsKeys` | read-only open of the checkpoint matches manifest counts |
| `TestCheckpointCrossDeviceRefused` | no silent copy |
| `TestRetentionFloors` | newest verified; restore-referenced |
| `TestRetentionRunsBeforeSpaceCheck` | stuck-disk regression from 2026-08-29 |
| `TestLegacyABOArchiveRestores` | gzip/zstd/none, with and without sidecar, entry root `audiobooks.pebble/`, foreign confirmation |
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
| `RestoreBackupIn` / `extractArchive` | legacy reader for format `legacy-abo/0`, used only by `StageRestore` into staging |
| `ListBackups`, `newestBackupAge` (`organizer/service.go:567`) | `Service.List` (directory-derived catalogue) and `Ensurer.Ensure` |
| `handlers/system` backup routes (`/api/v1/backup/*`) | core routes `/api/core/v1/backups[/{id}[/verify|/restore]]`; legacy paths aliased for the 09 compatibility window, `POST /api/v1/backup/restore` re-implemented as stage-restore (it can no longer extract into an arbitrary `target_path`) |
| organizer auto-backup | `Deps.Backup.Ensure(ctx, 6h, "pre:books.organize")` |
| config `backup_dir`, `backup_max_total_bytes`, `backup_compression`, `backup_compression_level`, `create_backups` | `core.backup.*` aliases (09) |

First run on an upgraded ABO install: existing `audiobooks_*` archives appear as `legacy-abo/0`
(pre-provenance); the first `core.backup.create` writes a plaintext `mediacore-backup/1` main archive
under the legacy name pattern, which the previous build lists and restores; nothing is deleted except by
retention. The staged restore ships with ABO's `internal/backup` at S1 (09), its journaled swap moves into
core at S2b, both long before this package; nothing in the extraction deletes data a restore would need
(R3-1: there are no contracts).

## 5B.11 Open items

1. Whether `Import` (uploading an archive over HTTP) needs a size cap and streaming verification
   before it is stored — default cap = `retention.max_total_bytes`.
2. TLS material under `certs/` may be ACME-managed (re-creatable) or operator-provided; class decided
   with [05-http-api](05-http-api.md).
3. Multi-app in one binary: one archive per install (one DB); `Manifest.App` becomes a list then. A
   merge of two installs' stores is not an archive operation (05-storage open item 6, DATA-30). Not
   built until that mode exists (lowest priority).
4. (Closed; SIMP4-5.) Encrypted archives: not built during the extraction (§5B.3.9). They land with the
   first app without a pre-core history that ships backups, or with the first per-family cutover (the
   level step past books' legacy restore bound). Until then the stated cost is plaintext archives (whose
   secrets are ciphertext in the DB and whose config file is redacted).

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
