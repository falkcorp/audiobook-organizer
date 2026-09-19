// file: internal/plugins/maintenance/author_path_link_test.go
// version: 1.1.0
// guid: 0d4c7f61-2b58-4a39-9c6e-51f0a7d3b284
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// --- fixture helpers ---

// pathLinkFixture is an in-memory library: author rows plus book rows, served
// through a database.MockStore whose write methods are wired by each test.
type pathLinkFixture struct {
	authors []database.Author
	books   []*database.Book
}

func (f *pathLinkFixture) author(id int, name string) {
	f.authors = append(f.authors, database.Author{ID: id, Name: name})
}

func (f *pathLinkFixture) book(id, path string, authorID *int) {
	f.books = append(f.books, &database.Book{ID: id, Title: "T " + id, FilePath: path, AuthorID: authorID})
}

// filler adds n books already carrying authorID, so the author row's book_count
// (live books whose scalar names it) clears the suspect-thin-row bar.
func (f *pathLinkFixture) filler(authorID, n int) {
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("fill-%d-%d", authorID, i)
		aid := authorID
		f.book(id, "/mnt/bigdata/books/audiobook-organizer/Filler/"+id+"/x.m4b", &aid)
	}
}

func (f *pathLinkFixture) store(t *testing.T) *database.MockStore {
	t.Helper()
	byID := map[string]*database.Book{}
	for _, b := range f.books {
		byID[b.ID] = b
	}
	authorsByID := map[int]*database.Author{}
	authorsByName := map[string]*database.Author{}
	for i := range f.authors {
		a := &f.authors[i]
		authorsByID[a.ID] = a
		authorsByName[normalizeForTest(a.Name)] = a
	}
	return &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return f.authors, nil },
		GetAllBooksCoreCompleteFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			out := make([]database.BookCore, 0, len(f.books))
			for _, b := range f.books {
				out = append(out, b.Core())
			}
			return out, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := byID[id]
			if !ok {
				return nil, nil
			}
			cp := *b
			return &cp, nil
		},
		GetAuthorByIDFunc:   func(id int) (*database.Author, error) { return authorsByID[id], nil },
		GetAuthorByNameFunc: func(name string) (*database.Author, error) { return authorsByName[normalizeForTest(name)], nil },
		GetBookAuthorsFunc:  func(string) ([]database.BookAuthor, error) { return nil, nil },
	}
}

func normalizeForTest(s string) string {
	return normalizeAuthorNameForLink(s)
}

func runPathLink(t *testing.T, p *Plugin, params string) *authorPathLinkResult {
	t.Helper()
	var ps authorPathLinkParams
	if params != "" {
		if err := json.Unmarshal([]byte(params), &ps); err != nil {
			t.Fatalf("params %s: %v", params, err)
		}
	}
	res, err := p.authorPathLink(context.Background(), ps, &fakeReporter{})
	if err != nil {
		t.Fatalf("authorPathLink(%s): %v", params, err)
	}
	return res
}

func outcomeOf(t *testing.T, res *authorPathLinkResult, bookID string) authorPathLinkChange {
	t.Helper()
	c, ok := findChange(res, bookID)
	if !ok {
		t.Fatalf("book %s missing from the change list (%d entries)", bookID, len(res.Changes))
	}
	return c
}

func findChange(res *authorPathLinkResult, bookID string) (authorPathLinkChange, bool) {
	for _, c := range res.Changes {
		if c.BookID == bookID {
			return c, true
		}
	}
	return authorPathLinkChange{}, false
}

// --- classification ---

// 🔴 The whole classification contract in one table. Each case is one book in a
// shared fixture, so the book_count of every author row is computed over the
// same snapshot an apply would see.
func TestAuthorPathLink_Classification(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Charles Dickens")
	f.author(2, "Jane Doe")
	f.author(3, "John Smith")
	f.author(4, "Thin Person")
	f.author(5, "Michael Grant")
	f.author(6, "Christopher Paolini")
	f.author(7, "Of Fire and Night") // a title-fragment row the gate must never reach
	f.filler(1, 4)
	f.filler(2, 4)
	f.filler(3, 4)
	f.filler(5, 4)
	f.filler(6, 4)
	f.filler(7, 4)
	f.filler(4, 1) // exactly one book: the suspect-thin-row bar

	existing := 1
	f.book("has-author", "/mnt/bigdata/books/audiobook-organizer/Jane Doe/Some Title/x.m4b", &existing)
	f.book("exact", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/1844 - Martin Chuzzlewit/x.m4b", nil)
	f.book("dash", "/mnt/bigdata/books/newbooks/moved/Michael Grant - Gone (7-9)/Book 1/x.m4b", nil)
	f.book("doctorwho", "/mnt/bigdata/books/audiobook-organizer/Jane Doe/Doctor Who and the Cybermen/x.m4b", nil)
	f.book("bigfinish", "/mnt/bigdata/books/audiobook-organizer/Jane Doe/Big.Finish Dalek Empire/x.m4b", nil)
	f.book("itunes", "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/Jane Doe/x.m4b", nil)
	f.book("thin", "/mnt/bigdata/books/audiobook-organizer/Thin Person/A Title/x.m4b", nil)
	f.book("leaf", "/mnt/bigdata/books/abooks/John Smith/x.m4b", nil)
	f.book("ambiguous", "/mnt/bigdata/books/audiobook-organizer/Jane Doe/John Smith/A Title/x.m4b", nil)
	f.book("nearmiss", "/mnt/bigdata/books/newbooks/Christopher Paolin/The Inheritance Cycle/x.m4b", nil)
	f.book("newrow", "/mnt/bigdata/books/abooks/imported/Ursula Le Guin/A Wizard of Earthsea/x.m4b", nil)
	f.book("titlefrag", "/mnt/bigdata/books/audiobook-organizer/Of Fire and Night/Disc 1/x.m4b", nil)
	f.book("container", "/mnt/bigdata/books/audiobook-organizer/x.m4b", nil)
	f.book("series", "/mnt/bigdata/books/audiobook-organizer/Night Angel Trilogy Book 1/Disc 1/x.m4b", nil)

	p := New(&fakeDeps{store: f.store(t)})
	res := runPathLink(t, p, `{"dry_run":true}`)

	cases := []struct {
		book     string
		outcome  string
		authorID int
	}{
		{"has-author", authorPathLinkHasAuthor, 0},
		{"exact", authorPathLinkWouldLink, 1},
		{"dash", authorPathLinkWouldLink, 5},
		{"doctorwho", authorPathLinkOwnerManual, 0},
		{"bigfinish", authorPathLinkOwnerManual, 0},
		{"itunes", authorPathLinkITunesHandsOff, 0},
		{"thin", authorPathLinkSuspectThin, 4},
		{"leaf", authorPathLinkSuspectLeaf, 3},
		{"ambiguous", authorPathLinkAmbiguous, 0},
		{"nearmiss", authorPathLinkNearMiss, 0},
		{"newrow", authorPathLinkWouldCreate, 0},
		{"titlefrag", authorPathLinkNotDerivable, 0},
		{"container", authorPathLinkNotDerivable, 0},
		{"series", authorPathLinkNotDerivable, 0},
	}
	for _, tc := range cases {
		t.Run(tc.book, func(t *testing.T) {
			got, listed := findChange(res, tc.book)
			// The library-scale buckets are counted, not listed: a full entry
			// per already-authored book would bury the rows worth reading.
			if !authorPathLinkDetailed(tc.outcome) {
				if listed {
					t.Fatalf("book %s: %q is counter-only but was listed: %+v", tc.book, tc.outcome, got)
				}
				if res.Outcomes[tc.outcome] == 0 {
					t.Fatalf("book %s: outcome %q not counted (outcomes=%v)", tc.book, tc.outcome, res.Outcomes)
				}
				return
			}
			if !listed {
				t.Fatalf("book %s: %q is detailed but was not listed (outcomes=%v)", tc.book, tc.outcome, res.Outcomes)
			}
			if got.Outcome != tc.outcome {
				t.Fatalf("book %s: outcome %q, want %q (derived=%q author=%d)", tc.book, got.Outcome, tc.outcome, got.DerivedName, got.AuthorID)
			}
			if tc.authorID != 0 && got.AuthorID != tc.authorID {
				t.Fatalf("book %s: author id %d, want %d", tc.book, got.AuthorID, tc.authorID)
			}
			if got.Applied {
				t.Fatalf("book %s: dry run reported Applied", tc.book)
			}
		})
	}
	if res.Outcomes[authorPathLinkWouldLink] != 2 {
		t.Fatalf("would_link=%d, want 2 (outcomes=%v)", res.Outcomes[authorPathLinkWouldLink], res.Outcomes)
	}
	// The near miss names the row it nearly matched, and never links to it.
	nm := outcomeOf(t, res, "nearmiss")
	if nm.NearestAuthorID != 6 || nm.AuthorID != 0 {
		t.Fatalf("near miss: nearest=%d author=%d, want nearest 6 and author 0", nm.NearestAuthorID, nm.AuthorID)
	}
}

// 🔴 DRY RUN WRITES NOTHING. Every write method fails the test.
func TestAuthorPathLink_DryRunWritesNothing(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Charles Dickens")
	f.filler(1, 4)
	f.book("exact", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/A Title/x.m4b", nil)
	f.book("newrow", "/mnt/bigdata/books/abooks/imported/Ursula Le Guin/A Title/x.m4b", nil)

	store := f.store(t)
	fail := func(what string) { t.Errorf("dry run called %s", what) }
	store.UpdateBookFunc = func(string, *database.Book) (*database.Book, error) { fail("UpdateBook"); return nil, errors.New("no") }
	store.SetBookAuthorsFunc = func(string, []database.BookAuthor) error { fail("SetBookAuthors"); return errors.New("no") }
	store.ModifyBookAuthorsFunc = func(string, func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
		fail("ModifyBookAuthors")
		return nil, errors.New("no")
	}
	store.CreateAuthorFunc = func(string) (*database.Author, error) { fail("CreateAuthor"); return nil, errors.New("no") }
	store.CreateOperationChangeFunc = func(*database.OperationChange) error { fail("CreateOperationChange"); return errors.New("no") }

	p := New(&fakeDeps{store: store})
	for _, params := range []string{``, `{"dry_run":true}`, `{"dryRun":true}`} {
		res := runPathLink(t, p, params)
		if !res.DryRun {
			t.Fatalf("params %q: DryRun=false", params)
		}
		if res.UndoLedgerRows != 0 {
			t.Fatalf("params %q: undo rows %d", params, res.UndoLedgerRows)
		}
	}
}

// --- apply, against a real PebbleStore ---

func pathLinkPebble(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pathLinkCreateBook(t *testing.T, s database.Store, id, path string, authorID *int) {
	t.Helper()
	if _, err := s.CreateBook(&database.Book{ID: id, Title: "T " + id, FilePath: path, AuthorID: authorID}); err != nil {
		t.Fatalf("CreateBook(%s): %v", id, err)
	}
}

// 🔴 An apply writes the join row and the scalar, and records the undo ledger.
func TestAuthorPathLink_ApplyLinksAndRecords(t *testing.T) {
	s := pathLinkPebble(t)
	a, err := s.CreateAuthor("Charles Dickens")
	if err != nil {
		t.Fatalf("CreateAuthor: %v", err)
	}
	for i := 0; i < 3; i++ {
		pathLinkCreateBook(t, s, fmt.Sprintf("fill%d", i), fmt.Sprintf("/mnt/bigdata/books/audiobook-organizer/Filler/f%d/x.m4b", i), &a.ID)
	}
	pathLinkCreateBook(t, s, "link-me", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/Bleak House/x.m4b", nil)

	p := New(&fakeDeps{store: s})
	res := runPathLink(t, p, `{"dry_run":false}`)
	if res.Outcomes[authorPathLinkLinked] != 1 {
		t.Fatalf("linked=%d, want 1 (outcomes=%v)", res.Outcomes[authorPathLinkLinked], res.Outcomes)
	}
	b, err := s.GetBookByID("link-me")
	if err != nil || b == nil || b.AuthorID == nil || *b.AuthorID != a.ID {
		t.Fatalf("book scalar = %+v (%v), want author %d", b, err, a.ID)
	}
	joins, err := s.GetBookAuthors("link-me")
	if err != nil || len(joins) != 1 || joins[0].AuthorID != a.ID {
		t.Fatalf("book_authors = %+v (%v), want one credit for %d", joins, err, a.ID)
	}
	if res.UndoLedgerRows < 1 {
		t.Fatalf("undo ledger rows = %d, want >= 1", res.UndoLedgerRows)
	}
}

// 🔴 An explicit-id apply touches ONLY the listed books, even when others in the
// library would classify as confident links.
func TestAuthorPathLink_ExplicitBookIDs(t *testing.T) {
	s := pathLinkPebble(t)
	a, _ := s.CreateAuthor("Charles Dickens")
	for i := 0; i < 3; i++ {
		pathLinkCreateBook(t, s, fmt.Sprintf("fill%d", i), fmt.Sprintf("/mnt/bigdata/books/audiobook-organizer/Filler/f%d/x.m4b", i), &a.ID)
	}
	pathLinkCreateBook(t, s, "chosen", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/Bleak House/x.m4b", nil)
	pathLinkCreateBook(t, s, "other", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/Hard Times/x.m4b", nil)

	p := New(&fakeDeps{store: s})
	res := runPathLink(t, p, `{"dry_run":false,"bookIds":["chosen"]}`)
	if res.Outcomes[authorPathLinkLinked] != 1 {
		t.Fatalf("linked=%d, want 1 (outcomes=%v)", res.Outcomes[authorPathLinkLinked], res.Outcomes)
	}
	if b, _ := s.GetBookByID("other"); b == nil || b.AuthorID != nil {
		t.Fatalf("book outside bookIds was written: %+v", b)
	}
	if b, _ := s.GetBookByID("chosen"); b == nil || b.AuthorID == nil {
		t.Fatalf("listed book was not linked: %+v", b)
	}
}

// 🔴 A book that already carries an AuthorID is never rewritten, not even when
// the path names a different existing author row.
func TestAuthorPathLink_ExistingAuthorIDUntouched(t *testing.T) {
	s := pathLinkPebble(t)
	dickens, _ := s.CreateAuthor("Charles Dickens")
	jane, _ := s.CreateAuthor("Jane Doe")
	for i := 0; i < 3; i++ {
		pathLinkCreateBook(t, s, fmt.Sprintf("fill%d", i), fmt.Sprintf("/mnt/bigdata/books/audiobook-organizer/Filler/f%d/x.m4b", i), &dickens.ID)
	}
	pathLinkCreateBook(t, s, "owned", "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/Bleak House/x.m4b", &jane.ID)

	p := New(&fakeDeps{store: s})
	res := runPathLink(t, p, `{"dry_run":false}`)
	if res.Outcomes[authorPathLinkLinked] != 0 {
		t.Fatalf("linked=%d, want 0 (outcomes=%v)", res.Outcomes[authorPathLinkLinked], res.Outcomes)
	}
	b, _ := s.GetBookByID("owned")
	if b == nil || b.AuthorID == nil || *b.AuthorID != jane.ID {
		t.Fatalf("scalar was rewritten: %+v, want %d", b, jane.ID)
	}
}

// staleNameStore makes every author-name lookup MISS, which is the shape of the
// create race: two workers both believe the row does not exist yet. CreateAuthor
// itself re-checks under the name-index lock, so exactly one row must be minted.
type staleNameStore struct {
	database.Store
	mu    sync.Mutex
	calls int
}

func (s *staleNameStore) GetAuthorByName(name string) (*database.Author, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return nil, nil
}

// 🔴 Creation is idempotent under a concurrent duplicate. Two books in different
// folders derive the SAME new author name and are processed by the op's worker
// pool at the same time, with every name lookup lying about the row's absence.
func TestAuthorPathLink_CreateIsIdempotentUnderRace(t *testing.T) {
	s := pathLinkPebble(t)
	for i := 0; i < 24; i++ {
		pathLinkCreateBook(t, s, fmt.Sprintf("newrow%d", i),
			fmt.Sprintf("/mnt/bigdata/books/abooks/imported/Ursula Le Guin/Book %d/x.m4b", i), nil)
	}
	stale := &staleNameStore{Store: s}
	p := New(&fakeDeps{store: stale})
	res := runPathLink(t, p, `{"dry_run":false}`)

	if res.Outcomes[authorPathLinkCreatedAndLinked] != 24 {
		t.Fatalf("created_and_linked=%d, want 24 (outcomes=%v)", res.Outcomes[authorPathLinkCreatedAndLinked], res.Outcomes)
	}
	authors, err := s.GetAllAuthors()
	if err != nil {
		t.Fatalf("GetAllAuthors: %v", err)
	}
	minted := 0
	for _, a := range authors {
		if normalizeAuthorNameForLink(a.Name) == normalizeAuthorNameForLink("Ursula Le Guin") {
			minted++
		}
	}
	if minted != 1 {
		t.Fatalf("minted %d rows for one derived name, want exactly 1", minted)
	}
	if len(res.CreatedAuthors) != 1 {
		t.Fatalf("result reports %d created authors, want 1", len(res.CreatedAuthors))
	}
	// Every book ends on the ONE row, not on an unreachable duplicate.
	want := res.CreatedAuthors[0].AuthorID
	for i := 0; i < 24; i++ {
		b, _ := s.GetBookByID(fmt.Sprintf("newrow%d", i))
		if b == nil || b.AuthorID == nil || *b.AuthorID != want {
			t.Fatalf("book newrow%d scalar = %+v, want %d", i, b, want)
		}
	}
}

// 🔴 create_missing=false mints nothing, and says so in its own bucket rather
// than reporting a creation that will not happen.
func TestAuthorPathLink_CreateMissingDisabled(t *testing.T) {
	s := pathLinkPebble(t)
	pathLinkCreateBook(t, s, "newrow", "/mnt/bigdata/books/abooks/imported/Ursula Le Guin/A Title/x.m4b", nil)

	p := New(&fakeDeps{store: s})
	res := runPathLink(t, p, `{"dry_run":false,"create_missing":false}`)
	if res.Outcomes[authorPathLinkCreateDisabled] != 1 {
		t.Fatalf("outcomes=%v, want one %s", res.Outcomes, authorPathLinkCreateDisabled)
	}
	authors, _ := s.GetAllAuthors()
	if len(authors) != 0 {
		t.Fatalf("author rows = %+v, want none minted", authors)
	}
	if b, _ := s.GetBookByID("newrow"); b == nil || b.AuthorID != nil {
		t.Fatalf("book was linked: %+v", b)
	}
}

// 🔴 A path prefix scopes the run; books outside it are not classified at all.
func TestAuthorPathLink_PathPrefixScopes(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Charles Dickens")
	f.filler(1, 4)
	f.book("in", "/mnt/bigdata/books/abooks/imported/Charles Dickens/A/x.m4b", nil)
	f.book("out", "/mnt/bigdata/books/newbooks/Charles Dickens/B/x.m4b", nil)

	p := New(&fakeDeps{store: f.store(t)})
	res := runPathLink(t, p, `{"pathPrefix":"/mnt/bigdata/books/abooks/"}`)
	if got := outcomeOf(t, res, "in").Outcome; got != authorPathLinkWouldLink {
		t.Fatalf("in-scope book: %q", got)
	}
	for _, c := range res.Changes {
		if c.BookID == "out" {
			t.Fatalf("out-of-scope book was classified: %+v", c)
		}
	}
}
