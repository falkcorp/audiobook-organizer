// file: internal/plugins/metafetch/asin_backfill_pcache_test.go
// version: 1.1.0
// guid: 6a3e9c04-1f72-4b8d-9e25-c0d84b7a1f36
// last-edited: 2026-10-11

package metafetch

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func withSharedProviderCache(t *testing.T) {
	t.Helper()
	prevMF, prevTTL := config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays
	t.Cleanup(func() {
		config.AppConfig.MetadataFetch, config.AppConfig.MetadataFetchCacheTTLDays = prevMF, prevTTL
	})
	config.AppConfig.MetadataFetch = config.DefaultMetadataFetchConfig()
	config.AppConfig.MetadataFetchCacheTTLDays = 180
}

func pcacheKeys(t *testing.T, s *database.PebbleStore, prefix string) []string {
	t.Helper()
	pairs, err := s.ScanPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, kv.Key)
	}
	return out
}

// pcacheEntry reads the one P1 entry under prefix.
func pcacheEntry(t *testing.T, s *database.PebbleStore, key string) database.PCacheEntry {
	t.Helper()
	raw, err := s.GetRaw(key)
	if err != nil || raw == nil {
		t.Fatalf("GetRaw(%s) = %v, %v", key, raw, err)
	}
	var e database.PCacheEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// ageMissMarker rewrites book's no-match marker to have been written ago.
func ageMissMarker(t *testing.T, s *database.PebbleStore, bookID string, ago time.Duration) {
	t.Helper()
	data, err := json.Marshal(asinMissMarker{At: time.Now().Add(-ago).UTC(), Outcome: "no_match"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRaw(asinMissKey(bookID), data); err != nil {
		t.Fatal(err)
	}
}

// The backfill's Audible identity searches go through the shared cache: two
// books asking the same question make one request, and a book first looked at
// on a later run replays the stored answer (an empty one included) with no
// request. Synthetic fixtures only; every question here has no answer, so no
// ASIN is written and each book gets a no-match marker.
func TestASINBackfill_UsesP1(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	mkBook(t, s, &database.Book{Title: "Title 000404", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})
	mkBook(t, s, &database.Book{Title: "Title 000404", AuthorID: &aid, FilePath: "/synthetic/b.m4b"})
	mkBook(t, s, &database.Book{Title: "Title 000405", AuthorID: &aid, FilePath: "/synthetic/c.m4b"})

	runASIN(t, p, `{"dry_run":false,"workers":1}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("first run made %d Audible searches, want 2 (one per distinct question)", got)
	}
	keys := pcacheKeys(t, s, database.PCacheQuestionPrefix)
	if len(keys) != 2 {
		t.Fatalf("pcache keys = %v, want the two questions", keys)
	}
	for _, k := range keys {
		if !strings.HasPrefix(k, "pcache:q:audible:identity_search:") {
			t.Fatalf("unexpected key %q", k)
		}
	}

	// A copy added later is first looked at on the next run: it replays the
	// stored (empty) answer; the three books with fresh markers are skipped.
	d := mkBook(t, s, &database.Book{Title: "Title 000404", AuthorID: &aid, FilePath: "/synthetic/d.m4b"})
	runASIN(t, p, `{"dry_run":false,"workers":1}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("second run made %d searches in total, want 2: the new copy replays the stored empty answer", got)
	}
	pointed, err := database.PCachePointedKeys(s, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pointed["audible"]) != 1 {
		t.Fatalf("the new copy's pointer row = %v, want its one question", pointed)
	}
}

// Once a book's no-match marker is past the retry window, the retry asks
// Audible live even though a stored answer is still inside its TTL, and
// stores the fresh answer.
func TestASINBackfill_RetryWindowPassedAsksLive(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	a := mkBook(t, s, &database.Book{Title: "Title 000406", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})

	runASIN(t, p, `{"dry_run":false}`)
	if got := fa.calls.Load(); got != 1 {
		t.Fatalf("first run made %d searches, want 1", got)
	}
	keys := pcacheKeys(t, s, database.PCacheQuestionPrefix)
	if len(keys) != 1 {
		t.Fatalf("pcache keys = %v, want one", keys)
	}
	first := pcacheEntry(t, s, keys[0])

	runASIN(t, p, `{"dry_run":false}`)
	if got := fa.calls.Load(); got != 1 {
		t.Fatalf("inside the retry window the book is skipped; searches = %d, want 1", got)
	}

	ageMissMarker(t, s, a.ID, 31*24*time.Hour)
	time.Sleep(10 * time.Millisecond) // a fresh answer's asked_at must differ
	runASIN(t, p, `{"dry_run":false}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("past the retry window the book is asked live; searches = %d, want 2", got)
	}
	if again := pcacheEntry(t, s, keys[0]); !again.AskedAt.After(first.AskedAt) {
		t.Fatalf("the fresh answer was not stored: asked_at %s, first %s", again.AskedAt, first.AskedAt)
	}
}

// retry_after_days 0 means "look at every book again": every search asks
// Audible live.
func TestASINBackfill_RetryZeroAsksLive(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	mkBook(t, s, &database.Book{Title: "Title 000407", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})

	runASIN(t, p, `{"dry_run":false}`)
	runASIN(t, p, `{"dry_run":false,"retry_after_days":0}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("retry_after_days 0 replayed a stored answer: searches = %d, want 2", got)
	}
}

// A dry run writes nothing: no answer row and no pointer row. It may read a
// stored answer.
func TestASINBackfill_DryRunWritesNoPCacheRows(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	a := mkBook(t, s, &database.Book{Title: "Title 000408", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})
	b := mkBook(t, s, &database.Book{Title: "Title 000408", AuthorID: &aid, FilePath: "/synthetic/b.m4b"})
	c := mkBook(t, s, &database.Book{Title: "Title 000409", AuthorID: &aid, FilePath: "/synthetic/c.m4b"})

	runASIN(t, p, `{"dry_run":true,"workers":1,"book_ids":["`+b.ID+`","`+c.ID+`"]}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("dry run searches = %d, want 2", got)
	}
	if keys := pcacheKeys(t, s, database.PCachePrefix); len(keys) != 0 {
		t.Fatalf("a dry run wrote pcache rows: %v", keys)
	}

	// A live run stores book a's answer; a dry run for book b then replays it
	// without writing b's pointer row.
	runASIN(t, p, `{"dry_run":false,"book_ids":["`+a.ID+`"]}`)
	if got := fa.calls.Load(); got != 3 {
		t.Fatalf("searches after the live run = %d, want 3", got)
	}
	before := pcacheKeys(t, s, database.PCachePrefix)
	runASIN(t, p, `{"dry_run":true,"book_ids":["`+b.ID+`"]}`)
	if got := fa.calls.Load(); got != 3 {
		t.Fatalf("the dry run did not read the stored answer: searches = %d, want 3", got)
	}
	if after := pcacheKeys(t, s, database.PCachePrefix); len(after) != len(before) {
		t.Fatalf("a dry run wrote pcache rows: before %v, after %v", before, after)
	}
}

func TestASINBackfill_FailedSearchIsNotCached(t *testing.T) {
	withSharedProviderCache(t)
	fa := &fakeAudible{err: errors.New("503 from the provider")}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	mkBook(t, s, &database.Book{Title: "Title 000500", AuthorID: &aid, FilePath: "/synthetic/d.m4b"})

	runASIN(t, p, `{"dry_run":true}`)
	if keys := pcacheKeys(t, s, database.PCachePrefix); len(keys) != 0 {
		t.Fatalf("a failed search wrote pcache keys: %v", keys)
	}
}
