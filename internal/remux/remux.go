// file: internal/remux/remux.go
// version: 1.9.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-10-30

package remux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/appdirs"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/walkresume"
	taglib "go.senan.xyz/taglib"
)

const RemuxKey = "malformed_m4b_remux_v2_done"

// Store interface for setting persistence.
type Store interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// BookFileStore keeps a rewritten file's book_file row describing the bytes
// on disk: the row is found by path, its hashes are recorded after the
// rewrite, and after a transcode its audio properties are refreshed. The
// server passes its store; without one nothing is recorded and the next
// rescan treats every rewritten file as replaced.
type BookFileStore interface {
	fileops.BookFileHashRecorder
	PatchBookFileFields(bookID, fileID string, patch database.BookFileFieldPatch) (before, after *database.BookFile, err error)
}

// ProtectedChecker reports whether a path is protected: under a Deluge
// save_path or a configured protected prefix such as the iTunes library.
// Satisfied by *deluge.ProtectedPathCache, the predicate the tag-write guard
// (tagger.SafeWriteDeps.ProtectedCache) uses.
type ProtectedChecker interface {
	IsProtected(filePath string) bool
}

// isProtected reports whether path is protected; a nil checker protects nothing.
func isProtected(c ProtectedChecker, path string) bool {
	return c != nil && c.IsProtected(path)
}

// protectedListLoaded reports whether c's protected list is usable. A checker
// with a Loaded method (the Deluge cache) that has never loaded answers "not
// protected" for every seeding file, so no pass may rewrite on it.
func protectedListLoaded(c ProtectedChecker) bool {
	if c == nil {
		return true
	}
	if lr, ok := c.(interface{ Loaded() bool }); ok {
		return lr.Loaded()
	}
	return true
}

// notDoneReason says why a pass must not set its done flag, or "" when it
// may: a canceled walk stopped part-way, and a protected file was never
// probed, so marking either done would leave those files unconsidered forever.
func notDoneReason(ctx context.Context, skippedProtected int) string {
	switch {
	case ctx.Err() != nil:
		return "the walk was canceled"
	case skippedProtected > 0:
		return fmt.Sprintf("%d protected file(s) were skipped", skippedProtected)
	}
	return ""
}

// errProtectedListNotLoaded is returned by both passes when the protected
// list has never loaded. Nothing is rewritten and no done flag is set.
var errProtectedListNotLoaded = errors.New("the protected-path list (Deluge save paths) has not loaded, so no file can be rewritten safely; the pass runs again once it has")

// Remuxer provides malformed M4B remux operations.
type Remuxer struct {
	store     Store
	files     BookFileStore
	protected ProtectedChecker
}

// SetBookFileStore installs the store remuxed files' hashes are recorded
// through. Nil disables recording.
func (r *Remuxer) SetBookFileStore(files BookFileStore) { r.files = files }

// SetProtectedChecker installs the protected-path predicate. A protected file
// is never remuxed: ffmpeg would rewrite a file a torrent client is seeding,
// or one the iTunes library owns. The file is counted as skipped_protected.
// Nil disables the check.
func (r *Remuxer) SetProtectedChecker(c ProtectedChecker) { r.protected = c }

// remuxAndRecord rewrites path with remux and records the new bytes' hashes on
// the file's book_file row. A remux rewrites the container, so every byte-level
// hash of the file changes even though the audio does not.
// ctx is passed to remux, which stops ffmpeg on cancel; a cancelled rewrite
// returns an error, so no hash is recorded.
func (r *Remuxer) remuxAndRecord(ctx context.Context, path string, remux func(context.Context, string) error) error {
	var rec fileops.BookFileHashRecorder
	if r.files != nil {
		rec = r.files
	}
	return fileops.RecordRewrite(rec, path, func() error { return remux(ctx, path) })
}

// New creates a new Remuxer instance.
func New(store Store) *Remuxer {
	return &Remuxer{store: store}
}

// isRemuxCandidate reports whether path is an M4B/M4A file this pass should
// consider (used identically by the pre-count pass and the work pass so the
// reported "total" never drifts from what actually gets processed).
func isRemuxCandidate(path string, d fs.DirEntry) bool {
	if d.IsDir() {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".m4b" && ext != ".m4a" {
		return false
	}
	// Skip orphaned temp files — those are handled by cleanupOrphanedTempFiles.
	return !strings.Contains(filepath.Base(path), ".tmp.")
}

// AlreadyDone reports whether a previous RemuxMalformedFiles run completed and set
// RemuxKey, in which case RemuxMalformedFiles returns at once without walking.
// maintenance.malformed-m4b-remux reads it to tag such a run a no-op.
func (r *Remuxer) AlreadyDone() bool {
	if r.store == nil {
		return false
	}
	setting, err := r.store.GetSetting(RemuxKey)
	return err == nil && setting != nil && setting.Value == "true"
}

// RemuxMalformedFiles walks the library once and re-muxes any M4B/M4A
// file that taglib cannot parse (malformed atom structure). Re-muxing with
// ffmpeg -c copy rewrites the atom layout without re-encoding audio, making
// the file readable by taglib, AtomicParsley, and Apple Devices. The output
// is verified before replacing the original. Runs once at startup. The walk
// checks ctx per file and stops early via fs.SkipAll when canceled (SYS-1);
// a canceled run does not write the done flag, so the next startup resumes.
//
// opts make the walk resumable (walkresume.WithRun): the candidates are
// collected and sorted, items before the resume cursor are skipped, a cursor is
// checkpointed as files complete (a pool, so several files are in flight), and
// the walk returns walkresume.ErrYield after a checkpoint when the run says its
// time budget is nearly spent. Without options it is the plain sequential walk.
//
// progress is called every 25 processed files (and once more at the end)
// with the running counts, so a caller wired to an op reporter's
// UpdateProgress can surface a live "X/Y" during a multi-hour run instead of
// a single log line at completion (C2). progress may be nil.
//
// A non-nil error is returned only for fatal setup problems (RootDir not
// configured, ffmpeg missing) — those used to be swallowed as a Warn log,
// letting the op report success while doing nothing. Per-file remux
// failures are expected in normal operation (the transcode op is the
// designed fallback for files that can't be remuxed) and are counted in the
// progress message rather than failing the whole run.
func (r *Remuxer) RemuxMalformedFiles(ctx context.Context, progress func(processed, total int, msg string), opts ...walkresume.Option) error {
	if r.store == nil {
		return nil
	}

	if r.AlreadyDone() {
		slog.Info("Malformed M4B remux already completed, skipping")
		return nil
	}

	root := config.AppConfig.RootDir
	if root == "" {
		return fmt.Errorf("RemuxMalformedFiles: RootDir not configured")
	}

	// Both walks below MUST agree on what they skip: the first is the progress
	// denominator for the second. A subtree counted but not processed makes
	// the bar unable to reach 100%; one processed but not counted overshoots.
	app := appdirs.Current()

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("RemuxMalformedFiles: ffmpeg not found: %w", err)
	}
	if !protectedListLoaded(r.protected) {
		return fmt.Errorf("RemuxMalformedFiles: %w", errProtectedListNotLoaded)
	}

	// Collect the candidates first (a directory walk, no content reads): the
	// list is the progress denominator AND the sorted sequence a resume cursor
	// is searched in. The walk skips what the work used to skip, so the
	// denominator never drifts from what is processed. The library root holds
	// the application's own state as well as books -- a backup directory of
	// multi-GB archives and an OpenLibrary dump directory containing an
	// embedded database of ~1,200 files. Neither has a leading dot by default,
	// and this pass opens and REWRITES files it accepts, so descending is not
	// merely wasted I/O.
	files, err := walkresume.CollectFiles(ctx, root,
		func(path string) bool { return pathutil.ShouldSkipDir(root, path, app) },
		isRemuxCandidate, nil)
	if err != nil {
		log := logger.New("remux")
		log.Info("Malformed M4B remux not marked done (the walk was canceled); it runs again next time")
		return nil
	}
	total := len(files)

	run := walkresume.Resolve(opts)
	base := run.Resume
	slog.Info("Starting malformed M4B remux scan under", "root", root, "candidates", total, "resume_cursor", base.Cursor)
	var remuxed, clean, failed, skippedProtected, processed atomic.Int64
	log := logger.New("remux")
	state := func() walkresume.State {
		return walkresume.State{
			Processed:        base.Processed + int(processed.Load()),
			Changed:          base.Changed + int(remuxed.Load()),
			Clean:            base.Clean + int(clean.Load()),
			Failed:           base.Failed + int(failed.Load()),
			SkippedProtected: base.SkippedProtected + int(skippedProtected.Load()),
		}
	}
	message := func() string {
		st := state()
		return fmt.Sprintf("Probing M4B: %d/%d (remuxed=%d failed=%d skipped_protected=%d)", min(st.Processed, total), total, st.Changed, st.Failed, st.SkippedProtected)
	}

	derr := walkresume.Drive(ctx, run, files, func(ctx context.Context, path string) error {
		abs := base.Processed + int(processed.Add(1))
		if progress != nil && abs%25 == 0 {
			progress(min(abs, total), total, message())
		}

		// A protected file is counted by the collection (it is a candidate) and
		// skipped here, after processed is counted, so the two still agree. It
		// is never probed or rewritten: remux replaces the file with ffmpeg's
		// output.
		if isProtected(r.protected, path) {
			log.Info("malformed M4B remux: skipping protected file %s", logger.SanitizeLogValue(path))
			skippedProtected.Add(1)
			return nil
		}

		if _, err := taglib.ReadTags(path); err == nil {
			clean.Add(1)
			return nil
		}

		// taglib failed — attempt to remux with ffmpeg.
		if err := r.remuxAndRecord(ctx, path, RemuxFile); err != nil {
			if ctx.Err() != nil {
				// Cancelled mid-ffmpeg: not a failure of this file, and the
				// original is untouched (the temp output is removed), and the
				// item stays past the watermark, so a resume redoes it.
				return ctx.Err()
			}
			slog.Warn("malformed M4B remux failed for", "path", path, "err", err)
			failed.Add(1)
			return nil
		}

		// Verify the output is now readable.
		if _, err := taglib.ReadTags(path); err != nil {
			slog.Warn("malformed M4B remux produced unreadable file for", "path", path, "err", err)
			failed.Add(1)
			return nil
		}

		slog.Info("malformed M4B remuxed", "path", path)
		remuxed.Add(1)
		return nil
	}, state)

	if progress != nil {
		progress(min(base.Processed+int(processed.Load()), total), total, message())
	}
	if errors.Is(derr, walkresume.ErrYield) {
		log.Info("Malformed M4B remux yielding to continue under the same operation: %s", message())
		return derr
	}
	if derr != nil && ctx.Err() == nil {
		return fmt.Errorf("RemuxMalformedFiles: %w", derr)
	}

	st := state()
	log.Info("Malformed M4B remux complete: remuxed=%d clean=%d failed=%d skipped_protected=%d", st.Changed, st.Clean, st.Failed, st.SkippedProtected)
	// The done flag means every candidate was considered. A canceled walk
	// stopped part-way and a protected file was never probed; marking either
	// done stops the pass from ever reaching the files it left. Until
	// 2026-09-14 the flag was set after both. st.SkippedProtected includes the
	// earlier attempts' count, so a file skipped before a resume still holds
	// the flag back.
	if why := notDoneReason(ctx, st.SkippedProtected); why != "" {
		log.Info("Malformed M4B remux not marked done (%s); it runs again next time", why)
		return nil
	}
	_ = r.store.SetSetting(RemuxKey, "true", "bool", false)
	return nil
}

// RemuxFile re-muxes an M4B/M4A file in-place using ffmpeg -c copy.
// Writes to a temp file first, then atomically renames over the original.
// ffmpeg runs under ctx, and a ctx cancelled before the rename returns its
// error with the original left in place.
func RemuxFile(ctx context.Context, path string) error {
	tmp := path + ".remux.tmp"
	defer os.Remove(tmp)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-nostdin", "-loglevel", "error", "-y",
		"-i", path,
		"-map", "0",
		"-c", "copy",
		"-map_metadata", "0",
		"-map_chapters", "0",
		"-f", "mp4",
		tmp,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w — %s", err, strings.TrimSpace(string(out)))
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
