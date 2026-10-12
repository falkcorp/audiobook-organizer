// file: internal/plugins/metafetch/asin_backfill_pcache_test.go
// version: 1.0.0
// guid: 6a3e9c04-1f72-4b8d-9e25-c0d84b7a1f36
// last-edited: 2026-10-11

package metafetch

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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

// The backfill's Audible identity searches go through the shared cache: two
// books asking the same question make one request, a later run replays the
// stored answers (an empty one included) with no request, and a failed
// search stores nothing. Synthetic fixtures only.
func TestASINBackfill_UsesP1(t *testing.T) {
	withSharedProviderCache(t)
	product := metadata.AudibleIdentity{ASIN: "B0SYNTH001", Title: "Title 000123", Authors: []string{"Author 07"}, RuntimeMin: 600}
	fa := &fakeAudible{byTitle: map[string][]metadata.AudibleIdentity{"title 000123": {product}}}
	p, s := newASINTestPlugin(t, fa)
	aid := mkAuthor(t, s, "Author 07")
	mkBook(t, s, &database.Book{Title: "Title 000123", AuthorID: &aid, FilePath: "/synthetic/a.m4b"})
	mkBook(t, s, &database.Book{Title: "Title 000123", AuthorID: &aid, FilePath: "/synthetic/b.m4b"})
	mkBook(t, s, &database.Book{Title: "Title 000404", AuthorID: &aid, FilePath: "/synthetic/c.m4b"})

	runASIN(t, p, `{"dry_run":true,"workers":1}`)
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

	runASIN(t, p, `{"dry_run":true,"workers":1,"retry_after_days":0}`)
	if got := fa.calls.Load(); got != 2 {
		t.Fatalf("second run made %d searches in total, want 2: every answer, the empty one included, is replayed", got)
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
