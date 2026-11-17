// file: internal/transcribe/journal_identity_test.go
// version: 1.0.0
// guid: 5b0f3f64-3a51-4f3e-9d0e-6c1f6f2a8e17
// last-edited: 2026-11-17

package transcribe

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
)

// The journal key of a result is the identity of the worker that DECODED it. The gate's
// /health probe can be up to gateProbeTTL old, and a worker process replaced since (its
// supervisor restarted it and --device auto resolved to another model) answers as a
// different model than that probe said. These tests give the cached probe one identity
// and the answer another, and pin that the row is keyed under the answer's.

// idAnswer is the "identity" object of a worker answer.
func idAnswer(model, backend, decodeFP, extractFP string) map[string]any {
	return map[string]any{"model": model, "backend": backend, "decode_fingerprint": decodeFP, "extract_fingerprint": extractFP}
}

// identityServer speaks /health, /transcribe and /transcribe-batch. identityFor is asked
// once per answer (its argument is the 0-based answer number) so a test can change the
// worker's identity between two answers; nil means an older worker (no identity key).
func identityServer(t *testing.T, health map[string]any, identityFor func(n int) map[string]any) *httptest.Server {
	t.Helper()
	health["batch_pipeline"] = true
	var mu sync.Mutex
	answers := 0
	next := func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		n := answers
		answers++
		if identityFor == nil {
			return nil
		}
		return identityFor(n)
	}
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
		out := map[string]any{"text": "T:" + string(b)}
		if id := next(); id != nil {
			out["identity"] = id
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /transcribe-batch", func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		results := map[string]map[string]any{}
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				break
			}
			b, _ := io.ReadAll(part)
			results[part.FileName()] = map[string]any{"text": "T:" + string(b)}
		}
		out := map[string]any{"results": results}
		if id := next(); id != nil {
			out["identity"] = id
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// clipHashOf is the sha256 the journal key is built from for a written clip.
func clipHashOf(t *testing.T, path string) string {
	t.Helper()
	h, err := hashClip(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (m *mapJournal) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.entries))
	for k := range m.entries {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (m *mapJournal) labels() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.models)
}

func clipKeyFor(model, fp, clipHash string) string {
	return resultjournal.ContentKey(clipHash, model, fp, clipParamsVersion)
}

// Upload path, per-file: the probe says X, the answer says Y, the row is keyed Y and
// labelled Y.
func TestIdentity_PerFileRowIsKeyedUnderTheAnswersIdentity(t *testing.T) {
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(int) map[string]any { return idAnswer("Nightwing", "cpu", "fp-y", "") })
	jobs := writeClips(t, 1)
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	if !ok || !h.supportsBatch() {
		t.Fatalf("setup: ok=%v", ok)
	}
	// Force the per-file protocol with the probe the gate "cached".
	h.BatchPipeline = nil
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	hash := clipHashOf(t, jobs["book-0"])
	want := clipKeyFor("cpu/Nightwing", "fp-y", hash)
	if got := j.keys(); len(got) != 1 || got[0] != want {
		t.Fatalf("journal keys = %v, want exactly the answer's identity key %s", got, want)
	}
	if l := j.labels(); len(l) != 1 || l[0] != "cpu/Nightwing#fp-y" {
		t.Fatalf("label = %v, want cpu/Nightwing#fp-y", l)
	}
	if _, stale, _ := j.Lookup(clipKeyFor("cpu/Robin", "fp-x", hash)); stale {
		t.Fatal("the probe's identity key was written for text another model produced")
	}
}

// Upload path, batch.
func TestIdentity_BatchRowIsKeyedUnderTheAnswersIdentity(t *testing.T) {
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(int) map[string]any { return idAnswer("Nightwing", "cpu", "fp-y", "") })
	jobs := writeClips(t, 3)
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	if !ok || !h.supportsBatch() {
		t.Fatalf("setup: ok=%v batch=%v", ok, h.supportsBatch())
	}
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, p := range jobs {
		want = append(want, clipKeyFor("cpu/Nightwing", "fp-y", clipHashOf(t, p)))
	}
	slices.Sort(want)
	if got := j.keys(); !slices.Equal(got, want) {
		t.Fatalf("journal keys = %v, want the answer's identity keys %v", got, want)
	}
}

// Path pipeline: model, decode AND extract fingerprint all come from the answer.
func TestIdentity_PathRowIsKeyedUnderTheAnswersIdentity(t *testing.T) {
	jobs := pathJobs(t, 1) // one file: a path job's signature is the file's stat
	keysFor := func(health, answer map[string]any) []string {
		w := newPathWorker(t, pathHealth(health))
		w.identity = answer
		useEndpoints(t, 0, w.srv.URL)
		j := newMapJournal()
		if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
			t.Fatal(err)
		}
		return j.keys()
	}
	// The key a worker whose /health already says Y produces: the reference.
	ref := keysFor(map[string]any{"backend": "cpu", "model": "Nightwing", "decode_fingerprint": "fp-y", "extract_fingerprint": "ex-y"}, nil)
	// A worker whose probe says X but whose answer says Y must produce the same key.
	got := keysFor(map[string]any{"backend": "cpu", "model": "Robin", "decode_fingerprint": "fp-x", "extract_fingerprint": "ex-x"},
		idAnswer("Nightwing", "cpu", "fp-y", "ex-y"))
	if len(ref) != 1 || !slices.Equal(ref, got) {
		t.Fatalf("path journal keys: answer-identified %v, want the key of a worker whose probe said so %v", got, ref)
	}
	// And not the probe's.
	probeKey := keysFor(map[string]any{"backend": "cpu", "model": "Robin", "decode_fingerprint": "fp-x", "extract_fingerprint": "ex-x"}, nil)
	if slices.Equal(got, probeKey) {
		t.Fatal("the row was keyed by the stale probe, not by the answer")
	}
}

// A mismatch drops the cached probe, so the next dispatch asks /health again instead of
// planning and gating from the view the answer just disproved.
func TestIdentity_AMismatchDropsTheCachedProbe(t *testing.T) {
	enableProbeCache(t)
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(int) map[string]any { return idAnswer("Nightwing", "cpu", "fp-y", "") })
	h, ok := probeForGate(context.Background(), srv.URL, true)
	if !ok {
		t.Fatal("setup probe failed")
	}
	gateProbeCache.mu.Lock()
	_, cached := gateProbeCache.m[srv.URL]
	gateProbeCache.mu.Unlock()
	if !cached {
		t.Fatal("setup: the probe was not cached")
	}
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 2), nil, newMapJournal()); err != nil {
		t.Fatal(err)
	}
	gateProbeCache.mu.Lock()
	_, cached = gateProbeCache.m[srv.URL]
	gateProbeCache.mu.Unlock()
	if cached {
		t.Fatal("the cached /health probe survived an answer that disagreed with it")
	}
}

// An answer that agrees with the probe leaves the cache alone (no extra probe per chunk).
func TestIdentity_AMatchKeepsTheCachedProbe(t *testing.T) {
	enableProbeCache(t)
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(int) map[string]any { return idAnswer("Robin", "cpu", "fp-x", "") })
	h, ok := probeForGate(context.Background(), srv.URL, true)
	if !ok {
		t.Fatal("setup probe failed")
	}
	j := newMapJournal()
	jobs := writeClips(t, 2)
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	gateProbeCache.mu.Lock()
	_, cached := gateProbeCache.m[srv.URL]
	gateProbeCache.mu.Unlock()
	if !cached {
		t.Fatal("a matching answer dropped the cached probe")
	}
	if len(j.keys()) != 2 {
		t.Fatalf("journalled %d, want 2", len(j.keys()))
	}
}

// A worker that predates the identity field is journalled under what /health said (the
// old behaviour), and the missing identity is logged once per endpoint, not per chunk.
func TestIdentity_AnOldWorkerKeepsTheHealthKey(t *testing.T) {
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"}, nil)
	noIdentityLogged.Delete(srv.URL)
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	jobs := writeClips(t, 2)
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, p := range jobs {
		want = append(want, clipKeyFor("cpu/Robin", "fp-x", clipHashOf(t, p)))
	}
	slices.Sort(want)
	if got := j.keys(); !slices.Equal(got, want) {
		t.Fatalf("journal keys = %v, want the /health identity keys %v", got, want)
	}
	if _, logged := noIdentityLogged.Load(srv.URL); !logged {
		t.Fatal("an answer without identity was not noted")
	}
}

// observe unit cases.
func TestIdentity_ObserveRules(t *testing.T) {
	t.Run("nil journal and nil identity are safe", func(t *testing.T) {
		var ej *endpointJournal
		ej.observe(&responseIdentity{Model: "m"})
		(&endpointJournal{endpoint: "unit-nil-id"}).observe(nil)
	})
	t.Run("a blank model is no identity", func(t *testing.T) {
		ej := &endpointJournal{endpoint: "unit-blank", model: "cpu/Robin", fingerprint: "fp"}
		ej.observe(&responseIdentity{Model: "  ", DecodeFingerprint: "other"})
		if id := ej.ident(); id.model != "cpu/Robin" || id.fingerprint != "fp" {
			t.Fatalf("a blank model overwrote the identity: %+v", id)
		}
	})
	t.Run("an answer that omits extract keeps the probe's", func(t *testing.T) {
		ej := &endpointJournal{endpoint: "unit-extract", model: "cpu/Robin", fingerprint: "fp", extract: "ex1"}
		ej.observe(&responseIdentity{Model: "Robin", Backend: "cpu", DecodeFingerprint: "fp2"})
		if id := ej.ident(); id.extract != "ex1" || id.fingerprint != "fp2" {
			t.Fatalf("ident = %+v, want extract ex1 kept and fingerprint fp2 taken", id)
		}
	})
	t.Run("concurrent observes and reads do not race", func(t *testing.T) {
		ej := &endpointJournal{endpoint: "unit-race", model: "cpu/Robin", fingerprint: "fp"}
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(2)
			go func() {
				defer wg.Done()
				ej.observe(&responseIdentity{Model: "M", Backend: "cpu", DecodeFingerprint: strings.Repeat("f", i+1)})
			}()
			go func() {
				defer wg.Done()
				id := ej.ident()
				_ = id.clipKey("h")
				_ = id.label()
			}()
		}
		wg.Wait()
	})
}

// The identity moves DURING a call (the worker is replaced between two chunks): each row
// is keyed under the identity of the answer that produced it, not all under the first.
func TestIdentity_RowsFollowTheWorkerAcrossAChunkBoundary(t *testing.T) {
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(n int) map[string]any {
			if n == 0 {
				return idAnswer("Robin", "cpu", "fp-x", "")
			}
			return idAnswer("Nightwing", "cpu", "fp-y", "")
		})
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	// One request per clip, strictly one after another, so answer 0 is clip 0's.
	h.BatchPipeline = nil
	jobs := writeClips(t, 2)
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	labels := j.labels()
	slices.Sort(labels)
	if want := []string{"cpu/Nightwing#fp-y", "cpu/Robin#fp-x"}; !slices.Equal(labels, want) {
		t.Fatalf("labels = %v, want one row per identity %v", labels, want)
	}
}
