// file: internal/server/handlers/metadata_cache_unreviewable_bucket_test.go
// version: 1.4.1
// guid: 3b7e91c4-58d2-4a6f-9e13-c0a4f27d8b95
// last-edited: 2026-10-10

// GET /metadata/cache/review?bucket=unreviewable lists the books the review
// rail's chips count but the default list drops (owner request 2026-09-27:
// "the 11324 with no candidates let me click on the chips"). A chip whose
// count disagrees with the rows it shows is the bug the whole rail has been
// fixed for twice already, so every assertion here is chip count == rows.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

type unreviewableRowBody struct {
	Book struct {
		ID                string `json:"id"`
		Title             string `json:"title"`
		Author            string `json:"author"`
		FilePath          string `json:"file_path"`
		DurationSeconds   int    `json:"duration_seconds"`
		StoredDurationSec int    `json:"stored_duration_seconds"`
	} `json:"book"`
	Candidate    *json.RawMessage `json:"candidate"`
	Status       string           `json:"status"`
	Error        string           `json:"error_message"`
	ReviewStatus string           `json:"review_status"`
	FetchedAt    *time.Time       `json:"fetched_at"`
	IsFresh      *bool            `json:"is_fresh"`
}

type unreviewableBody struct {
	Data struct {
		Results              []unreviewableRowBody `json:"results"`
		Bucket               string                `json:"bucket"`
		TotalCount           int                   `json:"total_count"`
		Truncated            bool                  `json:"truncated"`
		Matched              int                   `json:"matched"`
		NoMatch              int                   `json:"no_match"`
		Errors               int                   `json:"errors"`
		Stale                int                   `json:"stale"`
		Unreviewable         int                   `json:"unreviewable"`
		ResolvedNoCandidates int                   `json:"resolved_no_candidates"`
		BulkApplyMaxItems    int                   `json:"bulk_apply_max_items"`
		ByCause              struct {
			Orphaned     int `json:"orphaned"`
			NoCandidates int `json:"no_candidates"`
			DecodeErrors int `json:"decode_errors"`
		} `json:"unreviewable_by_cause"`
	} `json:"data"`
}

// unreviewableFixture: two reviewable rows (one stale), two pending
// no-candidate rows (one stale), one resolved no-candidate row, one decode
// error, and one orphan. GetBookFiles is NOT stubbed: the bucket must build
// its rows from the batch book read alone, and the strict mock fails the test
// if any per-row file read happens.
//
// allowFileReads is for the DEFAULT list, whose rows are built with full book
// info (BuildCandidateBookInfo reads each book's files).
func unreviewableFixture(t *testing.T, allowFileReads bool) *handlers.MetadataCacheHandler {
	t.Helper()
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	// No owner rejections unless a test says otherwise (applygate.ReasonOwnerRejected).
	store.EXPECT().ScanPrefix(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetRaw(mock.Anything).Return(nil, nil).Maybe() // authority lists: no known people
	store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(map[string][]database.BookFileCore{}, nil).Maybe()
	if allowFileReads {
		store.EXPECT().GetBookFiles(mock.Anything).Return(nil, nil).Maybe()
	}
	// The loader reads every row's files in ONE batch call per chunk (shared
	// by the legacy filter, the resolver and the book info; the expectation
	// is set where the store is built). That batch is the only file read; a
	// per-row GetBookFiles still fails the strict mock.
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)

	now := time.Now()
	old := now.Add(-2 * database.MetadataCacheTTL)
	strptr := func(s string) *string { return &s }
	intptr := func(n int) *int { return &n }

	svc.EXPECT().ListCachedSummaries(mock.Anything).Return([]metafetch.MetadataCacheSummary{
		{BookID: "rev-fresh", FetchedAt: now},
		{BookID: "rev-stale", FetchedAt: old},
		{BookID: "empty-fresh", FetchedAt: now},
		{BookID: "empty-stale", FetchedAt: old},
		{BookID: "resolved-empty", FetchedAt: now},
		{BookID: "broken", FetchedAt: now},
		{BookID: "gone", FetchedAt: now},
	}, nil)

	store.EXPECT().GetBooksByIDs(mock.Anything).Return([]database.Book{
		{ID: "rev-fresh", Title: "Reviewable"},
		{ID: "rev-stale", Title: "Reviewable Stale"},
		{
			ID: "empty-fresh", Title: "Dune", FilePath: "/books/Dune",
			Author: &database.Author{Name: "Frank Herbert"}, Duration: intptr(75600),
		},
		{ID: "empty-stale", Title: "Old Empty"},
		{ID: "resolved-empty", Title: "Settled", MetadataReviewStatus: strptr("no_match")},
		{ID: "broken", Title: "Broken"},
	}, nil)
	// "gone" misses the batch and its point read finds nothing: orphaned.
	store.EXPECT().GetBookByID("gone").Return(nil, nil)

	raw, err := json.Marshal(map[string]any{"title": "T", "score": 0.9})
	require.NoError(t, err)
	with := func() *metafetch.MetadataCandidateCache {
		return &metafetch.MetadataCandidateCache{Candidates: []json.RawMessage{raw}}
	}
	svc.EXPECT().GetCachedCandidates("rev-fresh").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("rev-stale").Return(with(), true, nil)
	svc.EXPECT().GetCachedCandidates("empty-fresh").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("empty-stale").Return(nil, false, nil)
	svc.EXPECT().GetCachedCandidates("resolved-empty").Return(&metafetch.MetadataCandidateCache{}, true, nil)
	svc.EXPECT().GetCachedCandidates("broken").Return(&metafetch.MetadataCandidateCache{
		Candidates: []json.RawMessage{json.RawMessage(`"not an object"`)},
	}, true, nil)

	return handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil)
}

func TestGetCacheReviewResults_UnreviewableBucketMatchesTheChips(t *testing.T) {
	h := unreviewableFixture(t, false)
	c, w := reviewCtx("all=true&bucket=unreviewable")
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body unreviewableBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	d := body.Data

	assert.Equal(t, "unreviewable", d.Bucket)
	assert.Equal(t, 5000, d.BulkApplyMaxItems, "unset bulk_apply_max_items reports applycap.Default")
	assert.False(t, d.Truncated)

	byStatus := map[string][]unreviewableRowBody{}
	for _, r := range d.Results {
		byStatus[r.Status] = append(byStatus[r.Status], r)
		assert.Nil(t, r.Candidate, "an unreviewable row has no candidate to show")
		require.NotNil(t, r.IsFresh, "every listed row carries its age")
		require.NotNil(t, r.FetchedAt)
	}

	// Chip count == rows listed, per cause.
	assert.Equal(t, d.ByCause.NoCandidates, len(byStatus["no_candidates"]))
	assert.Equal(t, 2, d.ByCause.NoCandidates)
	assert.Equal(t, d.ResolvedNoCandidates, len(byStatus["resolved_no_candidates"]))
	assert.Equal(t, 1, d.ResolvedNoCandidates)
	assert.Equal(t, d.Errors, len(byStatus["decode_error"]))
	assert.Equal(t, 1, d.Errors)
	assert.Equal(t, d.ByCause.DecodeErrors, len(byStatus["decode_error"]))
	// Orphans are counted and never listed: there is no book to show.
	assert.Equal(t, 1, d.ByCause.Orphaned)
	assert.Equal(t, 4, d.TotalCount, "total_count is the size of the bucket")
	assert.Len(t, d.Results, 4)

	// The summary is the same one the default list reports.
	assert.Equal(t, 2, d.Matched)
	assert.Equal(t, d.ByCause.Orphaned+d.ByCause.NoCandidates+d.ByCause.DecodeErrors, d.Unreviewable)

	// Stale: rows marked stale here plus stale reviewable rows == `stale`.
	staleHere := 0
	for _, r := range d.Results {
		if !*r.IsFresh {
			staleHere++
		}
	}
	assert.Equal(t, 1, staleHere, "only empty-stale in this bucket is past the TTL")
	assert.Equal(t, 2, d.Stale, "empty-stale here + rev-stale in the reviewable list")

	// The row carries what the rail needs to list and search the book.
	var dune unreviewableRowBody
	for _, r := range byStatus["no_candidates"] {
		if r.Book.ID == "empty-fresh" {
			dune = r
		}
	}
	assert.Equal(t, "Dune", dune.Book.Title)
	assert.Equal(t, "Frank Herbert", dune.Book.Author)
	assert.Equal(t, "/books/Dune", dune.Book.FilePath)
	assert.Equal(t, 75600, dune.Book.StoredDurationSec)
	assert.Zero(t, dune.Book.DurationSeconds,
		"no file read, so no canonical runtime: the stored duration is labelled as such")
	assert.Empty(t, dune.ReviewStatus, "nobody has ruled on it")

	assert.Equal(t, "no_match", byStatus["resolved_no_candidates"][0].ReviewStatus)
	assert.Contains(t, byStatus["decode_error"][0].Error, "will not decode")
}

// The default list must not change: same rows, same counts, no bucket field.
func TestGetCacheReviewResults_DefaultBucketUnchanged(t *testing.T) {
	h := unreviewableFixture(t, true)
	c, w := reviewCtx("all=true")
	h.GetCacheReviewResults(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	_, hasBucket := body.Data["bucket"]
	assert.False(t, hasBucket)
	var results []map[string]any
	require.NoError(t, json.Unmarshal(body.Data["results"], &results))
	assert.Len(t, results, 2, "only the two rows with a decodable candidate")
	var total int
	require.NoError(t, json.Unmarshal(body.Data["total_count"], &total))
	assert.Equal(t, 2, total)
}

func TestGetCacheReviewResults_RejectsUnknownBucket(t *testing.T) {
	store := handlersmocks.NewMockMetadataCacheBookStore(t)
	// No owner rejections unless a test says otherwise (applygate.ReasonOwnerRejected).
	store.EXPECT().ScanPrefix(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetRaw(mock.Anything).Return(nil, nil).Maybe() // authority lists: no known people
	store.EXPECT().GetBookFilesForIDsCore(mock.Anything).Return(map[string][]database.BookFileCore{}, nil).Maybe()
	svc := handlersmocks.NewMockMetadataCacheFetchService(t)
	h := handlers.NewMetadataCacheHandler(store, svc, nil, nil, nil)
	c, w := reviewCtx("bucket=everything")
	h.GetCacheReviewResults(c)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
