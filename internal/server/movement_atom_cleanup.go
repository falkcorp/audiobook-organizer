// file: internal/server/movement_atom_cleanup.go
// version: 1.2.0
// guid: c2d3e4f5-a6b7-8c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-07-03

package server

import (
	"context"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
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
func (s *Server) stripMovementAtoms(ctx context.Context) {
	store := s.Store()
	if store == nil {
		return
	}

	if setting, err := store.GetSetting(movementAtomCleanupKey); err == nil && setting != nil && setting.Value == "true" {
		slog.Info("Movement atom cleanup already completed, skipping")
		return
	}

	root := config.AppConfig.RootDir
	if root == "" {
		slog.Warn("stripMovementAtoms RootDir not configured, skipping")
		return
	}

	// Build safe-write deps from server state so protected paths are guarded.
	deps := s.safeWriteDeps()

	slog.Info("Starting movement atom cleanup under …", "root", root)
	stripped, clean, failed := 0, 0, 0

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

		changed, err := removeMovementAtomsFromFile(path, deps)
		switch {
		case err != nil:
			slog.Warn("movement atom cleanup:", "path", path, "err", err)
			failed++
		case changed:
			stripped++
		default:
			clean++
		}
		return nil
	})

	slog.Info("Movement atom cleanup stripped, already clean, errors", "stripped", stripped, "clean", clean, "failed", failed)
	_ = store.SetSetting(movementAtomCleanupKey, "true", "bool", false)
}

// removeMovementAtomsFromFile reads the file's tags, removes the three
// movement atoms if present, and writes back with taglib.Clear (which
// replaces the full tag set, preserving all other tags and cover art).
// Returns (true, nil) if the file was modified, (false, nil) if it was
// already clean, or (false, err) on failure.
//
// deps provides the pre-flight protection guard: if the path is protected
// it is imported to the library before the write proceeds.
func removeMovementAtomsFromFile(path string, deps tagger.SafeWriteDeps) (bool, error) {
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

	if err := tagger.WriteTagsSafe(context.Background(), path, tags, taglib.Clear, deps); err != nil {
		return false, err
	}
	return true, nil
}
