// file: internal/transcribe/journal_test.go
// version: 1.0.0
// guid: 971eb805-72d8-4757-83dd-d7e9da270afb
// last-edited: 2026-09-19

package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// mapJournal is an in-memory ResultJournal that records every Complete.
type mapJournal struct {
	mu      sync.Mutex
	entries map[string]json.RawMessage
	models  []string
}

func newMapJournal() *mapJournal { return &mapJournal{entries: map[string]json.RawMessage{}} }

func (m *mapJournal) Lookup(k string) (json.RawMessage, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[k]
	return v, ok, nil
}

func (m *mapJournal) Complete(k, _, model string, result any) error {
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[k] = b
	m.models = append(m.models, model)
	return nil
}

func (m *mapJournal) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// perFileServer speaks the legacy per-file protocol (POST /transcribe, one
// "file" part). health is what /health returns (no batch_pipeline => per-file).
func perFileServer(t *testing.T, health map[string]any) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(health)
	})
	mux.HandleFunc("POST /transcribe", func(w http.ResponseWriter, r *http.Request) {
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := io.ReadAll(f)
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "T:" + string(b)})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

func writeClips(t *testing.T, n int) map[string]string {
	t.Helper()
	dir := t.TempDir()
	jobs := make(map[string]string, n)
	for i := range n {
		p := filepath.Join(dir, fmt.Sprintf("c%d.wav", i))
		if err := os.WriteFile(p, fmt.Appendf(nil, "clip-%d", i), 0o644); err != nil {
			t.Fatal(err)
		}
		jobs[fmt.Sprintf("book-%d", i)] = p
	}
	return jobs
}

// Per-file path: each result is journalled before its progress tick, and a
// second call is served entirely from the journal.
func TestPerFile_JournalsBeforeProgressAndServesRerun(t *testing.T) {
	srv, calls := perFileServer(t, map[string]any{"status": "ok", "model": "m1"})
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	if !ok || h.supportsBatch() {
		t.Fatalf("setup: probe ok=%v batch=%v", ok, h.supportsBatch())
	}
	jobs := writeClips(t, 6)
	j := newMapJournal()

	var progressMu sync.Mutex
	onProgress := func(done, _ int) {
		progressMu.Lock()
		defer progressMu.Unlock()
		if got := j.len(); got < done {
			t.Errorf("progress reported %d done with only %d journalled", done, got)
		}
	}
	res, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 2, jobs, onProgress, j)
	if err != nil || len(res) != 6 {
		t.Fatalf("first call: %d results, err=%v", len(res), err)
	}
	if calls() != 6 || j.len() != 6 {
		t.Fatalf("first call: server calls=%d journalled=%d, want 6/6", calls(), j.len())
	}
	res2, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 2, jobs, nil, j)
	if err != nil {
		t.Fatal(err)
	}
	if calls() != 6 {
		t.Fatalf("rerun re-sent %d clips, want 0", calls()-6)
	}
	for id, r := range res {
		if res2[id] != r {
			t.Errorf("%s: served %+v, transcribed %+v", id, res2[id], r)
		}
	}
}

// An endpoint that reports no model is not journalled at all: its results
// must not be keyed under a blank model where another model's lookup could
// find them.
func TestJournal_NoModelMeansNoJournal(t *testing.T) {
	srv, _ := perFileServer(t, map[string]any{"status": "ok"})
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 2), nil, j); err != nil {
		t.Fatal(err)
	}
	if j.len() != 0 {
		t.Fatalf("journalled %d results from a model-less endpoint, want 0", j.len())
	}
}

// A result from model A is never served for model B.
func TestJournal_KeyIncludesModel(t *testing.T) {
	jobs := writeClips(t, 1)
	j := newMapJournal()
	srvA, callsA := perFileServer(t, map[string]any{"status": "ok", "model": "A"})
	hA, okA := probeRemoteHealth(context.Background(), srvA.URL)
	if _, err := transcribeRemoteWithHealth(context.Background(), srvA.URL, hA, okA, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	srvB, callsB := perFileServer(t, map[string]any{"status": "ok", "model": "B"})
	hB, okB := probeRemoteHealth(context.Background(), srvB.URL)
	if _, err := transcribeRemoteWithHealth(context.Background(), srvB.URL, hB, okB, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	if callsA() != 1 || callsB() != 1 {
		t.Fatalf("calls A=%d B=%d, want 1/1 (model B must not be served model A's text)", callsA(), callsB())
	}
}

// Per-file whisper errors are not journalled (they would be served forever).
func TestJournal_PerFileErrorNotJournalled(t *testing.T) {
	ej := &endpointJournal{journal: newMapJournal(), endpoint: "e", model: "m", keys: map[string]string{"a": "ka", "b": "kb"}}
	ej.complete(map[string]BatchResult{"a": {Error: "decode failed"}, "b": {Text: "ok"}})
	if n := ej.journal.(*mapJournal).len(); n != 1 {
		t.Fatalf("journalled %d, want 1 (only the success)", n)
	}
}
