// file: internal/plugins/maintenance/junk_title_derive.go
// version: 2.2.3
// guid: 6fb9129d-4c59-4097-979e-6cfe61bc6894
// last-edited: 2026-10-25

package maintenance

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// junkTitles are stored Book.Title values that are demonstrably NOT a book title.
// They come from two distinct accidents, both confirmed against production:
//
//   - "read by narrator" (1,595 books) — the on-disk convention is
//     "<Title> - <Author> - read by <narrator>.<ext>" and the importer kept the
//     trailing credit instead of the leading title. The organizer then *wrote*
//     folders named after it, so the directory is poisoned too.
//   - "intro" / "opening credits" / "big finish ident" (466 books) — track 1's
//     ID3 title tag was promoted to the book title on a multi-file book.
//
// Matched case-insensitively after trimming.
var junkTitles = map[string]struct{}{
	"read by narrator":  {},
	"intro":             {},
	"opening credits":   {},
	"big finish ident":  {},
	"opening credits 1": {},
}

// IsJunkTitle reports whether a stored title is not a title. It is the
// Repairs-lane classifier (metadata.ClassifyJunkTitle) — chapter positions,
// narrator credits, track tags, placeholders, bare roman numerals, junk
// prefixes — with junkTitles kept as a floor. The empty title is not junk
// here: the derivation below rejects short candidates on its own.
func IsJunkTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	if _, ok := junkTitles[strings.ToLower(t)]; ok {
		return true
	}
	return metadata.ClassifyJunkTitle(t) != metadata.JunkNone
}

// audioExts are stripped from a filename before it is read as a title.
var audioExts = map[string]struct{}{
	".mp3": {}, ".m4a": {}, ".m4b": {}, ".mp4": {}, ".ogg": {},
	".opus": {}, ".flac": {}, ".wav": {}, ".aac": {}, ".wma": {},
}

// stripAudioExt removes a known audio extension, and nothing else — an unknown
// suffix is left alone rather than guessed at, so "Vol. 1.5" keeps its ".5".
func stripAudioExt(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if _, ok := audioExts[ext]; ok {
		return name[:len(name)-len(ext)]
	}
	return name
}

// authorRecognisers tells titleFromFilename which trailing " - <segment>"
// is a person's credit. A recogniser that is nil recognises nobody.
type authorRecognisers struct {
	// own recognises the book's own credited people: the stored author, a
	// linked author or a narrator. Their names go behind a "read by" credit;
	// without one they go only when independent says so.
	own func(string) bool
	// independent recognises a name the book's own file name is not the only
	// evidence for: an author with books other than this one, or a narrator who
	// narrates another book (the narrator field is NOT out of a title
	// segment's reach: the AI filename parse and the artist-tag fallback fill
	// it from the very file name and tags the stem came from, so only a name
	// the library holds elsewhere counts).
	// Without a credit the stem is a bare "<Title> - <Segment>" and the scanner
	// reads such a stem as a title and an author, so a stored author with no
	// other book is that very segment read back, and stripping it is circular:
	// it cuts a subtitle off the title and nothing checks what is left. A nil
	// independent recognises nobody.
	independent func(string) bool
	// library recognises any author of the library. It is honoured only behind
	// a " - read by " credit, for the one segment that stands where the
	// convention puts the author, and never for a name that is also a real
	// title (the caller's recogniser must say so): the library holds author
	// rows that are really titles, and stripping one of those deletes title
	// text (🔴 above).
	library func(string) bool
}

// filenameHead is a file name's stem without its extension and without a
// trailing " - read by <narrator>" credit, and whether it had one. A stem that
// is only an author credit ("read by X") has no title: "".
func filenameHead(filename string) (head string, credited bool) {
	stem := strings.TrimSpace(stripAudioExt(filename))
	if stem == "" || readByLeadRe.MatchString(stem) {
		return "", false
	}
	// The index comes from the stem itself: lower-casing a copy first can
	// change its byte length (U+0130, U+212A) and misalign the cut.
	if locs := readBySepRe.FindAllStringIndex(stem, -1); len(locs) > 0 {
		return strings.TrimSpace(stem[:locs[len(locs)-1][0]]), true
	}
	return stem, false
}

// titleFromFilename recovers the title from a file named by the
// "<Title> - <Author> - read by <narrator>" convention.
//
// 🔑 Only a segment that is provably an author is stripped, and that is the
// whole point. Naively dropping the last " - " segment after the credit would
// corrupt any title that legitimately contains one:
//
//	"Vessmir Hollow - The Quorn Warden Part 2 - read by narrator.mp3"
//	  naive → "Vessmir Hollow - The Quorn Warden"   ✗ eats real title text
//	  ours  → "Vessmir Hollow - The Quorn Warden Part 2"  ✓ (no author match, so
//	                                                       only the credit goes)
//
// When author is known and present, both it and the credit are removed:
//
//	"Pellmoor - 3 - Dolmen Hush - Tamsin Verrow - read by narrator.m4b"
//	  → "Pellmoor - 3 - Dolmen Hush"
//
// A filename carrying no credit at all is used whole, but for a trailing
// placeholder author and a name with evidence of its own. rec (the zero value
// recognises nobody) names the people beyond the stored author that a trailing
// segment may be. A trailing " - <segment>" goes when it is
//
//   - a placeholder author ("Unknown Author", "Unknown"): never title text, so
//     with or without a credit ("Marble Gate - Unknown Author.mp3" is "Marble
//     Gate": the stored author was junk, so no author match was possible);
//   - the stored author, unless that author is junk (a chapter label or an
//     essay name the parser put in the author field is title text in the file
//     name, and stripping it left a plausible-looking fragment), or one of the
//     book's own people (rec.own): behind a "read by" credit, or, with no
//     credit in the stem, only when rec.independent vouches for the name. The
//     stored author is often parsed from this very file name, so with no
//     credit and no evidence of its own the strip is circular;
//   - behind a "read by" credit, and for the first author segment only, a name
//     of the library (rec.library): the convention is one author before the
//     credit, so a second name-shaped segment is title text.
//
// A segment that opens with a position word ("Volume II", "Part 3") is title
// text even when a scan stored it as the author (a reviewer accepted such a
// title).
func titleFromFilename(filename, author string, rec authorRecognisers) string {
	head, credited := filenameHead(filename)
	if head == "" {
		return ""
	}
	a := strings.TrimSpace(author)
	if junkStoredAuthor(a) {
		a = ""
	}
	authorStripped := false
	// vouched: the stem itself credits the author (a " - read by " suffix), or
	// the name has evidence beyond the stem.
	vouched := func(seg string) bool {
		return credited || (rec.independent != nil && rec.independent(seg))
	}
	// A trailing author segment, and the placeholder one an organizer pass
	// may have written before it ("Title - Author - Unknown Author").
	for range 3 {
		i := strings.LastIndex(head, " - ")
		if i < 0 {
			break
		}
		seg := strings.TrimSpace(head[i+3:])
		if seg == "" || positionLeadRe.MatchString(seg) {
			break
		}
		switch {
		case authorname.IsPlaceholderAuthor(seg):
		case a != "" && strings.EqualFold(seg, a) && vouched(seg):
			authorStripped = true
		case rec.own != nil && rec.own(seg) && vouched(seg):
			authorStripped = true
		case credited && !authorStripped && rec.library != nil && rec.library(seg):
			authorStripped = true
		default:
			return head
		}
		head = strings.TrimSpace(head[:i])
	}
	return head
}

// majorityDirOf returns the directory holding the most of these files. Books
// whose files span directories are common enough that picking the first would
// be arbitrary.
func majorityDirOf(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	counts := map[string]int{}
	for _, p := range paths {
		if d := filepath.Dir(p); d != "" && d != "." {
			counts[d]++
		}
	}
	best, bestN := "", -1
	keys := make([]string, 0, len(counts))
	for d := range counts {
		keys = append(keys, d)
	}
	sort.Strings(keys) // deterministic when two dirs tie
	for _, d := range keys {
		if counts[d] > bestN {
			best, bestN = d, counts[d]
		}
	}
	return best
}

// deriveJunkTitleReplacement works out the real title for a book whose stored
// title is junk, and reports which evidence it used.
//
// Candidates are tried in order of trustworthiness for the shape observed:
//
//  1. Multi-file book → the deepest folder segment. These are the ID3-residue
//     cases, and the organizer already named the folder correctly:
//     ".../Big Finish Productions/Vessmir Hollow/Vessmir Hollow - The Quorn Warden Part 2"
//  2. The filename, parsed for the "read by" convention. This is the only
//     evidence for the single-file cases, whose folder was named from the same
//     bad title and is therefore useless.
//  3. Walking up the directory, first segment that is not itself junk — a last
//     resort for ".../Brindlewick/read by narrator/..." shapes.
//
// Returns ok=false when nothing trustworthy is found; the caller must then leave
// the book alone rather than invent a title.
//
// rec recognises the names beyond the stored author that titleFromFilename
// strips from a filename's tail (the zero value recognises none).
func deriveJunkTitleReplacement(storedTitle, author string, filePaths []string, rec authorRecognisers) (title, method string, ok bool) {
	stored := strings.TrimSpace(storedTitle)

	// corroborated: some path element already matches the stored title, which
	// means the stored value is what the library actually says. Walking further
	// up would then "repair" a correct title into its parent folder — how an
	// early version of this turned "Some Book" into its author's name.
	corroborated := false

	accept := func(cand, how string) (string, string, bool) {
		c := strings.TrimSpace(cand)
		// Never swap one junk title for another, and never write an empty or
		// single-character title.
		if len(c) < 2 || IsJunkTitle(c) {
			return "", "", false
		}
		if strings.EqualFold(c, stored) {
			corroborated = true
			return "", "", false
		}
		return c, how, true
	}

	dir := majorityDirOf(filePaths)

	// 1. Multi-file: trust the folder.
	if len(filePaths) >= 2 && dir != "" {
		if t, m, good := accept(filepath.Base(dir), "folder"); good {
			return t, m, true
		}
	}

	// 2. The filename and its "read by" convention.
	if len(filePaths) > 0 {
		if t, m, good := accept(titleFromFilename(filepath.Base(filePaths[0]), author, rec), "filename"); good {
			return t, m, true
		}
	}

	// 3. Walk up for the first non-junk segment — but only a BOUNDED distance,
	//    and only if nothing so far corroborated the stored title.
	//
	//    🔴 Unbounded, this is actively destructive: it climbs past the book, past
	//    the author, and happily returns a library root. It produced "lib" from
	//    "/lib/A/intro.mp3" and "Author" from "/lib/Author/Some Book/Some Book.m4b"
	//    before this bound existed. The real shape needing rescue is only ever one
	//    level up — ".../Brindlewick/read by narrator/<file>" — so one level is all
	//    it gets.
	//    The hop is also only taken to ESCAPE A POISONED FOLDER. If the folder was
	//    rejected for any other reason — too short, corroborating the stored title —
	//    the layout is not the shape this rescue understands, and climbing anyway
	//    just returns whatever happens to sit above it (that is how "/lib/A/intro.mp3"
	//    yielded "lib").
	//    "Poisoned" means the EXACT junkTitles set, the folder names the organizer
	//    itself wrote from a bad title — not the broad classifier: a folder that
	//    merely looks like a chapter ("Chapter 1") or a credit is not the
	//    organizer's doing, and climbing out of it lands on a library root.
	const maxAncestorHops = 1
	_, poisoned := junkTitles[strings.ToLower(strings.TrimSpace(filepath.Base(dir)))]
	if !corroborated && dir != "" && poisoned {
		d := dir
		for hop := 0; hop <= maxAncestorHops; hop++ {
			if d == "" || d == "/" || d == "." {
				break
			}
			if t, m, good := accept(filepath.Base(d), "ancestor-folder"); good {
				return t, m, true
			}
			if corroborated {
				break
			}
			d = filepath.Dir(d)
		}
	}

	return "", "", false
}

// junkTitleOwnerManual reports whether any of path, title or series marks the
// book as owner-manual (Doctor Who / Big Finish / Torchwood). The title is
// checked because "Big Finish Ident" is itself one of the junk titles this op
// targets, and a book carrying it is Big Finish content.
func junkTitleOwnerManual(path, title, series string) bool {
	return applygate.IsOwnerManualOnly(path, series) || applygate.IsOwnerManualOnly(title, "")
}

// splitCreditNames splits a denormalized credit string ("A, B & C") into the
// names it lists, keeping the whole string too.
func splitCreditNames(s string) []string {
	out := []string{s}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '&' || r == '/' })
	for _, p := range parts {
		for q := range strings.SplitSeq(p, " and ") {
			if q = strings.TrimSpace(q); q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

// titleNamesAPerson reports whether title equals, case-insensitively and
// ignoring surrounding whitespace, any of the given names.
func titleNamesAPerson(title string, names []string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && strings.EqualFold(t, n) {
			return true
		}
	}
	return false
}
