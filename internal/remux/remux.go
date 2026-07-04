// file: internal/remux/remux.go
// version: 1.1.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d
// last-edited: 2026-07-03

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

// RemuxMalformedFiles walks the library once and re-muxes any M4B/M4A
// file that taglib cannot parse (malformed atom structure). Re-muxing with
// ffmpeg -c copy rewrites the atom layout without re-encoding audio, making
// the file readable by taglib, AtomicParsley, and Apple Devices. The output
// is verified before replacing the original. Runs once at startup. The walk
// checks ctx per file and stops early via fs.SkipAll when canceled (SYS-1);
// a canceled run does not write the done flag, so the next startup resumes.
func (r *Remuxer) RemuxMalformedFiles(ctx context.Context) {
	if r.store == nil {
		return
	}

	if setting, err := r.store.GetSetting(RemuxKey); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Malformed M4B remux already completed, skipping")
		return
	}

	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("RemuxMalformedFiles RootDir not configured, skipping")
		return
	}

	if _, err := exec.LookPath("ffmpeg"); err != nil {
		slog.Warn("RemuxMalformedFiles ffmpeg not found, skipping")
		return
	}

	slog.Info("Starting malformed M4B remux scan under", "root", root)
	remuxed, clean, failed := 0, 0, 0

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
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".m4b" && ext != ".m4a" {
			return nil
		}

		// Skip orphaned temp files — those are handled by cleanupOrphanedTempFiles.
		if strings.Contains(filepath.Base(path), ".tmp.") {
			return nil
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

	slog.Info("Malformed M4B remux complete", "remuxed", remuxed, "clean", clean, "failed", failed)
	_ = r.store.SetSetting(RemuxKey, "true", "bool", false)
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
