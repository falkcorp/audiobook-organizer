// file: internal/authorname/parse.go
// version: 1.1.0
// guid: 9f4c2a71-58d3-4e60-b19a-6c0e7d35f8b2
// last-edited: 2026-09-01

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

var translatorCreditRe = regexp.MustCompile(`^([^-]+)\s*-\s*(?:translator|narrated by)\s*-`)

// ExtractAuthorFromDirectory derives an author from the directory a file sits
// in, or "" when the directory does not name one.
//
// Every branch gates on personname.LooksLikePersonName. That is deliberate and
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
			if candidate := strings.TrimSpace(matches[1]); personname.LooksLikePersonName(candidate) {
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
		if personname.LooksLikePersonName(author) {
			return author
		}
	}

	// Use the directory name if it is person-SHAPED, not merely non-empty.
	if personname.LooksLikePersonName(dirName) {
		return dirName
	}

	return ""
}

// ParseFilenameForAuthor splits a "Title - Author" or "Author - Title" filename,
// returning (title, author). Author is "" when the shape is not a simple
// two-part pattern or the sides cannot be told apart.
//
// The side-choosing decision itself is personname.ChooseAuthorSide, one shared
// decision behind every call site; this function is only the split and the
// tie-break policy.
func ParseFilenameForAuthor(filename string) (string, string) {
	parts := strings.Split(filename, " - ")
	if len(parts) != 2 {
		return "", "" // Not a simple two-part pattern
	}

	left := strings.TrimSpace(parts[0])
	right := strings.TrimSpace(parts[1])

	title, author, ok := personname.ChooseAuthorSide(left, right, personname.PreferRightOnTie)
	if !ok {
		// Couldn't determine, return empty author
		return "", ""
	}
	return title, author
}
