// file: internal/backup/stage_restore.go
// version: 1.0.0
// guid: 5d0b2087-bf00-4056-bd1f-86fb060a11e5
// last-edited: 2026-11-09

package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stageLog logs StageRestore.
var stageLog = logger.New("backup.stage-restore")

// restoreIntentTTL is how long a staged restore stays valid: the swap
// rejects it at a boot after it expires (restoreswap's intentTTL).
const restoreIntentTTL = 24 * time.Hour

// StageRestoreOptions are StageRestore's inputs.
type StageRestoreOptions struct {
	// BackupDir and Filename name the archive: Filename is matched against
	// BackupDir's listing, never joined into a path unchecked (see
	// lookupArchive).
	BackupDir, Filename string
	// Verify requires the archive's checksum sidecar: an archive without one
	// is refused with ErrVerificationUnsupported. A sidecar that exists is
	// checked either way (ExtractStore).
	Verify bool
	// StateDir is the secure state directory holding the swap slot, and
	// DBPath core.storage.path, the live store the restore replaces; both
	// absolute and both the kernel's.
	StateDir, DBPath string
	// BuildID is the running binary's ID (app.BinaryID): the boot that
	// applies the intent rejects one staged by another binary, so it must
	// be the value the kernel computes.
	BuildID string
	// InstallID is the live store's install ID (05-backup §5B.3.4 step 8;
	// R1-21 decision D8): P2 gives it to a restored store without a marker.
	InstallID string
	// RequestedBy names who asked, for the journal.
	RequestedBy string
	// LegacySuffixes are the build's LegacyGuard sidecar suffixes (books'
	// storage-format sidecar): the live store's sidecars the swap sets aside
	// with it.
	LegacySuffixes []string
	// VerifyStore opens the extracted store read-only with this build's
	// guard and closes it (05-backup's VerifyOpen, R1's form): a store this
	// build cannot open is never staged. Required.
	VerifyStore func(ctx context.Context, dir string) error
	// BeforeStage runs with DBPath after every check passed (the archive
	// extracted and opened) and before the journal directory and the intent
	// are written: books removes its HNSW clean-stop marker there, as the
	// offline commands' BeforeSwap does, so the boot after the swap
	// reconciles the vector graph instead of trusting the replaced store's.
	// An error refuses with nothing staged. nil = none.
	BeforeStage func(dbPath string) error
	// Probe is the rename pre-check's view of the filesystem; nil is the
	// OS's (restorejournal.OSProbe).
	Probe restorejournal.Probe
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// StagedRestore is what StageRestore staged.
type StagedRestore struct {
	ID        string // the intent's id: restore/<ID>/ under the state directory
	Archive   string // the archive's path
	ArchiveID string // the archive's SHA-256, the intent's archive_id
	// Verified: the archive was checked against its checksum sidecar. Always
	// so with Verify; without it, whenever the archive has a sidecar.
	Verified bool
}

// StageRestore is ABO's live restore (05-backup §5B.3.4 steps 1-9, release
// R1's form): it stages an archive restore in the one journaled swap slot
// while the service serves, and touches neither the live store nor its
// settings. The swap runs at the next boot (06 P0a), which the caller asks
// for by requesting a restart (exit 75). In order:
//
//  1. the archive is found in BackupDir's listing; with Verify it must have
//     a checksum sidecar;
//  2. the slot is read: a swap staged or in flight refuses with
//     restorejournal.ErrSwapPending before anything is written;
//  3. the staging directory <db parent>/.mediacore-restore/<id>/staging/db
//     is made, and the intent's targets are checked before the archive is
//     extracted: restorejournal.ValidateTargets (the shapes) and
//     restorejournal.Validate over every rename the swap will make, the
//     live store and its sidecars set aside and the staged store placed
//     (DATA-23: same filesystem, parent writable, not a mount point;
//     ErrRenameRefused);
//  4. ExtractStore extracts the archive there (its sidecar checked when it
//     exists); with Verify that check must have run (a sidecar removed
//     after step 1 refuses with ErrVerificationUnsupported, never an
//     unverified stage); then VerifyStore opens the result read-only;
//  5. BeforeStage runs (books: the HNSW clean-stop marker goes);
//  6. the journal directory restore/<id>/ is made and synced, and
//     restorejournal.CreateIntent links the intent into the slot, the one
//     step that decides between two stagers (link(2)).
//
// A failure at any step removes everything this call made (the staging
// tree and the journal directory) and leaves the slot as it was. An intent
// that was linked but whose directory could not be synced
// (restorejournal.ErrIntentUnsynced) is staged: it is logged and returned
// as success, because the next boot applies what the slot holds. The
// journal's staged record is written by that boot's P0a.
func StageRestore(ctx context.Context, o StageRestoreOptions) (_ StagedRestore, err error) {
	if err := ctx.Err(); err != nil {
		return StagedRestore{}, err
	}
	if !filepath.IsAbs(o.StateDir) || !filepath.IsAbs(o.DBPath) {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: state directory %q and database %q must be absolute", o.StateDir, o.DBPath)
	}
	if o.BuildID == "" || o.VerifyStore == nil {
		return StagedRestore{}, errors.New("backup: stage restore: the binary ID and the store check are required")
	}
	probe, now := o.Probe, o.Now
	if probe == nil {
		probe = restorejournal.OSProbe()
	}
	if now == nil {
		now = time.Now
	}
	archive, hasSidecar, err := lookupArchive(o.BackupDir, o.Filename)
	if err != nil {
		return StagedRestore{}, err
	}
	if o.Verify && !hasSidecar {
		return StagedRestore{}, fmt.Errorf("%w: %s: this backup predates checksum sidecars; restore with verification disabled or re-create the backup",
			ErrVerificationUnsupported, o.Filename)
	}
	st, err := restorejournal.ReadSlot(vfs.Default, o.StateDir)
	if err != nil {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: read the swap slot: %w", err)
	}
	if err := st.Err(); err != nil {
		return StagedRestore{}, err
	}

	at := now().UTC()
	id, err := newRestoreID(at)
	if err != nil {
		return StagedRestore{}, err
	}
	dbPath := filepath.Clean(o.DBPath)
	staging := restorejournal.StagingDir(dbPath, id)
	store := filepath.Join(staging, "db")
	journal := restorejournal.JournalDir(o.StateDir, id)
	// Everything below is this call's own: .mediacore-restore/<id>/ and
	// restore/<id>/ are new names (the id is fresh), so a failure removes
	// both whole.
	staged := false
	defer func() {
		if staged {
			return
		}
		for _, dir := range []string{filepath.Dir(staging), journal} {
			if rerr := os.RemoveAll(dir); rerr != nil {
				err = errors.Join(err, fmt.Errorf("backup: stage restore: remove %s: %w", dir, rerr))
			}
		}
	}()
	if err := os.MkdirAll(store, 0o700); err != nil {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: %w", err)
	}
	in := restorejournal.Intent{
		ID: id, Kind: restorejournal.KindArchive, DBPath: dbPath,
		Targets:       []restorejournal.Target{{From: store, To: dbPath}},
		StagedByBuild: o.BuildID, ExpiresAt: at.Add(restoreIntentTTL),
		RequestedBy: o.RequestedBy, RequestedAt: at, InstallID: o.InstallID,
	}
	if err := restorejournal.ValidateTargets(o.StateDir, in, o.LegacySuffixes); err != nil {
		return StagedRestore{}, err
	}
	moves := append(restorejournal.SetAsides(o.StateDir, in, o.LegacySuffixes), in.Targets...)
	if err := restorejournal.Validate(probe, moves); err != nil {
		return StagedRestore{}, err
	}
	sum, verified, err := extractStore(ctx, archive, store)
	if err != nil {
		return StagedRestore{}, err
	}
	if o.Verify && !verified {
		// The extraction checks a sidecar only if one exists when it reads
		// it: a sidecar removed since the listing must not turn a verified
		// restore into an unverified one.
		return StagedRestore{}, fmt.Errorf("%w: %s: its checksum sidecar is gone; restore with verification disabled or re-create the backup",
			ErrVerificationUnsupported, o.Filename)
	}
	if err := o.VerifyStore(ctx, store); err != nil {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: the archive's store does not open: %w", err)
	}
	in.ArchiveID = sum
	if err := ctx.Err(); err != nil {
		return StagedRestore{}, err
	}
	if o.BeforeStage != nil {
		if err := o.BeforeStage(dbPath); err != nil {
			return StagedRestore{}, fmt.Errorf("backup: stage restore: %w", err)
		}
	}
	if _, err := os.Stat(journal); err == nil {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: restore/%s already exists; an intent id is never reused", id)
	}
	if err := restorejournal.MkdirAllSynced(vfs.Default, o.StateDir, journal); err != nil {
		return StagedRestore{}, fmt.Errorf("backup: stage restore: %w", err)
	}
	switch err := restorejournal.CreateIntent(o.StateDir, in); {
	case errors.Is(err, restorejournal.ErrIntentUnsynced):
		stageLog.Warn("restore %s staged, but the slot directory could not be synced; a crash may lose it: %s",
			id, logger.SanitizeLogValue(err.Error()))
	case err != nil:
		return StagedRestore{}, err
	}
	staged = true
	stageLog.Info("restore %s staged from %s (archive %s); it applies at the next start of the service", id, archive, sum)
	return StagedRestore{ID: id, Archive: archive, ArchiveID: sum, Verified: verified}, nil
}

// lookupArchive resolves filename against backupDir's own listing (CodeQL
// go/path-injection: the request's name is only a lookup key) and reports
// whether its checksum sidecar is listed beside it. A missing archive
// wraps os.ErrNotExist.
func lookupArchive(backupDir, filename string) (string, bool, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return "", false, fmt.Errorf("failed to read backup directory: %w", err)
	}
	var archive string
	sidecar := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch e.Name() {
		case filename:
			archive = filepath.Join(backupDir, e.Name())
		case filename + ".sha256":
			sidecar = true
		}
	}
	if archive == "" {
		return "", false, fmt.Errorf("backup %q not found in %s: %w", filename, backupDir, os.ErrNotExist)
	}
	abs, err := filepath.Abs(archive)
	if err != nil {
		return "", false, fmt.Errorf("backup: resolve %s: %w", archive, err)
	}
	return abs, sidecar, nil
}

// newRestoreID is a fresh intent id in the swap's form: the UTC time and
// eight random hex digits, one path segment.
func newRestoreID(at time.Time) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("backup: stage restore: %w", err)
	}
	return at.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]), nil
}
