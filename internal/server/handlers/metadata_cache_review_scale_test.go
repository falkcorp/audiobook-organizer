// file: internal/server/handlers/metadata_cache_review_scale_test.go
// version: 1.6.0
// guid: 3f7b2d90-5c1e-4a86-9e43-8b6d1f0c2a75
// last-edited: 2026-10-09

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
//   - 1-4 files per book, ten candidates per row with a ~1 KB description,
//     an 8-step score breakdown and two category tags;
//   - a spread of review statuses, empty and undecodable rows, stale rows.
func reviewSeed(tb testing.TB, n int) (*database.PebbleStore, *metafetch.Service) {
	tb.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(tb.TempDir(), "review-db"))
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = store.Close() })

	desc := strings.Repeat("A long publisher description of the book. ", 25)
	breakdown := seedScoreBreakdown()
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
					"score_breakdown": breakdown,
					"category_tags":   []string{fmt.Sprintf("Category %02d", k), "Category 99"},
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

// seedScoreBreakdown is a synthetic 8-step derivation the size of the
// scorer's: a base, six multipliers and one additive term, each with a label
// and an explanation sentence. Running totals replay, so it reads as a real
// breakdown to anything that recomposes it.
func seedScoreBreakdown() *metafetch.ScoreBreakdown {
	steps := []metafetch.ScoreStep{{
		ID: "base", Label: "Base similarity", Op: metafetch.ScoreOpBase, Operand: 0.8, Running: 0.8,
		Detail: "Weighted title and author similarity between the book and the candidate.",
	}}
	running := 0.8
	for i, id := range []string{"compilation", "length", "series", "narrator", "language", "edition"} {
		running *= 0.98
		steps = append(steps, metafetch.ScoreStep{
			ID: id, Label: fmt.Sprintf("Penalty %d (%s)", i+1, id), Op: metafetch.ScoreOpMultiply,
			Operand: 0.98, Running: running,
			Detail: fmt.Sprintf("The candidate's %s differs slightly from the book's, so the score is scaled down.", id),
		})
	}
	running += 0.05
	steps = append(steps, metafetch.ScoreStep{
		ID: "rich_metadata", Label: "Rich metadata bonus", Op: metafetch.ScoreOpAdd, Operand: 0.05, Running: running,
		Detail: "The candidate carries a cover, an ISBN, a narrator and a duration.", Capped: true,
	})
	return &metafetch.ScoreBreakdown{Score: running, Steps: steps}
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
	memo := metabatch.NewFolderMemo(store)
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
	set, err := loadCacheRows(ctx, store, svc)
	require.NoError(t, err)
	snap, err := newReviewSnapshotBuilder(store, svc).build(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, wantOrphaned, snap.orphaned())
	require.Len(t, set.rows, len(want))
	require.Len(t, snap.rows, len(want))
	legacyFiltered := 0
	for i, w := range want {
		got := snap.rows[i]
		require.Equal(t, w.id, got.sum.BookID)
		lr := set.rows[i]
		wantCount, wantFirst := 0, json.RawMessage(nil)
		if w.entry != nil && len(w.entry.Candidates) > 0 {
			wantCount, wantFirst = len(w.entry.Candidates), w.entry.Candidates[0]
		}
		require.Equal(t, wantCount, lr.candidateCount, "candidates after the legacy filter for %s", w.id)
		require.Equal(t, wantFirst, lr.first, "first candidate for %s", w.id)
		require.Equal(t, cacheRowLastChecked(w.entry, lr.sum.FetchedAt), lr.lastChecked, "lastChecked for %s", w.id)
		require.Nil(t, got.book, "the snapshot keeps no book rows")
		require.Nil(t, got.first, "the snapshot keeps no raw candidate")
		require.Equal(t, w.searchable, got.searchable, "searchable for %s", w.id)
		require.Equal(t, w.info, metabatch.BuildCandidateBookInfoWithFacts(lr.book, got.files), "book info for %s", w.id)
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
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)

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
		require.Nil(t, xr.Candidate.ScoreBreakdown, "the index drops score breakdowns")
		require.Nil(t, xr.Candidate.CategoryTags, "the index drops category tags")
		full := *ar.Candidate
		full.Description = ""
		full.ScoreBreakdown = nil
		full.CategoryTags = nil
		require.Equal(t, full, *xr.Candidate, "only the description, score breakdown and category tags differ")
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
	// A detail lookup re-reads only the asked books; its counts cover them.
	require.Equal(t, 2, byIDs.Data.TotalCount)

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

// TestMetadataCache_IndexViewOmitsScoreBreakdown: no index row carries a score
// breakdown or category tags, while each still carries the fields the page
// filters, groups and pins on (score, title, hash).
func TestMetadataCache_IndexViewOmitsScoreBreakdown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 120)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)

	idx, _ := serveReview(t, h, "all=true&view=index")
	require.Positive(t, len(idx.Data.Results))
	for _, r := range idx.Data.Results {
		require.NotNil(t, r.Candidate)
		require.Nil(t, r.Candidate.ScoreBreakdown, "book %s", r.Book.ID)
		require.Nil(t, r.Candidate.CategoryTags, "book %s", r.Book.ID)
		require.NotEmpty(t, r.Candidate.Title)
		require.Positive(t, r.Candidate.Score)
		require.NotEmpty(t, r.CandidateHash)
	}
}

// TestMetadataCache_DetailViewKeepsScoreBreakdown is the anti-over-suppression
// check: the ids= detail rows the evidence panel is fed from keep the full
// breakdown and tags, and their hash is the index row's, so the page swaps the
// detail candidate in.
func TestMetadataCache_DetailViewKeepsScoreBreakdown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 120)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)

	idx, _ := serveReview(t, h, "all=true&view=index")
	require.GreaterOrEqual(t, len(idx.Data.Results), 3)
	pick := []string{idx.Data.Results[0].Book.ID, idx.Data.Results[2].Book.ID}
	detail, _ := serveReview(t, h, "ids="+strings.Join(pick, ","))
	require.Len(t, detail.Data.Results, 2)
	want := seedScoreBreakdown()
	for i, d := range detail.Data.Results {
		require.NotNil(t, d.Candidate)
		require.NotNil(t, d.Candidate.ScoreBreakdown, "book %s", d.Book.ID)
		require.Len(t, d.Candidate.ScoreBreakdown.Steps, 8)
		require.Equal(t, want, d.Candidate.ScoreBreakdown)
		require.Len(t, d.Candidate.CategoryTags, 2)
		require.NotEmpty(t, d.Candidate.Description)
		ix := idx.Data.Results[[]int{0, 2}[i]]
		require.Equal(t, ix.Book.ID, d.Book.ID)
		require.Equal(t, ix.CandidateHash, d.CandidateHash, "the index pins the full candidate")
		require.Equal(t, ix.Candidate.Score, d.Candidate.Score)
		require.Equal(t, ix.Candidate.Title, d.Candidate.Title)
	}
}

// TestMetadataCache_IndexViewDoesNotMutateSnapshot: the index clears fields on
// a copy of the snapshot's candidate. A second request against the same
// snapshot -- an all=true listing and an ids= detail -- still sees every
// breakdown, and a second index request is byte-identical to the first.
func TestMetadataCache_IndexViewDoesNotMutateSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 120)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)

	first, firstBytes := serveReview(t, h, "all=true&view=index")
	all, _ := serveReview(t, h, "all=true")
	require.Len(t, all.Data.Results, len(first.Data.Results))
	for _, r := range all.Data.Results {
		require.NotNil(t, r.Candidate.ScoreBreakdown, "book %s lost its breakdown after an index request", r.Book.ID)
		require.NotEmpty(t, r.Candidate.CategoryTags, "book %s", r.Book.ID)
	}
	detail, _ := serveReview(t, h, "ids="+first.Data.Results[1].Book.ID)
	require.Len(t, detail.Data.Results, 1)
	require.NotNil(t, detail.Data.Results[0].Candidate.ScoreBreakdown)

	second, secondBytes := serveReview(t, h, "all=true&view=index")
	require.Equal(t, firstBytes, secondBytes)
	require.Equal(t, first.Data.Results, second.Data.Results)
}

// TestReviewSnapshot_StatusIsLive: a book ruled on after the snapshot was
// built is reported with its new status on the very next request -- the
// snapshot is never the source of a book's review status.
func TestReviewSnapshot_StatusIsLive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, svc := reviewSeed(t, 60)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
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
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			snap, err := newReviewSnapshotBuilder(store, svc).build(ctx, nil)
			require.NoError(b, err)
			runtime.GC()
			runtime.ReadMemStats(&after)
			b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/(1<<20), "retained_MB")
			runtime.KeepAlive(snap)
		}
	})
	// An apply op's footprint between two rebuilds: one cache row deleted and
	// one book written per applied book.
	const changedPerRound = 50
	bld, _ := incrBuilder(b, store, svc)
	b.Run("after_incremental_rebuild_50_changed", func(b *testing.B) {
		prev, err := bld.build(ctx, nil)
		require.NoError(b, err)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			for _, r := range prev.rows[i*changedPerRound%(len(prev.rows)-changedPerRound):][:changedPerRound] {
				require.NoError(b, store.DeleteMetadataCache(r.sum.BookID))
				_, err := store.ModifyBook(r.sum.BookID, func(bk *database.Book) error { st := "matched"; bk.MetadataReviewStatus = &st; return nil })
				require.NoError(b, err)
			}
			b.StartTimer()
			next, err := bld.build(ctx, prev)
			require.NoError(b, err)
			require.True(b, next.incremental)
			prev = next
		}
		b.ReportMetric(float64(len(prev.rows)), "rows")
	})
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
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
	// The index request while books are being written under the snapshot
	// (no rebuild in between): the overlay reads only the changed books.
	b.Run("after_warm_index_50_books_changed_since_snapshot", func(b *testing.B) {
		r, _ := serveReview(b, h, "limit=50&offset=3000")
		for _, x := range r.Data.Results {
			_, err := store.ModifyBook(x.Book.ID, func(bk *database.Book) error { st := "no_match"; bk.MetadataReviewStatus = &st; return nil })
			require.NoError(b, err)
		}
		var bytes int
		for i := 0; i < b.N; i++ {
			_, bytes = serveReview(b, h, "all=true&view=index")
		}
		b.ReportMetric(float64(bytes), "body_bytes")
	})
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

// The real store, through the real constructor, resolves the cache write
// counter -- without it the snapshot would rebuild only on marks and age.
func TestReviewSnapshot_RealStoreResolvesTheWriteCounter(t *testing.T) {
	store, svc := reviewSeed(t, 3)
	h := NewMetadataCacheHandler(store, svc, nil, nil, nil)
	require.NotNil(t, h.reviewSnap.gen)
	before := h.reviewSnap.gen()
	require.NoError(t, store.PutMetadataCache(&database.MetadataCandidateCache{BookID: "x"}))
	require.Equal(t, before+1, h.reviewSnap.gen())
}
