// file: internal/audiobooks/filter_duration_test.go
// version: 1.0.0
// guid: 9a3d6e14-2c7b-4f58-8e01-6b4f2d9c7a35
// last-edited: 2026-09-27

package audiobooks

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func strp(s string) *string { return &s }

func TestParseDurationValue_Units(t *testing.T) {
	cases := map[string]int{
		"1200": 1200, "90.5": 91, "20m": 1200, "1h30m": 5400, "1.5h": 5400,
		"45s": 45, "1h 30m": 5400, "2H": 7200, "0": 0,
	}
	for in, want := range cases {
		got, err := parseDurationValue(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "abc", "-5", "-1m", "20 minutes"} {
		_, err := parseDurationValue(bad)
		assert.Error(t, err, bad)
	}
}

func TestDurationMatches_ComparisonsAndRanges(t *testing.T) {
	short := database.Book{ID: "s", Duration: intp(300)}  // 5 min
	edge := database.Book{ID: "e", Duration: intp(1200)}  // exactly 20 min
	long := database.Book{ID: "l", Duration: intp(36000)} // 10 h
	cases := []struct {
		expr string
		want [3]bool // short, edge, long
	}{
		{">20m", [3]bool{false, false, true}},
		{">=1200", [3]bool{false, true, true}},
		{"<20m", [3]bool{true, false, false}},
		{"<=20m", [3]bool{true, true, false}},
		{"<1h30m", [3]bool{true, true, false}},
		{"20m", [3]bool{false, true, false}},
		{"=20m", [3]bool{false, true, false}},
		{"!=20m", [3]bool{true, false, true}},
		{"[10m TO 2h]", [3]bool{false, true, false}},
		{"[* TO 20m]", [3]bool{true, true, false}},
		{"[1h to *]", [3]bool{false, false, true}},
	}
	for _, tc := range cases {
		for i, b := range []database.Book{short, edge, long} {
			assert.Equalf(t, tc.want[i], fieldMatchesValue(b, "duration", tc.expr), "%s on %s", tc.expr, b.ID)
			assert.Equalf(t, tc.want[i], fieldMatchesValue(b, "duration_seconds", tc.expr), "duration_seconds %s on %s", tc.expr, b.ID)
		}
	}
}

// The substring bug this replaces: duration:>1200 matched nothing and
// duration:0 matched every book whose seconds contained a zero digit.
func TestDurationFilter_IsNotSubstring(t *testing.T) {
	b := database.Book{ID: "x", Duration: intp(3600)}
	assert.True(t, fieldMatchesValue(b, "duration", ">1200"))
	assert.False(t, fieldMatchesValue(b, "duration", "0"), "3600 must not match duration:0 by containing a 0")
	assert.False(t, fieldMatchesValue(b, "duration", "60"))
}

func TestDurationFilter_UnknownRuntime(t *testing.T) {
	for _, b := range []database.Book{{ID: "nil"}, {ID: "zero", Duration: intp(0)}} {
		for _, expr := range []string{">20m", "<20m", "[0 TO 99h]", "0"} {
			assert.Falsef(t, fieldMatchesValue(b, "duration", expr), "unknown runtime must not match %s", expr)
		}
		assert.True(t, fieldMatchesValue(b, "has_duration", "no"))
		assert.False(t, fieldMatchesValue(b, "has_duration", "yes"))
		// Negation is plain set complement: -duration:<20m keeps unknowns.
		assert.True(t, matchesFieldFilters(b, []FieldFilter{{Field: "duration", Value: "<20m", Negated: true}}))
	}
	known := database.Book{ID: "k", Duration: intp(10)}
	assert.True(t, fieldMatchesValue(known, "has_duration", "yes"))
}

func TestMetadataApplied_Definition(t *testing.T) {
	cases := map[string]bool{"matched": true, "audio_confirmed": true, "no_match": false, "": false, "pending": false}
	for status, applied := range cases {
		b := database.Book{ID: status}
		if status != "" {
			b.MetadataReviewStatus = strp(status)
		}
		assert.Equal(t, applied, BookMetadataApplied(&b), status)
		assert.Equal(t, applied, fieldMatchesValue(b, "metadata", "applied"), status)
		assert.Equal(t, !applied, fieldMatchesValue(b, "metadata", "none"), status)
		// Exact, not substring: "unapplied" contains "applied".
		assert.Equal(t, !applied, fieldMatchesValue(b, "metadata", "unapplied"), status)
	}
	// MetadataSource alone (title repair stamps it) is not an apply.
	src := database.Book{ID: "src", MetadataSource: strp("audible")}
	assert.False(t, BookMetadataApplied(&src))
	nilBook := database.Book{}
	assert.False(t, fieldMatchesValue(nilBook, "metadata", "bogus"))
}

func TestOwnerQuery_NeedsMetadataHidesChapters(t *testing.T) {
	filters := []FieldFilter{
		{Field: "metadata", Value: "applied", Negated: true},
		{Field: "duration", Value: ">20m"},
	}
	books := []database.Book{
		{ID: "chapter", Duration: intp(600)},
		{ID: "needs", Duration: intp(36000)},
		{ID: "done", Duration: intp(36000), MetadataReviewStatus: strp("matched")},
		{ID: "confirmed", Duration: intp(36000), MetadataReviewStatus: strp("audio_confirmed")},
		{ID: "nomatch", Duration: intp(36000), MetadataReviewStatus: strp("no_match")},
		{ID: "unknown"},
	}
	var got []string
	for _, b := range books {
		if matchesFieldFilters(b, filters) {
			got = append(got, b.ID)
		}
	}
	assert.Equal(t, []string{"needs", "nomatch"}, got)
}

func TestValidateFilterValue(t *testing.T) {
	assert.NoError(t, FirstInvalidFilterValue([]FieldFilter{{Field: "duration", Value: ">20m"}, {Field: "title", Value: ">>x"}}))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "duration", Value: ">abc"}))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "duration", Value: "[10m 2h]"}))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "has_duration", Value: "maybe"}))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "metadata", Value: "whatever"}))
	assert.True(t, FieldIsKnown("metadata"))
	assert.True(t, FieldIsKnown("has_duration"))
}

type coreFiles []database.BookFileCore

func (c coreFiles) GetAllBookFilesCore() ([]database.BookFileCore, error) { return c, nil }

func TestRuntimeIndex_UsesCanonicalRuntime(t *testing.T) {
	files := coreFiles{
		// Complete multi-file book: 3 x 20 min = 1 h, stored aggregate stale.
		{BookID: "multi", Duration: 1200}, {BookID: "multi", Duration: 1200}, {BookID: "multi", Duration: 1200},
		// Partial: one known 10-min chapter, one unprobed → unknown, not 10 min.
		{BookID: "partial", Duration: 600}, {BookID: "partial"},
		// Single file, unprobed row: the stored aggregate stands in.
		{BookID: "single"},
	}
	ri := newRuntimeIndex()
	require.True(t, ri.ensure(files))

	multi := database.Book{ID: "multi", Duration: intp(1200)}
	sec, ok := ri.runtimeOf(&multi)
	assert.True(t, ok)
	assert.Equal(t, 3600, sec)

	partial := database.Book{ID: "partial", Duration: intp(600)}
	_, ok = ri.runtimeOf(&partial)
	assert.False(t, ok, "a partial sum is a lower bound, never a runtime")
	assert.False(t, durationMatches(&partial, "<20m", ri.runtimeOf), "a partial book must not be listed as short")

	single := database.Book{ID: "single", Duration: intp(900)}
	sec, ok = ri.runtimeOf(&single)
	assert.True(t, ok)
	assert.Equal(t, 900, sec)

	fresh := database.Book{ID: "not-in-index", Duration: intp(42)}
	sec, ok = ri.runtimeOf(&fresh)
	assert.True(t, ok)
	assert.Equal(t, 42, sec)
}

func TestRuntimeIndex_RebuildsInBackgroundWhenStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	ri := newRuntimeIndex()
	ri.now = func() time.Time { return now }
	require.True(t, ri.ensure(coreFiles{{BookID: "a", Duration: 100}}))
	now = now.Add(runtimeIndexTTL + time.Second)
	// Stale: still usable immediately (serves the old copy).
	assert.True(t, ri.ensure(coreFiles{{BookID: "a", Duration: 200}}))
	assert.Eventually(t, func() bool {
		sec, _ := ri.runtimeOf(&database.Book{ID: "a"})
		return sec == 200
	}, 2*time.Second, 5*time.Millisecond)
	assert.False(t, ri.ensure(struct{}{}), "a store without file rows falls back")
}

// syntheticLibrary builds n books where ~36% are single-file chapter books
// under 20 minutes (the prod mix measured 2026-09-27), ~17% unknown runtime,
// and the rest multi-file (8 files each).
func syntheticLibrary(n int) ([]database.Book, coreFiles) {
	books := make([]database.Book, n)
	files := make(coreFiles, 0, n*4)
	for i := range books {
		id := fmt.Sprintf("book-%06d", i)
		b := database.Book{ID: id}
		switch {
		case i%100 < 36:
			b.Duration = intp(300 + i%600)
			files = append(files, database.BookFileCore{BookID: id, Duration: *b.Duration})
		case i%100 < 53:
			files = append(files, database.BookFileCore{BookID: id})
		default:
			b.Duration = intp(8 * 1800)
			for j := 0; j < 8; j++ {
				files = append(files, database.BookFileCore{BookID: id, Duration: 1800})
			}
		}
		if i%5 == 0 {
			b.MetadataReviewStatus = strp("matched")
		}
		books[i] = b
	}
	return books, files
}

// BenchmarkOwnerQuery_100k measures the per-query predicate cost over 100k
// books with the runtime index warm: "-metadata:applied duration:>20m".
func BenchmarkOwnerQuery_100k(b *testing.B) {
	books, files := syntheticLibrary(100_000)
	ri := newRuntimeIndex()
	if !ri.ensure(files) {
		b.Fatal("index build failed")
	}
	filters := []FieldFilter{{Field: "metadata", Value: "applied", Negated: true}, {Field: "duration", Value: ">20m"}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := 0
		for j := range books {
			if matchesFieldFiltersRT(books[j], filters, ri.runtimeOf) {
				n++
			}
		}
		if n == 0 {
			b.Fatal("no matches")
		}
	}
}

// BenchmarkRuntimeIndexBuild_100k measures the (cached, TTL'd) index build
// over 100k books / ~450k file rows.
func BenchmarkRuntimeIndexBuild_100k(b *testing.B) {
	_, files := syntheticLibrary(100_000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if m := buildRuntimeEntries(files); len(m) == 0 {
			b.Fatal("empty")
		}
	}
}
