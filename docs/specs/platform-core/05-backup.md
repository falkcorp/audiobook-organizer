<!-- file: docs/specs/platform-core/05-backup.md -->
<!-- version: 1.2.1 -->
<!-- guid: 604febb0-a09e-4cf9-99ee-5f3722534da6 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Backup and restore (`backup`)

Evidence commit `748fd787a`; every `path:line` below is at that commit. Raw inputs:
[`raw/05-storage.md`](../../plans/platform-core/raw/05-storage.md) §5 (backup and restore) and §1
(engines), [`raw/12-cross-cutting.md`](../../plans/platform-core/raw/12-cross-cutting.md) §6,
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §7 (secret storage).
Storage terms (`storage.Admin`, keyspaces, levels, checkpoints, mirrors, derived indexes, generations,
auxiliary DBs) are defined in [05-storage](05-storage.md); the secrets keyring, its derivation key and
the boot record in [05-config](05-config.md); the post-restore credential hook in
[05-authn](05-authn.md) §4.14; role re-application and audit copy-forward in [05-rbac](05-rbac.md) §7.1.
`backup` is **L4** (R4) and lands at 09 S8. The revocation-journal and restore-hook types live in the
leaf package `backup/journal` (L4; imports `entity` and `storage` only), which lands at S4 with `auth`
and `rbac`, its first writers (COR2-9). Boot order is [06](06-app-sdk.md) §6.5.1's one phase table
(R2-8): this file's pre-boot swap is P0a, its journal re-apply and restore hooks are P4d steps 2 and 5.
This file owns the **one archive format** (cross-cutting resolution 8); [09](09-abo-on-core.md) S8
references it and decides only when each step ships.

## 5B.0 Decisions in one table

| # | Decision |
|---|---|
| B1 | **One archive format, `mediacore-backup/1`.** A **main archive** in ABO's legacy layout — the Pebble checkpoint rooted at the DB directory's basename, plus one root entry `mediacore-manifest.json` — that every ABO build, pre-core ones included, can list and restore; and a **companion archive** (`.extra`) for everything else (config file, boot record, module artifacts, optional derived and re-creatable stores), which older builds never list. **No archive ever contains the secrets keyring**, and an unencrypted archive never contains a secret: the operator config file goes in as class `Secret` when it holds one, and only a redacted copy goes into a plaintext archive (DATA2-17). |
| B2 | **Restore is staged and applied in the kernel pre-boot phase** (06 P0a), after the updater's own pre-boot check (P−1) and before config is read and storage opens. The API verifies, pre-validates every rename and extracts into a staging directory on the DB's filesystem; the kernel swaps by rename using the absolute targets recorded at staging and keeps the previous data as an undo point. **Any failure after the swap swaps back automatically.** No code path writes into a live store. |
| B3 | **Encryption is opt-in on upgrade** (age). The install's archive identity is **derived from the one keyring** (05-config), so there is no second key artifact and no second export (SIMP2-2). Upgraded installs default to unencrypted; fresh installs default to encrypted. Encryption becomes **active** only once the keyring export covers the active derivation key (or an operator recipient is configured), and only when no build that can still serve the store lacks the code to restore an encrypted archive. Secrets inside the DB are ciphertext either way. |
| B4 | Verification is part of creation (decrypt, per-entry SHA-256, open the checkpoint read-only, compare key counts) and runs again on a schedule. Retention never deletes the newest **verified** archive, and never deletes the newest verified **plaintext** archive until an encrypted archive is verified **and** the keyring export covers its derivation key. |
| B5 | **Provenance.** The manifest carries an HMAC bound to the install (keyring-derived key named by its key ID, install ID in the MAC'd bytes). An archive without a valid MAC — imported, planted, from another install, or from before provenance existed — is **foreign**: restoring it needs an explicit `Foreign` request, `core:admin` and an interactive owner proof, and is audited. |
| B6 | **What must outlive a restore lives outside the restored DB**: the archive catalogue is derived from sidecar files in the backup directory; the restore journal and the **revocation journal** are files. The revocation journal is hash-chained with a keyring-derived MAC and anchored by a head record inside the store (SEC2-9); every build that can open an adopted store — the Teach build included — journals its revocations and restores (R2-1); every boot re-applies the journal tail the store lacks, and every revocation reaches the legacy row in the same batch as the native one (R2-2). |
| B7 | Old ABO archives (`audiobooks_<dbtype>_<ts>.tar[.gz|.zst]` + `.sha256`) stay listable, verifiable and restorable through the same staged path as format `legacy-abo/0` (pre-provenance, so restoring one is a foreign restore). |
| B8 | Create/verify/prune/stage-restore are core **tasks** (`core.backup.*`) with progress from real work, cancellation, one exclusive `Resource`, and schedules declared under backup's own namespace with type `tasks.ScheduleConfig` and bound through `tasks.ScheduleFromConfig` (SIMP-6, R2-7). |

## 5B.1 Purpose & scope

In scope: what is backed up, the archive format, encryption, provenance, creation (scheduled, on
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
| Level stamps and legacy projection | metadata | always (as manifest values) | manifest | read from the checkpoint | sidecars `.storage-levels` and `.storage-format` rewritten from the staged stamps |
| Operator config file (05-config) | `Secret` when 05-config's `ConfigFileHasSecrets()` is true, else `SystemOfRecord` (DATA2-17) | always | companion `config/<file>` — the file itself in an encrypted archive; in a plaintext archive, 05-config's `RedactedConfigFile()` and manifest flag `config_redacted` | file copy | staged; swapped only when `ReplaceConfig` and the rename pre-check pass, else left in `restore/<id>/config.restored` for the operator; a redacted copy is never swapped in, and restore warns that its secret values must come from env or be re-entered |
| Boot record `<state_dir>/config.boot.json` (05-config §1.3.5) | `SystemOfRecord` | always | companion `config/config.boot.json` | file copy | as the config file |
| ABO's plaintext mirror `<db parent>/config.yaml` | — | **never** | — | — | — (it duplicates secrets in clear, `persistence.go:32`) |
| Secrets keyring | — | **never** | — | — | recovered from 05-config's keyring export; restore checks that the keyring covers the archive's secret records (§5B.3.4) |
| TLS material (`certs/` beside the DB, ABO `appdirs`) | `Secret` | encrypted archives only, if present | companion `certs/…` | dir copy | staged |
| Module artifacts declared `SystemOfRecord` | `SystemOfRecord` | always | companion `artifacts/<owner>/<name>/…` | module `Snapshot` hook | module `Restore` hook into staging |
| Full-text indexes (`storage/index`) | `Derived` | off (`core.backup.include_derived`) | companion `index/…` | bleve online `CopyTo` (`bleve/v2 index.go:350-354`) | offered to the index only if its recorded generation matches the restored store's; else rebuilt |
| Derived snapshots (hnsw) | `Cache` | off | companion `derived/<name>/…` | `DerivedIndex.Snapshot` | `DerivedIndex.Load` with the generation rule; else rebuilt |
| Auxiliary DBs (Open Library mirror) | `Recreatable` | off (`core.backup.include_recreatable`) | companion `aux/…` | `Aux` checkpoint | staged |
| Revocation journal, restore journal | — | never inside an archive | backup directory (`<prefix>revocations.journal`) / `<state_dir>/restore/` | — | read by the restore, never replaced by it |
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
	Levels              map[string]int         `json:"levels"`         // per-owner reader levels in the checkpoint (05-storage §5S.3.7)
	LegacyStorageFormat int                    `json:"legacy_storage_format,omitempty"` // books' projection, 1 or LegacyMaxFormat+1
	PebbleFormat        uint64                 `json:"pebble_format"`
	Keyspaces           map[string]KSInfo      `json:"keyspaces"`      // owner -> version, approx keys/bytes
	Modules             map[string]string      `json:"modules"`        // module ID -> module version
	Entries             []Entry                `json:"entries"`        // main archive files: path, size, sha256, class
	Companion           []Entry                `json:"companion"`      // companion archive files
	Derived             map[string]DerivedInfo `json:"derived,omitempty"`
	ConfigRedacted      bool                   `json:"config_redacted,omitempty"` // the companion holds 05-config's RedactedConfigFile (DATA2-17)
	Encrypted           bool                   `json:"encrypted"`
	Recipients          []string               `json:"recipients"`     // age recipient fingerprints, never keys
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
	Encrypted  bool
	Provenance ProvenanceClass
	Manifest   *Manifest     // nil for legacy archives until first verification synthesises one
	Verified   *VerifyResult // last verification, from <main>.verify.json
}

type VerifyLevel uint8
const (
	VerifyInvalid VerifyLevel = iota
	VerifyChecksum // outer SHA-256 sidecars only (fast)
	VerifyEntries  // decrypt + every entry hash vs manifest, both archives
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
	Restart                 bool        // request a graceful restart once staged
}

type PendingRestore struct {
	ArchiveID     ArchiveID
	Kind          journal.RestoreKind
	StagedAt      time.Time
	StagedByBuild string    // version.Version + build ID; another build rejects the intent
	ExpiresAt     time.Time // StagedAt + core.backup.restore.intent_ttl
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
	Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error)    // the archive as stored (encrypted stays encrypted)
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
	Delete(ctx context.Context, id ArchiveID) error // refused for archives protected by a retention floor
	// Rewrap re-creates an Own archive under the ACTIVE derivation key: re-MAC,
	// and re-encrypt to the current identity while encryption is active (plain
	// or legacy -> encrypted). The original is deleted only if no floor protects it.
	Rewrap(ctx context.Context, id ArchiveID) (Archive, error)
	ExportPlain(ctx context.Context, id ArchiveID, dir string) error // CLI only: decrypt to the plain main + companion form
}

var (
	ErrBackupInProgress    = errors.New("backup: another backup or restore task holds the backup resource")
	ErrInsufficientSpace   = errors.New("backup: insufficient disk space")
	ErrCrossDevice         = errors.New("backup: checkpoint or restore staging is not on the database's filesystem")
	ErrChecksumMismatch    = errors.New("backup: archive does not match its recorded checksum")
	ErrNoChecksum          = errors.New("backup: archive has no checksum sidecar")
	ErrDecrypt             = errors.New("backup: no configured identity can decrypt this archive")
	ErrManifest            = errors.New("backup: manifest missing, invalid or inconsistent with entries")
	ErrForeignArchive      = errors.New("backup: archive provenance is not this install; restore requires Foreign")
	ErrBackupTooNew        = errors.New("backup: archive needs a newer build (reader level, Pebble format or Breaking step)")
	ErrWrongApp            = errors.New("backup: archive belongs to a different app")
	ErrRestorePending      = errors.New("backup: a restore is already staged")
	ErrRestoreStale        = errors.New("backup: the staged restore expired or was staged by another build")
	ErrRenameRefused       = errors.New("backup: a restore target cannot be swapped by rename (other filesystem, read-only, mount point)")
	ErrUnreadableSecrets   = errors.New("backup: the keyring lacks keys the archive's secret records need")
	ErrUnencryptedSecrets  = errors.New("backup: refusing to write secret material into an unencrypted archive")
	ErrProtectedByFloor    = errors.New("backup: archive is protected by a retention floor")
)
```

```go
// Package journal is the leaf of backup that auth and rbac import (COR2-9):
// github.com/falkcorp/mediacore/backup/journal, L4, imports entity and storage
// only, lands at 09 S4. It holds the out-of-DB revocation journal (§5B.3.6),
// its file-backed implementation, and the restore-hook contract.
package journal

import (
	"context"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
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
	Restore               Kind = "restore"                 // a swap replaced the store; Detail = [kind, archive or source id, boundary seq]
	Gap                   Kind = "gap"                     // closes an interval in which appends failed; Detail = [since]
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
	Source  string     // writing build: version.Version + build ID
}

// Head identifies the last line a store has seen: c/backup/journal_head.
type Head struct {
	Seq uint64
	MAC string
}

// RevocationJournal is what core/auth and core/rbac receive from the kernel.
type RevocationJournal interface {
	// Append writes r as one fsynced line BEFORE the caller commits the
	// revocation, and stages c/backup/journal_head = the new head into b by
	// participant binding (05-storage §5S.3.2). On error nothing was recorded
	// and nothing staged: the caller still commits the revocation and calls
	// Gap(b, at) — a disk problem never blocks a security action.
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}

type RestoreKind uint8
const (
	RestoreKindInvalid RestoreKind = iota
	KindArchive    // an archive
	KindRevert     // the undo point of the last restore
	KindCheckpoint // a pre-migration checkpoint (05-storage §5S.3.12)
)

// RestoreContext is what the kernel hands every restore hook at 06 P4d step 5.
type RestoreContext struct {
	ArchiveID string // backup.ArchiveID (journal cannot import backup); "" for revert and checkpoint restores
	Kind      RestoreKind
	Foreign   bool
	// Boundary is the journal seq the swapped-in store had seen (§5B.3.6):
	// min(the source's recorded journal seq, the swapped-in store's
	// journal_head). AuditHighWater is the manifest's (0 for revert/checkpoint).
	Boundary       uint64
	AuditHighWater uint64
	// Previous opens a read-only view of the pre-swap store (restore/<id>/previous/),
	// limited to the calling owner's keyspace and claims; nil when there was no live store.
	Previous func(owner storage.Owner) (storage.Reader, error)
	// Revocations yields the verified journal lines after Boundary; nil when
	// coverage fails (§5B.3.6): foreign, host-loss, broken chain, gap, other install.
	Revocations iter.Seq[Revocation]
	// Journal is the out-of-DB restore journal (staged, swapped, ... records).
	Journal []JournalRecord
	// Counts carries what earlier hooks returned (auth runs first), so audit's
	// hook can record core.restore.revoke with them.
	Counts map[string]int
}

// RestoreHook is the kernel's adapter over an owner's package function (auth's
// PostRestore, rbac's, audit's), bound to that owner's P4c handles. Hooks run
// in the order auth, rbac, audit; each returns per-family counts.
type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)

type JournalRecord struct {
	At    time.Time
	Step  string // "staged", "rechecked", "previous_moved", "staged_moved", "sidecars", "swapped", "opened", "hooks_done", "applied", "failed", "swapped_back", "rejected"
	Seq   uint64 // staged: the outgoing store's journal head seq, the recorded boundary of a later revert (DATA2-25)
	Actor string
	Note  string
}
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
    certs/...                                  encrypted archives only, if present
    artifacts/<owner>/<name>/...
    index/<module>/<name>.bleve/...            if IncludeDerived
    derived/<name>/...                         if IncludeDerived
    aux/<module>/<name>/...                    if IncludeRecreatable
Sidecars
  <main name>.manifest.json                    the manifest (MAC'd); source of the catalogue
  <main name>.verify.json                      last verification result {at, level, ok, detail}
Encrypted variant (only while encryption is active, B3)
  <main name>.age, <main name>.extra.age       each archive wrapped in age; sidecars stay plain (no secrets in them)
```

- **Why this layout** (DATA-21, OPS-14). An older build restores by extracting every entry under the
  target by name and lists only `audiobooks_` names with a `.tar.gz`/`.tar.zst`/`.tar` extension
  (`backup.go:777-870`, `codec.go:82-84,225-245`). The main archive therefore restores the DB exactly
  as today, the manifest lands as a harmless stray file, and the companion and sidecars match no
  listing rule, so an older build neither restores nor prunes them. During every compatibility window
  an operator who rolls back can restore any plaintext archive made since the upgrade.
- **Names.** ABO (the books module registers it) keeps the legacy pattern
  `audiobooks_pebble_<YYYYMMDD_HHMMSS><ext>` (`backup.go:333`); other apps use `<AppID>_<YYYYMMDD_HHMMSS><ext>`.
  The archive ID lives in the manifest. A name collision within one second waits for the next second.
  Only files with the app's registered prefix are ever listed or pruned (the prefix rule protects
  foreign files in a shared backup dir, `codec.go:225-245`).
- **Codecs** are sniffed by magic bytes on read (age header, zstd, gzip, plain tar), as ABO does for
  compression (`codec.go:204-222`); `core.backup.compression` picks the writer (default `zstd`; ABO keeps
  its current `gzip` until the operator changes it, so an upgrade never changes the codec silently).
- **No key material in any archive.** The secrets keyring stays in the state directory and is recovered
  from 05-config's `secrets export-keyring`; the archive's secrets stay ciphertext. 05-config's
  `KeyIDsInUse`/manifest coupling is not used: the manifest records no key IDs, and restore computes the
  key IDs the restored records reference from the restored data (§5B.3.4 step 5).

**Encryption** (B3; DATA-22, SIMP-9, SIMP2-2). `filippo.io/age` (BSD-3-Clause; latest stable recorded in
[13-toolchain](13-toolchain.md)). Recipients: (1) the **install archive identity**, an X25519 identity
whose scalar is the keyring's derivation output for info `core.backup/age-identity/v1` (05-config §2.4,
through the `core.backup.` secrets scope) — **not a file**: there is no identity file, no identity
export and no second "not exported" health reason, because the keyring export recovers it; (2) zero or
more **operator recipients** (`core.backup.recipients`, public keys only), whose identities never touch
the server and which an operator adds to separate archive decryption from secret access. Encryption is
**active** only when all hold:

1. `core.backup.encryption = age`. Default: `none` on an install whose store existed before its first
   core boot (an upgrade), `age` on a fresh install.
2. 05-config's export record `<state_dir>/secrets.keyring.exported` covers the **active derivation key**
   (`secrets_keyring_not_exported` is not raised for it), **or** at least one operator recipient is
   configured.
3. The **legacy restore bound** is passed: an app with a pre-core history declares
   `backup.LegacyRestoreBound{Owner, Level}` (books: `{Owner: core, Level: 5}`, 09 §9.2.2; contract C8
   raises the store to `core` 6, past the bound); while the store's level
   for that owner is ≤ the bound, some build that can still serve the store has only ABO's plaintext
   restore code, so encryption stays inactive. Apps without such history declare none. A **fresh**
   store is stamped at the build's supported level on its first boot (05-storage §5S.4 step 5,
   DATA2-28), so on a fresh install the bound is already passed and encryption is active as soon as
   rule 2 holds.

While `age` is configured but not active, archives are written unencrypted, secret-class items are
omitted (the config file as its redacted copy), and health is `degraded` with token
`backup_encryption_pending` and the reason (`keyring_not_exported`, `legacy_restore_bound`). Encrypted
archives are never created during a window in which a rollback target could not restore them.

**Derivation-key rotation** (SEC2-24; 05-config `secrets rotate-derive`). Every archive records the
derivation key ID it was made under (`Provenance.KeyID`); its MAC verifies, and its install-identity
encryption decrypts, with that key through `DeriveKeyFor(KeyID, …)` while the key is `active` or
`verify`. The scheduled `core.backup.verify` task rewraps (`Service.Rewrap`) every Own archive whose
key is in `verify` state, so no retained archive depends on a key once 05-config retires it; 05-config
also keeps a `verify` key past `retire_after` while a retained archive, undo point or unconsumed checkpoint
still records it (`secrets.Options.DeriveKeyInUse`, bound by the kernel to a manifest scan of this
directory). An archive whose key is already retired (an imported one, or a key retired before this rule) verifies as foreign and is restorable only as a foreign restore.

**Host-loss restore** (documented procedure, also in the operator runbook): copy to the new host the
backup directory (archives, companions, sidecars and the revocation journal) and the keyring export
(05-config); for an archive encrypted only to an operator recipient, also that operator identity. Put
the exported keyring in the new state directory first (05-config §2.4 — it is the archive identity
too), then run the offline restore `<app> restore --archive <file> [--identity <operator-identity>]
[--foreign]` before the first start. With the keyring in place and no live store, the archive's MAC
verifies and the restore is `Own`; otherwise it is foreign and needs `--foreign`. An operator identity
file is read for that one command and never stored. With no pre-swap store, nothing anchors the end of
the revocation journal, so the restore treats it as uncovered and every API key needs confirmation
(§5B.3.6, SEC2-9).

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

1. Refuse if a restore is already pending (`ErrRestorePending`).
2. Verify at `RestoreOptions.Verify` (default `VerifyOpen`). Legacy archives without a sidecar need
   `AllowWeakVerify` (today's `ErrVerificationUnsupported` choice, made explicit).
3. Provenance: `Own`, or `Foreign: true` (`ErrForeignArchive` otherwise). Audit `core.restore.stage`
   with `{foreign}` (05-rbac §7.1 action names).
4. Compatibility: `App` must match (`ErrWrongApp`); every owner level in the manifest ≤ the build's
   supported level (the guard's own comparison, 05-storage §5S.3.7); `PebbleFormat` ≤ the pin; every
   owner's version known, or newer with only `Additive` steps; else `ErrBackupTooNew`. Modules present
   in the archive but not in the build: restored, dormant, warning.
5. Secrets: open the staged checkpoint read-only and compute the key IDs its secret records reference
   (05-config `KeyIDsReferenced`); any key missing from the keyring → `ErrUnreadableSecrets` unless
   `AcceptUnreadableSecrets` (warning: those secrets must be re-entered).
6. **Pre-validate every rename** the boot phase will do (DATA-23): staging and each target on the same
   filesystem, parent writable, target not a mount point and not a single-file bind mount (device of the
   file ≠ device of its directory). The DB directory and its sidecars must pass (`ErrRenameRefused`
   otherwise). The config file and boot record are swapped only when `ReplaceConfig` is set, the archive
   holds the file itself (not a redacted copy) **and** they pass; otherwise they are extracted to
   `restore/<id>/config.restored/` for the operator to merge.
7. Extract into `<db parent>/.mediacore-restore/<id>/staging/` — the DB's filesystem, device-checked as
   `ensureSameDevice` does (`backup.go:577-598`), because the swap is a rename. Every file fsynced, then
   each directory.
8. Write `<state_dir>/restore/pending.json` = `{archive_id, kind, staging, targets[{from,to}],
   db_path, manifest_sha256, staged_by_build, expires_at, requested_by, requested_at}` — every path
   **absolute**, resolved now from 05-config's `BootView` (`core.storage.path`, `core.paths.state_dir`;
   COR2-8) — by temp file + rename + directory fsync, and the journal record `staged` to
   `<state_dir>/restore/<id>/journal.json`. The `staged` record carries the live store's
   `c/backup/journal_head` seq: the store it describes can only see more lines before the swap, so it is
   a safe boundary for a later revert of this restore (DATA2-25).
9. Return `202` with `restart_required: true`; with `Restart`, request a graceful restart through the
   lifecycle supervisor. While `pending.json` exists, `make rollback`, the updater (05-storage
   `CheckReader` token `restore_pending`, OPS-14) and the operator `deploy` recipe (09 S1; override
   `DEPLOY_DISCARD_RESTORE=1`, OPS2-26) refuse, so a deploy cannot silently discard the restore.

Boot phase — kernel **pre-boot** step P0a ([06](06-app-sdk.md) §6.5.1), before any config file or
storage is read. The updater's pre-boot check P−1 runs first (OPS2-3): it decides a rollback to
`.prev` before any swap, and `CheckReader` ignores a pending intent staged by the failing build as long
as no swap step has started; the restored `.prev` then rejects that intent at step a. P0a resolves only
the state directory (flags, env, compiled default — never the config file or the DB) and trusts the
**absolute targets recorded in `pending.json`**; 06 P2 then verifies that storage opened at the
recorded `db_path`, and a mismatch is a failure after the swap (step x) (DATA2-25):

| Step | Action (journaled in `restore/<id>/journal.json`; each step idempotent: rename only if source exists and destination does not) |
|---|---|
| a | Reject a **stale or foreign intent** (OPS-14): `expires_at` passed, or `staged_by_build` ≠ this build → move `pending.json` to `restore/<id>/rejected.json`, boot on the **untouched** live data, health `degraded` (`restore_rejected`). An older build ignores `pending.json`; this rule stops a later roll-forward from applying it by surprise. A journal that already shows `previous_moved` or later is never rejected: the swap is resumed or swapped back (step x), whichever build is running. |
| b | Re-check staging against `manifest_sha256` and entry sizes (full re-hash if `core.backup.restore.rehash_on_boot`). Failure → reject as in a. |
| c | Rename live items to `<db parent>/.mediacore-restore/<id>/previous/`: the DB dir, its sidecars (`.storage-levels`, `.storage-format`, `.migration-checkpoint`), and — only when being replaced — config file and boot record; index and derived dirs. |
| d | Rename staged items into place. |
| e | Rewrite the sidecars from the staged stamps: `.storage-levels`, and for books `.storage-format` (removes the "older archive, newer sidecar" case; DATA-23d). |
| f | Mark every derived index for rebuild; included `Derived`/`Cache` items are offered to `Load` under the generation rule. |
| g | Journal `swapped`; append a `restore` line to the revocation journal (§5B.3.6, a file outside the store); remove `pending.json`. Boot continues: P0 reads the restored config (if replaced), storage opens at the recorded `db_path` (journal `opened`), the pre-migration checkpoint and the migration DAG bring an older archive forward (G2 rules). |
| h | At 06 P4d (05-storage §5S.4 step 11), **after** the identity rebuild from the restored legacy rows (reconciler (a)) and the journal-tail re-apply (b), and before any listener: the kernel runs the restore hooks in the order auth, rbac, audit with a `journal.RestoreContext` (`Previous` = read-only view of `previous/`, `Revocations` per §5B.3.6 or nil, the restore journal, the counts of earlier hooks). 05-authn re-applies the later revocations, invalidates every native session and one-time token **and deletes every legacy `sess:`/`idx:sess:user:` and `invite:`/`idx:invite:username:` row**, bumps epochs, marks `password.changed` users reset-required, and sets keys `needs_confirmation` when `Revocations` is nil; 05-rbac re-applies role kinds; audit raises its `Sequence` past and copies forward the records above `AuditHighWater` from `previous/` and appends the restore records (`core.restore.revoke`, `core.restore.apply`). **During the compatibility window every hook write to a mirrored family goes through `Mirror.Stage`** — legacy and native rows in one batch (R2-2, SEC2-3, DATA2-4) — and a verification rebuild of each mirror the hooks wrote must match the native copy (05-storage §5S.3.11), so neither the next boot's rebuild nor a rollback build undoes them. Journal `applied`. |
| x | **Automatic swap-back** (DATA-23): any failure after step c — a rename, storage opening at a path other than the recorded `db_path`, the guard refusing the restored levels, a failing migration, a failing hook or verification rebuild — closes storage, moves the restored items to `restore/<id>/failed/`, renames `previous/` back, rewrites the sidecars from the original stamps, journals `swapped_back`, and exits non-zero. The automatic restart boots the original data with health `degraded` (`restore_failed`) and the reason. If the swap-back itself fails, boot halts with the journal naming every path, and the offline `<app> restore --revert <id>` completes it. |

Undo: `previous/` is kept for `core.backup.restore.keep_previous`. `RevertLastRestore` (API) and
`<app> restore --revert` (offline, for an app that no longer boots) stage it through the same mechanism
with kind `KindRevert`. 05-storage's offline `storage restore-checkpoint` uses the same swap with kind
`KindCheckpoint`. A crash at any step resumes from the journal at the next boot: forward to `applied`,
or into the swap-back.

### 5B.3.5 Creation, verification, retention, catalogue

Create (task `core.backup.create`, `Resource` = `entity.Ref{Module: "core", Kind: "backup", ID: "main"}`,
exclusive with stage-restore, prune and rewrap):

1. Resolve codec, recipients and whether encryption is active first (configuration errors before any
   work, the ABO ordering at `backup.go:428-440`). Encryption inactive + secret-class item → item
   skipped (manifest notes it).
2. Retention **before** the space check (kept from `backup.go:445-458`).
3. Space check (`Avail` vs estimated size + margin).
4. Read the revocation-journal head seq (`JournalSeq`) and the audit `Sequence` high-water mark
   (`AuditHighWater`) **before** the checkpoint call (DATA2-7), then checkpoint on the DB's filesystem
   with `WithFlushedWAL` (cross-device → `ErrCrossDevice`, never a silent copy); read the level stamps,
   the books legacy projection and every derived generation from it. A revocation appended before step 4
   is either in the checkpoint or above `JournalSeq`; the same holds for audit records.
5. Capture artifacts into the run directory beside the checkpoint (hooks write there), so every input is
   an immutable local file; the config file is captured as itself only while encryption is active, else
   as 05-config's `RedactedConfigFile()` when `ConfigFileHasSecrets()` (`ConfigRedacted`). Capture
   included derived items and mark stale every item whose source generation at the end of its capture
   differs from the checkpoint's (B1). Hash pass over all files (bounded pool), build the manifest,
   compute the provenance MAC under the active derivation key (`Provenance.KeyID` = 05-config
   `ActiveDeriveID()`), then stream the main archive (manifest first) and the companion through tar →
   codec (→ age when active) into `.partial` files, re-hashing while streaming and aborting on any
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
| floor 2 (DATA-22) | — | the newest verified **plaintext** archive (legacy ones included), until an encrypted archive is verified **and** the keyring export covers its derivation key (or an operator recipient is configured) | no state in which every remaining archive needs a key that exists only on this host |
| floor 3 | — | any archive referenced by a pending or undo-able restore | |

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
| Where (DATA2-27) | `<backup dir>/<prefix>revocations.journal` (0600; JSON lines), where `<prefix>` is the app's archive prefix (ABO: `audiobooks_pebble_revocations.journal`), so two apps sharing a backup directory never share a journal; an older ABO build neither lists nor prunes it (no archive extension). It sits beside the archives so an off-host copy of the backup directory carries it. |
| Format and integrity (SEC2-9) | Line 0 is the header `{seq: 0, install_id, app, started_at, kid, base_seq, base_mac, mac}`; every later line is a `journal.Revocation` plus `kid` and `mac`, with `seq` = previous + 1 and `mac = HMAC-SHA256(DeriveKeyFor(kid, "core.backup/revocations/v1"), prev_mac ‖ canonical line without mac)`, `kid` being the derivation key ID the line was written under (05-config; a derive-key rotation changes `kid` mid-journal without breaking the chain). Deleting, reordering, editing or forging a line breaks the chain. |
| Head in the store (SEC2-9, DATA2-5) | Every revoking batch also writes `c/backup/journal_head` = `{seq, mac}` of its line (`RevocationJournal.Append` stages it by participant binding). The store — and so every archive and checkpoint made from it — knows the last line it committed; truncating the file below a store's head is detected. |
| Writers (R2-1, DATA2-5) | Every build that can open an adopted store: `core/auth` (key revoke and disable, user disable and delete, credential-epoch bump, password change, identity unlink, session revocation), `core/rbac` (`role.unassigned`, `role.narrowed`, `role.deleted`) — the kinds of §5B.3.2 — and the **Teach build** (05-authn §11.1), whose legacy handlers journal the same restrictions they perform; builds older than the Teach build cannot open such a store (05-storage §5S.3.7). `Append` is called **before** the revocation's batch commits; each line is fsynced. A recorded revocation whose commit then fails is harmless: re-applying it revokes more, never less. Every swap that replaces the store — 06 P0a (step g of §5B.3.4) and every restore the Teach build performs — appends a `restore` line `{kind, source, boundary}`. |
| What a legacy writer writes | The Teach build's legacy handlers (ABO's gin code, 05-authn §11.1) journal through the same public `backup/journal` writer when the build has it; a Teach build that predates the package writes exactly the same bytes with `storage/keys` (05-storage §5S.3.3). Either way: the line format above (the Teach build holds the S3 keyring, so it can MAC); `c/backup/journal_head` in its revoking batch; on append failure `c/backup/journal_gap/<since>` in that batch, and a `gap` line on the next successful append; after a restore it performs, a `restore` line. |
| Append failure | The revocation still commits — a disk problem never blocks a security action — and the same batch records `c/backup/journal_gap/<since>` through `Gap`; health is `degraded` (`revocation_journal_unwritable`) until the next successful append writes a `gap` line closing the interval. |
| Boot re-apply (DATA2-5, R2-2) | At every boot, 06 P4d step 2 (05-storage §5S.4 reconciler (b)): when the store's `journal_head.seq` is below the journal's last verified line — the store was replaced by any means (a restore by any build, a checkpoint restore, a file copy) or a commit after an append failed — the kernel re-applies every verified line after the head through auth's and rbac's re-apply functions, each staged with `Mirror.Stage` (legacy and native rows in one batch) and writing the new head. A store with no head never committed a journaled revocation (it predates the journal, or is an older archive), so every verified line is re-applied. Re-application stops at a chain break (health `degraded`, `revocation_journal_broken`); the post-restore coverage rule below then fails safe. |
| Coverage at restore | `RestoreContext.Boundary` = min(the source's recorded seq — the manifest's `JournalSeq`, the `staged` record of the restore being reverted, or the checkpoint record's `journal_seq` (05-storage §5S.3.12) — and the swapped-in store's own `journal_head.seq`). `Revocations` yields the lines after `Boundary` only when: the journal's `install_id` equals the archive's; the chain verifies from `Boundary` to the **pre-swap store's** `journal_head` (the journal is not truncated below what the live store had committed); and no gap — in the journal or in the pre-swap store — lies after `Boundary`. Otherwise it is nil and 05-authn sets every API key `needs_confirmation` (§4.14 step 3). Foreign restores and host-loss restores (no pre-swap store, so nothing anchors the journal's end) always get nil. |
| Retention | Prune drops lines whose `seq` is at or below the smallest boundary any retained archive, undo point or unconsumed checkpoint records, by rewriting the journal (temp file + rename) with a new header whose `base_seq`/`base_mac` are the last dropped line's, so the chain continues. |

### 5B.3.7 Provenance (SEC-16)

age gives confidentiality, not authenticity: recipients are public, so anyone can make an archive that
"decrypts" here. The manifest's `Provenance.MAC` is HMAC-SHA256 over the canonical manifest bytes (with
the MAC field empty) under `DeriveKeyFor(Provenance.KeyID, "core.backup/manifest/v1")` of this install's
keyring (05-config; the derivation key survives data-key rotation, and its own rotation leaves older
keys verify-only until retired, SEC2-24). The MAC'd bytes include `install_id`, `key_id`, every entry
hash of both archives, the levels and the journal and audit boundaries, so neither archive can be
swapped or edited. An archive is `Own` when the MAC verifies **and** `install_id` equals the live
store's `c/storage/install` (or no live store exists, the host-loss case once the exported keyring is in
place). Everything else — including every `legacy-abo/0` archive and every archive whose `key_id` is
retired — is restored only as a foreign restore (B5).

### 5B.3.8 Routes (SEC2-8)

All under `/api/core/v1`, registered by `core/internal/backuphttp`; the legacy `/api/v1/backup/*` paths
are aliases for the 09 window (§5B.10).

| Route | Permission and guards | Audited |
|---|---|---|
| `GET /backups`, `GET /backups/{id}` | `core:system.view` | no |
| `POST /backups/{id}/verify` | `core:system.view` | no |
| `POST /backups` (create) | `core:system.manage` | `core.backup.create` |
| `DELETE /backups/{id}` | `core:system.manage` + `Guards.Interactive` | no (floors refuse protected archives) |
| `POST /backups/import` | `core:system.manage` + `Guards.Interactive` | `core.backup.import` |
| `GET /backups/{id}/download` | `core:system.manage` + `Guards.Interactive`; streams the archive as stored | `core.backup.download` |
| `POST /backups/{id}/restore`, `POST /restore/revert`, `DELETE /restore/pending`, `GET /restore/pending` | `core:backup.restore` (`Sensitive`, `AuthRequired`) + `Guards.Interactive` + `Guards.Owner`; a foreign restore also `core:admin`; `GET` needs only `core:backup.restore` | `core.restore.stage` (`{foreign}`), then `core.restore.apply` at boot |
| `POST /backups/{id}/rewrap` | `core:system.manage` + `Guards.Interactive` | no |

`ExportPlain` is CLI only (`<app> backup export-plain`): it writes decrypted archives to a directory and
needs filesystem access, which no HTTP route grants.

## 5B.4 Lifecycle

Phases are 06 §6.5.1's (R2-8).

- Construction: `backup/journal` is opened by the kernel from S4 (its file under the backup directory,
  the derive function from `secrets`) and handed to `auth` and `rbac`; `backup` itself is built in P5
  after storage and secrets, before modules; module artifacts registered through `Registrar.Backup`
  ([06](06-app-sdk.md)) are validated in P7 (unique names, required `Consistency`).
- Boot: the pending-restore swap is P0a (§5B.3.4), after the updater's P−1; storage then opens at the
  recorded DB path. At P4d step 2 the journal tail is re-applied (§5B.3.6) and, after a restore, the
  restore hooks run at step 5. After services are built the catalogue is reconciled from the backup
  directory (orphans removed), the revocation journal's header is checked against `c/storage/install`,
  and interrupted `.partial` archives and staging dirs older than 24 h are swept (ABO
  `sweepStaleCheckpoints`, `backup.go:600-640`).
- Schedules: `core.backup.create` and `core.backup.verify` through `tasks.ScheduleFromConfig`.
- Shutdown: a running create is cancelled at an entry boundary; its partial files are removed.

## 5B.5 Error semantics

| Error path | Fail-open / fail-closed | Why |
|---|---|---|
| Codec/recipient/config invalid | closed before any work | ABO lesson: discover config errors before pruning and checkpointing (`backup.go:428-440`). |
| Encryption configured but not active | open (archive written unencrypted, secret-class items omitted, the config file redacted), health `degraded` with the reason | Never produce archives a rollback target or a host loss cannot recover (DATA-22), nor plaintext secrets (DATA2-17). |
| Insufficient space | closed (`ErrInsufficientSpace`) after retention ran | Full disk kills Pebble's WAL (`backup.go:85-100`). |
| Checkpoint or restore staging cross-device | closed (`ErrCrossDevice`) | A silent multi-GB copy is worse than a fast failure (`backup.go:477-498`). |
| Secret item with encryption inactive | closed for that item (skipped), noted | Never write secret material in clear. |
| Artifact hook fails | closed (archive aborted, partial removed) | An archive missing a system-of-record artifact is not a backup. |
| Derived/recreatable capture fails or goes stale | open (item omitted or marked `stale`) | Rebuildable on restore. |
| Retention delete fails | open (logged; budget not credited) | Kept from `backup.go:1255-1262`. |
| Delete of a floor-protected archive | closed (`ErrProtectedByFloor`) | The floors are the recovery guarantee. |
| Post-create verification fails | closed: archive marked failed, not counted as a floor, alert | An unverifiable archive must not protect itself from pruning. |
| Restore verify fails / checksum mismatch / no identity | closed (`409` / `400`), nothing staged | Kept fail-closed semantics of `handler.go:870-895`. |
| Foreign provenance without `Foreign` | closed (`ErrForeignArchive`) | Account takeover through a planted archive (SEC-16). |
| Archive too new / wrong app | closed | Older build would misread. |
| Keyring lacks the archive's keys | closed (`ErrUnreadableSecrets`) unless `AcceptUnreadableSecrets` | Silent loss of secrets is worse than a refusal. |
| A rename target fails the pre-check | closed for the DB (`ErrRenameRefused`); for config/boot record: not swapped, staged for the operator | A swap must not move the DB and then fail on a bind-mounted config file (DATA-23a). |
| Stale or foreign pending intent at boot (no swap started) | open for the app (boot on live data), closed for the restore (rejected) | OPS-14: no surprise restore after a rollback and roll-forward; after the updater's rollback, `.prev` rejects the failing build's intent (OPS2-3). |
| Storage opens at a path other than the recorded `db_path` | closed for the restore: automatic swap-back | The swap must never be applied to one path and served from another (DATA2-25). |
| Boot-time staging re-check fails | open for the app (boot on live data), closed for the restore (rejected) | Live data is untouched; refusing to boot would turn a bad archive into an outage. |
| Any failure after the swap (guard, migration, hook, verification rebuild) | closed for the restore: automatic swap-back, exit, restart on the original data with health `degraded` | DATA-23b: never leave the app down with live data parked in `previous/`. |
| Swap-back fails | closed (boot halts; journal names every path; offline `restore --revert`) | Manual recovery with exact instructions beats guessing. |
| Crash during the swap | resume from journal | Each step idempotent. |
| Post-restore hook fails | handled by the swap-back row | Resurrecting revoked credentials is a security failure (05-authn §4.14). |
| Revocation journal append fails | open (revocation commits; gap recorded; health `degraded`) | Never block a revocation; the gap makes a later restore fail safe (keys need confirmation). |
| Revocation journal chain broken or truncated below the live store's head | open for the app (health `degraded`, `revocation_journal_broken`); closed for coverage (`Revocations` nil → keys need confirmation) | A tampered journal must never make a restore look covered (SEC2-9). |
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
| `core.backup.dir` | path | `backups` beside the DB (relative values resolve against the DB's parent, `backup.ResolveDir` semantics, `backup.go:120-146`; ABO: `backup_dir`) | yes (next run) | admin |
| `core.backup.schedules.create` | `tasks.ScheduleConfig` | `{enabled: true, every: 24h}` (the durable clock's first-sight seed spreads installs, 05-tasks §5T.3.8) | yes | none |
| `core.backup.compression` | enum `zstd`/`gzip`/`none` | `zstd` (ABO keeps `gzip` until changed) | yes | none |
| `core.backup.compression_level` | int | 0 = codec default | yes | none |
| `core.backup.encryption` | enum `age`/`none` | `none` on an upgraded install, `age` on a fresh one (active only per §5B.3.3) | yes | admin |
| `core.backup.recipients` | list of age public keys | empty | yes | admin |
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
| `core.backup.restore.keep_previous` | duration | 168h | yes | interactive |
| `core.backup.restore.intent_ttl` | duration | 24h | yes | none |
| `core.backup.restore.rehash_on_boot` | bool | false | yes | none |

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
| `mediacore_backup_archives` | gauge | `format`, `encrypted`, `provenance` |
| `mediacore_backup_retained_bytes` | gauge | — |
| `mediacore_backup_verify_total` | counter | `level`, `outcome` |
| `mediacore_backup_restore_total` | counter | `stage` (staged, applied, rejected, swapped_back, reverted), `foreign` |
| `mediacore_backup_encryption_active` | gauge | — (0/1) |
| `mediacore_backup_revocation_journal_gaps` | gauge | — |
| `mediacore_backup_revocation_reapplied_total` | counter | `kind`, `cause` (boot, restore) |

Health ([05-health](05-health.md)) turns `last_verified` older than 2 × the verify schedule into
`degraded`, and reports `backup_encryption_pending`, `restore_rejected`, `restore_failed`,
`revocation_journal_unwritable`, `revocation_journal_broken`.

## 5B.9 Tests

| Test | Proves |
|---|---|
| `TestRestoreNeverWritesLiveStore` | the API phase touches only staging and `restore/`; live DB bytes unchanged |
| `TestStagedRestoreAppliedBeforeConfigAndStorage` | pre-boot order; a replaced config file is in effect for the applying boot |
| `TestRestoreCrashAtEveryJournalStep` | each crash point resumes to applied, to swapped-back, or to untouched live data |
| `TestRestoreRejectedKeepsLiveData` | bad staging → boot on live data, health degraded |
| `TestStaleOrForeignIntentRejected` | expired intent, other build; a journal past `previous_moved` is resumed or swapped back, never rejected |
| `TestRestoreRejectsMismatchedDBPath` | P0a swaps the recorded absolute targets; storage opening elsewhere swaps back (DATA2-25) |
| `TestUpdaterRollbackRejectsFailingBuildsIntent` | P−1 rolls back with a pending intent staged by the failing build; `.prev` rejects it at step a (OPS2-3) |
| `TestRenamePrecheckRefusesBindMountedConfig` | config not swapped and staged for the operator; DB still restorable |
| `TestRestoreFailureSwapsBack` | guard refusal, failing migration and failing hook each swap back and boot the original data |
| `TestRevertLastRestore` / `TestOfflineRevert` | undo point round trip, with and without a booting app |
| `TestRestoreOlderArchiveRunsMigrations` | older versions migrate forward after the swap |
| `TestRestoreRefusesTooNewArchive` | level, Pebble format, Breaking step, wrong app |
| `TestRestoreRewritesSidecarsFromStamps` | `.storage-levels` and `.storage-format` match the restored stamps |
| `TestPostRestoreReappliesRevocations` | key revoked after the archive stays revoked; journal gap → keys need confirmation |
| `TestRestoreHooksWriteThroughMirror` | hook writes run after the rebuild, through `Mirror.Stage`; after a restart and after a rollback to the Teach build the re-revoked key, a restored legacy session and a restored legacy invite stay refused; a native-only hook write fails the verification rebuild and swaps back (R2-2, SEC2-3, DATA2-4) |
| `TestRevocationCommittedAfterCheckpointReapplied` | appended before, committed after the checkpoint: re-applied by seq, whatever the clocks say (DATA2-7) |
| `TestRevocationJournal_TruncationDetected` | deleted, edited, reordered or truncated lines break coverage → keys need confirmation (SEC2-9) |
| `TestJournalCoversRollbackRevocation` / `TestOlderBuildRestoreReappliedAtNextBoot` | a revocation on the Teach build after a rollback, and a restore the Teach build performed, are re-applied by the next new-build boot (R2-1, DATA2-5) |
| `TestRevertBoundaryFromStagedRecord` | a revert and a checkpoint restore re-apply the lines after their recorded boundary (DATA2-25) |
| `TestRestore_DemotedAdminStaysDemoted` | `role.unassigned` / `role.narrowed` / `role.deleted` re-applied (with 05-rbac, SEC2-10) |
| `TestRestore_PostArchivePasswordChangeNotReverted` | `password.changed` → reset required; `identity.unlinked` link removed (with 05-authn, SEC2-11) |
| `TestRestoreKeepsAuditTrail` | audit records above the manifest's high-water mark and the restore records survive, no seq collision (with 05-rbac, DATA2-16) |
| `TestForeignArchiveNeedsFlag` | planted archive encrypted to the public recipient is refused without `Foreign` |
| `TestProvenanceBindsInstallAndEntries` | edited entry, swapped companion, other install ID → foreign |
| `TestPreviousBuildRestoresMainArchive` | the HEAD build's restore code restores a `mediacore-backup/1` main archive and ignores companion and sidecars |
| `TestPreviousBuildNeverListsCompanion` | HEAD listing and pruning skip `.extra`, `.manifest.json`, `.age` |
| `TestUpgradeDoesNotSwitchToUnrecoverableArchives` | upgraded install stays plaintext; floor 2 keeps the newest verified plaintext archive |
| `TestEncryptionInactiveUntilExportAndBound` | keyring export covering the active derivation key, operator recipient, legacy restore bound; a fresh install is past the bound (DATA2-28) |
| `TestEncryptedArchiveNeedsIdentity` | the keyring-derived identity and an operator recipient both decrypt; neither present → `ErrDecrypt`; no identity file exists (SIMP2-2) |
| `TestRewrapBeforeDeriveKeyRetired` | after `rotate-derive`, the verify task rewraps archives under the `verify` key; a retired key's archive verifies as foreign (SEC2-24) |
| `TestNoKeyMaterialInArchives` | keyring never written; certs only when encrypted |
| `TestPlainArchiveHasNoConfigSecrets` | with `ConfigFileHasSecrets()`, a plaintext archive carries the redacted file and `config_redacted`; `config.yaml` beside the DB is never archived (DATA2-17) |
| `TestUnreadableSecretsRefused` | keyring missing a referenced key → refused unless accepted |
| `TestCatalogueSurvivesRestore` | archives newer than the restored one stay listed and protected |
| `TestCatalogueDropsOrphans` / `TestJournalNamePerApp` | sidecars of a main archive an older build pruned are removed; two apps in one directory keep separate journals (DATA2-27) |
| `TestBackupRoutesPermissions` | the §5B.3.8 table: permission, guards and audit per route (SEC2-8) |
| `TestManifestMatchesEntries` | tampered entry → `ErrManifest` |
| `TestVerifyOpenCountsKeys` | read-only open of the checkpoint matches manifest counts |
| `TestCheckpointCrossDeviceRefused` | no silent copy |
| `TestRetentionFloors` | newest verified; newest verified plaintext; restore-referenced |
| `TestRetentionRunsBeforeSpaceCheck` | stuck-disk regression from 2026-08-29 |
| `TestLegacyABOArchiveRestores` | gzip/zstd/none, with and without sidecar, entry root `audiobooks.pebble/`, foreign confirmation |
| `TestRewrapLegacyArchive` | plain → encrypted, verified, original kept while a floor protects it |
| `TestDerivedItemsGenerationChecked` | stale bleve/hnsw items marked and rebuilt; an item whose source was touched between the checkpoint and the capture is stale (DATA2-27) |
| `TestHostLossRestore` | backup dir + keyring export on an empty host → own restore of an archive encrypted to the derived identity; journal unanchored → keys need confirmation |
| `TestBackupTaskExclusive` | create vs stage-restore vs prune |
| `TestCreateCancelRemovesPartial` (`synctest`) | cancellation cleanliness |

## 5B.10 Migration from ABO

| ABO code | Becomes |
|---|---|
| `internal/backup/codec.go` (gzip/zstd/none, magic sniffing, level validation) | `core/internal/backuparchive` codecs (+ age layer) |
| `CreateBackupWithCheckpoint`, `ensureSameDevice`, `sweepStaleCheckpoints`, space guard, retention | `backup.Service.Create` / prune internals (rules kept; floors as §5B.3.5) |
| `CreateBackup` live-walk fallback and SQLite branches (`backup.go:283,959,1028,1103`) | deleted: core always checkpoints |
| `RestoreBackupIn` / `extractArchive` | legacy reader for format `legacy-abo/0`, used only by `StageRestore` into staging |
| `ListBackups`, `newestBackupAge` (`organizer/service.go:567`) | `Service.List` (directory-derived catalogue) and `Ensurer.Ensure` |
| `handlers/system` backup routes (`/api/v1/backup/*`) | core routes `/api/core/v1/backups[/{id}[/verify|/restore]]`; legacy paths aliased for the 09 compatibility window, `POST /api/v1/backup/restore` re-implemented as stage-restore (it can no longer extract into an arbitrary `target_path`) |
| organizer auto-backup | `Deps.Backup.Ensure(ctx, 6h, "pre:books.organize")` |
| config `backup_dir`, `backup_max_total_bytes`, `backup_compression`, `backup_compression_level`, `create_backups` | `core.backup.*` aliases (09) |

First run on an upgraded ABO install: existing `audiobooks_*` archives appear as `legacy-abo/0`
(unencrypted, pre-provenance); encryption stays `none` until the operator opts in; the first
`core.backup.create` writes a `mediacore-backup/1` main archive under the legacy name pattern, which the
previous build lists and restores; nothing is deleted except by retention, and floor 2 keeps the newest
verified plaintext archive. When the staged restore ships relative to the first contract (OPS-15) is 09's
decision; this file only requires that no contract precede it.

## 5B.11 Open items

1. Whether `Import` (uploading an archive over HTTP) needs a size cap and streaming verification
   before it is stored — default cap = `retention.max_total_bytes`.
2. TLS material under `certs/` may be ACME-managed (re-creatable) or operator-provided; class decided
   with [05-http-api](05-http-api.md).
3. Multi-app in one binary: one archive per install (one DB); `Manifest.App` becomes a list then. A
   merge of two installs' stores is not an archive operation (05-storage open item 6, DATA-30). Not
   built until that mode exists (lowest priority).

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

- R2-1 / DATA2-5 — the Teach build journals its revocations and restores (§5B.3.6 Writers, "What a pre-core writer writes"); every boot re-applies the journal tail the store lacks; builds older than the Teach build cannot open an adopted store.
- R2-2 / DATA2-4 / SEC2-3 — restore hooks run at P4d step 5 after the rebuild and the journal re-apply, write mirrored families only through `Mirror.Stage`, delete legacy `sess:`/`invite:` rows, and are checked by a verification rebuild (§5B.3.4 step h, step x).
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

- integration pass (round 2): B3: books' legacy restore bound is `{Owner: core, Level: 5}`; C8 raises the store to 6 (09 v1.2.0).
- integration pass (round 2): Schedule keys `core.backup.schedules.create` / `core.backup.schedules.verify` (R2-7 form).
- integration pass (round 2): Rewrap paragraph: 05-config keeps a `verify` derive key while a retained archive records it (`DeriveKeyInUse`).
