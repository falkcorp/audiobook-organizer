// file: core/storage/spec.go
// version: 1.0.0
// guid: 156e1b2e-7ad2-4241-b21e-71d4f21d472b
// last-edited: 2026-11-07

package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// MigrationCompat classifies a step (renamed from Compat, COR-22).
type MigrationCompat uint8

const (
	CompatInvalid MigrationCompat = iota
	Additive                      // a build that does not know the step can still read and write the owner's data (the same-release-line binary-swap tolerance, §5S.3.5 "Newer version on disk")
	Breaking                      // a build that does not know the step must refuse the store: requires RaisesLevel (§5S.3.7)
)

// String is the compat's name, as an applied record stores it.
func (c MigrationCompat) String() string {
	switch c {
	case Additive:
		return "additive"
	case Breaking:
		return "breaking"
	default:
		return "invalid"
	}
}

func parseCompat(s string) MigrationCompat {
	switch s {
	case "additive":
		return Additive
	case "breaking":
		return Breaking
	default:
		return CompatInvalid
	}
}

// Spec is what a module (module.Manifest.Storage) or core subsystem declares.
// Data migrations are declared separately (storage/datamigrate, L4) because
// storage (L1) cannot import tasks (SIMP-17).
//
// R1-20 lands the members below. Derived (§5S.3.9) arrives with derived
// indexes and ReadAdapters with the deferred Online mode (§5S.3.14); neither
// field exists until then, so no declaration can set one and have it ignored.
// VersionSource is the per-owner version-source adapter of §5S.3.5's
// "Bookkeeping" row, the one member this file adds to the spec's block.
type Spec struct {
	Keyspace          bool                   // wants m/<id>/ (or c/<sub>/)
	Level             int                    // highest minimum-reader level of this owner the build serves (§5S.3.7)
	MinVersionToServe int                    // 0 = every registered versioned step must be applied before the owner starts
	Claims            []Claim                // legacy claims; books in practice
	Serialized        []string               // relative sub-prefixes of the keyspace that are Serialized; "" = the whole keyspace; every Set/Delete in them carries an Expect (R6-1)
	Migrations        []Migration            // versioned, §5S.3.5
	Legacy            []LegacyMigration      // books only, versions 1..LegacyCap, §5S.3.5
	LegacyGuard       *LegacyGuardProjection // books only, §5S.3.7
	UpgradeFloor      *UpgradeFloor          // §5S.3.5 (R2-4)
	// VersionSource, when set, keeps the owner's bookkeeping in its own rows
	// instead of c/storage/ks/<owner>/ (books: HEAD's migration-version rows).
	VersionSource VersionSource
}

// StepRef names one migration step of any owner, for cross-owner ordering.
// Its one printed spelling is "<owner>#<version>" ("core/tasks#1", "books#66")
// or "<owner>#<data-migration-id>"; bookkeeping is keyed by the same owner ID
// (c/storage/ks/core/auth/...), never by "core" alone (DATA2-29).
type StepRef struct {
	Owner         Owner
	Version       int    // a versioned or books legacy step; or
	DataMigration string // a storage/datamigrate ID
}

// String is the step's one printed spelling.
func (r StepRef) String() string {
	if r.DataMigration != "" {
		return string(r.Owner) + "#" + r.DataMigration
	}
	return string(r.Owner) + "#" + strconv.Itoa(r.Version)
}

// Migration is one forward-only step of one keyspace. There is no Down.
type Migration struct {
	Version     int // 1..n, contiguous per owner; books' are numbered from LegacyCap+1, contiguous with its legacy track (DATA7-7)
	Name        string
	Compat      MigrationCompat // zero rejected at registration
	RaisesLevel int             // required when Compat == Breaking: the owner's new level (§5S.3.7)
	After       []StepRef       // must run after these steps (DATA-9)
	// Seed marks a step that creates an owner's initial rows (hello's names,
	// a TV or Movies module's default profiles). Allowed only with Additive.
	// On a fresh install the stamp batch records applied only the owner's
	// steps below its lowest Seed step; that seed and every later step of the
	// owner then run in P4b in version order, so a later step that reshapes
	// the seeded rows converts them (§5S.4 step 6, COR5-8, COR6-4). On an
	// upgraded store it runs like any step, so it must leave rows that already
	// exist unchanged: it reads each key first and writes only on ErrNotFound.
	Seed bool
	Up   func(ctx context.Context, tx *MigrationTx) error
}

// UpgradeFloor lets a build delete step code that an older store would still
// need (books' legacy steps 1..LegacyCap, the store methods they call and
// the HEAD-format readers of the cutover steps; R2-4, DATA4-19). A store
// whose applied version for Owner is below Version, or on which a step in
// Requires is not done, is refused before anything is written. An empty
// store is exempt: it is a fresh install, whose one batch records every
// version up to the floor as applied, every registered data migration as
// done and every step a declared floor's Requires names as done whether or
// not this build registers it (R3 registers none of M2-M6), so the floor
// passes at every later boot too (§5S.4 steps 3 and 6; R7-2, DATA7-7,
// IMPL-7).
type UpgradeFloor struct {
	Owner        Owner
	Version      int       // the minimum applied version
	Requires     []StepRef // data migrations that must be done (R3: books' cutover steps, §5S.3.13)
	InstallFirst string    // the tag of the release to install first (the R2 release's tag), named in the refusal
}

// LegacyMigration is books' existing step form, kept for versions
// 1..LegacyCap only (DATA-8, DATA4-19). Up runs ABO's MigrationFunc through
// the books repository layer over books' own LegacyRange r (§5S.3.4); its
// store methods commit their own batches and allocate IDs with their own
// Sync writes, so the step is NOT atomic.
type LegacyMigration struct {
	Version int
	Name    string
	Up      func(ctx context.Context, r LegacyRange) error
}

// AppliedStep is one applied step's bookkeeping record:
// c/storage/ks/<owner>/applied/<v> = {name, compat, raises_level, applied_at,
// build}, or the same fields in an owner's own rows through its
// VersionSource. HasCompat is false for a record written before compat
// existed (a HEAD books record), which counts as Breaking (§5S.3.5).
type AppliedStep struct {
	Version     int
	Name        string
	Compat      MigrationCompat
	HasCompat   bool
	RaisesLevel int
	AppliedAt   time.Time
	Build       string
}

// VersionSource is the per-owner version-source adapter of §5S.3.5
// ("Bookkeeping"): an owner whose applied version lives in its own rows
// (books: HEAD's migration-version rows, ABO-migration table §5S.9.1) reads
// and stages it here, through its own LegacyRange, so HEAD's bookkeeping
// stays books' version source. An owner without one keeps its bookkeeping in
// c/storage/ks/<owner>/.
type VersionSource interface {
	// Applied reads the owner's applied version and the records it holds.
	// An unreadable version is an error, never 0 (fail closed,
	// ErrBookkeepingUnreadable).
	Applied(ctx context.Context, r LegacyRange) (version int, records map[int]AppliedStep, err error)
	// Stage stages step as applied (and the owner's version raised to it)
	// into b, through r, which the runner has bound to b. It adds compat and
	// raises_level to the record (HEAD ignores unknown JSON fields).
	Stage(ctx context.Context, r LegacyRange, b *Batch, step AppliedStep) error
}

// MigrationTx is an indexed batch over the keyspace (plus its legacy claims),
// with a hard size cap. Reads see the step's own writes. The runner commits
// tx + version bump + applied record (+ level stamp) as ONE Pebble batch.
type MigrationTx struct {
	b        *Batch
	ks       Keyspace
	legacy   LegacyRange
	log      *slog.Logger
	maxBytes int64
	maxOps   int
}

// Keyspace is the step's keyspace; reads go through the tx. Writes go through
// tx.Set and tx.Delete. A write on this handle or a Sub of it, and a batch or
// a sequence taken from either, are refused (errTxKeyspaceWrite), and so is a
// batch of the step's LegacyRange, so a step cannot commit outside its one
// batch. A snapshot is refused too (errTxSnapshot): it would not see the
// step's own writes.
func (tx *MigrationTx) Keyspace() Keyspace { return tx.ks }

// Legacy is the owner's LegacyRange; reads and writes go through the tx. Its
// NewBatch, NewIndexedBatch and Snapshot are refused as the Keyspace's are.
func (tx *MigrationTx) Legacy() LegacyRange { return tx.legacy }

// Set stages a write of the step's keyspace.
func (tx *MigrationTx) Set(key, value []byte) error {
	if tx.ks.db == nil {
		return fmt.Errorf("storage: migration of an owner without a keyspace: %w", ErrInvalidKeyspace)
	}
	if err := tx.b.stage(tx.ks, opSet, key, value); err != nil {
		return err
	}
	return tx.checkSize()
}

// Delete stages a delete of the step's keyspace.
func (tx *MigrationTx) Delete(key []byte) error {
	if tx.ks.db == nil {
		return fmt.Errorf("storage: migration of an owner without a keyspace: %w", ErrInvalidKeyspace)
	}
	if err := tx.b.stage(tx.ks, opDelete, key, nil); err != nil {
		return err
	}
	return tx.checkSize()
}

// Log is the step's logger.
func (tx *MigrationTx) Log() *slog.Logger { return tx.log }

// checkSize enforces core.storage.migration.max_batch_bytes and
// max_batch_ops: past either the tx is poisoned with ErrMigrationTooLarge.
func (tx *MigrationTx) checkSize() error {
	if int64(tx.b.Len()) > tx.maxBytes || tx.b.Count() > tx.maxOps {
		err := fmt.Errorf("storage: %d bytes in %d ops (limits %d bytes, %d ops): %w",
			tx.b.Len(), tx.b.Count(), tx.maxBytes, tx.maxOps, ErrMigrationTooLarge)
		tx.b.poison(err)
		return err
	}
	return nil
}

var (
	ErrMigrationTooLarge     = errors.New("storage: migration step exceeds core.storage.migration.max_batch_bytes; make it a DataMigration")
	ErrKeyspaceTooNew        = errors.New("storage: owner has a Breaking step above this build's supported level")
	ErrBookkeepingUnreadable = errors.New("storage: migration bookkeeping unreadable")
	ErrMigrationCycle        = errors.New("storage: migration DAG has a cycle, an unknown reference or an unordered owner")
)

// ErrInvalidSpec: a storage.Spec fails registration (versions not 1..n, a
// zero Compat, Breaking without RaisesLevel, Seed without Additive, a member
// its owner cannot use). Every problem is listed.
var ErrInvalidSpec = errors.New("storage: invalid storage spec")

// SpecError lists every registration problem of the build's specs.
type SpecError struct{ Problems []string }

func (e *SpecError) Error() string {
	return fmt.Sprintf("storage: %d spec problem(s): %s", len(e.Problems), strings.Join(e.Problems, "; "))
}

func (e *SpecError) Is(target error) bool { return target == ErrInvalidSpec }

// ErrUpgradeFloor: the store has not applied what a declared UpgradeFloor
// requires (R2-4, DATA2-2); the message names InstallFirst and the missing
// steps. Nothing was written; the process exits 78 (06).
type ErrUpgradeFloor struct {
	Owner        Owner
	Have, Floor  int
	Missing      []StepRef
	InstallFirst string
}

func (e *ErrUpgradeFloor) Error() string {
	var missing []string
	for _, m := range e.Missing {
		missing = append(missing, m.String())
	}
	return fmt.Sprintf("storage: %s is at version %d, below its upgrade floor %d (missing: %s); install %s first",
		e.Owner, e.Have, e.Floor, strings.Join(missing, ", "), e.InstallFirst)
}

// ErrMigrationFailed wraps the failing step.
type ErrMigrationFailed struct {
	Step StepRef
	Err  error
}

func (e *ErrMigrationFailed) Error() string {
	return fmt.Sprintf("storage: migration %s failed: %v", e.Step, e.Err)
}

func (e *ErrMigrationFailed) Unwrap() error { return e.Err }

// Levels is a reader-level vector by level owner ("core" and module IDs).
type Levels map[Owner]int

// LevelTooNewError: a store level is above this build's supported level for
// that owner (§5S.3.7); fail closed, exit 78, nothing written.
type LevelTooNewError struct {
	Owner            Owner
	Store, Supported int
	Source           string // "stamp", "sidecar" or "legacy-stamp"
}

func (e *LevelTooNewError) Error() string {
	return fmt.Sprintf("storage: %s's store level %d (%s) is above this build's supported level %d; install a release that serves it first",
		e.Owner, e.Store, e.Source, e.Supported)
}

// LegacyGuardProjection is declared by books only, so that HEAD — which reads
// only ABO's storage-format names — refuses a store whose levels were raised.
// Core never writes these names into another app's DB.
type LegacyGuardProjection struct {
	StampKey      string // the app's storage-format stamp row (books supplies it, §5S.9.1 row 10)
	MarkerKey     string // the app's storage-migration marker row
	SidecarSuffix string // the app's sidecar suffix
	// LegacyMaxFormat is the highest SupportedStorageFormat any pre-guard
	// release shipped (1 at 748fd787a; TestLegacyMaxFormatMatchesReleases
	// checks the release history). The projection of a raised store is
	// LegacyMaxFormat+1, which every pre-guard build refuses (OPS2-10).
	LegacyMaxFormat int
	// Stage writes the stamp and marker rows into b through the app's own
	// claim, in the app's own row format (books: it rewrites the existing
	// record keeping its ID and, for an absent stamp, allocates the ID from
	// its own counter, as storage_format.go:338-356 does today, DATA2-22).
	// The kernel binds the app's LegacyRange to the level step's batch for
	// this call only.
	Stage func(ctx context.Context, r LegacyRange, b *Batch, value int, marker bool) error
}

// projection is the legacy projection value of a books level: 1 while no
// level step has run, LegacyMaxFormat+1 once one has (§5S.3.7).
func (g *LegacyGuardProjection) projection(level int) int {
	if level > 0 {
		return g.LegacyMaxFormat + 1
	}
	return 1
}
