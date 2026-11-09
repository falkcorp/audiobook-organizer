// file: internal/books/restore_stager.go
// version: 1.0.1
// guid: 9a1f6c38-2d7e-4b50-8e94-c3b05d71e2a6
// last-edited: 2026-11-09

package books

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// restoreStager is POST /backup/restore's stager (system.RestoreStager),
// which Attach installs once the App exists (05-backup §5B.3.4; R1-25 Goal
// item 3): HEAD's live restore extracted the archive over the running store
// (09 §9.1); this one stages it in the one journaled swap slot through
// backup.StageRestore, under the state directory and store path the kernel
// boots with, the binary ID the next boot's P0a checks and the live store's
// install ID, and touches neither the live store nor its settings. The
// handler then asks for the restart (exit 75), and the swap runs at the
// next boot, before the store opens.
type restoreStager struct {
	stateDir, dbPath string
	buildID          string // app.BinaryID of serve's Options: what P0a compares StagedByBuild with
	installID        string // the live store's (storage.Admin.InstallID)
	restart          func(reason string)
	probe            restorejournal.Probe // the rename pre-check's devices; nil is the OS (tests stand in another)
}

// StageRestore stages the archive filename in backupDir.
func (s restoreStager) StageRestore(ctx context.Context, backupDir, filename string, verify bool, requestedBy string, diverged func()) (backup.StagedRestore, error) {
	return backup.StageRestore(ctx, backup.StageRestoreOptions{
		BackupDir: backupDir, Filename: filename, Verify: verify,
		StateDir: s.stateDir, DBPath: s.dbPath,
		BuildID: s.buildID, InstallID: s.installID, RequestedBy: requestedBy,
		LegacySuffixes: legacySidecarSuffixes(),
		VerifyStore:    database.VerifyStoreOpens,
		Probe:          s.probe,
		// Once every check passed, before the intent: first the live HNSW
		// store is marked diverged, so this run's stop (the restart that
		// applies the restore) writes no clean-stop marker for the rows the
		// swap replaces; then the marker on disk goes (the offline
		// commands' BeforeSwap), so the boot after the swap reconciles the
		// vector graph. In that order, so no stop between the two can write
		// the marker again.
		BeforeStage: func(dbPath string) error {
			if diverged != nil {
				diverged()
			}
			return database.DiscardHNSWCleanStop(dbPath)
		},
	})
}

// RequestRestart asks the kernel to restart the app (Lifecycle.RequestRestart:
// the stop order, then exit 75).
func (s restoreStager) RequestRestart(reason string) { s.restart(reason) }

// legacySidecarSuffixes are books' LegacyGuard sidecar suffixes, the live
// store's sidecars the swap sets aside with it (the kernel's swapper derives
// the same list from its owners' specs; books is ABO's one owner with a
// guard, whose SidecarSuffix is database.StorageFormatSidecarSuffix).
func legacySidecarSuffixes() []string {
	return []string{database.StorageFormatSidecarSuffix}
}
