// file: internal/database/book_own_folder.go
// version: 2.0.0
// guid: 83d7e159-50f7-47e5-8303-8d8212c3bd8e
// last-edited: 2026-09-25

package database

import (
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Out-of-folder copies.
//
// WHY this exists: a book's book_file rows can point at several copies of the
// same content — the iTunes copy, the organized library copy, and chapter
// files left behind by an older layout. Every consumer that sums the rows
// (the ABS mapper, the ABS progress/userdata duration, RecomputeBookAggregates)
// then counts that content once per copy.
//
// No existing BookFile field can mark the extra copies out of the sum:
//   - Missing is ignored by the ABS mapper (it sums every row), and
//     ComputeBookRuntime drops a missing row only when it is a content
//     duplicate of a present one. It would also be false: the files exist,
//     and the next rescan's stat flips it back.
//   - SkipScan and VersionID are honoured by neither sum; there is no
//     superseded / role / kind field.
//
// Rows outside the book's own folder are NOT all copies, though: a merge
// moves the loser's rows to the winner, and they stay in the loser's folder
// until organize runs. They are real content and must count (the owner's
// decision of 2026-09-25, "skip only copies";
// TestMoveBookFilesToBookRecomputesBothBooks pins it).
//
// So the rule is: when a book's rows span its own folder and somewhere else,
// a row outside the own folder is excluded from the sums ONLY when it is a
// copy of a present own-folder row (IsBookFileCopy). Every other row counts.
// With no present own-folder row there is no basis to call anything a copy,
// and every row counts. The rows are never deleted or rewritten — the copies
// are simply not summed.

// OwnFolderSplit partitions a book's rows by the book's own folder (the
// directory of Book.FilePath) and marks the out-of-folder copies.
type OwnFolderSplit struct {
	// Dir is the book's own folder, or "" when Book.FilePath gives none.
	Dir string
	// Own are the rows inside Dir (recursively: CD1/, CD2/ subfolders count).
	Own []BookFile
	// Copies are the rows outside Dir that are copies of a present Own row
	// (IsBookFileCopy). They are the only rows excluded from the sums.
	Copies []BookFile
	// Others are the rows outside Dir that are not copies. They count.
	Others []BookFile

	// copyIdx are the Copies' indexes in the files slice that was split.
	copyIdx []int
}

// Spans reports whether the rows lie both inside the own folder and outside it.
func (s OwnFolderSplit) Spans() bool {
	return len(s.Own) > 0 && len(s.Copies)+len(s.Others) > 0
}

// HasOwnBasis reports whether at least one own-folder row is present on disk:
// the only rows a copy is matched against. Without one Book.FilePath may be
// the stale one (book.file_path is known to lag the active book_file rows),
// and nothing is called a copy.
func (s OwnFolderSplit) HasOwnBasis() bool {
	for i := range s.Own {
		if !s.Own[i].Missing {
			return true
		}
	}
	return false
}

// Counted returns the rows every sum over this book must use: files without
// the Copies, in their original order. With no copies it returns files
// unchanged.
func (s OwnFolderSplit) Counted(files []BookFile) []BookFile {
	if len(s.Copies) == 0 {
		return files
	}
	drop := make(map[int]bool, len(s.Copies))
	for _, i := range s.copyIdx {
		drop[i] = true
	}
	out := make([]BookFile, 0, len(files)-len(drop))
	for i := range files {
		if !drop[i] {
			out = append(out, files[i])
		}
	}
	return out
}

// SplitOwnFolderFiles partitions files by the book's own folder and marks the
// out-of-folder copies. It keeps the order of files within each part.
func SplitOwnFolderFiles(book *Book, files []BookFile) OwnFolderSplit {
	var s OwnFolderSplit
	if book == nil {
		return s
	}
	s.Dir = bookOwnFolder(book.FilePath, files)
	if s.Dir == "" {
		return s
	}
	var outIdx []int
	for i := range files {
		if pathutil.IsWithin(files[i].FilePath, s.Dir) {
			s.Own = append(s.Own, files[i])
		} else {
			outIdx = append(outIdx, i)
		}
	}
	var present []BookFile
	for i := range s.Own {
		if !s.Own[i].Missing {
			present = append(present, s.Own[i])
		}
	}
	for _, i := range outIdx {
		if isCopyOfAny(files[i], present) {
			s.Copies = append(s.Copies, files[i])
			s.copyIdx = append(s.copyIdx, i)
		} else {
			s.Others = append(s.Others, files[i])
		}
	}
	return s
}

// OwnFolderFiles is SplitOwnFolderFiles(book, files).Counted(files): the rows
// a book's duration and size sums must count.
func OwnFolderFiles(book *Book, files []BookFile) []BookFile {
	return SplitOwnFolderFiles(book, files).Counted(files)
}

// IsBookFileCopy reports whether a is a copy of b. It is a copy when EITHER:
//   - both carry the same non-empty FileHash; OR
//   - they share a file name (a non-empty OriginalFilename or the base name
//     of FilePath, compared across both), have the same known FileSize
//     (> 0: an unmeasured size is no evidence), and their Durations are
//     within 1 s of each other.
func IsBookFileCopy(a, b BookFile) bool {
	if a.FileHash != "" && a.FileHash == b.FileHash {
		return true
	}
	if a.FileSize <= 0 || a.FileSize != b.FileSize {
		return false
	}
	if d := a.Duration - b.Duration; d > 1 || d < -1 {
		return false
	}
	for _, x := range bookFileNames(a) {
		for _, y := range bookFileNames(b) {
			if x == y {
				return true
			}
		}
	}
	return false
}

func isCopyOfAny(f BookFile, own []BookFile) bool {
	for i := range own {
		if IsBookFileCopy(f, own[i]) {
			return true
		}
	}
	return false
}

// bookFileNames are the non-empty names a row is known by.
func bookFileNames(f BookFile) []string {
	var out []string
	if f.OriginalFilename != "" {
		out = append(out, f.OriginalFilename)
	}
	if f.FilePath != "" {
		out = append(out, filepath.Base(f.FilePath))
	}
	return out
}

// bookOwnFolder derives the book's own folder from its library path.
// FilePath is a FILE (so its directory is the folder) only when it is one of
// the rows' paths or carries an audio extension; otherwise it is the folder
// itself. Deciding "directory" from "some row lies under it" would be wrong
// the other way: with no row under it, Dir(FilePath) climbs to the author
// folder, where sibling copies would then count as own.
func bookOwnFolder(p string, files []BookFile) string {
	if p == "" {
		return ""
	}
	isFile := ownFolderAudioExt[strings.ToLower(filepath.Ext(p))]
	for i := 0; !isFile && i < len(files); i++ {
		isFile = files[i].FilePath == p
	}
	dir := filepath.Clean(p)
	if isFile {
		dir = filepath.Dir(dir)
	}
	// A root or relative "." would contain every row: no own folder.
	if dir == "/" || dir == "." || dir == string(filepath.Separator) {
		return ""
	}
	return dir
}

// ownFolderAudioExt are the extensions that mark Book.FilePath as a file.
var ownFolderAudioExt = map[string]bool{
	".m4b": true, ".m4a": true, ".mp3": true, ".mp4": true, ".aac": true,
	".flac": true, ".ogg": true, ".oga": true, ".opus": true, ".wma": true,
	".wav": true, ".aax": true, ".aa": true, ".aif": true, ".aiff": true,
}
