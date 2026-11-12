// file: core/integrations/ai/openai/adapter_test.go
// version: 1.0.0
// guid: 7d3a9c52-1e86-4f07-b4a9-2c5e8d0f6b31
// last-edited: 2026-11-12

package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/secrets"
)

// seen is one request that reached the upstream.
type seen struct {
	Method, Path, RawQuery string
	Header                 http.Header
	Body                   []byte
}

func (s seen) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Body, &m); err != nil {
		t.Fatalf("%s %s: the request body is not JSON: %v\n%s", s.Method, s.Path, err, s.Body)
	}
	return m
}

// rig is one adapter instance wired through a real providerhttp gate to a
// loopback upstream: the adapter's SDK calls go over a real HTTP wire.
type rig struct {
	t    testing.TB
	kit  *coretest.ProviderHTTPKit
	mux  *http.ServeMux
	inst *instance

	mu     sync.Mutex
	reqs   []seen
	key    string
	keyErr error
}

// Secret is the instance's SecretReader.
func (r *rig) Secret(_ context.Context, field string) (secrets.Value, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if field != secretField {
		return secrets.Value{}, fmt.Errorf("the rig holds no secret %q", field)
	}
	if r.keyErr != nil {
		return secrets.Value{}, r.keyErr
	}
	return secrets.NewValue(r.key), nil
}

func (r *rig) setKey(k string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.key, r.keyErr = k, err
}

func (r *rig) requests() []seen {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seen(nil), r.reqs...)
}

// only returns the single request the upstream saw.
func (r *rig) only() seen {
	r.t.Helper()
	reqs := r.requests()
	if len(reqs) != 1 {
		r.t.Fatalf("the upstream saw %d request(s), want 1: %+v", len(reqs), reqs)
	}
	return reqs[0]
}

// on routes a "METHOD /path" pattern (ServeMux syntax) to h.
func (r *rig) on(pattern string, h http.HandlerFunc) { r.mux.HandleFunc(pattern, h) }

// settings are the adapter settings the rig builds by default.
func (r *rig) settings() map[string]any {
	return map[string]any{"url": r.kit.URL() + "/v1", secretField: "ref:arkham-key"}
}

// newRig builds an instance. edit may change the settings before the build.
func newRig(t testing.TB, edit ...func(map[string]any)) *rig {
	t.Helper()
	kit := coretest.ProviderHTTP(t)
	return newRigAt(t, kit, "http://"+kit.Host(), edit...)
}

// newRigAt builds an instance whose configured origin is origin and whose
// outbound goes through kit's gate: kit's own server for a recorded run, or a
// live server the kit's lan_allow admits.
func newRigAt(t testing.TB, kit *coretest.ProviderHTTPKit, origin string, edit ...func(map[string]any)) *rig {
	t.Helper()
	d, err := Descriptor()
	if err != nil {
		t.Fatalf("Descriptor: %v", err)
	}
	meta := d.Meta()
	r := &rig{t: t, kit: kit, mux: http.NewServeMux(), key: "sk-batarang-0001"}
	r.kit.Handle(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(body))
		r.mu.Lock()
		r.reqs = append(r.reqs, seen{Method: req.Method, Path: req.URL.Path, RawQuery: req.URL.RawQuery, Header: req.Header.Clone(), Body: body})
		r.mu.Unlock()
		r.mux.ServeHTTP(w, req)
	})
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"}
	h, g := r.kit.Registry.Outbound(integrations.OutboundSpec{
		Ref: ref, Adapter: AdapterID, Egress: meta.Egress, Limits: meta.Limits,
		ConfiguredOrigins: []string{origin},
	})
	env := integrations.Env{Ref: ref, Log: slog.New(slog.DiscardHandler), HTTP: h, Gate: g, Secrets: r, Now: r.kit.Clock.Now}
	settings := r.settings()
	for _, e := range edit {
		e(settings)
	}
	b, err := build(context.Background(), env, integrations.InstanceConfig{Ref: ref, Adapter: AdapterID, Enabled: true, Settings: settings})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	r.inst = b.(*instance)
	return r
}

func writeJSON(t testing.TB, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if s, ok := v.(string); ok {
		_, _ = io.WriteString(w, s)
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("rig: encode the answer: %v", err)
	}
}

// completion is a chat completion answer.
func completion(text string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "gpt-batmobile",
		"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
		"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 5, "total_tokens": 16,
			"prompt_tokens_details": map[string]any{"cached_tokens": 4}},
	}
}

func classOf(err error) integrations.Class { return integrations.ClassOf(context.Background(), err) }

func asIntegrationsError(t testing.TB, err error) *integrations.Error {
	t.Helper()
	e, ok := errors.AsType[*integrations.Error](err)
	if !ok {
		t.Fatalf("error %T (%v) is not an *integrations.Error", err, err)
	}
	return e
}

// ---- the descriptor and its schema ---------------------------------------------------

// The settings schema carries the routing properties the router needs and the
// adapter's own, and renders the locality as the string enum an operator
// writes (a uint8-kind locality rendered "integer 0..255" and refused "local").
func TestDescriptor_SchemaCarriesRoutingProps(t *testing.T) {
	t.Parallel()
	d, err := Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	m := d.Meta()
	if m.ID != AdapterID || m.Kind != integrations.KindAI {
		t.Fatalf("meta = %s / %s", m.ID, m.Kind)
	}
	if m.Limits.MaxRetries != -1 {
		t.Errorf("MaxRetries = %d, want -1: the router owns failover and the bench", m.Limits.MaxRetries)
	}
	schema, err := config.SchemaFor[Settings]()
	if err != nil {
		t.Fatal(err)
	}
	if p := ai.SettingsProblem(schema); p != "" {
		t.Fatalf("settings schema: %s", p)
	}
	if string(m.Settings.JSON()) != string(schema.JSON()) {
		t.Error("the descriptor does not carry the reflected settings schema (ai.Adapter dropped it)")
	}
	var doc struct {
		Properties map[string]struct {
			Type   any      `json:"type"`
			Enum   []string `json:"enum"`
			Format string   `json:"format"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema.JSON(), &doc); err != nil {
		t.Fatal(err)
	}
	loc, ok := doc.Properties["locality"]
	if !ok || loc.Type != "string" || strings.Join(loc.Enum, ",") != "auto,local,cloud" {
		t.Errorf("locality = %+v, want a string enum auto|local|cloud", loc)
	}
	if _, ok := doc.Properties["root_paths"]; !ok {
		t.Error("the shared-filesystem roots are not in the schema under root_paths")
	}
	if _, ok := doc.Properties["host_roots"]; ok {
		t.Error("host_roots is back: the Registry refuses a key that names a destination (SEC-15)")
	}
	if u := doc.Properties["url"]; u.Format != "uri" {
		t.Errorf("url = %+v, want format uri", u)
	}
	if _, ok := doc.Properties["api_key"]; !ok {
		t.Error("api_key is not in the schema")
	}
}

// The router, not the gate, owns an endpoint's cooldown (05-ai section 4 step
// 4): the adapter's hold policy must not outlast the router's first rung, or
// the gate refuses the very call the router just decided to try. The gate's own
// defaults (a 5xx for 30m, a rate limit for 15m, quota for 4h) did exactly that.
func TestDescriptor_HoldsYieldToTheRouter(t *testing.T) {
	t.Parallel()
	d, err := Descriptor()
	if err != nil {
		t.Fatal(err)
	}
	holds := d.Meta().Limits.Holds
	if holds.Max != ai.DefaultCooldownMax {
		t.Errorf("Holds.Max = %v, want the router's cooldown cap %v: it is the only clamp on a Retry-After", holds.Max, ai.DefaultCooldownMax)
	}
	for _, c := range []integrations.Class{integrations.ClassTransient, integrations.ClassRateLimited, integrations.ClassQuota} {
		if got := holds.Durations[c]; got <= 0 || got > ai.DefaultCooldownBase {
			t.Errorf("Holds.Durations[%s] = %v, want a positive duration no longer than the router's first rung %v", c, got, ai.DefaultCooldownBase)
		}
	}
	if got := holds.Durations[integrations.ClassAuth]; got <= 0 || got > ai.DefaultCooldownMax {
		t.Errorf("Holds.Durations[auth] = %v, want a positive duration within the router's cap %v", got, ai.DefaultCooldownMax)
	}
	if holds.Persist {
		t.Error("Holds.Persist = true: a hold that outlives a restart is the router's to own")
	}
}

// ---- base URL -------------------------------------------------------------------------

func TestBaseURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want, bad string }{
		{in: "http://127.0.0.1:11434/v1", want: "http://127.0.0.1:11434/v1/"},
		{in: "https://api.openai.com/v1/", want: "https://api.openai.com/v1/"},
		{in: "  https://gotham.example/v1  ", want: "https://gotham.example/v1/"},
		{in: "http://gotham.example", want: "http://gotham.example/"},
		{in: "", bad: "absolute"},
		{in: "gotham.example/v1", bad: "absolute"},
		{in: "ftp://gotham.example/v1", bad: "absolute"},
		{in: "//gotham.example/v1", bad: "absolute"},
		{in: "http:///v1", bad: "absolute"},
		{in: "https://joker:ha@gotham.example/v1", bad: "credentials"},
		{in: "https://joker@gotham.example/v1", bad: "credentials"},
		{in: "https://gotham.example/v1?api_key=SECRET", bad: "query"},
		{in: "https://gotham.example/v1?", bad: "query"},
		{in: "https://gotham.example/v1#batcave", bad: "query"},
	} {
		got, err := baseURL(tc.in)
		switch {
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("baseURL(%q) = %q, %v; want an error mentioning %q", tc.in, got, err, tc.bad)
		case tc.bad == "" && (err != nil || got != tc.want):
			t.Errorf("baseURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestBuild_RefusesABadURLAsInvalid(t *testing.T) {
	t.Parallel()
	ref := integrations.Ref{Kind: integrations.KindAI, Instance: "arkham"}
	_, err := build(context.Background(), integrations.Env{Ref: ref}, integrations.InstanceConfig{Ref: ref, Settings: map[string]any{"url": "https://joker:ha@gotham.example/v1"}})
	e := asIntegrationsError(t, err)
	if e.Class != integrations.ClassInvalid || e.Op != "build" || e.Ref != ref {
		t.Errorf("error = %+v", e)
	}
	if strings.Contains(err.Error(), "joker") || strings.Contains(err.Error(), "ha@") {
		t.Errorf("the error repeats the credentials the URL carried: %v", err)
	}
}

// ---- text -----------------------------------------------------------------------------

func TestGenerate_Wire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, completion("The Riddler")) })
	temp, seed := 0.25, int64(1939)
	got, err := r.inst.Generate(context.Background(), "gpt-batmobile", ai.TextRequest{
		System: "You are Alfred.",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Parts: []ai.Part{{Text: "Who "}, {Text: "left the riddle?"}}},
			{Role: ai.RoleAssistant, Parts: []ai.Part{{Text: "Not sure."}}},
			{Role: ai.RoleUser, Parts: []ai.Part{{Text: "Guess."}}},
		},
		MaxOutputTokens: 64, Temperature: &temp, Seed: &seed, Stop: []string{"END", "STOP"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "The Riddler" || got.FinishReason != "stop" || got.Model != "gpt-batmobile" {
		t.Errorf("response = %+v", got)
	}
	if got.Usage != (ai.Usage{Requests: 1, InputTokens: 11, CachedInputTokens: 4, OutputTokens: 5}) {
		t.Errorf("usage = %+v", got.Usage)
	}
	req := r.only()
	if req.Method != "POST" || req.Path != "/v1/chat/completions" {
		t.Fatalf("request = %s %s", req.Method, req.Path)
	}
	if a := req.Header.Get("Authorization"); a != "Bearer sk-batarang-0001" {
		t.Errorf("Authorization = %q", a)
	}
	body := req.json(t)
	if body["model"] != "gpt-batmobile" || body["max_completion_tokens"] != float64(64) || body["temperature"] != 0.25 || body["seed"] != float64(1939) {
		t.Errorf("body = %v", body)
	}
	if fmt.Sprint(body["stop"]) != "[END STOP]" {
		t.Errorf("stop = %v", body["stop"])
	}
	msgs := body["messages"].([]any)
	var roles, texts []string
	for _, m := range msgs {
		mm := m.(map[string]any)
		roles = append(roles, mm["role"].(string))
		texts = append(texts, mm["content"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" || texts[0] != "You are Alfred." || texts[1] != "Who left the riddle?" {
		t.Errorf("messages = %v %v", roles, texts)
	}
	if _, ok := body["response_format"]; ok {
		t.Error("a plain-text request asked for a response format")
	}
}

func TestGenerate_OutputFormats(t *testing.T) {
	t.Parallel()
	schema := json.RawMessage(`{"type":"object","properties":{"villain":{"type":"string"}},"required":["villain"]}`)
	for _, tc := range []struct {
		name string
		out  ai.OutputFormat
		want func(t *testing.T, rf map[string]any)
	}{
		{"json object", ai.OutputFormat{Kind: ai.OutputJSONObject}, func(t *testing.T, rf map[string]any) {
			if rf["type"] != "json_object" {
				t.Errorf("response_format = %v", rf)
			}
		}},
		{"json schema, strict", ai.OutputFormat{Kind: ai.OutputJSONSchema, Name: "villain", Schema: schema, Strict: true}, func(t *testing.T, rf map[string]any) {
			js, _ := rf["json_schema"].(map[string]any)
			if rf["type"] != "json_schema" || js["name"] != "villain" || js["strict"] != true {
				t.Fatalf("response_format = %v", rf)
			}
			if props := js["schema"].(map[string]any)["properties"].(map[string]any); props["villain"] == nil {
				t.Errorf("schema = %v", js["schema"])
			}
		}},
		{"json schema, not strict", ai.OutputFormat{Kind: ai.OutputJSONSchema, Name: "villain", Schema: schema}, func(t *testing.T, rf map[string]any) {
			if js, _ := rf["json_schema"].(map[string]any); js["strict"] != nil {
				t.Errorf("strict = %v, want it absent", js["strict"])
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, completion(`{"villain":"Joker"}`)) })
			if _, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}, Output: tc.out}); err != nil {
				t.Fatal(err)
			}
			rf, _ := r.only().json(t)["response_format"].(map[string]any)
			tc.want(t, rf)
		})
	}

	t.Run("a schema output without its schema is refused before any request", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Output: ai.OutputFormat{Kind: ai.OutputJSONSchema, Name: "x"}})
		if classOf(err) != integrations.ClassInvalid || r.kit.Hits() != 0 {
			t.Errorf("err = %v (class %v), hits = %d", err, classOf(err), r.kit.Hits())
		}
	})
}

func TestGenerate_ImagePartsGoOutAsDataURLs(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, completion("a bat")) })
	_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{
		{Text: "What is this?"}, {Image: &ai.Image{MIME: "image/png", Data: []byte("\x89PNGbatsignal")}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	parts := r.only().json(t)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "text" || parts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("content = %v", parts)
	}
	if u := parts[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(u, "data:image/png;base64,") {
		t.Errorf("image url = %q", u)
	}
	_, err = r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Image: &ai.Image{MIME: "image/png"}}}}}})
	if classOf(err) != integrations.ClassInvalid {
		t.Errorf("an image part without data = %v (class %v), want Invalid", err, classOf(err))
	}
}

// A 200 that is not a usable answer is ClassBadOutput, never a silent empty
// text: the router must not fail it over, and the caller must see it.
func TestGenerate_BadOutput(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]any{
		"no choices": map[string]any{"id": "x", "object": "chat.completion", "model": "m", "choices": []any{}},
		"refusal": map[string]any{"id": "x", "object": "chat.completion", "model": "m", "choices": []any{
			map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "", "refusal": "I cannot help the Joker"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, answer) })
			_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}})
			if classOf(err) != integrations.ClassBadOutput {
				t.Errorf("err = %v (class %v), want BadOutput", err, classOf(err))
			}
		})
	}
}

// ---- embeddings ---------------------------------------------------------------------------

func TestEmbed_Wire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/embeddings", func(w http.ResponseWriter, _ *http.Request) {
		// Out of order on purpose: the adapter places each vector by its index.
		writeJSON(t, w, 200, map[string]any{"object": "list", "model": "embed-bat", "usage": map[string]any{"prompt_tokens": 7, "total_tokens": 7},
			"data": []any{
				map[string]any{"object": "embedding", "index": 1, "embedding": []float64{0.5, 0.25}},
				map[string]any{"object": "embedding", "index": 0, "embedding": []float64{1, 2}},
			}})
	})
	got, err := r.inst.Embed(context.Background(), "embed-bat", ai.EmbedRequest{Inputs: []string{"first", "second"}, Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.Vectors) != "[[1 2] [0.5 0.25]]" || got.Model != "embed-bat" || got.Usage != (ai.Usage{Requests: 1, InputTokens: 7}) {
		t.Errorf("response = %+v", got)
	}
	req := r.only()
	body := req.json(t)
	if req.Path != "/v1/embeddings" || body["model"] != "embed-bat" || body["dimensions"] != float64(2) || fmt.Sprint(body["input"]) != "[first second]" {
		t.Errorf("request = %s %v", req.Path, body)
	}
}

func TestEmbed_BadOutput(t *testing.T) {
	t.Parallel()
	vec := func(i int) any { return map[string]any{"object": "embedding", "index": i, "embedding": []float64{1}} }
	for name, data := range map[string][]any{
		"fewer vectors than inputs": {vec(0)},
		"more vectors than inputs":  {vec(0), vec(1), vec(2)},
		"a duplicate index":         {vec(0), vec(0)},
		"an index out of range":     {vec(0), vec(5)},
		"a negative index":          {vec(0), vec(-1)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("POST /v1/embeddings", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, 200, map[string]any{"object": "list", "model": "m", "data": data, "usage": map[string]any{}})
			})
			_, err := r.inst.Embed(context.Background(), "m", ai.EmbedRequest{Inputs: []string{"a", "b"}})
			if classOf(err) != integrations.ClassBadOutput {
				t.Errorf("err = %v (class %v), want BadOutput: never padded, never truncated", err, classOf(err))
			}
		})
	}
}

// ---- transcription ----------------------------------------------------------------------------

func multipartOf(t testing.TB, s seen) *multipart.Form {
	t.Helper()
	req, err := http.NewRequest(s.Method, "http://x"+s.Path, bytes.NewReader(s.Body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = s.Header
	if err := req.ParseMultipartForm(32 << 20); err != nil {
		t.Fatalf("%s %s: not multipart: %v", s.Method, s.Path, err)
	}
	return req.MultipartForm
}

func TestTranscribe_Wire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/audio/transcriptions", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"task":"transcribe","language":"english","duration":12.5,"text":"Holy riddles, Batman.",
			"segments":[{"id":0,"start":0.0,"end":4.5,"text":"Holy riddles,"},{"id":1,"start":4.5,"end":12.5,"text":" Batman."}]}`)
	})
	got, err := r.inst.Transcribe(context.Background(), "whisper-bat", ai.TranscribeRequest{
		Audio: ai.AudioSource{Data: []byte("RIFFbatwave"), MIME: "audio/wav"}, Language: "en", Prompt: "Gotham names", Timestamps: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Holy riddles, Batman." || got.Language != "english" || got.Duration != 12500*time.Millisecond || len(got.Segments) != 2 ||
		got.Segments[1].Start != 4500*time.Millisecond || got.Segments[1].End != 12500*time.Millisecond || got.Segments[1].Text != " Batman." {
		t.Errorf("response = %+v", got)
	}
	if got.Usage != (ai.Usage{Requests: 1, AudioSeconds: 12.5}) {
		t.Errorf("usage = %+v", got.Usage)
	}
	form := multipartOf(t, r.only())
	v := form.Value
	if v["model"][0] != "whisper-bat" || v["response_format"][0] != "verbose_json" || v["language"][0] != "en" || v["prompt"][0] != "Gotham names" {
		t.Errorf("form = %v", v)
	}
	if g := v["timestamp_granularities[]"]; len(g) != 1 || g[0] != "segment" {
		t.Errorf("timestamp_granularities = %v", g)
	}
	fh := form.File["file"]
	if len(fh) != 1 || fh[0].Filename != "audio.wav" {
		t.Fatalf("file = %+v", fh)
	}
	f, _ := fh[0].Open()
	data, _ := io.ReadAll(f)
	_ = f.Close()
	if string(data) != "RIFFbatwave" {
		t.Errorf("uploaded %q", data)
	}
}

func TestTranscribe_DefaultsAndRefusals(t *testing.T) {
	t.Parallel()
	t.Run("the default model and an extension from the MIME type", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("POST /v1/audio/transcriptions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, `{"text":"x"}`) })
		if _, err := r.inst.Transcribe(context.Background(), "", ai.TranscribeRequest{Audio: ai.AudioSource{Data: []byte("m4a-bytes"), MIME: "audio/x-m4a"}}); err != nil {
			t.Fatal(err)
		}
		form := multipartOf(t, r.only())
		if form.Value["model"][0] != "whisper-1" || form.File["file"][0].Filename != "audio.m4a" {
			t.Errorf("model %v, file %s", form.Value["model"], form.File["file"][0].Filename)
		}
		if _, ok := form.Value["language"]; ok {
			t.Error("language sent although none was asked for")
		}
	})
	t.Run("a library path is unsupported and sends nothing", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Path: library.Path{Root: "batcave", Rel: "Wayne/01.mp3"}, MIME: "audio/mpeg"}})
		if classOf(err) != integrations.ClassUnsupported || r.kit.Hits() != 0 {
			t.Errorf("err = %v (class %v), hits %d", err, classOf(err), r.kit.Hits())
		}
	})
	t.Run("a transcript that is not JSON is bad output", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("POST /v1/audio/transcriptions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, `[1,2,3]`) })
		_, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: []byte("x"), MIME: "audio/mpeg"}})
		if classOf(err) != integrations.ClassBadOutput {
			t.Errorf("err = %v (class %v), want BadOutput", err, classOf(err))
		}
	})
	// A caller that does not know the audio's MIME type leaves it empty: the
	// upload must still go out with a valid content type (the SDK refuses to
	// encode one without), and the format the server reads is the extension.
	t.Run("an unknown or missing MIME type uploads as mp3 with a valid content type", func(t *testing.T) {
		t.Parallel()
		for _, mt := range []string{"", "   ", "audio/aiff", "not a mime type"} {
			r := newRig(t)
			r.on("POST /v1/audio/transcriptions", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, `{"text":"x"}`) })
			if _, err := r.inst.Transcribe(context.Background(), "m", ai.TranscribeRequest{Audio: ai.AudioSource{Data: []byte("bytes"), MIME: mt}}); err != nil {
				t.Fatalf("MIME %q: %v", mt, err)
			}
			fh := multipartOf(t, r.only()).File["file"][0]
			if fh.Filename != "audio.mp3" || fh.Header.Get("Content-Type") != "audio/mpeg" {
				t.Errorf("MIME %q: uploaded %q as %q", mt, fh.Filename, fh.Header.Get("Content-Type"))
			}
		}
	})
}

// ---- caps and health ---------------------------------------------------------------------------

func TestCapsAndHealth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	models := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "gpt-batmobile", "object": "model", "created": 1, "owned_by": "wayne"},
			map[string]any{"id": "embed-bat", "object": "model", "created": 1, "owned_by": "wayne"},
		}})
	}
	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/models", models)
		caps, err := r.inst.Caps(ctx)
		if err != nil || strings.Join(caps.Models, ",") != "gpt-batmobile,embed-bat" {
			t.Fatalf("Caps = %+v, %v", caps, err)
		}
		if h := r.inst.Health(ctx); h.Status != integrations.HealthOK {
			t.Errorf("Health = %+v", h)
		}
		if err := r.inst.Test(ctx); err != nil {
			t.Errorf("Test = %v", err)
		}
	})
	for _, tc := range []struct {
		name   string
		status int
		want   integrations.HealthStatus
	}{
		{"the key is rejected", 401, integrations.HealthAuthFailed},
		{"the server is down", 503, integrations.HealthDown},
		{"the server errors", 500, integrations.HealthDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(t, w, tc.status, `{"error":{"message":"no","type":"x","code":"y"}}`)
			})
			if h := r.inst.Health(ctx); h.Status != tc.want {
				t.Errorf("Health = %+v, want %s", h, tc.want)
			}
			if err := r.inst.Test(ctx); err == nil {
				t.Error("Test passed")
			}
		})
	}
	// switchable answers GET /v1/models with the status the test last set.
	switchable := func(status *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			if s := int(status.Load()); s != 200 {
				writeJSON(t, w, s, `{"error":{"message":"no","type":"x","code":"y"}}`)
				return
			}
			models(w, req)
		}
	}
	t.Run("a hold does not stop the probe, and a probe that succeeds ends the hold", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		var status atomic.Int32
		status.Store(503)
		r.on("GET /v1/models", switchable(&status))
		if h := r.inst.Health(ctx); h.Status != integrations.HealthDown {
			t.Fatalf("Health = %+v, want down", h)
		}
		held, ok := r.kit.Registry.HoldFor(r.inst.ref)
		if !ok {
			t.Fatal("a 503 did not put the instance on hold")
		}
		if d := held.Until.Sub(r.kit.Clock.Now()); d > ai.DefaultCooldownBase {
			t.Errorf("the hold runs %v, longer than the router's first cooldown rung (%v)", d, ai.DefaultCooldownBase)
		}
		// The router's capability refresh keeps the hold: it must not probe
		// through a stand-off the router itself set.
		hits := r.kit.Hits()
		if _, err := r.inst.Caps(ctx); classOf(err) != integrations.ClassRefusedLocal {
			t.Errorf("Caps during a hold = %v, want RefusedLocal", err)
		}
		if r.kit.Hits() != hits {
			t.Error("Caps sent a request through the hold")
		}
		// Health and Test are the ways a hold ends: they pass through it.
		if err := r.inst.Test(ctx); err == nil {
			t.Error("Test passed against a failing server")
		}
		if r.kit.Hits() != hits+1 {
			t.Errorf("Test sent %d request(s) during the hold, want 1", r.kit.Hits()-hits)
		}
		status.Store(200)
		if h := r.inst.Health(ctx); h.Status != integrations.HealthOK {
			t.Fatalf("Health after the server recovered = %+v, want ok", h)
		}
		if h, ok := r.kit.Registry.HoldFor(r.inst.ref); ok {
			t.Errorf("a successful probe left the hold in force: %+v", h)
		}
	})
	t.Run("an open breaker is degraded, not misconfigured", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		var status atomic.Int32
		status.Store(503)
		r.on("GET /v1/models", switchable(&status))
		for range 5 { // the breaker's default threshold of consecutive failures
			if h := r.inst.Health(ctx); h.Status != integrations.HealthDown {
				t.Fatalf("Health = %+v, want down while the breaker is closed", h)
			}
		}
		h := r.inst.Health(ctx)
		if h.Status != integrations.HealthDegraded || !strings.Contains(h.Message, "breaker") {
			t.Errorf("Health with the breaker open = %+v, want degraded naming the breaker", h)
		}
		if r.kit.Hits() != 5 {
			t.Errorf("%d request(s) reached the server, want 5: the open breaker sends nothing", r.kit.Hits())
		}
		r.kit.Clock.Advance(time.Minute) // past the breaker's cooldown: the probe goes out again
		status.Store(200)
		if h := r.inst.Health(ctx); h.Status != integrations.HealthOK {
			t.Errorf("Health after the cooldown = %+v, want ok", h)
		}
	})
	t.Run("an egress refusal is misconfigured and says so", func(t *testing.T) {
		t.Parallel()
		// A loopback server other than the rig's own is a private destination:
		// the gate refuses it without Local, and nothing is dialed.
		r := newRig(t, func(m map[string]any) { m["url"] = "http://127.0.0.1:1/v1" })
		h := r.inst.Health(ctx)
		if h.Status != integrations.HealthMisconfigured || !strings.Contains(h.Message, "egress guard") {
			t.Errorf("Health = %+v, want misconfigured naming the egress guard", h)
		}
		if r.kit.Hits() != 0 {
			t.Errorf("%d request(s) reached the server", r.kit.Hits())
		}
	})
	t.Run("a key that cannot be read is ours, not the server's rejection", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.setKey("", errors.New("the secret store is locked"))
		h := r.inst.Health(ctx)
		if h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonSecretMissing {
			t.Errorf("Health = %+v, want misconfigured (%s): the server was never asked", h, integrations.ReasonSecretMissing)
		}
		if r.kit.Hits() != 0 {
			t.Errorf("%d request(s) went out without the key", r.kit.Hits())
		}
	})
	t.Run("a withheld operator secret is misconfigured, and nothing is sent", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.setKey("", integrations.ErrOperatorSecretWithheld)
		if h := r.inst.Health(ctx); h.Status != integrations.HealthMisconfigured {
			t.Errorf("Health = %+v", h)
		}
		if r.kit.Hits() != 0 {
			t.Errorf("%d request(s) went out without the key", r.kit.Hits())
		}
	})
}

// ---- the key --------------------------------------------------------------------------------------

// The key is read at each request, so a rotation reaches the next call without
// a rebuild; a key that cannot be read stops the request before it is sent,
// never an unauthenticated request to a server that expects one.
func TestKeyMiddleware_RotationAndFailClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := newRig(t)
	r.on("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, `{"object":"list","data":[]}`) })

	for _, k := range []string{"sk-batarang-0001", "sk-batarang-0002"} {
		r.setKey(k, nil)
		if _, err := r.inst.Caps(ctx); err != nil {
			t.Fatal(err)
		}
	}
	reqs := r.requests()
	if len(reqs) != 2 || reqs[0].Header.Get("Authorization") != "Bearer sk-batarang-0001" || reqs[1].Header.Get("Authorization") != "Bearer sk-batarang-0002" {
		t.Fatalf("requests = %+v: the second call must carry the rotated key", reqs)
	}

	for _, tc := range []struct {
		name string
		err  error
		want integrations.Class
	}{
		{"unset or unreadable", errors.New("secret batarang-key has no value for the REAL-VALUE-123 field"), integrations.ClassAuth},
		{"withheld operator secret", integrations.ErrOperatorSecretWithheld, integrations.ClassRefusedLocal},
	} {
		before := r.kit.Hits()
		r.setKey("", tc.err)
		_, err := r.inst.Caps(ctx)
		if got := classOf(err); got != tc.want {
			t.Errorf("%s: class = %v, want %v (err %v)", tc.name, got, tc.want, err)
		}
		if r.kit.Hits() != before {
			t.Errorf("%s: a request was sent without the key", tc.name)
		}
		if strings.Contains(err.Error(), "REAL-VALUE-123") {
			t.Errorf("%s: the secret reader's text reached the error: %v", tc.name, err)
		}
	}

	t.Run("an instance with no key sends no Authorization header", func(t *testing.T) {
		t.Parallel()
		r := newRig(t, func(s map[string]any) { delete(s, secretField) })
		r.on("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, `{"object":"list","data":[]}`) })
		if _, err := r.inst.Caps(ctx); err != nil {
			t.Fatal(err)
		}
		if a := r.only().Header.Get("Authorization"); a != "" {
			t.Errorf("Authorization = %q on a keyless instance", a)
		}
	})
}

// ---- errors -----------------------------------------------------------------------------------------

func apiError(typ, code, msg string) string {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": typ, "param": nil, "code": code}})
	return string(b)
}

// A provider answer is classified by its status and by the type and code of
// its body: a 429 with a quota marker in EITHER field is Quota, a plain 429
// is RateLimited. HEAD's classifier read one field and misfiled the captured
// production payload, which sets the two to different values.
func TestErrors_Classification(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   integrations.Class
		retry  time.Duration
		header string
	}{
		{"quota marker in type", 429, apiError("insufficient_quota", "other", "You exceeded your current quota"), integrations.ClassQuota, 0, ""},
		{"quota marker in code", 429, apiError("other", "insufficient_quota", "quota"), integrations.ClassQuota, 0, ""},
		{"the other marker, different type and code", 429, apiError("billing", "credit_balance_exhausted", "credits"), integrations.ClassQuota, 0, ""},
		{"plain rate limit with Retry-After", 429, apiError("requests", "rate_limit_exceeded", "slow down"), integrations.ClassRateLimited, 7 * time.Second, "7"},
		{"429 with no JSON body", 429, "slow down", integrations.ClassRateLimited, 0, ""},
		{"unauthorized", 401, apiError("invalid_request_error", "invalid_api_key", "bad key"), integrations.ClassAuth, 0, ""},
		{"forbidden", 403, apiError("x", "y", "no"), integrations.ClassAuth, 0, ""},
		{"no such model", 404, apiError("invalid_request_error", "model_not_found", "no model"), integrations.ClassNotFound, 0, ""},
		{"bad request", 400, apiError("invalid_request_error", "", "bad"), integrations.ClassInvalid, 0, ""},
		{"server error", 500, apiError("server_error", "", "oops"), integrations.ClassTransient, 0, ""},
		{"bad gateway with an HTML body", 502, "<html>bad gateway</html>", integrations.ClassTransient, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				writeJSON(t, w, tc.status, tc.body)
			})
			_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}})
			e := asIntegrationsError(t, err)
			if e.Class != tc.want || e.Status != tc.status || e.RetryAfter != tc.retry || e.Op != "generate" || e.Ref.Instance != "arkham" {
				t.Errorf("error = class %v status %d retry %v op %q ref %v; want class %v status %d retry %v", e.Class, e.Status, e.RetryAfter, e.Op, e.Ref, tc.want, tc.status, tc.retry)
			}
			if n := r.kit.Hits(); n != 1 {
				t.Errorf("%d request(s) for one call: nothing below the router may retry", n)
			}
		})
	}

	t.Run("the detail is bounded and one line, and the text never repeats the body or the key", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		long := "Gotham\nline two\t" + strings.Repeat("x", 4*integrations.MaxDetail)
		r.on("POST /v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 500, apiError("server_error", "", long+" sk-batarang-0001"))
		})
		_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}})
		e := asIntegrationsError(t, err)
		if len(e.Detail) == 0 || len(e.Detail) > integrations.MaxDetail || strings.ContainsAny(e.Detail, "\n\t\r") {
			t.Errorf("detail = %d bytes %q", len(e.Detail), e.Detail)
		}
		if strings.Contains(err.Error(), "line two") || strings.Contains(err.Error(), "sk-batarang") {
			t.Errorf("the error text repeats the provider's body or the key: %v", err)
		}
	})

	t.Run("a refused connection is a transient failure whose text carries no URL", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.kit.Server.Close()
		_, err := r.inst.Generate(context.Background(), "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}})
		// The dial error names the address it could not reach, which is the
		// operator's own diagnostic; the URL (scheme, path, query) is what must
		// not appear, because a URL is where a credential would hide.
		if err == nil || strings.Contains(err.Error(), "http://") || strings.Contains(err.Error(), "/v1") {
			t.Errorf("err = %v", err)
		}
		if c := classOf(err); c != integrations.ClassTransient {
			t.Errorf("class = %v, want Transient", c)
		}
	})

	t.Run("a canceled caller is Canceled, not a remote failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.inst.Generate(ctx, "m", ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "x"}}}}})
		if c := integrations.ClassOf(ctx, err); c != integrations.ClassCanceled {
			t.Errorf("class = %v, want Canceled (err %v)", c, err)
		}
	})
}

// ---- batch -------------------------------------------------------------------------------------------

const batchJSON = `{"id":"batch_7","object":"batch","endpoint":"/v1/chat/completions","input_file_id":"file-in-1","completion_window":"24h",
"status":"%s","created_at":1,"output_file_id":%q,"error_file_id":%q,"request_counts":{"total":3,"completed":2,"failed":1},"metadata":{"job":"riddler-001"}}`

func batchAnswer(status, out, errFile string) string {
	return fmt.Sprintf(batchJSON, status, out, errFile)
}

func TestSubmitBatch_Wire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/files", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, `{"id":"file-in-1","object":"file","bytes":1,"created_at":1,"filename":"batch.jsonl","purpose":"batch"}`)
	})
	r.on("POST /v1/batches", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, batchAnswer("validating", "", "")) })
	h, err := r.inst.SubmitBatch(context.Background(), "gpt-batmobile", []ai.BatchItem{
		{CustomID: "q1", Request: ai.TextRequest{System: "Alfred", Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "one"}}}}, MaxOutputTokens: 9}},
		{CustomID: "q2", Request: ai.TextRequest{Messages: []ai.Message{{Role: ai.RoleUser, Parts: []ai.Part{{Text: "two"}}}}}},
	}, map[string]string{"job": "riddler-001", "capability": "gotham.riddle_batch"})
	if err != nil || h != "batch_7" {
		t.Fatalf("SubmitBatch = %q, %v", h, err)
	}
	reqs := r.requests()
	if len(reqs) != 2 || reqs[0].Path != "/v1/files" || reqs[1].Path != "/v1/batches" {
		t.Fatalf("requests = %+v, want the upload and then the create", reqs)
	}
	form := multipartOf(t, reqs[0])
	if form.Value["purpose"][0] != "batch" || form.File["file"][0].Filename != "batch.jsonl" {
		t.Fatalf("upload form = %v %v", form.Value, form.File["file"][0].Filename)
	}
	f, _ := form.File["file"][0].Open()
	raw, _ := io.ReadAll(f)
	_ = f.Close()
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("input file = %q", raw)
	}
	var first struct {
		CustomID string         `json:"custom_id"`
		Method   string         `json:"method"`
		URL      string         `json:"url"`
		Body     map[string]any `json:"body"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.CustomID != "q1" || first.Method != "POST" || first.URL != "/v1/chat/completions" || first.Body["model"] != "gpt-batmobile" || first.Body["max_completion_tokens"] != float64(9) {
		t.Errorf("first line = %+v", first)
	}
	body := reqs[1].json(t)
	md, _ := body["metadata"].(map[string]any)
	if body["input_file_id"] != "file-in-1" || body["endpoint"] != "/v1/chat/completions" || body["completion_window"] != "24h" || md["job"] != "riddler-001" || md["capability"] != "gotham.riddle_batch" {
		t.Errorf("create body = %v", body)
	}
}

// A batch the provider did NOT create takes its uploaded input file with it:
// the provider's file store is not a place to leave a job's prompts behind. A
// batch it MAY have created keeps its input: after a 5xx, a cut connection or a
// 2xx the adapter could not read, the batch can exist and be reading that very
// file, and the router leaves the job Pending for its tag reconcile to settle.
//
// What proves "not created" is a refusal (a 4xx), a request the gate never
// sent, or an answer that was not the API's at all (a web page). The cleanup
// passes through an active hold, so the hold that a quota refusal just set does
// not refuse the delete that tidies up after it.
func TestSubmitBatch_CreateFailureDeletesTheUpload(t *testing.T) {
	t.Parallel()
	items := []ai.BatchItem{{CustomID: "q", Request: ai.TextRequest{System: "x"}}}
	const html = `<html><body>Sign in to the Wayne Enterprises network</body></html>`
	rigFor := func(t *testing.T, create http.HandlerFunc) *rig {
		r := newRig(t)
		r.on("POST /v1/files", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, `{"id":"file-in-9","object":"file","bytes":1,"created_at":1,"filename":"batch.jsonl","purpose":"batch"}`)
		})
		r.on("POST /v1/batches", create)
		r.on("DELETE /v1/files/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, `{"id":"file-in-9","object":"file","deleted":true}`)
		})
		return r
	}
	answer := func(status int, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, status, body) }
	}

	rows := []struct {
		name       string
		create     http.HandlerFunc
		wantClass  integrations.Class
		wantDelete bool
	}{
		{"a rejected create (4xx)", answer(400, apiError("invalid_request_error", "", "bad batch")), integrations.ClassInvalid, true},
		{"a rate limit (429)", answer(429, apiError("rate_limit_error", "rate_limit_exceeded", "slow down")), integrations.ClassRateLimited, true},
		{"a quota refusal, through the hold it set", answer(429, apiError("x", "insufficient_quota", "no credit")), integrations.ClassQuota, true},
		{"a reply that is not the API's (200 text/html)", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, html)
		}, integrations.ClassBadOutput, true},
		{"a 5xx", answer(503, apiError("server_error", "", "overloaded")), integrations.ClassTransient, false},
		{"a 2xx cut off mid-body", answer(200, `{"id":"batch_1",`), integrations.ClassTransient, false},
		{"a 2xx with an empty body", answer(200, ``), integrations.ClassBadOutput, false},
		{"a 2xx without a batch ID", answer(200, `{"object":"batch"}`), integrations.ClassBadOutput, false},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := rigFor(t, tc.create)
			handle, err := r.inst.SubmitBatch(context.Background(), "m", items, nil)
			if handle != "" {
				t.Errorf("handle = %q, want none", handle)
			}
			if e := asIntegrationsError(t, err); e.Class != tc.wantClass || e.Op != "batch create" {
				t.Errorf("error = %+v, want class %s from batch create", e, tc.wantClass)
			}
			want := []string{"POST /v1/files", "POST /v1/batches"}
			if tc.wantDelete {
				want = append(want, "DELETE /v1/files/file-in-9")
			}
			if got := paths(r.requests()); strings.Join(got, "; ") != strings.Join(want, "; ") {
				t.Errorf("requests = %v, want %v (the input file must be deleted: %v)", got, want, tc.wantDelete)
			}
		})
	}

	t.Run("a create the gate refused before sending", func(t *testing.T) {
		t.Parallel()
		// The upload succeeds, and a failure of another call puts the instance
		// on hold before the create is sent: the create is refused locally,
		// nothing of it was sent, and the delete still reaches the provider.
		r := newRig(t)
		r.on("POST /v1/files", func(w http.ResponseWriter, _ *http.Request) {
			// The gate releases, on a success, only a hold that was set before
			// the succeeding attempt started; the rig's clock stands still, so
			// move it for the hold to count as set during the upload.
			r.kit.Clock.Advance(time.Millisecond)
			if _, err := r.inst.Caps(context.Background()); err == nil {
				t.Error("the models request should have failed and set a hold")
			}
			writeJSON(t, w, 200, `{"id":"file-in-9","object":"file","bytes":1,"created_at":1,"filename":"batch.jsonl","purpose":"batch"}`)
		})
		r.on("GET /v1/models", answer(503, apiError("server_error", "", "overloaded")))
		r.on("POST /v1/batches", answer(200, `{"id":"batch_1"}`))
		r.on("DELETE /v1/files/{id}", answer(200, `{"id":"file-in-9","object":"file","deleted":true}`))
		_, err := r.inst.SubmitBatch(context.Background(), "m", items, nil)
		if e := asIntegrationsError(t, err); e.Class != integrations.ClassRefusedLocal {
			t.Fatalf("error = %+v, want the gate's RefusedLocal", e)
		}
		if reason, _ := providerhttp.ReasonOf(err); reason != providerhttp.ReasonHold {
			t.Fatalf("refused for %q, want the hold", reason)
		}
		want := "POST /v1/files; GET /v1/models; DELETE /v1/files/file-in-9"
		if got := strings.Join(paths(r.requests()), "; "); got != want {
			t.Errorf("requests = %s, want %s: the create was never sent and the input must go", got, want)
		}
	})

	t.Run("a create the caller gave up on keeps the upload", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r := rigFor(t, func(_ http.ResponseWriter, req *http.Request) {
			cancel()
			<-req.Context().Done() // the client has gone: the provider may still create it
		})
		_, err := r.inst.SubmitBatch(ctx, "m", items, nil)
		if e := asIntegrationsError(t, err); e.Class != integrations.ClassCanceled {
			t.Errorf("error = %+v, want Canceled", e)
		}
		if got := strings.Join(paths(r.requests()), "; "); got != "POST /v1/files; POST /v1/batches" {
			t.Errorf("requests = %s: a canceled create is ambiguous and must not delete the input", got)
		}
	})

	t.Run("an upload failure creates no batch", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("POST /v1/files", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 500, apiError("server_error", "", "disk"))
		})
		_, err := r.inst.SubmitBatch(context.Background(), "m", items, nil)
		if e := asIntegrationsError(t, err); e.Op != "batch upload" || e.Class != integrations.ClassTransient {
			t.Errorf("error = %+v", e)
		}
		if n := len(r.requests()); n != 1 {
			t.Errorf("%d request(s), want only the failed upload", n)
		}
	})
}

func paths(reqs []seen) []string {
	out := make([]string, len(reqs))
	for i, q := range reqs {
		out[i] = q.Method + " " + q.Path
	}
	return out
}

func TestBatchStatus_PhaseMapping(t *testing.T) {
	t.Parallel()
	for status, want := range map[string]ai.BatchPhase{
		"validating": ai.BatchInProgress, "in_progress": ai.BatchInProgress, "finalizing": ai.BatchInProgress, "cancelling": ai.BatchInProgress,
		"completed": ai.BatchCompleted, "failed": ai.BatchFailed, "expired": ai.BatchExpired, "cancelled": ai.BatchCanceled,
	} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, batchAnswer(status, "", "")) })
			got, err := r.inst.BatchStatus(context.Background(), "batch_7")
			if err != nil || got != (ai.BatchState{Phase: want, Total: 3, Completed: 2, Failed: 1}) {
				t.Errorf("BatchStatus = %+v, %v; want phase %v with 3/2/1", got, err, want)
			}
			if p := r.only().Path; p != "/v1/batches/batch_7" {
				t.Errorf("path = %q", p)
			}
		})
	}
}

func TestBatchResults_Wire(t *testing.T) {
	t.Parallel()
	okRow := `{"id":"r1","custom_id":"q1","response":{"status_code":200,"request_id":"x","body":` +
		`{"id":"c","object":"chat.completion","model":"gpt-batmobile","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Joker"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}},"error":null}`
	badStatus := `{"id":"r2","custom_id":"q2","response":{"status_code":400,"request_id":"x","body":{"error":{"message":"bad prompt"}}},"error":null}`
	errRow := `{"id":"r3","custom_id":"q3","response":null,"error":{"code":"batch_expired","message":"too slow"}}`

	r := newRig(t)
	r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 200, batchAnswer("completed", "file-out-1", "file-err-1"))
	})
	r.on("GET /v1/files/{id}/content", func(w http.ResponseWriter, req *http.Request) {
		switch req.PathValue("id") {
		case "file-out-1":
			_, _ = io.WriteString(w, okRow+"\n"+badStatus+"\n")
		case "file-err-1":
			_, _ = io.WriteString(w, errRow+"\n")
		default:
			http.NotFound(w, req)
		}
	})
	seq, err := r.inst.BatchResults(context.Background(), "batch_7")
	if err != nil {
		t.Fatal(err)
	}
	var got []ai.BatchResult
	for res, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, res)
	}
	if len(got) != 3 || got[0].CustomID != "q1" || got[1].CustomID != "q2" || got[2].CustomID != "q3" {
		t.Fatalf("results = %+v, want the output file's rows then the error file's", got)
	}
	if got[0].Err != "" || got[0].Text == nil || got[0].Text.Text != "Joker" || got[0].Text.Usage.InputTokens != 3 || got[0].Text.Model != "gpt-batmobile" {
		t.Errorf("row 1 = %+v", got[0])
	}
	if !strings.HasPrefix(got[1].Err, "status 400:") || got[1].Text != nil {
		t.Errorf("row 2 = %+v", got[1])
	}
	if got[2].Err != "batch_expired: too slow" || got[2].Text != nil {
		t.Errorf("row 3 = %+v", got[2])
	}

	t.Run("a row's error is one bounded line", func(t *testing.T) {
		t.Parallel()
		noisy := `{"id":"r4","custom_id":"q4","response":null,"error":{"code":"bad\ncode","message":"` +
			strings.Repeat(`Gotham\u0007\n`, 2*integrations.MaxDetail) + `"}}`
		r := newRig(t)
		r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, batchAnswer("completed", "", "file-err-1"))
		})
		r.on("GET /v1/files/{id}/content", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, noisy+"\n") })
		seq, err := r.inst.BatchResults(context.Background(), "batch_7")
		if err != nil {
			t.Fatal(err)
		}
		var rows []ai.BatchResult
		for res, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, res)
		}
		if len(rows) != 1 || rows[0].Err == "" {
			t.Fatalf("rows = %+v, want the one error row", rows)
		}
		if len(rows[0].Err) > integrations.MaxDetail {
			t.Errorf("Err is %d bytes, want at most %d", len(rows[0].Err), integrations.MaxDetail)
		}
		if strings.ContainsFunc(rows[0].Err, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			t.Errorf("Err carries a control character: %q", rows[0].Err[:40])
		}
	})
	t.Run("a batch that is not completed has no results", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, batchAnswer("in_progress", "", "")) })
		if _, err := r.inst.BatchResults(context.Background(), "batch_7"); classOf(err) != integrations.ClassBadOutput {
			t.Errorf("err = %v (class %v), want BadOutput", err, classOf(err))
		}
	})
	t.Run("an unreadable output file fails at the call, not midway", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, batchAnswer("completed", "file-out-1", ""))
		})
		r.on("GET /v1/files/{id}/content", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 404, apiError("x", "file_not_found", "gone"))
		})
		if _, err := r.inst.BatchResults(context.Background(), "batch_7"); classOf(err) != integrations.ClassNotFound {
			t.Errorf("err = %v (class %v), want NotFound", err, classOf(err))
		}
	})
	t.Run("a line that is not JSON ends the sequence with bad output", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/batches/{id}", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, batchAnswer("completed", "file-out-1", ""))
		})
		r.on("GET /v1/files/{id}/content", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, okRow+"\nnot json\n") })
		seq, err := r.inst.BatchResults(context.Background(), "batch_7")
		if err != nil {
			t.Fatal(err)
		}
		var rows int
		var last error
		for _, err := range seq {
			if err != nil {
				last = err
				break
			}
			rows++
		}
		if rows != 1 || classOf(last) != integrations.ClassBadOutput {
			t.Errorf("rows = %d, last = %v (class %v)", rows, last, classOf(last))
		}
	})
}

func TestCancelBatch_Wire(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.on("POST /v1/batches/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, batchAnswer("cancelling", "", "")) })
	if err := r.inst.CancelBatch(context.Background(), "batch_7"); err != nil {
		t.Fatal(err)
	}
	if q := r.only(); q.Method != "POST" || q.Path != "/v1/batches/batch_7/cancel" {
		t.Errorf("request = %s %s", q.Method, q.Path)
	}
	r2 := newRig(t)
	r2.on("POST /v1/batches/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, 404, apiError("x", "batch_not_found", "gone"))
	})
	if err := r2.inst.CancelBatch(context.Background(), "batch_x"); classOf(err) != integrations.ClassNotFound || asIntegrationsError(t, err).Op != "batch cancel" {
		t.Errorf("err = %v", err)
	}

	// A cancel stops spend, so a hold set by an earlier rate limit or quota
	// refusal must not keep a paid batch running.
	t.Run("a hold does not stop a cancel", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 429, apiError("x", "insufficient_quota", "no credit"))
		})
		r.on("POST /v1/batches/{id}/cancel", func(w http.ResponseWriter, _ *http.Request) { writeJSON(t, w, 200, batchAnswer("cancelling", "", "")) })
		if _, err := r.inst.Caps(context.Background()); classOf(err) != integrations.ClassQuota {
			t.Fatalf("Caps = %v, want the quota refusal that sets the hold", err)
		}
		if _, ok := r.kit.Registry.HoldFor(r.inst.ref); !ok {
			t.Fatal("the quota refusal did not put the instance on hold")
		}
		if err := r.inst.CancelBatch(context.Background(), "batch_7"); err != nil {
			t.Fatalf("CancelBatch during a hold = %v, want it sent", err)
		}
		if got := strings.Join(paths(r.requests()), "; "); got != "GET /v1/models; POST /v1/batches/batch_7/cancel" {
			t.Errorf("requests = %s", got)
		}
	})
}

// batchListing answers GET /v1/batches pages of n rows each, with the job tag
// on every row whose index is in tagged.
func batchListing(t testing.TB, pages, perPage int, tagged map[int]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		page := 0
		if after := req.URL.Query().Get("after"); after != "" {
			if _, err := fmt.Sscanf(after, "batch_p%d_", &page); err != nil {
				t.Errorf("the next page's cursor %q is not one this listing issued", after)
			}
			page++
		}
		var data []any
		for i := range perPage {
			md := map[string]string{}
			if tagged[page*perPage+i] {
				md["job"] = "riddler-001"
			}
			data = append(data, map[string]any{"id": fmt.Sprintf("batch_p%d_%d", page, i), "object": "batch", "endpoint": "/v1/chat/completions",
				"input_file_id": "f", "completion_window": "24h", "status": "completed", "created_at": 1, "metadata": md,
				"request_counts": map[string]any{"total": 1, "completed": 1, "failed": 0}})
		}
		writeJSON(t, w, 200, map[string]any{"object": "list", "data": data, "has_more": page+1 < pages,
			"first_id": fmt.Sprintf("batch_p%d_0", page), "last_id": fmt.Sprintf("batch_p%d_%d", page, perPage-1)})
	}
}

func TestFindBatches_Wire(t *testing.T) {
	t.Parallel()
	t.Run("walks every page and matches on every tag", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		// Rows 1 (page 0) and 4 (page 1) carry the tag; page 2 has none.
		r.on("GET /v1/batches", batchListing(t, 3, 3, map[int]bool{1: true, 4: true}))
		got, err := r.inst.FindBatches(context.Background(), map[string]string{"job": "riddler-001"})
		if err != nil || fmt.Sprint(got) != "[batch_p0_1 batch_p1_1]" {
			t.Fatalf("FindBatches = %v, %v", got, err)
		}
		if n := len(r.requests()); n != 3 {
			t.Errorf("%d list requests, want 3 pages", n)
		}
		if q := r.requests()[0].RawQuery; !strings.Contains(q, "limit=100") {
			t.Errorf("first page query = %q", q)
		}
		none, err := r.inst.FindBatches(context.Background(), map[string]string{"job": "riddler-001", "capability": "other"})
		if err != nil || len(none) != 0 {
			t.Errorf("a batch missing one of the tags matched: %v, %v", none, err)
		}
	})
	t.Run("a listing cut off at the page cap is an error, never a negative", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/batches", batchListing(t, maxBatchListPages()+5, 1, nil))
		got, err := r.inst.FindBatches(context.Background(), map[string]string{"job": "riddler-001"})
		if classOf(err) != integrations.ClassBadOutput {
			t.Errorf("err = %v (class %v), want BadOutput: absence from a truncated listing proves nothing", err, classOf(err))
		}
		if len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("an empty project", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.on("GET /v1/batches", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(t, w, 200, `{"object":"list","data":[],"has_more":false}`)
		})
		got, err := r.inst.FindBatches(context.Background(), map[string]string{"job": "x"})
		if err != nil || len(got) != 0 {
			t.Errorf("FindBatches = %v, %v", got, err)
		}
	})
}
