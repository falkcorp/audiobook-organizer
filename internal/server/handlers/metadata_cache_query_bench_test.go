// file: internal/server/handlers/metadata_cache_query_bench_test.go
// version: 1.1.0
// guid: b5d2c8e4-1f6a-4a37-9c08-7e4b3d2a6f91
// last-edited: 2026-10-10

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// syntheticReviewSnapshot is an in-memory snapshot of n rows shaped like
// production's review set and like reviewSeed's rows: ULID-length book ids,
// a spread of review statuses, rows with no candidate and undecodable ones,
// stale rows, and candidates the size of real ones (a ~1 KB description, an
// 8-step score breakdown, two category tags). Built directly, without a
// store, so the benchmark times the query and nothing else. With
// allReviewable every row carries a decodable candidate.
func syntheticReviewSnapshot(n int, allReviewable bool) *reviewSnapshot {
	desc := strings.Repeat("A long publisher description of the book. ", 25)
	breakdown := seedScoreBreakdown()
	statuses := []string{"", "", "", "", "no_match", "matched", "audio_confirmed"}
	langs := []string{"en", "English", "de", "", "spa"}
	sources := []string{"Audible", "Google Books", "Open Library"}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	snap := &reviewSnapshot{
		rows:      make([]snapshotRow, 0, n),
		books:     make(map[string]*database.Book, n),
		orphanIDs: map[string]struct{}{},
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("01J%023d", i)
		b := &database.Book{ID: id, Title: fmt.Sprintf("Title %06d", i), FilePath: fmt.Sprintf("/lib/Author %03d/Title %06d", i%997, i), Format: "m4b"}
		if st := statuses[i%len(statuses)]; st != "" {
			b.MetadataReviewStatus = &st
		}
		if l := langs[i%len(langs)]; l != "" {
			b.Language = &l
		}
		lr := loadedCacheRow{
			sum:         metafetch.MetadataCacheSummary{BookID: id, FetchedAt: start.Add(-time.Duration(i) * time.Minute)},
			lastChecked: start.Add(-time.Duration(i%60) * 24 * time.Hour),
			searchable:  true,
		}
		lr.files.Runtime = database.BookRuntime{Seconds: 36000 + i%600, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}
		lr.files.FileCount = 1
		sr := snapshotRow{loadedCacheRow: lr, title: b.Title, titleFold: strings.ToLower(b.Title)}
		switch {
		case !allReviewable && i%23 == 0:
			// no candidates
		case !allReviewable && i%41 == 0:
			sr.candidateCount = 1
			sr.decodeErr = errors.New("json: cannot unmarshal string into Go value of type metafetch.MetadataCandidate")
		default:
			c := &metafetch.MetadataCandidate{
				Title: fmt.Sprintf("Title %06d", i), Author: fmt.Sprintf("Author %03d", i%997), Narrator: "Reader",
				Source: sources[i%3], Score: 0.5 + float64(i%5)/10, Description: desc,
				Language: langs[(i+1)%len(langs)], DurationSec: 36000 + (i%7)*1000, DurationDeltaSec: (i % 7) * 1000,
				ScoreBreakdown: breakdown,
				CategoryTags:   []string{fmt.Sprintf("Category %02d", i%10), "Category 99"},
			}
			if i%2 == 0 {
				c.ASIN = fmt.Sprintf("B0%08d", i/2)
			}
			sr.candidateCount = 1
			sr.cand = c
			sr.hash = metafetch.CandidateHash(*c)
		}
		snap.books[id] = b
		snap.rows = append(snap.rows, sr)
	}
	return snap
}

// BenchmarkReviewQuery_40k times one evaluation (the per-keystroke work: the
// LRU is bypassed) over 40,000 rows, for the four shapes appendix D measured.
// base_build is the per-generation work the evaluations share (the overlay
// with no changed books, and the row derivation), reported for completeness.
func BenchmarkReviewQuery_40k(b *testing.B) {
	snap := syntheticReviewSnapshot(40000, false)
	set, err := overlayLiveBooks(snap, nil, nil, changedIDs())
	require.NoError(b, err)
	base := newReviewQueryBase(reviewBaseKey{snap: snap}, set, time.Now())
	chips := map[string]string{
		"source": "audible", "hide_applied": "true", "hide_rejected": "true", "hide_no_match": "true",
		"hide_runtime": "true", "min_confidence": "50",
	}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range chips {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	cases := []struct {
		name   string
		params map[string]string
	}{
		{"chips_only", chips},
		{"chips_substring_title", with(map[string]string{"q": "title 01"})},
		{"chips_re2_title", with(map[string]string{"q": `/^title 0*1\d/`})},
		{"worst_re2_every_row_sorted", map[string]string{"q": "/./", "sort": "title"}},
	}
	for _, tc := range cases {
		q := mustReviewQuery(b, tc.params)
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			var listed int
			for b.Loop() {
				listed = len(mustEvaluate(b, base, q).refs)
			}
			b.ReportMetric(float64(listed), "rows_listed")
		})
	}
	b.Run("base_build", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s, err := overlayLiveBooks(snap, nil, nil, changedIDs())
			if err != nil {
				b.Fatal(err)
			}
			_ = newReviewQueryBase(reviewBaseKey{snap: snap}, s, time.Now())
		}
	})
}

// TestReviewQuery_ResponseSizes40k measures the two response bodies at
// 40,000 synthetic rows: a 50-row page (index rows plus facets) and ids=all.
func TestReviewQuery_ResponseSizes40k(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 40,000-row snapshot")
	}
	snap := syntheticReviewSnapshot(40000, false)
	set, err := overlayLiveBooks(snap, nil, nil, changedIDs())
	require.NoError(t, err)
	base := newReviewQueryBase(reviewBaseKey{snap: snap}, set, time.Now())

	pageQ := mustReviewQuery(t, map[string]string{"limit": "50"})
	page := mustEvaluate(t, base, pageQ)
	pageBody, err := json.Marshal(gin.H{"data": reviewPageResponse(page, pageQ, nil)})
	require.NoError(t, err)

	// ids=all over a set where all 40,000 rows are listed.
	allSnap := syntheticReviewSnapshot(40000, true)
	allSet, err := overlayLiveBooks(allSnap, nil, nil, changedIDs())
	require.NoError(t, err)
	allBase := newReviewQueryBase(reviewBaseKey{snap: allSnap}, allSet, time.Now())
	idsQ := mustReviewQuery(t, map[string]string{"ids": "all"})
	ids := mustEvaluate(t, allBase, idsQ)
	idsBody, err := json.Marshal(gin.H{"data": reviewIDsResponse(ids)})
	require.NoError(t, err)

	t.Logf("view=page 50 rows: %d bytes (%.1f KB) over %d listed rows", len(pageBody), float64(len(pageBody))/1024, len(page.refs))
	t.Logf("ids=all: %d bytes (%.2f MB) for %d ids", len(idsBody), float64(len(idsBody))/(1024*1024), len(ids.refs))
	require.LessOrEqual(t, len(pageBody), 150*1024)
	require.LessOrEqual(t, len(idsBody), 1536*1024)
	require.Len(t, ids.refs, 40000)
}
