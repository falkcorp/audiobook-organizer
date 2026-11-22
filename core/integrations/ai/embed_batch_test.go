// file: core/integrations/ai/embed_batch_test.go
// version: 0.1.0
// guid: 5b0f7c2e-93a4-4d68-b1e7-2c8a4f6d9e13
// last-edited: 2026-11-22

package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/tasks"
)

// embedBatchEnv is a harness with one embed capability that requires batch_api,
// its apply task registered, and one batch-capable embedding endpoint.
type embedBatchEnv struct {
	*harness
	c    ai.Capability
	f    interface{ RemoteBatches() []ai.BatchHandle }
	def  tasks.Definition
	mu   sync.Mutex
	rows map[string]ai.BatchResult
}

func (e *embedBatchEnv) handle(_ *tasks.RC, _ ai.BatchJob, results iter.Seq2[ai.BatchResult, error]) (ai.BatchOutcome, error) {
	var out ai.BatchOutcome
	for row, err := range results {
		if err != nil {
			return out, err
		}
		e.mu.Lock()
		e.rows[row.CustomID] = row
		e.mu.Unlock()
		out.Applied++
	}
	return out, nil
}

func newEmbedBatchEnv(t *testing.T, opts ...harnessOption) (*embedBatchEnv, func(ai.BatchHandle)) {
	t.Helper()
	h := newHarness(t, opts...)
	c := h.capability("embed_batch", func(s *ai.CapabilitySpec) {
		s.Kind, s.RequiredFeatures = ai.KindEmbed, []ai.Feature{ai.FeatureBatch}
	})
	e := &embedBatchEnv{harness: h, c: c, rows: map[string]ai.BatchResult{}}
	e.def = registerApply(t, c, e.handle)
	f := fake()
	e.f = f
	set := textEndpoint("joker-7b", "gotham.embed_batch")
	set.EmbedModel = "joker-embed"
	set.Features = []ai.Feature{ai.FeatureBatch}
	h.addEndpoint("arkham", 1, set, f)
	return e, f.CompleteBatch
}

func embedItems() []ai.BatchItem {
	return []ai.BatchItem{
		ai.EmbedItem("e1", ai.EmbedRequest{Inputs: []string{"the batcave", "the bat signal"}}),
		ai.EmbedItem("e2", ai.EmbedRequest{Inputs: []string{"arkham"}}),
	}
}

// A batch of embeddings goes through the whole path: submit (pinned model),
// the remote completes, the poll stages the apply run, the module's handler
// receives one Embed answer per item with its vectors and usage, and the job
// completes.
func TestEmbedBatch_RoundTripSubmitPollApply(t *testing.T) {
	t.Parallel()
	e, complete := newEmbedBatchEnv(t, withMetrics())
	ctx := context.Background()
	id, err := e.r.SubmitBatch(ctx, e.c, embedItems(), []byte(`{"case":"vectors"}`), ai.WithPinnedModel("joker-embed"))
	if err != nil {
		t.Fatalf("SubmitBatch of embed items: %v", err)
	}
	remote := e.f.RemoteBatches()
	if len(remote) != 1 {
		t.Fatalf("%d remote batches, want 1", len(remote))
	}
	complete(remote[0])
	if res, err := e.r.PollBatches(newRC(t), ai.BatchPollParams{}); err != nil || res.ApplyStaged != 1 {
		t.Fatalf("PollBatches = %+v, %v, want the job moved to applying", res, err)
	}
	if _, err := runDefinition(t, e.def, newApplyRC(t, e.def.Kind()), fmt.Sprintf(`{"job":%q}`, id)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(e.rows) != 2 {
		t.Fatalf("handler saw %d rows, want 2", len(e.rows))
	}
	for cid, want := range map[string]int{"e1": 2, "e2": 1} {
		row := e.rows[cid]
		if row.Text != nil || row.Embed == nil || len(row.Embed.Vectors) != want || row.Embed.Usage.InputTokens == 0 {
			t.Errorf("row %s = %+v, want an embed answer with %d vector(s) and usage", cid, row, want)
		}
	}
	job, err := e.r.BatchJob(ctx, id)
	if err != nil || job.State != ai.JobCompleted {
		t.Errorf("job after apply = %+v, %v, want completed", job, err)
	}
	// The embed answers' tokens reach the batch counters: FakeAI answers each
	// embed item with 10 input tokens.
	got := series(t, e.prov.Gatherer(), "mediacore_ai_tokens_total", map[string]string{"mode": "batch", "direction": "input"}, true)
	if got != 20 {
		t.Errorf("batch input tokens = %v, want 20 (2 embed rows x 10)", got)
	}
}

// SubmitBatch refuses, before a job row or a remote batch exists, a batch that
// is not one kind of item, an embed batch without a pinned model, and a
// capability that is neither text nor embed work.
func TestEmbedBatch_SubmitRefusals(t *testing.T) {
	t.Parallel()
	e, _ := newEmbedBatchEnv(t)
	text := e.capability("riddle_batch")
	registerApply(t, text, e.handle)
	ctx := context.Background()
	pin := ai.WithPinnedModel("joker-embed")
	cases := []struct {
		name  string
		c     ai.Capability
		items []ai.BatchItem
		opts  []ai.CallOption
		want  error
	}{
		{"embed batch without a pinned model", e.c, embedItems(), nil, ai.ErrEmbedModelNotPinned},
		{"a text item in an embed batch", e.c, append(embedItems(), ai.BatchItem{CustomID: "t1", Request: ai.TextRequest{System: "x"}}), []ai.CallOption{pin}, ai.ErrBatchKind},
		{"an embed item in a text batch", text, []ai.BatchItem{ai.EmbedItem("e1", ai.EmbedRequest{Inputs: []string{"a"}})}, nil, ai.ErrBatchKind},
	}
	for _, tc := range cases {
		if _, err := e.r.SubmitBatch(ctx, tc.c, tc.items, nil, tc.opts...); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := e.r.SubmitBatch(ctx, e.c, []ai.BatchItem{ai.EmbedItem("e1", ai.EmbedRequest{})}, nil, pin); err == nil {
		t.Error("an embed item with no inputs was accepted")
	}
	if n := len(e.f.RemoteBatches()); n != 0 {
		t.Errorf("%d remote batch(es) created by refused submits", n)
	}

	tr := e.capability("transcribe_batch", func(s *ai.CapabilitySpec) { s.Kind, s.RequiredFeatures = ai.KindTranscribe, nil })
	if _, err := e.r.SubmitBatch(ctx, tr, items(1), nil); !errors.Is(err, ai.ErrBatchKind) {
		t.Errorf("SubmitBatch of transcription work: err = %v, want ErrBatchKind", err)
	}
}

// The same path over the real openai adapter and a fake provider: the upload
// is a /v1/embeddings JSONL file with the pinned model on every line, the batch
// is created for /v1/embeddings, and the provider's embeddings rows come back
// to the module's handler as Embed answers.
func TestEmbedBatch_OpenAIAdapterRoundTrip(t *testing.T) {
	t.Parallel()
	h, kit := openaiHarness(t)
	var mu sync.Mutex
	var uploaded []string
	var created map[string]any
	kit.Handle(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.Method + " " + req.URL.Path {
		case "POST /v1/files":
			if err := req.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload is not multipart: %v", err)
			}
			f, _, err := req.FormFile("file")
			if err != nil {
				t.Errorf("no file part: %v", err)
				return
			}
			raw, _ := io.ReadAll(f)
			mu.Lock()
			uploaded = strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"file-in-1","object":"file","bytes":1,"created_at":1,"filename":"batch.jsonl","purpose":"batch"}`))
		case "POST /v1/batches":
			mu.Lock()
			_ = json.NewDecoder(req.Body).Decode(&created)
			mu.Unlock()
			_, _ = w.Write([]byte(embedBatchAnswer("validating", "")))
		case "GET /v1/batches/batch_e1":
			_, _ = w.Write([]byte(embedBatchAnswer("completed", "file-out-1")))
		case "GET /v1/files/file-out-1/content":
			row := func(id string, n int) string {
				data := make([]string, n)
				for i := range data {
					data[i] = fmt.Sprintf(`{"object":"embedding","index":%d,"embedding":[0.5,%d.25]}`, i, i)
				}
				return fmt.Sprintf(`{"id":"r","custom_id":%q,"response":{"status_code":200,"request_id":"x","body":{"object":"list","data":[%s],"model":"joker-embed","usage":{"prompt_tokens":7,"total_tokens":7}}},"error":null}`,
					id, strings.Join(data, ","))
			}
			_, _ = io.WriteString(w, row("e1", 2)+"\n"+row("e2", 1)+"\n")
		default:
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"joker-embed","object":"model","created":1,"owned_by":"wayne"}]}`))
		}
	})
	c := h.capability("embed_batch", func(s *ai.CapabilitySpec) {
		s.Kind, s.RequiredFeatures = ai.KindEmbed, []ai.Feature{ai.FeatureBatch}
	})
	e := &embedBatchEnv{harness: h, c: c, rows: map[string]ai.BatchResult{}}
	e.def = registerApply(t, c, e.handle)
	cfg := compatConfig(t, aiRef("arkham"), kit.URL()+"/v1", true, 0, func(m map[string]any) {
		m["capabilities"] = []any{"gotham.embed_batch"}
		m["embed_model"] = "joker-embed"
		m["features"] = []any{"batch_api"}
	})
	if _, err := h.reg.Put(admin(), cfg, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ctx := context.Background()
	id, err := h.r.SubmitBatch(ctx, c, embedItems(), nil, ai.WithPinnedModel("joker-embed"))
	if err != nil {
		t.Fatalf("SubmitBatch: %v", err)
	}
	mu.Lock()
	lines, body := uploaded, created
	mu.Unlock()
	if len(lines) != 2 {
		t.Fatalf("uploaded %d line(s), want 2: %q", len(lines), lines)
	}
	var first struct {
		CustomID, Method, URL string
		Body                  struct {
			Model string
			Input []string
		}
	}
	if err := json.Unmarshal([]byte(strings.ToLower(lines[0])), &first); err != nil {
		t.Fatal(err)
	}
	if first.URL != "/v1/embeddings" || first.Body.Model != "joker-embed" || len(first.Body.Input) != 2 || body["endpoint"] != "/v1/embeddings" {
		t.Errorf("first line = %+v, create body = %v; want /v1/embeddings, the pinned model and both inputs", first, body)
	}

	if res, err := h.r.PollBatches(newRC(t), ai.BatchPollParams{}); err != nil || res.ApplyStaged != 1 {
		t.Fatalf("PollBatches = %+v, %v", res, err)
	}
	if _, err := runDefinition(t, e.def, newApplyRC(t, e.def.Kind()), fmt.Sprintf(`{"job":%q}`, id)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for cid, want := range map[string]int{"e1": 2, "e2": 1} {
		row := e.rows[cid]
		if row.Embed == nil || len(row.Embed.Vectors) != want || row.Embed.Vectors[0][0] != 0.5 || row.Embed.Model != "joker-embed" || row.Embed.Usage.InputTokens != 7 {
			t.Errorf("row %s = %+v, want %d vector(s), the model and 7 input tokens", cid, row.Embed, want)
		}
	}
}

func embedBatchAnswer(status, out string) string {
	return fmt.Sprintf(`{"id":"batch_e1","object":"batch","endpoint":"/v1/embeddings","input_file_id":"file-in-1","completion_window":"24h",`+
		`"status":%q,"created_at":1,"output_file_id":%q,"request_counts":{"total":2,"completed":2,"failed":0}}`, status, out)
}
