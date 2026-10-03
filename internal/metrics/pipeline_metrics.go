// file: internal/metrics/pipeline_metrics.go
// version: 1.0.0
// guid: 1bafd1d0-de42-4e6d-a079-b28e4ea4858b
// last-edited: 2026-10-03

package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Bounded label values for filename_parse_total{shape}. The parser classifies
// a filename into exactly one of these before counting; a raw filename or an
// unlisted shape string must never reach the label (cardinality). ParseShapeOther
// is for a recognised-but-unlisted layout, ParseShapeUnknown for "the parser
// could not tell what layout this is at all".
const (
	ParseShapeAuthorSeqTitle       = "author_seq_title"
	ParseShapeSeriesSeqTitle       = "series_seq_title"
	ParseShapeSeriesSeqAuthorTitle = "series_seq_author_title"
	ParseShapeSeqTitle             = "seq_title"
	ParseShapeDigitsOnly           = "digits_only"
	ParseShapeRangeSpan            = "range_span"
	ParseShapeYearTitle            = "year_title"
	ParseShapeOther                = "other"
	ParseShapeUnknown              = "unknown"
)

// Bounded label values for filename_parse_total{outcome}: what the parse did
// with the file. matched = the parse agreed with the stored title; retitled =
// the parse produced a title and it was written; skipped = a parse existed but
// a gate (allowlist, lock, iTunes rule, ...) declined to apply it; unparsed =
// no usable parse.
const (
	ParseOutcomeMatched  = "matched"
	ParseOutcomeRetitled = "retitled"
	ParseOutcomeSkipped  = "skipped"
	ParseOutcomeUnparsed = "unparsed"
)

// Bounded label values for metadata_fetch_total{source}: where one provider
// lookup was answered from. cache_hit/cache_miss are recorded at the fetch-cache
// decision (internal/metafetch), network/error at the live provider call
// (metadata.ProtectedSource, metafetch's by-hand ASIN lookup). A miss that goes
// on to the network therefore records cache_miss AND network (or error): the
// hit ratio is cache_hit / (cache_hit + cache_miss), and the live error rate is
// error / (network + error).
const (
	FetchSourceCacheHit  = "cache_hit"
	FetchSourceCacheMiss = "cache_miss"
	FetchSourceNetwork   = "network"
	FetchSourceError     = "error"
)

// Bounded label values for review_index_request_seconds{view}: the review
// listing's two shapes. "index" is GET ...?view=index (descriptions stripped,
// what the review page loads first); "full" is every other listing request.
const (
	ReviewViewIndex = "index"
	ReviewViewFull  = "full"
)

// Bounded label values for fixer_duration_seconds{phase}: the two framework
// operations a repairs fixer runs under (internal/repairs/engine.go).
const (
	FixerPhasePlan  = "plan"
	FixerPhaseApply = "apply"
)

var (
	// filenameParseTotal counts filename-parse decisions by the shape the
	// parser recognised and what it did with the result. Instrument only: the
	// call sites are wired by the number-leading-titles fixer branch, which is
	// where the shape classification lives.
	filenameParseTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "filename_parse_total",
		Help:      "Filename parse decisions by recognised shape (author_seq_title, series_seq_title, series_seq_author_title, seq_title, digits_only, range_span, year_title, other, unknown) and outcome (matched, retitled, skipped, unparsed)",
	}, []string{"shape", "outcome"})

	// metadataFetchTotal counts provider lookups by provider and by where the
	// answer came from. The provider label is metadata.ProviderKey(src): a
	// configured provider id (audible, audnexus, google-books, hardcover,
	// openlibrary, wikipedia) or, for an unidentified source, its display name
	// -- both come from configuration, never from a request.
	metadataFetchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "audiobook_organizer",
		Name:      "metadata_fetch_total",
		Help:      "Metadata provider lookups by provider and source (cache_hit, cache_miss at the fetch-cache check; network, error at the live call)",
	}, []string{"provider", "source"})

	// reviewIndexRequestSeconds is the handler time of the review-page listing
	// (GET /audiobooks/metadata/cache/review), by view. The review page is the
	// owner's main surface and its "<3s" target is measured here, not at the
	// client. Buckets run 0.1s..120s because the request has been observed
	// anywhere from sub-second (warm snapshot) to minutes (cold rebuild during
	// a scan); the slow-request WARN in the handler fires at 5s.
	reviewIndexRequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "audiobook_organizer",
		Name:      "review_index_request_seconds",
		Help:      "Handler latency of the metadata-cache review listing (GET /audiobooks/metadata/cache/review) by view (index, full)",
		Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 10, 20, 30, 60, 120},
	}, []string{"view"})

	// numberLeadingTitlesGauge is the number of PRIMARY, non-deleted books whose
	// title starts with a digit and is not on titleutil.LegitNumberTitles. It is
	// computed in the SAME pass as books_total (PebbleStore.countPrimaryBooksScan)
	// so the two gauges always describe the same set of rows at the same instant.
	numberLeadingTitlesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "audiobook_organizer",
		Name:      "number_leading_titles",
		Help:      "PRIMARY books whose title starts with a digit, excluding titleutil.LegitNumberTitles; computed in the same scan as books_total",
	})

	// fixerDurationSeconds is the wall time of one repairs fixer run, by fixer
	// id and phase. operation_duration_seconds{type} cannot show this: every
	// fixer runs under the same two op types (repairs.plan, repairs.apply), so
	// the fixer id has to be its own label. The id set is the registered fixer
	// list (bounded by code, never by a request).
	fixerDurationSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "audiobook_organizer",
		Name:      "fixer_duration_seconds",
		Help:      "Wall time of one repairs fixer run by fixer id and phase (plan, apply)",
		Buckets:   []float64{0.1, 0.5, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600, 7200},
	}, []string{"fixer", "phase"})
)

// pipelineCollectors is what Register adds for this file.
var pipelineCollectors = []prometheus.Collector{
	filenameParseTotal, metadataFetchTotal, reviewIndexRequestSeconds, numberLeadingTitlesGauge, fixerDurationSeconds,
}

// IncFilenameParse counts one filename-parse decision. shape and outcome MUST
// be the ParseShape*/ParseOutcome* constants above; the caller classifies, this
// function only counts.
func IncFilenameParse(shape, outcome string) {
	filenameParseTotal.WithLabelValues(shape, outcome).Inc()
}

// IncMetadataFetch counts one provider lookup event. provider is
// metadata.ProviderKey(src); source is one of the FetchSource* constants.
func IncMetadataFetch(provider, source string) {
	metadataFetchTotal.WithLabelValues(provider, source).Inc()
}

// ObserveReviewIndexRequest records one review-listing handler duration under
// view (ReviewViewIndex or ReviewViewFull).
func ObserveReviewIndexRequest(view string, d time.Duration) {
	reviewIndexRequestSeconds.WithLabelValues(view).Observe(d.Seconds())
}

// SetNumberLeadingTitles publishes the number-leading primary-title count.
func SetNumberLeadingTitles(n int) { numberLeadingTitlesGauge.Set(float64(n)) }

// ObserveFixerDuration records one fixer run's wall time under its id and
// phase (FixerPhasePlan or FixerPhaseApply).
func ObserveFixerDuration(fixer, phase string, d time.Duration) {
	fixerDurationSeconds.WithLabelValues(fixer, phase).Observe(d.Seconds())
}
