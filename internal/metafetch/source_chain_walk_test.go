// file: internal/metafetch/source_chain_walk_test.go
// version: 1.2.0
// guid: 3e91c7d4-8b52-4a06-9f13-6c8d2e5a70b4
// last-edited: 2026-09-05

package metafetch

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// emptyKV is a RawKVStore with nothing in it, so every cache lookup misses and
// walkSourceChain always takes the live path.
type emptyKV struct{}

func (emptyKV) SetRaw(string, []byte) error                  { return nil }
func (emptyKV) GetRaw(string) ([]byte, error)                { return nil, nil }
func (emptyKV) DeleteRaw(string) error                       { return nil }
func (emptyKV) ScanPrefix(string) ([]database.KVPair, error) { return nil, nil }
func (emptyKV) CountPrefix(string) (int64, error)            { return 0, nil }

// fakeSource returns a fixed (results, error) pair for every query.
type fakeSource struct {
	name    string
	results []metadata.BookMetadata
	err     error
}

func (f fakeSource) Name() string { return f.name }
func (f fakeSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	return f.results, f.err
}
func (f fakeSource) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	return f.results, f.err
}

// TestWalkSourceChain_ErrorIsNotAMiss is the regression test for the defect that
// made a rate-limited run indistinguishable from a complete one: a provider that
// ERRORS and a book that genuinely is not in any catalog both end the walk with
// zero results, and the old code recorded both as "not_found".
//
// The consequence was operational, not cosmetic: with false misses in the
// ledger, "fetch only what is missing" cannot be trusted and the only safe
// recovery is a full re-scan of the library.
func TestWalkSourceChain_ErrorIsNotAMiss(t *testing.T) {
	throttled := errors.New("429 Too Many Requests")

	tests := []struct {
		name       string
		chain      []metadata.MetadataSource
		wantStatus string
		wantErr    bool
	}{
		{
			name:       "provider throttled is a retryable fetch_error, never not_found",
			chain:      []metadata.MetadataSource{fakeSource{name: "hardcover", err: throttled}},
			wantStatus: FetchStatusFetchError,
			wantErr:    true,
		},
		{
			name:       "clean empty answer from every source is a genuine miss",
			chain:      []metadata.MetadataSource{fakeSource{name: "hardcover"}},
			wantStatus: FetchStatusNotFound,
			wantErr:    false,
		},
		{
			name: "a later source succeeding outranks an earlier source erroring",
			chain: []metadata.MetadataSource{
				fakeSource{name: "hardcover", err: throttled},
				fakeSource{name: "audible", results: []metadata.BookMetadata{{Title: "Dune"}}},
			},
			wantStatus: FetchStatusCached,
			wantErr:    true, // recorded, but does not change the status
		},
		{
			name: "every source erroring is still a fetch_error, not a miss",
			chain: []metadata.MetadataSource{
				fakeSource{name: "hardcover", err: throttled},
				fakeSource{name: "audible", err: errors.New("circuit open")},
			},
			wantStatus: FetchStatusFetchError,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sem := NewProviderSemaphore(tc.chain, 2)
			out, err := WalkSourceChain(context.Background(), emptyKV{}, tc.chain, sem,
				"01BOOK", "Dune", "Frank Herbert", time.Hour)
			if err != nil {
				t.Fatalf("walkSourceChain returned a hard error: %v", err)
			}
			if got := out.Status(); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if (out.Err != nil) != tc.wantErr {
				t.Errorf("outcome.err = %v, wantErr = %v", out.Err, tc.wantErr)
			}
			if tc.wantErr && out.ErrSource == "" {
				t.Error("an error was recorded but errSource is empty — the provider that failed must be identifiable")
			}
		})
	}
}

// TestWalkSourceChain_UntrimmedTitleRetry pins the behaviour that had silently
// drifted between the two copies of this walk: when stripChapterFromTitle
// changes the title, the ORIGINAL title must also be tried. Only the all-books
// copy did this; the by-IDs copy did not, so the same book could resolve
// differently depending on which entry point fetched it.
func TestWalkSourceChain_UntrimmedTitleRetry(t *testing.T) {
	var seen []string
	src := &recordingSource{name: "audible", hitOn: "01 Chapter 1 Dune"}
	chain := []metadata.MetadataSource{src}
	sem := NewProviderSemaphore(chain, 2)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "01 Chapter 1 Dune", "", time.Hour)
	if err != nil {
		t.Fatalf("walkSourceChain: %v", err)
	}
	seen = src.queries
	if out.Status() != FetchStatusCached {
		t.Fatalf("status = %q, want %q (queries tried: %v)", out.Status(), FetchStatusCached, seen)
	}
	if len(seen) < 2 {
		t.Errorf("expected a retry with the untrimmed title; only tried %v", seen)
	}
}

type recordingSource struct {
	name    string
	hitOn   string
	queries []string
	author  string // named on every answer; a one-word variant hit needs someone to vouch
}

func (r *recordingSource) Name() string { return r.name }
func (r *recordingSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	r.queries = append(r.queries, title)
	if title == r.hitOn {
		return []metadata.BookMetadata{{Title: title, Author: r.author}}, nil
	}
	return nil, nil
}
func (r *recordingSource) SearchByTitleAndAuthor(ctx context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return r.SearchByTitle(ctx, title)
}

// idSource is a fakeSource that also declares a provider id, so the throttle
// registry can key holds on it. Name deliberately DIFFERS from the id — every
// real client has that divergence ("Audible" vs "audible") and a fixture where
// they match cannot tell which one the gate uses.
type idSource struct {
	id      string
	results []metadata.BookMetadata
	err     error
	calls   *int
}

func (s idSource) Name() string       { return "Stub " + s.id }
func (s idSource) ProviderID() string { return s.id }
func (s idSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	if s.calls != nil {
		*s.calls++
	}
	return s.results, s.err
}

func (s idSource) SearchByTitleAndAuthor(ctx context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	return s.SearchByTitle(ctx, "")
}

const walkQuotaBody = `{"error":{"message":"Quota exceeded for quota metric 'Queries' and limit 'Queries per day'"}}`

func walkThrottle(t *testing.T, id string, status int, body string) {
	t.Helper()
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
	if _, ok := metadata.DefaultThrottleRegistry().RecordFailure(id, metadata.StatusError(id, resp)); !ok {
		t.Fatalf("setup: %d did not throttle %s", status, id)
	}
}

func resetWalkThrottles(t *testing.T) {
	t.Helper()
	metadata.ResetThrottlesForTesting()
	t.Cleanup(metadata.ResetThrottlesForTesting)
}

// AllThrottled is the mid-run counterpart to the pre-flight refusal, and it is
// what stops the incident this feature exists for. The chain is filtered once
// before the worker pool starts; the quota that motivated this work ran out at
// book 350 of 22,934, so without this flag the remaining ~22,584 books each got
// a ledger row for work that provably could not happen.
func TestWalkSourceChain_AllThrottledIsReportedMidRun(t *testing.T) {
	resetWalkThrottles(t)
	var googleCalls, hardcoverCalls int
	chain := []metadata.MetadataSource{
		metadata.NewChainSource(idSource{id: "google-books", calls: &googleCalls}),
		metadata.NewChainSource(idSource{id: "hardcover", calls: &hardcoverCalls}),
	}
	sem := NewProviderSemaphore(chain, DefaultPerProviderFetchCap)

	walkThrottle(t, "google-books", 429, walkQuotaBody)
	walkThrottle(t, "hardcover", 401, "bad token")

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem, "b1", "Dune", "Herbert", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if !out.AllThrottled {
		t.Fatal("every provider refused on the throttle and AllThrottled was not set; the run would ledger a failure for this book and every one after it")
	}
	if googleCalls != 0 || hardcoverCalls != 0 {
		t.Fatalf("a throttled provider was still called: google=%d hardcover=%d", googleCalls, hardcoverCalls)
	}
}

// One live provider means the run can still do work — AllThrottled must stay
// false or a partial throttle would abort a run that was making progress.
func TestWalkSourceChain_PartialThrottleIsNotAllThrottled(t *testing.T) {
	resetWalkThrottles(t)
	chain := []metadata.MetadataSource{
		metadata.NewChainSource(idSource{id: "google-books"}),
		metadata.NewChainSource(idSource{id: "hardcover", results: []metadata.BookMetadata{{Title: "Dune"}}}),
	}
	sem := NewProviderSemaphore(chain, DefaultPerProviderFetchCap)
	walkThrottle(t, "google-books", 429, walkQuotaBody)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem, "b1", "Dune", "Herbert", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.AllThrottled {
		t.Fatal("a partial throttle reported AllThrottled; a run still making progress would be aborted")
	}
	if len(out.Results) != 1 {
		t.Fatalf("the live provider's results were lost: %v", out.Results)
	}
}

// A clean chain that simply finds nothing is not throttled — otherwise every
// book absent from every catalog would abort the run.
func TestWalkSourceChain_NoResultsIsNotAllThrottled(t *testing.T) {
	resetWalkThrottles(t)
	chain := []metadata.MetadataSource{metadata.NewChainSource(idSource{id: "google-books"})}
	sem := NewProviderSemaphore(chain, DefaultPerProviderFetchCap)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem, "b1", "Dune", "Herbert", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.AllThrottled {
		t.Fatal("an ordinary miss was reported as all-throttled")
	}
}

// The first book to hit a quota is the one row an operator opens to find out
// what happened. The query ladder retries up to four times, so attempts 2-4
// return the throttle sentinel — which must not overwrite the real diagnosis.
func TestWalkSourceChain_SentinelDoesNotDisplaceTheDiagnosis(t *testing.T) {
	resetWalkThrottles(t)
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(walkQuotaBody)),
	}
	quotaErr := metadata.StatusError("google-books", resp)
	chain := []metadata.MetadataSource{metadata.NewChainSource(idSource{id: "google-books", err: quotaErr})}
	sem := NewProviderSemaphore(chain, DefaultPerProviderFetchCap)

	out, err := WalkSourceChain(context.Background(), emptyKV{}, chain, sem, "b1", "Dune", "Herbert", time.Hour)
	if err != nil {
		t.Fatalf("WalkSourceChain: %v", err)
	}
	if out.Err == nil {
		t.Fatal("no error recorded")
	}
	if !strings.Contains(out.Err.Error(), "Queries per day") {
		t.Fatalf("the quota message was displaced by the throttle sentinel, so the ledger row cannot name the cause: %v", out.Err)
	}
}
