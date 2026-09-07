// file: internal/metafetch/file_pipeline.go
// version: 2.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-09-07

// The metadata-apply rename path, expressed entirely in terms of
// internal/organizer.
//
// Until 2026-08-15 this file was a hand-copied TWIN of
// internal/organizer/pipeline.go, and internal/metafetch/path_format.go was a
// twin of internal/organizer/path_format.go. The copies were not kept in sync,
// and the drift was not cosmetic:
//
//   - The twin had NO scrubVar. The fix for a '/' inside {title} exploding into
//     one directory per path segment — real production data, "Tarkin - Star
//     Wars - 3/85", which made the scanner create 85 separate Book records —
//     landed in internal/organizer only. The LIVE apply path never got it.
//   - The twin stripped '[' and ']' and had no 200-byte component cap.
//   - The twin computed target paths from path_format while organize computed
//     them from folder_naming_pattern + file_naming_pattern. They disagreed by
//     two whole directory levels, so every apply undid the previous organize
//     and vice versa.
//
// Everything here is now an alias or a forwarder. There is one path builder,
// one sanitizer, and one rename implementation.
package metafetch

import (
	"errors"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
)

// tmpRenameSuffix is appended to the target path to form the intermediate temp
// path used by RenameFiles' two-phase rename. Aliased rather than re-declared
// so a change to the suffix cannot leave stranded files this package can no
// longer recognize.
const tmpRenameSuffix = organizer.TmpRenameSuffix

// The rename vocabulary is organizer's. These are Go type ALIASES, not new
// types: metafetch.FileRenameEntry and organizer.FileRenameEntry are the same
// type, so entries cross the package boundary without conversion and cannot
// drift apart field by field.
type (
	FileRenameEntry    = organizer.FileRenameEntry
	FilePipelineResult = organizer.FilePipelineResult
	RenameResult       = organizer.RenameFilesResult
	RelocateRequest    = organizer.RelocateRequest
	RelocateResult     = organizer.RelocateResult
	CollisionPolicy    = organizer.CollisionPolicy
)

// RenameFiles performs the two-phase rename. See organizer.RenameFiles for the
// failure semantics — in particular that entries in result.Succeeded have
// physically moved even when an error is returned, so callers must still
// persist their DB path updates.
//
// policy is threaded through rather than defaulted here: it decides what
// happens to real files when a target is occupied, so it belongs at the call
// site. Both write-back callers in this package pass one (see
// applyCollisionPolicy); nil restores the pre-2026-09-07 behaviour of failing
// the batch on any occupied target.
func RenameFiles(entries []FileRenameEntry, policy *CollisionPolicy) (*RenameResult, error) {
	return organizer.RenameFiles(entries, policy)
}

// applyCollisionPolicy builds the collision policy the metadata write-back
// paths use.
//
// It is shared by BOTH callers on purpose. runApplyPipeline and
// RunApplyPipelineRenameOnly are the same operation reached two ways
// (batch apply, and the "Save to Files" button), and the permanent-failure bug
// — an occupied target failing forever with no resolution — was identical in
// each. Giving one of them a resolver and not the other would mean the same
// book succeeds or fails depending on which button pressed it.
func applyCollisionPolicy(store organizer.CollisionStore, bookID string) *CollisionPolicy {
	return &CollisionPolicy{
		RootDir: config.AppConfig.RootDir,
		BookID:  bookID,
		Store:   store,
	}
}

// targetPathsOf is the plan the durable-failure self-heal compares against: a
// book whose computed targets no longer include the path a previous run was
// blocked on is not blocked any more, whatever is still sitting at that path.
func targetPathsOf(entries []FileRenameEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.TargetPath)
	}
	return out
}

// recordRenameCollisionFailure persists a durable failure for a book whose
// rename failed on an UNRESOLVED COLLISION, so later runs skip it instead of
// re-attempting the same doomed rename forever.
//
// Only collisions are recorded. Every other rename failure (a stranded temp
// awaiting an operator, a NAS blip, a permissions problem) keeps the old
// retry-next-run behaviour, because those genuinely can succeed on a re-run
// with no change to the world. Recording them all would convert a transient
// outage into a library-wide skip list.
func recordRenameCollisionFailure(store database.UserPreferenceStore, bookID string, result *RenameResult, renameErr error) {
	var cerr *organizer.CollisionError
	if !errors.As(renameErr, &cerr) || len(cerr.Failures) == 0 {
		return
	}
	// Record the FIRST unresolved collision. One is enough to block the book,
	// and it is the one an operator has to look at first; the rest are in the
	// result and the log.
	f := cerr.Failures[0]
	organizer.RecordApplyRenameFailure(store, organizer.ApplyRenameFailure{
		BookID:          bookID,
		TargetPath:      f.TargetPath,
		OccupantPath:    f.OccupantPath,
		OccupantSize:    f.OccupantSize,
		OccupantModUnix: f.OccupantModUnix,
		Reason:          f.Reason,
	})
	// Paths and the reason string are user-controlled (tags, filesystem), and
	// this call reaches log/slog directly rather than through a logger that
	// applies the barrier itself — see logging.Sanitize.
	slog.Warn("apply rename: recording a durable collision failure — this book is skipped until the blocking file changes or goes away",
		"book_id", logging.Sanitize(bookID),
		"target_path", logging.Sanitize(f.TargetPath),
		"occupant_path", logging.Sanitize(f.OccupantPath),
		"reason", logging.Sanitize(f.Reason),
		"unresolved_total", len(cerr.Failures),
		"resolved_total", len(result.Resolutions))
}

// newPathOrganizer builds the Organizer the apply path plans renames with.
//
// The store matters: without it, a book whose Author/Series objects are not
// populated resolves to an EMPTY author, and the "Unknown Author" fallback
// files it under the placeholder — the exact mistake the 2026-08-11 mass
// reorganize made 23,622 times. Wiring mfs.db means the apply path follows
// AuthorID/SeriesID the same way organize does.
func newPathOrganizer(store organizer.OrganizerStore) *organizer.Organizer {
	org := organizer.NewOrganizer(&config.AppConfig)
	org.SetStore(store)
	return org
}

// ComputeTargetPaths computes the target path for every file of a book using
// the SAME builder the organize path uses. See organizer.ComputeTargetPaths.
func ComputeTargetPaths(rootDir, folderPattern, filePattern string, files []database.BookFile, vars organizer.PathVars, opts organizer.BuildOpts) ([]FileRenameEntry, error) {
	return organizer.ComputeTargetPaths(rootDir, folderPattern, filePattern, files, vars, opts)
}
