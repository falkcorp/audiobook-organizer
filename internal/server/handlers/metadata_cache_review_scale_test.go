// file: internal/server/handlers/metadata_cache_review_scale_test.go
// version: 1.0.0
// guid: 3f7b2d90-5c1e-4a86-9e43-8b6d1f0c2a75
// last-edited: 2026-10-02

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// reviewSeed builds a real Pebble store + metafetch service holding n books
// with a metadata-cache row each, shaped like production's review set:
//
//   - ~30% legacy rows (no current search fingerprint), which run the
//     legacy candidate filter and its runtime read;
//   - ~10% chapter-shaped titles ("07") filed as separate rows in a shared
//     folder, which make the resolver list that folder;
//   - 1-4 files per book, ten candidates per row with a ~1 KB description;
//   - a spread of review statuses, empty and undecodable rows, stale rows.
func reviewSeed(tb testing.TB, n int) (*database.PebbleStore, *metafetch.Service) {
	tb.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(tb.TempDir(), "review-db"))
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = store.Close() })

	desc := strings.Repeat("A long publisher description of the book. ", 25)
	now := time.Now()
	statuses := []string{"", "", "", "", "no_match", "matched", "audio_confirmed"}
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("Book Title %d", i)
		path := fmt.Sprintf("/lib/Author %d/Book %d", i%997, i)
		if i%10 == 0 {
			// Chapter rows: ten per set, one set per folder.
			set := i / 100
			title = fmt.Sprintf("%02d", (i/10)%10+1)
			path = fmt.Sprintf("/lib/Chapters %d/Set/%s.mp3", set, title)
		}
		b := &database.Book{Title: title, FilePath: path, Format: "mp3"}
		if st := statuses[i%len(statuses)]; st != "" {
			b.MetadataReviewStatus = &st
		}
		created, err := store.CreateBook(b)
		require.NoError(tb, err)
		for f := 0; f < 1+i%4; f++ {
			require.NoError(tb, store.CreateBookFile(&database.BookFile{
				ID:          fmt.Sprintf("f-%d-%d", i, f),
				BookID:      created.ID,
				FilePath:    fmt.Sprintf("%s/part%02d.mp3", path, f),
				TrackNumber: f + 1,
				Duration:    1800 + i%600,
				ITunesPath:  fmt.Sprintf("itunes/%d/%d", i, f),
			}))
		}
		entry := &database.MetadataCandidateCache{
			BookID:    created.ID,
			FetchedAt: now.Add(-time.Duration(i%60) * 24 * time.Hour),
		}
		switch {
		case i%23 == 0:
			// no candidates
		case i%41 == 0:
			entry.Candidates = []json.RawMessage{json.RawMessage(`"not an object"`)}
		default:
			for k := 0; k < 10; k++ {
				raw, merr := json.Marshal(map[string]any{
					"title": fmt.Sprintf("%s %d", title, k), "author": "Some Author",
					"source": []string{"audible", "google_books", "open_library"}[k%3],
					"score":  0.5 + float64(k%5)/10, "description": desc,
					"duration_sec": 3600 + k*60, "narrator": "Reader",
				})
				require.NoError(tb, merr)
				entry.Candidates = append(entry.Candidates, raw)
			}
		}
		if i%10 >= 3 { // ~70% written by the current search version
			entry.SearchFingerprint = metafetch.FingerprintPrefix + strconv.Itoa(i)
		}
		require.NoError(tb, store.PutMetadataCache(entry))
	}
	_, err = store.BackfillBookAtPathIndex(context.Background())
	require.NoError(tb, err)
	return store, metafetch.NewService(store)
}

// legacyReviewRows is the loader as it was before the snapshot (origin/main
// at 88a3c98e6): per row, GetCachedCandidates (whose legacy filter re-reads
// the book and range-scans its files), the resolver over the store, then --
// for the all=true page, i.e. every row -- BuildCandidateBookInfo (a third
// file read) plus the candidate decode and hash. It is the "before" of the
// benchmark and the oracle of the equivalence test.
type legacyRow struct {
	id         string
	entry      *metafetch.MetadataCandidateCache
	searchable bool
	info       metabatch.CandidateBookInfo
	hash       string
}

func legacyReviewRows(ctx context.Context, store cacheRowBookReader, svc *metafetch.Service) ([]legacyRow, int, error) {
	sums, err := svc.ListCachedSummaries(ctx)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]string, len(sums))
	for i := range sums {
		ids[i] = sums[i].BookID
	}
	books, err := store.GetBooksByIDs(ids)
	if err != nil {
		return nil, 0, err
	}
	byID := map[string]*database.Book{}
	for i := range books {
		byID[books[i].ID] = &books[i]
	}
	rows := make([]legacyRow, 0, len(sums))
	var bks []*database.Book
	orphaned := 0
	for _, s := range sums {
		b := byID[s.BookID]
		if b == nil {
			orphaned++
			continue
		}
		rows = append(rows, legacyRow{id: s.BookID})
		bks = append(bks, b)
	}
	memo := metabatch.NewFolderMemo()
	var g errgroup.Group
	g.SetLimit(reviewListConcurrency)
	for i := range rows {
		g.Go(func() error {
			e, _, cerr := svc.GetCachedCandidates(rows[i].id)
			if cerr == nil {
				rows[i].entry = e
			}
			rows[i].searchable = metabatch.ResolveCandidateSearchQueryMemo(store, bks[i], memo).Usable
			return nil
		})
	}
	_ = g.Wait()
	for i := range rows {
		rows[i].info = metabatch.BuildCandidateBookInfo(store, bks[i])
		if e := rows[i].entry; e != nil && len(e.Candidates) > 0 {
			var c metafetch.MetadataCandidate
			if json.Unmarshal(e.Candidates[0], &c) == nil {
				rows[i].hash = metafetch.CandidateHash(c)
			}
		}
	}
	return rows, orphaned, nil
}

// TestReviewSnapshot_MatchesTheLegacyLoader: on one seeded store, the batched
// loader yields, row for row, the same candidates after the legacy filter, the
// same searchability, the same book info and the same candidate hash as the
// per-row loader it replaced.
func TestReviewSnapshot_MatchesTheLegacyLoader(t *testing.T) {
	store, svc := reviewSeed(t, 400)
	ctx := context.Background()
	want, wantOrphaned, err := legacyReviewRows(ctx, store, svc)
	require.NoError(t, err)
	snap, err := buildReviewSnapshot(ctx, store, svc)
	require.NoError(t, err)
	require.Equal(t, wantOrphaned, snap.orphaned)
	require.Len(t, snap.rows, len(want))
	legacyFiltered := 0
	for i, w := range want {
		got := snap.rows[i]
		require.Equal(t, w.id, got.sum.BookID)
		require.Equal(t, w.entry, got.entry, "entry for %s", w.id)
		require.Equal(t, w.searchable, got.searchable, "searchable for %s", w.id)
		require.Equal(t, w.info, metabatch.BuildCandidateBookInfoWithFacts(got.book, got.files), "book info for %s", w.id)
		require.Equal(t, w.hash, got.hash, "hash for %s", w.id)
		if w.entry != nil && !strings.HasPrefix(w.entry.SearchFingerprint, metafetch.FingerprintPrefix) {
			legacyFiltered++
		}
	}
	require.Positive(t, legacyFiltered, "the seed must exercise the legacy filter")
}

type reviewResp struct {
	Data struct {
		Matched              int                         `json:"matched"`
		NoMatch              int                         `json:"no_match"`
		Errors               int                         `json:"errors"`
		TotalApplied         int                         `json:"total_applied"`
		Unreviewable         int                         `json:"unreviewable"`
		Stale                int                         `json:"stale"`
		ResolvedNoCandidates int                         `json:"resolved_no_candidates"`
		UnreviewableByCause  map[string]int              `json:"unreviewable_by_cause"`
		TotalCount           int                         `json:"total_count"`
		Truncated            bool                        `json:"truncated"`
		Results              []metabatch.CandidateResult `json:"results"`
	} `json:"data"`
}

func serveReview(t testing.TB, h *MetadataCacheHandler, query string) (reviewResp, int) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/audiobooks/metadata/cache/review?"+query, nil)
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var r reviewResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r))
	return r, w.Body.Len()
}

// TestReviewIndex_SameRowsAndCountsAsAll: the index the page now loads carries
// the same summary, the same rows in the same order with the same statuses,
// stale flags and pins as all=true; ids= returns exactly the asked rows in
// full; and limit/offset pages tile the bucket.
func TestReviewIndex_SameRowsAndCountsAsAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 300)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil, nil)

	all, _ := serveReview(t, h, "all=true")
	idx, _ := serveReview(t, h, "all=true&view=index")
	a, x := all.Data, idx.Data
	require.Equal(t, a.Matched, x.Matched)
	require.Equal(t, a.NoMatch, x.NoMatch)
	require.Equal(t, a.Errors, x.Errors)
	require.Equal(t, a.TotalApplied, x.TotalApplied)
	require.Equal(t, a.Unreviewable, x.Unreviewable)
	require.Equal(t, a.Stale, x.Stale)
	require.Equal(t, a.ResolvedNoCandidates, x.ResolvedNoCandidates)
	require.Equal(t, a.UnreviewableByCause, x.UnreviewableByCause)
	require.Equal(t, a.TotalCount, x.TotalCount)
	require.Len(t, x.Results, len(a.Results))
	require.Positive(t, len(a.Results))
	for i := range a.Results {
		ar, xr := a.Results[i], x.Results[i]
		require.Equal(t, ar.Book, xr.Book)
		require.Equal(t, ar.Status, xr.Status)
		require.Equal(t, *ar.Stale, *xr.Stale)
		require.Equal(t, ar.CandidateHash, xr.CandidateHash)
		require.Empty(t, xr.Candidate.Description, "the index drops descriptions")
		full := *ar.Candidate
		full.Description = ""
		require.Equal(t, full, *xr.Candidate, "only the description differs")
	}

	// stale == the refetch-all-stale set.
	staleIDs, err := StaleCachedBookIDs(context.Background(), store, svc)
	require.NoError(t, err)
	require.Equal(t, a.Stale, len(staleIDs))

	// ids= returns the full rows asked for, in bucket order.
	pick := []string{a.Results[5].Book.ID, a.Results[1].Book.ID, "no-such-book"}
	byIDs, _ := serveReview(t, h, "ids="+strings.Join(pick, ","))
	require.Len(t, byIDs.Data.Results, 2)
	require.Equal(t, a.Results[1], byIDs.Data.Results[0])
	require.Equal(t, a.Results[5], byIDs.Data.Results[1])
	require.Equal(t, a.TotalCount, byIDs.Data.TotalCount)

	// Pages tile the bucket with no gap or repeat.
	var paged []string
	for off := 0; off < a.TotalCount; off += 50 {
		p, _ := serveReview(t, h, fmt.Sprintf("limit=50&offset=%d", off))
		for _, r := range p.Data.Results {
			paged = append(paged, r.Book.ID)
		}
	}
	var allIDs []string
	for _, r := range a.Results {
		allIDs = append(allIDs, r.Book.ID)
	}
	require.Equal(t, allIDs, paged)

	// The unreviewable bucket's ids= lookup too.
	un, _ := serveReview(t, h, "all=true&bucket=unreviewable")
	require.Positive(t, len(un.Data.Results))
	one, _ := serveReview(t, h, "bucket=unreviewable&ids="+un.Data.Results[0].Book.ID)
	require.Equal(t, un.Data.Results[:1], one.Data.Results)
}

// TestReviewSnapshot_StatusIsLive: a book ruled on after the snapshot was
// built is reported with its new status on the very next request -- the
// snapshot is never the source of a book's review status.
func TestReviewSnapshot_StatusIsLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 60)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil, nil)
	before, _ := serveReview(t, h, "all=true")
	var target string
	for _, r := range before.Data.Results {
		if r.Status == "matched" {
			target = r.Book.ID
			break
		}
	}
	require.NotEmpty(t, target)
	_, err := store.ModifyBook(target, func(b *database.Book) error {
		st := "matched"
		b.MetadataReviewStatus = &st
		return nil
	})
	require.NoError(t, err)
	after, _ := serveReview(t, h, "all=true")
	require.Equal(t, before.Data.Matched-1, after.Data.Matched)
	require.Equal(t, before.Data.TotalApplied+1, after.Data.TotalApplied)
	for _, r := range after.Data.Results {
		if r.Book.ID == target {
			require.Equal(t, "applied", r.Status)
		}
	}
}

// BenchmarkReviewLoad measures the review page's load on a seeded Pebble
// store (no memdb: the pessimistic, pre-warmup path). REVIEW_BENCH_BOOKS sets
// the size (default 40000, prod's 39,689).
//
//	go test ./internal/server/handlers/ -run '^$' -bench BenchmarkReviewLoad -benchtime 1x
func BenchmarkReviewLoad(b *testing.B) {
	gin.SetMode(gin.TestMode)
	n := 40000
	if v, err := strconv.Atoi(os.Getenv("REVIEW_BENCH_BOOKS")); err == nil && v > 0 {
		n = v
	}
	seedStart := time.Now()
	store, svc := reviewSeed(b, n)
	b.Logf("seeded %d books in %s", n, time.Since(seedStart).Round(time.Millisecond))
	ctx := context.Background()

	b.Run("before_legacy_loader_all_rows", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			rows, _, err := legacyReviewRows(ctx, store, svc)
			require.NoError(b, err)
			b.ReportMetric(float64(len(rows)), "rows")
		}
	})
	b.Run("after_cold_snapshot_build", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := buildReviewSnapshot(ctx, store, svc)
			require.NoError(b, err)
		}
	})
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil, nil)
	_, _ = serveReview(b, h, "limit=1") // build the snapshot once
	for _, q := range []string{"all=true", "all=true&view=index", "limit=50&offset=1000", "ids=" + firstIDs(b, h, 50)} {
		b.Run("after_warm_"+strings.NewReplacer("=", "_", "&", "+", ",", "").Replace(q[:min(len(q), 24)]), func(b *testing.B) {
			var bytes int
			for i := 0; i < b.N; i++ {
				_, bytes = serveReview(b, h, q)
			}
			b.ReportMetric(float64(bytes), "body_bytes")
		})
	}
}

func firstIDs(b *testing.B, h *MetadataCacheHandler, n int) string {
	r, _ := serveReview(b, h, fmt.Sprintf("limit=%d&offset=2000", n))
	ids := make([]string, 0, n)
	for _, x := range r.Data.Results {
		ids = append(ids, x.Book.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}
