// file: core/storage/boot.go
// version: 1.0.1
// guid: 34d5477f-2cd4-4e60-b7cc-876d94c85db0
// last-edited: 2026-11-08

package storage

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// OwnerSpec is one owner of the build and what it declares.
type OwnerSpec struct {
	Owner Owner
	Spec  Spec
	// Enabled is false for a disabled module (G1): its claims are still
	// validated and recorded in the claim ledger and its keyspace stays
	// reserved, but it gets no handle and runs no migration (§5S.4).
	Enabled bool
}

// StateFile is one file the pre-migration checkpoint copies beside the store
// (05-config §2.4's state-file table): its label and resolved absolute path.
// A file that does not exist is skipped (the keyring before its first
// secrets.Open); one that exists and cannot be read halts boot before any
// write.
type StateFile struct {
	Label string
	Path  string
}

// OpenOptions is what the kernel passes to Open: the resolved path, every
// owner of the build, and the core.storage.* values (§5S.7). Zero values take
// the documented defaults.
type OpenOptions struct {
	Path            string        // core.storage.path: the DB directory, absolute
	Owners          []OwnerSpec   // every owner of the build, enabled or not; core/storage is implicit
	Build           string        // the binary ID (§5S.3.7 "Binary ID")
	FromRelease     string        // the predecessor release's tag the build embeds; "" when none (§5S.3.12 "How")
	StateFiles      []StateFile   // copied into the checkpoint
	CheckpointDir   string        // core.storage.checkpoint_dir: the directory that holds .mediacore-checkpoints/, absolute; "" = the DB's parent
	FreeSpaceMargin uint64        // core.storage.checkpoint.free_space_margin; 0 = 2 GiB
	InstallID       string        // a restore boot's hand-over for a marker-less store; "" = none
	CacheBytes      int64         // core.storage.cache_bytes; 0 = Pebble's default
	MemtableBytes   int64         // core.storage.memtable_bytes; 0 = Pebble's default
	MaxBatchBytes   int64         // core.storage.max_batch_bytes; 0 = 256 MiB
	SequenceLease   uint64        // core.storage.sequence.lease; 0 = 64
	MigrationBytes  int64         // core.storage.migration.max_batch_bytes; 0 = 64 MiB
	MigrationOps    int           // core.storage.migration.max_batch_ops; 0 = 250 000
	CloseTimeout    time.Duration // core.storage.close_timeout; 0 = 30 s
	ClaimsShadow    bool          // core.storage.claims.mode=shadow (§5S.0 D-S2)
	Logger          *slog.Logger  // nil discards
	ExtendStart     func(step string)
	Clock           func() time.Time // nil = time.Now
	FS              FS               // the filesystem the store opens on; zero = the OS's (tests: MemFS)

	// fs is FS's filesystem, else the OS's (defaults).
	fs vfs.FS

	// Seams for this package's tests; zero in every other caller.
	freeSpace   func(dir string) (uint64, error)
	dirIdentity func(path string) (dev, ino uint64, err error)
	sameDevice  func(a, b string) (bool, error)
	chmod       func(path string, dir bool) error
	hooks       testHooks

	// cpDir is the checkpoint directory itself:
	// <CheckpointDir or the DB's parent>/.mediacore-checkpoints.
	cpDir string
}

const (
	defaultMargin         = 2 << 30
	defaultMaxBatchBytes  = 256 << 20
	defaultSequenceLease  = 64
	defaultMigrationBytes = 64 << 20
	defaultMigrationOps   = 250_000
	defaultCloseTimeout   = 30 * time.Second
	checkpointRetention   = 7 * 24 * time.Hour
)

// FS is the filesystem a store opens on. The zero value is the OS's; MemFS
// is the one other, for tests. It keeps Pebble's vfs inside this package:
// app.Options and kernel.Config carry an FS, never a vfs.FS.
type FS struct{ fs vfs.FS }

// MemFS returns a new, empty in-memory filesystem (Pebble's vfs.NewMem). A
// store on it lives as long as the value and is never durable: tests only,
// such as coretest's harness (06 §6.4.8).
func MemFS() FS { return FS{fs: vfs.NewMem()} }

// defaults resolves the zero values. The filesystem is the first set of: o.fs
// as defaults finds it (the in-package slot a boot seam fills, such as a
// fault-injecting filesystem taken from the context, which must see every
// write), FS, and the OS's. A seam fills o.fs only when FS is zero, so an
// explicit FS still wins over the context.
func (o *OpenOptions) defaults() {
	if o.fs == nil {
		o.fs = o.FS.fs
	}
	if o.fs == nil {
		o.fs = vfs.Default
	}
	if o.FreeSpaceMargin == 0 {
		o.FreeSpaceMargin = defaultMargin
	}
	if o.MaxBatchBytes <= 0 {
		o.MaxBatchBytes = defaultMaxBatchBytes
	}
	if o.SequenceLease == 0 {
		o.SequenceLease = defaultSequenceLease
	}
	if o.MigrationBytes <= 0 {
		o.MigrationBytes = defaultMigrationBytes
	}
	if o.MigrationOps <= 0 {
		o.MigrationOps = defaultMigrationOps
	}
	if o.CloseTimeout <= 0 {
		o.CloseTimeout = defaultCloseTimeout
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.ExtendStart == nil {
		o.ExtendStart = func(string) {}
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	parent := o.CheckpointDir
	if parent == "" {
		parent = filepath.Dir(o.Path)
	}
	o.cpDir = filepath.Join(parent, checkpointLeaf)
	fs := o.fs
	if o.freeSpace == nil {
		o.freeSpace = func(dir string) (uint64, error) {
			u, err := fs.GetDiskUsage(dir)
			return u.AvailBytes, err
		}
	}
	if o.dirIdentity == nil {
		o.dirIdentity = osDirIdentity(fs)
	}
	if o.sameDevice == nil {
		o.sameDevice = osSameDevice(fs)
	}
	if o.chmod == nil {
		o.chmod = osChmod(fs)
	}
}

// ---- c/storage/ records (§5S.3.7 table, §5S.3.5 "Bookkeeping") ----

type installRecord struct {
	InstallID string    `json:"install_id"`
	Created   time.Time `json:"created"`
	Build     string    `json:"build"`
}

// epochRecord is c/storage/epoch, the store generation (§5S.3.7). DirDev and
// DirIno are the DB directory's identity (osDirIdentity): the filesystem ID
// statfs reports, not st_dev (the key keeps the dir_dev name), and the inode.
type epochRecord struct {
	Epoch  uint64    `json:"epoch"`
	DirDev uint64    `json:"dir_dev"`
	DirIno uint64    `json:"dir_ino"`
	At     time.Time `json:"at"`
}

type stampRecord struct {
	Level   int       `json:"level"`
	ByBuild string    `json:"by_build"`
	At      time.Time `json:"at"`
}

type appliedRecord struct {
	Name        string    `json:"name"`
	Compat      string    `json:"compat,omitempty"`
	RaisesLevel int       `json:"raises_level"`
	AppliedAt   time.Time `json:"applied_at"`
	Build       string    `json:"build"`
}

// attemptRecord is c/storage/attempt: written by step 9's batch on a boot
// that took or reused a checkpoint, deleted once that checkpoint is
// completed. A key a boot finds names an attempt whose steps all committed
// but whose completion a kill cut off (review BLOCKER-2).
type attemptRecord struct {
	Checkpoint string `json:"checkpoint"`
}

func codecAttempt() codec.Codec[attemptRecord] { return codec.JSON[attemptRecord](1) }

type dmRecord struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
	Build string    `json:"build"`
}

// Relative keys of storage's own keyspace c/storage/ (notation §5S.3.3).
func installKey() []byte      { return keys.Join(keys.Str("install")) }
func epochKey() []byte        { return keys.Join(keys.Str("epoch")) }
func attemptKey() []byte      { return keys.Join(keys.Str("attempt")) }
func levelPrefix() []byte     { return keys.Join(keys.Str("level")) }
func levelKey(o Owner) []byte { return keys.Join(keys.Str("level"), keys.Str(string(o))) }
func versionKey(o Owner) []byte {
	return keys.Join(keys.Str("ks"), keys.Str(string(o)), keys.Str("version"))
}
func appliedPrefix(o Owner) []byte {
	return keys.Join(keys.Str("ks"), keys.Str(string(o)), keys.Str("applied"))
}
func claimsPrefix() []byte     { return keys.Join(keys.Str("claims")) }
func claimsKey(o Owner) []byte { return keys.Join(keys.Str("claims"), keys.Str(string(o))) }
func dmKey(id string) []byte   { return keys.Join(keys.Str("dm"), keys.Str(id)) }
func appliedKey(o Owner, v int) []byte {
	return keys.Join(keys.Str("ks"), keys.Str(string(o)), keys.Str("applied"), keys.U64(uint64(v)))
}

// ---- the boot ----

// bootState is what Open read and Prepare and Migrate advance.
type bootState struct {
	opts      OpenOptions
	fresh     bool // an empty store: a fresh install is planned (§5S.4 step 5)
	marker    bool
	installID string
	epoch     epochRecord // the generation this boot runs at
	epochNew  bool        // written at step 9 (or in the fresh-install batch)
	stamps    Levels      // committed stamps, every owner found in the store
	supported Levels      // the build's supported level per level owner
	versions  map[Owner]int
	applied   map[Owner]map[int]AppliedStep
	ledgers   map[Owner]claimLedger
	plan      []*stepNode // runnable pending steps, in DAG order
	deferred  []*stepNode
	needsCP   bool // the boot will write something a restore point must cover
	cpName    string
	attempt   string // c/storage/attempt's checkpoint, read at step 5: an attempt a kill left uncompleted
	prepared  bool
	migrated  bool
	finished  bool // Migrate ran every step and step 9: the boot may reach readiness
}

// Opened is a store after §5S.4 steps 1-5 (06 P2): open, guarded and
// validated, with nothing written. Prepare is P4a.
type Opened struct {
	db *DB
}

// Open runs §5S.4 steps 1-5 (06 P2): it opens the store without a format
// version, reads the level stamps, sidecars, legacy projection, install
// marker, store generation and bookkeeping, applies the guard and the
// upgrade floor, validates every spec and claim, checks a marker-less store
// for foreign keys and plans the migration DAG. It writes nothing to the
// store; every refusal leaves the store as it found it.
func Open(ctx context.Context, opts OpenOptions) (*Opened, error) {
	opts.defaults()
	if !filepath.IsAbs(opts.Path) {
		return nil, fmt.Errorf("storage: core.storage.path %q is not absolute", opts.Path)
	}
	if err := opts.checkCheckpointDir(); err != nil {
		return nil, err
	}
	owners, claims, refs, err := register(opts.Owners)
	if err != nil {
		return nil, err
	}
	opts.ExtendStart("storage.open")
	eng, err := ipebble.Open(opts.Path, ipebble.Options{
		FS: opts.fs, CacheBytes: opts.CacheBytes, MemtableBytes: opts.MemtableBytes, Logger: opts.Logger,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	db := newDB(eng, opts, owners, claims, false)
	st := &bootState{opts: opts}
	db.boot = st
	if err := db.readBoot(ctx, refs); err != nil {
		return nil, errors.Join(err, eng.Close(opts.CloseTimeout))
	}
	return &Opened{db: db}, nil
}

func newDB(eng *ipebble.Engine, opts OpenOptions, owners map[Owner]*ownerInfo, claims *claimIndex, readOnly bool) *DB {
	return &DB{
		eng: eng, log: opts.Logger, clock: opts.Clock, readOnly: readOnly,
		set: settings{
			maxBatchBytes: opts.MaxBatchBytes, seqLease: opts.SequenceLease, migMaxBytes: opts.MigrationBytes,
			migMaxOps: opts.MigrationOps, closeTimeout: opts.CloseTimeout, shadow: opts.ClaimsShadow,
		},
		owners: owners, claims: claims, hooks: opts.hooks,
	}
}

// Close closes a store Prepare was never called on.
func (o *Opened) Close() error { return o.db.Close() }

// Fresh reports whether the store is empty, so Prepare will run a fresh
// install.
func (o *Opened) Fresh() bool { return o.db.boot.fresh }

// Levels is the committed level stamp of every owner the store holds.
func (o *Opened) Levels() Levels { return maps0(o.db.boot.stamps) }

// storageKS is storage's own keyspace c/storage/.
func (db *DB) storageKS() Keyspace {
	ks, _ := db.Keyspace(storageOwner)
	return ks
}

// readBoot is steps 2-5: every read the guard, the floor, validation and the
// marker decision need, and the plan.
func (db *DB) readBoot(ctx context.Context, refs []claimRef) error {
	st := db.boot
	ks := db.storageKS()
	empty, err := db.isEmpty()
	if err != nil {
		return err
	}
	st.fresh = empty
	if st.stamps, err = db.readStamps(ctx); err != nil {
		return err
	}
	st.supported = db.supportedLevels()
	if st.versions, st.applied, err = db.readBookkeeping(ctx); err != nil {
		return err
	}
	// Step 3: the guard, then the newer-on-disk consistency check, then the
	// upgrade floor. Every refusal is listed.
	var refusals []error
	refusals = append(refusals, db.guard()...)
	refusals = append(refusals, db.newerOnDisk()...)
	if !st.fresh {
		refusals = append(refusals, db.floors(ctx)...)
	}
	if len(refusals) > 0 {
		return errors.Join(refusals...)
	}
	// Step 4: claim orphans against the ledger.
	if st.ledgers, err = db.readLedgers(ctx); err != nil {
		return err
	}
	inBuild := make(map[Owner]bool, len(db.owners))
	for o := range db.owners {
		inBuild[o] = true
	}
	if probs := orphanProblems(refs, st.ledgers, inBuild); len(probs) > 0 {
		return &ClaimConflictError{Problems: probs}
	}
	// Step 5: the install marker and the store generation.
	var mk installRecord
	switch raw, err := ks.Get(ctx, installKey()); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return err
	default:
		if mk, err = codec.JSON[installRecord](1).Decode(raw); err != nil {
			return fmt.Errorf("storage: install marker unreadable: %w", err)
		}
		st.marker, st.installID = true, mk.InstallID
	}
	if !st.marker && !st.fresh {
		if err := db.foreignKeys(); err != nil {
			return err
		}
	}
	switch raw, err := ks.Get(ctx, attemptKey()); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return err
	default:
		rec, err := codecAttempt().Decode(raw)
		if err != nil {
			return fmt.Errorf("storage: attempt record unreadable: %w", err)
		}
		st.attempt = rec.Checkpoint
	}
	if err := db.readEpoch(ctx); err != nil {
		return err
	}
	switch {
	case st.marker:
	case st.fresh:
		// An empty store is a new install. It takes no checkpoint, so it has
		// no record to keep an ID for, and inheriting one from a record left
		// beside it (a store set aside by a reset, or deleted by hand) would
		// let its first upgrade adopt the old store's copy and keep that copy
		// as its rollback (review BLOCKER-3).
		st.installID = st.opts.InstallID
		if st.installID == "" {
			st.installID = newInstallID()
		}
	default:
		id, err := db.chooseInstallID()
		if err != nil {
			return err
		}
		st.installID = id
	}
	if err := db.planDAG(ctx); err != nil {
		return err
	}
	st.needsCP = !st.fresh && (db.eng.NeedsRatchet() || len(st.plan) > 0 || !st.marker)
	return nil
}

func maps0(l Levels) Levels {
	out := make(Levels, len(l))
	maps.Copy(out, l)
	return out
}

// isEmpty reports a store that holds no key at all.
func (db *DB) isEmpty() (bool, error) {
	release, err := db.lease()
	if err != nil {
		return false, err
	}
	defer release()
	it, err := db.eng.DB().NewIter(nil)
	if err != nil {
		return false, fmt.Errorf("storage: iterator: %w", err)
	}
	empty := !it.First()
	if err := errors.Join(it.Error(), it.Close()); err != nil {
		return false, fmt.Errorf("storage: iterator: %w", err)
	}
	return empty, nil
}

// readStamps reads c/storage/level/<owner> for every owner in the store. An
// unreadable stamp fails closed.
func (db *DB) readStamps(ctx context.Context) (Levels, error) {
	out := Levels{}
	ks := db.storageKS()
	for kv, err := range ks.ScanPrefix(ctx, levelPrefix()) {
		if err != nil {
			return nil, err
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 2 {
			return nil, fmt.Errorf("storage: level stamp key %q: %w", kv.Key, ErrBookkeepingUnreadable)
		}
		rec, err := codec.JSON[stampRecord](1).Decode(kv.Value)
		if err != nil || rec.Level < 0 {
			return nil, fmt.Errorf("storage: level stamp of %s unreadable (%v): %w", segs[1], err, ErrBookkeepingUnreadable)
		}
		out[Owner(segs[1])] = rec.Level
	}
	return out, nil
}

// supportedLevels is the build's supported level per level owner: "core" is
// the highest Level any core subsystem declares, a module its own.
func (db *DB) supportedLevels() Levels {
	out := Levels{coreLevelOwner: 0}
	for _, o := range db.owners {
		lo := o.levelOwner()
		out[lo] = max(out[lo], o.spec.Level)
	}
	return out
}

// guard is §5S.3.7 at open: max(stamp, sidecar) per owner against the build,
// and the legacy projection's sidecar against the value the build would write.
func (db *DB) guard() []error {
	st := db.boot
	fs := st.opts.fs
	side, sideOK, err := ipebble.ReadLevels(fs, st.opts.Path)
	if err != nil {
		db.log.Error("storage: level sidecar unreadable; it is rewritten from the stamps at the end of this boot",
			"path", st.opts.Path+ipebble.LevelsSuffix, "err", err)
		sideOK = false
	}
	g := ipebble.Guard{StampSource: "stamp", SidecarSource: "sidecar", Compare: ipebble.MinReader}
	var errs []error
	for _, lo := range sortedOwners(st.supported) {
		stamp, stampOK := st.stamps[lo]
		sv, svOK := side[string(lo)]
		d := g.Decide(stamp, stampOK, sv, sideOK && svOK, st.supported[lo])
		if d.Disagree {
			db.log.Warn("storage: level stamp and sidecar disagree", "owner", string(lo), "stamp", stamp, "sidecar", sv)
		}
		if d.TooNew {
			errs = append(errs, &LevelTooNewError{Owner: lo, Store: d.Store, Supported: st.supported[lo], Source: d.Source})
		}
	}
	for lo, lvl := range st.stamps {
		if _, ok := st.supported[lo]; !ok {
			db.log.Info("storage: owner present in the store but not in the build; left dormant", "owner", string(lo), "level", lvl)
		}
	}
	lg := ipebble.Guard{StampSource: "legacy-stamp", SidecarSource: "legacy-stamp", Compare: ipebble.LegacyExact}
	for _, o := range sortedInfos(db.owners) {
		gp := o.spec.LegacyGuard
		if gp == nil {
			continue
		}
		side := st.opts.Path + gp.SidecarSuffix
		v, ok, err := ipebble.ReadInt(fs, side)
		if err != nil {
			// The sidecar is this guard's only input: skipping it would let
			// a store a newer build converted pass, and step 9 would then
			// overwrite the evidence (review MINOR-3). Absent is ok == false,
			// which is why deleting it is the remedy only when no newer build
			// has opened the store.
			errs = append(errs, fmt.Errorf("storage: %s's legacy projection sidecar %s is unreadable, so a store too new for this build cannot be ruled out. "+
				"Make it readable by the service's user; if its content is damaged, restore it from a backup, or delete it only if no build newer than this one "+
				"has opened this store (this build then rewrites it): %w", o.owner, side, err))
			continue
		}
		would := gp.projection(st.supported[o.levelOwner()])
		if d := lg.Decide(0, false, v, ok, would); d.TooNew {
			errs = append(errs, &LevelTooNewError{Owner: o.owner, Store: d.Store, Supported: would, Source: d.Source})
		}
	}
	return errs
}

// readBookkeeping reads every registered owner's applied version and
// records: its VersionSource, else c/storage/ks/<owner>/. Unreadable is an
// error, never 0 (§5S.3.5 "Unreadable version").
func (db *DB) readBookkeeping(ctx context.Context) (map[Owner]int, map[Owner]map[int]AppliedStep, error) {
	versions := map[Owner]int{}
	applied := map[Owner]map[int]AppliedStep{}
	for _, o := range sortedInfos(db.owners) {
		if len(o.spec.Migrations) == 0 && len(o.spec.Legacy) == 0 && o.spec.VersionSource == nil {
			continue
		}
		var v int
		var recs map[int]AppliedStep
		var err error
		if vs := o.spec.VersionSource; vs != nil {
			lr, _ := db.Legacy(o.owner)
			v, recs, err = vs.Applied(ctx, lr)
			if err != nil {
				return nil, nil, fmt.Errorf("storage: %s's version source: %w: %w", o.owner, ErrBookkeepingUnreadable, err)
			}
		} else if v, recs, err = db.nativeBookkeeping(ctx, o.owner); err != nil {
			return nil, nil, err
		}
		if v < 0 {
			return nil, nil, fmt.Errorf("storage: %s's applied version %d: %w", o.owner, v, ErrBookkeepingUnreadable)
		}
		versions[o.owner], applied[o.owner] = v, recs
	}
	return versions, applied, nil
}

// nativeBookkeeping reads c/storage/ks/<owner>/version and its applied records.
func (db *DB) nativeBookkeeping(ctx context.Context, o Owner) (int, map[int]AppliedStep, error) {
	ks := db.storageKS()
	v := 0
	switch raw, err := ks.Get(ctx, versionKey(o)); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return 0, nil, err
	case len(raw) != 8:
		return 0, nil, fmt.Errorf("storage: %s's version record is %d bytes, want 8: %w", o, len(raw), ErrBookkeepingUnreadable)
	default:
		n := binary.BigEndian.Uint64(raw)
		if n > 1<<31 {
			return 0, nil, fmt.Errorf("storage: %s's version %d: %w", o, n, ErrBookkeepingUnreadable)
		}
		v = int(n)
	}
	recs := map[int]AppliedStep{}
	for kv, err := range ks.ScanPrefix(ctx, appliedPrefix(o)) {
		if err != nil {
			return 0, nil, err
		}
		segs, serr := keys.Split(kv.Key)
		rec, derr := codec.JSON[appliedRecord](1).Decode(kv.Value)
		if serr != nil || len(segs) != 4 || len(segs[3]) != 8 || derr != nil {
			return 0, nil, fmt.Errorf("storage: %s's applied record %q unreadable: %w", o, kv.Key, ErrBookkeepingUnreadable)
		}
		n := int(binary.BigEndian.Uint64(segs[3]))
		step := AppliedStep{Version: n, Name: rec.Name, RaisesLevel: rec.RaisesLevel, AppliedAt: rec.AppliedAt, Build: rec.Build}
		if rec.Compat != "" {
			step.Compat, step.HasCompat = parseCompat(rec.Compat), true
		}
		recs[n] = step
	}
	return v, recs, nil
}

// newerOnDisk is §5S.3.5 "Newer version on disk": applied steps above the
// build's highest registered version. All Additive: a warning. A Breaking or
// compat-less one above the supported level: ErrKeyspaceTooNew.
func (db *DB) newerOnDisk() []error {
	st := db.boot
	var errs []error
	for _, o := range sortedInfos(db.owners) {
		have, ok := st.versions[o.owner]
		if !ok {
			continue
		}
		top := highestVersion(o.spec)
		supported := st.supported[o.levelOwner()]
		for v := top + 1; v <= have; v++ {
			rec, ok := st.applied[o.owner][v]
			if !ok {
				errs = append(errs, fmt.Errorf("storage: %s is at version %d above this build's %d with no applied record for %d: %w",
					o.owner, have, top, v, ErrBookkeepingUnreadable))
				break
			}
			if rec.HasCompat && rec.Compat == Additive {
				db.log.Warn("storage: store holds an Additive step this build does not know; continuing",
					"owner", string(o.owner), "version", v, "name", rec.Name)
				continue
			}
			if rec.RaisesLevel > supported {
				errs = append(errs, fmt.Errorf("storage: %s step %d %q (breaking, raises level to %d; this build serves %d): %w",
					o.owner, v, rec.Name, rec.RaisesLevel, supported, ErrKeyspaceTooNew))
			}
		}
	}
	return errs
}

// floors is the UpgradeFloor check of a non-empty store (§5S.4 step 3).
func (db *DB) floors(ctx context.Context) []error {
	st := db.boot
	var errs []error
	for _, o := range sortedInfos(db.owners) {
		f := o.spec.UpgradeFloor
		if f == nil {
			continue
		}
		have := st.versions[f.Owner]
		var missing []StepRef
		for _, r := range f.Requires {
			done, err := db.stepDone(ctx, r)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !done {
				missing = append(missing, r)
			}
		}
		if have < f.Version || len(missing) > 0 {
			errs = append(errs, &ErrUpgradeFloor{Owner: f.Owner, Have: have, Floor: f.Version, Missing: missing, InstallFirst: f.InstallFirst})
		}
	}
	return errs
}

// stepDone reports whether a step is applied (versioned) or done (data
// migration) in this store.
func (db *DB) stepDone(ctx context.Context, r StepRef) (bool, error) {
	if r.DataMigration != "" {
		raw, err := db.storageKS().Get(ctx, dmKey(r.String()))
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		rec, err := codec.JSON[dmRecord](1).Decode(raw)
		if err != nil {
			return false, fmt.Errorf("storage: data migration record %s: %w", r, ErrBookkeepingUnreadable)
		}
		return rec.State == "done", nil
	}
	v, err := db.appliedVersion(ctx, r.Owner)
	return r.Version <= v, err
}

// appliedVersion is an owner's applied version: the boot's reading for a
// registered owner, the native bookkeeping for an absent one.
func (db *DB) appliedVersion(ctx context.Context, o Owner) (int, error) {
	if v, ok := db.boot.versions[o]; ok {
		return v, nil
	}
	if _, ok := db.owners[o]; ok {
		return 0, nil
	}
	v, _, err := db.nativeBookkeeping(ctx, o)
	return v, err
}

// readLedgers reads c/storage/claims/<owner> for every owner on disk.
func (db *DB) readLedgers(ctx context.Context) (map[Owner]claimLedger, error) {
	out := map[Owner]claimLedger{}
	for kv, err := range db.storageKS().ScanPrefix(ctx, claimsPrefix()) {
		if err != nil {
			return nil, err
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 2 {
			return nil, fmt.Errorf("storage: claim ledger key %q unreadable", kv.Key)
		}
		l, err := codec.JSON[claimLedger](1).Decode(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("storage: claim ledger of %s unreadable: %w", segs[1], err)
		}
		out[Owner(segs[1])] = l
	}
	return out, nil
}

// ErrForeignKeys: a store without the install marker holds keys under c/ or
// m/ (§5S.3.4 rule 4): an unregistered writer put them there.
var ErrForeignKeys = errors.New("storage: keys under c/ or m/ on a store without an install marker")

// ForeignKeysError carries a sample of the foreign keys.
type ForeignKeysError struct{ Sample []string }

func (e *ForeignKeysError) Error() string {
	return fmt.Sprintf("storage: keys under c/ or m/ on a store without an install marker (sample: %s)", strings.Join(e.Sample, ", "))
}

func (e *ForeignKeysError) Is(target error) bool { return target == ErrForeignKeys }

// foreignKeys verifies [c/, c0) and [m/, m0) are empty (read only).
func (db *DB) foreignKeys() error {
	release, err := db.lease()
	if err != nil {
		return err
	}
	defer release()
	var sample []string
	for _, r := range []Range{{Start: []byte("c/"), End: []byte("c0")}, {Start: []byte("m/"), End: []byte("m0")}} {
		it, err := db.eng.DB().NewIter(&cpebble.IterOptions{LowerBound: r.Start, UpperBound: r.End})
		if err != nil {
			return fmt.Errorf("storage: iterator: %w", err)
		}
		for ok := it.First(); ok && len(sample) < 5; ok = it.Next() {
			sample = append(sample, fmt.Sprintf("%q", it.Key()))
		}
		if err := errors.Join(it.Error(), it.Close()); err != nil {
			return fmt.Errorf("storage: iterator: %w", err)
		}
	}
	if len(sample) > 0 {
		return &ForeignKeysError{Sample: sample}
	}
	return nil
}

// readEpoch reads the store generation and the directory identity: a record
// that is absent or names another directory makes this boot's generation
// epoch+1, written at step 9 (§5S.3.7 "Store generation").
func (db *DB) readEpoch(ctx context.Context) error {
	st := db.boot
	dev, ino, err := st.opts.dirIdentity(st.opts.Path)
	if err != nil {
		return fmt.Errorf("storage: DB directory identity: %w", err)
	}
	var rec epochRecord
	found := false
	switch raw, err := db.storageKS().Get(ctx, epochKey()); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return err
	default:
		if rec, err = codec.JSON[epochRecord](1).Decode(raw); err != nil {
			return fmt.Errorf("storage: store generation unreadable: %w", err)
		}
		found = true
	}
	if found && rec.DirDev == dev && rec.DirIno == ino {
		st.epoch = rec
		return nil
	}
	st.epoch = epochRecord{Epoch: rec.Epoch + 1, DirDev: dev, DirIno: ino, At: st.opts.Clock().UTC()}
	st.epochNew = true
	return nil
}

// chooseInstallID is step 5's choice for a non-empty marker-less store (a
// HEAD store, or a crash between the checkpoint and the marker): the restore
// boot's hand-over, else the install ID of an open checkpoint record of this
// store (the same epoch and DB directory identity, ofStore), else a new one.
func (db *DB) chooseInstallID() (string, error) {
	st := db.boot
	if st.opts.InstallID != "" {
		return st.opts.InstallID, nil
	}
	// Unreadable records fail the boot before any write: guessing a new ID
	// would orphan the open record and take a new full checkpoint each boot.
	recs, err := db.checkpointRecords()
	if err != nil {
		return "", err
	}
	for _, r := range recs {
		if r.rec.ofStore(st.epoch) && r.rec.open() {
			return r.rec.InstallID, nil
		}
	}
	return newInstallID(), nil
}

func newInstallID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Unreachable: crypto/rand.Read never returns an error (it crashes
		// the program instead, Go 1.24 and later).
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Prepare runs §5S.4 steps 6-7 (06 P4a): a fresh install's one batch and its
// sidecars, or the pre-migration checkpoint (taken or reused) followed by the
// install marker as the first store write after it; then the format ratchet.
// First it completes the checkpoint of an attempt a kill stranded after step
// 9's batch (c/storage/attempt), which writes checkpoint files only; the key
// is deleted after the checkpoint.
// The returned DB is ready for Migrate.
func (o *Opened) Prepare(ctx context.Context) (*DB, error) {
	db := o.db
	st := db.boot
	if st.prepared {
		return nil, errors.New("storage: Prepare called twice")
	}
	st.prepared = true
	// A stranded attempt is completed before the checkpoint decision, so a
	// later upgrade never reuses it; its key is deleted only after the
	// checkpoint, which therefore still holds every row as it was.
	if err := db.completeStranded(); err != nil {
		return nil, err
	}
	switch {
	case st.fresh:
		if err := db.freshInstall(ctx); err != nil {
			return nil, err
		}
	case st.needsCP:
		if err := db.takeCheckpoint(ctx); err != nil {
			return nil, err
		}
		if !st.marker {
			if err := db.writeMarker(ctx); err != nil {
				return nil, err
			}
		}
	}
	if st.attempt != "" {
		if err := db.storageKS().Delete(ctx, attemptKey()); err != nil {
			return nil, fmt.Errorf("storage: clearing the completed attempt: %w", err)
		}
		st.attempt = ""
	}
	if db.eng.NeedsRatchet() {
		if err := db.eng.Ratchet(); err != nil {
			return nil, fmt.Errorf("storage: %w", err)
		}
	}
	return db, nil
}

func (db *DB) markerRecord() installRecord {
	st := db.boot
	return installRecord{InstallID: st.installID, Created: st.opts.Clock().UTC(), Build: st.opts.Build}
}

// writeMarker commits the install marker alone: the first write after the
// checkpoint on a marker-less store (§5S.3.4 rule 4).
func (db *DB) writeMarker(ctx context.Context) error {
	raw, err := codec.JSON[installRecord](1).Encode(db.markerRecord())
	if err != nil {
		return err
	}
	if err := db.storageKS().Set(ctx, installKey(), raw); err != nil {
		return fmt.Errorf("storage: install marker: %w", err)
	}
	db.boot.marker = true
	return nil
}

// freshInstall is §5S.4 step 6 on an empty store: ONE batch with the marker,
// the store generation, every owner's stamp at the build's level, the applied
// records below each owner's lowest Seed step, `done` for every step a
// declared floor's Requires names, and the claim ledger; then the sidecars.
// A crash before the batch leaves an empty store.
func (db *DB) freshInstall(ctx context.Context) (err error) {
	st := db.boot
	now := st.opts.Clock().UTC()
	ks := db.storageKS()
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := setEnc(b, installKey(), codec.JSON[installRecord](1), db.markerRecord()); err != nil {
		return err
	}
	ep := st.epoch
	ep.Epoch, ep.At = 1, now
	if err := setEnc(b, epochKey(), codec.JSON[epochRecord](1), ep); err != nil {
		return err
	}
	for _, lo := range sortedOwners(st.supported) {
		rec := stampRecord{Level: st.supported[lo], ByBuild: st.opts.Build, At: now}
		if err := setEnc(b, levelKey(lo), codec.JSON[stampRecord](1), rec); err != nil {
			return err
		}
	}
	versions := map[Owner]int{}
	for _, o := range sortedInfos(db.owners) {
		upTo := freshAppliedThrough(o.spec)
		if upTo == 0 {
			continue
		}
		for _, step := range appliedSteps(o.spec, upTo, now, st.opts.Build) {
			if err := db.stageApplied(ctx, b, o, step); err != nil {
				return err
			}
		}
		versions[o.owner] = upTo
	}
	for _, o := range sortedInfos(db.owners) {
		if f := o.spec.UpgradeFloor; f != nil {
			for _, r := range f.Requires {
				if r.DataMigration == "" {
					continue
				}
				if err := setEnc(b, dmKey(r.String()), codec.JSON[dmRecord](1), dmRecord{State: "done", At: now, Build: st.opts.Build}); err != nil {
					return err
				}
			}
		}
	}
	if err := db.stageLedgers(b, now, true); err != nil {
		return err
	}
	if err := b.Commit(ctx, Sync); err != nil {
		return fmt.Errorf("storage: fresh install: %w", err)
	}
	st.marker, st.epoch, st.epochNew = true, ep, false
	maps.Copy(st.stamps, st.supported)
	for o, v := range versions {
		st.versions[o] = v
		st.applied[o] = map[int]AppliedStep{}
		for _, step := range appliedSteps(db.owners[o].spec, v, now, st.opts.Build) {
			st.applied[o][step.Version] = step
		}
	}
	if err := db.writeSidecars(); err != nil {
		return err
	}
	return db.planDAG(ctx)
}

func enc[T any](c codec.Codec[T], v T) ([]byte, error) { return c.Encode(v) }

// setEnc encodes v and stages it at key through b's origin.
func setEnc[T any](b *Batch, key []byte, c codec.Codec[T], v T) error {
	raw, err := c.Encode(v)
	if err != nil {
		return err
	}
	return b.Set(key, raw)
}

// freshAppliedThrough is the highest version a fresh install records
// applied: every step below the owner's lowest Seed step (all of them without
// one), legacy steps included.
func freshAppliedThrough(s Spec) int {
	top := highestVersion(s)
	for _, m := range s.Migrations {
		if m.Seed && m.Version-1 < top {
			top = m.Version - 1
		}
	}
	return top
}

// appliedSteps are the records of an owner's steps 1..upTo.
func appliedSteps(s Spec, upTo int, now time.Time, build string) []AppliedStep {
	var out []AppliedStep
	for _, l := range s.Legacy {
		if l.Version <= upTo {
			out = append(out, AppliedStep{Version: l.Version, Name: l.Name, AppliedAt: now, Build: build})
		}
	}
	for _, m := range s.Migrations {
		if m.Version <= upTo {
			out = append(out, AppliedStep{Version: m.Version, Name: m.Name, Compat: m.Compat, HasCompat: true,
				RaisesLevel: m.RaisesLevel, AppliedAt: now, Build: build})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out
}

// stageApplied stages one step's bookkeeping: through the owner's
// VersionSource (its LegacyRange bound to b), else the native version and
// applied record.
func (db *DB) stageApplied(ctx context.Context, b *Batch, o *ownerInfo, step AppliedStep) error {
	if vs := o.spec.VersionSource; vs != nil {
		lr, _ := db.Legacy(o.owner)
		b.Bind(lr)
		return vs.Stage(ctx, lr, b, step)
	}
	ks := db.storageKS()
	b.Bind(ks)
	w := b.In(ks)
	if err := w.Set(versionKey(o.owner), binary.BigEndian.AppendUint64(nil, uint64(step.Version))); err != nil {
		return err
	}
	rec := appliedRecord{Name: step.Name, RaisesLevel: step.RaisesLevel, AppliedAt: step.AppliedAt, Build: step.Build}
	if step.HasCompat {
		rec.Compat = step.Compat.String()
	}
	raw, err := codec.JSON[appliedRecord](1).Encode(rec)
	if err != nil {
		return err
	}
	return w.Set(appliedKey(o.owner, step.Version), raw)
}

// stageLedgers stages c/storage/claims/<owner> for every owner of the build
// whose ledger is absent or differs (all of them when force).
func (db *DB) stageLedgers(b *Batch, now time.Time, force bool) error {
	st := db.boot
	ks := db.storageKS()
	b.Bind(ks)
	for _, o := range sortedInfos(db.owners) {
		if o.owner == storageOwner {
			continue
		}
		claims := ledgerOf(o.spec.Claims)
		if old, ok := st.ledgers[o.owner]; ok && !force && sameLedgerClaims(old.Claims, claims) {
			continue
		}
		l := claimLedger{Claims: claims, Build: st.opts.Build, At: now.Format(time.RFC3339Nano)}
		raw, err := codec.JSON[claimLedger](1).Encode(l)
		if err != nil {
			return err
		}
		if err := b.In(ks).Set(claimsKey(o.owner), raw); err != nil {
			return err
		}
		st.ledgers[o.owner] = l
	}
	return nil
}

// sortedOwners is a Levels' owners in order.
func sortedOwners(l Levels) []Owner {
	out := make([]Owner, 0, len(l))
	for o := range l {
		out = append(out, o)
	}
	slices.Sort(out)
	return out
}

// sortedInfos is the owner table in order.
func sortedInfos(m map[Owner]*ownerInfo) []*ownerInfo {
	out := make([]*ownerInfo, 0, len(m))
	for _, o := range m {
		out = append(out, o)
	}
	slices.SortFunc(out, func(a, b *ownerInfo) int { return strings.Compare(string(a.owner), string(b.owner)) })
	return out
}
