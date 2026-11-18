// file: internal/transcribe/batch_model_test.go
// version: 1.0.1
// guid: a47c0e92-6d18-4f35-b2a9-5e1f83d7c046
// last-edited: 2026-11-17

package transcribe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The model a result carries is the model the answering worker reported; the
// language is whatever the worker reports and is never assumed.

func TestBatchResult_ModelComesFromTheAnsweringWorkersHealth(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{"model": "gotham-large-v3", "machine_id": "aaaa0001"}))
	useEndpoints(t, 0, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 3), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Fatalf("got %d results", len(res))
	}
	for id, r := range res {
		if r.Model != "gotham-large-v3" {
			t.Errorf("%s: Model = %q, want the worker's model", id, r.Model)
		}
		// Neither health nor the response names a language: it stays empty
		// rather than being filled with an assumed one.
		if r.Language != "" {
			t.Errorf("%s: Language = %q, want empty (the worker did not report one)", id, r.Language)
		}
	}
}

func TestBatchResult_LanguageFromHealthWhenTheWorkerReportsIt(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{"model": "M", "language": "en", "machine_id": "aaaa0001"}))
	useEndpoints(t, 0, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 2), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for id, r := range res {
		if r.Language != "en" || r.Model != "M" {
			t.Errorf("%s: model %q language %q, want M / en", id, r.Model, r.Language)
		}
	}
}

func TestBatchResult_AResultThatNamesItsOwnModelWinsOverHealth(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{"model": "from-health", "machine_id": "aaaa0001"}))
	w.respond = func(_ string, _ map[string]any) map[string]any {
		return map[string]any{"text": "Welcome to Gotham City.", "error": nil, "model": "from-result", "language": "fr"}
	}
	useEndpoints(t, 0, w.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 1), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for id, r := range res {
		if r.Model != "from-result" || r.Language != "fr" {
			t.Errorf("%s: model %q language %q, want from-result / fr", id, r.Model, r.Language)
		}
	}
}

func TestBatchResult_JournalServedResultsCarryTheModelToo(t *testing.T) {
	w := newPathWorker(t, pathHealth(map[string]any{"model": "gotham-large-v3", "machine_id": "aaaa0001"}))
	useEndpoints(t, 0, w.srv.URL)
	jobs := pathJobs(t, 2)
	j := newMapJournal()
	if _, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j}); err != nil {
		t.Fatal(err)
	}
	second, err := TranscribeByPathBatch(context.Background(), jobs, BatchOptions{Journal: j})
	if err != nil {
		t.Fatal(err)
	}
	if w.postCount() != 1 {
		t.Fatalf("rerun reached the worker (%d POSTs)", w.postCount())
	}
	for id, r := range second {
		if r.Model != "gotham-large-v3" {
			t.Errorf("%s: journal-served Model = %q, want the endpoint's model", id, r.Model)
		}
	}
}

// sendPathBatch decodes a model/language named once for the whole response, and
// a per-result one overrides it.
func TestSendPathBatch_DecodesModelAndLanguage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"model": "top-model", "language": "en",
			"results": map[string]any{
				"a": map[string]any{"text": "A", "error": nil},
				"b": map[string]any{"text": "B", "error": nil, "model": "own-model", "language": "de"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	got, _, err := sendPathBatch(context.Background(), srv.Client(), srv.URL, []pathJobEntry{
		{id: "a", job: PathJob{Path: "/x/a", Seconds: 1}},
		{id: "b", job: PathJob{Path: "/x/b", Seconds: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["a"].Model != "top-model" || got["a"].Language != "en" {
		t.Errorf("a = %+v, want the response-level model and language", got["a"])
	}
	if got["b"].Model != "own-model" || got["b"].Language != "de" {
		t.Errorf("b = %+v, want its own model and language", got["b"])
	}
}

// A worker whose /health was never read is not credited with a model.
func TestStampAnswer_UnprobedEndpointGetsNoModel(t *testing.T) {
	g := gatedEndpoint{Endpoint: Endpoint{URL: "http://192.0.2.10:8000"}, Health: remoteHealth{Model: "M", Language: "en"}}
	r := stampAnswer(BatchResult{Text: "x"}, g, g.Endpoint.URL, g.storedWorker())
	if r.Model != "" || r.Language != "" || r.Worker != "" || r.Endpoint != g.Endpoint.URL {
		t.Fatalf("unprobed stamp = %+v", r)
	}
	g.Probed = true
	r = stampAnswer(BatchResult{Text: "x"}, g, g.Endpoint.URL, g.storedWorker())
	if r.Model != "M" || r.Language != "en" {
		t.Fatalf("probed stamp = %+v", r)
	}
}

// In a pool each answer carries the model of the worker that gave it, not of the
// pool's first member.
func TestBatchResult_PoolStampsEachAnswerWithItsOwnWorkersModel(t *testing.T) {
	a := newPathWorker(t, pathHealth(map[string]any{"model": "model-a", "machine_id": "aaaa0001"}))
	b := newPathWorker(t, pathHealth(map[string]any{"model": "model-b", "machine_id": "bbbb0002"}))
	useEndpoints(t, 1, a.srv.URL, b.srv.URL)
	res, err := TranscribeByPathBatch(context.Background(), pathJobs(t, 8), BatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{a.srv.URL: "model-a", b.srv.URL: "model-b"}
	seen := map[string]bool{}
	for id, r := range res {
		if r.Model != want[r.Endpoint] {
			t.Errorf("%s answered by %s carries model %q, want %q", id, r.Endpoint, r.Model, want[r.Endpoint])
		}
		seen[r.Endpoint] = true
	}
	if len(seen) == 0 {
		t.Fatal("no endpoint answered")
	}
}
