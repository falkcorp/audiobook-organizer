// file: internal/server/metadata_ops_throttle_test.go
// version: 1.0.0
// guid: 11f72a9a-9bd0-4e67-925a-1e1b498f9baf
// last-edited: 2026-09-03

package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// throttleTestSource is a minimal chain member with a stable provider id.
type throttleTestSource struct{ id string }

func (s *throttleTestSource) Name() string       { return s.id }
func (s *throttleTestSource) ProviderID() string { return s.id }
func (s *throttleTestSource) SearchByTitle(context.Context, string) ([]metadata.BookMetadata, error) {
	return nil, nil
}

func (s *throttleTestSource) SearchByTitleAndAuthor(context.Context, string, string) ([]metadata.BookMetadata, error) {
	return nil, nil
}

func throttleProvider(t *testing.T, id string, status int, body string) {
	t.Helper()
	resp := &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	if _, ok := metadata.DefaultThrottleRegistry().RecordFailure(id, metadata.StatusError(id, resp)); !ok {
		t.Fatalf("setup: %d did not throttle %s", status, id)
	}
}

func resetThrottles(t *testing.T) {
	t.Helper()
	if _, err := metadata.DefaultThrottleRegistry().ClearAll(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	t.Cleanup(func() { _, _ = metadata.DefaultThrottleRegistry().ClearAll() })
}

const quotaBody = `{"error":{"message":"Quota exceeded for quota metric 'Queries' and limit 'Queries per day'"}}`

// The load-bearing case. On 2026-09-03 a run over 22,934 books was cancelled
// after ~99% errors because the chain kept calling a provider whose daily quota
// was gone. Refusing to start is the difference between 22,934 wrong ledger rows
// and zero books touched.
func TestPrepareFetchChain_AllThrottledRefusesToStart(t *testing.T) {
	resetThrottles(t)
	chain := []metadata.MetadataSource{
		metadata.NewChainSource(&throttleTestSource{id: "google-books"}),
		metadata.NewChainSource(&throttleTestSource{id: "hardcover"}),
	}
	throttleProvider(t, "google-books", 429, quotaBody)
	throttleProvider(t, "hardcover", 401, "bad token")

	got, err := prepareFetchChain(context.Background(), chain, false)
	if err == nil {
		t.Fatal("an all-throttled chain was allowed to start; every book would be ledgered a failure")
	}
	if got != nil {
		t.Fatalf("chain returned alongside the error: %v", got)
	}
	// The message has to name the holds, or the operator sees a failed op with
	// no route to the cause and no idea which reset to press.
	for _, want := range []string{"google-books", "hardcover", "daily-quota", "throttles"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message does not mention %q: %v", want, err)
		}
	}
}

// A partial throttle must NOT stop the run — the remaining providers can still
// answer, and stopping would be its own kind of silent data loss.
func TestPrepareFetchChain_PartialThrottleKeepsTheRest(t *testing.T) {
	resetThrottles(t)
	chain := []metadata.MetadataSource{
		metadata.NewChainSource(&throttleTestSource{id: "google-books"}),
		metadata.NewChainSource(&throttleTestSource{id: "hardcover"}),
	}
	throttleProvider(t, "google-books", 429, quotaBody)

	got, err := prepareFetchChain(context.Background(), chain, false)
	if err != nil {
		t.Fatalf("a partially throttled chain was refused: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(chain) = %d, want 1", len(got))
	}
	if id := metadata.ProviderIDOf(got[0]); id != "hardcover" {
		t.Fatalf("surviving provider = %q, want hardcover", id)
	}
}

func TestPrepareFetchChain_CleanChainIsUntouched(t *testing.T) {
	resetThrottles(t)
	chain := []metadata.MetadataSource{
		metadata.NewChainSource(&throttleTestSource{id: "google-books"}),
		metadata.NewChainSource(&throttleTestSource{id: "hardcover"}),
	}
	got, err := prepareFetchChain(context.Background(), chain, false)
	if err != nil {
		t.Fatalf("clean chain refused: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(chain) = %d, want 2", len(got))
	}
}

// prefer_audible prepended a BARE client until 2026-09-03 — the one source in
// the chain with neither a circuit breaker nor a throttle, on the exact flag the
// quota-exhausted run was dispatched with.
func TestPrepareFetchChain_PreferAudibleIsWrappedAndThrottleable(t *testing.T) {
	resetThrottles(t)
	chain := []metadata.MetadataSource{metadata.NewChainSource(&throttleTestSource{id: "hardcover"})}

	got, err := prepareFetchChain(context.Background(), chain, true)
	if err != nil {
		t.Fatalf("prepareFetchChain: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(chain) = %d, want 2", len(got))
	}
	if id := metadata.ProviderIDOf(got[0]); id != metadata.SourceIDAudible {
		t.Fatalf("front of chain = %q, want %q — a bare client reports no provider id at all",
			id, metadata.SourceIDAudible)
	}

	// And being identified is what makes it excludable.
	throttleProvider(t, metadata.SourceIDAudible, 429, quotaBody)
	got, err = prepareFetchChain(context.Background(), chain, true)
	if err != nil {
		t.Fatalf("prepareFetchChain: %v", err)
	}
	if len(got) != 1 || metadata.ProviderIDOf(got[0]) != "hardcover" {
		t.Fatalf("throttled audible was not excluded: %v", got)
	}
}

func TestPrepareFetchChain_EmptyChainIsDistinctFromAllThrottled(t *testing.T) {
	resetThrottles(t)
	_, err := prepareFetchChain(context.Background(), nil, false)
	if err == nil {
		t.Fatal("an empty chain was accepted")
	}
	if strings.Contains(err.Error(), "throttled") {
		t.Fatalf("no-sources-configured reported as a throttle: %v", err)
	}
}
