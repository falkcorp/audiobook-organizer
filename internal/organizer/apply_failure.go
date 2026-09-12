// file: internal/organizer/apply_failure.go
// version: 1.2.0
// guid: 8a2d64f1-0c53-4b97-91ae-63f7c0d5b284
// last-edited: 2026-09-12

// Durable per-book failure records for the metadata apply pipeline's rename
// phase.
//
// # The problem
//
// service_writeback.go deliberately does NOT set the rename checkpoint when
// RenameFiles returns an error, so the next apply run retries the rename. For a
// TRANSIENT failure (a stranded temp to resume, a NAS blip) that is right. For
// a collision that the resolver cannot resolve it is a permanent loop: the same
// book is re-attempted on every run, forever, and never advances past
// phaseRename. The user's requirement was blunt — "don't let one bad item
// constantly make it fail."
//
// # The shape of the fix
//
// This mirrors the precedent in `fix(transcribe): skip durably-failed books,
// self-heal when their source returns` (#3086, 33597e8f6): record the durable
// failure, EXCLUDE the book from later runs, and re-promote it automatically
// the moment the blocking condition clears — no flag, no operator action.
//
// The self-heal check has one condition the transcribe precedent does not need.
// Transcription re-attempts when the SOURCE returns or changes. A rename can
// also become possible because the TARGET moved: re-applying metadata changes
// the computed path, so a record keyed on the book alone would suppress a
// rename that would now succeed somewhere else entirely. So the record stores
// the target it failed on, and a book whose newly-computed targets no longer
// include that path is not blocked.
//
// # Storage
//
// The same `_system` user-preference namespace the phase checkpoints already
// use (checkpoint.go), for the same reason: no schema change, and the record is
// a small per-book value that a maintenance op can enumerate through
// GetAllPreferencesForUser and clear. Clearing writes "" — the same convention
// ClearCheckpoints uses — so every reader MUST treat an empty value as "no
// record" BEFORE attempting to decode it, or a cleared record would surface as
// a decode error forever.
package organizer

import (
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ApplyRenameFailurePrefix namespaces the per-book records. The maintenance op
// that clears them enumerates on this prefix.
const ApplyRenameFailurePrefix = "apply_rename_failure:"

// ApplyRenameFailure is the durable record of a rename the apply pipeline could
// not complete, plus everything needed to notice that the block has cleared.
type ApplyRenameFailure struct {
	BookID string `json:"book_id"`
	// TargetPath is the path the rename was blocked ON. A later run that
	// computes a different target is not blocked by this record.
	TargetPath string `json:"target_path"`
	// OccupantPath, OccupantSize and OccupantModUnix fingerprint the file that
	// was in the way. Any change to them means the situation is not the one
	// that was recorded, so the book is retried.
	OccupantPath    string `json:"occupant_path"`
	OccupantSize    int64  `json:"occupant_size"`
	OccupantModUnix int64  `json:"occupant_mod_unix"`
	Reason          string `json:"reason"`
	RecordedAt      string `json:"recorded_at"`
	// Category is the organize outcome category (Outcome* in
	// inplace_collision.go). Empty for apply-pipeline records.
	Category string `json:"category,omitempty"`
	// SourcePath, SourceSize and SourceModUnix fingerprint the file that
	// could not be moved. Organize records set them so a pair is retried when
	// EITHER side changes; apply records leave them empty and are not
	// compared on them.
	SourcePath    string `json:"source_path,omitempty"`
	SourceSize    int64  `json:"source_size,omitempty"`
	SourceModUnix int64  `json:"source_mod_unix,omitempty"`
}

// OrganizeCollisionSkipPrefix namespaces organize's in-place collision skips.
// Separate from ApplyRenameFailurePrefix so an apply block never suppresses an
// organize (or the reverse); the clear-apply-rename-failures op clears both.
const OrganizeCollisionSkipPrefix = "organize_collision_skip:"

// DurableSkipStore is the two preference methods the durable records need —
// narrower than database.UserPreferenceStore so the organizer's own Store can
// satisfy it without taking on the rest.
type DurableSkipStore interface {
	GetUserPreferenceForUser(userID, key string) (*database.UserPreferenceKV, error)
	SetUserPreferenceForUser(userID, key, value string) error
}

// RecordApplyRenameFailure persists the durable failure for a book. Failing to
// write it is logged and otherwise ignored: the consequence is the old
// behaviour (retry next run), which is a regression in efficiency, not in
// correctness.
func recordDurableSkip(store DurableSkipStore, prefix string, f ApplyRenameFailure) {
	if store == nil || strings.TrimSpace(f.BookID) == "" {
		return
	}
	if f.RecordedAt == "" {
		f.RecordedAt = time.Now().Format(time.RFC3339)
	}
	blob, err := json.Marshal(f)
	if err != nil {
		slog.Warn("could not encode apply rename failure record", "book_id", logger.SanitizeLogValue(f.BookID), "error", err)
		return
	}
	if err := store.SetUserPreferenceForUser("_system", prefix+f.BookID, string(blob)); err != nil {
		slog.Warn("could not persist apply rename failure record", "book_id", logger.SanitizeLogValue(f.BookID), "error", err)
	}
}

// LoadApplyRenameFailure returns the record for a book, or (nil, false).
//
// An EMPTY stored value means "cleared", not "corrupt": ClearCheckpoints and
// ClearApplyRenameFailure both blank the value rather than deleting the row, so
// the emptiness check has to come before the JSON decode.
func loadDurableSkip(store DurableSkipStore, prefix, bookID string) (*ApplyRenameFailure, bool) {
	if store == nil || strings.TrimSpace(bookID) == "" {
		return nil, false
	}
	pref, err := store.GetUserPreferenceForUser("_system", prefix+bookID)
	if err != nil || pref == nil || strings.TrimSpace(pref.Value) == "" {
		return nil, false
	}
	var f ApplyRenameFailure
	if err := json.Unmarshal([]byte(pref.Value), &f); err != nil {
		// A record we cannot read must not block the book forever — that is
		// the failure mode this whole file exists to remove.
		// book_id and every path below are user-controlled; this package logs
		// through log/slog directly, which applies no barrier of its own.
		slog.Warn("discarding unreadable apply rename failure record", "book_id", logger.SanitizeLogValue(bookID), "error", err)
		return nil, false
	}
	return &f, true
}

// ClearApplyRenameFailure removes the record so the book is attempted again.
// Called on a successful rename and by the maintenance op that exists so a bad
// classification is never permanent.
func clearDurableSkip(store DurableSkipStore, prefix, bookID string) {
	if store == nil || strings.TrimSpace(bookID) == "" {
		return
	}
	_ = store.SetUserPreferenceForUser("_system", prefix+bookID, "")
}

// ApplyRenameBlocked reports whether a book should SKIP its rename phase this
// run because a previous run recorded a durable failure that has not cleared.
//
// It self-heals — returning false and clearing the record — as soon as any of
// these is true:
//
//   - the occupant is gone from disk;
//   - the occupant's size or mtime differs from what was recorded (someone
//     replaced or rewrote it, so this is a different situation);
//   - none of the targets this run computed is the target that failed (the
//     book's metadata changed and it is now heading somewhere else entirely).
//
// plannedTargets is the current run's target set. Passing nil means "the caller
// has no plan to compare", and only the disk conditions are checked.
func durableSkipBlocked(store DurableSkipStore, prefix, bookID string, plannedTargets []string, source string) bool {
	rec, ok := loadDurableSkip(store, prefix, bookID)
	if !ok {
		return false
	}

	if len(plannedTargets) > 0 {
		stillPlanned := false
		for _, t := range plannedTargets {
			if t == rec.TargetPath {
				stillPlanned = true
				break
			}
		}
		if !stillPlanned {
			slog.Info("durable skip cleared — the book no longer targets the blocked path",
				"book_id", logger.SanitizeLogValue(bookID), "blocked_target", logger.SanitizeLogValue(rec.TargetPath))
			clearDurableSkip(store, prefix, bookID)
			return false
		}
	}

	occupant := rec.OccupantPath
	if occupant == "" {
		occupant = rec.TargetPath
	}
	info, err := os.Lstat(occupant)
	if err != nil {
		slog.Info("durable skip cleared — the blocking file is gone",
			"book_id", logger.SanitizeLogValue(bookID), "occupant", logger.SanitizeLogValue(occupant))
		clearDurableSkip(store, prefix, bookID)
		return false
	}
	if info.Size() != rec.OccupantSize || info.ModTime().Unix() != rec.OccupantModUnix {
		slog.Info("durable skip cleared — the blocking file changed",
			"book_id", logger.SanitizeLogValue(bookID), "occupant", logger.SanitizeLogValue(occupant),
			"recorded_size", rec.OccupantSize, "now_size", info.Size())
		clearDurableSkip(store, prefix, bookID)
		return false
	}

	// Organize records also fingerprint the SOURCE: a source that moved,
	// changed size or was rewritten is a different situation.
	if rec.SourcePath != "" {
		si, serr := os.Lstat(rec.SourcePath)
		if (source != "" && source != rec.SourcePath) || serr != nil ||
			si.Size() != rec.SourceSize || si.ModTime().Unix() != rec.SourceModUnix {
			slog.Info("durable skip cleared — the source file moved or changed",
				"book_id", logger.SanitizeLogValue(bookID), "source", logger.SanitizeLogValue(rec.SourcePath))
			clearDurableSkip(store, prefix, bookID)
			return false
		}
	}

	return true
}

// RecordApplyRenameFailure persists an apply-pipeline durable failure. See
// recordDurableSkip.
func RecordApplyRenameFailure(store database.UserPreferenceStore, f ApplyRenameFailure) {
	recordDurableSkip(store, ApplyRenameFailurePrefix, f)
}

// LoadApplyRenameFailure returns a book's apply record. See loadDurableSkip.
func LoadApplyRenameFailure(store database.UserPreferenceStore, bookID string) (*ApplyRenameFailure, bool) {
	return loadDurableSkip(store, ApplyRenameFailurePrefix, bookID)
}

// ClearApplyRenameFailure removes a book's apply record. See clearDurableSkip.
func ClearApplyRenameFailure(store database.UserPreferenceStore, bookID string) {
	clearDurableSkip(store, ApplyRenameFailurePrefix, bookID)
}

// ApplyRenameBlocked reports whether the apply rename phase should skip a
// book. See durableSkipBlocked.
func ApplyRenameBlocked(store database.UserPreferenceStore, bookID string, plannedTargets []string) bool {
	return durableSkipBlocked(store, ApplyRenameFailurePrefix, bookID, plannedTargets, "")
}

// RecordOrganizeCollisionSkip persists organize's durable skip for a declined
// in-place destination conflict.
func RecordOrganizeCollisionSkip(store DurableSkipStore, f ApplyRenameFailure) {
	recordDurableSkip(store, OrganizeCollisionSkipPrefix, f)
}

// OrganizeCollisionBlocked reports whether moving bookID from source to target
// was declined on an earlier run and nothing has changed since: same target,
// same source path, and neither file's size nor mtime differs. Any change
// clears the record and returns false, so the pair is retried.
func OrganizeCollisionBlocked(store DurableSkipStore, bookID, target, source string) bool {
	return durableSkipBlocked(store, OrganizeCollisionSkipPrefix, bookID, []string{target}, source)
}
