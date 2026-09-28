// file: internal/metadata/chapter_title.go
// version: 1.0.0
// guid: c962e504-746a-454a-996f-1020803a8cab
// last-edited: 2026-09-28

package metadata

import (
	"path/filepath"
	"regexp"
	"strings"
)

// chapterOnlyTitleRe matches a filename-derived "title" that is nothing but a
// chapter position: "98", "007", "Chapter 12", "Part 3", "Disc 2", "CD1",
// "Track 07", "3 of 12".
var chapterOnlyTitleRe = regexp.MustCompile(`(?i)^(?:(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*)?\d+(?:\s*of\s*\d+)?$`)

// IsChapterOnlyTitle reports whether a filename-derived title carries no title
// at all, only a chapter number. An empty title counts: the scanner's parser
// strips a leading number before it looks at the rest, so "98.mp3" reaches it
// as "".
func IsChapterOnlyTitle(title string) bool {
	t := strings.TrimSpace(title)
	return t == "" || chapterOnlyTitleRe.MatchString(t)
}

// genericDirNames are folder names that say nothing about the work: a disc or
// part folder, or a library/import root.
var genericDirNames = map[string]bool{
	"": true, ".": true, "/": true,
	"books": true, "audiobooks": true, "audiobook": true, "downloads": true, "import": true,
	"imports": true, "incoming": true, "library": true, "media": true, "audio": true,
	"unknown author": true, "unknown": true,
}

// ChapterTitleFromDirectory answers the filename fallback's worst case: a file
// named only by its chapter number ("Eldest/98.mp3"). The number is not a
// title, and the parent folder usually is -- so it returns the parent
// directory's name and true, and the caller takes the AUTHOR from the
// grandparent instead of the parent (see AuthorProbePath).
//
// Until 2026-09-28 both filename parsers returned title "98" with author
// "Eldest": the book's own title as its author and a bare number as its title,
// for every chapter of every multi-file book the scanner mistook for a set of
// separate books.
//
// It returns ("", false) when title is a real title, when the parent is itself
// generic (a "CD1"/"Disc 2" folder, a library root), or when the grandparent is
// a library root (the parent is then an author folder, not a work): there is no
// better title to offer, and the caller keeps what it had.
func ChapterTitleFromDirectory(filePath, title string) (string, bool) {
	if !IsChapterOnlyTitle(title) {
		return "", false
	}
	parentDir := filepath.Dir(filePath)
	parent := strings.TrimSpace(filepath.Base(parentDir))
	if genericDirNames[strings.ToLower(parent)] || IsChapterOnlyTitle(parent) {
		return "", false
	}
	// The folder is only a title when it sits UNDER an author folder. In an
	// author-folder layout ("iTunes Media/Audiobooks/Bruce Sentar/01.mp3") the
	// parent is the author and the grandparent a library root; taking the
	// author's name as the title would trade recognisable junk ("01") for junk
	// nothing can recognise.
	grand := strings.TrimSpace(filepath.Base(filepath.Dir(parentDir)))
	if genericDirNames[strings.ToLower(grand)] || strings.EqualFold(grand, "iTunes Media") {
		return "", false
	}
	return parent, true
}

// AuthorProbePath is the path whose PARENT directory an author-from-directory
// fallback should read. Normally that is filePath itself (the author is the
// folder the file sits in). When the title was taken from that folder by
// ChapterTitleFromDirectory, the folder is the title, so the probe moves up one
// level and the author comes from the grandparent: "Christopher
// Paolini/Eldest/98.mp3" gives title "Eldest", author "Christopher Paolini".
func AuthorProbePath(filePath string, titleFromDir bool) string {
	if titleFromDir {
		return filepath.Dir(filePath)
	}
	return filePath
}
