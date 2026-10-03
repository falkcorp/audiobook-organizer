// file: internal/metrics/pipeline_metrics_test.go
// version: 1.0.0
// guid: 2b18e9de-8f9d-46b8-91f8-94c25c4bcd6b
// last-edited: 2026-10-03

package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIncFilenameParse_CountsPerShapeAndOutcome(t *testing.T) {
	filenameParseTotal.Reset()
	t.Cleanup(filenameParseTotal.Reset)

	IncFilenameParse(ParseShapeAuthorSeqTitle, ParseOutcomeRetitled)
	IncFilenameParse(ParseShapeAuthorSeqTitle, ParseOutcomeRetitled)
	IncFilenameParse(ParseShapeDigitsOnly, ParseOutcomeUnparsed)

	if got := testutil.ToFloat64(filenameParseTotal.WithLabelValues(ParseShapeAuthorSeqTitle, ParseOutcomeRetitled)); got != 2 {
		t.Errorf("author_seq_title/retitled = %v, want 2", got)
	}
	if got := testutil.ToFloat64(filenameParseTotal.WithLabelValues(ParseShapeDigitsOnly, ParseOutcomeUnparsed)); got != 1 {
		t.Errorf("digits_only/unparsed = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(filenameParseTotal); got != 2 {
		t.Errorf("series = %d, want 2", got)
	}
}

func TestIncMetadataFetch_CountsPerProviderAndSource(t *testing.T) {
	metadataFetchTotal.Reset()
	t.Cleanup(metadataFetchTotal.Reset)

	IncMetadataFetch("audible", FetchSourceCacheHit)
	IncMetadataFetch("audible", FetchSourceCacheMiss)
	IncMetadataFetch("audible", FetchSourceNetwork)
	IncMetadataFetch("openlibrary", FetchSourceError)

	for _, tc := range []struct {
		provider, source string
		want             float64
	}{
		{"audible", FetchSourceCacheHit, 1},
		{"audible", FetchSourceCacheMiss, 1},
		{"audible", FetchSourceNetwork, 1},
		{"openlibrary", FetchSourceError, 1},
		{"openlibrary", FetchSourceNetwork, 0},
	} {
		if got := testutil.ToFloat64(metadataFetchTotal.WithLabelValues(tc.provider, tc.source)); got != tc.want {
			t.Errorf("%s/%s = %v, want %v", tc.provider, tc.source, got, tc.want)
		}
	}
}

func TestObserveReviewIndexRequest_RecordsUnderView(t *testing.T) {
	reviewIndexRequestSeconds.Reset()
	t.Cleanup(reviewIndexRequestSeconds.Reset)

	ObserveReviewIndexRequest(ReviewViewIndex, 1500*time.Millisecond)
	ObserveReviewIndexRequest(ReviewViewIndex, 300*time.Millisecond)
	ObserveReviewIndexRequest(ReviewViewFull, 40*time.Second)

	// CollectAndCount counts series; a HistogramVec exposes one series per
	// label set (each with its own _bucket/_sum/_count), so two views = 2.
	if got := testutil.CollectAndCount(reviewIndexRequestSeconds); got != 2 {
		t.Errorf("series = %d, want 2 (index, full)", got)
	}

	Register()
	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `audiobook_organizer_review_index_request_seconds_count{view="index"} 2`) {
		t.Errorf("scrape missing index count=2; body excerpt:\n%s", excerpt(body, "review_index_request_seconds"))
	}
	if !strings.Contains(body, `audiobook_organizer_review_index_request_seconds_count{view="full"} 1`) {
		t.Errorf("scrape missing full count=1; body excerpt:\n%s", excerpt(body, "review_index_request_seconds"))
	}
	// The bucket ladder must reach the requested 120s ceiling, so a 40s
	// request lands in a finite bucket rather than only +Inf.
	if !strings.Contains(body, `audiobook_organizer_review_index_request_seconds_bucket{view="full",le="120"} 1`) {
		t.Errorf("scrape missing le=120 bucket for full view; body excerpt:\n%s", excerpt(body, "review_index_request_seconds_bucket{view=\"full\""))
	}
}

func TestSetNumberLeadingTitles_SetsGauge(t *testing.T) {
	for _, v := range []int{0, 7, 1932} {
		SetNumberLeadingTitles(v)
		if got := testutil.ToFloat64(numberLeadingTitlesGauge); got != float64(v) {
			t.Errorf("SetNumberLeadingTitles(%d): gauge reads %v", v, got)
		}
	}
}

func TestObserveFixerDuration_RecordsUnderFixerAndPhase(t *testing.T) {
	fixerDurationSeconds.Reset()
	t.Cleanup(fixerDurationSeconds.Reset)

	ObserveFixerDuration("number-leading-titles", FixerPhasePlan, 12*time.Second)
	ObserveFixerDuration("number-leading-titles", FixerPhaseApply, 2*time.Second)
	ObserveFixerDuration("duplicate-copies", FixerPhasePlan, 90*time.Second)

	if got := testutil.CollectAndCount(fixerDurationSeconds); got != 3 {
		t.Errorf("series = %d, want 3", got)
	}
}

// TestPipelineMetrics_AppearInScrape pins every new metric NAME a Prometheus
// scrape sees, via the same promhttp.Handler() /metrics is wired to. A
// collector left out of Register's MustRegister list would pass every
// in-process test above and still be invisible to Prometheus.
func TestPipelineMetrics_AppearInScrape(t *testing.T) {
	Register()
	IncFilenameParse(ParseShapeUnknown, ParseOutcomeUnparsed)
	IncMetadataFetch("audnexus", FetchSourceNetwork)
	ObserveReviewIndexRequest(ReviewViewIndex, time.Second)
	SetNumberLeadingTitles(3)
	ObserveFixerDuration("x", FixerPhasePlan, time.Second)

	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, name := range []string{
		"audiobook_organizer_filename_parse_total",
		"audiobook_organizer_metadata_fetch_total",
		"audiobook_organizer_review_index_request_seconds",
		"audiobook_organizer_number_leading_titles",
		"audiobook_organizer_fixer_duration_seconds",
	} {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("scrape has no TYPE line for %s", name)
		}
	}
}

// TestOperationDurationBuckets_ReachADay pins the widened ladder: an op that
// ran for an hour must land in a finite bucket.
func TestOperationDurationBuckets_ReachADay(t *testing.T) {
	operationDuration.Reset()
	t.Cleanup(operationDuration.Reset)
	Register()
	ObserveOperationDuration("dedup.full-scan", time.Hour)

	rec := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `audiobook_organizer_operation_duration_seconds_bucket{type="dedup.full-scan",le="86400"} 1`) {
		t.Errorf("no le=86400 bucket holding the 1h observation; body excerpt:\n%s", excerpt(body, "operation_duration_seconds_bucket"))
	}
}

func excerpt(body, needle string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
