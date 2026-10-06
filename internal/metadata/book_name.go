// file: internal/metadata/book_name.go
// version: 1.1.0
// guid: 8eab30cb-5e6e-4bc7-bfba-dfb603b81ef2
// last-edited: 2026-10-05
//
// ParseBookName: the one reader of the title/author/series/number shapes that
// file and folder names pack into a book's title. Scan-time folder parsing
// (parseSeriesTitleSegment) and fetch-time query building
// (metafetch.parseSearchTitle) both call it, so a shape one of them learns the
// other knows too.

package metadata

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
)

// NameEvidence is what ParseBookName may check a segment against. Every field
// is optional: with none, only shapes the text proves on its own are read.
type NameEvidence struct {
	// Authors and Narrators are the book's known people. A leading or
	// trailing dash segment naming one of them (authorjunk.SamePersonName) is
	// a credit, not part of the title.
	Authors   []string
	Narrators []string
	// IsKnownAuthor reports whether a name is a known author (the authority
	// lists, internal/authority). A person-shaped segment it vouches for is a
	// credit. nil means no such evidence.
	IsKnownAuthor func(name string) bool
	// Path is the book's file or folder path. A leading segment equal to an
	// author-shaped ancestor folder ("Joshua Dalzelle/2018/...") is that
	// author's credit.
	Path string
	// FolderName applies the folder conventions (set by the folder parser
	// only): ANY author-shaped trailing segment (looksLikeFolderAuthor) is a
	// credit -- "<title> - <author>" -- and trailing "[tags]" are kept, since
	// a folder's "[Dramatized Adaptation]" names the edition. A search keeps
	// an unknown trailing name, because "Dune - Frank Herbert" and "The
	// Witcher - Blood of Elves" are the same shape.
	FolderName bool
}

// Shapes ParseBookName recognises, recorded in BookName.Shapes.
const (
	ShapeRipTail           = "rip_tail"           // "(Narrator) 64k 12.07.23 {345mb}"
	ShapeTrailingTag       = "trailing_tag"       // "[Fixed]", "[44m4]"
	ShapePlaceholderAuthor = "placeholder_author" // " - Unknown Author"
	ShapeNarratorCredit    = "narrator_credit"    // " - read by X"
	ShapeTrailingAuthor    = "trailing_author"    // "Title - Author" (transposed credit)
	ShapeLeadingAuthor     = "leading_author"     // "Author - Series - Title" (a known person)
	ShapeLeadingFolder     = "leading_folder"     // "Star Wars/Star Wars - Thrawn": repeats an ancestor folder
	ShapeLeadingYear       = "leading_year"       // "2018 - Blueshift"
	ShapeTrackSuffix       = "track_suffix"       // " - 01" after a series slot, "(1 of 3)"
	ShapeSeriesSlot        = "series_slot"        // "Series NN - Title", "Series - NN - Title", "Series Book NN"
	ShapeSubseries         = "subseries"          // "Series Book 05 - Some Trilogy - Title"
)

// BookName is what ParseBookName read out of a title.
type BookName struct {
	// Title is the text left once every recognised shape was removed, still
	// holding a series slot when there was one ("Discworld 24 - The Fifth
	// Elephant" from "Discworld 24 - The Fifth Elephant - 01"). Never "": a
	// strip that would leave no letter is not made.
	Title string
	// Author is a credit read from a leading or trailing segment; Narrator
	// one from a rip tail or a "read by" segment.
	Author   string
	Narrator string
	// Year is a leading release year that was removed ("2018").
	Year string
	// Suffix is a removed track or part suffix ("01", "1 of 3").
	Suffix string
	// Series and Position are a series slot; Name the book's own name behind
	// it ("" when the slot is all there is: "Iron Tyrant Book 03").
	Series   string
	Position string
	Name     string
	// Shapes lists the shapes found, in the order they were read.
	Shapes []string
}

// Has reports whether shape was read.
func (b BookName) Has(shape string) bool {
	for _, s := range b.Shapes {
		if s == shape {
			return true
		}
	}
	return false
}

// SearchName is the book's own name: Name when a slot carried one, else
// Title.
func (b BookName) SearchName() string {
	if b.Name != "" {
		return b.Name
	}
	return b.Title
}

var (
	// bookNameDashRe splits a title into its spaced-dash fields. An unspaced
	// hyphen ("Para-Military", "Catch-22") is part of a word.
	bookNameDashRe = regexp.MustCompile(`\s+[-–—]\s+`)
	// ripTailRe is the unbracketed rip tail some collections append: an
	// optional "(Narrator)", a bitrate, an h.mm.ss running time and an
	// optional "{size}" ("The Urth of the New Sun (Avers) 64k 12.07.23
	// {345mb}"). StripRipJunk reads only the bracketed form.
	ripTailRe = regexp.MustCompile(`(?i)\s*(?:\(([^()]*)\)\s*)?\b\d{2,3}\s*k(?:bps)?\s+\d{1,2}[.;:]\d{2}[.;:]\d{2}\s*(?:\{[^{}]*\})?\s*$`)
	// trailingTagRe is a trailing square-bracket group ("[Fixed]", "[44m4]"):
	// a release tag, never part of a catalog title.
	trailingTagRe = regexp.MustCompile(`\s*\[[^\[\]]*\]\s*$`)
	// countSuffixRe is an explicit "(1 of 3)" / "(Part 2 of 3)" count.
	countSuffixRe = regexp.MustCompile(`(?i)\s*[(\[]\s*(?:(?:part|pt|disc|disk|cd)\.?\s*)?(\d{1,3}\s+of\s+\d{1,4})\s*[)\]]\s*$`)
	// bareNumberSegRe is a field that is only a number ("01", "#3", "2.5").
	bareNumberSegRe = regexp.MustCompile(`^#?(\d{1,3}(?:\.\d+)?)$`)
	// leadingYearSegRe is a field that is only a release year ("2018",
	// "2023b": a second release in a year).
	leadingYearSegRe = regexp.MustCompile(`^(?:19|20)\d{2}[a-z]?$`)
	// slotHeadRe is a field ending in a series position: "Discworld 24",
	// "Para-Military Recruiter 06", "Legend of Drizzt Book 05".
	slotHeadRe = regexp.MustCompile(`(?i)^(.*?\pL.*?)[\s,]+(?:(?:book|bk|vol(?:ume)?|no)\.?\s*)?#?(\d{1,3}(?:\.\d+)?)$`)
	// labelledSlotRe is a lone field ending in a LABELLED position ("Iron
	// Tyrant Book 03"). A bare trailing number alone is not read here:
	// "Apollo 13" and "Fahrenheit 451" are titles.
	labelledSlotRe = regexp.MustCompile(`(?i)^(.*?\pL.*?)[\s,]+(?:book|bk|vol(?:ume)?)\.?\s*#?(\d{1,3}(?:\.\d+)?)$`)
	// slotWordOnlyRe is a series head that is only a slot word ("Book 3").
	slotWordOnlyRe = regexp.MustCompile(`(?i)^(?:book|bk|part|pt|vol(?:ume)?|episode|ep|chapter|disc|disk|track|cd)\.?$`)
	// narratorSegRe is a "read by X" / "narrated by X" field.
	narratorSegRe    = regexp.MustCompile(`(?i)^(?:read|narrated)\s+by\s+(.+)$`)
	bookNameLetterRe = regexp.MustCompile(`\pL`)
)

// setWords mark a field naming a set of books ("Icewind Dale Trilogy"): in
// "Series Book 05 - Icewind Dale Trilogy - Streams of Silver" it is a
// sub-series, and the book's name is the field after it.
var setWords = map[string]bool{
	"trilogy": true, "saga": true, "series": true, "cycle": true, "chronicles": true,
	"quartet": true, "duology": true, "sequence": true,
}

// ParseBookName reads the book's own name, and the credits, release year,
// series slot and track suffix packed around it, out of a title or a file or
// folder name. Each shape is removed only when what remains still holds a
// letter, so Title is never empty. The shapes, in the order they are read:
//
//   - rip details: bracketed groups (StripRipJunk), an unbracketed "(Narrator)
//     64k 12.07.23 {345mb}" tail, trailing "[tags]";
//   - an explicit "(1 of 3)" count;
//   - trailing fields: " - Unknown Author", " - read by X", a known person
//     (the book's author or narrator, an authority-known author, or -- for the
//     folder parser only -- any author-shaped name: "Title - Author");
//   - leading fields: a release year ("2018 - Blueshift"), a known author or
//     an author-shaped ancestor folder's name ("M.R. Forbes - Starship for
//     Rent 02");
//   - a series slot: "Series - NN - Title", "Series NN - Title" (one-word
//     series too, "Discworld 24 - The Fifth Elephant": the spaced dash is the
//     evidence a colon is not -- "Fahrenheit 451: A Novel" is never read),
//     "Series Book NN" alone, and a sub-series field after the slot;
//   - a trailing bare-number field, removed as a track suffix only when a
//     series slot already gave the position ("Discworld 24 - The Fifth
//     Elephant - 01"). Without one it may be the position itself ("The
//     Witcher - 4"), so it stays.
//
// A bare leading number that is not a year ("15 - Harry...", "96 Hours") is
// deliberately not read: it is indistinguishable from a real title by shape.
func ParseBookName(raw string, ev NameEvidence) BookName {
	var b BookName
	t, had := StripRipJunk(strings.TrimSpace(raw))
	if had {
		b.Shapes = append(b.Shapes, ShapeRipTail)
	}
	if m := ripTailRe.FindStringSubmatchIndex(t); m != nil && hasLetterBefore(t, m[0]) {
		if m[2] >= 0 {
			b.Narrator = strings.TrimSpace(t[m[2]:m[3]])
		}
		t = strings.TrimSpace(t[:m[0]])
		b.Shapes = append(b.Shapes, ShapeRipTail)
	}
	for !ev.FolderName {
		loc := trailingTagRe.FindStringIndex(t)
		if loc == nil || !hasLetterBefore(t, loc[0]) {
			break
		}
		t = strings.TrimSpace(t[:loc[0]])
		b.Shapes = append(b.Shapes, ShapeTrailingTag)
	}
	if m := countSuffixRe.FindStringSubmatchIndex(t); m != nil && hasLetterBefore(t, m[0]) {
		b.Suffix = strings.Join(strings.Fields(t[m[2]:m[3]]), " ")
		t = strings.TrimSpace(t[:m[0]])
		b.Shapes = append(b.Shapes, ShapeTrackSuffix)
	}

	f := splitDashFields(t)
	ancestors := ancestorFolders(ev.Path)
	// leadFolder is a first field removed because it repeats an ancestor
	// folder's name (no person evidence): the series, unless a slot names one.
	leadFolder := ""

	// Trailing fields, innermost first: "Title - Author - read by X - Unknown
	// Author" peels all three.
trailing:
	for f.n() > 1 && f.sub(0, f.n()-1).hasLetter() {
		last := f.segs[f.n()-1]
		switch {
		case authorname.IsPlaceholderAuthor(last):
			b.Shapes = append(b.Shapes, ShapePlaceholderAuthor)
		case narratorSegRe.MatchString(last):
			if name := narratorSegRe.FindStringSubmatch(last)[1]; !authorname.IsPlaceholderAuthor(name) && b.Narrator == "" {
				b.Narrator = strings.TrimSpace(name)
			}
			b.Shapes = append(b.Shapes, ShapeNarratorCredit)
		case ev.isKnownPerson(last) || (ev.FolderName && looksLikeFolderAuthor(last)):
			if b.Author == "" && !anyPerson(ev.Narrators, last) {
				b.Author = last
			} else if b.Narrator == "" && anyPerson(ev.Narrators, last) {
				b.Narrator = last
			}
			b.Shapes = append(b.Shapes, ShapeTrailingAuthor)
		default:
			break trailing
		}
		f = f.sub(0, f.n()-1)
	}
	// Leading fields: a year, then an author, in either order ("2002 - Neil
	// Gaiman - American Gods", "Gene Wolfe - 1987 - ...").
leading:
	for range 2 {
		// What would remain must be a title: never a placeholder, and never
		// a genre tagline ("Apocalypse Healer - A LitRPG Adventure" filed
		// under an "Apocalypse Healer" folder names the book, not an author).
		if f.n() < 2 {
			break
		}
		if rest := f.sub(1, f.n()); !rest.hasLetter() || IsUnsearchableTitle(rest.text()) || authorjunk.IsGenreTagline(rest.text()) {
			break
		}
		first := f.segs[0]
		switch {
		case b.Year == "" && leadingYearSegRe.MatchString(first):
			b.Year = first
			b.Shapes = append(b.Shapes, ShapeLeadingYear)
		case b.Author == "" && (anyPerson(ev.Authors, first) || ev.knownAuthor(first)):
			// Person evidence: the book's own author or an authority-known
			// author.
			b.Author = first
			b.Shapes = append(b.Shapes, ShapeLeadingAuthor)
		case b.Author == "" && leadFolder == "" && ancestorFolder(ancestors, first) && !bareNumberSegRe.MatchString(f.segs[1]):
			// A first field that only repeats an ancestor folder's name is
			// NOT person evidence: a top-level series folder has the same
			// shape as an author folder ("Star Wars/Star Wars - Thrawn",
			// "Doctor Who/Doctor Who - The Pescatons"). It is removed from
			// the title and kept as the series when no slot names another
			// (below) -- never an author. A first field followed by a bare
			// number is that series' slot ("Reclaiming Honor/Reclaiming
			// Honor - 02 - Claimed by Honor"), read by readSeriesSlot.
			leadFolder = first
			b.Shapes = append(b.Shapes, ShapeLeadingFolder)
		default:
			break leading
		}
		f = f.sub(1, f.n())
	}

	b.readSeriesSlot(f)
	if b.Position != "" && f.n() > 2 {
		// A trailing bare number after a slot that already gave the
		// position is a track or part suffix.
		if m := bareNumberSegRe.FindStringSubmatch(f.segs[f.n()-1]); m != nil && f.sub(0, f.n()-1).hasLetter() {
			b.Suffix = m[1]
			b.Shapes = append(b.Shapes, ShapeTrackSuffix)
			f = f.sub(0, f.n()-1)
			b.Series, b.Position, b.Name = "", "", ""
			b.Shapes = dropShape(dropShape(b.Shapes, ShapeSeriesSlot), ShapeSubseries)
			b.readSeriesSlot(f)
		}
	}
	if leadFolder != "" && b.Series == "" {
		b.Series = leadFolder
	}
	b.Title = f.text()
	if b.Title == "" {
		b.Title = strings.TrimSpace(raw)
	}
	return b
}

// readSeriesSlot reads a series slot out of f (ParseBookName). A name behind
// the slot that is a genre tagline ("A Dungeon Core Experience", "A Xianxia
// LitRPG"; authorjunk.IsGenreTagline) is no book name: the slot is recorded
// with no Name.
func (b *BookName) readSeriesSlot(f dashFields) {
	var name dashFields
	switch {
	case f.n() >= 3 && bareNumberSegRe.MatchString(f.segs[1]) && bookNameLetterRe.MatchString(f.segs[0]) &&
		!slotWordOnlyRe.MatchString(f.segs[0]) && f.sub(2, f.n()).hasLetter():
		// "Series - NN - Title".
		b.Series, b.Position = f.segs[0], bareNumberSegRe.FindStringSubmatch(f.segs[1])[1]
		name = f.sub(2, f.n())
	case f.n() >= 2 && f.sub(1, f.n()).hasLetter():
		// "Series NN - Title", "Series Book NN - Title".
		m := slotHeadRe.FindStringSubmatch(f.segs[0])
		if m == nil || slotWordOnlyRe.MatchString(strings.TrimSpace(m[1])) {
			return
		}
		b.Series, b.Position = strings.Trim(strings.TrimSpace(m[1]), " ,"), m[2]
		name = f.sub(1, f.n())
	case f.n() == 1:
		m := labelledSlotRe.FindStringSubmatch(f.segs[0])
		if m == nil || slotWordOnlyRe.MatchString(strings.TrimSpace(m[1])) {
			return
		}
		b.Series, b.Position = strings.Trim(strings.TrimSpace(m[1]), " ,"), m[2]
	default:
		return
	}
	b.Shapes = append(b.Shapes, ShapeSeriesSlot)
	// "Icewind Dale Trilogy - Streams of Silver": a set-naming field before
	// the last is a sub-series; the book is the last field.
	if name.n() >= 2 {
		sub := true
		for _, s := range name.segs[:name.n()-1] {
			if !namesASet(s) {
				sub = false
				break
			}
		}
		if sub && bookNameLetterRe.MatchString(name.segs[name.n()-1]) {
			name = name.sub(name.n()-1, name.n())
			b.Shapes = append(b.Shapes, ShapeSubseries)
		}
	}
	if n := name.text(); n != "" && !authorjunk.IsGenreTagline(n) {
		b.Name = n
	}
}

// dashFields is a title split at its spaced dashes, keeping where each field
// sits in the text, so what is kept is a slice of the original -- its own
// dashes ("–", "—") and spacing -- never a re-join.
type dashFields struct {
	s     string
	segs  []string
	spans [][2]int
}

// splitDashFields splits s at its spaced dashes, dropping empty fields.
func splitDashFields(s string) dashFields {
	f := dashFields{s: s}
	start := 0
	add := func(lo, hi int) {
		seg := s[lo:hi]
		trimmed := strings.TrimSpace(seg)
		if trimmed == "" {
			return
		}
		off := lo + strings.Index(seg, trimmed)
		f.segs = append(f.segs, trimmed)
		f.spans = append(f.spans, [2]int{off, off + len(trimmed)})
	}
	for _, loc := range bookNameDashRe.FindAllStringIndex(s, -1) {
		add(start, loc[0])
		start = loc[1]
	}
	add(start, len(s))
	return f
}

func (f dashFields) n() int { return len(f.segs) }

// sub is fields [i, j).
func (f dashFields) sub(i, j int) dashFields {
	return dashFields{s: f.s, segs: f.segs[i:j], spans: f.spans[i:j]}
}

// text is the original text from the first field to the last.
func (f dashFields) text() string {
	if f.n() == 0 {
		return ""
	}
	return f.s[f.spans[0][0]:f.spans[f.n()-1][1]]
}

func (f dashFields) hasLetter() bool {
	for _, s := range f.segs {
		if bookNameLetterRe.MatchString(s) {
			return true
		}
	}
	return false
}

func hasLetterBefore(s string, i int) bool { return bookNameLetterRe.MatchString(s[:i]) }

func dropShape(shapes []string, shape string) []string {
	out := shapes[:0:0]
	for _, s := range shapes {
		if s != shape {
			out = append(out, s)
		}
	}
	return out
}

// namesASet reports whether a field names a set of books ("Icewind Dale
// Trilogy", "The Expanse Series").
func namesASet(f string) bool {
	for _, w := range strings.Fields(strings.ToLower(f)) {
		if setWords[strings.Trim(w, ".,:;()")] {
			return true
		}
	}
	return false
}

// anyPerson reports whether name is one of people (authorjunk.SamePersonName).
func anyPerson(people []string, name string) bool {
	for _, p := range people {
		if strings.TrimSpace(p) != "" && !authorname.IsPlaceholderAuthor(p) && authorjunk.SamePersonName(p, name) {
			return true
		}
	}
	return false
}

// knownAuthor reports whether name is a person-shaped name the authority
// lists know as an author.
func (ev NameEvidence) knownAuthor(name string) bool {
	return ev.IsKnownAuthor != nil && looksLikeAuthorSegment(name) && !strings.ContainsAny(name, "0123456789") && ev.IsKnownAuthor(name)
}

// isKnownPerson reports whether name is one of the book's people or a known
// author.
func (ev NameEvidence) isKnownPerson(name string) bool {
	return anyPerson(ev.Authors, name) || anyPerson(ev.Narrators, name) || ev.knownAuthor(name)
}

// ancestorFolders returns the folder names of path's ancestors, innermost
// first, with the library roots splitPathSegments skips removed. The last
// element of path is dropped: it is the file (or the book's own folder),
// whose name is the title being parsed.
func ancestorFolders(path string) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	segs := splitPathSegments(filepath.Dir(filepath.ToSlash(path)))
	out := make([]string, 0, len(segs))
	for i := len(segs) - 1; i >= 0; i-- {
		out = append(out, segs[i])
	}
	return out
}

// ancestorFolder reports whether name equals one of the ancestor folders
// (authorjunk.SamePersonName folding: case, punctuation, "Last, First").
// Equality alone says only that the folder names the same thing -- an author
// or a series; ParseBookName never reads it as a person.
func ancestorFolder(ancestors []string, name string) bool {
	for _, a := range ancestors {
		if authorjunk.SamePersonName(a, name) {
			return true
		}
	}
	return false
}
