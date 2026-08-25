// file: internal/server/handlers/abs/browse_sort_test.go
// version: 1.0.0
// guid: 9c4e2f81-3a76-4b50-8d19-6e2b7c05af34
// last-edited: 2026-08-24

package abs

import (
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// TestAbsSortField pins the mapping from the dotted sort keys Audiobookshelf
// clients send to the store's sort field names.
//
// The regression this guards: the whole parser used to be
// `strings.Contains(sort, "title")`, so `media.metadata.publishedYear` mapped
// to "" and the store returned rows in book-ID order behind a 200 OK.
func TestAbsSortField(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The reported bug.
		{"published year, dotted", "media.metadata.publishedYear", "year"},
		{"published year, bare", "publishedYear", "year"},
		{"published year, lowercase", "media.metadata.publishedyear", "year"},

		// Must keep working -- the only key that worked before.
		{"title, dotted", "media.metadata.title", "title"},
		{"title ignore articles", "media.metadata.titleIgnoreArticles", "title"},

		// Everything else that was silently dropped.
		{"author", "media.metadata.authorName", "author"},
		{"author last-first", "media.metadata.authorNameLF", "author"},
		{"narrator", "media.metadata.narratorName", "narrator"},
		{"series", "media.metadata.seriesName", "series"},
		{"added at", "addedAt", "created_at"},
		{"updated at", "updatedAt", "updated_at"},
		{"duration", "media.duration", "duration"},
		{"size", "size", "file_size"},

		// Unrecognised stays empty rather than guessing.
		{"empty", "", ""},
		{"unknown key", "media.metadata.nonsense", ""},
		{"dot only", ".", ""},

		// Guard the old bug directly: a key merely CONTAINING "title" as a
		// substring of another word must not be treated as a title sort.
		{"subtitle is not title", "media.metadata.subtitle", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := absSortField(tc.in); got != tc.want {
				t.Fatalf("absSortField(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAbsItemFilterSort checks the filter the handler actually builds, including
// the desc flag, which was previously set but never consulted because SortBy
// was empty for every non-title sort.
func TestAbsItemFilterSort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(q string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?"+q, nil)
		return c
	}

	t.Run("year ascending", func(t *testing.T) {
		f := absItemFilter(newCtx("sort=media.metadata.publishedYear"))
		if f.SortBy != "year" {
			t.Fatalf("SortBy = %q, want year", f.SortBy)
		}
		if !f.SortAscending {
			t.Fatal("SortAscending = false, want true when desc is absent")
		}
	})

	t.Run("year descending", func(t *testing.T) {
		f := absItemFilter(newCtx("sort=media.metadata.publishedYear&desc=1"))
		if f.SortBy != "year" {
			t.Fatalf("SortBy = %q, want year", f.SortBy)
		}
		if f.SortAscending {
			t.Fatal("SortAscending = true, want false when desc=1")
		}
	})

	t.Run("no sort param leaves SortBy empty", func(t *testing.T) {
		if f := absItemFilter(newCtx("")); f.SortBy != "" {
			t.Fatalf("SortBy = %q, want empty", f.SortBy)
		}
	})

	t.Run("base filter invariants survive", func(t *testing.T) {
		f := absItemFilter(newCtx("sort=media.metadata.publishedYear"))
		if f.IsPrimaryVersion == nil || !*f.IsPrimaryVersion {
			t.Fatal("IsPrimaryVersion must stay set; dropping it changes which rows are returned")
		}
		if f.LibraryState != "organized" || !f.ExcludeQuarantined {
			t.Fatalf("base filter altered: state=%q excludeQuarantined=%v", f.LibraryState, f.ExcludeQuarantined)
		}
	})
}

// TestEnabledSortIndexDefaultsAreRecognised guards the seam between the config
// default and the store.
//
// EnabledSortIndexes is a []string validated only at runtime: cmd/root.go
// prints a warning for unknown entries and carries on. So a typo in the
// default ("file_sizes", "createdAt") would leave that sort permanently
// unindexed and unordered, with the only signal a startup line nobody reads.
// Assert the names round-trip through the store's own validator.
func TestEnabledSortIndexDefaultsAreRecognised(t *testing.T) {
	// Must match config.go's viper.SetDefault("enabled_sort_indexes", ...).
	defaults := []string{"year", "author"}

	if unknown := database.SetEnabledSortIndexes(defaults); len(unknown) > 0 {
		t.Fatalf("config default names the store does not recognise: %v", unknown)
	}
	t.Cleanup(func() { database.SetEnabledSortIndexes(defaults) })

	// Sorts that must actually be ORDERED for the user. "title" is always
	// indexed and not configurable; year is the reported bug; author is the
	// other field a library is commonly sorted by.
	for key, want := range map[string]string{
		"media.metadata.title":         "title",
		"media.metadata.publishedYear": "year",
		"media.metadata.authorName":    "author",
		"media.metadata.authorNameLF":  "author",
	} {
		got := absSortField(key)
		if got != want {
			t.Errorf("absSortField(%q) = %q, want %q", key, got, want)
			continue
		}
		if got != "title" && !database.CanPushDownSort(got) {
			t.Errorf("sort %q -> %q is NOT push-downable; the app would show unordered results", key, got)
		}
	}

	// Sorts the client menu offers that we deliberately leave UNINDEXED,
	// because each index taxes scan insert throughput. They must still map to
	// a real store field so warnUnindexedSort can name it -- but they are
	// expected to be off.
	//
	// This half of the assertion is what makes the trade-off explicit: if
	// someone widens the default later, this fails and they have to
	// acknowledge the insert-throughput cost rather than drift into it.
	for key, want := range map[string]string{
		"addedAt":        "created_at",
		"updatedAt":      "updated_at",
		"media.duration": "duration",
		"size":           "file_size",
	} {
		got := absSortField(key)
		if got != want {
			t.Errorf("absSortField(%q) = %q, want %q", key, got, want)
			continue
		}
		if database.CanPushDownSort(got) {
			t.Errorf("sort %q -> %q is indexed, but the default is deliberately narrow; update this test AND config.go's comment together", key, got)
		}
	}

	// Negative control: a bogus name must be REPORTED, not silently accepted.
	// If the validator were inert, the first assertion would prove nothing.
	if unknown := database.SetEnabledSortIndexes([]string{"year", "definitely_not_a_field"}); len(unknown) != 1 {
		t.Fatalf("validator did not report a bogus field: unknown=%v", unknown)
	}
}
