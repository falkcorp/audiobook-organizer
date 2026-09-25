// file: internal/database/book_own_folder.go
// version: 1.0.0
// guid: 83d7e159-50f7-47e5-8303-8d8212c3bd8e
// last-edited: 2026-09-25

package database

import (
	"path/filepath"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Own-folder rows.
//
// WHY this exists: a book's book_file rows can point at several copies of the
// same content — the iTunes copy, the organized library copy, and chapter
// files left behind by an older layout. Every consumer that sums the rows
// (the ABS mapper, the ABS progress/userdata duration, RecomputeBookAggregates)
// then counts the book once per copy. Measured 2026-09-25: filling the zero
// rows of "Awaken Online: Flame" would have taken it from 17.6 h to 43.7 h.
//
// No existing BookFile field can mark the extra copies out of the sum:
//   - Missing is ignored by the ABS mapper (it sums every row), and
//     ComputeBookRuntime drops a missing row only when it is a content
//     duplicate of a present one. It would also be false: the files exist,
//     and the next rescan's stat flips it back.
//   - SkipScan and VersionID are honoured by neither sum; there is no
//     superseded / role / kind field.
//
// So the rule is structural: when a book's rows span its own folder AND
// somewhere else, only the rows inside its own folder count. The rows are
// never deleted or rewritten — the others are simply not summed.

// OwnFolderSplit partitions a book's rows by whether they lie in the book's
// own folder (the directory of Book.FilePath).
type OwnFolderSplit struct {
	// Dir is the book's own folder, or "" when Book.FilePath gives none.
	Dir string
	// Own are the rows inside Dir (recursively: CD1/, CD2/ subfolders count).
	Own []BookFile
	// Stray are the rows outside Dir. Empty when Dir is "".
	Stray []BookFile
}

// Active reports whether the own-folder filter applies: the rows span the own
// folder and somewhere else, and at least one own row is present on disk.
// Without a present own row Book.FilePath may be the stale one (book.file_path
// is known to lag the active book_file rows), and filtering on it could hide
// the book's real files, so every row is kept instead.
func (s OwnFolderSplit) Active() bool {
	if len(s.Own) == 0 || len(s.Stray) == 0 {
		return false
	}
	for i := range s.Own {
		if !s.Own[i].Missing {
			return true
		}
	}
	return false
}

// Counted returns the rows every sum over this book must use: the own rows
// when Active, otherwise all of files unchanged.
func (s OwnFolderSplit) Counted(files []BookFile) []BookFile {
	if s.Active() {
		return s.Own
	}
	return files
}

// SplitOwnFolderFiles partitions files by the book's own folder. It keeps
// the order of files within each half.
func SplitOwnFolderFiles(book *Book, files []BookFile) OwnFolderSplit {
	var s OwnFolderSplit
	if book == nil {
		return s
	}
	s.Dir = bookOwnFolder(book.FilePath, files)
	if s.Dir == "" {
		return s
	}
	for i := range files {
		if pathutil.IsWithin(files[i].FilePath, s.Dir) {
			s.Own = append(s.Own, files[i])
		} else {
			s.Stray = append(s.Stray, files[i])
		}
	}
	return s
}

// OwnFolderFiles is SplitOwnFolderFiles(book, files).Counted(files): the rows
// a book's duration and size sums must count.
func OwnFolderFiles(book *Book, files []BookFile) []BookFile {
	return SplitOwnFolderFiles(book, files).Counted(files)
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
