// file: internal/server/search_index_metrics_test.go
// version: 1.0.0
// guid: 2024660e-31f9-4957-85ca-5e9d31589271
// last-edited: 2026-08-22
//
// Tests for updateSearchIndexMetrics (TODO L3433): the search index's own
// document count, exported as search_index_docs_total so it can be graphed
// against books_total and catch a divergence like the 2026-08-14 incident
// (67,824 indexed docs vs 63,871 live books) instead of it going unnoticed.

package server

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// gatherSearchIndexDocsGauge reads search_index_docs_total from the DEFAULT
// registry — the same registry /metrics serves — mirroring
// gatherResumeFallbackCount in maintenance_resume_fallback_metric_test.go.
// The gauge has no labels, so the first metric in the family is the value.
func gatherSearchIndexDocsGauge(t *testing.T) (float64, bool) {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != "audiobook_organizer_search_index_docs_total" {
			continue
		}
		ms := mf.GetMetric()
		if len(ms) == 0 {
			return 0, false
		}
		return ms[0].GetGauge().GetValue(), true
	}
	return 0, false
}

// TestUpdateSearchIndexMetrics_NilIndexIsNoop mirrors the nil-guard already
// used by reconcileSearchIndexCoverage (search_coverage.go): the search
// index may not be open yet (or ever, in a search-disabled config), and
// that must not panic the metrics-gathering loop. Treated as "nothing to
// report", not a disqualifying error.
func TestUpdateSearchIndexMetrics_NilIndexIsNoop(t *testing.T) {
	srv := &Server{}
	srv.updateSearchIndexMetrics() // must not panic
}

// TestUpdateSearchIndexMetrics_TracksDocCount is the acceptance-criteria
// test: the scraped value must track the search index's actual DocCount(),
// verified by comparing the default registry's gathered value (what a real
// /metrics scrape serves) against a direct s.searchIndex.DocCount() call.
func TestUpdateSearchIndexMetrics_TracksDocCount(t *testing.T) {
	metrics.Register()
	srv, store, idx := newDropOnlyServer(t)

	books := coverageBooks()
	seedPartialIndex(t, store, idx, books, 3)

	want, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if want == 0 {
		t.Fatal("precondition: seeded index has 0 docs, test would be vacuous")
	}

	srv.updateSearchIndexMetrics()

	got, ok := gatherSearchIndexDocsGauge(t)
	if !ok {
		t.Fatal("audiobook_organizer_search_index_docs_total absent from the default registry after updateSearchIndexMetrics")
	}
	if got != float64(want) {
		t.Errorf("search_index_docs_total = %v, want %d (direct DocCount())", got, want)
	}

	// Index a fourth book and re-run: the gauge must move with DocCount(),
	// not just match once by coincidence.
	if err := idx.IndexBook(search.BookToDoc(store, &books[3])); err != nil {
		t.Fatalf("index %s: %v", books[3].ID, err)
	}
	want2, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount (2nd): %v", err)
	}
	if want2 != want+1 {
		t.Fatalf("precondition: DocCount after indexing one more doc = %d, want %d", want2, want+1)
	}

	srv.updateSearchIndexMetrics()
	got2, ok := gatherSearchIndexDocsGauge(t)
	if !ok {
		t.Fatal("audiobook_organizer_search_index_docs_total absent on second read")
	}
	if got2 != float64(want2) {
		t.Errorf("search_index_docs_total after re-index = %v, want %d (direct DocCount())", got2, want2)
	}
}
