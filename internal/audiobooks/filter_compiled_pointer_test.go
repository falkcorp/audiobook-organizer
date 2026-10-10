// file: internal/audiobooks/filter_compiled_pointer_test.go
// version: 1.0.0
// guid: 5b7e2c14-8a93-4d60-b1f7-3c9a0e6d2f58
// last-edited: 2026-10-09

package audiobooks

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The duration expression must be parsed once per request (at compile time),
// not once per row.
func TestCompiledDuration_ParsedOnce(t *testing.T) {
	before := durationParseCount.Load()
	cfs, err := compileFieldFilters([]FieldFilter{{Field: "duration", Value: ">20m"}})
	require.NoError(t, err)
	books := make([]database.Book, 1000)
	for i := range books {
		books[i] = database.Book{ID: fmt.Sprintf("b%04d", i), Duration: intp(600 + i*3)}
	}
	matched := 0
	for i := range books {
		if matchesCompiledFilters(&books[i], cfs, nil) {
			matched++
		}
	}
	assert.Equal(t, int64(1), durationParseCount.Load()-before, "parseDurationExpr must run once for 1,000 rows")
	assert.Greater(t, matched, 0)
	assert.Less(t, matched, len(books))
}

func TestHydrateAuthorSeriesNames_DoesNotMutateSource(t *testing.T) {
	aid, sid := 7, 9
	src := &database.Book{ID: "x", AuthorID: &aid, SeriesID: &sid}
	var dst database.Book
	got := hydrateAuthorSeriesNames(&dst, src, map[int]string{7: "Author 07"}, map[int]string{9: "Series 09"})
	assert.Same(t, &dst, got)
	require.NotNil(t, got.Author)
	assert.Equal(t, "Author 07", got.Author.Name)
	require.NotNil(t, got.Series)
	assert.Equal(t, "Series 09", got.Series.Name)
	assert.Nil(t, src.Author, "source Author must stay nil")
	assert.Nil(t, src.Series, "source Series must stay nil")

	// Already-set Author is left untouched; nil maps are a no-op copy.
	pre := &database.Author{ID: 7, Name: "Preset"}
	src2 := &database.Book{ID: "y", AuthorID: &aid, Author: pre}
	got = hydrateAuthorSeriesNames(&dst, src2, map[int]string{7: "Author 07"}, nil)
	assert.Same(t, pre, got.Author)
	got = hydrateAuthorSeriesNames(&dst, src, nil, nil)
	assert.Nil(t, got.Author)
}

// durationMatches (parse then match) and durationMatchesExpr must agree.
func TestDurationMatchesExpr_AgreesWithWrapper(t *testing.T) {
	exprs := []string{">20m", "<20m", "[10m TO 20m]", "=0", "==1200", "!=1200", ">=20m", "<=20m"}
	books := []database.Book{
		{ID: "a", Duration: intp(0)},
		{ID: "b", Duration: intp(600)},
		{ID: "c", Duration: intp(1200)},
		{ID: "d", Duration: intp(3600)},
		{ID: "unknown"},
	}
	rts := map[string]runtimeFunc{"stored": nil, "unknown": func(*database.Book) (int, bool) { return 0, false }}
	for _, ex := range exprs {
		e, err := parseDurationExpr(ex)
		require.NoError(t, err, ex)
		for name, rt := range rts {
			for i := range books {
				assert.Equal(t, durationMatches(&books[i], ex, rt), durationMatchesExpr(&books[i], e, rt),
					"%s rt=%s book=%s", ex, name, books[i].ID)
			}
		}
	}
}
