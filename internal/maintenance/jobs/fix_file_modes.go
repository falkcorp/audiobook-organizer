// file: internal/maintenance/jobs/fix_file_modes.go
// version: 1.1.0
// guid: 6d3a9f82-1e47-4b50-8c2d-5f9e7a3b1c48
// last-edited: 2026-08-17

package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&fixFileModesJob{}) }

// fixFileModesJob repairs audio files left mode 0600 by the WriteTagsSafe
// permission bug (fixed in the same PR): the tag rewrite replaced originals
// with the temp file's 0600 mode, which also zeroes the POSIX-ACL mask, so
// every non-owner reader (SMB share, other users' ACL entries) lost access.
//
// Scope is deliberately tight: only regular files that are (a) recorded as
// book_file rows, (b) owned by THIS process's uid (the service can only have
// broken files it owns, and chmod on anything else would fail anyway), and
// (c) currently mode exactly 0600. Those are restored to 0664 — the standard
// mode per the library's group-write convention, which also restores the ACL
// mask to rw-.
type fixFileModesJob struct{}

func (j *fixFileModesJob) ID() string       { return "fix-file-modes" }
func (j *fixFileModesJob) Name() string     { return "Repair 0600 file modes" }
func (j *fixFileModesJob) Category() string { return "maintenance" }
func (j *fixFileModesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixFileModesJob) Description() string {
	return "Restore 0664 on service-owned book files left mode 0600 by the tag write-back permission bug"
}
func (j *fixFileModesJob) CanResume() bool { return false } // idempotent

func (j *fixFileModesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("list book files: %w", err)
	}
	uid := os.Getuid()
	var examined, broken, repaired, failed int
	for i := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path := files[i].FilePath
		if path == "" {
			continue
		}
		info, serr := os.Lstat(path)
		if serr != nil || !info.Mode().IsRegular() {
			continue
		}
		examined++
		if info.Mode().Perm() != 0o600 || !ownedByUID(info, uid) {
			continue
		}
		broken++
		if dryRun {
			continue
		}
		if cerr := os.Chmod(path, 0o664); cerr != nil {
			slog.Warn("fix-file-modes: chmod failed", "path", path, "err", cerr)
			failed++
			continue
		}
		repaired++
	}
	slog.Info("fix-file-modes: complete",
		"examined", examined, "mode_0600_owned", broken,
		"repaired", repaired, "failed", failed, "dry_run", dryRun)
	if failed > 0 {
		return fmt.Errorf("fix-file-modes: %d chmods failed", failed)
	}
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *fixFileModesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
