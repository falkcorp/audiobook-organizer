// file: internal/authorname/parse.go
// version: 1.7.0
// guid: 9f4c2a71-58d3-4e60-b19a-6c0e7d35f8b2
// last-edited: 2026-09-29

package authorname

import (
	"path/filepath"
	"regexp"
	"slices"
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

// genericDirNames are folder names that say nothing about the work or its
// author: a library or import root, or the "unknown" placeholder. They live
// here, not in internal/metadata, so the directory fallback below and the
// metadata folder parser refuse the SAME set (metadata imports this package;
// the reverse would be a cycle). Read through IsGenericFolder.
var genericDirNames = map[string]bool{
	"": true, ".": true, "/": true,
	"books": true, "audiobooks": true, "audiobook": true, "downloads": true, "import": true,
	"imports": true, "incoming": true, "library": true, "media": true, "audio": true,
	"unknown author": true, "unknown": true,
}

// genreDirNames are genre / category folder names that are person-SHAPED (two
// capitalised words, or one) and would otherwise pass a shape test: a library
// shelved "<genre>/<title>/<file>" handed the genre to the author fallback
// ("/lib/Science Fiction/Good Omens/Good Omens.mp3" gave "Science Fiction").
// No genre list existed in the repo to reuse: the provider genre data (Audible
// category ladders, Google Books categories) is per-book API output, not a
// lookup set. Read through IsGenreFolder.
var genreDirNames = map[string]bool{
	"science fiction": true, "sci-fi": true, "scifi": true, "fantasy": true,
	"science fiction & fantasy": true, "science fiction and fantasy": true,
	"mystery": true, "mysteries": true, "thriller": true, "thrillers": true,
	"mystery & thriller": true, "horror": true, "romance": true,
	"non-fiction": true, "nonfiction": true, "non fiction": true, "fiction": true,
	"biography": true, "biographies": true, "history": true,
	"young adult": true, "kids": true, "children": true, "childrens": true,
	"children's": true, "classics": true, "literature": true,
}

// IsGenericFolder reports whether a folder name is a container (library or
// import root, disc-less "unknown" placeholder), never a work or an author.
func IsGenericFolder(name string) bool {
	return genericDirNames[strings.ToLower(strings.TrimSpace(name))]
}

// IsGenreFolder reports whether a folder name is a genre or category shelf.
func IsGenreFolder(name string) bool {
	return genreDirNames[strings.ToLower(strings.TrimSpace(name))]
}

// looksLikeDirectoryAuthor is the gate every branch of ExtractAuthorFromDirectory
// uses: person-SHAPED (personname.LooksLikePersonName), not work-NAMED
// (personname.LooksLikeWorkTitle), and not a genre or container folder
// (IsGenreFolder / IsGenericFolder).
//
// The second half was added 2026-09-28. The shape test alone accepts "The
// Stormlight Archive" and "The Hobbit" -- two to four capitalised words -- so a
// file in a series or title folder whose own name named no author took the
// FOLDER as its author. That was also the scanner's fallback after
// ParseDashFilename correctly refused "The Stormlight Archive - The Way of
// Kings": fixing the filename parse alone would have moved the bogus author
// one line down.
func looksLikeDirectoryAuthor(s string) bool {
	if IsGenreFolder(s) || IsGenericFolder(s) {
		return false
	}
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
//     TWO EXCEPTIONS keep the right side as the author:
//     - the organizer's own layout, "<root>/<author>/<title>/<title> -
//     <author>.ext": the IMMEDIATE parent folder equals the left side and
//     the right side names a folder above it. A title carrying a padded
//     number ("Pratchett 036", "Discworld 01") arrives exactly so. Matching
//     the right side against ANY ancestor was fooled by
//     "<author>/<series>/<title>/<series NN> - <title>.ext" layouts;
//     - a STRONG name on the right (looksLikeStrongName): a single-letter
//     initial, a "Surname, Given" comma form, a list of person-shaped names,
//     or an edition suffix ("Brandon Sanderson (Unabridged)"); never with a
//     capitalised function word inside ("Words Of Radiance").
//     COST, accepted: a plain name outside the organizer layout -- "The Dark
//     Tower 01 - Stephen King", "Red Rising 01 - Pierce Brown", "Stormlight
//     01 - Brandon Sanderson Jr" -- gives that name as the title and no
//     author. "Leviathan Wakes" and "Stephen King" have the same shape; this
//     errs to an ABSENT author (tags or AI nomination fill it) rather than a
//     wrong one.
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
		!isOrganizerLayout(left, right, filePath) && !looksLikeStrongName(right) {
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
		// "The Hobbit - Chapter 01" / "- Part 1" / "- 01": the non-credit
		// side is a position marker, not the title, so the title is the
		// credit-shaped side.
		if personname.HasSeriesMarker(right) {
			return DashParse{Parsed: true, Title: left}
		}
		return DashParse{Parsed: true, Title: right}
	case !leftCredit && rightCredit:
		// "Mistborn Book 1 - The Final Empire": a series-marked left side is
		// the series and the right side the title. (Step 1 is NOT widened to
		// every series marker: "The Book Thief", "The Jungle Book" and "Book
		// Lovers" carry one and are titles.)
		if personname.HasSeriesMarker(left) {
			return DashParse{Parsed: true, Series: left, Title: right}
		}
		return DashParse{Parsed: true, Title: left}
	default:
		return DashParse{Parsed: true, Title: left + " - " + right}
	}
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

// isOrganizerLayout reports whether filePath is the organizer's own
// "<author>/<title>/<title> - <author>.ext" shape for this left/right pair:
// the immediate parent is the left side and a folder above it is the right.
func isOrganizerLayout(left, right, filePath string) bool {
	if filePath == "" {
		return false
	}
	parent := filepath.Dir(filePath)
	if !strings.EqualFold(strings.TrimSpace(filepath.Base(parent)), left) {
		return false
	}
	return namesAnAncestorFolder(right, parent)
}

// looksLikeStrongName reports whether s is a credit whose SHAPE, not just its
// capitalisation, says person. A plain run of capitalised words is not strong:
// "Leviathan Wakes" and "Stephen King" cannot be told apart, and neither can
// "Words Of Radiance", "Children Of Dune" or "Mr. Mercedes" from a name by word
// count or a "." alone -- each of those was credited as the author while this
// accepted three or more words and any ".". Strong evidence is one of:
//   - a single-letter initial ("J. R. R. Tolkien", "James S. A. Corey");
//   - "Surname, Given" (isSurnameGiven);
//   - a list joined by ",", "&" or "and" whose every part is person-shaped;
//   - an edition suffix ("Brandon Sanderson (Unabridged)"): a title with a
//     padded series number in front of it is not decorated that way.
//
// A capitalised function word inside the string (Of, The, And, In, To, At,
// From, For, A, An) vetoes all of these.
func looksLikeStrongName(s string) bool {
	trimmed := strings.TrimSpace(s)
	bare := strings.TrimSpace(personname.StripEditionSuffix(trimmed))
	if bare == "" || !personname.LooksLikeAuthorCredit(bare) || hasInnerFunctionWord(bare) {
		return false
	}
	if bare != trimmed {
		return true
	}
	return hasInitial(bare) || isSurnameGiven(bare) || isPersonList(bare)
}

// innerFunctionWords are the capitalised function words that sit inside titles
// ("Words Of Radiance", "Harry Potter And The ...") and not inside names.
// Lowercase "and" is a list joiner and is not on it.
var innerFunctionWords = map[string]bool{
	"Of": true, "The": true, "And": true, "In": true, "To": true,
	"At": true, "From": true, "For": true, "A": true, "An": true,
}

func hasInnerFunctionWord(s string) bool {
	fields := strings.Fields(s)
	for i := 1; i < len(fields); i++ {
		w := strings.Trim(fields[i], ",&")
		if !innerFunctionWords[w] {
			continue
		}
		// A bare "A" between two capitalised words is a middle initial
		// ("Robert A Heinlein"), not the article.
		if w == "A" && i+1 < len(fields) && startsUpper(fields[i-1]) && startsUpper(fields[i+1]) {
			continue
		}
		// A trailing "A" in a "Surname, Given A" form is an initial too.
		if w == "A" && i == len(fields)-1 && startsUpper(fields[i-1]) && isSurnameGiven(s) {
			continue
		}
		return true
	}
	return false
}

func startsUpper(w string) bool {
	return w != "" && w[0] >= 'A' && w[0] <= 'Z'
}

// initialRe is one initial or a run of them: "J", "S.", "J.R.R.", "J.R.R".
var initialRe = regexp.MustCompile(`^(?:[A-Z]\.){1,4}[A-Z]?$|^[A-Z]$`)

func isInitial(tok string) bool {
	return initialRe.MatchString(strings.TrimRight(tok, ","))
}

func hasInitial(s string) bool {
	return slices.ContainsFunc(strings.Fields(s), isInitial)
}

// isSurnameGiven reports the "Surname, Given" shape: exactly one comma, a
// surname of one or two words, and a given part that is one word, or one word
// or initial followed only by initials ("King, Stephen", "Tolkien, J.R.R.",
// "Le Guin, Ursula K."). "Neil Gaiman, Terry Pratchett" is a list, not this.
func isSurnameGiven(s string) bool {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return false
	}
	surname, given := strings.Fields(parts[0]), strings.Fields(parts[1])
	if len(surname) == 0 || len(surname) > 2 || len(given) == 0 {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])) {
		// "Guards, Guards" is a title: a name does not repeat itself.
		return false
	}
	for _, g := range given[1:] {
		if !isInitial(g) {
			return false
		}
	}
	return true
}

// creditListSepRe splits a credit list on ",", "&" or a standalone "and".
var creditListSepRe = regexp.MustCompile(`(?i)\s*(?:,|&|\s+and\s+)\s*`)

// isPersonList reports a list of two or more credits, each person-shaped.
// "Preston & Child" is refused: its parts are single words.
func isPersonList(s string) bool {
	parts := creditListSepRe.Split(s, -1)
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if !personname.LooksLikePersonName(strings.TrimSpace(p)) {
			return false
		}
	}
	return true
}

// CreditIncludes reports whether side credits every name in tag, up to case,
// an edition suffix ("Andy Weir (Unabridged)"), initial punctuation ("J.R.R."
// / "J R R"), the "Surname, Given" inversion, and the order and joiner of a
// list ("Neil Gaiman, Terry Pratchett" / "Neil Gaiman & Terry Pratchett"). It
// exists so a caller orienting a filename by a TAGGED author does not read a
// spelling difference, or a tag naming one of several credited authors, as a
// disagreement: tag "Neil Gaiman" is included in "Neil Gaiman & Terry
// Pratchett". It compares name SETS, not substrings: tag "Douglas Preston &
// Lincoln Child" is not included in "Lincoln Child", and "S. King" is not
// "Stephen King".
func CreditIncludes(side, tag string) bool {
	have, want := creditNames(side), creditNames(tag)
	if len(want) == 0 {
		return false
	}
	for _, n := range want {
		if _, found := slices.BinarySearch(have, n); !found {
			return false
		}
	}
	return true
}

// creditNames normalises a credit to its sorted set of names. The comma swap
// applies only to the "Surname, Given" shape; swapping any two-part comma
// string turned "Neil Gaiman, Terry Pratchett" into "Terry Pratchett Neil
// Gaiman".
func creditNames(s string) []string {
	s = strings.TrimSpace(personname.StripEditionSuffix(s))
	if isSurnameGiven(s) {
		parts := strings.SplitN(s, ",", 2)
		s = strings.TrimSpace(parts[1]) + " " + strings.TrimSpace(parts[0])
	}
	var names []string
	for _, p := range creditListSepRe.Split(s, -1) {
		p = strings.ToLower(strings.NewReplacer(".", " ", "_", " ").Replace(p))
		if p = strings.Join(strings.Fields(p), " "); p != "" {
			names = append(names, p)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ExtractAuthorAboveTitle is ExtractAuthorFromDirectory for a probe whose
// parent folder sits directly above a TITLE folder -- the "<x>/<title>/<file>"
// fallbacks, where the caller has already recognised the file's own folder as
// the title and passes that folder as the probe. The folder read is then
// whatever shelves titles: an author, but just as often a series or a genre.
// A series folder whose name OPENS the title and runs on into it ("Harry
// Potter" above "Harry Potter and the Goblet of Fire") is refused here; genre
// folders are refused by the shared gate. title == "" disables the check.
//
// "Runs on" means the word after the folder name is lowercase or a joining
// word (and, the, of, &, in, at, to, for; capitalised "The" excepted, see
// titleRunsOn). An author's name opening the title
// is ordinary shelving -- "Stephen King Short Stories", "Stephen King
// Collection", "Brandon Sanderson Mistborn", "Stephen King - The Stand" -- and
// refusing every prefix lost those authors. A " - " after the name is never
// refused: that is the "<author> - <title>" credit form.
//
// COST of the narrower rule: a series folder followed by a capitalised word
// ("Alex Cross/Alex Cross Must Die/", "Jack Reacher/Jack Reacher Killing
// Floor/") is read as the author, as it was before this check existed.
//
// LIMIT: a series folder that does not prefix its titles ("Jack
// Reacher/Killing Floor/") is person-shaped and still read as the author.
func ExtractAuthorAboveTitle(probe, title string) string {
	if title != "" {
		folder := strings.TrimSpace(filepath.Base(filepath.Dir(probe)))
		t := strings.TrimSpace(title)
		if folder != "" && len(t) > len(folder) && strings.EqualFold(t[:len(folder)], folder) &&
			t[len(folder)] == ' ' && titleRunsOn(t[len(folder):]) {
			return ""
		}
	}
	return ExtractAuthorFromDirectory(probe)
}

// coAuthorDashRe matches a co-author joined to the folder-name prefix and
// followed by " - ": the joiner, then the co-author (group 1), then the dash.
var coAuthorDashRe = regexp.MustCompile(`^\s*(?:&|(?i:and)|,)\s+([^-]+?)\s+-\s`)

// joiningWords continue a phrase: a title folder whose name is a series name
// followed by one of these ("Harry Potter and the ...") is a series title, not
// an author's shelf.
var joiningWords = map[string]bool{
	"and": true, "the": true, "of": true, "&": true, "in": true, "at": true, "to": true, "for": true,
}

// titleRunsOn reports whether rest (the title after a folder-name prefix,
// starting with a space) continues the prefix as one phrase: its first word is
// lowercase or a joining word. A " - " separator is never a continuation.
func titleRunsOn(rest string) bool {
	if strings.HasPrefix(rest, " - ") {
		return false
	}
	// A co-author list before the credit separator ("Terry Pratchett &
	// Stephen Baxter - The Long Earth", "Neil Gaiman and Terry Pratchett -
	// Good Omens") is the "<authors> - <title>" form, not a run-on title --
	// but only when the joined part is itself a credit: "Harry Potter and the
	// Goblet of Fire - Part 1" is still a run-on.
	if m := coAuthorDashRe.FindStringSubmatch(rest); m != nil && personname.LooksLikeAuthorCredit(m[1]) {
		return false
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return false
	}
	next := fields[0]
	if next == "The" {
		// Capitalised "The" after an author's name starts the work's own
		// title ("Terry Pratchett The Colour of Magic", "Stephen King The
		// Stand"); lowercase "the" is still caught below.
		return false
	}
	if joiningWords[strings.ToLower(next)] {
		return true
	}
	return next[0] >= 'a' && next[0] <= 'z'
}
