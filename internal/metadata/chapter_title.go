// file: internal/metadata/chapter_title.go
// version: 1.3.0
// guid: c962e504-746a-454a-996f-1020803a8cab
// last-edited: 2026-09-28

package metadata

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// chapterOnlyTitleRe matches a filename-derived "title" that is nothing but a
// chapter position: "98", "007", "Chapter 12", "Part 3", "Disc 2", "CD1",
// "Track 07", "3 of 12".
var chapterOnlyTitleRe = regexp.MustCompile(`(?i)^(?:(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*)?\d+(?:\s*of\s*\d+)?$`)

// yearLikeTitleRe is a bare four-digit number in the range of a year
// (1000-2099). As a whole filename it is a title ("1984", "2001", "1776"),
// not a chapter position: chapter numbers are zero-padded ("0012") or small.
var yearLikeTitleRe = regexp.MustCompile(`^(?:1\d|20)\d\d$`)

// IsChapterOnlyTitle reports whether a filename-derived title carries no title
// at all, only a chapter number. An empty title counts: a parser that strips a
// leading number before it looks at the rest hands "98.mp3" over as "". A
// year-like four-digit number does NOT count: "Orwell/Classics/1984.mp3" is
// titled "1984".
func IsChapterOnlyTitle(title string) bool {
	t := strings.TrimSpace(title)
	if yearLikeTitleRe.MatchString(t) {
		return false
	}
	return t == "" || chapterOnlyTitleRe.MatchString(t)
}

// IsUnsearchableTitle reports whether a book's title is no title to search a
// catalog by: empty or one of the system's placeholders ("Unknown Title",
// "read by narrator"; authorname.IsPlaceholderTitle), a bare chapter
// position ("Chapter 3", "03"; IsChapterOnlyTitle), or a chapter fragment of
// a shattered book ("06 Chapter 6"; IsLikelyChapterFragment). A catalog
// answers any of these with whatever it ranks first.
//
// It is the ONE predicate the metadata search paths share (the batch
// candidate fetch's query resolver, the bulk metadata fetch, and the search
// ladder's rule that such a title plays no part in the search), so a title
// one path refuses to search cannot be searched verbatim by another.
func IsUnsearchableTitle(title string) bool {
	return authorname.IsPlaceholderTitle(title) || IsChapterOnlyTitle(title) ||
		IsLikelyChapterFragment(title) || IsSectionHeadingTitle(title)
}

// sectionHeadingTitleRe matches a title that is only a section label and its
// position, where the position is a numeral, a roman numeral or a number word:
// "Chapter One", "Part II", "Book 1", "Vol. 2", "Volume 2", "Episode Three".
// IsChapterOnlyTitle already covers the digit forms of chapter/part/disc/
// track; this adds the words and the book/volume labels, for the search paths
// only (IsChapterOnlyTitle also judges filenames and is left as it is).
//
// A roman numeral or word must be set off from the label by a space ("Part
// II"), so a real title that merely starts with a label's letters ("Epic",
// "Chill": ep+ic, ch+ill) is not read as one.
var sectionHeadingTitleRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt|book|bk|volume|vol|episode|ep|section|sect|act|disc|disk|cd|track)\.?` +
	`(?:[\s_\-]*\d+|[\s_\-]+(?:[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|first|second|third|fourth|fifth|last|final))` +
	`(?:\s*of\s*(?:\d+|[a-z]+))?$`)

// sectionNameTitles are the names of a book's front and back matter, which a
// per-file title often is and which name no book.
var sectionNameTitles = map[string]bool{
	"prologue": true, "epilogue": true, "introduction": true, "intro": true, "preface": true,
	"foreword": true, "forward": true, "afterword": true, "interlude": true, "dedication": true,
	"acknowledgments": true, "acknowledgements": true, "contents": true, "table of contents": true,
	"opening credits": true, "closing credits": true, "end credits": true, "credits": true,
	"copyright": true, "about the author": true, "author's note": true, "authors note": true,
}

// IsSectionHeadingTitle reports whether a title is only a section heading --
// a labelled position ("Chapter One", "Part II", "Book 1", "Vol. 2") or the
// name of front or back matter ("Prologue", "Introduction", "Opening
// Credits") -- and so names no book. A year-like number keeps its title:
// "1984" is not matched here (it has no label).
func IsSectionHeadingTitle(title string) bool {
	t := strings.Join(strings.Fields(strings.ToLower(title)), " ")
	if t == "" {
		return false
	}
	return sectionHeadingTitleRe.MatchString(t) || sectionNameTitles[strings.Trim(t, ".:-_ ")]
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
// title, and the folder holding it usually is -- so it returns that folder's
// name, the folder itself (titleDir), and true. The caller takes the AUTHOR
// from the folder above titleDir instead of from the file's parent (pass
// titleDir to the author-from-directory fallback, which reads its argument's
// parent).
//
// A disc or part folder ("Eldest/CD1/03.mp3", "Eldest/Disc 2/07.mp3") names
// no work, so the search moves up one level and the work folder above it
// supplies the title. Only one such level is skipped.
//
// Until 2026-09-28 both filename parsers returned title "98" with author
// "Eldest": the book's own title as its author and a bare number as its title,
// for every chapter of every multi-file book the scanner mistook for a set of
// separate books.
//
// It returns ("", "", false) when title is a real title, when the candidate
// folder is generic (a library root, an import folder), or when the folder
// above it is a library root (the candidate is then an author folder, not a
// work): there is no better title to offer, and the caller keeps what it had.
func ChapterTitleFromDirectory(filePath, title string) (dirTitle, titleDir string, ok bool) {
	if !IsChapterOnlyTitle(title) {
		return "", "", false
	}
	dir := filepath.Dir(filePath)
	name := strings.TrimSpace(filepath.Base(dir))
	if IsChapterOnlyTitle(name) && !genericDirNames[strings.ToLower(name)] {
		// A "CD1" / "Disc 2" / "Part 3" folder: the work is one level up.
		dir = filepath.Dir(dir)
		name = strings.TrimSpace(filepath.Base(dir))
	}
	if genericDirNames[strings.ToLower(name)] || IsChapterOnlyTitle(name) {
		return "", "", false
	}
	// The folder is only a title when it sits UNDER an author folder. In an
	// author-folder layout ("iTunes Media/Audiobooks/Bruce Sentar/01.mp3") the
	// folder is the author and the one above it a library root; taking the
	// author's name as the title would trade recognisable junk ("01") for junk
	// nothing can recognise.
	above := strings.TrimSpace(filepath.Base(filepath.Dir(dir)))
	if genericDirNames[strings.ToLower(above)] || strings.EqualFold(above, "iTunes Media") {
		return "", "", false
	}
	return name, dir, true
}

// SeriesFromTitlePrefix decides whether the "X" of an authorless "X - ... - Y"
// filename is a series. It is, as it always was, EXCEPT when either end is only
// a chapter position: "Eldest - 02" (series "Eldest" stamped on one book per
// chapter) and "02 - Eldest" (series "02"). "The Stormlight Archive - The Way
// of Kings" keeps its series.
func SeriesFromTitlePrefix(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	first := strings.TrimSpace(parts[0])
	last := strings.TrimSpace(parts[len(parts)-1])
	if IsChapterOnlyTitle(first) || IsChapterOnlyTitle(last) {
		return ""
	}
	return first
}
