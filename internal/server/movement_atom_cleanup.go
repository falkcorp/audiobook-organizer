// file: internal/server/movement_atom_cleanup.go
// version: 1.11.0
// guid: c2d3e4f5-a6b7-8c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-10-30

package server

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	taglib "go.senan.xyz/taglib"
)

const movementAtomCleanupKey = "movement_atom_cleanup_v1_done"

// movementAtoms are the three classical-music atoms the organizer incorrectly
// wrote to audiobook files (stik=2). Their presence causes Apple Devices for
// Windows to crash at "Determining Tracks to Sync".
var movementAtoms = []string{"SHOWWORKMOVEMENT", "MOVEMENTNUMBER", "MOVEMENTNAME"}

// stripMovementAtoms walks the library root once, removes the three movement
// atoms from every M4B/M4A file that has them, then marks itself done via a
// settings flag so it never runs again. The walk checks ctx per file and
// stops early via fs.SkipAll when canceled (SYS-1), so shutdown's 30s grace
// period is not blown by a first-run walk over a large library. A canceled
// run does not write the done flag, so the next startup resumes the cleanup.
// opts make the walk resumable (walkresume.WithRun, 04-P6): the candidates are
// collected and sorted, files before the resume cursor are skipped, a cursor is
// checkpointed as files complete on a worker pool, and the walk stops after a
// checkpoint (result.Yielded) when the run says its time budget is nearly spent.
// movementAtomCleanupResult reports what the walk actually visited.
//
// The function previously returned nothing, which made its walk unobservable:
// a file inside an app directory is left byte-identical whether it was skipped
// or visited-and-failed, so file state alone cannot tell a working guard from a
// deleted one. Returning the counters gives the guard a real instrument.
// AlreadyDone tells maintenance.movement-atom-cleanup that the done flag was
// set before this call, so its run did nothing and is tagged a no-op.
type movementAtomCleanupResult struct {
	Stripped int
	Clean    int
	Failed   int
	// SkippedProtected counts files the write guard refused because they are
	// protected (tagger.ErrProtectedPathWrite). Not failures: the file is
	// left alone on purpose.
	SkippedProtected int
	WalkErrs         int
	Visited          int // Stripped + Clean + Failed + SkippedProtected: every file the walk accepted
	Completed        bool
	AlreadyDone      bool // the done flag was already set; nothing was walked
	// Yielded: the walk stopped on purpose after a checkpoint because the run's
	// time budget is nearly spent (walkresume.Run.YieldDue); the done flag is
	// unset and the op continues from the cursor under the same operation.
	Yielded bool
}

func (s *Server) stripMovementAtoms(ctx context.Context, opts ...walkresume.Option) movementAtomCleanupResult {
	store := s.Ops()
	if store == nil {
		return movementAtomCleanupResult{}
	}

	if setting, err := store.GetSetting(movementAtomCleanupKey); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Movement atom cleanup already completed, skipping")
		return movementAtomCleanupResult{Completed: true, AlreadyDone: true}
	}

	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("stripMovementAtoms RootDir not configured, skipping")
		return movementAtomCleanupResult{}
	}

	// Build safe-write deps from server state so protected paths are guarded.
	deps := s.safeWriteDeps()
	// A whole-library rewrite keeps no .bak-* sibling per file (owner
	// decision D69, as for the bulk write-back ops).
	writeCtx := tagger.WithoutBackup(ctx)
	app := appdirs.Current()

	// Collect the candidates first: the sorted list is the progress denominator
	// and the sequence a resume cursor is searched in (see walkresume.Drive).
	// The directory skip is unchanged: this pass REWRITES tag atoms in place on
	// every .m4b/.m4a it accepts, and the library root contains the
	// application's backup directory and OpenLibrary dump directory, neither
	// dot-named by default; an .m4b archived into a backup tree would be edited.
	log := logger.New("movement-atom-cleanup")
	var walkErrs atomic.Int64
	files, cerr := walkresume.CollectFiles(ctx, root,
		func(path string) bool { return pathutil.ShouldSkipDir(root, path, app) },
		func(path string, _ fs.DirEntry) bool {
			ext := strings.ToLower(filepath.Ext(path))
			return ext == ".m4b" || ext == ".m4a"
		},
		func(path string, err error) {
			// Previously swallowed: an unreadable dir/file silently shrank
			// the walk. Log it so partial coverage is visible.
			slog.Warn("movement atom cleanup: walk error", "path", path, "err", err)
			walkErrs.Add(1)
		})
	if cerr != nil {
		log.Info("movement atom cleanup canceled before completion — done flag not written, will resume next startup")
		return movementAtomCleanupResult{WalkErrs: int(walkErrs.Load())}
	}
	total := len(files)

	run := walkresume.Resolve(opts)
	base := run.Resume
	slog.Info("Starting movement atom cleanup under …", "root", root, "candidates", total, "resume_cursor", base.Cursor)
	var stripped, clean, failed, skippedProtected, processed atomic.Int64
	state := func() walkresume.State {
		return walkresume.State{
			Processed:        base.Processed + int(processed.Load()),
			Changed:          base.Changed + int(stripped.Load()),
			Clean:            base.Clean + int(clean.Load()),
			Failed:           base.Failed + int(failed.Load()),
			SkippedProtected: base.SkippedProtected + int(skippedProtected.Load()),
		}
	}
	message := func() string {
		st := state()
		return fmt.Sprintf("Stripping movement atoms: %d/%d (stripped=%d failed=%d skipped_protected=%d)", min(st.Processed, total), total, st.Changed, st.Failed, st.SkippedProtected)
	}

	derr := walkresume.Drive(ctx, run, files, func(ctx context.Context, path string) error {
		changed, err := removeMovementAtomsFromFile(writeCtx, path, deps)
		switch {
		case errors.Is(err, tagger.ErrProtectedPathWrite):
			log.Info("movement atom cleanup: skipping protected file %s: %v", logger.SanitizeLogValue(path), err)
			skippedProtected.Add(1)
		case err != nil:
			if ctx.Err() != nil {
				return ctx.Err() // canceled mid-write: the resume redoes this file
			}
			slog.Warn("movement atom cleanup:", "path", path, "err", err)
			failed.Add(1)
		case changed:
			stripped.Add(1)
		default:
			clean.Add(1)
		}
		abs := base.Processed + int(processed.Add(1))
		if abs%500 == 0 {
			st := state()
			log.Info("movement atom cleanup: progress: processed=%d stripped=%d clean=%d failed=%d skipped_protected=%d",
				st.Processed, st.Changed, st.Clean, st.Failed, st.SkippedProtected)
		}
		if run.Progress != nil && abs%25 == 0 {
			run.Progress(min(abs, total), total, message())
		}
		return nil
	}, state)
	if run.Progress != nil {
		run.Progress(min(base.Processed+int(processed.Load()), total), total, message())
	}

	st := state()
	log.Info("Movement atom cleanup: stripped=%d clean=%d failed=%d skipped_protected=%d walk_errors=%d",
		st.Changed, st.Clean, st.Failed, st.SkippedProtected, int(walkErrs.Load()))

	res := movementAtomCleanupResult{
		Stripped: st.Changed, Clean: st.Clean, Failed: st.Failed, SkippedProtected: st.SkippedProtected,
		WalkErrs: int(walkErrs.Load()),
		Visited:  st.Changed + st.Clean + st.Failed + st.SkippedProtected,
	}

	if errors.Is(derr, walkresume.ErrYield) {
		log.Info("movement atom cleanup yielding with a checkpoint — it continues under the same operation")
		res.Yielded = true
		return res
	}
	if ctx.Err() != nil {
		log.Info("movement atom cleanup canceled before completion — done flag not written, will resume next startup")
		return res
	}
	if derr != nil {
		slog.Warn("movement atom cleanup: walk did not complete — done flag not written, will re-run next startup", "err", derr)
		return res
	}
	if serr := store.SetSetting(movementAtomCleanupKey, "true", "bool", false); serr != nil {
		// Previously swallowed: a failed write meant a silent full re-walk
		// on every boot. Do not claim completion.
		slog.Error("movement atom cleanup: failed to persist done flag — cleanup will re-run on next startup",
			"key", movementAtomCleanupKey, "err", serr)
		return res
	}
	slog.Info("Movement atom cleanup complete — done flag persisted", "key", movementAtomCleanupKey)
	res.Completed = true
	return res
}

// removeMovementAtomsFromFile reads the file's tags, removes the three
// movement atoms if present, and writes back with taglib.Clear (which
// replaces the full tag set, preserving all other tags and cover art).
// Returns (true, nil) if the file was modified, (false, nil) if it was
// already clean, or (false, err) on failure.
//
// deps provides the pre-flight protection guard. A protected path is REFUSED
// (tagger.ErrProtectedPathWrite, counted as skipped by the caller), never
// imported: the walk covers files outside any book folder, and an import
// copied a Deluge-seeding file to RootDir/<basename> and repointed its row.
func removeMovementAtomsFromFile(ctx context.Context, path string, deps tagger.SafeWriteDeps) (bool, error) {
	deps.Importer = nil
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return false, err
	}

	found := false
	for _, key := range movementAtoms {
		if _, ok := tags[key]; ok {
			delete(tags, key)
			found = true
		}
	}
	if !found {
		return false, nil
	}

	// The rewrite changes the bytes, so record the new hashes on the book_file
	// row of the file written (looked up only for files actually rewritten,
	// after any protected-path redirect).
	deps = metadata.WithBookFileHashes(deps)
	if err := tagger.WriteTagsSafe(ctx, path, tags, taglib.Clear, deps); err != nil {
		return false, err
	}
	return true, nil
}
