// file: internal/audiobooks/filter_ident_test.go
// version: 1.0.0
// guid: 9b7e3c52-4f18-4d0a-8e6b-2a1c5d9f7e30
// last-edited: 2026-10-11

package audiobooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newIdentFilterStore(t testing.TB) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("identfilter_" + ulid.Make().String())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// identFixture is one book per identification state among primary books,
// plus a non-primary unasked book and a trashed unasked book.
type identFixture struct {
	store   *database.PebbleStore
	svc     *AudiobookService
	byState map[database.IdentState][]string
	other   []string // non-primary and trashed: never in the default view
}

func identCandidates(n int) []json.RawMessage {
	out := make([]json.RawMessage, n)
	for i := range out {
		out[i] = json.RawMessage(fmt.Sprintf(`{"title":"Title %06d"}`, i))
	}
	return out
}

func newIdentFixture(t *testing.T) *identFixture {
	t.Helper()
	s := newIdentFilterStore(t)
	f := &identFixture{store: s, svc: NewAudiobookService(s), byState: map[database.IdentState][]string{}}
	n := 0
	create := func(mod func(*database.Book)) string {
		n++
		b := &database.Book{Title: fmt.Sprintf("Title %06d", n), FilePath: fmt.Sprintf("/lib/ident/%06d.m4b", n), Format: "m4b"}
		if mod != nil {
			mod(b)
		}
		created, err := s.CreateBook(b)
		require.NoError(t, err)
		return created.ID
	}
	status := func(v string) func(*database.Book) {
		return func(b *database.Book) { b.MetadataReviewStatus = &v }
	}

	f.byState[database.IdentUnasked] = []string{create(nil), create(nil)}
	empty := create(nil)
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: empty, FetchedAt: time.Now()}))
	f.byState[database.IdentAskedEmpty] = []string{empty}
	cand, stale := create(nil), create(nil)
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: cand, Candidates: identCandidates(2)}))
	require.NoError(t, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: stale, Candidates: identCandidates(1), Stale: true}))
	f.byState[database.IdentHasCandidates] = []string{cand, stale}
	f.byState[database.IdentApplied] = []string{create(status("matched")), create(status("audio_confirmed"))}
	f.byState[database.IdentHeld] = []string{create(status("no_match"))}

	notPrimary := false
	trashed := true
	f.other = []string{
		create(func(b *database.Book) { b.IsPrimaryVersion = &notPrimary }),
		create(func(b *database.Book) { b.MarkedForDeletion = &trashed }),
	}
	return f
}

// defaultView is the filter set the Library sends: primary versions,
// quarantined books excluded.
func defaultView(ffs ...FieldFilter) ListFilters {
	primary := true
	return ListFilters{IsPrimaryVersion: &primary, ExcludeQuarantined: true, FieldFilters: ffs}
}

func (f *identFixture) list(t *testing.T, lf ListFilters) []string {
	t.Helper()
	books, _, _, err := f.svc.GetAudiobooksPage(context.Background(), 1000, 0, "", nil, nil, lf)
	require.NoError(t, err)
	ids := make([]string, 0, len(books))
	for _, b := range books {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids
}

func (f *identFixture) want(states ...database.IdentState) []string {
	var out []string
	for _, s := range states {
		out = append(out, f.byState[s]...)
	}
	sort.Strings(out)
	if out == nil {
		out = []string{}
	}
	return out
}

func complementStates(of ...database.IdentState) []database.IdentState {
	skip := map[database.IdentState]bool{}
	for _, s := range of {
		skip[s] = true
	}
	var out []database.IdentState
	for _, s := range database.AllIdentStates {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}

func TestIdentFilter_EachState(t *testing.T) {
	f := newIdentFixture(t)
	ctx := context.Background()
	cases := map[string][]database.IdentState{
		"unasked":        {database.IdentUnasked},
		"asked_empty":    {database.IdentAskedEmpty},
		"has_candidates": {database.IdentHasCandidates},
		"applied":        {database.IdentApplied},
		"held":           {database.IdentHeld},
		"missing":        {database.IdentUnasked, database.IdentAskedEmpty},
	}
	for value, states := range cases {
		t.Run(value, func(t *testing.T) {
			lf := defaultView(FieldFilter{Field: "ident", Value: value})
			require.Equal(t, f.want(states...), f.list(t, lf))
			// The O(1) counter path and the walk agree with the page.
			n, err := f.svc.CountAudiobooksFiltered(ctx, lf)
			require.NoError(t, err)
			require.Equal(t, len(f.want(states...)), n)

			neg := defaultView(FieldFilter{Field: "ident", Value: value, Negated: true})
			require.Equal(t, f.want(complementStates(states...)...), f.list(t, neg), "-ident:%s", value)
			n, err = f.svc.CountAudiobooksFiltered(ctx, neg)
			require.NoError(t, err)
			require.Equal(t, len(f.want(complementStates(states...)...)), n)
		})
	}

	// Case-insensitive state names.
	require.Equal(t, f.want(database.IdentHeld), f.list(t, defaultView(FieldFilter{Field: "ident", Value: "HELD"})))

	// Outside the default view the restricted walk counts every scope the
	// request asks for: the non-primary unasked book joins ident:unasked.
	all := ListFilters{FieldFilters: []FieldFilter{{Field: "ident", Value: "unasked"}}}
	got := f.list(t, all)
	require.Contains(t, got, f.other[0], "non-primary book is listed without the primary filter")
	n, err := f.svc.CountAudiobooksFiltered(ctx, all)
	require.NoError(t, err)
	require.Equal(t, len(got), n)

	// The O(1) count equals the review summary's counter for the same scope.
	c := f.store.IdentCounts()
	require.True(t, c.Ready)
	n, err = f.svc.CountAudiobooksFiltered(ctx, defaultView(FieldFilter{Field: "ident", Value: "missing"}))
	require.NoError(t, err)
	require.Equal(t, int(database.IdentMissing(c.Primary)), n)
	t.Logf("ident:missing on the fixture: Library count=%d, ident_counts.missing=%d", n, database.IdentMissing(c.Primary))

	// Unknown values and patterns are refused, naming the valid values.
	for _, bad := range []FieldFilter{{Field: "ident", Value: "nope"}, {Field: "ident", Value: "/miss/"}, {Field: "ident", Value: "miss*"}} {
		err := FirstInvalidFilterValue([]FieldFilter{bad})
		require.Error(t, err, bad.Value)
		require.Contains(t, err.Error(), "has_candidates")
	}
	_, _, _, err = f.svc.GetAudiobooksPage(ctx, 10, 0, "", nil, nil, defaultView(FieldFilter{Field: "ident", Value: "nope"}))
	require.Error(t, err)

	// Two ident: filters AND: missing minus unasked is asked_empty.
	both := defaultView(FieldFilter{Field: "ident", Value: "missing"}, FieldFilter{Field: "ident", Value: "unasked", Negated: true})
	require.Equal(t, f.want(database.IdentAskedEmpty), f.list(t, both))
}

func TestIdentFilter_CombinedWithOtherFilters(t *testing.T) {
	f := newIdentFixture(t)
	ctx := context.Background()
	unasked := f.byState[database.IdentUnasked]

	// With a title filter: only the unasked book whose title matches.
	b, err := f.store.GetBookByID(unasked[0])
	require.NoError(t, err)
	lf := defaultView(FieldFilter{Field: "ident", Value: "missing"}, FieldFilter{Field: "title", Value: b.Title, Quoted: true})
	require.Equal(t, []string{unasked[0]}, f.list(t, lf))
	n, err := f.svc.CountAudiobooksFiltered(ctx, lf)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// With a tag: the tag set ANDs with the ident set.
	require.NoError(t, f.store.AddBookTag(unasked[1], "tag-ident"))
	tagged := defaultView(FieldFilter{Field: "ident", Value: "missing"})
	tagged.Tags = []string{"tag-ident"}
	require.Equal(t, []string{unasked[1]}, f.list(t, tagged))
	n, err = f.svc.CountAudiobooksFiltered(ctx, tagged)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// With a caller id restriction: intersected, never replaced.
	restricted := defaultView(FieldFilter{Field: "ident", Value: "applied"})
	restricted.RestrictToIDs = map[string]struct{}{f.byState[database.IdentApplied][0]: {}, unasked[0]: {}}
	require.Equal(t, []string{f.byState[database.IdentApplied][0]}, f.list(t, restricted))
	require.Len(t, restricted.RestrictToIDs, 2, "the caller's set is not written through")

	// A state change is seen by the next request (nothing cached it).
	require.NoError(t, f.store.PutMetadataCache(&database.MetadataCandidateCache{BookID: unasked[0], Candidates: identCandidates(1)}))
	require.Equal(t, []string{unasked[1]}, f.list(t, defaultView(FieldFilter{Field: "ident", Value: "unasked"})))

	// MatchingBookIDs (bulk selection) resolves ident: too.
	ids, err := f.svc.MatchingBookIDs(ctx, "", nil, nil, defaultView(FieldFilter{Field: "ident", Value: "held"}))
	require.NoError(t, err)
	require.Equal(t, f.byState[database.IdentHeld], ids)

	// A filter that reached the row matcher unresolved matches nothing.
	require.False(t, matchesFieldFilters(database.Book{ID: "x"}, []FieldFilter{{Field: "ident", Value: "unasked"}}))
}

// notReadyIdentStore is a store whose identification index is being built.
type notReadyIdentStore struct{ *database.PebbleStore }

func (notReadyIdentStore) IdentCounts() database.IdentCounts { return database.IdentCounts{} }
func (notReadyIdentStore) IdentBookIDs([]database.IdentState) (map[string]struct{}, error) {
	return nil, database.ErrIdentIndexBuilding
}

func TestIdentFilter_BuildingReturnsErrIdentIndexBuilding(t *testing.T) {
	f := newIdentFixture(t)
	ctx := context.Background()
	svc := NewAudiobookService(notReadyIdentStore{f.store})
	_, _, _, err := svc.GetAudiobooksPage(ctx, 10, 0, "", nil, nil, defaultView(FieldFilter{Field: "ident", Value: "missing"}))
	require.ErrorIs(t, err, database.ErrIdentIndexBuilding)
	_, err = svc.CountAudiobooksFiltered(ctx, defaultView(FieldFilter{Field: "ident", Value: "missing"}))
	require.ErrorIs(t, err, database.ErrIdentIndexBuilding, "the O(1) path refuses too")
	_, err = svc.CountAudiobooksFiltered(ctx, ListFilters{FieldFilters: []FieldFilter{{Field: "ident", Value: "missing"}}})
	require.ErrorIs(t, err, database.ErrIdentIndexBuilding, "and the walk")
	_, err = svc.MatchingBookIDs(ctx, "", nil, nil, defaultView(FieldFilter{Field: "ident", Value: "held"}))
	require.ErrorIs(t, err, database.ErrIdentIndexBuilding)
	// A request without ident: is unaffected.
	_, _, _, err = svc.GetAudiobooksPage(ctx, 10, 0, "", nil, nil, defaultView())
	require.NoError(t, err)
}

func TestAsinFilter_MemdbPath(t *testing.T) {
	s := newIdentFilterStore(t)
	svc := NewAudiobookService(s)
	asin, other := "B0SYNTH001", "B0SYNTH002"
	with, err := s.CreateBook(&database.Book{Title: "Title 000001", FilePath: "/lib/asin/1.m4b", Format: "m4b", ASIN: &asin})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "Title 000002", FilePath: "/lib/asin/2.m4b", Format: "m4b", ASIN: &other})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "Title 000003", FilePath: "/lib/asin/3.m4b", Format: "m4b"})
	require.NoError(t, err)

	// The memdb-resident row carries the ASIN: the walker's predicate sees
	// it on the *Book memdb hands it (stripBookForMemdb does not strip it).
	seen := map[string]string{}
	_, err = s.GetAllBookSummariesFiltered(0, 0, database.BookSummaryFilter{Predicate: func(b *database.Book) bool {
		if b.ASIN != nil {
			seen[b.ID] = *b.ASIN
		}
		return true
	}})
	require.NoError(t, err)
	require.Equal(t, asin, seen[with.ID], "ASIN present on the memdb-resident book")

	books, _, _, err := svc.GetAudiobooksPage(context.Background(), 50, 0, "", nil, nil,
		defaultView(FieldFilter{Field: "asin", Value: asin, Quoted: true}))
	require.NoError(t, err)
	require.Len(t, books, 1)
	require.Equal(t, with.ID, books[0].ID)

	books, _, _, err = svc.GetAudiobooksPage(context.Background(), 50, 0, "", nil, nil,
		defaultView(FieldFilter{Field: "asin", Value: "B0SYNTH*"}))
	require.NoError(t, err)
	require.Len(t, books, 2, "a glob matches both ASINs and not the book without one")
}

// BenchmarkIdentMissingFirstPage_40k measures the Library's ident:missing
// request over 40,000 primary books (every third holding candidates): the
// first page of 50 (the index's membership read plus the restricted walk)
// and the total (the O(1) counter read), as buildAudiobookListResponse makes
// them.
func BenchmarkIdentMissingFirstPage_40k(b *testing.B) {
	const books = 40000
	s := newIdentFilterStore(b)
	began := time.Now()
	for i := range books {
		bk, err := s.CreateBook(&database.Book{Title: fmt.Sprintf("Title %06d", i), FilePath: fmt.Sprintf("/lib/bench/%06d.m4b", i), Format: "m4b"})
		require.NoError(b, err)
		if i%3 == 0 {
			require.NoError(b, s.PutMetadataCache(&database.MetadataCandidateCache{BookID: bk.ID, Candidates: identCandidates(1)}))
		}
	}
	b.Logf("seeded %d books in %s", books, time.Since(began).Round(time.Millisecond))
	svc := NewAudiobookService(s)
	lf := defaultView(FieldFilter{Field: "ident", Value: "missing"})
	ctx := context.Background()

	countStart := time.Now()
	n, err := svc.CountAudiobooksFiltered(ctx, lf)
	countTook := time.Since(countStart)
	require.NoError(b, err)

	b.ResetTimer()
	var page []database.Book
	for b.Loop() {
		page, _, _, err = svc.GetAudiobooksPage(ctx, 50, 0, "", nil, nil, lf)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	perPage := time.Duration(b.Elapsed().Nanoseconds() / int64(max(b.N, 1)))
	b.Logf("ident:missing over %d primary books: count=%d (counter read %s), first page of %d in %s",
		books, n, countTook, len(page), perPage)
}
