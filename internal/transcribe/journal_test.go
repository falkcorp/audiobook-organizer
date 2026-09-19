// file: internal/transcribe/journal_test.go
// version: 1.1.0
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

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	ej := &endpointJournal{journal: newMapJournal(), endpoint: "e", model: "m"}
	ej.complete(map[string]BatchResult{"a": {Error: "decode failed"}, "b": {Text: "ok"}},
		map[string]string{"a": "ha", "b": "hb"})
	if n := ej.journal.(*mapJournal).len(); n != 1 {
		t.Fatalf("journalled %d, want 1 (only the success)", n)
	}
}

// Empty text is never journalled: a silent result is exactly what decoder or
// VAD tuning changes, and a journalled "" would be served to retry_silence
// forever without the server ever hearing the clip again.
func TestJournal_EmptyTextNotJournalled(t *testing.T) {
	j := newMapJournal()
	ej := &endpointJournal{journal: j, endpoint: "e", model: "m"}
	ej.completeOne("a", "hash-a", BatchResult{Text: ""})
	if j.len() != 0 {
		t.Fatalf("journalled %d empty results, want 0", j.len())
	}
}

// Same model, different decode_fingerprint: the second server must be asked,
// not served the first configuration's transcript.
func TestJournal_KeyIncludesDecodeFingerprint(t *testing.T) {
	jobs := writeClips(t, 1)
	j := newMapJournal()
	srvA, callsA := perFileServer(t, map[string]any{"status": "ok", "model": "M", "decode_fingerprint": "fp-a"})
	hA, okA := probeRemoteHealth(context.Background(), srvA.URL)
	if _, err := transcribeRemoteWithHealth(context.Background(), srvA.URL, hA, okA, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	srvB, callsB := perFileServer(t, map[string]any{"status": "ok", "model": "M", "decode_fingerprint": "fp-b"})
	hB, okB := probeRemoteHealth(context.Background(), srvB.URL)
	if _, err := transcribeRemoteWithHealth(context.Background(), srvB.URL, hB, okB, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	if callsA() != 1 || callsB() != 1 {
		t.Fatalf("calls A=%d B=%d, want 1/1 (a retuned decoder must not be served the old transcript)", callsA(), callsB())
	}
}

// hookJournal runs hook on the first Lookup -- i.e. after plan hashed the
// clip and before it is uploaded.
type hookJournal struct {
	*mapJournal
	once sync.Once
	hook func()
}

func (h *hookJournal) Lookup(k string) (json.RawMessage, bool, error) {
	h.once.Do(h.hook)
	return h.mapJournal.Lookup(k)
}

// The journal key must describe the bytes whisper actually heard. Here the
// clip changes between plan's hash and the upload (a concurrent writer
// replacing the cache file); the result must be keyed by the UPLOADED bytes,
// so a later run over the new bytes is served from the journal.
func TestJournal_KeyIsFromUploadedBytes(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%v", batch), func(t *testing.T) {
			health := map[string]any{"status": "ok", "model": "M"}
			var srv *httptest.Server
			var calls func() int
			if batch {
				srv, calls = batchServer(t, health)
			} else {
				srv, calls = perFileServer(t, health)
			}
			h, ok := probeRemoteHealth(context.Background(), srv.URL)
			jobs := writeClips(t, 1)
			path := jobs["book-0"]
			j := &hookJournal{mapJournal: newMapJournal(), hook: func() {
				if err := os.WriteFile(path, []byte("the-full-clip"), 0o644); err != nil {
					t.Error(err)
				}
			}}
			if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
				t.Fatal(err)
			}
			if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j.mapJournal); err != nil {
				t.Fatal(err)
			}
			if calls() != 1 {
				t.Fatalf("server calls=%d, want 1: the result was keyed by the bytes hashed before upload, not the bytes uploaded", calls())
			}
		})
	}
}

// batchServer speaks /transcribe-batch; health must set batch_pipeline.
func batchServer(t *testing.T, health map[string]any) (*httptest.Server, func() int) {
	t.Helper()
	health["batch_pipeline"] = true
	var mu sync.Mutex
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(health)
	})
	mux.HandleFunc("POST /transcribe-batch", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := map[string]map[string]any{}
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				break
			}
			b, _ := io.ReadAll(part)
			mu.Lock()
			calls++
			mu.Unlock()
			out[part.FileName()] = map[string]any{"text": "T:" + string(b)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// RefreshJournal skips every journal LOOKUP (retry_silence) but still
// journals the fresh results.
func TestTranscribeBatchOpts_RefreshJournalBypassesLookup(t *testing.T) {
	srv, calls := perFileServer(t, map[string]any{"status": "ok", "model": "M"})
	orig := config.Snapshot()
	config.Mutate(func(c *config.Config) {
		c.WhisperRemoteURL = srv.URL
		c.WhisperEndpoints = nil
		c.WhisperRequires = nil
	})
	t.Cleanup(func() {
		config.Mutate(func(c *config.Config) {
			c.WhisperRemoteURL = orig.WhisperRemoteURL
			c.WhisperEndpoints = orig.WhisperEndpoints
			c.WhisperRequires = orig.WhisperRequires
		})
	})
	jobs := writeClips(t, 2)
	j := newMapJournal()
	if _, err := TranscribeBatchOpts(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
		t.Fatal(err)
	}
	if _, err := TranscribeBatchOpts(context.Background(), jobs, BatchOptions{Journal: j, RefreshJournal: true}); err != nil {
		t.Fatal(err)
	}
	if calls() != 4 {
		t.Fatalf("server calls=%d, want 4 (refresh must reach the server)", calls())
	}
	if j.len() != 2 {
		t.Fatalf("journalled %d, want 2 (refresh still journals)", j.len())
	}
}
