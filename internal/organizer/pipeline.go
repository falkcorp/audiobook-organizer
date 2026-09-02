// file: internal/organizer/pipeline.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8901-bcde-f01234567890
// last-edited: 2026-09-02

package organizer

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TmpRenameSuffix is appended to the target path to form the intermediate temp
// path used by RenameFiles' two-phase rename: `<target>.tmp-rename-<nonce>`
// (see renameTempPath; runs before 2026-09-02 used the bare suffix, and
// strandedRenameTemps still recognises that shape). Exported so
// internal/metafetch shares the one constant: a file stranded at a temp path is
// only recoverable by a process that recognizes the suffix, so two copies of
// it would mean two definitions of "recoverable".
const TmpRenameSuffix = ".tmp-rename"

// FileRenameEntry represents a planned file rename operation.
type FileRenameEntry struct {
	SegmentID  string `json:"segment_id"`
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
}

// FilePipelineResult holds the results of a file pipeline operation.
type FilePipelineResult struct {
	Entries []FileRenameEntry `json:"entries"`
	Renamed int               `json:"renamed"`
	Errors  []string          `json:"errors,omitempty"`
}

// ComputeTargetPaths computes the target file paths for all files of a book
// through BuildRelPath — the SAME composer Organizer.generateTargetPath uses.
//
// It used to run its own builder (FormatPath, driven by path_format) while
// organize ran another (driven by folder_naming_pattern + file_naming_pattern).
// Under the production config of 2026-08-15 they disagreed by two whole
// directory levels, and since ReOrganizeInPlace is a true os.Rename, each one
// dragged a book back toward its own answer indefinitely. Both now expand the
// same two patterns; a book that is already organized produces zero entries
// instead of a rename back and forth.
//
// It returns an error rather than a best-effort path when a pattern is broken:
// a bad pattern must stop the rename, not quietly relocate the whole library to
// a path built from a half-substituted template.
func ComputeTargetPaths(rootDir, folderPattern, filePattern string, files []database.BookFile, vars PathVars, opts BuildOpts) ([]FileRenameEntry, error) {
	planned, err := planTargetPaths(rootDir, folderPattern, filePattern, files, vars, opts)
	if err != nil {
		return nil, err
	}
	var entries []FileRenameEntry
	for _, e := range planned {
		if e.TargetPath != e.SourcePath {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// planTargetPaths is the shared core: it returns the target path for EVERY
// non-missing file, including files already sitting at their target.
//
// ComputeTargetPaths drops those (a rename plan must not list no-op renames),
// but OrganizeBookDirectory must not: its pathMap is the authority for what each
// book_file row's file_path gets set to, and a file that is already in the right
// place still needs a map entry saying so. Before 2026-08-15 the two computed
// filenames independently -- OrganizeBookDirectory kept filepath.Base(src) and
// never applied the file naming pattern at all -- so a directory book was folder
// aware but not file aware.
func planTargetPaths(rootDir, folderPattern, filePattern string, files []database.BookFile, vars PathVars, opts BuildOpts) ([]FileRenameEntry, error) {
	if rootDir == "" || len(files) == 0 {
		return nil, nil
	}

	// Normalize the row set HERE rather than trusting callers to agree on it.
	//
	// The plan is a pure function of (root, patterns, rows, vars), so the three
	// paths agree only if they pass the same rows -- and they did not:
	// OrganizeDirectoryBook pre-filtered empty-FilePath rows while
	// CreateOrganizedVersion and the metafetch apply paths passed GetBookFiles
	// straight through. One empty-path row is enough to break it: it changes
	// totalTracks, and since "" sorts first it shifts every position-derived
	// track number by one. Organize would copy to "... - 07.mp3" while the row
	// writer planned "... - 08.mp3", found nothing there, and fell back to the
	// un-organized source -- which still exists, because every organize strategy
	// is reflink/hardlink/copy/symlink and never a move. The organized book's
	// rows would then point at the original book's files.
	//
	// A row with no path is not a file, so it is dropped outright. Rows flagged
	// Missing are NOT dropped: see the totalTracks note below.
	sorted := make([]database.BookFile, 0, len(files))
	seenPath := make(map[string]struct{}, len(files))
	dupes := 0
	for _, f := range files {
		if f.FilePath == "" {
			continue
		}
		// Collapse duplicate book_file rows for ONE path. Planning both gives
		// two entries with the same SourcePath: the first rename moves the
		// file and the second fails ENOENT. It also inflates totalTracks,
		// which renumbered a 21-file book as 42 tracks in production on
		// 2026-08-21. This runs BEFORE the sort.Slice below, so "first wins"
		// means first in the caller's order, not first by track number --
		// the sort is not stable with respect to equal (TrackNumber,
		// FilePath) pairs, so deduping after it would make the survivor
		// non-deterministic.
		key := filepath.Clean(strings.TrimSpace(f.FilePath))
		if _, ok := seenPath[key]; ok {
			dupes++
			continue
		}
		seenPath[key] = struct{}{}
		sorted = append(sorted, f)
	}
	if dupes > 0 {
		slog.Warn("duplicate book_file paths collapsed while planning target paths",
			"title", vars.Title, "rows", len(files), "distinct", len(sorted), "collapsed", dupes)
	}
	if len(sorted) == 0 {
		return nil, nil
	}
	sort.Slice(sorted, func(i, j int) bool {
		ti := sorted[i].TrackNumber
		tj := sorted[j].TrackNumber
		if ti != 0 && tj != 0 {
			if ti != tj {
				return ti < tj
			}
		} else if ti != 0 {
			return true
		} else if tj != 0 {
			return false
		}
		return sorted[i].FilePath < sorted[j].FilePath
	})

	// totalTracks counts every row, INCLUDING rows flagged Missing, which are
	// then skipped in the loop. That is deliberate: a 12-file book with 11 files
	// missing off disk is still a 12-track book, so its one surviving file is
	// named "<title> - 07" and keeps the track number it will still have after
	// the others are restored. Counting only the survivors would renumber the
	// whole book every time a file went missing or came back.
	totalTracks := len(sorted)

	entries, collided, err := planPass(rootDir, folderPattern, filePattern, sorted, totalTracks, vars, opts, false)
	if err != nil {
		return nil, err
	}

	// A file naming pattern with no {track} placeholder gives every file of a
	// multi-file book the SAME target. That was harmless while the destination
	// filename was filepath.Base(src) and this function only planned renames;
	// now that organize copies to these paths, it would collapse a 40-part book
	// into one file. The production pattern on 2026-08-15 was
	// "{title} - {author} - read by {narrator}" — no track placeholder at all —
	// so this is the live configuration, not a hypothetical.
	//
	// Disambiguate rather than refuse: a numbered suffix keeps the book intact
	// and is what the pattern was missing. The suffix goes on EVERY file of the
	// book, not just the colliding ones, so the numbering is uniform.
	if collided {
		slog.Warn("file naming pattern does not distinguish the files of a multi-file book — appending a track suffix so they do not overwrite each other",
			"file_naming_pattern", filePattern,
			"title", vars.Title,
			"files", totalTracks)
		entries, _, err = planPass(rootDir, folderPattern, filePattern, sorted, totalTracks, vars, opts, true)
		if err != nil {
			return nil, err
		}
	}

	return entries, nil
}

// planPass builds one full set of target paths. forceTrackSuffix appends the
// zero-padded track number to every stem; planTargetPaths turns it on for the
// second pass when the first produced duplicate targets. It reports whether any
// two files landed on the same target.
func planPass(rootDir, folderPattern, filePattern string, sorted []database.BookFile, totalTracks int, vars PathVars, opts BuildOpts, forceTrackSuffix bool) ([]FileRenameEntry, bool, error) {
	// Pad to the width of the largest track number so 9/10 sort as 09/10.
	width := max(len(fmt.Sprintf("%d", totalTracks)), 2)

	var entries []FileRenameEntry
	seen := make(map[string]struct{}, len(sorted))
	collided := false

	for i, f := range sorted {
		if f.Missing {
			continue
		}

		trackNum := i + 1
		if f.TrackNumber != 0 {
			trackNum = f.TrackNumber
		}

		ext := strings.TrimPrefix(filepath.Ext(f.FilePath), ".")
		if ext == "" {
			ext = f.Format
		}

		segVars := vars
		segVars.Ext = ext
		segVars.Track = trackNum
		segVars.TotalTracks = totalTracks

		// A one-file book has no track to name. Leaving Track set to 1 would
		// make the default pattern "{title} - {track:02d}" produce
		// "Foundation - 01.m4b" for a book that has exactly one file — the
		// segment has to be ABSENT, not 1, for the empty-segment rule to drop
		// it. This is what lets ONE pattern serve both book layouts.
		if totalTracks <= 1 {
			segVars.Track = 0
			segVars.TotalTracks = 0
		}

		relPath, err := BuildRelPath(folderPattern, filePattern, segVars, opts)
		if err != nil {
			return nil, false, err
		}
		if forceTrackSuffix && totalTracks > 1 {
			relPath += fmt.Sprintf(" - %0*d", width, trackNum)
		}
		targetPath := filepath.Join(rootDir, relPath)
		if ext != "" {
			targetPath += "." + ext
		}

		if _, dup := seen[targetPath]; dup {
			collided = true
		}
		seen[targetPath] = struct{}{}

		entries = append(entries, FileRenameEntry{
			SegmentID:  f.ID,
			SourcePath: f.FilePath,
			TargetPath: targetPath,
		})
	}

	return entries, collided, nil
}

// ComputeTargetPaths plans the rename of every file of a book using this
// Organizer's config, store and naming patterns.
//
// This method — not the package function — is what other packages should call.
// The metadata-apply path used to assemble its own variables, its own patterns
// and its own builder, and every one of the three differed from organize's.
// Going through the Organizer means the apply path resolves author and series
// through the same store lookups, applies the same "Unknown Author" fallback,
// and expands the same two patterns. It cannot arrive at a different answer,
// which is the only durable form of "the two agree".
func (o *Organizer) ComputeTargetPaths(book *database.Book, files []database.BookFile) ([]FileRenameEntry, error) {
	return ComputeTargetPaths(
		o.config.RootDir,
		o.config.FolderNamingPattern,
		o.config.FileNamingPattern,
		files,
		o.pathVars(book, 0, 0, ""),
		o.buildOpts(),
	)
}

// ComputeTargetPathsFromSegments is a backward-compatible wrapper that accepts
// BookSegment slices and converts them to BookFile before computing paths.
// Deprecated: callers should use ComputeTargetPaths with []BookFile directly.
func ComputeTargetPathsFromSegments(rootDir, folderPattern, filePattern string, segments []database.BookSegment, vars PathVars, opts BuildOpts) ([]FileRenameEntry, error) {
	files := make([]database.BookFile, 0, len(segments))
	for _, seg := range segments {
		trackNum := 0
		if seg.TrackNumber != nil {
			trackNum = *seg.TrackNumber
		}
		trackCount := 0
		if seg.TotalTracks != nil {
			trackCount = *seg.TotalTracks
		}
		bf := database.BookFile{
			ID:          seg.ID,
			BookID:      fmt.Sprintf("%d", seg.BookID),
			FilePath:    seg.FilePath,
			Format:      seg.Format,
			FileSize:    seg.SizeBytes,
			Duration:    seg.DurationSec * 1000, // seconds to milliseconds
			TrackNumber: trackNum,
			TrackCount:  trackCount,
			Missing:     !seg.Active,
		}
		if seg.FileHash != nil {
			bf.FileHash = *seg.FileHash
		}
		if seg.SegmentTitle != nil {
			bf.Title = *seg.SegmentTitle
		}
		files = append(files, bf)
	}
	return ComputeTargetPaths(rootDir, folderPattern, filePattern, files, vars, opts)
}

// RenameFilesResult holds the outcome of a rename operation.
type RenameFilesResult struct {
	Succeeded []FileRenameEntry `json:"succeeded"`
	Skipped   []FileRenameEntry `json:"skipped"` // source not found
	Errors    []string          `json:"errors,omitempty"`
}

// renameTemp tracks a file parked at its intermediate temp path during a
// two-phase rename.
type renameTemp struct {
	TempPath string
	Entry    FileRenameEntry
}

// rollbackRenameTemps returns files parked at their temp paths back to their
// original source paths. A rollback failure is loud: a file left at a
// .tmp-rename path is invisible to the library (its DB row points at the old
// source path), so each failure is logged as an Error with both paths and
// recorded in result.Errors — never silently dropped.
//
// The rollback is a safeRename, not a bare os.Rename: the source path was
// vacated by phase 1 moments ago, but "moments ago" is exactly the window in
// which a concurrent worker can land its own file there, and a bare rename
// would replace it. Refusing leaves this file stranded at the temp (reported
// above); replacing would destroy the other one silently.
func rollbackRenameTemps(temps []renameTemp, result *RenameFilesResult) {
	for _, t := range temps {
		if err := safeRename(t.TempPath, t.Entry.SourcePath); err != nil {
			slog.Error("RenameFiles rollback failed — file stranded at temp path",
				"temp_path", t.TempPath,
				"source_path", t.Entry.SourcePath,
				"target_path", t.Entry.TargetPath,
				"error", err)
			result.Errors = append(result.Errors, fmt.Sprintf(
				"rollback failed, file stranded at %s (source %s): %v",
				t.TempPath, t.Entry.SourcePath, err))
		}
	}
}

// renameTempPath returns the phase-1 parking name for target: the target path,
// TmpRenameSuffix, and a per-call nonce. The nonce is what makes two workers
// renaming into the same target independent. With the fixed
// `target+TmpRenameSuffix` both parked on ONE name: the second safeRename saw
// the first worker's temp, refused, and rolled back — the good case — or, when
// the first worker's phase 2 had just vacated the temp, parked on the freed
// name and the two then raced for the target with rename(2), which replaces
// silently. Phase 2 now uses finalizeExclusive, but the shared parking name
// was a second way for the workers to meet, and it is removed rather than
// reasoned about.
func renameTempPath(target string) string {
	return target + TmpRenameSuffix + "-" + tempNonce()
}

// strandedRenameTemps finds files a previous, interrupted RenameFiles left
// parked for target: the legacy fixed name `target+TmpRenameSuffix` (runs
// before 2026-09-02) and any nonce-suffixed `target+TmpRenameSuffix+"-*"`.
// Directory entries are ignored; a parked file is always a regular file.
func strandedRenameTemps(target string) ([]string, error) {
	var found []string
	legacy := target + TmpRenameSuffix
	if info, err := os.Lstat(legacy); err == nil && info.Mode().IsRegular() {
		found = append(found, legacy)
	}
	// filepath.Glob treats the target's own characters as pattern syntax, so
	// escape them; only our "-*" tail is meant to match anything.
	matches, err := filepath.Glob(globEscape(legacy) + "-*")
	if err != nil {
		return nil, fmt.Errorf("scan for stranded temps of %s: %w", target, err)
	}
	for _, m := range matches {
		if info, err := os.Lstat(m); err == nil && info.Mode().IsRegular() {
			found = append(found, m)
		}
	}
	return found, nil
}

// globEscape quotes every filepath.Match metacharacter in s so it matches
// itself. Library paths contain '[' and '*' (track titles, "[Unabridged]")
// often enough that an unescaped glob would silently match the wrong files or
// none at all.
func globEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '*', '?', '[', '\\':
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// RenameFiles performs atomic file renames using a temp intermediate step
// to avoid conflicts when source and target overlap.
// Missing source files are skipped (not fatal) and reported in the result.
//
// Failure semantics:
//   - A file stranded at its temp path by a previously interrupted run (temp
//     exists, source doesn't) is picked up and resumed through phase 2 instead
//     of being skipped forever. If MORE than one stranded temp exists for a
//     target, the batch fails before anything moves: picking one would publish
//     a file whose identity nobody verified, and the choice is an operator's.
//   - On any phase failure, every file still parked at a temp path is rolled
//     back to its source path; rollback failures are logged and recorded in
//     result.Errors.
//   - Entries that already reached their final path before the failure remain
//     in result.Succeeded — callers must persist DB path updates for them even
//     when an error is returned.
//   - Phase 1 parks each file on a per-call unique temp name (renameTempPath),
//     and phase 2 publishes it with finalizeExclusive, which cannot replace a
//     destination that exists — a collision fails the batch and rolls back
//     instead of silently destroying bytes, even against a concurrent worker
//     that appeared between the pre-check and the syscall.
func RenameFiles(entries []FileRenameEntry) (*RenameFilesResult, error) {
	result := &RenameFilesResult{}
	if len(entries) == 0 {
		return result, nil
	}

	// Pre-filter: skip entries where source doesn't exist — unless the file
	// was stranded at its temp path by an interrupted phase 2, in which case
	// it re-enters phase 2 below so the rename completes.
	var valid []FileRenameEntry
	var temps []renameTemp
	for _, entry := range entries {
		if _, err := os.Stat(entry.SourcePath); os.IsNotExist(err) {
			stranded, serr := strandedRenameTemps(entry.TargetPath)
			if serr != nil {
				return result, serr
			}
			switch len(stranded) {
			case 0:
				result.Skipped = append(result.Skipped, entry)
			case 1:
				slog.Warn("RenameFiles resuming stranded temp file from interrupted rename",
					"temp_path", stranded[0], "target_path", entry.TargetPath)
				temps = append(temps, renameTemp{TempPath: stranded[0], Entry: entry})
			default:
				msg := fmt.Sprintf("%d stranded temp files for %s (source %s is gone); refusing to guess which is the file: %s",
					len(stranded), entry.TargetPath, entry.SourcePath, strings.Join(stranded, ", "))
				slog.Error("RenameFiles found ambiguous stranded temps — operator must resolve",
					"target_path", entry.TargetPath, "source_path", entry.SourcePath, "temps", stranded)
				result.Errors = append(result.Errors, msg)
				return result, errors.New(msg)
			}
			continue
		}
		valid = append(valid, entry)
	}

	if len(valid) == 0 && len(temps) == 0 {
		return result, nil
	}

	// Phase 1: rename source -> temp
	for _, entry := range valid {
		// Ensure target directory exists
		targetDir := filepath.Dir(entry.TargetPath)
		if err := os.MkdirAll(targetDir, 0o775); err != nil {
			rollbackRenameTemps(temps, result)
			return result, fmt.Errorf("create target dir %s: %w", targetDir, err)
		}

		tempPath := renameTempPath(entry.TargetPath)
		if err := safeRename(entry.SourcePath, tempPath); err != nil {
			// Rollback temps already moved
			rollbackRenameTemps(temps, result)
			return result, fmt.Errorf("rename %s -> temp: %w", entry.SourcePath, err)
		}
		temps = append(temps, renameTemp{TempPath: tempPath, Entry: entry})
	}

	// Phase 2: publish temp -> final without ever replacing an occupant. On
	// failure, roll back this and every remaining temp so no file is left
	// stranded at a .tmp-rename path.
	for i, t := range temps {
		if err := finalizeExclusive(t.TempPath, t.Entry.TargetPath); err != nil {
			rollbackRenameTemps(temps[i:], result)
			return result, fmt.Errorf("rename temp -> %s: %w", t.Entry.TargetPath, err)
		}
		result.Succeeded = append(result.Succeeded, t.Entry)
	}

	return result, nil
}

// RelocateRequest represents a request to relocate book files.
type RelocateRequest struct {
	SegmentID  string `json:"segment_id,omitempty"`
	NewPath    string `json:"new_path,omitempty"`
	FolderPath string `json:"folder_path,omitempty"`
}

// RelocateResult holds the outcome of a relocate operation.
type RelocateResult struct {
	Updated int      `json:"updated"`
	Errors  []string `json:"errors,omitempty"`
}
