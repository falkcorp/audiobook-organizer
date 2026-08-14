// file: internal/server/handlers/audiobooks/bare_filter_param_test.go
// version: 1.1.0
// guid: 5a9e2c68-4b71-4d03-9e85-1c6f0a37b2d9
// last-edited: 2026-08-14

package audiobookshandler

import (
	"net/http/httptest"
	"net/url"
	"testing"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Field filters travel inside the `filters` JSON parameter. Passed bare
// (?title=Skills) gin ignores the parameter entirely, so the request lists the
// whole library while looking exactly like a narrowed query — count included.
//
// Measured on production 2026-08-13: ?title=Skills answered count=63870 with
// non-matching rows, while filters=[{"field":"title","value":"Skills"}]
// answered 25 and value "zzqqxx" answered 0. The 63,870 was read as evidence
// of a storage-layer filtering bug and written into a handoff as a root cause.
// It was not one.

// queryCtx builds a gin context carrying only a query string, which is all
// firstBareFilterFieldParam inspects.
func queryCtx(t *testing.T, rawQuery string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/v1/audiobooks?"+rawQuery, nil)
	return c
}

func TestFirstBareFilterFieldParam_RejectsFilterFieldsPassedBare(t *testing.T) {
	// Every name the guard knows must be caught on its own — a set that is
	// only spot-checked drifts into holes.
	//
	// Counted explicitly: a table that silently iterates nothing also reports
	// success, so assert the set is the size the derivation should produce
	// before trusting a green run.
	ran := 0
	for field := range filterFieldQueryParams {
		ran++
		t.Run(field, func(t *testing.T) {
			got, bare := firstBareFilterFieldParam(
				queryCtx(t, url.Values{field: {"anything"}}.Encode()))
			require.True(t, bare, "bare ?%s= must be rejected", field)
			require.Equal(t, field, got)
		})
	}
	require.Equal(t, len(audiobookspkg.KnownFilterFields())-len(bareParamAllowListPresentInCanonical()),
		ran, "guard table must exercise every derived name")
}

// bareParamAllowListPresentInCanonical returns the allow-list entries that are
// actually in the canonical field list. The allow-list may name a field
// defensively before it exists there (fingerprint_status does), and those
// entries subtract nothing from the derived set.
func bareParamAllowListPresentInCanonical() []string {
	canonical := make(map[string]struct{})
	for _, n := range audiobookspkg.KnownFilterFields() {
		canonical[n] = struct{}{}
	}
	out := []string{}
	for name := range bareParamAllowList {
		if _, ok := canonical[name]; ok {
			out = append(out, name)
		}
	}
	return out
}

// TestFilterFieldQueryParams_DerivedFromCanonicalList is the assertion that
// replaces the hand-maintained mirror this set used to be.
//
// audiobooks.KnownFilterFields() is the single source of truth for what the
// list can filter on, already pinned to the matcher by
// TestFilterFieldNames_MatchTheMatcher. The guard used to be a THIRD copy of
// that set, held against nothing — and it drifted: 17 real filter fields were
// missing, so ?year=2001 answered with the entire 63,870-book library while
// looking exactly like a narrowed query.
//
// With the set derived, this test is what keeps the derivation honest: every
// canonical field is either guarded or explicitly allow-listed, and nothing is
// guarded that is not a canonical field.
func TestFilterFieldQueryParams_DerivedFromCanonicalList(t *testing.T) {
	canonical := audiobookspkg.KnownFilterFields()
	require.NotEmpty(t, canonical, "canonical field list must not be empty")

	for _, name := range canonical {
		_, guarded := filterFieldQueryParams[name]
		_, allowed := bareParamAllowList[name]
		if allowed {
			require.False(t, guarded,
				"%q is allow-listed and must NOT be guarded", name)
			continue
		}
		require.True(t, guarded,
			"canonical filter field %q is not guarded — a bare ?%s= would list "+
				"the entire library while looking like a narrowed query", name, name)
	}

	// Nothing may be guarded that is not a canonical filter field: the error
	// message tells the caller to move the name into `filters`, which would be
	// a lie for a name the matcher does not accept.
	canonicalSet := make(map[string]struct{}, len(canonical))
	for _, n := range canonical {
		canonicalSet[n] = struct{}{}
	}
	for name := range filterFieldQueryParams {
		require.Contains(t, canonicalSet, name,
			"%q is guarded but is not a canonical filter field", name)
	}
}

// TestFilterFieldQueryParams_CoversTheFieldsThatDrifted pins the specific names
// the hand-written set was missing. Named individually rather than counted, so
// the test says WHICH field regressed if the derivation is ever unwound.
//
// Each of these answered count=63870 when passed bare, on a production library
// of that exact size.
func TestFilterFieldQueryParams_CoversTheFieldsThatDrifted(t *testing.T) {
	drifted := []string{
		"series_number", "duration", "bitrate", "bitrate_kbps",
		"file_size", "file_size_bytes", "sample_rate", "sample_rate_hz",
		"channels", "bit_depth", "isbn10", "isbn13", "work_id", "year",
		"created_at", "updated_at", "marked_for_deletion",
	}
	for _, field := range drifted {
		t.Run(field, func(t *testing.T) {
			got, bare := firstBareFilterFieldParam(
				queryCtx(t, url.Values{field: {"1"}}.Encode()))
			require.True(t, bare,
				"bare ?%s= must be rejected, not answered with the whole library", field)
			require.Equal(t, field, got)
		})
	}
	require.Len(t, drifted, 17)
}

func TestFirstBareFilterFieldParam_AllowsRealParameters(t *testing.T) {
	// The genuine query parameters this endpoint accepts. If the guard fired
	// on any of these it would break working clients — including the web UI,
	// which sends the `filters` form plus these.
	// Derived from the accessors ListAudiobooks actually reads, across every
	// spelling (c.Query, c.QueryArray, httputil.ParseQuery*), not from one
	// grep of one form — see the note on filterFieldQueryParams.
	realParams := []string{
		"limit", "offset", "sort_by", "sort_order", "filters", "search",
		"author_id", "series_id", "tag", "tags", "show_quarantined",
		"is_primary_version", "has_file_errors", "missing_covers",
		"in_import_path", "no_isbn", "duplicates_flagged",
		"coverage_percent_min", "coverage_percent_max",
		// Both a filter field name AND a genuine bare parameter. An earlier
		// revision of the guard rejected it and broke
		// TestListBooksWithTagFilter, which asserts
		// ?tag=…&library_state=organized narrows to one book. This entry is
		// the regression pin for that.
		"library_state",
		"fingerprint_status",
	}
	for _, p := range realParams {
		t.Run(p, func(t *testing.T) {
			_, bare := firstBareFilterFieldParam(
				queryCtx(t, url.Values{p: {"1"}}.Encode()))
			require.False(t, bare,
				"%q is a real query parameter and must not be rejected", p)
		})
	}
}

// TestFirstBareFilterFieldParam_AuthorIDIsNotAuthor pins the one adjacent pair
// that would be easy to get wrong: "author" is a filter field, "author_id" is
// a genuine parameter. A prefix or substring match instead of exact-name
// lookup would break the library's author drill-down.
func TestFirstBareFilterFieldParam_AuthorIDIsNotAuthor(t *testing.T) {
	_, bare := firstBareFilterFieldParam(queryCtx(t, "author_id=42"))
	require.False(t, bare, "author_id is a real parameter, not the author filter field")

	got, bare := firstBareFilterFieldParam(queryCtx(t, "author=Tolkien"))
	require.True(t, bare)
	require.Equal(t, "author", got)
}

// TestFirstBareFilterFieldParam_Deterministic guards the message: with several
// bad fields at once, the reported one must not vary between identical
// requests. Go randomizes map iteration, so an unsorted implementation names a
// different field on each retry.
func TestFirstBareFilterFieldParam_Deterministic(t *testing.T) {
	q := url.Values{"title": {"a"}, "genre": {"b"}, "narrator": {"c"}}.Encode()
	first, bare := firstBareFilterFieldParam(queryCtx(t, q))
	require.True(t, bare)
	for i := 0; i < 25; i++ {
		got, ok := firstBareFilterFieldParam(queryCtx(t, q))
		require.True(t, ok)
		require.Equal(t, first, got, "reported field must be stable across calls")
	}
	// Sorted order, so the alphabetically-first offender is named.
	require.Equal(t, "genre", first)
}

func TestFirstBareFilterFieldParam_NoParamsIsClean(t *testing.T) {
	_, bare := firstBareFilterFieldParam(queryCtx(t, ""))
	require.False(t, bare)

	_, bare = firstBareFilterFieldParam(queryCtx(t, "limit=50&offset=0"))
	require.False(t, bare)
}
