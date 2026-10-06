// file: internal/metabatch/nomatch_query_shapes_test.go
// version: 1.0.0
// guid: 42692a27-c193-48c7-b76a-6bf2a21ef8e3
// last-edited: 2026-10-06

package metabatch

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// nomatchRow is one row of testdata/nomatch_shapes.tsv: a made-up book with
// the shape of one class of the 2026-10-05 no-match census (732 books a
// forced candidate re-fetch left with no match).
type nomatchRow struct {
	id, title, author, asin, path string
	minutes                       int
}

// nomatchPath is the fixture, or NOMATCH_TSV: a private census file of the
// same columns, measured locally and never committed.
func nomatchPath() string {
	if p := os.Getenv("NOMATCH_TSV"); p != "" {
		return p
	}
	return "testdata/nomatch_shapes.tsv"
}

func readNomatchRows(t *testing.T) []nomatchRow {
	t.Helper()
	f, err := os.Open(nomatchPath())
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var rows []nomatchRow
	sc := bufio.NewScanner(f)
	header := true
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		if header {
			header = false
			continue
		}
		c := strings.Split(line, "\t")
		require.Len(t, c, 6, "row %q", line)
		m, _ := strconv.Atoi(c[3])
		rows = append(rows, nomatchRow{id: c[0], title: c[1], author: c[2], minutes: m, asin: c[4], path: c[5]})
	}
	require.NoError(t, sc.Err())
	return rows
}

// The query shapes no provider can answer. Each is a class the 732-book census
// counted; a query carrying one asks the catalog for a book that is not there.
var (
	badReadByRe   = regexp.MustCompile(`(?i)^(?:read|narrated)\s+by\b`)
	badBracketRe  = regexp.MustCompile(`\[[^\]]*\]`)
	badZZRe       = regexp.MustCompile(`^zz\p{Lu}`)
	badCopyRe     = regexp.MustCompile(`(?i)_?copy\d+\b|_\d{1,2}-\d{1,2}$`)
	badEntityRe   = regexp.MustCompile(`(?i)&(?:amp|lt|gt|quot|apos|nbsp|#\d+|#x[0-9a-f]+);`)
	badUnabridged = regexp.MustCompile(`(?i)\((?:un)?abridged\)`)
)

// queryShapeClasses names the bad shapes a built query (title, author) has.
func queryShapeClasses(title, author string) []string {
	var out []string
	if badReadByRe.MatchString(strings.TrimSpace(title)) {
		out = append(out, "title_read_by")
	}
	if badBracketRe.MatchString(author) {
		out = append(out, "author_bracket_tag")
	}
	if badZZRe.MatchString(strings.TrimSpace(author)) {
		out = append(out, "author_zz_prefix")
	}
	if badCopyRe.MatchString(title) || badCopyRe.MatchString(author) {
		out = append(out, "copy_suffix")
	}
	if a := authority.Fold(author); a != "" && a == authority.Fold(title) {
		out = append(out, "author_equals_title")
	} else if restatesTitle(author, title) {
		out = append(out, "author_restates_title")
	}
	if badEntityRe.MatchString(title) || badEntityRe.MatchString(author) {
		out = append(out, "html_entity")
	}
	if badUnabridged.MatchString(title) || badUnabridged.MatchString(author) {
		out = append(out, "unabridged")
	}
	return out
}

var wordRe = regexp.MustCompile(`[\pL\pN]+`)

// restatesTitle: every word of author is in title (any order) and the title
// does not name author as its owner ("Tom Clancy's ...").
func restatesTitle(author, title string) bool {
	aw := wordRe.FindAllString(strings.ToLower(author), -1)
	if len(aw) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(title), -1) {
		have[w] = true
	}
	for _, w := range aw {
		if w != "and" && !have[w] {
			return false
		}
	}
	lt, la := strings.ToLower(title), strings.ToLower(author)
	return !strings.Contains(lt, la+"'s") && !strings.Contains(lt, la+"’s")
}

// authorRepeatsStoredTitle: a sent author that is the book's stored title,
// or restates its words, is the title filed as an author. A seeded known
// person (the swap) is a real author.
func authorRepeatsStoredTitle(author, stored string) bool {
	if strings.TrimSpace(author) == "" {
		return false
	}
	for _, p := range knownPeopleForNomatch {
		if authority.Fold(p) == authority.Fold(author) {
			return false
		}
	}
	return authority.Fold(author) == authority.Fold(stored) || restatesTitle(author, stored)
}

// censusClasses are the 2026-10-05 census's classes, in the order a row is
// classified.
var censusClasses = []string{"read_by", "bracket", "copy", "zz", "swapped", "author=title", "unabridged", "html", "short", "plain"}

var (
	censusCopyRe   = regexp.MustCompile(`(?i)_?copy\d+|_\d\d-\d\d`)
	censusPersonRe = regexp.MustCompile(`^\p{Lu}\pL+ \p{Lu}\pL+$`)
)

// censusClass is the census class of a stored row.
func censusClass(r nomatchRow) string {
	t, a := r.title, r.author
	switch {
	case badReadByRe.MatchString(t):
		return "read_by"
	case strings.HasPrefix(a, "["):
		return "bracket"
	case censusPersonRe.MatchString(t) && censusCopyRe.MatchString(a):
		return "swapped"
	case censusCopyRe.MatchString(t) || censusCopyRe.MatchString(a):
		return "copy"
	case strings.HasPrefix(strings.ToLower(a), "zz"):
		return "zz"
	case strings.EqualFold(strings.TrimSpace(t), strings.TrimSpace(a)):
		return "author=title"
	case badUnabridged.MatchString(t + a):
		return "unabridged"
	case badEntityRe.MatchString(t + a):
		return "html"
	case r.minutes < 30:
		return "short"
	}
	return "plain"
}

// dropRestateClasses removes the author-restates-title classes from bad.
func dropRestateClasses(bad []string) []string {
	var out []string
	for _, c := range bad {
		switch c {
		case "author_equals_title", "author_restates_title", "author_is_stored_title":
		default:
			out = append(out, c)
		}
	}
	return out
}

// knownPeopleForNomatch are the made-up people the fixture's authority lists
// know, so the swapped-title rows are told apart the way prod tells them
// (the authority lists), not by shape.
var knownPeopleForNomatch = []string{"Jane Example"}

// TestNomatchShapes_NoQueryHasABadShape runs every row of the shape fixture
// through the real query path -- the stand-in title
// (ResolveCandidateSearchQuery) and the ladder's resolved title and author
// (metafetch.Service.SearchQuestion, i.e. resolveSearchInputs) -- and asserts
// no query asks by a shape no catalog can answer: "read by narrator" as the
// title, a "[XYZ]" release tag or a "zz" sort prefix as the author, a
// "_copy1" or "_10-02" suffix, an author that is the title, an HTML entity or
// an "(Unabridged)" note. It logs the per-class counts.
func TestNomatchShapes_NoQueryHasABadShape(t *testing.T) {
	rows := readNomatchRows(t)
	if os.Getenv("NOMATCH_TSV") == "" {
		require.Len(t, rows, 38)
	}

	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	for _, name := range knownPeopleForNomatch {
		require.NoError(t, authority.PutPersonOverride(store, authority.PersonOverride{
			Name: name, Roles: map[authority.Role]bool{authority.RoleAuthor: true}, SetAt: time.Unix(0, 0)}))
	}

	type built struct {
		row           nomatchRow
		book          *database.Book
		title, author string
		usable        bool
		bad           []string
	}
	var all []built
	for _, r := range rows {
		b := &database.Book{Title: r.title, FilePath: r.path, Format: "m4b"}
		if r.minutes > 0 {
			d := r.minutes * 60
			b.Duration = &d
		}
		if r.asin != "" {
			a := r.asin
			b.ASIN = &a
		}
		// The book's author row, as prod has it: authorVsTitle reads the
		// row's other books. A name the store refuses ("[XYZ]") stays
		// unlinked, as it would have been.
		if a, aerr := store.CreateAuthor(r.author); aerr == nil && a != nil {
			b.AuthorID = &a.ID
		}
		created, err := store.CreateBook(b)
		require.NoError(t, err)
		all = append(all, built{row: r, book: created})
	}

	// An author with none of the census's shapes is sent exactly as stored.
	for _, r := range rows {
		if len(queryShapeClasses("", r.author)) == 0 && !strings.Contains(r.author, "_") && !strings.Contains(r.author, "꞉") {
			if got := metafetch.SearchAuthorHint(r.author); got != "" {
				require.Equal(t, strings.TrimSpace(r.author), got, "a clean author is unchanged")
			}
		}
	}

	svc := metafetch.NewService(store)
	counts := map[string]int{}
	long := map[string]int{} // of counts, rows of 30 min or more
	var offenders []string
	usable := 0
	for i := range all {
		x := &all[i]
		q := ResolveCandidateSearchQuery(store, x.book)
		if !q.Usable {
			continue
		}
		usable++
		x.usable = true
		x.title, x.author = svc.SearchQuestion(x.book, q.Title, metafetch.SearchAuthorHint(x.row.author))
		bad := queryShapeClasses(x.title, x.author)
		if authorRepeatsStoredTitle(x.author, x.row.title) {
			bad = append(bad, "author_is_stored_title")
		}
		// A credit that restates the title but is not proven junk is kept
		// as a suspect, and the title is asked alone as well: that query
		// can find the book whatever the credit is.
		if svc.SearchAsksTitleAlone(x.book, q.Title, metafetch.SearchAuthorHint(x.row.author)) {
			bad = dropRestateClasses(bad)
		}
		x.bad = bad
		for _, c := range bad {
			counts[c]++
			if x.row.minutes >= 30 {
				long[c]++
			}
			if counts[c] <= 5 {
				offenders = append(offenders, c+": "+x.row.title+" | "+x.row.author+" -> "+x.title+" | "+x.author)
			}
		}
	}
	classes := make([]string, 0, len(counts))
	for c := range counts {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	longUsable := 0
	for _, x := range all {
		if x.usable && x.row.minutes >= 30 {
			longUsable++
		}
	}
	t.Logf("searched %d of %d rows, %d of them 30 min or more (the rest are refused part rows or have no usable title)",
		usable, len(rows), longUsable)
	for _, c := range classes {
		t.Logf("bad shape %-22s %d (%d of them 30 min or more)", c, counts[c], long[c])
	}
	for _, o := range offenders {
		t.Log(o)
	}
	// The census's own classes, read off the STORED row: per class, the rows,
	// how many were searched, and how many searched rows still asked with a
	// bad shape.
	type tally struct{ rows, long, searched, defects int }
	byClass := map[string]*tally{}
	for _, x := range all {
		c := censusClass(x.row)
		if byClass[c] == nil {
			byClass[c] = &tally{}
		}
		tl := byClass[c]
		tl.rows++
		if x.row.minutes >= 30 {
			tl.long++
		}
		if x.usable {
			tl.searched++
			if len(x.bad) > 0 {
				tl.defects++
			}
		}
	}
	for _, c := range censusClasses {
		if tl := byClass[c]; tl != nil {
			t.Logf("census class %-14s rows %3d (%3d >=30 min)  searched %3d  bad query %3d", c, tl.rows, tl.long, tl.searched, tl.defects)
		}
	}
	if os.Getenv("NOMATCH_DUMP") != "" {
		for _, x := range all {
			if x.usable {
				t.Logf("Q %q | %q  =>  %q | %q", x.row.title, x.row.author, x.title, x.author)
			} else {
				t.Logf("SKIP %q | %q | %d min | %s", x.row.title, x.row.author, x.row.minutes, x.row.path)
			}
		}
	}
	require.Empty(t, counts, "no query may carry a shape no catalog can answer")
}

// The organizer once named a book's folder from its bad title ("read by
// narrator", "Unknown Title"), so that folder is no stand-in -- the one above
// it is the book's. Climbed one level only, out of exactly those names, and a
// person's folder above ("Example, Jane") is no title. Made-up rows with the
// census's shapes; the first is a whole 22-hour book.
func TestResolveCandidateSearchQuery_ClimbsOutOfThePoisonedFolder(t *testing.T) {
	const lib = "/srv/example-organizer/"
	for _, tc := range []struct {
		title, path, want string
		minutes           int
	}{
		{"read by narrator", lib + "Glass Orchard Book 3/Glass Orchard/read by narrator/read by narrator.mp3", "Glass Orchard", 1300},
		{"read by narrator", lib + "Book 1 (Unabridged)/- The Quiet Field/read by narrator/read by narrator.mp3", "The Quiet Field", 42},
		{"Unknown Title", lib + "Some Choreography/Unknown Title/Unknown Title.mp3", "Some Choreography", 300},
		{"read by narrator", lib + "Example Book/Example, Jane/read by narrator/read by narrator.mp3", "", 9},
	} {
		t.Run(tc.path, func(t *testing.T) {
			d := tc.minutes * 60
			book := &database.Book{ID: "b1", Title: tc.title, FilePath: tc.path, Duration: &d}
			q := ResolveCandidateSearchQuery(fakeBookFiles{dir: map[string]string{"b1": tc.path}}, book)
			assert.Equal(t, tc.want, q.Title)
			assert.Equal(t, tc.want != "", q.Usable)
		})
	}
}
