// file: internal/books/core_restore_stager.go
// version: 0.1.0
// guid: b4e7a2d9-3c61-4f58-9a0d-6e1f8c3b7a25
// last-edited: 2026-11-22

package books

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	corebackup "github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// coreRestorer is the part of core's backup Service the stager uses (F8-04a
// G2): the catalogue, to name an archive file by its ID, and the staged
// restore.
type coreRestorer interface {
	List(ctx context.Context) ([]corebackup.Archive, error)
	StageRestore(ctx context.Context, id corebackup.ArchiveID, o corebackup.RestoreOptions) (corebackup.PendingRestore, error)
}

var _ coreRestorer = (*corebackup.Svc)(nil)

// coreRestoreStager is restoreStager over core's backup Service (F8-04a: the
// restore stager's re-point), with the same contract at the handler's seam
// (system.RestoreStager): stage the archive file filename of the backup
// directory in the one journaled swap slot, touching neither the live store
// nor its settings, and let the handler ask for the restart (RequestRestart).
//
// It is not installed: options.go still sets restoreStager, so the live
// restore is unchanged until the fold swaps them (F8-04b).
//
// What the Service now owns, that restoreStager passed per call: the state
// directory and store path, the binary ID and the live store's install ID (the
// intent's StagedByBuild and InstallID: backup.Options.BuildID and .Store), the
// legacy sidecar suffixes (the swapper derives them from the owners' specs),
// the open-with-this-build check (Options.Inspector) and the rename pre-check
// (the swapper's CheckStage). What this adds to it: the books-only HNSW
// ordering below.
//
// The caller's authority is not this type's to give. Core refuses every
// restore whose context carries no RestoreProof with Owner, and a foreign one
// (every legacy archive: it has no provenance) without Admin too
// (authorizeRestore). The handler establishes the proof after its own guards
// (interactive session, owner assurance) and puts it on the context it passes
// here, as the route of core/internal/backuphttp does; this type neither mints
// nor widens one.
type coreRestoreStager struct {
	svc     coreRestorer
	dir     string // the backup directory svc serves; a call for another is refused
	dbPath  string // the live store, whose HNSW clean-stop marker is discarded
	restart func(reason string)
}

// StageRestore stages the archive filename in backupDir.
//
// Differences a caller can see, against restoreStager: the ArchiveID of the
// result is core's catalogue ID ("legacy-<stem>" for a legacy archive, the
// manifest's ID for a mediacore-backup/1 one), not the file's SHA-256; the
// error for an unknown file is still os.ErrNotExist, but the other refusals are
// core's sentinels (ErrChecksumMismatch, ErrBackupInProgress for a restore
// racing another one through the same Service, ...), which the handler maps in
// the fold; and the intent's RequestedBy is the Service's Actor(ctx), not
// requestedBy.
func (s coreRestoreStager) StageRestore(ctx context.Context, backupDir, filename string, verify bool, _ string, diverged func()) (backup.StagedRestore, error) {
	// A Service lists one directory. Staging from another would stage an
	// archive no catalogue vouches for, so refuse rather than guess.
	if filepath.Clean(backupDir) != filepath.Clean(s.dir) {
		return backup.StagedRestore{}, fmt.Errorf("books: restore: %q is not the backup directory the Service serves", backupDir)
	}
	list, err := s.svc.List(ctx)
	if err != nil {
		return backup.StagedRestore{}, err
	}
	i := slices.IndexFunc(list, func(a corebackup.Archive) bool { return a.Name == filename })
	if i < 0 {
		return backup.StagedRestore{}, fmt.Errorf("books: restore: no archive %q: %w", filename, os.ErrNotExist)
	}
	a := list[i]

	opts := corebackup.RestoreOptions{
		// A legacy archive predates provenance and an imported one is not this
		// install's: both are foreign restores, which is Admin on top of Owner.
		// An archive this install made needs neither.
		Foreign: a.Provenance != corebackup.Own,
		Verify:  corebackup.VerifyOpen,
	}
	if !verify {
		// restoreStager's verify=false still checked an archive that has a
		// checksum sidecar and staged one that has none unchecked: core's
		// checksum level with AllowWeakVerify is exactly that.
		opts.Verify, opts.AllowWeakVerify = corebackup.VerifyChecksum, true
	}

	// Once every check passed, before the intent: first the live HNSW store is
	// marked diverged, so this run's stop (the restart that applies the
	// restore) writes no clean-stop marker for the rows the swap replaces; then
	// the marker on disk goes (the offline commands' BeforeSwap), so the boot
	// after the swap reconciles the vector graph. In that order, so no stop
	// between the two can write the marker again. The Service runs the hook
	// after the swapper's own refusals and before its create (backup.BeforeStage),
	// or refuses the context (ErrBeforeStageUnsupported): a refused restore runs
	// neither step, and a staged one always ran both.
	var in restorejournal.Intent
	ctx = corebackup.WithBeforeStage(ctx, func(_ context.Context, intent restorejournal.Intent) error {
		in = intent
		if diverged != nil {
			diverged()
		}
		return database.DiscardHNSWCleanStop(s.dbPath)
	})
	pr, err := s.svc.StageRestore(ctx, a.ID, opts)
	if err != nil {
		return backup.StagedRestore{}, err
	}
	if in.ID == "" {
		// Unreachable while the Service wraps its Stager: it staged, so the
		// hook ran. Were it not, the restart would apply a restore whose
		// clean-stop marker was never discarded.
		return backup.StagedRestore{}, errors.New("books: restore: staged without the before-stage step; the Service's Stager did not run it")
	}
	path := filepath.Join(s.dir, a.Name)
	return backup.StagedRestore{
		ID: in.ID, Archive: path, ArchiveID: string(pr.ArchiveID),
		Verified: verify || hasChecksumSidecar(path),
	}, nil
}

// hasChecksumSidecar reports whether the archive at path has a checksum
// sidecar: what "verified" meant for a restore that did not ask to verify.
func hasChecksumSidecar(path string) bool {
	fi, err := os.Lstat(path + corebackup.ChecksumSidecar)
	return err == nil && fi.Mode().IsRegular()
}

// RequestRestart asks the kernel to restart the app (Lifecycle.RequestRestart:
// the stop order, then exit 75).
func (s coreRestoreStager) RequestRestart(reason string) { s.restart(reason) }
