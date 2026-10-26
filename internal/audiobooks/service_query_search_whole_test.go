// file: internal/audiobooks/service_query_search_whole_test.go
// version: 1.0.0
// guid: 8b4e1f27-3a96-4c05-b7d2-6e0a9c5f1d38
// last-edited: 2026-10-26

package audiobooks

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// wholeSendService is a service over a real index of four books, with the viewer
// "alice" having finished b1 and b3 and being part-way through b2.
//
//	b1  Hush            noir, gotham   finished
//	b2  Year One        noir           in progress
//	b3  Knightfall      gotham         finished
//	b4  (no tags)                      unstarted
func wholeSendService(t *testing.T) *AudiobookService {
	t.Helper()
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.IndexBookBatch([]search.BookDocument{
		{BookID: "b1", Title: "Hush", Author: "kyle", Tags: []string{"noir", "gotham"}},
		{BookID: "b2", Title: "Year One", Author: "kyle", Tags: []string{"noir"}},
		{BookID: "b3", Title: "Knightfall", Author: "kyle", Tags: []string{"gotham"}},
		{BookID: "b4", Title: "The Long Halloween", Author: "kyle"},
	}))
	st := mocks.NewMockStore(t)
	st.EXPECT().GetBooksByIDs(mock.Anything).RunAndReturn(func(ids []string) ([]database.Book, error) {
		out := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			out = append(out, database.Book{ID: id})
		}
		return out, nil
	}).Maybe()
	status := map[string]string{
		"b1": database.UserBookStatusFinished,
		"b2": database.UserBookStatusInProgress,
		"b3": database.UserBookStatusFinished,
	}
	st.EXPECT().GetUserBookState("alice", mock.Anything).RunAndReturn(func(_, id string) (*database.UserBookState, error) {
		if s, ok := status[id]; ok {
			return &database.UserBookState{Status: s}, nil
		}
		return nil, nil
	}).Maybe()
	st.EXPECT().SearchBooks(mock.Anything, mock.Anything, mock.Anything).
		Return([]database.Book{}, nil).Maybe()
	svc := NewAudiobookService(st)
	svc.SetSearchIndex(completedIndex(t, idx))
	return svc
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
// whole in search=, and no longer sends its tag: terms as tags[]. The index field
// is "tags"; with no "tag" spelling the term matched no book and the list showed
// nothing, with no message. The ROWS are asserted here, not the status: a green
// test of an empty answer is what let it through.
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
		{"(read_status:finished tag:gotham -tag:noir)", []string{"b3"}},
		{"(read_status:in_progress tag:noir)", []string{"b2"}},
		{"(read_status:finished tag:camelot)", []string{}},
		// the same terms without boolean structure, the way the box sends them
		{"read_status:finished tag:noir", []string{"b1"}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := wholeSendIDs(t, svc, tc.query)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
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
