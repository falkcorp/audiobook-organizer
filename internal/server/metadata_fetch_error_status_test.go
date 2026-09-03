// file: internal/server/metadata_fetch_error_status_test.go
// version: 1.0.0
// guid: 3e91c7d4-8b52-4a06-9f13-6c8d2e5a70b4
// last-edited: 2026-09-02

package server

import (
	"context"
	"errors"
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
			wantStatus: fetchStatusFetchError,
			wantErr:    true,
		},
		{
			name:       "clean empty answer from every source is a genuine miss",
			chain:      []metadata.MetadataSource{fakeSource{name: "hardcover"}},
			wantStatus: fetchStatusNotFound,
			wantErr:    false,
		},
		{
			name: "a later source succeeding outranks an earlier source erroring",
			chain: []metadata.MetadataSource{
				fakeSource{name: "hardcover", err: throttled},
				fakeSource{name: "audible", results: []metadata.BookMetadata{{Title: "Dune"}}},
			},
			wantStatus: fetchStatusCached,
			wantErr:    true, // recorded, but does not change the status
		},
		{
			name: "every source erroring is still a fetch_error, not a miss",
			chain: []metadata.MetadataSource{
				fakeSource{name: "hardcover", err: throttled},
				fakeSource{name: "audible", err: errors.New("circuit open")},
			},
			wantStatus: fetchStatusFetchError,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sem := newProviderSemaphore(tc.chain, 2)
			out, err := walkSourceChain(context.Background(), emptyKV{}, tc.chain, sem,
				"01BOOK", "Dune", "Frank Herbert", time.Hour)
			if err != nil {
				t.Fatalf("walkSourceChain returned a hard error: %v", err)
			}
			if got := out.status(); got != tc.wantStatus {
				t.Errorf("status = %q, want %q", got, tc.wantStatus)
			}
			if (out.err != nil) != tc.wantErr {
				t.Errorf("outcome.err = %v, wantErr = %v", out.err, tc.wantErr)
			}
			if tc.wantErr && out.errSource == "" {
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
	sem := newProviderSemaphore(chain, 2)

	out, err := walkSourceChain(context.Background(), emptyKV{}, chain, sem,
		"01BOOK", "01 Chapter 1 Dune", "", time.Hour)
	if err != nil {
		t.Fatalf("walkSourceChain: %v", err)
	}
	seen = src.queries
	if out.status() != fetchStatusCached {
		t.Fatalf("status = %q, want %q (queries tried: %v)", out.status(), fetchStatusCached, seen)
	}
	if len(seen) < 2 {
		t.Errorf("expected a retry with the untrimmed title; only tried %v", seen)
	}
}

type recordingSource struct {
	name    string
	hitOn   string
	queries []string
}

func (r *recordingSource) Name() string { return r.name }
func (r *recordingSource) SearchByTitle(_ context.Context, title string) ([]metadata.BookMetadata, error) {
	r.queries = append(r.queries, title)
	if title == r.hitOn {
		return []metadata.BookMetadata{{Title: title}}, nil
	}
	return nil, nil
}
func (r *recordingSource) SearchByTitleAndAuthor(ctx context.Context, title, _ string) ([]metadata.BookMetadata, error) {
	return r.SearchByTitle(ctx, title)
}
