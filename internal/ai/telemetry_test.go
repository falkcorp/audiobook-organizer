// file: internal/ai/telemetry_test.go
// version: 1.1.0
// guid: 5d0b3f7e-6c1a-4e48-9a52-0f1b7c9d2e31
// last-edited: 2026-10-10

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
)

// These tests swap package-level telemetry state (aiMetrics, aiTracerOverride)
// and so must not call t.Parallel.

// aiTelemetry is a private meter + tracer pair installed for one test.
type aiTelemetry struct {
	reader *sdkmetric.ManualReader
	spans  *tracetest.SpanRecorder
}

func installAITelemetry(t *testing.T) *aiTelemetry {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append([]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetry.Views()...)...)
	spans := tracetest.NewSpanRecorder()
	tr := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("ai-test")

	prevMetrics := aiMetrics.Load()
	aiMetrics.Store(newAIInstruments(telemetry.MeterFrom(mp, "ai")))
	aiTracerOverride.Store(&tr)
	// The admitted-model set is process state like the instruments: a test must
	// start from an empty one or its model labels depend on test order.
	modelLabels.reset()
	t.Cleanup(func() {
		modelLabels.reset()
		aiMetrics.Store(prevMetrics)
		aiTracerOverride.Store(nil)
		_ = mp.Shutdown(context.Background())
	})
	return &aiTelemetry{reader: reader, spans: spans}
}

func (a *aiTelemetry) collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := a.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func metricByName(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func attrsMatch(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.String() != v {
			return false
		}
	}
	return true
}

// counterSum is the sum of every data point of the named counter whose
// attributes include all of want.
func counterSum(rm metricdata.ResourceMetrics, name string, want map[string]string) int64 {
	m := metricByName(rm, name)
	if m == nil {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		return 0
	}
	var total int64
	for _, dp := range sum.DataPoints {
		if attrsMatch(dp.Attributes, want) {
			total += dp.Value
		}
	}
	return total
}

func histogramCount(rm metricdata.ResourceMetrics, name string, want map[string]string) uint64 {
	m := metricByName(rm, name)
	if m == nil {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	var total uint64
	for _, dp := range h.DataPoints {
		if attrsMatch(dp.Attributes, want) {
			total += dp.Count
		}
	}
	return total
}

// allMetricAttributeKeys lists every attribute key on every data point.
func allMetricAttributeKeys(rm metricdata.ResourceMetrics) map[string]bool {
	keys := map[string]bool{}
	add := func(s attribute.Set) {
		for _, kv := range s.ToSlice() {
			keys[string(kv.Key)] = true
		}
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range d.DataPoints {
					add(dp.Attributes)
				}
			case metricdata.Histogram[float64]:
				for _, dp := range d.DataPoints {
					add(dp.Attributes)
				}
			}
		}
	}
	return keys
}

const siteSentinel = "SENTINEL-FREE-TEXT"

// chatReply is a minimal chat completion carrying the given content and usage.
func chatReply(content string, promptTokens, completionTokens int) string {
	b, _ := json.Marshal(map[string]any{
		"id": "test", "object": "chat.completion", "created": 1, "model": "test-model",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": content},
		}},
		"usage": map[string]any{
			"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
			"total_tokens": promptTokens + completionTokens,
		},
	})
	return string(b)
}

const embeddingReply = `{"object":"list","model":"embed-model","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],` +
	`"usage":{"prompt_tokens":11,"total_tokens":11}}`

// stubEndpoint serves an OpenAI-compatible endpoint. handler may be nil (always
// 200 with body). requests counts every HTTP request received.
type stubEndpoint struct {
	*httptest.Server
	requests atomic.Int64
}

func newStubEndpoint(t *testing.T, body string, handler func(n int64, w http.ResponseWriter) bool) *stubEndpoint {
	t.Helper()
	s := &stubEndpoint{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := s.requests.Add(1)
		if handler != nil && handler(n, w) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

// assertSiteTelemetry checks the metrics and the span one successful request
// at a call site must leave behind. wantOutput is false for embeddings, which
// have input tokens only.
func assertSiteTelemetry(t *testing.T, tel *aiTelemetry, task, model string, wantOutput bool) {
	t.Helper()
	rm := tel.collect(t)
	base := map[string]string{"provider": "local", "model": model, "task": task}
	with := func(k, v string) map[string]string {
		m := map[string]string{k: v}
		for bk, bv := range base {
			m[bk] = bv
		}
		return m
	}
	if got := counterSum(rm, "ai.requests", with("outcome", "ok")); got != 1 {
		t.Errorf("ai.requests{task=%s,outcome=ok} = %d, want 1", task, got)
	}
	if got := histogramCount(rm, "ai.request.duration", base); got != 1 {
		t.Errorf("ai.request.duration{task=%s} count = %d, want 1", task, got)
	}
	if got := counterSum(rm, "ai.tokens", with("direction", "input")); got != 11 {
		t.Errorf("ai.tokens{task=%s,direction=input} = %d, want 11", task, got)
	}
	wantOut := int64(0)
	if wantOutput {
		wantOut = 7
	}
	if got := counterSum(rm, "ai.tokens", with("direction", "output")); got != wantOut {
		t.Errorf("ai.tokens{task=%s,direction=output} = %d, want %d", task, got, wantOut)
	}
	if got := counterSum(rm, "ai.failures", base); got != 0 {
		t.Errorf("ai.failures{task=%s} = %d on a clean call, want 0", task, got)
	}

	var found bool
	for _, sp := range tel.spans.Ended() {
		if sp.Name() != "ai."+task {
			continue
		}
		found = true
		for _, kv := range sp.Attributes() {
			if strings.Contains(kv.Value.String(), siteSentinel) {
				t.Errorf("span ai.%s attribute %s carries caller text: %q", task, kv.Key, kv.Value.String())
			}
			switch string(kv.Key) {
			case "ai.provider", "ai.model", "ai.task", "ai.tokens.input", "ai.tokens.output":
			default:
				t.Errorf("span ai.%s has unexpected attribute %s", task, kv.Key)
			}
		}
	}
	if !found {
		t.Errorf("no span named ai.%s was recorded", task)
	}
}

func newTestParser(t *testing.T, srv *stubEndpoint) *OpenAIParser {
	t.Helper()
	p := NewOpenAIParserWithBaseURL(nil, "test-key", srv.URL, "test-model", true)
	if p.provider != "local" {
		t.Fatalf("provider = %q for a non-OpenAI base URL, want local", p.provider)
	}
	return p
}

func TestObserve_FilenameParse(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T","author":"A"}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).ParseFilename(context.Background(), siteSentinel); err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskFilenameParse, "test-model", true)
}

func TestObserve_AudiobookParse(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T","author":"A"}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).ParseAudiobook(context.Background(), AudiobookContext{FilePath: siteSentinel}); err != nil {
		t.Fatalf("ParseAudiobook: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskAudiobookParse, "test-model", true)
}

func TestObserve_BatchParse(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"results":[{"title":"T"}]}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).ParseBatch(context.Background(), []string{siteSentinel}); err != nil {
		t.Fatalf("ParseBatch: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskBatchParse, "test-model", true)
}

func TestObserve_CoverArt(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T"}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).ParseCoverArt(context.Background(), []byte{1, 2, 3}, "image/png"); err != nil {
		t.Fatalf("ParseCoverArt: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskCoverArt, "test-model", true)
}

func TestObserve_CoverText(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).readCoverTextImage(context.Background(), "data:image/png;base64,AA=="); err != nil {
		t.Fatalf("readCoverTextImage: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskCoverText, "test-model", true)
}

func TestObserve_AuthorDedupReview(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"suggestions":[]}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).reviewAuthorBatch(context.Background(), []AuthorDedupInput{{Index: 0, CanonicalName: siteSentinel}}); err != nil {
		t.Fatalf("reviewAuthorBatch: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskAuthorDedupReview, "test-model", true)
}

func TestObserve_AuthorDiscovery(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"suggestions":[]}`, 11, 7), nil)
	if _, err := newTestParser(t, srv).discoverAuthorBatch(context.Background(), []AuthorDiscoveryInput{{ID: 1, Name: siteSentinel}}); err != nil {
		t.Fatalf("discoverAuthorBatch: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskAuthorDiscovery, "test-model", true)
}

func TestObserve_MetadataReview(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"scores":[]}`, 11, 7), nil)
	_, err := newTestParser(t, srv).scoreMetadataBatch(context.Background(),
		MetadataLLMQuery{Title: siteSentinel}, []MetadataLLMCandidate{{Index: 0, Title: siteSentinel}})
	if err != nil {
		t.Fatalf("scoreMetadataBatch: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskMetadataReview, "test-model", true)
}

func TestObserve_Embed(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, embeddingReply, nil)
	c := NewEmbeddingClientWithOptions("test-key", "embed-model", srv.URL)
	if _, err := c.embedBatchRaw(context.Background(), []string{siteSentinel}); err != nil {
		t.Fatalf("embedBatchRaw: %v", err)
	}
	assertSiteTelemetry(t, tel, TaskEmbed, "embed-model", false)
}

// A reply with no usage block (some local servers omit it) records no token
// series at all, rather than a false zero.
func TestZeroUsageRecordsNoTokenSeries(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T"}`, 0, 0), nil)
	if _, err := newTestParser(t, srv).ParseFilename(context.Background(), "x"); err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	rm := tel.collect(t)
	if m := metricByName(rm, "ai.tokens"); m != nil {
		t.Errorf("ai.tokens was exported for a zero-usage reply: %+v", m.Data)
	}
	if got := counterSum(rm, "ai.requests", map[string]string{"outcome": "ok"}); got != 1 {
		t.Errorf("ai.requests ok = %d, want 1", got)
	}
}

// A reply that arrives but cannot be used is an ok request plus a failure; the
// request is not counted as an error because the SDK call succeeded.
func TestReplyFailureIsOkRequestPlusFailure(t *testing.T) {
	tel := installAITelemetry(t)

	t.Run("reply_parse", func(t *testing.T) {
		srv := newStubEndpoint(t, chatReply("this is not json", 11, 7), nil)
		_, err := newTestParser(t, srv).ParseFilename(context.Background(), "x")
		if err == nil {
			t.Fatal("ParseFilename accepted a non-JSON reply")
		}
	})
	t.Run("count_mismatch", func(t *testing.T) {
		srv := newStubEndpoint(t, chatReply(`{"results":[{"title":"A"},{"title":"B"}]}`, 11, 7), nil)
		_, err := newTestParser(t, srv).ParseBatch(context.Background(), []string{"only-one"})
		if err == nil {
			t.Fatal("ParseBatch accepted a reply of the wrong length")
		}
		if _, ok := errorsAsResultCount(err); !ok {
			t.Fatalf("error is not a *ResultCountError: %v", err)
		}
	})

	rm := tel.collect(t)
	if got := counterSum(rm, "ai.requests", map[string]string{"outcome": "ok"}); got != 2 {
		t.Errorf("ai.requests ok = %d, want 2 (both calls reached the SDK and succeeded)", got)
	}
	if got := counterSum(rm, "ai.requests", map[string]string{"outcome": "error"}); got != 0 {
		t.Errorf("ai.requests error = %d, want 0: a bad reply is not a transport error", got)
	}
	for _, c := range []struct{ task, reason string }{
		{TaskFilenameParse, "reply_parse"},
		{TaskBatchParse, "count_mismatch"},
	} {
		if got := counterSum(rm, "ai.failures", map[string]string{"task": c.task, "reason": c.reason}); got != 1 {
			t.Errorf("ai.failures{task=%s,reason=%s} = %d, want 1", c.task, c.reason, got)
		}
	}
	// The count error is wrapped in a reply error: it must not also count as
	// reply_parse.
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskBatchParse, "reason": "reply_parse"}); got != 0 {
		t.Errorf("a count mismatch was also counted as reply_parse (%d)", got)
	}
}

// A transport failure that the retry loop absorbs is two attempts, each
// counted: one error, one ok.
func TestRetryIsANewAttempt(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"results":[{"title":"T"}]}`, 11, 7), func(n int64, w http.ResponseWriter) bool {
		if n == 1 {
			http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
			return true
		}
		return false
	})
	// newRoutedEndpointParser sets the SDK's own retries to zero, so the first
	// 500 reaches DoWithRetry instead of being absorbed inside the SDK.
	p := newRoutedEndpointParser("test-key", srv.URL, "test-model", "")
	if _, err := p.ParseBatch(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("ParseBatch: %v", err)
	}
	if got := srv.requests.Load(); got != 2 {
		t.Fatalf("server saw %d requests, want 2", got)
	}
	rm := tel.collect(t)
	for outcome, want := range map[string]int64{"error": 1, "ok": 1} {
		if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskBatchParse, "outcome": outcome}); got != want {
			t.Errorf("ai.requests{outcome=%s} = %d, want %d", outcome, got, want)
		}
	}
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskBatchParse, "reason": "other"}); got != 1 {
		t.Errorf("ai.failures{reason=other} = %d, want 1 for the 500", got)
	}
	if got := histogramCount(rm, "ai.request.duration", map[string]string{"task": TaskBatchParse}); got != 2 {
		t.Errorf("duration observations = %d, want 2 (one per attempt)", got)
	}
}

// The span the call creates is a child of the caller's span, so a
// library.ai-parse operation shows its model calls underneath it.
func TestSpanNestsUnderCallerContext(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T"}`, 11, 7), nil)

	parentCtx, parent := tracer().Start(context.Background(), "caller")
	if _, err := newTestParser(t, srv).ParseFilename(parentCtx, "x"); err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	parent.End()

	var call, caller tracetest.SpanStub
	for _, sp := range tel.spans.Ended() {
		switch sp.Name() {
		case "ai." + TaskFilenameParse:
			call = tracetest.SpanStubFromReadOnlySpan(sp)
		case "caller":
			caller = tracetest.SpanStubFromReadOnlySpan(sp)
		}
	}
	if call.Name == "" || caller.Name == "" {
		t.Fatalf("spans missing: call=%q caller=%q", call.Name, caller.Name)
	}
	if call.Parent.SpanID() != caller.SpanContext.SpanID() || call.SpanContext.TraceID() != caller.SpanContext.TraceID() {
		t.Errorf("ai span parent = %s (trace %s), want the caller span %s (trace %s)",
			call.Parent.SpanID(), call.SpanContext.TraceID(), caller.SpanContext.SpanID(), caller.SpanContext.TraceID())
	}
}

func TestWithAISpanPassesSpanContextAndMarksFailure(t *testing.T) {
	tel := installAITelemetry(t)
	failure := errors.New(`POST "http://192.0.2.7:11434/v1/chat/completions": 500 Internal Server Error ` + siteSentinel)
	_, err := WithAISpan(context.Background(), "ai.test", func(ctx context.Context) (int, error) {
		_, child := tracer().Start(ctx, "child")
		child.End()
		return 0, failure
	})
	if err != failure {
		t.Fatalf("err = %v", err)
	}
	var parentID, childParent string
	var errorAttr bool
	for _, sp := range tel.spans.Ended() {
		switch sp.Name() {
		case "ai.test":
			parentID = sp.SpanContext().SpanID().String()
			for _, kv := range sp.Attributes() {
				if kv.Key == "error" && kv.Value.AsBool() {
					errorAttr = true
				}
			}
			if sp.Status().Code != codes.Error {
				t.Errorf("status = %v, want Error", sp.Status().Code)
			}
			assertSpanHasNoText(t, sp, siteSentinel, "192.0.2.7")
		case "child":
			childParent = sp.Parent().SpanID().String()
		}
	}
	if parentID == "" || parentID != childParent {
		t.Errorf("child parent = %q, want %q: fn did not receive the span context", childParent, parentID)
	}
	if !errorAttr {
		t.Errorf("span has no error=true attribute")
	}
}

// A cancellation is the caller giving up, not a failed call: the metrics treat
// it that way, so the span must not be red either.
func TestWithAISpanCancellationIsNotAnError(t *testing.T) {
	tel := installAITelemetry(t)
	if _, err := WithAISpan(context.Background(), "ai.test", func(context.Context) (int, error) {
		return 0, context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	for _, sp := range tel.spans.Ended() {
		if sp.Status().Code == codes.Error {
			t.Errorf("a cancelled call set span status Error")
		}
		for _, kv := range sp.Attributes() {
			if kv.Key == "error" {
				t.Errorf("a cancelled call set the error attribute")
			}
		}
	}
}

// assertSpanHasNoText fails when anything a span carries (attributes, events,
// status description) contains one of the banned substrings.
func assertSpanHasNoText(t *testing.T, sp sdktrace.ReadOnlySpan, banned ...string) {
	t.Helper()
	check := func(where, v string) {
		for _, b := range banned {
			if b != "" && strings.Contains(v, b) {
				t.Errorf("span %s %s carries %q: %q", sp.Name(), where, b, v)
			}
		}
	}
	check("name", sp.Name())
	check("status description", sp.Status().Description)
	for _, kv := range sp.Attributes() {
		check("attribute "+string(kv.Key), kv.Value.Emit())
	}
	for _, ev := range sp.Events() {
		check("event name", ev.Name)
		for _, kv := range ev.Attributes {
			check("event "+ev.Name+" attribute "+string(kv.Key), kv.Value.Emit())
		}
	}
}

// S2: a failed call must not put the error text into any span EVENT, status or
// attribute. A transport error formats as the request URL and the dial target
// (for a pool endpoint, a LAN host and port); an API error carries the
// provider's raw response body in its fields and, in older SDK versions, in its
// text. Attribute-only checks cannot catch an exception event, which is what
// RecordError used to add, so this reads Events() and the status description.
func TestFailedCallSpanCarriesNoErrorText(t *testing.T) {
	const bodyText = "raw-body-echo-of-the-prompt"
	for name, build := range map[string]func(srv *stubEndpoint) *OpenAIParser{
		"legacy parser": func(srv *stubEndpoint) *OpenAIParser { return newTestParser(t, srv) },
		"routed parser": func(srv *stubEndpoint) *OpenAIParser {
			return newRoutedEndpointParser("test-key", srv.URL, "test-model", "pool-node")
		},
	} {
		t.Run(name, func(t *testing.T) {
			tel := installAITelemetry(t)
			srv := newStubEndpoint(t, "", func(_ int64, w http.ResponseWriter) bool {
				http.Error(w, `{"error":{"message":"`+bodyText+`","type":"invalid_request_error","code":"bad"}}`, http.StatusUnauthorized)
				return true
			})
			if _, err := build(srv).ParseFilename(context.Background(), "x"); err == nil {
				t.Fatal("ParseFilename succeeded against a 401")
			}
			host := strings.TrimPrefix(srv.URL, "http://")
			var seen bool
			for _, sp := range tel.spans.Ended() {
				if sp.Name() != "ai."+TaskFilenameParse {
					continue
				}
				seen = true
				assertSpanHasNoText(t, sp, bodyText, srv.URL, host, "127.0.0.1", "chat/completions", "POST")
				if sp.Status().Code != codes.Error {
					t.Errorf("status = %v, want Error", sp.Status().Code)
				}
				got := map[string]string{}
				for _, kv := range sp.Attributes() {
					got[string(kv.Key)] = kv.Value.Emit()
				}
				if got["ai.failure_reason"] != reasonPermanent || got["ai.outcome"] != outcomeError ||
					got["http.response.status_code"] != "401" {
					t.Errorf("classification attributes = %v, want reason=permanent outcome=error status=401", got)
				}
			}
			if !seen {
				t.Fatal("no ai.filename_parse span recorded")
			}
		})
	}
}

func TestUnreachableCallSpanCarriesNoErrorText(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, "", nil)
	url := srv.URL
	srv.Close() // nothing listens: the dial is refused
	p := newRoutedEndpointParser("test-key", url, "test-model", "pool-node")
	_, err := p.ParseFilename(context.Background(), "x")
	if err == nil {
		t.Fatal("ParseFilename succeeded against a closed port")
	}
	if !strings.Contains(err.Error(), strings.TrimPrefix(url, "http://")) {
		t.Skipf("the transport error no longer names the dial target (%v); nothing left to leak", err)
	}
	host := strings.TrimPrefix(url, "http://")
	var seen bool
	for _, sp := range tel.spans.Ended() {
		if sp.Name() != "ai."+TaskFilenameParse {
			continue
		}
		seen = true
		assertSpanHasNoText(t, sp, host, "127.0.0.1", "dial tcp", "connection refused", "Post ")
		got := map[string]string{}
		for _, kv := range sp.Attributes() {
			got[string(kv.Key)] = kv.Value.Emit()
		}
		if got["ai.failure_reason"] != reasonUnreachable {
			t.Errorf("ai.failure_reason = %q, want %q", got["ai.failure_reason"], reasonUnreachable)
		}
	}
	if !seen {
		t.Fatal("no ai.filename_parse span recorded")
	}
}

// N1: a reply the model got wrong is an event on the caller's span, never a red
// status: a count mismatch is recovered by the batch split by design.
func TestReplyFailureIsAnEventNotAnErrorStatus(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"results":[{"title":"A"},{"title":"B"}]}`, 11, 7), nil)
	parentCtx, parent := tracer().Start(context.Background(), "caller")
	_, err := newTestParser(t, srv).ParseBatch(parentCtx, []string{"only-one"})
	parent.End()
	if err == nil {
		t.Fatal("ParseBatch accepted a reply of the wrong length")
	}
	var found bool
	for _, sp := range tel.spans.Ended() {
		if sp.Name() != "caller" {
			continue
		}
		if sp.Status().Code == codes.Error {
			t.Errorf("a recoverable count mismatch turned the caller's span red")
		}
		for _, ev := range sp.Events() {
			if ev.Name == "ai.reply_failure" {
				found = true
				assertSpanHasNoText(t, sp, "only-one", "results", "title")
			}
		}
	}
	if !found {
		t.Error("no ai.reply_failure event on the caller's span")
	}
}

// S3: the SDK retries a 429 on its own inside one call. Each HTTP attempt is a
// request: two throttles then a success is three requests (two error, one ok),
// two rate_limit failures and three duration observations, whichever
// constructor built the parser.
func TestSDKRetriesAreCountedPerAttempt(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T"}`, 11, 7), func(n int64, w http.ResponseWriter) bool {
		if n <= 2 {
			// Retry-After-Ms keeps the SDK's backoff to a millisecond.
			w.Header().Set("Retry-After-Ms", "1")
			http.Error(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, http.StatusTooManyRequests)
			return true
		}
		return false
	})
	if _, err := newTestParser(t, srv).ParseFilename(context.Background(), "x"); err != nil {
		t.Fatalf("ParseFilename: %v", err)
	}
	if got := srv.requests.Load(); got != 3 {
		t.Fatalf("server saw %d requests, want 3 (the SDK retried twice)", got)
	}
	rm := tel.collect(t)
	task := map[string]string{"task": TaskFilenameParse}
	for outcome, want := range map[string]int64{"error": 2, "ok": 1} {
		if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": outcome}); got != want {
			t.Errorf("ai.requests{outcome=%s} = %d, want %d", outcome, got, want)
		}
	}
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskFilenameParse, "reason": "rate_limit"}); got != 2 {
		t.Errorf("ai.failures{reason=rate_limit} = %d, want 2 (the throttles the SDK absorbed)", got)
	}
	if got := histogramCount(rm, "ai.request.duration", task); got != 3 {
		t.Errorf("duration observations = %d, want 3 (one per attempt)", got)
	}
	if got := counterSum(rm, "ai.tokens", map[string]string{"direction": "input"}); got != 11 {
		t.Errorf("input tokens = %d, want 11 (the final reply only)", got)
	}
	for _, sp := range tel.spans.Ended() {
		if sp.Name() != "ai."+TaskFilenameParse {
			continue
		}
		var attempts int64
		for _, kv := range sp.Attributes() {
			if kv.Key == "ai.http_attempts" {
				attempts = kv.Value.AsInt64()
			}
		}
		if attempts != 3 {
			t.Errorf("span ai.http_attempts = %d, want 3", attempts)
		}
	}
}

// An absorbed 429 whose body names quota exhaustion is a quota failure, not a
// rate_limit, and peeking at the body must leave it whole for the SDK: the
// final attempt's error is classified from the body the SDK parses.
func TestSDKRetriedQuota429IsQuotaAndBodyIsIntact(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, "", func(_ int64, w http.ResponseWriter) bool {
		w.Header().Set("Retry-After-Ms", "1")
		http.Error(w, `{"error":{"message":"out of credit","type":"insufficient_quota","code":"credit_balance_exhausted"}}`, http.StatusTooManyRequests)
		return true
	})
	_, err := newTestParser(t, srv).ParseFilename(context.Background(), "x")
	if err == nil {
		t.Fatal("ParseFilename succeeded against a 429")
	}
	if _, quota := aiHTTPFailure(err); !quota {
		t.Fatalf("the final error lost its body to the peek: %v", err)
	}
	rm := tel.collect(t)
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskFilenameParse, "reason": "quota"}); got != 3 {
		t.Errorf("ai.failures{reason=quota} = %d, want 3 (two absorbed, one final)", got)
	}
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskFilenameParse, "reason": "rate_limit"}); got != 0 {
		t.Errorf("a quota 429 was counted as rate_limit %d time(s)", got)
	}
	if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": "error"}); got != 3 {
		t.Errorf("ai.requests{outcome=error} = %d, want 3", got)
	}
}

// S4: a cover-text reply that cannot be parsed is an ok request plus a
// reply_parse failure, so the reply-quality panel can show cover_text.
func TestCoverTextUnparseableReplyRecordsReplyParse(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newVisionServer(t, "I cannot read this.")
	tp := newTestPool(visionEP("pool-node", srv.url(), 10))
	if _, err := NewRoutedCoverTextReader(tp.PoolSource).ReadCoverText(context.Background(), []byte{1}, "image/jpeg"); err == nil {
		t.Fatal("ReadCoverText accepted a non-JSON reply")
	}
	rm := tel.collect(t)
	if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskCoverText, "reason": "reply_parse"}); got != 1 {
		t.Errorf("ai.failures{task=cover_text,reason=reply_parse} = %d, want 1", got)
	}
	if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskCoverText, "outcome": "ok"}); got != 1 {
		t.Errorf("ai.requests{task=cover_text,outcome=ok} = %d, want 1", got)
	}
	// N3: a routed parser knows its pool endpoint, so the span names it.
	var endpoint string
	for _, sp := range tel.spans.Ended() {
		if sp.Name() == "ai."+TaskCoverText {
			for _, kv := range sp.Attributes() {
				if kv.Key == "ai.endpoint_id" {
					endpoint = kv.Value.AsString()
				}
			}
		}
	}
	if endpoint != "pool-node" {
		t.Errorf("span ai.endpoint_id = %q, want pool-node", endpoint)
	}
}

// No label or span attribute may identify an endpoint, node, book, file or
// path (rules C1 and C5, and the repo is public).
func TestNoForbiddenLabels(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, chatReply(`{"title":"T"}`, 11, 7), nil)
	p := newTestParser(t, srv)
	if _, err := p.ParseFilename(context.Background(), siteSentinel); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ParseFilename(context.Background(), siteSentinel+"-2"); err != nil {
		t.Fatal(err)
	}
	RecordParseResult(TaskBatchParse, true)
	rm := tel.collect(t)

	allowed := map[string]bool{}
	for _, k := range telemetry.AttributeKeys() {
		allowed[string(k)] = true
	}
	forbidden := map[string]bool{"endpoint": true, "node": true, "book": true, "file": true, "path": true}
	for key := range allMetricAttributeKeys(rm) {
		if forbidden[key] {
			t.Errorf("metric attribute %q is forbidden on ai_* series", key)
		}
		if !allowed[key] {
			t.Errorf("metric attribute %q is not in internal/telemetry/attr.go", key)
		}
	}
	for _, sp := range tel.spans.Ended() {
		for _, kv := range sp.Attributes() {
			if forbidden[string(kv.Key)] {
				t.Errorf("span attribute %q is forbidden", kv.Key)
			}
		}
	}
}

func TestModelLabelFolding(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"gpt-5-mini", "gpt-5-mini"},
		{"", "unknown"},
		{"two words", "other"},
		{"new\nline", "other"},
		{strings.Repeat("m", 64), strings.Repeat("m", 64)},
		{strings.Repeat("m", 65), "other"},
	} {
		modelLabels.reset()
		if got := (callMeta{model: c.in}).normalized().model; got != c.want {
			t.Errorf("model %q folded to %q, want %q", c.in, got, c.want)
		}
	}
	modelLabels.reset()
	t.Cleanup(modelLabels.reset)
}

// The model label is bounded: the first maxModelLabels distinct (provider,
// model) pairs keep their names, every later one is "other", and a pair that
// was admitted keeps its name however many others arrive after it.
func TestModelLabelIsBounded(t *testing.T) {
	modelLabels.reset()
	t.Cleanup(modelLabels.reset)
	for i := range maxModelLabels {
		name := "model-" + string(rune('a'+i))
		if got := (callMeta{provider: providerLocal, model: name}).normalized().model; got != name {
			t.Fatalf("model %d folded to %q before the limit", i, got)
		}
	}
	if got := (callMeta{provider: providerLocal, model: "model-extra"}).normalized().model; got != modelOther {
		t.Errorf("model past the limit = %q, want %q", got, modelOther)
	}
	if got := (callMeta{provider: providerLocal, model: "model-a"}).normalized().model; got != "model-a" {
		t.Errorf("an admitted model lost its name after the limit was reached: %q", got)
	}
	// The same name under the other provider is a different pair.
	if got := (callMeta{provider: providerOpenAI, model: "model-a"}).normalized().model; got != modelOther {
		t.Errorf("a new (provider, model) pair past the limit = %q, want %q", got, modelOther)
	}
}

func TestProviderForBaseURL(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "")
	for _, c := range []struct{ in, want string }{
		{"", "openai"},
		{"https://api.openai.com/v1", "openai"},
		{"http://192.0.2.10:11434/v1", "local"},
		{"http://localhost:11434/v1", "local"},
	} {
		if got := providerForBaseURL(c.in); got != c.want {
			t.Errorf("providerForBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	t.Setenv("OPENAI_BASE_URL", "http://192.0.2.10:11434/v1")
	if got := providerForBaseURL(""); got != "local" {
		t.Errorf("empty base URL with OPENAI_BASE_URL set = %q, want local (the SDK honours the env)", got)
	}
}

// seriesPerFamilyCap is rule C2: at most 1,000 series per family.
const seriesPerFamilyCap = 1000

// exportedSeries counts what the Prometheus exporter writes for the instrument:
// one series per data point for a counter; for a histogram, one per bucket
// bound, +Inf, _sum and _count.
func exportedSeries(m *metricdata.Metrics) int {
	switch d := m.Data.(type) {
	case metricdata.Sum[int64]:
		return len(d.DataPoints)
	case metricdata.Histogram[float64]:
		n := 0
		for _, dp := range d.DataPoints {
			n += len(dp.Bounds) + 1 + 2
		}
		return n
	}
	return 0
}

// TestSeriesCardinalityBound drives every label combination the product can
// reach, with far more models than the label admits, and counts the exported
// series of each family against C2.
//
// Pool routing sends each endpoint's own model, so the number of models is the
// size of a growable pool, not a constant. The model label therefore admits at
// most maxModelLabels distinct (provider, model) pairs and folds the rest to
// "other" (see modelLabelSet); the worst case is (maxModelLabels + 2 "other"
// pairs) x 9 tasks, and the 15 series per combination of the 12-bucket
// histogram make that 945. The test fails if the bucket list, the label set or
// maxModelLabels grows past the cap.
func TestSeriesCardinalityBound(t *testing.T) {
	tel := installAITelemetry(t)
	ins := aiInstrumentsFor()
	ctx := context.Background()
	tasks := []string{TaskFilenameParse, TaskAudiobookParse, TaskBatchParse, TaskCoverArt, TaskCoverText,
		TaskAuthorDedupReview, TaskAuthorDiscovery, TaskMetadataReview, TaskEmbed}
	const modelsOffered = 40 // a pool far larger than any real one
	for _, provider := range []string{providerOpenAI, providerLocal} {
		for i := range modelsOffered {
			model := "model-" + string(rune('a'+i%26)) + string(rune('a'+i/26))
			for _, task := range tasks {
				m := callMeta{provider: provider, model: model, task: task}
				ins.duration.Record(ctx, 1, metricWithSet(m))
				for _, outcome := range []string{outcomeOK, outcomeError, outcomeTimeout, outcomeCanceled} {
					ins.requests.Add(ctx, 1, metricWithAttrs(m, attrOutcome.String(outcome)))
				}
				for _, dir := range []string{"input", "output"} {
					ins.tokens.Add(ctx, 1, metricWithAttrs(m, attrDirection.String(dir)))
				}
				for _, reason := range []string{reasonRateLimit, reasonQuota, reasonUnreachable, reasonTimeout,
					reasonPermanent, reasonReplyParse, reasonCountMismatch, reasonOther} {
					ins.failures.Add(ctx, 1, metricWithAttrs(m, attrReason.String(reason)))
				}
			}
		}
	}
	rm := tel.collect(t)
	for _, name := range []string{"ai.requests", "ai.request.duration", "ai.tokens", "ai.failures"} {
		m := metricByName(rm, name)
		if m == nil {
			t.Fatalf("%s not exported", name)
		}
		n := exportedSeries(m)
		t.Logf("%s: %d series", name, n)
		if n >= seriesPerFamilyCap {
			t.Errorf("%s exports %d series, cap is %d", name, n, seriesPerFamilyCap)
		}
	}
	// The histogram is the widest family; pin its worst case so a bucket or
	// label change is a visible diff, not a silent drift towards the cap.
	if n := exportedSeries(metricByName(rm, "ai.request.duration")); n != (maxModelLabels+2)*len(tasks)*15 {
		t.Errorf("ai.request.duration exports %d series, want %d", n, (maxModelLabels+2)*len(tasks)*15)
	}
}

func metricWithSet(m callMeta) otelmetric.RecordOption {
	return otelmetric.WithAttributeSet(m.normalized().labels())
}

func metricWithAttrs(m callMeta, extra ...attribute.KeyValue) otelmetric.AddOption {
	return otelmetric.WithAttributes(append(m.normalized().labelList(), extra...)...)
}

func errorsAsResultCount(err error) (*ResultCountError, bool) {
	return errors.AsType[*ResultCountError](err)
}
