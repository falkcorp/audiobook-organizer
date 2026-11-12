// file: core/backup/options.go
// version: 1.0.0
// guid: 7f8ad2c3-31d0-4b0e-b0c5-6a0f3a1d8e44
// last-edited: 2026-11-12

package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

const gib = int64(1) << 30

// Settings are the core.backup.* values the service reads (05-backup §5B.7),
// re-read on every operation so a hot-reloadable key takes effect at once. The
// kernel's core.backup section declares the same keys; backup mirrors the
// ones it uses so it never imports the kernel.
type Settings struct {
	Enabled            bool
	Compression        string // zstd, gzip or none
	CompressionLevel   int    // 0 = the codec's default
	IncludeDerived     bool
	IncludeRecreatable bool
	VerifyAfterCreate  bool
	VerifyKeep         int
	MaxCount           int           // >= 1
	MaxTotalBytes      int64         // 0 = unlimited
	MaxAge             time.Duration // 0 = off
	HashWorkers        int           // 0 = min(4, NumCPU)
	SpaceMargin        uint64        // headroom beyond the estimate; 0 = 2 GiB (HEAD's backupSpaceMargin)
	RehashOnBoot       bool          // core.backup.restore.rehash_on_boot
	IntentTTL          time.Duration // core.storage.swap.intent_ttl; 0 = 24h
}

// DefaultSettings are §5B.7's defaults.
func DefaultSettings() Settings {
	return Settings{
		Enabled: true, Compression: backuparchive.Zstd, VerifyAfterCreate: true,
		VerifyKeep: 3, MaxCount: 10, MaxTotalBytes: 40 * gib, HashWorkers: defaultHashWorkers(),
	}
}

func defaultHashWorkers() int { return min(4, runtime.NumCPU()) }

// Store is the live store's administration surface: storage.Admin satisfies it.
type Store interface {
	// Checkpoint writes a consistent copy of the store into dir (which must not
	// exist and must be on the store's filesystem), private: 0700 directories,
	// 0600 files.
	Checkpoint(ctx context.Context, dir string) error
	// InstallID is the store's c/storage/install marker.
	InstallID() string
}

// Keyring is the part of *secrets.Store backup reaches: the derivation keys
// for provenance, and the key IDs a restored store's secret records need
// (§5B.3.4 step 5).
type Keyring interface {
	journal.Keys
	KeyIDs() []string
	KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error)
}

// Boundaries are the three values Create reads before its checkpoint (§5B.3.5
// step 4), so every revocation or audit record the checkpoint lacks lies above
// them.
type Boundaries struct {
	JournalSeq     uint64
	AuditHighWater uint64
	ServedBoot     *journal.ServedBoot // nil when the store has no pair
}

// Markers is Create's two journal touch points. The kernel binds it over the
// journal component and the audit store: Read before the checkpoint, Advance
// right after it (journal.Component.AdvanceServedBoot, the one writer of
// backup.location.json, DATA7-14). Create never writes the location file.
type Markers interface {
	Read(ctx context.Context) (Boundaries, error)
	Advance(ctx context.Context) (journal.ServedBoot, error)
}

// Stager is the one swap slot's stage call (restoreswap.Swapper satisfies it).
type Stager interface {
	Stage(ctx context.Context, in restorejournal.Intent) error
}

// StoreInfo is what an Inspector reads from an extracted store directory.
type StoreInfo struct {
	InstallID    string
	Levels       map[string]int
	Versions     map[string]int // per-owner applied versions, when the store has them
	PebbleFormat uint64
	Keys         map[string]int64  // exact key count per owner (VerifyOpen)
	Bytes        map[string]uint64 // key plus value bytes per owner
	LegacyFormat int               // books' legacy storage projection, 0 when none
	Generations  map[string]string // derived item -> its source generation at this store, hex
	SecretKeyIDs []string          // key IDs the secret records reference (step 5)
}

// Inspector opens an extracted store directory read-only with this build's
// guard, upgrade floors and claims, and reports what it holds. A store the
// build cannot open fails here, with the storage error (a *storage.ErrUpgradeFloor
// among them), before anything is staged.
type Inspector interface {
	Inspect(ctx context.Context, dir string) (StoreInfo, error)
}

// ConfigFile is 05-config's view of the config file, for capture (§5B.3.5
// step 5): the file itself, or its redacted copy when it carries secrets.
type ConfigFile interface {
	ConfigFileHasSecrets() bool
	RedactedConfigFile() ([]byte, error)
}

// Disk answers the two questions about the filesystem Create and staging ask:
// whether two paths share a device, and how much a path's filesystem has free.
// The zero value uses the operating system; tests replace it.
type Disk struct {
	Device func(path string) (uint64, error)
	Avail  func(path string) (uint64, error)
}

func (d Disk) device(p string) (uint64, error) {
	if d.Device != nil {
		return d.Device(p)
	}
	return osDevice(p)
}

func (d Disk) avail(p string) (uint64, error) {
	if d.Avail != nil {
		return d.Avail(p)
	}
	return osAvail(p)
}

func osDevice(p string) (uint64, error) {
	fi, err := osStat(p)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("backup: no device ID on this platform")
	}
	return uint64(st.Dev), nil
}

func osAvail(p string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// Options are everything a Service needs. Every seam is a small interface the
// kernel binds at F8-04; nothing here imports the kernel.
type Options struct {
	AppID, AppVersion, CoreVersion, BuildID string

	// Prefix is the archive file-name prefix ("audiobooks_pebble_" for ABO,
	// "<AppID>_" otherwise): only files that carry it are ever listed or pruned
	// (§5B.3.3).
	Prefix string
	// Dir, StateDir and DBPath are absolute (core.backup.dir resolved against
	// the DB's parent, core.paths.state_dir, core.storage.path).
	Dir, StateDir, DBPath string
	// ConfigPath and BootRecordPath are the live config file and boot record, as
	// the swap places them when RestoreOptions.ReplaceConfig is set.
	ConfigPath, BootRecordPath string

	Settings func() Settings // nil = DefaultSettings
	// Owners are the build's storage owners: the levels it supports and the
	// upgrade floors it declares (§5B.3.4 step 4).
	Owners       []storage.OwnerSpec
	PebbleFormat uint64            // the build's Pebble format pin; 0 = unchecked
	Modules      map[string]string // module ID -> version, recorded in the manifest
	Artifacts    func() []Artifact // the registered artifacts; nil = none

	// DerivedGeneration is a derived item's source generation in the live store
	// now: Create compares it with the checkpoint's to mark an item stale
	// (§5B.3.5 step 5). nil = no derived item is ever fresh.
	DerivedGeneration func(ctx context.Context, name string) (string, error)

	Store     Store
	Keyring   Keyring
	Markers   Markers
	Stager    Stager
	Inspector Inspector
	Config    ConfigFile // nil = the config file is not archived

	// Actor names who is acting, from the request's context (the principal's
	// user ID); "" or nil = "unknown". It is the intent's requested_by.
	Actor func(ctx context.Context) string
	// Probe is the rename pre-check's filesystem probe; nil = the real one.
	Probe restorejournal.Probe
	// Restart requests a restart after a staged restore with
	// RestoreOptions.Restart (06 Lifecycle.RequestRestart); nil = none.
	Restart func(ctx context.Context) error
	// PruneJournal drops the revocation journal's lines at or below seq, the
	// smallest boundary any retained archive records (§5B.3.5, §5B.3.6
	// "Retention"). It is not called while a pending or undo-able restore
	// exists, whose boundary is not known here. nil until backup/journal has a
	// trim.
	PruneJournal func(ctx context.Context, seq uint64) error
	// Audit records an action before it happens (core.restore.stage); a failed
	// write fails the call. nil = none.
	Audit func(ctx context.Context, action string, fields map[string]string) error
	// Metrics receives the §5B.8 series; nil = none.
	Metrics *Metrics

	Limits backuparchive.Limits // zero = the package defaults
	Disk   Disk
	Clock  func() time.Time
	Logger *slog.Logger
}

func (o *Options) settings() Settings {
	if o.Settings == nil {
		return DefaultSettings()
	}
	s := o.Settings()
	if s.MaxCount < 1 {
		s.MaxCount = 1
	}
	if s.HashWorkers < 1 {
		s.HashWorkers = defaultHashWorkers()
	}
	return s
}

func (o *Options) now() time.Time {
	if o.Clock != nil {
		return o.Clock()
	}
	return time.Now()
}

func (o *Options) log() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (o *Options) validate() error {
	var missing []string
	if o.AppID == "" {
		missing = append(missing, "AppID")
	}
	if o.Prefix == "" {
		missing = append(missing, "Prefix")
	}
	for name, p := range map[string]string{"Dir": o.Dir, "StateDir": o.StateDir, "DBPath": o.DBPath} {
		if !filepath.IsAbs(p) {
			missing = append(missing, name+" (absolute)")
		}
	}
	if o.Keyring == nil {
		missing = append(missing, "Keyring")
	}
	if o.ConfigPath != "" && o.Config == nil {
		missing = append(missing, "Config (needed to archive ConfigPath without its secrets)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("backup: options: %s", strings.Join(missing, ", "))
	}
	return nil
}

// Svc is the Service implementation. It is safe for concurrent use: List and
// Verify of other archives run beside each other; Create, Prune, StageRestore
// and Rewrap hold the one backup resource and refuse with ErrBackupInProgress
// while another holder has it (§5B.6; the tasks layer holds the same resource
// by declaration, so this is the guard for direct callers such as Ensure).
type Svc struct {
	o   Options
	hold sync.Mutex // the backup resource

	cache sync.Map // classification cache: name -> cachedKind
}

// New builds the Service. It validates the options and creates nothing.
func New(o Options) (*Svc, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	return &Svc{o: o}, nil
}

// Compile-time proof that Svc is the Service, the Ensurer and the Runner.
var (
	_ Service = (*Svc)(nil)
	_ Ensurer = (*Svc)(nil)
	_ Runner  = (*Svc)(nil)
)

// acquire takes the backup resource or fails with ErrBackupInProgress.
func (s *Svc) acquire() (release func(), err error) {
	if !s.hold.TryLock() {
		return nil, ErrBackupInProgress
	}
	return s.hold.Unlock, nil
}

var errNotRegular = errors.New("backup: not a regular file")

var _ fs.FileInfo = nil
