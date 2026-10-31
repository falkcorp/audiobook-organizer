// file: internal/audiobooks/service_query_search_mixed_test.go
// version: 1.0.0
// guid: b4d7e2a9-6f13-4c58-9e0b-2a5c8d1f7e34
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture is wholeSendService: alice finished b1 (Hush), b3 (Knightfall) and b5
// (Batman Beyond), is part-way through b2 (Year One), and has no row for b4 (The Long
// Halloween) or b6 (Zero Year).

// A per-user term under an OR with a sibling, or under a NOT over a group, is
// answered as written. title:Hush OR read_status:finished used to be an AND (b1
// alone), then a 400.
func TestGetAudiobooks_PerUserTermUnderAnOrOrAGroupedNotIsEvaluatedAsWritten(t *testing.T) {
	svc := wholeSendService(t)
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"title:Hush OR read_status:finished", []string{"b1", "b3", "b5"}},
		{"read_status:finished OR title:Halloween", []string{"b1", "b3", "b4", "b5"}},
		{"title:Halloween OR read_status:in_progress", []string{"b2", "b4"}},
		{"read_status:finished OR read_status:in_progress", []string{"b1", "b2", "b3", "b5"}},
		// narrowed by a sibling conjunct, in a group or beside it
		{"(title:Hush OR read_status:in_progress) AND author:kyle", []string{"b1", "b2"}},
		{"(title:Year OR read_status:finished) AND title:Knightfall", []string{"b3"}},
		// a term a book with no row satisfies
		{"title:Zero OR read_status:unstarted", []string{"b4", "b6"}},
		{"title:Hush OR -read_status:finished", []string{"b1", "b2", "b4", "b6"}},
		// a NOT over a group: "not both", where the flat path answered "neither"
		{"-(read_status:finished author:kyle)", []string{"b2", "b4", "b6"}},
		{"NOT (read_status:finished OR title:Year)", []string{"b4"}}, // "Zero Year" is excluded by its title
		// with a tag: term, which scopes the rest of the query
		{"tag:gotham (title:Hush OR read_status:finished)", []string{"b1", "b3"}},
		{"tag:noir (title:Year OR read_status:finished)", []string{"b1", "b2"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := wholeSendIDs(t, svc, tc.query)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// The tag: term next to an OR over a per-user term is still refused where it sits
// under the OR; only the per-user shape was lifted.
func TestGetAudiobooks_PerUserOrStillRefusesATagUnderTheOr(t *testing.T) {
	svc := wholeSendService(t)
	_, err := wholeSendIDs(t, svc, "read_status:finished OR tag:noir")
	require.Error(t, err)
	assert.True(t, search.IsTagTermError(err), "got %v", err)
}

// The index answers the whole expression, so the match count is exact and the page
// is cut from the matches, not from a window: three books match, whatever the page.
func TestSearchWithBleve_PerUserOrCountsAndPagesExactly(t *testing.T) {
	svc := wholeSendService(t)
	const q = "title:Hush OR read_status:finished"
	books, total, err := svc.searchWithBleve(context.Background(), q, 1, 0, "alice", nil, hydratePage)
	require.NoError(t, err)
	assert.Len(t, books, 1)
	assert.Equal(t, 3, total)

	var seen []string
	for offset := 0; offset < 3; offset++ {
		page, total, err := svc.searchWithBleve(context.Background(), q, 1, offset, "alice", nil, hydratePage)
		require.NoError(t, err)
		require.Len(t, page, 1)
		assert.Equal(t, 3, total)
		seen = append(seen, page[0].ID)
	}
	sort.Strings(seen)
	assert.Equal(t, []string{"b1", "b3", "b5"}, seen, "the pages cover the matches once each")

	// A scoped search (restrictIDs: an author or a series) confines it.
	books, total, err = svc.searchWithBleve(context.Background(), q, 50, 0, "alice", []string{"b1", "b2", "b3"}, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	got := []string{}
	for _, b := range books {
		got = append(got, b.ID)
	}
	sort.Strings(got)
	assert.Equal(t, []string{"b1", "b3"}, got)
}

// A store that lists the viewer's rows is scanned ONCE and no row is read by id;
// one that cannot is read book by book (every indexed book, once).
func TestSearchWithBleve_PerUserOrReadsTheViewersRowsOnceOrByIDForAStoreThatCannotList(t *testing.T) {
	const q = "title:Hush OR read_status:finished"

	var lister listingCountingStore
	svc := wholeSendServiceOver(t, func(st database.Store) database.Store {
		lister = listingCountingStore{&callCountingStore{Store: st}}
		return lister
	})
	_, total, err := svc.searchWithBleve(context.Background(), q, 50, 0, "alice", nil, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Equal(t, 1, lister.listReads, "one scan of the viewer's rows")
	assert.Zero(t, lister.stateReads, "no row read by id")

	var plain *callCountingStore
	svc = wholeSendServiceOver(t, func(st database.Store) database.Store {
		plain = &callCountingStore{Store: st}
		return plain
	})
	_, total, err = svc.searchWithBleve(context.Background(), q, 50, 0, "alice", nil, hydratePage)
	require.NoError(t, err)
	assert.Equal(t, 3, total, "the same answer")
	assert.Equal(t, 6, plain.stateReads, "one read per indexed book")
}

// With no user, or with the per-user kill switch on, the terms are judged as the
// zero state: nothing is finished, everything is unstarted.
func TestSearchWithBleve_PerUserOrWithNoUserContextJudgesTheZeroState(t *testing.T) {
	svc := wholeSendService(t)
	ids := func(q, user string) []string {
		books, _, err := svc.searchWithBleve(context.Background(), q, 50, 0, user, nil, hydratePage)
		require.NoError(t, err)
		out := []string{}
		for _, b := range books {
			out = append(out, b.ID)
		}
		sort.Strings(out)
		return out
	}
	assert.Equal(t, []string{"b1"}, ids("title:Hush OR read_status:finished", ""))
	assert.Equal(t, []string{"b1", "b2", "b3", "b4", "b5", "b6"}, ids("title:Hush OR read_status:unstarted", ""))

	config.Mutate(func(c *config.Config) { c.DisablePerUserSearchFilters = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.DisablePerUserSearchFilters = false }) })
	assert.Equal(t, []string{"b1"}, ids("title:Hush OR read_status:finished", "alice"))
}

// A scan that fails fails the search: the rows decide which books MATCH under an OR,
// so a partial answer would be a wrong one.
func TestSearchWithBleve_PerUserOrFailsWhenTheViewersRowsCannotBeRead(t *testing.T) {
	svc := wholeSendServiceOver(t, func(st database.Store) database.Store {
		return &stateStub{Store: st, listErr: context.DeadlineExceeded}
	})
	_, _, err := svc.searchWithBleve(context.Background(), "title:Hush OR read_status:finished", 50, 0, "alice", nil, hydratePage)
	require.Error(t, err)
}
