// file: internal/audiobooks/filter_unified_grammar_test.go
// version: 1.4.0
// guid: 2c7e9a14-5b3f-4e81-9d06-a4f1c8b2e753
// last-edited: 2026-10-10

package audiobooks

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// Synthetic titles only (public repo; see feedback_no_real_library_rows).
func gramBook(title string) database.Book { return database.Book{Title: title} }

// The owner's query: title:a* returned 0 because "a*" was substring-matched
// with a literal asterisk. It must now mean "title starts with a".
func TestUnifiedGrammar_TitleGlobPrefix(t *testing.T) {
	ff := []FieldFilter{{Field: "title", Value: "a*"}}
	assert.True(t, matchesFieldFilters(gramBook("Alpha Story"), ff))
	assert.True(t, matchesFieldFilters(gramBook("  alpha story"), ff), "leading whitespace is trimmed for globs")
	assert.False(t, matchesFieldFilters(gramBook("The Alpha Story"), ff), "glob is whole-value: starts-with, not any word")
	assert.False(t, matchesFieldFilters(gramBook("01 Alpha"), ff))
	// Quoted keeps the asterisk literal.
	q := []FieldFilter{{Field: "title", Value: "a*", Quoted: true}}
	assert.False(t, matchesFieldFilters(gramBook("Alpha Story"), q))
	assert.True(t, matchesFieldFilters(gramBook("Grade a* Story"), q))
}

func TestUnifiedGrammar_TitleRegex(t *testing.T) {
	cases := []struct {
		value   string
		negated bool
		title   string
		want    bool
	}{
		{`/^\s*\p{L}/`, false, "Alpha", true},
		{`/^\s*\p{L}/`, false, " Émile", true},
		{`/^\s*\p{L}/`, false, "01 - Chapter One", false},
		// Negation is field-level (RE2 has no lookahead).
		{`/^\s*\d/`, true, "01 - Chapter One", false},
		{`/^\s*\d/`, true, "Alpha", true},
		{`/chapter \d+/`, false, "Part CHAPTER 12", true},
		{`/(?-i)^alpha$/`, false, "Alpha", false},
	}
	for _, tc := range cases {
		ff := []FieldFilter{{Field: "title", Value: tc.value, Negated: tc.negated}}
		assert.Equal(t, tc.want, matchesFieldFilters(gramBook(tc.title), ff),
			"negated=%v title:%s on %q", tc.negated, tc.value, tc.title)
	}
}

func TestUnifiedGrammar_InvalidValuesAreErrors(t *testing.T) {
	bad := []FieldFilter{
		{Field: "title", Value: "/^(?!The)/"},
		{Field: "title", Value: "/unterminated"},
		{Field: "title", Value: "//"},
		{Field: "title", Value: "/x/i"},
		{Field: "author", Value: "/a(b/"},
		{Field: "year", Value: ">abc"},
		{Field: "year", Value: "[2015 2020]"},
		{Field: "progress_pct", Value: ">x"},
	}
	for _, f := range bad {
		err := ValidateFilterValue(f)
		require.Error(t, err, "%s:%s must be rejected", f.Field, f.Value)
		assert.Contains(t, err.Error(), f.Field+":"+f.Value, "the error names the token")
		// And the matcher fails closed rather than matching everything.
		assert.False(t, matchesFieldFilters(gramBook("anything"), []FieldFilter{f}))
		assert.False(t, matchesFieldFilters(gramBook("anything"), []FieldFilter{{Field: f.Field, Value: f.Value, Negated: true}}))
	}
	assert.NoError(t, ValidateFilterValue(FieldFilter{Field: "title", Value: `/^\s*\p{L}/`}))
	assert.NoError(t, ValidateFilterValue(FieldFilter{Field: "title", Value: "/x", Quoted: true}), "quoted slash is literal")
}

func TestUnifiedGrammar_YearComparisons(t *testing.T) {
	both := database.Book{PrintYear: new(1999), AudiobookReleaseYear: new(2021)}
	only := database.Book{PrintYear: new(2017)}
	none := database.Book{}
	cases := []struct {
		value string
		book  database.Book
		want  bool
	}{
		{">2020", both, true}, // either year may satisfy
		{">2020", only, false},
		{"<2000", both, true},
		{"[2015 TO 2020]", only, true},
		{"[2015 TO 2020]", both, false},
		{"[2015 TO *]", both, true},
		{"2017", only, true},
		{"201", only, false}, // bare number = equality, not substring
		{"!=1999", both, false},
		{">0", none, false}, // unknown year matches no comparison
		{"/^20/", only, true},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, fieldMatchesValue(tc.book, "year", tc.value), "year:%s", tc.value)
	}
	// Negating a comparison includes unknown years (positive never matches them).
	assert.True(t, matchesFieldFilters(none, []FieldFilter{{Field: "year", Value: ">2020", Negated: true}}))
}

func TestUnifiedGrammar_UnknownNumericMatchesNoComparison(t *testing.T) {
	unprobed := database.Book{}
	low := database.Book{Bitrate: new(48)}
	assert.False(t, fieldMatchesValue(unprobed, "bitrate", "<64"), "unprobed bitrate is not < 64")
	assert.True(t, fieldMatchesValue(low, "bitrate", "<64"))
	assert.True(t, fieldMatchesValue(low, "bitrate", "[* TO 64]"))
}

func TestUnifiedGrammar_NumericTextFormsUseKnownValues(t *testing.T) {
	unprobed := database.Book{}
	probed := database.Book{Bitrate: new(128)}
	assert.False(t, fieldMatchesValue(unprobed, "bitrate", "*"), "unprobed bitrate has no value")
	assert.True(t, matchesFieldFilters(unprobed, []FieldFilter{{Field: "bitrate", Value: "*", Negated: true}}))
	assert.True(t, fieldMatchesValue(probed, "bitrate", "*"))
	assert.True(t, fieldMatchesValue(probed, "bitrate", "12*"))
	years := database.Book{PrintYear: new(1999), AudiobookReleaseYear: new(2021)}
	assert.True(t, fieldMatchesValue(years, "year", "/^20/"), "release year is matched too")
	assert.True(t, fieldMatchesValue(years, "year", "20*"))
	assert.False(t, fieldMatchesValue(years, "year", "/^18/"))
}

func TestUnifiedGrammar_ReadStatusRejectsPatterns(t *testing.T) {
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "read_status", Value: "/fin/"}))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "read_status", Value: "fin*"}))
	assert.NoError(t, ValidateFilterValue(FieldFilter{Field: "read_status", Value: "finished"}))
}

// The owner's exact query, end to end through GetAudiobooks (summaries path).
func TestUnifiedGrammar_OwnerQueryThroughGetAudiobooks(t *testing.T) {
	mockStore := mocks.NewMockStore(t)
	svc := NewAudiobookService(mockStore)
	long := new(3600)
	short := new(120)
	summaries := []database.BookSummary{
		{ID: "hit", Title: "Alpha Story", Duration: long},
		{ID: "lower", Title: "  another tale", Duration: long},
		{ID: "digit", Title: "01 Alpha", Duration: long},
		{ID: "notA", Title: "Beta", Duration: long},
		{ID: "short", Title: "Alpha Chapter", Duration: short},
		{ID: "applied", Title: "Alpha Done", Duration: long, MetadataReviewStatus: new("matched")},
		{ID: "nomatch", Title: "Alpha Ruled", Duration: long, MetadataReviewStatus: new("no_match")},
	}
	mockStore.EXPECT().GetAllBookSummaries(0, 0).Return(summaries, nil).Maybe()
	mockStore.EXPECT().GetAllBookFilesCore().Return(nil, nil).Maybe()
	got, err := svc.GetAudiobooks(context.Background(), 0, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{
			{Field: "metadata", Value: "applied", Negated: true},
			{Field: "review", Value: "no_match", Negated: true},
			{Field: "duration", Value: "<10m", Negated: true},
			{Field: "title", Value: "a*"},
		},
	})
	require.NoError(t, err)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.ID)
	}
	assert.ElementsMatch(t, []string{"hit", "lower"}, ids)
}

func TestUnifiedGrammar_ProgressPctComparison(t *testing.T) {
	st := &database.UserBookState{ProgressPct: 80}
	assert.True(t, matchesPerUserFilter(st, FieldFilter{Field: "progress_pct", Value: ">75"}))
	assert.False(t, matchesPerUserFilter(st, FieldFilter{Field: "progress_pct", Value: "<75"}))
	assert.True(t, matchesPerUserFilter(st, FieldFilter{Field: "progress_pct", Value: "80"}))
	assert.True(t, matchesPerUserFilter(st, FieldFilter{Field: "progress_pct", Value: "[50 TO 90]"}))
}

// A regex or glob review:/library_state: value must NOT be plucked into the
// store's exact-match ReviewStatus/LibraryState (EqualFold would compare the
// pattern text literally and return 0 books). A plain literal still is.
func TestUnifiedGrammar_PatternValuesAreNotPushedDownAsExact(t *testing.T) {
	svc := NewAudiobookService(mocks.NewMockStore(t))

	bsf, ok := svc.buildBookSummaryFilter(ListFilters{FieldFilters: []FieldFilter{
		{Field: "review", Value: "/^no/"},
		{Field: "library_state", Value: "org*"},
	}}, true, nil)
	require.True(t, ok)
	assert.Empty(t, bsf.ReviewStatus, "regex review value must stay on the predicate")
	assert.Empty(t, bsf.LibraryState, "glob library_state value must stay on the predicate")
	require.NotNil(t, bsf.Predicate)
	assert.True(t, bsf.Predicate(&database.Book{MetadataReviewStatus: new("no_match"), LibraryState: new("organized")}))
	assert.False(t, bsf.Predicate(&database.Book{MetadataReviewStatus: new("matched"), LibraryState: new("organized")}))

	bsf, ok = svc.buildBookSummaryFilter(ListFilters{FieldFilters: []FieldFilter{
		{Field: "review", Value: "no_match"},
	}}, true, nil)
	require.True(t, ok)
	assert.Equal(t, "no_match", bsf.ReviewStatus, "plain literal is still pushed down")
}

// Background ops resolve filters through GetAudiobooks; a bad pattern must be
// an error there, not "0 books".
func TestUnifiedGrammar_GetAudiobooksRejectsInvalidRegex(t *testing.T) {
	svc := NewAudiobookService(mocks.NewMockStore(t))
	_, err := svc.GetAudiobooks(context.Background(), 10, 0, "", nil, nil, ListFilters{
		FieldFilters: []FieldFilter{{Field: "title", Value: "/(?=x)/"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "title:/(?=x)/")
	_, err = svc.CountAudiobooksFiltered(context.Background(), ListFilters{
		FieldFilters: []FieldFilter{{Field: "year", Value: ">nope"}},
	})
	require.Error(t, err)
}

func TestUnifiedGrammar_FileSizeUnits(t *testing.T) {
	mb := int64(1 << 20)
	ten := database.Book{FileSize: new(10 * mb)}
	thirty := database.Book{FileSize: new(30 * mb)}
	unset := database.Book{}
	books := []database.Book{ten, thirty, unset}
	match := func(v string) []bool {
		out := make([]bool, len(books))
		for i, b := range books {
			out[i] = fieldMatchesValue(b, "file_size", v)
		}
		return out
	}
	assert.Equal(t, []bool{false, true, false}, match(">20mb"))
	assert.Equal(t, []bool{false, true, false}, match(">20MB"))
	assert.Equal(t, []bool{true, false, false}, match("<20m"))
	assert.Equal(t, []bool{true, true, false}, match("[5mb TO 40mb]"))
	assert.Equal(t, []bool{false, true, false}, match("[20mb TO *]"))
	assert.Equal(t, []bool{true, false, false}, match("10mb"), "bare value with unit = equality")
	assert.Equal(t, []bool{false, true, false}, match(">20971520"), "bare number is bytes")
	for _, bad := range []string{">abc", ">20zb", "[1mb 2mb]"} {
		require.Error(t, ValidateFilterValue(FieldFilter{Field: "file_size", Value: bad}), bad)
	}
}

func TestUnifiedGrammar_BitrateAndSampleRateUnits(t *testing.T) {
	low := database.Book{Bitrate: new(48), SampleRate: new(22050)}
	high := database.Book{Bitrate: new(128), SampleRate: new(44100)}
	assert.True(t, fieldMatchesValue(low, "bitrate", "<64k"))
	assert.True(t, fieldMatchesValue(low, "bitrate", "<64kbps"))
	assert.False(t, fieldMatchesValue(high, "bitrate", "<64k"))
	assert.True(t, fieldMatchesValue(high, "sample_rate", ">=44.1khz"))
	assert.False(t, fieldMatchesValue(low, "sample_rate", ">=44.1khz"))
	assert.Error(t, ValidateFilterValue(FieldFilter{Field: "bitrate", Value: "<64mb"}))
}

// Library search compiles its text values through the same querygrammar
// limits as the Review query: a value too long or a pattern too large to
// scan the library with is a 400 naming the token, refused at compile time,
// not a multi-second (or multi-minute) scan.
func TestUnifiedGrammar_OversizedPatternsAreRefused(t *testing.T) {
	for _, f := range []FieldFilter{
		{Field: "title", Value: `/(.*){1000}/`},
		{Field: "title", Value: `/(?:.?){1000}zzz/`},
		{Field: "author", Value: "/" + strings.Repeat("(a|b)", 6000) + "/"},
		{Field: "series", Value: strings.Repeat("a", 300), Quoted: true},
	} {
		start := time.Now()
		err := ValidateFilterValue(f)
		require.Error(t, err, "%s:%.20s must be refused", f.Field, f.Value)
		assert.Less(t, time.Since(start), 50*time.Millisecond)
		assert.True(t, strings.Contains(err.Error(), "too complex") || strings.Contains(err.Error(), "the limit is 256"), err.Error())
		assert.False(t, matchesFieldFilters(gramBook("anything"), []FieldFilter{f}), "fails closed")
	}
}
