// file: core/backup/types.go
// version: 1.1.1
// guid: 57a7b5b9-8d91-4be9-b34d-06a2bc8b8eeb
// last-edited: 2026-11-12

// Package backup is core's backup and restore (05-backup §5B), dark until
// F8-04 wires it: the one archive format `mediacore-backup/1` with its
// install-bound provenance (B1, B5), creation with verification (B4), the
// directory-derived catalogue and retention with its floors (B6), the staged
// restore API phase (B2, §5B.3.4 steps 1-9), the reader of HEAD's
// `legacy-abo/0` archives for staging and `storage restore-offline --archive`
// (B7), and the core.backup.* tasks (B8).
//
// Layer L4. It imports storage (and its L0 leaf restorejournal), config's
// value types, tasks and backup/journal; it never imports kernel, app or an
// HTTP package. What it needs of the kernel it takes as small seams in
// Options (Store, Keys, Stager, Journal, Inspector): the kernel binds them at
// F8-04. Nothing here writes an ABO store or changes an ABO route (I8).
//
// The types of this file are §5B.3.2's. One change is a width fix: core CI
// refuses an interface of more than eight declared entries (make -C core
// interface-width), and the spec's Service has eleven (Runner and ten
// methods), so Service embeds Catalogue and Restorer. Its method set is the
// spec's, member for member.
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

// ArchiveID is an archive's ID: a ULID for a mediacore-backup/1 archive, a
// name-derived "legacy-" ID for a legacy-abo/0 one.
type ArchiveID string

// Class is how a captured item is protected and restored.
type Class uint8

const (
	ClassInvalid Class = iota
	SystemOfRecord
	Secret
	Derived
	Cache
	Recreatable
)

// Consistency is how an artifact stays consistent with the checkpoint.
type Consistency uint8

const (
	ConsistencyInvalid Consistency = iota
	DescribedByStorage             // artifact content is derivable from Pebble rows
	AppendOnly                     // files are immutable once named; superset after checkpoint is safe
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

// ArtifactWriter is where a Snapshot hook writes, into the run directory
// beside the checkpoint.
type ArtifactWriter interface {
	Create(rel string, mode fs.FileMode) (io.WriteCloser, error) // writes into the run dir beside the checkpoint
}

// ArtifactReader is what a Restore hook reads an archived artifact through.
type ArtifactReader interface {
	Open(rel string) (io.ReadCloser, error)
	Walk(func(rel string, size int64) error) error
}

// Manifest is the archive's first entry and its .manifest.json sidecar.
type Manifest struct {
	Format      string    `json:"format"` // "mediacore-backup/1"
	ArchiveID   ArchiveID `json:"archive_id"`
	App         string    `json:"app"` // app.Options.AppID
	AppVersion  string    `json:"app_version"`
	CoreVersion string    `json:"core_version"` // version.Version (03 §3.3)
	InstallID   string    `json:"install_id"`   // c/storage/install
	CreatedAt   time.Time `json:"created_at"`   // informational only; no restore rule reads it (DATA2-7)
	Reason      string    `json:"reason"`       // "schedule", "manual", "pre:books.organize", "pre-update", "pre-migration"
	// Read BEFORE the checkpoint call, so every revocation or audit record the
	// checkpoint lacks lies above them (clock-free boundaries, DATA2-7).
	JournalSeq     uint64 `json:"journal_seq"`      // revocation journal head seq (§5B.3.6)
	AuditHighWater uint64 `json:"audit_high_water"` // c/audit/ Sequence high-water mark (05-rbac §7.1)
	// ServedBoot is the content marker c/backup/served_boot read before the
	// checkpoint; Create then writes a newer pair to the live store, so the
	// archive's pair is always older than the file's (§5B.3.6, SEC6-6).
	ServedBoot          *journal.ServedBoot    `json:"served_boot,omitempty"`
	Levels              map[string]int         `json:"levels"`                          // per-owner reader levels in the checkpoint (05-storage §5S.3.7)
	LegacyStorageFormat int                    `json:"legacy_storage_format,omitempty"` // books' projection, 1 or LegacyMaxFormat+1
	PebbleFormat        uint64                 `json:"pebble_format"`
	Keyspaces           map[string]KSInfo      `json:"keyspaces"` // owner -> version, approx keys/bytes
	Modules             map[string]string      `json:"modules"`   // module ID -> module version
	Entries             []Entry                `json:"entries"`   // archive files: path, size, sha256, class
	Derived             map[string]DerivedInfo `json:"derived,omitempty"`
	ConfigRedacted      bool                   `json:"config_redacted,omitempty"` // the archive holds 05-config's RedactedConfigFile (DATA2-17)
	// Encrypted and Recipients are deferred-build members (§5B.3.9); absent = plaintext.
	Provenance Provenance `json:"provenance"` // computed last; MAC over the manifest with this field's MAC empty
}

// KSInfo is one owner's row of Manifest.Keyspaces.
type KSInfo struct {
	Version int    `json:"version"`
	Keys    int64  `json:"keys"` // exact, counted from the checkpoint during verification
	Bytes   uint64 `json:"bytes"`
}

// Entry is one archive file.
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Class  Class  `json:"class"`
}

// DerivedInfo is one derived item's generation record.
type DerivedInfo struct {
	Generation string `json:"generation"` // 05-storage Generation, hex
	Stale      bool   `json:"stale"`      // source generation at the end of capture != checkpoint's: never offered on restore
}

// Provenance is the manifest's install-bound MAC (B5, §5B.3.7).
type Provenance struct {
	Alg   string `json:"alg"`    // "hmac-sha256"
	KeyID string `json:"key_id"` // 05-config ActiveDeriveID() when made; the archive identity derives from the same key (SEC2-24)
	MAC   string `json:"mac"`    // key: DeriveKeyFor(KeyID, "core.backup/manifest/v1") of this install
}

// ProvenanceClass is whether an archive was made by this install.
type ProvenanceClass uint8

const (
	ProvenanceInvalid ProvenanceClass = iota
	Own                               // MAC verifies with this install's derivation key KeyID and InstallID matches (or there is no live store)
	Foreign                           // MAC missing or wrong, KeyID retired or unknown, or another install's ID
	PreProvenance                     // legacy-abo/0: made before provenance existed; restored only as Foreign
)

// String is the class's name in logs, metrics labels and API bodies.
func (c ProvenanceClass) String() string {
	switch c {
	case Own:
		return "own"
	case Foreign:
		return "foreign"
	case PreProvenance:
		return "pre_provenance"
	}
	return "invalid"
}

// The archive formats Archive.Format names.
const (
	FormatMediacore = "mediacore-backup/1"
	FormatLegacyABO = "legacy-abo/0"
)

// Archive is one catalogue entry.
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

// VerifyLevel is how much of an archive a verification checks.
type VerifyLevel uint8

const (
	VerifyInvalid  VerifyLevel = iota
	VerifyChecksum             // outer SHA-256 sidecars only (fast)
	VerifyEntries              // every entry hash vs manifest, both archives
	VerifyOpen                 // VerifyEntries + open the checkpoint read-only in a temp dir and count keys per owner
)

// String is the level's name in logs, metrics labels and API bodies.
func (l VerifyLevel) String() string {
	switch l {
	case VerifyChecksum:
		return "checksum"
	case VerifyEntries:
		return "entries"
	case VerifyOpen:
		return "open"
	}
	return "invalid"
}

// VerifyResult is the last verification of an archive (<archive>.verify.json).
type VerifyResult struct {
	At     time.Time
	Level  VerifyLevel
	OK     bool
	Detail string
}

// CreateOptions is what one Create asks for.
type CreateOptions struct {
	Reason             string
	IncludeDerived     bool
	IncludeRecreatable bool
}

// RestoreOptions is what one staged restore asks for.
type RestoreOptions struct {
	Verify                  VerifyLevel // default VerifyOpen; lower requires AllowWeakVerify
	AllowWeakVerify         bool        // needed for legacy archives without a sidecar
	Foreign                 bool        // required unless provenance is Own; the route also requires core:admin + interactive owner proof
	ReplaceConfig           bool        // swap the config file and boot record too (default: stage them for the operator)
	AcceptUnreadableSecrets bool        // the keyring lacks keys the archive's secret records need: they must be re-entered
	Restart                 bool        // once staged, request a restart (06 Lifecycle.RequestRestart: drain, then the restart exit code; OPS8-12)
}

// PendingRestore is the staged swap, as the API reports it.
type PendingRestore struct {
	ArchiveID     ArchiveID
	Kind          journal.RestoreKind
	StagedAt      time.Time
	StagedByBuild string    // the binary ID (app.BinaryID; R5-7, FE5-3); another binary rejects the intent
	ExpiresAt     time.Time // StagedAt + core.storage.swap.intent_ttl (05-storage §5S.7; SIMP8-1)
	RequestedBy   string
	Warnings      []string // e.g. "module tv in backup is not in this build: its data is restored and dormant"
}

// Ensurer is the handle modules receive (module.Deps.Backup): "archive unless
// one verified within maxAge" (organizer's pre-organize backup, service.go:697).
type Ensurer interface {
	Ensure(ctx context.Context, maxAge time.Duration, reason string) (a Archive, created bool, err error)
}

// Runner is what the updater's pre-update backup would receive. That consumer
// is a deferred-build member of 05-updater §5U.3 (R10-8, OPS10-6): the R2
// updater is HEAD's moved pipeline and takes no backup, so no R0-R3 build
// passes a Runner to it. Service embeds it.
type Runner interface {
	Ensurer
	Create(ctx context.Context, o CreateOptions) (Archive, error)
}

// Catalogue is the archive-directory half of Service: list, verify, import,
// download, delete. It exists so no declared interface exceeds core's width
// limit (see the package comment); consumers hold Service.
type Catalogue interface {
	List(ctx context.Context) ([]Archive, error) // the catalogue derived from the backup directory (§5B.3.5); never hashes files
	Verify(ctx context.Context, id ArchiveID, l VerifyLevel) (VerifyResult, error)
	Import(ctx context.Context, name string, r io.Reader) (Archive, error) // provenance decides Own vs Foreign
	Download(ctx context.Context, id ArchiveID) (io.ReadCloser, error)     // the archive as stored
	Delete(ctx context.Context, id ArchiveID) error                        // refused for archives protected by a retention floor
}

// Restorer is the restore half of Service: stage, cancel, report and undo.
type Restorer interface {
	StageRestore(ctx context.Context, id ArchiveID, o RestoreOptions) (PendingRestore, error)
	CancelRestore(ctx context.Context) error
	Pending(ctx context.Context) (*PendingRestore, error)
	RevertLastRestore(ctx context.Context, o RestoreOptions) (PendingRestore, error)
}

// Service is core's backup API, exposed to the HTTP layer (§5B.3.8) and the CLI.
type Service interface {
	Runner
	Catalogue
	Restorer
	// Rewrap re-creates an Own archive under the ACTIVE derivation key (re-MAC;
	// re-encryption is a deferred-build member, §5B.3.9). The original is
	// deleted only if no floor protects it.
	Rewrap(ctx context.Context, id ArchiveID) (Archive, error)
}

var (
	ErrBackupInProgress   = errors.New("backup: another backup or restore task holds the backup resource")
	ErrInsufficientSpace  = errors.New("backup: insufficient disk space")
	ErrCrossDevice        = errors.New("backup: checkpoint or restore staging is not on the database's filesystem")
	ErrChecksumMismatch   = errors.New("backup: archive does not match its recorded checksum")
	ErrNoChecksum         = errors.New("backup: archive has no checksum sidecar")
	ErrManifest           = errors.New("backup: manifest missing, invalid or inconsistent with entries")
	ErrForeignArchive     = errors.New("backup: archive provenance is not this install; restore requires Foreign")
	ErrBackupTooNew       = errors.New("backup: archive needs a newer build (reader level, Pebble format or Breaking step)")
	ErrWrongApp           = errors.New("backup: archive belongs to a different app")
	ErrRestoreStale       = errors.New("backup: the staged restore expired, was staged by another build, or the store was opened since")
	ErrUnreadableSecrets  = errors.New("backup: the keyring lacks keys the archive's secret records need")
	ErrUnencryptedSecrets = errors.New("backup: refusing to write secret material into an unencrypted archive")
	ErrProtectedByFloor   = errors.New("backup: archive is protected by a retention floor")
	// ErrNotFound: no archive of that ID is in the backup directory (the
	// catalogue is derived from the directory, so this is also the answer for an
	// archive deleted outside the service).
	ErrNotFound = errors.New("backup: no such archive")
)
