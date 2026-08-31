// file: internal/transcribe/live_capability_probe_test.go
// version: 1.0.0
// guid: 5e9d1a34-72c6-4f80-b3e1-6a48f9c07d25
// last-edited: 2026-08-31

package transcribe

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestLiveEndpointCapabilities runs the capability gate against a REAL Whisper
// worker. It is skipped unless WHISPER_LIVE_URL is set, so CI never depends on
// a running server:
//
//	WHISPER_LIVE_URL=http://127.0.0.1:19848 go test ./internal/transcribe/ -run Live -v
//
// The unit tests use stubbed /health bodies, which means they verify the
// matcher against a body this repo also writes. This one verifies it against
// what a worker actually serves — the two are only the same until a worker
// changes its field names, and that divergence is invisible to a stub.
//
// Every case asserts an outcome rather than printing one: a probe that only
// logged would pass whatever the server said.
func TestLiveEndpointCapabilities(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("WHISPER_LIVE_URL"))
	if url == "" {
		t.Skip("WHISPER_LIVE_URL not set; skipping live endpoint probe")
	}

	// backend is the specific device the live worker reports, used to build
	// both a matching and a deliberately non-matching narrow requirement.
	h, ok := probeRemoteHealth(context.Background(), url)
	if !ok {
		t.Fatalf("could not read /health at %s", url)
	}
	backend := strings.ToLower(strings.TrimSpace(h.Device))
	if backend == "" {
		t.Fatalf("%s reports no device; a worker older than whisper_server.py 2.9.0 cannot be gated", url)
	}
	t.Logf("live worker %s reports device=%q compute_type=%q batch=%v", url, h.Device, h.ComputeType, h.supportsBatch())

	// wrongBackend is a GPU backend the worker is definitely not on, so a
	// narrow requirement must refuse it. Without this the suite could pass
	// against a matcher that accepts every GPU label.
	wrongBackend := "cuda"
	if backend == "cuda" {
		wrongBackend = "metal"
	}

	cases := []struct {
		name     string
		ep       Endpoint
		requires []string
		wantKept bool
	}{
		{"no requirement accepts", Endpoint{URL: url}, nil, true},
		{"narrow requirement matching the real backend", Endpoint{URL: url}, []string{backend}, true},
		{"narrow requirement for another backend", Endpoint{URL: url}, []string{wrongBackend}, false},
		{"declared label satisfies", Endpoint{URL: url, Capabilities: []string{"local"}}, []string{"local"}, true},
		{"undeclared label refuses", Endpoint{URL: url}, []string{"local"}, false},
		// The load-bearing one: an operator declaring a MEASURED label must
		// not be believed. This is what `kind` got wrong.
		{"declared backend cannot forge a measured one",
			Endpoint{URL: url, Capabilities: []string{wrongBackend}}, []string{wrongBackend}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gated, refused := gateEndpoints(context.Background(), []Endpoint{tc.ep}, tc.requires)
			kept := len(gated) == 1
			if kept != tc.wantKept {
				if len(refused) > 0 {
					t.Fatalf("kept=%v want=%v; refusal: %s", kept, tc.wantKept, refused[0].Reason)
				}
				t.Fatalf("kept=%v want=%v", kept, tc.wantKept)
			}
		})
	}
}
