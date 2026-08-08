// file: internal/transcribe/dispatcher_test.go
// version: 1.0.0
// guid: 1ca890f2-8505-4a42-9d74-209cc293077e
// last-edited: 2026-08-07

package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// newWhisperStub returns an httptest server that answers the legacy per-file
// protocol: /health 404s (so the dispatcher takes the per-file path) and
// /transcribe returns a fixed transcript.
func newWhisperStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "ok"})
	})
	return httptest.NewServer(mux)
}

// deadServerURL returns a URL whose listener has been closed, so every
// connection is refused.
func deadServerURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

func tempWAVJobs(t *testing.T, n int) map[string]string {
	t.Helper()
	dir := t.TempDir()
	jobs := make(map[string]string, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("book-%03d", i)
		path := filepath.Join(dir, id+".wav")
		if err := os.WriteFile(path, []byte("RIFF-fake-wav"), 0o644); err != nil {
			t.Fatalf("write wav: %v", err)
		}
		jobs[id] = path
	}
	return jobs
}

// Test (b): one endpoint down, the survivor completes everything, nil error.
func TestPoolOneEndpointDownSurvivorCompletes(t *testing.T) {
	downURL := deadServerURL(t)
	up := newWhisperStub(t)
	defer up.Close()

	endpoints := []Endpoint{
		{URL: downURL, Concurrency: 1, Label: "gpu-dead", Priority: 1, Kind: "gpu"},
		{URL: up.URL, Concurrency: 1, Label: "cpu-alive", Priority: 100, Kind: "cpu"},
	}
	jobs := tempWAVJobs(t, 5)

	results, err := transcribePool(context.Background(), endpoints, jobs, nil)
	if err != nil {
		t.Fatalf("expected nil error when a survivor remains, got: %v", err)
	}
	if len(results) != len(jobs) {
		t.Fatalf("expected %d results, got %d", len(jobs), len(results))
	}
	for id := range jobs {
		r, ok := results[id]
		if !ok {
			t.Fatalf("missing result for %s", id)
		}
		if r.Text != "ok" || r.Error != "" {
			t.Fatalf("unexpected result for %s: %+v", id, r)
		}
	}
}

// Test (c): every endpoint down -> *TransportError naming BOTH URLs.
func TestPoolAllEndpointsDownTransportError(t *testing.T) {
	urlA := deadServerURL(t)
	urlB := deadServerURL(t)

	endpoints := []Endpoint{
		{URL: urlA, Concurrency: 1, Label: "a", Priority: 1},
		{URL: urlB, Concurrency: 1, Label: "b", Priority: 2},
	}
	jobs := tempWAVJobs(t, 3)

	results, err := transcribePool(context.Background(), endpoints, jobs, nil)
	if err == nil {
		t.Fatalf("expected error with all endpoints down, got results: %v", results)
	}
	var te *TransportError
	if !errors.As(err, &te) {
		t.Fatalf("expected *TransportError, got %T: %v", err, err)
	}
	found := map[string]bool{}
	for _, u := range te.Endpoints {
		found[u] = true
	}
	if !found[urlA] || !found[urlB] {
		t.Fatalf("TransportError.Endpoints must name both URLs (%s, %s), got %v",
			urlA, urlB, te.Endpoints)
	}
}
