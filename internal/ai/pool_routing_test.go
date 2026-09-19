// file: internal/ai/pool_routing_test.go
// version: 1.0.0
// guid: bc526593-a37c-4d58-a9b8-2ebb97aeef5f
// last-edited: 2026-09-19

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
)

// fakeOpenAI is an OpenAI-compatible server: /v1/chat/completions answers a
// filename-parse batch with one result per numbered filename line (or with
// `short` fewer), /v1/embeddings answers one vector per input.
type fakeOpenAI struct {
	srv    *httptest.Server
	chats  atomic.Int64
	embeds atomic.Int64
	short  int
	// rawReply, when set, is returned verbatim as the chat reply content.
	rawReply string
	// block, when non-nil, holds every chat request until it is closed.
	block   chan struct{}
	entered chan struct{}

	mu     sync.Mutex
	models []string
}

var numberedLine = regexp.MustCompile(`(?m)^\d+\. `)

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) url() string { return f.srv.URL + "/v1" }

func (f *fakeOpenAI) seenModels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

func (f *fakeOpenAI) handle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Input []string `json:"input"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.models = append(f.models, body.Model)
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	switch {
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		f.chats.Add(1)
		if f.block != nil {
			if f.entered != nil {
				f.entered <- struct{}{}
			}
			<-f.block
		}
		n := 0
		for _, m := range body.Messages {
			if m.Role == "user" {
				n = len(numberedLine.FindAllString(m.Content, -1))
			}
		}
		n -= f.short
		items := make([]string, 0, n)
		for i := range n {
			items = append(items, fmt.Sprintf(`{"title":"t%d","author":"a%d","confidence":"high"}`, i, i))
		}
		reply := `{"results":[` + strings.Join(items, ",") + `]}`
		if f.rawReply != "" {
			reply = f.rawReply
		}
		content, _ := json.Marshal(reply)
		fmt.Fprintf(w, `{"id":"x","object":"chat.completion","created":0,"model":%q,"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":%s}}]}`,
			body.Model, content)
	case strings.HasSuffix(r.URL.Path, "/embeddings"):
		f.embeds.Add(1)
		data := make([]string, 0, len(body.Input))
		for i := range body.Input {
			data = append(data, fmt.Sprintf(`{"object":"embedding","index":%d,"embedding":[0.5,0.25]}`, i))
		}
		fmt.Fprintf(w, `{"object":"list","model":%q,"data":[%s],"usage":{"prompt_tokens":1,"total_tokens":1}}`,
			body.Model, strings.Join(data, ","))
	default:
		http.NotFound(w, r)
	}
}

// deadURL is an address that refuses connections: a server started and
// closed.
func deadURL(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL + "/v1"
	s.Close()
	return u
}

type testPool struct {
	*PoolSource
	health *aidispatch.Health
	attr   *aidispatch.Attribution
	active atomic.Bool
}

func newTestPool(eps ...aidispatch.Endpoint) *testPool {
	tp := &testPool{health: aidispatch.NewHealth(), attr: aidispatch.NewAttribution()}
	tp.active.Store(true)
	tp.PoolSource = &PoolSource{
		Active:    tp.active.Load,
		Endpoints: func() []aidispatch.Endpoint { return eps },
		Secret:    func(string) string { return "" },
		Options: []aidispatch.Option{
			aidispatch.WithSlots(aidispatch.NewSlots()),
			aidispatch.WithHealth(tp.health),
			aidispatch.WithAttribution(tp.attr),
		},
	}
	return tp
}

func ep(id, url string, prio int, caps ...string) aidispatch.Endpoint {
	return aidispatch.Endpoint{ID: id, Protocol: aidispatch.ProtocolOpenAICompat, URL: url,
		ChatModel: "chat-" + id, EmbedModel: "bge-m3", Priority: prio, Enabled: true, Capabilities: caps}
}

var (
	fp = aidispatch.LLMFilenameParse.ID()
	et = aidispatch.EmbedText.ID()
)

// An endpoint that does not tick llm.filename_parse is never selected, even
// when it is enabled, healthy and preferred by priority.
func TestRoutedParse_CapabilityGating(t *testing.T) {
	unticked, ticked := newFakeOpenAI(t), newFakeOpenAI(t)
	disabled := newFakeOpenAI(t)
	off := ep("off", disabled.url(), 0, fp)
	off.Enabled = false
	pool := newTestPool(ep("embed-only", unticked.url(), 1, et), off, ep("parser", ticked.url(), 9, fp))

	res, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"A - B.m4b", "C - D.m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results for 2 inputs", len(res))
	}
	if unticked.chats.Load() != 0 || disabled.chats.Load() != 0 {
		t.Fatalf("unticked endpoint got %d, disabled got %d chat requests; want 0", unticked.chats.Load(), disabled.chats.Load())
	}
	if ticked.chats.Load() != 1 {
		t.Fatalf("ticked endpoint got %d chat requests; want 1", ticked.chats.Load())
	}
	if got := ticked.seenModels(); len(got) != 1 || got[0] != "chat-parser" {
		t.Fatalf("model sent = %v, want the row's chat_model", got)
	}
}

// capability_models overrides chat_model for this capability.
func TestRoutedParse_CapabilityModelOverride(t *testing.T) {
	f := newFakeOpenAI(t)
	e := ep("box", f.url(), 1, fp)
	e.CapabilityModels = map[string]string{fp: "qwen2.5:7b-instruct"}
	if _, err := NewRoutedFilenameParser(newTestPool(e).PoolSource).ParseBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got := f.seenModels(); len(got) != 1 || got[0] != "qwen2.5:7b-instruct" {
		t.Fatalf("model sent = %v", got)
	}
}

// No endpoint ticks the capability: fail closed with a named error, never a
// silent success or a fallback to some other backend.
func TestRoutedParse_NoCapableEndpointFailsClosed(t *testing.T) {
	f := newFakeOpenAI(t)
	pool := newTestPool(ep("embed-only", f.url(), 1, et))
	_, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"x"})
	if !errors.Is(err, aidispatch.ErrNoCapableEndpoint) {
		t.Fatalf("err = %v, want ErrNoCapableEndpoint", err)
	}
	if f.chats.Load() != 0 {
		t.Fatal("an endpoint without the capability was called")
	}
}

// A transport failure benches the endpoint and the same request is retried
// on the next eligible peer.
func TestRoutedParse_FailoverOnTransportError(t *testing.T) {
	live := newFakeOpenAI(t)
	pool := newTestPool(ep("dead", deadURL(t), 1, fp), ep("live", live.url(), 2, fp))

	res, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || live.chats.Load() != 1 {
		t.Fatalf("results %d, live chats %d; want 3 and 1", len(res), live.chats.Load())
	}
	if !pool.health.InCooldown("dead") {
		t.Fatal("dead endpoint was not benched after a transport failure")
	}
	if a := pool.attr.Snapshot("dead"); a.Requests != 1 || a.Failures != 1 {
		t.Fatalf("dead attribution = %+v", a)
	}
	if a := pool.attr.Snapshot("live"); a.Requests != 1 || a.Failures != 0 || a.ByCapability[fp] != 1 {
		t.Fatalf("live attribution = %+v", a)
	}
}

// A short reply (fewer results than filenames -- what the Mac did at batch
// 20) stays an error carrying *ResultCountError, and is NOT retried on a
// peer: it is a quality failure that the scan phase's split handles.
func TestRoutedParse_ShortReplyIsQualityFailureNotFailover(t *testing.T) {
	short, peer := newFakeOpenAI(t), newFakeOpenAI(t)
	short.short = 1
	pool := newTestPool(ep("short", short.url(), 1, fp), ep("peer", peer.url(), 2, fp))

	res, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"a", "b", "c"})
	if _, ok := errors.AsType[*ResultCountError](err); !ok {
		t.Fatalf("err = %v (res len %d), want *ResultCountError", err, len(res))
	}
	if res != nil {
		t.Fatalf("a short reply returned %d results instead of none", len(res))
	}
	if peer.chats.Load() != 0 {
		t.Fatal("a quality failure was retried on a peer")
	}
	if pool.health.InCooldown("short") {
		t.Fatal("a quality failure benched the endpoint")
	}
}

// An undecodable reply is a quality failure even when the model's text looks
// like a transport error: Classify must never read model-written text as
// "connection refused" and move the batch to a peer.
func TestRoutedParse_ReplyTextNeverTriggersFailover(t *testing.T) {
	garbled, peer := newFakeOpenAI(t), newFakeOpenAI(t)
	garbled.rawReply = "dial tcp: connection refused"
	pool := newTestPool(ep("garbled", garbled.url(), 1, fp), ep("peer", peer.url(), 2, fp))

	_, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"a"})
	if _, ok := errors.AsType[*ReplyParseError](err); !ok {
		t.Fatalf("err = %v, want *ReplyParseError", err)
	}
	if peer.chats.Load() != 0 || pool.health.InCooldown("garbled") {
		t.Fatalf("peer chats %d, garbled benched %v; want 0 and false", peer.chats.Load(), pool.health.InCooldown("garbled"))
	}
}

// Free-slot assignment through the real parser: with the preferred endpoint's
// only slot held by a request in flight, the next request lands on the
// lower-priority peer instead of queueing.
func TestRoutedParse_SpilloverWhenPreferredSaturated(t *testing.T) {
	pref, peer := newFakeOpenAI(t), newFakeOpenAI(t)
	pref.block = make(chan struct{})
	pref.entered = make(chan struct{}, 1)
	p := ep("pref", pref.url(), 1, fp)
	p.Concurrency = 1
	pool := newTestPool(p, ep("peer", peer.url(), 2, fp))
	parser := NewRoutedFilenameParser(pool.PoolSource)

	firstDone := make(chan error, 1)
	go func() {
		_, err := parser.ParseBatch(context.Background(), []string{"first"})
		firstDone <- err
	}()
	select {
	case <-pref.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never reached the preferred endpoint")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := parser.ParseBatch(ctx, []string{"second"}); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if peer.chats.Load() != 1 {
		t.Fatalf("peer chats = %d, want the second request to spill there", peer.chats.Load())
	}
	close(pref.block)
	if err := <-firstDone; err != nil {
		t.Fatalf("first request: %v", err)
	}
	if pref.chats.Load() != 1 {
		t.Fatalf("preferred chats = %d, want 1", pref.chats.Load())
	}
}

// Embeddings go only to endpoints serving the client's own model, even when
// a preferred endpoint with another model ticks embed.text.
func TestEmbedRouted_PinnedToClientModel(t *testing.T) {
	other, same := newFakeOpenAI(t), newFakeOpenAI(t)
	o := ep("other", other.url(), 1, et)
	o.EmbedModel = "nomic-embed-text"
	pool := newTestPool(o, ep("same", same.url(), 5, et))

	c := NewEmbeddingClientWithOptions("ollama", "bge-m3", deadURL(t)).WithPoolRouting(pool.PoolSource)
	vecs, err := c.EmbedBatch(context.Background(), []string{"one", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors for 2 inputs", len(vecs))
	}
	if other.embeds.Load() != 0 {
		t.Fatal("an endpoint with a different embed model was used")
	}
	if got := same.seenModels(); len(got) != 1 || got[0] != "bge-m3" {
		t.Fatalf("models sent = %v, want [bge-m3]", got)
	}
}

// No endpoint serves the pinned model: fail closed rather than embed in a
// different vector space.
func TestEmbedRouted_PinnedModelFailsClosed(t *testing.T) {
	other := newFakeOpenAI(t)
	o := ep("other", other.url(), 1, et)
	o.EmbedModel = "nomic-embed-text"
	c := NewEmbeddingClientWithOptions("ollama", "bge-m3", "").WithPoolRouting(newTestPool(o).PoolSource)
	_, err := c.EmbedBatch(context.Background(), []string{"x"})
	if !errors.Is(err, aidispatch.ErrNoCapableEndpoint) || other.embeds.Load() != 0 {
		t.Fatalf("err = %v, other embeds = %d; want ErrNoCapableEndpoint and 0", err, other.embeds.Load())
	}
}

// A transport failure on embeddings fails over to a same-model peer.
func TestEmbedRouted_FailoverOnTransportError(t *testing.T) {
	live := newFakeOpenAI(t)
	pool := newTestPool(ep("dead", deadURL(t), 1, et), ep("live", live.url(), 2, et))
	c := NewEmbeddingClientWithOptions("ollama", "bge-m3", "").WithPoolRouting(pool.PoolSource)
	start := time.Now()
	vecs, err := c.EmbedBatch(context.Background(), []string{"x"})
	if err != nil || len(vecs) != 1 {
		t.Fatalf("vecs %d, err %v", len(vecs), err)
	}
	// The dead endpoint must fail over at once, not burn the legacy 1s+4s
	// backoff against a refused connection first.
	if elapsed := time.Since(start); elapsed > 900*time.Millisecond {
		t.Fatalf("failover took %v; a refused connection must not be retried in place", elapsed)
	}
	if !pool.health.InCooldown("dead") || live.embeds.Load() != 1 {
		t.Fatalf("dead benched=%v live embeds=%d", pool.health.InCooldown("dead"), live.embeds.Load())
	}
}

// With the switch off, the client uses its legacy base URL exactly as before
// and the pool is never consulted; flipping the switch takes effect on the
// next call, with no rebuild.
func TestEmbedRouted_SwitchOffPreservesLegacyResolution(t *testing.T) {
	legacy, pooled := newFakeOpenAI(t), newFakeOpenAI(t)
	pool := newTestPool(ep("pooled", pooled.url(), 1, et))
	pool.active.Store(false)
	c := NewEmbeddingClientWithOptions("ollama", "bge-m3", legacy.url()).WithPoolRouting(pool.PoolSource)

	if _, err := c.EmbedBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if legacy.embeds.Load() != 1 || pooled.embeds.Load() != 0 {
		t.Fatalf("switch off: legacy %d pooled %d; want 1 and 0", legacy.embeds.Load(), pooled.embeds.Load())
	}
	if a := pool.attr.Snapshot("pooled"); a.Requests != 0 {
		t.Fatalf("switch off still attributed a request: %+v", a)
	}

	pool.active.Store(true)
	if _, err := c.EmbedBatch(context.Background(), []string{"y"}); err != nil {
		t.Fatal(err)
	}
	if legacy.embeds.Load() != 1 || pooled.embeds.Load() != 1 {
		t.Fatalf("switch on: legacy %d pooled %d; want 1 and 1", legacy.embeds.Load(), pooled.embeds.Load())
	}
	if a := pool.attr.Snapshot("pooled"); a.Requests != 1 || a.ByCapability[et] != 1 {
		t.Fatalf("pooled attribution = %+v", a)
	}
}

// A nil pool or one with the switch off is inactive; ConfigPool reads the
// live switch (default off).
func TestPoolSource_Inactive(t *testing.T) {
	var nilPool *PoolSource
	if nilPool.IsActive() {
		t.Fatal("nil pool reported active")
	}
	if ConfigPool().IsActive() {
		t.Fatal("ConfigPool active with the default config (ai_endpoints_routing defaults to false)")
	}
}

// An endpoint naming an auth_ref whose secret is unset is an endpoint
// failure: the call fails over rather than sending an unauthenticated
// request.
func TestRoutedParse_MissingSecretFailsOver(t *testing.T) {
	cloud, local := newFakeOpenAI(t), newFakeOpenAI(t)
	c := ep("cloud", cloud.url(), 1, fp)
	c.AuthRef = "openai_api_key"
	pool := newTestPool(c, ep("local", local.url(), 2, fp))
	if _, err := NewRoutedFilenameParser(pool.PoolSource).ParseBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if cloud.chats.Load() != 0 || local.chats.Load() != 1 {
		t.Fatalf("cloud %d local %d; want 0 and 1", cloud.chats.Load(), local.chats.Load())
	}
}
