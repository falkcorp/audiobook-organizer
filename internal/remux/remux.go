// file: internal/remux/remux.go
// version: 1.2.0
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-07-18

package remux

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	taglib "go.senan.xyz/taglib"
)

const RemuxKey = "malformed_m4b_remux_v2_done"

// Store interface for setting persistence.
type Store interface {
	GetSetting(key string) (*database.Setting, error)
	SetSetting(key, value, typ string, isSecret bool) error
}

// Remuxer provides malformed M4B remux operations.
type Remuxer struct {
	store Store
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

// RemuxMalformedFiles walks the library once and re-muxes any M4B/M4A
// file that taglib cannot parse (malformed atom structure). Re-muxing with
// ffmpeg -c copy rewrites the atom layout without re-encoding audio, making
// the file readable by taglib, AtomicParsley, and Apple Devices. The output
// is verified before replacing the original. Runs once at startup. The walk
// checks ctx per file and stops early via fs.SkipAll when canceled (SYS-1);
// a canceled run does not write the done flag, so the next startup resumes.
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
func (r *Remuxer) RemuxMalformedFiles(ctx context.Context, progress func(processed, total int, msg string)) error {
	if r.store == nil {
		return nil
	}

	if setting, err := r.store.GetSetting(RemuxKey); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Malformed M4B remux already completed, skipping")
		return nil
	}

	root := config.AppConfig.RootDir
	if root == "" {
		return fmt.Errorf("RemuxMalformedFiles: RootDir not configured")
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("RemuxMalformedFiles: ffmpeg not found: %w", err)
	}

	// Pre-count candidates so progress can report an accurate "X/Y" instead
	// of an unbounded counter. Cheap relative to the ffmpeg work itself —
	// this pass does no file content reads, just a directory walk.
	total := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		select {
		case <-ctx.Done():
			return fs.SkipAll
		default:
		}
		if walkErr != nil {
			return nil
		}
		if isRemuxCandidate(path, d) {
			total++
		}
		return nil
	})

	slog.Info("Starting malformed M4B remux scan under", "root", root, "candidates", total)
	remuxed, clean, failed, processed := 0, 0, 0, 0

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		// Stop the walk cleanly on shutdown (SYS-1). fs.SkipAll ends WalkDir
		// without an error; WalkDir's return is discarded above by design.
		select {
		case <-ctx.Done():
			return fs.SkipAll
		default:
		}
		if walkErr != nil || d.IsDir() {
			return nil
		}
		if !isRemuxCandidate(path, d) {
			return nil
		}

		processed++
		if progress != nil && processed%25 == 0 {
			progress(processed, total, fmt.Sprintf("Probing M4B: %d/%d (remuxed=%d failed=%d)", processed, total, remuxed, failed))
		}

		if _, err := taglib.ReadTags(path); err == nil {
			clean++
			return nil
		}

		// taglib failed — attempt to remux with ffmpeg.
		if err := RemuxFile(path); err != nil {
			slog.Warn("malformed M4B remux failed for", "path", path, "err", err)
			failed++
			return nil
		}

		// Verify the output is now readable.
		if _, err := taglib.ReadTags(path); err != nil {
			slog.Warn("malformed M4B remux produced unreadable file for", "path", path, "err", err)
			failed++
			return nil
		}

		slog.Info("malformed M4B remuxed", "path", path)
		remuxed++
		return nil
	})

	if progress != nil {
		progress(processed, total, fmt.Sprintf("Probing M4B: %d/%d (remuxed=%d failed=%d)", processed, total, remuxed, failed))
	}

	slog.Info("Malformed M4B remux complete", "remuxed", remuxed, "clean", clean, "failed", failed)
	_ = r.store.SetSetting(RemuxKey, "true", "bool", false)
	return nil
}

// RemuxFile re-muxes an M4B/M4A file in-place using ffmpeg -c copy.
// Writes to a temp file first, then atomically renames over the original.
func RemuxFile(path string) error {
	tmp := path + ".remux.tmp"
	defer os.Remove(tmp)

	cmd := exec.Command("ffmpeg",
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

	return os.Rename(tmp, path)
}
