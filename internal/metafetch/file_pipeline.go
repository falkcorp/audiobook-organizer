// file: internal/metafetch/file_pipeline.go
// version: 2.0.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-08-15

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
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
)

// RenameFiles performs the two-phase rename. See organizer.RenameFiles for the
// failure semantics — in particular that entries in result.Succeeded have
// physically moved even when an error is returned, so callers must still
// persist their DB path updates.
func RenameFiles(entries []FileRenameEntry) (*RenameResult, error) {
	return organizer.RenameFiles(entries)
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
