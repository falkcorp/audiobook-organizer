// file: internal/audiobooks/service_query_search_whole_test.go
// version: 1.0.4
// guid: 8b4e1f27-3a96-4c05-b7d2-6e0a9c5f1d38
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// wholeSendService is a service over a real index of six books, with the viewer
// "alice" having finished b1, b3 and b5 and being part-way through b2. The tags
// are chosen so that a match by WORD gives a different answer from the exact match
// the tag: term must give: "dark-knight" holds the word "knight" another book is
// tagged with, "batman-beyond" holds "batman", and "the-batman" reduces to
// "batman" once the stop word is gone.
//
//	b1  Hush                noir, gotham, dark-knight   finished
//	b2  Year One            noir, batman                in progress
//	b3  Knightfall          gotham, knight              finished
//	b4  The Long Halloween  the-batman                  unstarted
//	b5  Batman Beyond       batman-beyond               finished
//	b6  Zero Year           (no tags)                   unstarted
func wholeSendService(t *testing.T) *AudiobookService {
	t.Helper()
	return wholeSendServiceOver(t, func(st database.Store) database.Store { return st })
}

// wholeSendStatuses is alice's state in the whole-send fixture, by book id.
var wholeSendStatuses = map[string]string{
	"b1": database.UserBookStatusFinished,
	"b2": database.UserBookStatusInProgress,
	"b3": database.UserBookStatusFinished,
	"b5": database.UserBookStatusFinished,
}

// wholeSendServiceOver is wholeSendService with the store wrapped, so a test can
// count the calls the search makes.
func wholeSendServiceOver(t *testing.T, wrap func(database.Store) database.Store) *AudiobookService {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	docs := []search.BookDocument{
		{BookID: "b1", Title: "Hush", Author: "kyle", Tags: []string{"noir", "gotham", "dark-knight"}},
		{BookID: "b2", Title: "Year One", Author: "kyle", Tags: []string{"noir", "batman"}},
		{BookID: "b3", Title: "Knightfall", Author: "kyle", Tags: []string{"gotham", "knight"}},
		{BookID: "b4", Title: "The Long Halloween", Author: "kyle", Tags: []string{"the-batman"}},
		{BookID: "b5", Title: "Batman Beyond", Author: "kyle", Tags: []string{"batman-beyond"}},
		{BookID: "b6", Title: "Zero Year", Author: "kyle"},
	}
	require.NoError(t, idx.IndexBookBatch(docs))
	byTag := map[string][]string{}
	for _, d := range docs {
		for _, tag := range d.Tags {
			byTag[tag] = append(byTag[tag], d.BookID)
		}
	}
	st := mocks.NewMockStore(t)
	// The exact lookup the tags[] parameter and the tag chips use: whole tag, case
	// folded (the real store folds and trims), never a word of one.
	st.EXPECT().GetBooksByTag(mock.Anything).RunAndReturn(func(tag string) ([]string, error) {
		return append([]string(nil), byTag[strings.ToLower(strings.TrimSpace(tag))]...), nil
	}).Maybe()
	st.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		out := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			out = append(out, database.Book{ID: id})
		}
		return out, nil
	}).Maybe()
	st.EXPECT().GetUserBookState("alice", mock.Anything).RunAndReturn(func(_, id string) (*database.UserBookState, error) {
		if s, ok := wholeSendStatuses[id]; ok {
			return &database.UserBookState{Status: s}, nil
		}
		return nil, nil
	}).Maybe()
	st.EXPECT().SearchBooks(mock.Anything, mock.Anything, mock.Anything).
		Return([]database.Book{}, nil).Maybe()
	svc := NewAudiobookService(wrap(st))
	svc.SetSearchIndex(completedIndex(t, idx))
	return svc
}

// callCountingStore records the store calls a per-user search makes: the point
// reads of a viewer's state, and the size of every batch of books it loads.
type callCountingStore struct {
	database.Store
	mu         sync.Mutex
	stateReads int
	listReads  int
	hydrated   []int
}

func (c *callCountingStore) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	c.mu.Lock()
	c.stateReads++
	c.mu.Unlock()
	return c.Store.GetUserBookState(userID, bookID)
}

func (c *callCountingStore) GetBooksByIDs(ids []string) ([]database.Book, error) {
	c.mu.Lock()
	c.hydrated = append(c.hydrated, len(ids))
	c.mu.Unlock()
	return c.Store.GetBooksByIDs(ids)
}

// listingCountingStore is a callCountingStore that can also list a viewer's rows in
// one read (database.UserBookStateLister), as the Pebble store can.
type listingCountingStore struct{ *callCountingStore }

func (l listingCountingStore) ListUserBookStates(_ context.Context, userID string) (map[string]*database.UserBookState, error) {
	l.mu.Lock()
	l.listReads++
	l.mu.Unlock()
	rows := map[string]*database.UserBookState{}
	if userID == "alice" {
		for id, st := range wholeSendStatuses {
			rows[id] = &database.UserBookState{Status: st}
		}
	}
	return rows, nil
}

func wholeSendIDs(t *testing.T, svc *AudiobookService, q string) ([]string, error) {
	t.Helper()
	got, err := svc.GetAudiobooks(context.Background(), 50, 0, q, nil, nil, ListFilters{UserID: "alice"})
	ids := []string{}
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids, err
}

// N-B1: the web search box sends a per-user query with an AND, an OR or a group
// whole in search=, and no longer sends its tag: terms as tags[]. A tag: term is
// an exact tag filter applied outside the index; the ROWS are asserted here, not
// the status: a green test of an empty answer is what let the first version of
// this through.
func TestGetAudiobooks_WholeSentQueryWithATagTermAnswersItsRows(t *testing.T) {
	svc := wholeSendService(t)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		// finished AND noir: only b1 is both (b2 is noir but in progress; b3 is
		// finished but not noir), so the answer needs BOTH terms to apply.
		{"read_status:finished AND tag:noir", []string{"b1"}},
		{"(read_status:finished tag:noir)", []string{"b1"}},
		{"(read_status:finished tag:Noir)", []string{"b1"}},
		{"read_status:finished && tag:gotham", []string{"b1", "b3"}},
		{"(read_status:in_progress tag:noir)", []string{"b2"}},
		{"(read_status:finished tag:camelot)", []string{}},
		// the same terms without boolean structure, the way the box sends them
		{"read_status:finished tag:noir", []string{"b1"}},
		// two tags are both required, and the rest of the query still applies
		{"tag:noir tag:gotham", []string{"b1"}},
		{"tag:noir tag:gotham read_status:finished", []string{"b1"}},
		{"tag:gotham title:Hush", []string{"b1"}},
		{"tag:gotham title:Year", []string{}},
		{"tag:gotham (author:kyle OR title:Hush)", []string{"b1", "b3"}},
		// N1: what is left next to the tag is noise (a stop word, bare punctuation),
		// dropped as it is next to any real term; the tag alone filters
		{"the tag:noir", []string{"b1", "b2"}},
		{"of the tag:noir", []string{"b1", "b2"}},
		{"- tag:noir", []string{"b1", "b2"}},
		{"(tag:noir OR)", []string{"b1", "b2"}},
		{"the read_status:finished tag:noir", []string{"b1"}},
		{"the Hush tag:noir", []string{"b1"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := wholeSendIDs(t, svc, tc.query)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// R-B1: the index holds a tag as words, so a term on it matches by word:
// tag:"dark-knight" would also find the book tagged "knight", tag:batman the books
// tagged "batman-beyond" and "the-batman" ("the" is a stop word). tag: is the
// exact match the tags[] parameter and the chips give: the whole tag, case folded.
func TestGetAudiobooks_TagTermIsExactNotByWord(t *testing.T) {
	svc := wholeSendService(t)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{`tag:"dark-knight"`, []string{"b1"}},
		{`tag:knight`, []string{"b3"}},
		{`tag:"Dark-Knight"`, []string{"b1"}},
		{`tag:batman`, []string{"b2"}},
		{`tag:"batman-beyond"`, []string{"b5"}},
		{`tag:"the-batman"`, []string{"b4"}},
		{`tag:"the"`, []string{}},
		{`tag:dark`, []string{}},
		{`tag:beyond`, []string{}},
		// the same terms in the whole-sent shapes the box produces
		{`read_status:finished AND tag:knight`, []string{"b3"}},
		{`read_status:finished AND tag:batman`, []string{}},
		{`(read_status:in_progress tag:batman)`, []string{"b2"}},
		{`(read_status:finished tag:"batman-beyond")`, []string{"b5"}},
		{`(read_status:unstarted tag:"the-batman")`, []string{"b4"}},
		{`(read_status:unstarted tag:batman)`, []string{}},
		{`read_status:finished tag:"dark-knight" tag:noir`, []string{"b1"}},
		{`tag:noir tag:batman`, []string{"b2"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := wholeSendIDs(t, svc, tc.query)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A tag: scope and the caller's own scope (author or series membership, the
// restrictIDs of a scoped search) are ANDed, and the match count is the number of
// matches, not of the page.
func TestSearchWithBleve_TagTermIsAndedWithTheCallersScopeAndCountsExactly(t *testing.T) {
	svc := wholeSendService(t)
	ids := func(books []database.Book) []string {
		out := []string{}
		for _, b := range books {
			out = append(out, b.ID)
		}
		sort.Strings(out)
		return out
	}
	books, total, err := svc.searchWithBleve(context.Background(), "tag:noir", 50, 0, "alice", []string{"b2", "b3", "b4"}, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, []string{"b2"}, ids(books))
	assert.Equal(t, 1, total)

	books, total, err = svc.searchWithBleve(context.Background(), "tag:noir read_status:finished", 50, 0, "alice", []string{"b2", "b3"}, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, []string{}, ids(books), "b2 is noir but in progress; b3 is finished but not noir")
	assert.Equal(t, 0, total)

	books, total, err = svc.searchWithBleve(context.Background(), "tag:noir", 50, 0, "alice", []string{"b3", "b4"}, hydratePage)
	require.NoError(t, err)
	assert.Empty(t, books)
	assert.Equal(t, 0, total, "no book in scope carries the tag")

	books, total, err = svc.searchWithBleve(context.Background(), "tag:noir", 1, 0, "alice", nil, hydratePage)
	require.NoError(t, err)
	assert.Len(t, books, 1, "one row of the page")
	assert.Equal(t, 2, total, "two books match, however long the page is")
}

// A tag: term that cannot be applied exactly is refused with a message, never
// answered as a word match or (under NOT) as the whole library.
func TestGetAudiobooks_GroupedOrNegatedTagTermIsRefused(t *testing.T) {
	svc := wholeSendService(t)
	for _, q := range []string{
		"tag:noir OR tag:gotham",
		"read_status:finished (tag:noir OR author:kyle)",
		"tag:(noir|gotham)",
		"-tag:noir",
		`read_status:finished -tag:"batman"`,
		"NOT (tag:noir author:kyle)",
		"tag:noir*",
		"tag:noir~",
		// N2: a pattern or a modifier on a tag name is refused, not searched as text
		"tag:no*ir",
		"tag:noir~1",
		"tag:x^",
		`tag:"noir"^2`,
		`read_status:finished tag:"noir"~`,
	} {
		t.Run(q, func(t *testing.T) {
			_, err := wholeSendIDs(t, svc, q)
			require.Error(t, err)
			assert.True(t, search.IsTagTermError(err), "got %v", err)
			assert.True(t, search.IsQueryTermError(err))
			assert.NotEmpty(t, search.QueryTermReason(err))
			assert.NotContains(t, search.QueryTermReason(err), "search: ", "the reason has no wrapper")
		})
	}
	// The pre-check handlers run says the same.
	assert.Error(t, SearchPerUserProblem("title:Hush OR tag:noir"))
	assert.NoError(t, SearchPerUserProblem("title:Hush tag:noir"))
}

// NIT 3: text the DSL parser rejects used to fall to a whole-text substring match
// over title and author, which shows no rows for a listening-state term and says
// nothing. A query that writes a per-user term and does not parse is refused with
// a message (a *PerUserValueError, which every handler answers with a 400); one
// that writes none keeps the substring fallback.
func TestGetAudiobooks_UnparsablePerUserQueryIsRefusedNotSubstringMatched(t *testing.T) {
	svc := wholeSendService(t)
	for _, q := range []string{
		"read_status:finished OR",
		"(read_status:finished author:kyle",
		"read_status:finished AND (",
		"progress_pct:>50 )",
		"last_played:>-30d OR (",
	} {
		t.Run(q, func(t *testing.T) {
			_, err := wholeSendIDs(t, svc, q)
			require.Error(t, err)
			assert.True(t, search.IsPerUserQueryError(err), "got %v", err)
			assert.Contains(t, search.PerUserQueryReason(err), "could not be read")
		})
	}

	for _, q := range []string{
		"The (Long Halloween",
		"Hush OR",
		"the read_status of things: (",
	} {
		t.Run("fallback "+q, func(t *testing.T) {
			_, err := wholeSendIDs(t, svc, q)
			assert.NoError(t, err, "no per-user term was written, so the substring fallback stays")
		})
	}
}

// The same refusal while the index is nil or rebuilding: the substring fallbacks
// are guarded by SearchPerUserProblem, which now judges unparsable text too.
func TestSearchPerUserProblem_JudgesUnparsableText(t *testing.T) {
	for _, tc := range []struct {
		q       string
		problem bool
	}{
		{"read_status:finished OR", true},
		{"(progress_pct:>50", true},
		{"READ_STATUS : finished OR (", true},
		{"The (Long Halloween", false},
		{"title:(Hush", false},
		{"read_status", false},
		{"", false},
	} {
		t.Run(tc.q, func(t *testing.T) {
			err := SearchPerUserProblem(tc.q)
			assert.Equal(t, tc.problem, err != nil, "got %v", err)
		})
	}
}

// S1: a per-user search over a scope (here the tag's books; an author or a series is
// the same shape) used to hydrate EVERY candidate book and read one state row each
// before it filtered anything, so a tag on most of the library cost a book and a row
// per book on every page request. The candidates are now filtered by id first, and only
// the page that survives is loaded. The store calls are counted: a regression to
// hydrating every hit fails here. A scope this small is judged by one point read per
// candidate (perUserPointReadMax); the scan branch is
// TestIdsMatchingPerUser_AScopeAboveTheThresholdIsOneScan.
func TestSearchWithBleve_ASmallPerUserSearchReadsOneRowPerCandidateAndLoadsOnlyThePage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		query     string
		limit     int
		offset    int
		wantTotal int
		wantPage  int
		wantReads int // candidates: one point read each
	}{
		// gotham = b1, b3; both finished.
		{"tag scope, first page", "read_status:finished tag:gotham", 1, 0, 2, 1, 2},
		{"tag scope, second page", "read_status:finished tag:gotham", 1, 1, 2, 1, 2},
		{"tag scope, offset past the matches", "read_status:finished tag:gotham", 1, 5, 2, 0, 2},
		// no tag: every book is a candidate, three are finished.
		{"no scope", "read_status:finished", 2, 0, 3, 2, 6},
		// the filter rejects some of the tag's books: noir = b1 (finished), b2 (not).
		{"filter rejects part of the scope", "read_status:finished tag:noir", 5, 0, 1, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cs *callCountingStore
			svc := wholeSendServiceOver(t, func(st database.Store) database.Store {
				cs = &callCountingStore{Store: st}
				return listingCountingStore{cs}
			})
			books, total, err := svc.searchWithBleve(context.Background(), tc.query, tc.limit, tc.offset, "alice", nil, hydratePage)
			require.NoError(t, err)
			assert.Equal(t, tc.wantTotal, total)
			assert.Len(t, books, tc.wantPage)

			assert.Equal(t, tc.wantReads, cs.stateReads, "one point read per candidate id")
			assert.Zero(t, cs.listReads, "a scope this small does not pay for a scan of every row the viewer has")
			pageLoads := 0
			for _, n := range cs.hydrated {
				pageLoads += n
			}
			assert.Equal(t, tc.wantPage, pageLoads, "only the page's books are loaded, not every candidate")
		})
	}
}

// A store that cannot list a viewer's rows reads one row per candidate ID, as
// before, but still before any book is loaded, so the page is all that is hydrated.
func TestSearchWithBleve_APerUserSearchOnAStoreWithoutBulkStateStillLoadsOnlyThePage(t *testing.T) {
	var cs *callCountingStore
	svc := wholeSendServiceOver(t, func(st database.Store) database.Store {
		cs = &callCountingStore{Store: st}
		return cs
	})
	books, total, err := svc.searchWithBleve(context.Background(), "read_status:finished tag:gotham", 1, 0, "alice", nil, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, books, 1)
	assert.Equal(t, 2, cs.stateReads, "one point read per candidate id (b1, b3)")
	assert.Equal(t, []int{1}, cs.hydrated, "one batch, the size of the page")
}

// N3: SearchHasPerUserFilters gates caches that are not keyed by user, so it must
// answer from the fields the query names, not from whether the query translates.
// A per-user query the translator refuses (a tag: term under an OR, a bad date) is
// still a per-user query.
func TestSearchHasPerUserFilters_IsDecidedByTheFieldNamesNotByTranslation(t *testing.T) {
	for q, want := range map[string]bool{
		"read_status:finished":                          true,
		"read_status:finished tag:noir":                 true,
		"read_status:finished (title:Hush OR tag:noir)": true, // refused by the search, still per-user
		"read_status:finished OR tag:noir":              true,
		"-read_status:finished":                         true,
		"Read_Status:finished":                          true, // the search refuses a capitalised name; it is still per-user
		"READ_STATUS:(finished|in_progress)":            true,
		"Progress_Pct:>50":                              true,
		"read_status:(finished|in_progress)":            true,
		"progress_pct:>50":                              true,
		"last_played:>30d":                              true, // no sign: refused, still per-user
		"title:Hush tag:noir":                           false,
		"tag:noir OR title:Hush":                        false,
		"":                                              false,
		"title:(Hush":                                   false, // does not parse
	} {
		assert.Equal(t, want, SearchHasPerUserFilters(q), q)
	}
}

// stateStub is a store that serves one viewer's rows, counts the reads and can be made
// to fail, for the branches of idsMatchingPerUser.
type stateStub struct {
	database.Store
	mu       sync.Mutex
	points   int
	lists    int
	listErr  error
	pointErr map[string]error
	onPoint  func(id string)
	onList   func()
	finished map[string]bool
}

func (s *stateStub) GetUserBookState(userID, bookID string) (*database.UserBookState, error) {
	s.mu.Lock()
	s.points++
	hook := s.onPoint
	err := s.pointErr[bookID]
	s.mu.Unlock()
	if hook != nil {
		hook(bookID)
	}
	if err != nil {
		return nil, err
	}
	if userID == "alice" && s.finished[bookID] {
		return &database.UserBookState{Status: database.UserBookStatusFinished}, nil
	}
	return nil, nil
}

func (s *stateStub) ListUserBookStates(_ context.Context, userID string) (map[string]*database.UserBookState, error) {
	s.mu.Lock()
	s.lists++
	hook := s.onList
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	if s.listErr != nil {
		return nil, s.listErr
	}
	rows := map[string]*database.UserBookState{}
	if userID == "alice" {
		for id := range s.finished {
			rows[id] = &database.UserBookState{Status: database.UserBookStatusFinished}
		}
	}
	return rows, nil
}

// stubIDs is n book ids; every third one is finished by alice in the stub.
func stubIDs(n int) ([]string, map[string]bool) {
	ids := make([]string, n)
	fin := map[string]bool{}
	for i := range ids {
		ids[i] = fmt.Sprintf("b%04d", i)
		if i%3 == 0 {
			fin[ids[i]] = true
		}
	}
	return ids, fin
}

func finishedFilters(t *testing.T) []search.PerUserFilter {
	t.Helper()
	ast, err := search.ParseQuery("read_status:finished")
	require.NoError(t, err)
	tr, err := search.TranslateQuery(ast, time.Now())
	require.NoError(t, err)
	require.Len(t, tr.PerUser, 1)
	return tr.PerUser
}

// N-S1a: the bulk scan costs the viewer's whole row count whatever the candidate
// count (1.6 ms at 1,000 rows, 30 ms at 20,000), a point read a few microseconds, so
// a narrow search must not pay for the scan. The branch is counted on both sides of
// perUserPointReadMax, with the same answer on both.
func TestIdsMatchingPerUser_AScopeAboveTheThresholdIsOneScan(t *testing.T) {
	for _, tc := range []struct {
		name       string
		n          int
		wantPoints int
		wantLists  int
	}{
		{"one candidate", 1, 1, 0},
		{"at the threshold", perUserPointReadMax, perUserPointReadMax, 0},
		{"one above the threshold", perUserPointReadMax + 1, 0, 1},
		{"a broad scope", 3000, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ids, fin := stubIDs(tc.n)
			stub := &stateStub{finished: fin}
			svc := NewAudiobookService(stub)
			got, err := svc.idsMatchingPerUser(context.Background(), "alice", ids, finishedFilters(t), time.Now())
			require.NoError(t, err)
			assert.Equal(t, tc.wantPoints, stub.points, "point reads")
			assert.Equal(t, tc.wantLists, stub.lists, "scans")
			want := []string{}
			for _, id := range ids {
				if fin[id] {
					want = append(want, id)
				}
			}
			assert.Equal(t, want, got, "the same ids, in order, whichever way they were read")
		})
	}
}

// A scan that fails (one undecodable row fails the whole scan by design) falls back
// to a point read per id, and the answer is the same as the scan's. The fallback is
// loud and is not an error.
func TestIdsMatchingPerUser_AFailedScanFallsBackToPointReads(t *testing.T) {
	ids, fin := stubIDs(perUserPointReadMax + 50)
	stub := &stateStub{finished: fin, listErr: errors.New("undecodable row")}
	svc := NewAudiobookService(stub)
	got, err := svc.idsMatchingPerUser(context.Background(), "alice", ids, finishedFilters(t), time.Now())
	require.NoError(t, err)
	assert.Equal(t, 1, stub.lists, "the scan was tried once")
	assert.Equal(t, len(ids), stub.points, "then every id was read on its own")
	assert.Len(t, got, (len(ids)+2)/3)
}

// N7: the fallback is affordable only for a bounded candidate list. A scoped search has
// no cap on its candidates, so past postFilterWindow() ids a failed scan is answered
// with its error (naming the cause), not with a point read per id on every request.
// Exactly at the window it still falls back.
func TestIdsMatchingPerUser_AFailedScanOnAHugeScopeIsAnErrorNotAThousandPointReads(t *testing.T) {
	restore := SetSearchPostFilterWindowForTesting(400)
	t.Cleanup(restore)
	scanErr := fmt.Errorf("%w: alice/b0007: bad json", database.ErrUserBookStateUndecodable)

	t.Run("one above the window", func(t *testing.T) {
		ids, fin := stubIDs(401)
		stub := &stateStub{finished: fin, listErr: scanErr}
		_, err := NewAudiobookService(stub).idsMatchingPerUser(context.Background(), "alice", ids, finishedFilters(t), time.Now())
		require.Error(t, err)
		assert.ErrorIs(t, err, database.ErrUserBookStateUndecodable)
		assert.Equal(t, 1, stub.lists)
		assert.Zero(t, stub.points, "no slow path of one read per id")
	})
	t.Run("exactly the window still falls back", func(t *testing.T) {
		ids, fin := stubIDs(400)
		stub := &stateStub{finished: fin, listErr: scanErr}
		got, err := NewAudiobookService(stub).idsMatchingPerUser(context.Background(), "alice", ids, finishedFilters(t), time.Now())
		require.NoError(t, err)
		assert.Equal(t, 400, stub.points)
		assert.Len(t, got, (400+2)/3)
	})
	t.Run("a cancelled request is its own error, not the scan's", func(t *testing.T) {
		ids, fin := stubIDs(401)
		ctx, cancel := context.WithCancel(context.Background())
		stub := &stateStub{finished: fin, listErr: scanErr}
		stub.onList = cancel
		_, err := NewAudiobookService(stub).idsMatchingPerUser(ctx, "alice", ids, finishedFilters(t), time.Now())
		require.ErrorIs(t, err, context.Canceled)
		assert.NotErrorIs(t, err, database.ErrUserBookStateUndecodable)
	})
}

// An unreadable point row is judged as an unstarted book, with a warning, and the
// rest of the page is served (Decision 5; the evaluator fails closed, see
// idsMatchingPerUser).
func TestIdsMatchingPerUser_AnUnreadableRowIsJudgedUnstartedNotFailed(t *testing.T) {
	ids, fin := stubIDs(9) // b0000, b0003, b0006 finished
	stub := &stateStub{finished: fin, pointErr: map[string]error{"b0003": errors.New("undecodable row")}}
	svc := NewAudiobookService(stub)
	got, err := svc.idsMatchingPerUser(context.Background(), "alice", ids, finishedFilters(t), time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"b0000", "b0006"}, got, "b0003 is judged as the zero-value state, so it is not finished")
}

// A request that has gone away stops the reads: before the scan, after a failed scan,
// and between point reads (the loop used to run on to the end).
func TestIdsMatchingPerUser_ACancelledRequestStopsReading(t *testing.T) {
	t.Run("cancelled before the scan", func(t *testing.T) {
		ids, fin := stubIDs(perUserPointReadMax + 1)
		stub := &stateStub{finished: fin}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewAudiobookService(stub).idsMatchingPerUser(ctx, "alice", ids, finishedFilters(t), time.Now())
		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, stub.lists, "no scan for a request that is already gone")
		assert.Zero(t, stub.points)
	})
	t.Run("cancelled during a failed scan", func(t *testing.T) {
		ids, fin := stubIDs(perUserPointReadMax + 1)
		ctx, cancel := context.WithCancel(context.Background())
		stub := &stateStub{finished: fin, listErr: context.Canceled}
		stub.onList = cancel // the request goes away while the scan runs, and the scan fails
		_, err := NewAudiobookService(stub).idsMatchingPerUser(ctx, "alice", ids, finishedFilters(t), time.Now())
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, stub.lists)
		assert.Zero(t, stub.points, "a cancelled scan does not fall back to a thousand point reads")
	})
	t.Run("cancelled between point reads", func(t *testing.T) {
		ids, fin := stubIDs(100)
		ctx, cancel := context.WithCancel(context.Background())
		stub := &stateStub{finished: fin}
		stub.onPoint = func(id string) {
			if id == "b0009" {
				cancel()
			}
		}
		_, err := NewAudiobookService(stub).idsMatchingPerUser(ctx, "alice", ids, finishedFilters(t), time.Now())
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 10, stub.points, "the loop stops at the next id after the cancellation")
	})
}

// N-B1 (server half): a /regex/ field value that reaches search= is refused, never
// answered as the words between the slashes (title:/hush|knight/ found Hush only) or
// as a substring match over the whole raw text (title:/^(the|a) /, 0 rows). The
// refusal is a search.RegexValueError, which every handler answers with a 400.
func TestGetAudiobooks_ARegexValueInSearchIsRefusedNotAnsweredAsWords(t *testing.T) {
	svc := wholeSendService(t)
	for _, q := range []string{
		"title:/hush|knight/",
		"title:/^(the|a) /",
		"-title:/^(?:z)/",
		"description:/(?i)gotham/",
		"metadata:/(audible|google)/",
		"title:/hush/ OR title:knight",
		"read_status:finished title:/hush|knight/",
		"read_status:finished title:/^(the|a) /",
	} {
		t.Run(q, func(t *testing.T) {
			_, err := wholeSendIDs(t, svc, q)
			require.Error(t, err)
			assert.True(t, search.IsRegexValueError(err), "got %v", err)
			assert.True(t, search.IsQueryTermError(err))
		})
	}
	// the substring fallback of a title that merely has punctuation in it is kept
	_, err := wholeSendIDs(t, svc, "The (Long Halloween")
	assert.NoError(t, err)
}

func TestSearchPerUserProblem_RefusesARegexValueBeforeTheSubstringFallback(t *testing.T) {
	for q, refused := range map[string]bool{
		"title:/^(the|a) /":                   true,
		"title:/hush|knight/":                 true,
		"read_status:finished title:/a(b)/":   true,
		"metadata:/(audible|google)/":         true,
		"The (Long Halloween":                 false,
		`title:"/^(the|a) /"`:                 false,
		"title:Batman(1989)":                  false,
		"http://example.invalid/Batman (1989": false,
	} {
		err := SearchPerUserProblem(q)
		assert.Equal(t, refused, err != nil, "%s: got %v", q, err)
		if refused {
			assert.True(t, search.IsRegexValueError(err), q)
		}
	}
}
