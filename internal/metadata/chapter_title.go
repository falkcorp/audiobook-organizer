// file: internal/metadata/chapter_title.go
// version: 1.9.0
// guid: c962e504-746a-454a-996f-1020803a8cab
// last-edited: 2026-09-30

package metadata

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

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
// catalog by, whatever book it is on: empty or one of the system's
// placeholders ("Unknown Title", "read by narrator";
// authorname.IsPlaceholderTitle), a bare chapter position ("Chapter 3", "03";
// IsChapterOnlyTitle), a chapter fragment of a shattered book ("06 Chapter
// 6", "Elantris_copy179"; IsLikelyChapterFragment), or a labelled position
// in digits ("Book 1",
// "Vol. 2", "Episode 3"). A catalog answers any of these with whatever it
// ranks first.
//
// A title ending in a bare part token ("The Sunrise Lands 1", "Sealed to the
// Flame E"; SiblingPartStem), a counted part ("Cobra 100 of 151";
// IsCountedPartTitle) and rip details (StripRipJunk) are NOT matched here
// either: "Apollo 13", "Plan B" and "Golden Son (Part 1 of 2)" are books, and
// only the book's folder and duration can tell (NeedsFolderEvidence,
// metabatch's titleJudge).
//
// A heading in words or roman numerals ("Book Two", "Part II") and the name
// of front or back matter ("Prologue", "Introduction") are NOT matched here:
// each is also some real book's title ("Act One", "Epilogue", "Interlude").
// IsSectionHeadingTitle names those, and a caller holding the book's files
// decides with their corroboration (metabatch.ResolveCandidateSearchQuery).
//
// It is the one unconditional predicate the metadata search paths share (the
// batch candidate fetch's query resolver, the bulk metadata fetch, and the
// search ladder's rule that such a title plays no part in the search), so a
// title one path refuses to search cannot be searched verbatim by another.
func IsUnsearchableTitle(title string) bool {
	return authorname.IsPlaceholderTitle(title) || IsChapterOnlyTitle(title) ||
		IsLikelyChapterFragment(title) || sectionDigitTitleRe.MatchString(normHeading(title))
}

// MayBeUnsearchableTitle is IsUnsearchableTitle or IsSectionHeadingTitle: a
// title a caller without the book's files hands to one that has them
// (metabatch.ResolveCandidateSearchQuery) rather than searching it as-is.
//
// The folder-evidence shapes (NeedsFolderEvidence) are deliberately NOT
// here: "Apollo 13" and "Henry V" are almost always searched by their own
// title, so a bulk fetch still probes skip_cached for them with that title's
// identity and only its worker asks the resolver.
func MayBeUnsearchableTitle(title string) bool {
	return IsUnsearchableTitle(title) || IsSectionHeadingTitle(title)
}

// NeedsFolderEvidence reports whether title has a shape whose verdict
// depends on the book's folder and duration: a counted part
// (IsCountedPartTitle), a bare trailing part token (SiblingPartStem) or rip
// details (StripRipJunk). A caller that would search the title as-is hands
// such a title to metabatch.ResolveCandidateSearchQuery first.
func NeedsFolderEvidence(title string) bool {
	if IsCountedPartTitle(title) {
		return true
	}
	if _, _, ok := SiblingPartStem(title); ok {
		return true
	}
	_, had := StripRipJunk(title)
	return had
}

// sectionLabels are the labels a section position is written after.
const sectionLabels = `(?:chapter|chap|ch|part|pt|book|bk|volume|vol|episode|ep|section|sect|act|disc|disk|cd|track)`

// sectionDigitTitleRe is a label and a position in digits ("Book 1", "Vol. 2",
// "Episode 3 of 12"): no book is titled that, so it is unsearchable anywhere.
var sectionDigitTitleRe = regexp.MustCompile(`^` + sectionLabels + `\.?[\s_\-]*\d+(?:\s*of\s*\d+)?$`)

// sectionWordTitleRe is a label and a position in words or a VALID roman
// numeral ("Chapter One", "Part II", "Book X", "Volume IV"). The numeral must
// be well formed and set off by a space, so "Book Ill" (I-L-L), "CD Civil",
// "Epic" and "Chill" are not read as one.
//
// The position is captured: every part of the roman form is optional, so an
// empty capture ("Act of Will" read as act + "" + "of will") is refused by
// IsSectionHeadingTitle.
var sectionWordTitleRe = regexp.MustCompile(`^` + sectionLabels + `\.?[\s_\-]+` +
	`(m{0,3}(?:cm|cd|d?c{0,3})(?:xc|xl|l?x{0,3})(?:ix|iv|v?i{0,3})|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|first|second|third|fourth|fifth|last|final)` +
	`(?:\s*of\s*(?:\d+|[a-z]+))?$`)

// normHeading lowercases a title and collapses its whitespace.
func normHeading(title string) string {
	return strings.Join(strings.Fields(strings.ToLower(title)), " ")
}

// sectionNameTitles are the names of a book's front and back matter, which a
// per-file title often is.
var sectionNameTitles = map[string]bool{
	"prologue": true, "epilogue": true, "introduction": true, "intro": true, "preface": true,
	"foreword": true, "forward": true, "afterword": true, "interlude": true, "dedication": true,
	"acknowledgments": true, "acknowledgements": true, "contents": true, "table of contents": true,
	"opening credits": true, "closing credits": true, "end credits": true, "credits": true,
	"copyright": true, "about the author": true, "author's note": true, "authors note": true,
}

// IsSectionHeadingTitle reports whether a title reads as a section heading
// that IsUnsearchableTitle does not already refuse: a labelled position in
// words or a valid roman numeral ("Chapter One", "Part II", "Book X") or the
// name of front or back matter ("Prologue", "Introduction", "Opening
// Credits").
//
// Each is ALSO a real book's title ("Act One", "Epilogue", "Book Two",
// "Interlude"), so this is half the evidence only: a caller treats the title
// as unsearchable when the book's files corroborate that it names a part of
// something (metabatch: more than one present file, or the title is a
// file's own tag title on a numbered track). A year-like number ("1984") has
// no label and is never matched.
func IsSectionHeadingTitle(title string) bool {
	t := normHeading(title)
	if t == "" {
		return false
	}
	if m := sectionWordTitleRe.FindStringSubmatch(t); m != nil && m[1] != "" {
		return true
	}
	return sectionNameTitles[strings.Trim(t, ".:-_ ")]
}

// Generic folder names (library and import roots) are authorname.IsGenericFolder,
// shared with the author-from-directory fallback.

// placeholderDirNames are folder names someone left as they were or gave a
// set of books: they name no work, but unlike authorname.IsGenericFolder they
// are not a library root, so a work folder under one ("Complete
// Collection/The Martian/01.mp3") still counts as a title.
var placeholderDirNames = map[string]bool{"new folder": true, "collection": true, "complete collection": true}

// newFolderRe: the file manager's default name, numbered ("New Folder (2)",
// "New Folder 2").
var newFolderRe = regexp.MustCompile(`^new folder(?: \(?\d+\)?)?$`)

// IsGenericDirName reports whether a folder name says nothing about the
// work: a library or import root, a generic "Books" folder
// (authorname.IsGenericFolder), or a placeholder ("New Folder (2)",
// "Complete Collection"). The junk-title fixer refuses such a name as a
// proposed title and does not count it as evidence.
func IsGenericDirName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return authorname.IsGenericFolder(n) || placeholderDirNames[n] || newFolderRe.MatchString(n)
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
	return workFolder(filePath, false)
}

// workFolder is ChapterTitleFromDirectory's folder walk. placeholderAuthorAbove
// decides one layout: a folder under a placeholder author folder
// ("Unknown Author/Mort/01.mp3", the organizer's own layout for a book with no
// author). ChapterTitleFromDirectory treats "Unknown Author" as a library root
// there and offers nothing, which the scanner's title recovery has always
// done; WorkFolderTitle, which only READS the folder as evidence of the work,
// passes true and takes "Mort".
func workFolder(filePath string, placeholderAuthorAbove bool) (dirTitle, titleDir string, ok bool) {
	dir := filepath.Dir(filePath)
	name := strings.TrimSpace(filepath.Base(dir))
	if IsChapterOnlyTitle(name) && !IsGenericDirName(name) {
		// A "CD1" / "Disc 2" / "Part 3" folder: the work is one level up.
		dir = filepath.Dir(dir)
		name = strings.TrimSpace(filepath.Base(dir))
	}
	if IsGenericDirName(name) || IsChapterOnlyTitle(name) {
		return "", "", false
	}
	// The folder is only a title when it sits UNDER an author folder. In an
	// author-folder layout ("iTunes Media/Audiobooks/Bruce Sentar/01.mp3") the
	// folder is the author and the one above it a library root; taking the
	// author's name as the title would trade recognisable junk ("01") for junk
	// nothing can recognise.
	above := strings.TrimSpace(filepath.Base(filepath.Dir(dir)))
	if placeholderAuthorAbove && authorname.IsPlaceholderAuthor(above) {
		return name, dir, true
	}
	if authorname.IsGenericFolder(above) || strings.EqualFold(above, "iTunes Media") {
		return "", "", false
	}
	return name, dir, true
}

// IsFileExt tells a real extension (".m4b", ".mp3") from a folder name that
// merely contains a dot ("Book 1.5" -> ".5").
func IsFileExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 5 {
		return false
	}
	return strings.IndexFunc(ext[1:], unicode.IsLetter) >= 0
}

// WorkFolderTitle returns the name of the work folder holding path, or ok
// false when there is none (ChapterTitleFromDirectory's rules: a library
// root, an import folder or an author folder is no work folder). path may be
// a file -- one chapter of a multi-file book, a single-file book -- or a
// directory, which is what a multi-file book's own FilePath is.
//
// ChapterTitleFromDirectory reads the last path element as a FILE name, so a
// directory is given a stand-in child first: ".../Terry Pratchett/Mort" is
// asked about as ".../Terry Pratchett/Mort/_", and answers "Mort", not
// "Terry Pratchett" (or nothing, when the parent is an author folder under a
// library root). A path whose extension is not a real one (IsFileExt: "Book
// 1.5") is a directory too. A work folder under a placeholder author folder
// (".../Unknown Author/Mort", the organizer's layout for a book with no
// author) is a work folder: see workFolder.
func WorkFolderTitle(path string) (string, bool) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", false
	}
	if !IsFileExt(filepath.Ext(p)) {
		p = filepath.Join(p, "_")
	}
	t, _, ok := workFolder(p, true)
	t = strings.TrimSpace(t)
	return t, ok && t != ""
}

// ParentIsTitleFolder reports whether the folder holding filePath is named
// exactly the book's title -- the organizer's "<author>/<title>/<file>" layout,
// and any library shelved the same way. The author-from-directory fallback
// reads the IMMEDIATE parent, which here is the title, so a person-shaped title
// ("Good Omens", "Pratchett 036" is not, "The Martian" is refused as a work)
// was filed as the author. Callers pass the folder itself as the probe instead,
// so the fallback reads the folder ABOVE it
// (todo.d/20260825-directory-fallback-reads-title-as-author.md). It only fires
// on an exact, case-insensitive name match, so an author folder holding
// "<author>/<title>.ext" is untouched. The folder above is read through
// authorname.ExtractAuthorAboveTitle, which refuses a series folder that
// prefixes the title and (with the shared gate) a genre folder.
//
// LIMIT: "/lib/Stephen King/Stephen King.mp3" -- a file named for its author
// folder -- reads "lib" and gets no author. It has exactly the shape of
// "/lib/Audiobooks/Good Omens/Good Omens.mp3", where reading the parent would
// credit the title, so it is not special-cased.
func ParentIsTitleFolder(filePath, title string) bool {
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(filepath.Base(filepath.Dir(filePath))), title)
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
