// file: internal/authorname/parse.go
// version: 1.3.0
// guid: 9f4c2a71-58d3-4e60-b19a-6c0e7d35f8b2
// last-edited: 2026-09-28

package authorname

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// This file collapses the two path->author parsers that authorname.go's package
// comment has been tracking as still-duplicated: extractAuthorFromDirectory and
// parseFilenameForAuthor, which lived as separate copies in internal/scanner and
// internal/metadata. That NOTE is now closed.
//
// The copies were NOT equivalent, and the difference was measured rather than
// read: a 28-path differential corpus run through both produced exactly ONE
// disagreement, at "<root>/Unknown Author/01.mp3" -- scanner returned "" (its
// skipDirs carried the placeholder), metadata returned "Unknown Author". Every
// other path, including all three of scanner's other extra skipDirs entries,
// agreed. See the PR for the corpus.
//
// WHY ONLY ONE: skipDirs is very nearly dead code, in BOTH copies, and only the
// differential shows it. LooksLikePersonName refuses anything outside 2-4 words,
// so every single-word directory name -- "import", "imports", "organized",
// "books", "audiobooks", "downloads", "bt", "data", all of them -- returns ""
// at the shape gate whether or not the map catches it first. "Unknown Author" is
// the map's only two-word entry, which is why it is the only entry that can
// change THIS FUNCTION'S RETURN VALUE.
//
// It does not follow that it changes any CONSUMER's outcome, and mutation
// testing showed it does not: delete the placeholder entry and both
// internal/metadata and internal/scanner stay green, because each clears the
// placeholder again downstream (metadata.go:733/:745, scanner.go:1713) via
// IsPlaceholder(StripEditionSuffix(...)). Only this package's own unit tests
// catch it.
//
// So the entry is defence in depth, not the load-bearing guard, and the honest
// statement of its value is: it stops the placeholder at the earliest point, and
// it is the guard that survives if a consumer's own clear is ever moved or
// missed -- which has already happened once, at scanner.go:3024. Keep it; do not
// cite it as the thing preventing the bug.
//
// The map is kept anyway, as the union of both copies. It is a statement about
// what these directories MEAN ("container, never an author") and it stops being
// redundant the moment the shape predicate changes. It is documented as
// currently-redundant so that no future reader mistakes it for the thing doing
// the work -- and so that no future reader deletes it believing that is free.

// skipDirs are directory names that are containers, never authors.
//
// Mostly redundant against the LooksLikePersonName gate below -- see the file
// comment -- with the placeholder as the one live entry.
var skipDirs = map[string]bool{
	// The organizer's own placeholder directory. Reading it back as an author
	// is what made an authorless book look authored and locked it out of AI
	// re-parsing.
	strings.ToLower(Placeholder): true,

	"books": true, "audiobooks": true, "newbooks": true, "downloads": true,
	"media": true, "audio": true, "library": true, "collection": true,
	"import": true, "imports": true, "organized": true,
	"bt": true, "incomplete": true, "data": true,
}

// looksLikeDirectoryAuthor is the gate every branch of ExtractAuthorFromDirectory
// uses: person-SHAPED (personname.LooksLikePersonName) and not work-NAMED
// (personname.LooksLikeWorkTitle).
//
// The second half was added 2026-09-28. The shape test alone accepts "The
// Stormlight Archive" and "The Hobbit" -- two to four capitalised words -- so a
// file in a series or title folder whose own name named no author took the
// FOLDER as its author. That was also the scanner's fallback after
// ParseFilenameForAuthor correctly refused "The Stormlight Archive - The Way of
// Kings": fixing the filename parse alone would have moved the bogus author
// one line down.
func looksLikeDirectoryAuthor(s string) bool {
	return personname.LooksLikePersonName(s) && !personname.LooksLikeWorkTitle(s)
}

var translatorCreditRe = regexp.MustCompile(`^([^-]+)\s*-\s*(?:translator|narrated by)\s*-`)

// ExtractAuthorFromDirectory derives an author from the directory a file sits
// in, or "" when the directory does not name one.
//
// Every branch gates on looksLikeDirectoryAuthor (a person-shaped, non-work name). That is deliberate and
// it is the single most important property of this function: a WRONG author is
// strictly worse than an ABSENT one on the paths that consume this. A wrong
// author still closes the AI nomination gate and nothing downstream can
// recognise it as junk, while an empty author routes to AI filename nomination
// and gets a second chance. Measured 2026-08-25; that asymmetry is STRUCTURAL,
// not a headcount.
//
// COST, stated plainly: single-word directory authors ("Tolkien",
// "Shakespeare", "Homer") are refused, because LooksLikePersonName requires 2-4
// words. They are not lost -- they become AI-parse candidates. Both former
// copies already had this loss, so collapsing them does not introduce it.
//
// HONEST LIMIT: this does not restore the behaviour of the pre-personname
// prefix matcher, and nothing can. That matcher also rejected "Bookclub Picks",
// "Partition Wall", "Part-Time Job", "Partners In Crime" and "Bookkeeping
// Basics" -- by the SAME accident that rejected Booker T. Washington, Volker
// Kutscher and Partha Chatterjee. Those strings are person-SHAPED; no shape
// predicate can separate them, and keeping the accident means keeping the bug.
// They pass here.
func ExtractAuthorFromDirectory(filePath string) string {
	// filepath.Base(filepath.Dir(...)) rather than splitting on
	// os.PathSeparator, which was scanner's idiom: Base is separator-correct on
	// Windows for paths written with "/", and it needs no empty-slice guard.
	// The two agreed on all 28 paths of the differential corpus, including the
	// "/01.mp3" and bare-"01.mp3" edges, so this is a measured swap.
	dirName := filepath.Base(filepath.Dir(filePath))

	if skipDirs[strings.ToLower(dirName)] {
		return ""
	}

	// SUBSUMED ON REALISTIC INPUT, and kept deliberately. Mutation testing
	// deleted this whole branch and every test in all three packages stayed
	// green. The reason is structural: the regex anchors at ^ and its capture is
	// [^-]+, so it can only match when the FIRST hyphen in dirName is the credit
	// separator -- and in exactly that case the trimmed capture equals
	// SplitN(dirName, " - ", 2)[0] trimmed, which the branch below returns
	// anyway. A 632-case structured probe and a 400,000-case fuzz found zero
	// differences on canonically-spaced input.
	//
	// It is NOT an equivalent branch, so it is not deleted: 12 differences exist,
	// all requiring degenerate spacing, and in those the branch gives the BETTER
	// answer. "Terry Pratchett-translator-Mort - translator - X" yields
	// "Terry Pratchett" here and "Terry Pratchett-translator-Mort" without it.
	//
	// What this does mean: this branch's corpus rows, and the eight in each of
	// metadata's and scanner's gates tests, pin a path that cannot change an
	// answer on input anyone will actually have. Do not read their passing as
	// evidence about the credit-parsing behaviour they are named for.
	//
	// Handle "Author - translator - Title" patterns, and "Author, Co-Author -
	// translator - Title" for TWO authors only. The shape gate gives
	// LooksLikePersonName the whole credit, and that caps it at four words, so
	// "Terry Pratchett, Neil Gaiman, Stephen Fry - translator - X" is refused
	// where the ungated code accepted it. A refusal here yields no author
	// rather than a wrong one, which is the trade this function makes
	// everywhere, but the old comment promised a capability the gate does not
	// deliver.
	if strings.Contains(dirName, " - translator - ") || strings.Contains(dirName, " - narrated by - ") {
		matches := translatorCreditRe.FindStringSubmatch(dirName)
		if len(matches) > 1 {
			// Shape-gated like the two branches below. This returned matches[1]
			// with NO predicate at all -- not IsValidAuthor, not
			// LooksLikePersonName -- and it is the FIRST branch tried, so it
			// decided the author before either gate could run:
			//   "Discworld - translator - Mort"            -> "Discworld"
			//   "the quick brown - translator - Mort"       -> "the quick brown"
			//   "Unabridged - narrated by - Stephen Fry"    -> "Unabridged"
			// Same defect as internal/dedup's slash branch, and missed the same
			// way: the branches were gated one at a time by READING the
			// function, and the first-tried one was not in the corpus that
			// measured it.
			if candidate := strings.TrimSpace(matches[1]); looksLikeDirectoryAuthor(candidate) {
				return candidate
			}
		}
	}

	// "Author - Title" directory pattern.
	//
	// Reviewed as a candidate to leave on the bare IsValidAuthor and declined,
	// for the reason above. Junk does reach this branch: "Discworld - Mort",
	// "Bookends - Volume One", "Chapterhouse - Dune" and "Discography - Live"
	// each yield the series name as the author when it is ungated, so the claim
	// that only bare directory names carry junk here is false.
	if strings.Contains(dirName, " - ") {
		// No `len(parts) > 0` guard. Both copies carried one; it is
		// unreachable-false, because strings.SplitN never returns an empty slice
		// for a non-empty separator. personname.go:457 refuses to write exactly
		// this shape of guard, by name, on the grounds that no test can kill it
		// -- so carrying it across would have imported into this package the
		// pattern its sibling rejects.
		author := strings.TrimSpace(strings.SplitN(dirName, " - ", 2)[0])
		if looksLikeDirectoryAuthor(author) {
			return author
		}
	}

	// Use the directory name if it is person-SHAPED, not merely non-empty.
	if looksLikeDirectoryAuthor(dirName) {
		return dirName
	}

	return ""
}

// DashParse is the reading of a two-part "X - Y" filename.
//
// Exactly one of three shapes comes back when Parsed is true:
//
//   - Author and Title set: one side is the author.
//   - Series and Title set: "Series - Title". Callers still apply their own
//     chapter-position check (metadata.SeriesFromTitlePrefix) to Series.
//   - Title alone: no author and NO series. One side was refused as an author
//     but may still BE one, so it is not filed anywhere.
//
// Parsed is false when the name is not exactly two " - " parts; the caller's
// own multi-part fallback applies then, unchanged.
type DashParse struct {
	Parsed bool
	Author string
	Title  string
	Series string
}

// ParseDashFilename reads a two-part "X - Y" filename.
//
// ORDER OF DECISIONS, each one measured against a case that broke:
//
//  1. A left side carrying a zero-padded volume number is "Series NN - Title":
//     "The Expanse 01 - Leviathan Wakes" filed "Leviathan Wakes" as the AUTHOR,
//     because the right side is two capitalised words. The padded number is a
//     sequence position, which a person's name never carries.
//     EXCEPT when the right side names one of the file's ancestor folders.
//     The organizer writes "<root>/<author>/<title>/<title> - <author>.ext",
//     so a title carrying a padded number ("Pratchett 036", "Discworld 01")
//     arrives as "Discworld 01 - Terry Pratchett.mp3" under ".../Terry
//     Pratchett/Discworld 01/"; the folder is the evidence that the right side
//     is the author, which the filename alone cannot give.
//     COST, accepted: the same name OUTSIDE such a folder gives title "Terry
//     Pratchett" and no author. Nothing in the text separates a title from a
//     name of the same shape; this errs to an ABSENT author (AI nomination
//     gets the book) rather than the wrong one.
//  2. Otherwise personname.ChooseAuthorSide picks the author, with a
//     work-named side never eligible (personname.LooksLikeWorkTitle).
//  3. When it refuses, the refused sides are NOT all alike. A side refused only
//     by its leading article can still be a real credit -- "An Na", "The
//     Arbinger Institute", and "A Johnston" is in the production author table
//     -- so it must become neither the SERIES nor the TITLE. Until this was
//     split out, "An Na - A Step from Heaven" filed series "An Na". A side is
//     a possible credit when it is credit-SHAPED
//     (personname.LooksLikeAuthorCreditShape) and carries no series marker
//     (personname.HasSeriesMarker).
//     - neither side a possible credit: Series = left, Title = right
//     ("A Song of Ice and Fire - A Game of Thrones",
//     "The Stormlight Archive - The Way of Kings");
//     - one side a possible credit: Title = the other side, no series;
//     - both: Title = the whole name, no series ("The Dark Tower - The
//     Gunslinger" -- two article-led, person-shaped phrases).
//
// KNOWN LIMIT, not fixed here: "The Hunger Games - Catching Fire" gives author
// "Catching Fire". An article-led left side against a clean two-word right side
// is exactly "The Stand - Stephen King", and the text cannot separate them.
//
// filePath is the file's full path, used only for the ancestor-folder check in
// step 1; "" disables it.
func ParseDashFilename(filename, filePath string) DashParse {
	parts := strings.Split(filename, " - ")
	if len(parts) != 2 {
		return DashParse{}
	}
	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	if personname.HasPaddedVolumeNumber(left) && !personname.HasSeriesMarker(right) &&
		!namesAnAncestorFolder(right, filePath) {
		return DashParse{Parsed: true, Series: left, Title: right}
	}

	if title, author, ok := personname.ChooseAuthorSide(left, right, personname.PreferRightOnTie); ok {
		return DashParse{Parsed: true, Author: author, Title: title}
	}

	possibleCredit := func(side string) bool {
		return !personname.HasSeriesMarker(side) && personname.LooksLikeAuthorCreditShape(side)
	}
	leftCredit, rightCredit := possibleCredit(left), possibleCredit(right)
	switch {
	case !leftCredit && !rightCredit:
		return DashParse{Parsed: true, Series: left, Title: right}
	case leftCredit && !rightCredit:
		return DashParse{Parsed: true, Title: right}
	case !leftCredit && rightCredit:
		return DashParse{Parsed: true, Title: left}
	default:
		return DashParse{Parsed: true, Title: left + " - " + right}
	}
}

// ParseFilenameForAuthor splits a "Title - Author" or "Author - Title" filename,
// returning (title, author). Both are "" when no author was found; callers that
// need the series or title in that case use ParseDashFilename, which is what
// this wraps.
func ParseFilenameForAuthor(filename string) (string, string) {
	p := ParseDashFilename(filename, "")
	if p.Author == "" {
		return "", ""
	}
	return p.Title, p.Author
}

// namesAnAncestorFolder reports whether name equals (case-insensitively) the
// base name of any folder above filePath.
func namesAnAncestorFolder(name, filePath string) bool {
	if filePath == "" {
		return false
	}
	for dir := filepath.Dir(filePath); ; dir = filepath.Dir(dir) {
		if strings.EqualFold(strings.TrimSpace(filepath.Base(dir)), name) {
			return true
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}
