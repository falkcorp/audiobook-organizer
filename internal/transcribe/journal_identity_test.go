// file: internal/transcribe/journal_identity_test.go
// version: 1.2.0
// guid: 5b0f3f64-3a51-4f3e-9d0e-6c1f6f2a8e17
// last-edited: 2026-11-17

package transcribe

import (
	"context"
	"encoding/json"
	"io"
	"maps"
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
	t.Run("a blank model is a partial identity", func(t *testing.T) {
		ej := &endpointJournal{endpoint: "unit-blank", model: "cpu/Robin", fingerprint: "fp"}
		if ej.observe(&responseIdentity{Model: "  ", Backend: "cpu", DecodeFingerprint: "other"}) {
			t.Fatal("an answer with a blank model was accepted for journalling")
		}
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

// partialIdentityCases are identity objects an answer can carry that are present but not
// complete: a journal key built from any of them would be less specific than the worker's
// real identity. "Complete" is model, backend and decode_fingerprint (the three that key
// every row).
var partialIdentityCases = []struct {
	name   string
	answer map[string]any
}{
	// The reviewer's trace: the probe said backend and fingerprint, the answer has neither.
	{"blank backend and fingerprint", idAnswer("Robin", "", "", "ex-x")},
	{"blank backend", idAnswer("Robin", "", "fp-y", "ex-x")},
	{"blank fingerprint", idAnswer("Robin", "cpu", "", "ex-x")},
	{"spaces-only fingerprint", idAnswer("Robin", "cpu", "   ", "ex-x")},
	{"blank model", idAnswer("", "cpu", "fp-y", "ex-x")},
	{"empty object", map[string]any{}},
}

// A partial identity is not trusted: the answer is not journalled at all, under neither its
// own partial values (a model-only key that a fingerprint-less worker with other decode
// settings could be served from) nor the probe's (the answer said something else).
func TestIdentity_APartialIdentityIsNotJournalled(t *testing.T) {
	health := map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x", "extract_fingerprint": "ex-x"}
	for _, tc := range partialIdentityCases {
		t.Run("batch/"+tc.name, func(t *testing.T) {
			srv := identityServer(t, maps.Clone(health), func(int) map[string]any { return tc.answer })
			jobs := writeClips(t, 2)
			h, ok := probeRemoteHealth(context.Background(), srv.URL)
			if !ok || !h.supportsBatch() {
				t.Fatalf("setup: ok=%v", ok)
			}
			j := newMapJournal()
			res, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 2 {
				t.Fatalf("the transcripts must still reach the caller, got %d results", len(res))
			}
			if got := j.keys(); len(got) != 0 {
				t.Fatalf("a partial identity was journalled under %v", got)
			}
		})
		t.Run("per-file/"+tc.name, func(t *testing.T) {
			srv := identityServer(t, maps.Clone(health), func(int) map[string]any { return tc.answer })
			jobs := writeClips(t, 2)
			h, ok := probeRemoteHealth(context.Background(), srv.URL)
			h.BatchPipeline = nil
			j := newMapJournal()
			res, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j)
			if err != nil {
				t.Fatal(err)
			}
			if len(res) != 2 {
				t.Fatalf("the transcripts must still reach the caller, got %d results", len(res))
			}
			if got := j.keys(); len(got) != 0 {
				t.Fatalf("a partial identity was journalled under %v", got)
			}
		})
		t.Run("path/"+tc.name, func(t *testing.T) {
			w := newPathWorker(t, pathHealth(maps.Clone(health)))
			w.identity = tc.answer
			useEndpoints(t, 0, w.srv.URL)
			j := newMapJournal()
			if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{Journal: j}); err != nil {
				t.Fatal(err)
			}
			if got := j.keys(); len(got) != 0 {
				t.Fatalf("a partial identity was journalled under %v", got)
			}
		})
	}
}

// The extract fingerprint is part of a path key only. A path answer without it is partial
// (its key would mix the answer's decode identity with the probe's extraction settings);
// an upload answer without it is complete, because no upload key contains it.
func TestIdentity_ExtractIsRequiredForPathRowsOnly(t *testing.T) {
	health := map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x", "extract_fingerprint": "ex-x"}
	noExtract := idAnswer("Nightwing", "cpu", "fp-y", "")

	srv := identityServer(t, maps.Clone(health), func(int) map[string]any { return noExtract })
	jobs := writeClips(t, 1)
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, jobs, nil, j); err != nil {
		t.Fatal(err)
	}
	if want := clipKeyFor("cpu/Nightwing", "fp-y", clipHashOf(t, jobs["book-0"])); len(j.keys()) != 1 || j.keys()[0] != want {
		t.Fatalf("upload answer without extract: keys = %v, want %s", j.keys(), want)
	}

	w := newPathWorker(t, pathHealth(maps.Clone(health)))
	w.identity = noExtract
	useEndpoints(t, 0, w.srv.URL)
	jp := newMapJournal()
	if _, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{Journal: jp}); err != nil {
		t.Fatal(err)
	}
	if got := jp.keys(); len(got) != 0 {
		t.Fatalf("a path answer without extract_fingerprint was journalled under %v", got)
	}
}

// A partial answer between two complete ones costs only its own rows: the call keeps its
// identity from the last complete answer and the later complete answer is journalled.
func TestIdentity_APartialAnswerSkipsOnlyItsOwnRows(t *testing.T) {
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(n int) map[string]any {
			if n == 1 {
				return idAnswer("Robin", "", "", "")
			}
			return idAnswer("Robin", "cpu", "fp-x", "")
		})
	h, ok := probeRemoteHealth(context.Background(), srv.URL)
	h.BatchPipeline = nil
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 3), nil, j); err != nil {
		t.Fatal(err)
	}
	if got := j.keys(); len(got) != 2 {
		t.Fatalf("journalled %d rows, want the 2 complete answers' rows", len(got))
	}
}

// Without a journal there is nothing to key, but the cached probe is just as stale: an
// answer that disagrees with it drops it all the same.
func TestIdentity_AMismatchDropsTheCachedProbeWithoutAJournal(t *testing.T) {
	enableProbeCache(t)
	srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
		func(int) map[string]any { return idAnswer("Nightwing", "cpu", "fp-y", "") })
	h, ok := probeForGate(context.Background(), srv.URL, true)
	if !ok {
		t.Fatal("setup probe failed")
	}
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 2), nil, nil); err != nil {
		t.Fatal(err)
	}
	gateProbeCache.mu.Lock()
	_, cached := gateProbeCache.m[srv.URL]
	gateProbeCache.mu.Unlock()
	if cached {
		t.Fatal("with no journal, the cached /health probe survived an answer that disagreed with it")
	}
}

// A partial identity shows the cached view is stale just as a different one does, so it drops
// the cached probe too (and with no journal at all).
func TestIdentity_APartialIdentityDropsTheCachedProbe(t *testing.T) {
	for _, tc := range partialIdentityCases {
		for _, withJournal := range []bool{true, false} {
			name := tc.name + "/no-journal"
			if withJournal {
				name = tc.name + "/journal"
			}
			t.Run(name, func(t *testing.T) {
				enableProbeCache(t)
				srv := identityServer(t, map[string]any{"status": "ok", "model": "Robin", "backend": "cpu", "decode_fingerprint": "fp-x"},
					func(int) map[string]any { return tc.answer })
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
				var j ResultJournal
				if withJournal {
					j = newMapJournal()
				}
				if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 2), nil, j); err != nil {
					t.Fatal(err)
				}
				gateProbeCache.mu.Lock()
				_, cached = gateProbeCache.m[srv.URL]
				gateProbeCache.mu.Unlock()
				if cached {
					t.Fatal("the cached /health probe survived an answer with a partial identity")
				}
			})
		}
	}
}

// Same without a journal, for a probe that named no model (so nothing could be journalled
// anyway): the answer still shows the cached view is stale.
func TestIdentity_AMismatchDropsTheCachedProbeOfAModellessEndpoint(t *testing.T) {
	enableProbeCache(t)
	srv := identityServer(t, map[string]any{"status": "ok"},
		func(int) map[string]any { return idAnswer("Nightwing", "cpu", "fp-y", "") })
	h, ok := probeForGate(context.Background(), srv.URL, true)
	if !ok {
		t.Fatal("setup probe failed")
	}
	j := newMapJournal()
	if _, err := transcribeRemoteWithHealth(context.Background(), srv.URL, h, ok, 1, writeClips(t, 2), nil, j); err != nil {
		t.Fatal(err)
	}
	if j.len() != 0 {
		t.Fatalf("an endpoint whose probe named no model was journalled: %v", j.keys())
	}
	gateProbeCache.mu.Lock()
	_, cached := gateProbeCache.m[srv.URL]
	gateProbeCache.mu.Unlock()
	if cached {
		t.Fatal("the cached /health probe survived an answer that disagreed with it")
	}
}

// observe's contract in one table: what it reports about whether the answer's rows may be
// journalled, and what it leaves behind.
func TestIdentity_ObserveReturnsWhetherTheAnswerMayBeJournalled(t *testing.T) {
	probe := func() *endpointJournal {
		return &endpointJournal{journal: newMapJournal(), endpoint: "unit-ret", model: "cpu/Robin", fingerprint: "fp-x", extract: "ex-x"}
	}
	t.Run("an answer without identity", func(t *testing.T) {
		if !probe().observe(nil) {
			t.Fatal("an old worker's answer must still be journalled under /health")
		}
	})
	t.Run("a complete identity", func(t *testing.T) {
		ej := probe()
		if !ej.observe(&responseIdentity{Model: "Nightwing", Backend: "cpu", DecodeFingerprint: "fp-y"}) {
			t.Fatal("a complete identity was refused")
		}
		if id := ej.ident(); id.model != "cpu/Nightwing" || id.fingerprint != "fp-y" {
			t.Fatalf("ident = %+v", id)
		}
	})
	for _, tc := range partialIdentityCases {
		t.Run("partial/"+tc.name, func(t *testing.T) {
			ej := probe()
			resp := &responseIdentity{}
			b, _ := json.Marshal(tc.answer)
			if err := json.Unmarshal(b, resp); err != nil {
				t.Fatal(err)
			}
			if ej.observe(resp) {
				t.Fatalf("partial identity %+v accepted", resp)
			}
			if id := ej.ident(); id.model != "cpu/Robin" || id.fingerprint != "fp-x" || id.extract != "ex-x" {
				t.Fatalf("a refused answer changed the identity: %+v", id)
			}
		})
	}
}
