// file: internal/activity/relocate.go
// version: 1.0.0
// guid: 9c3e7b41-2d58-4a06-b7f9-1e5a8c04d3b6
// last-edited: 2026-09-07

package activity

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// relocateActivityDBIfNeeded reconciles where the activity database IS with where
// the configuration says it SHOULD be, and is called at boot immediately before
// the store is opened.
//
// The comparison is against the path recorded on the last successful open, not
// against whatever happens to exist on disk. That distinction is what lets a
// genuine first boot at a new path stay silent while an operator's path change is
// acted on: only the recorded path can tell them apart.
//
// It never returns an error. A relocation that cannot be completed must not stop
// the server from starting — the fallback is always to open the configured path
// and keep logging, which is strictly better than refusing to boot over the
// location of a log file. Failures are loud, and the source is left intact for
// the operator to deal with, so nothing is lost by continuing.
func relocateActivityDBIfNeeded(pebbleStore *database.PebbleActivityStore, desiredPath string, moveOnChange bool) {
	if pebbleStore == nil || desiredPath == "" {
		return
	}

	lastPath, recorded := pebbleStore.LastActivityDBPath()
	if !recorded {
		// First boot, or an install predating the record. Nothing is known to
		// exist elsewhere, so there is nothing to move; the path is recorded
		// after the store opens.
		return
	}
	if lastPath == desiredPath {
		return
	}

	// The path changed. Decide by config whether the data follows it.
	if !moveOnChange {
		slog.Warn("[activity-relocate] activity database path changed but moving is disabled — "+
			"a NEW, EMPTY database will be started at the new path and the existing history "+
			"stays readable only at the old one",
			"old_path", lastPath, "new_path", desiredPath,
			"setting", "activity_db_move_on_change")
		return
	}

	if _, err := os.Stat(lastPath); err != nil {
		// Recorded, but gone — moved by hand, or on a volume that is not
		// mounted. Either way there is nothing to copy, and guessing would risk
		// acting on a path that means something different now.
		slog.Warn("[activity-relocate] recorded activity database is not present — "+
			"starting at the configured path without moving anything",
			"recorded_path", lastPath, "new_path", desiredPath, "err", err)
		return
	}

	slog.Info("[activity-relocate] activity database path changed — relocating",
		"old_path", lastPath, "new_path", desiredPath)

	// Deliberately context.Background(): a partially-copied database is discarded
	// on cancellation, so cutting the copy short at boot would leave the work to
	// be redone from scratch on the next start with nothing gained.
	st, err := database.RelocateActivityDB(context.Background(), lastPath, desiredPath)
	switch {
	case errors.Is(err, database.ErrRelocateSourceMissing):
		slog.Warn("[activity-relocate] nothing at the recorded path to move", "recorded_path", lastPath)
	case err != nil:
		// The source is intact — RelocateActivityDB guarantees that on every
		// failure path — so the history is not lost, it is merely still at the
		// old location. Say so precisely, because the next boot will start an
		// empty database at the new path and that must not look like data loss.
		slog.Error("[activity-relocate] RELOCATION FAILED — the existing activity database was NOT moved "+
			"and remains complete at its old path; a new empty database will be opened at the "+
			"configured path until this is resolved",
			"old_path", lastPath, "new_path", desiredPath, "err", err)
	default:
		slog.Info("[activity-relocate] activity database relocated",
			"old_path", st.SrcPath, "new_path", st.DstPath,
			"rows", st.Rows, "bytes", st.Bytes,
			"elapsed", st.Duration.Round(1e9).String())
	}
}
