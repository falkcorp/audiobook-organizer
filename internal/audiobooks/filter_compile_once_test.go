// file: internal/audiobooks/filter_compile_once_test.go
// version: 1.0.0
// guid: f11ca9f6-e8e7-49d7-9b65-2a501c9b4d52
// last-edited: 2026-10-09

package audiobooks

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestListPath_CompilesFiltersOncePerRequest guards the list path against a
// per-row filter compile: one request with two field filters over 1,000
// books must compile the filter set exactly once. Synthetic rows only.
func TestListPath_CompilesFiltersOncePerRequest(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	ps.WaitForWarmup()
	for i := range 1000 {
		_, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("Title %06d", i), FilePath: fmt.Sprintf("/synthetic/Title %06d.m4b", i)})
		require.NoError(t, err)
	}
	svc := NewAudiobookService(ps)

	var compiles atomic.Int64
	prev := compileFiltersHook
	compileFiltersHook = func() { compiles.Add(1) }
	t.Cleanup(func() { compileFiltersHook = prev })

	// total may be -1 ("unknown") on the post-filter path; only the page
	// and the compile count matter here.
	books, _, err := svc.GetAudiobooksWithTotal(context.Background(), 50, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			// Both stay in the per-row predicate (neither is a native
			// pushdown), and the title is narrow enough (10 rows) that the
			// walk visits every row instead of stopping at a full page.
			{Field: "title", Value: "Title 00099"},
			{Field: "has_duration", Value: "no"},
		},
	})
	require.NoError(t, err)
	require.Len(t, books, 10)
	require.Equal(t, int64(1), compiles.Load(), "field filters must compile once per request, not per row")
}
