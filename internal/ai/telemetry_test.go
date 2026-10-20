// file: internal/ai/telemetry_test.go
// version: 1.4.1
// guid: 5d0b3f7e-6c1a-4e48-9a52-0f1b7c9d2e31
// last-edited: 2026-10-20

package ai

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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
	// The named-model set is process state like the instruments: a test must
	// start from a known one or its model labels depend on test order. The
	// models the site tests send are "configured" here, the way a real run's
	// come from settings; an unconfigured model would label as "other".
	modelLabels.reset()
	modelLabels.setConfigured(
		modelPair{providerLocal, "test-model"},
		modelPair{providerLocal, "embed-model"},
	)
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
		maps.Copy(m, base)
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
		check("attribute "+string(kv.Key), kv.Value.String())
	}
	for _, ev := range sp.Events() {
		check("event name", ev.Name)
		for _, kv := range ev.Attributes {
			check("event "+ev.Name+" attribute "+string(kv.Key), kv.Value.String())
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
					got[string(kv.Key)] = kv.Value.String()
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
			got[string(kv.Key)] = kv.Value.String()
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

// histogramSum is the sum of every observation of the named histogram whose
// attributes include all of want.
func histogramSum(rm metricdata.ResourceMetrics, name string, want map[string]string) float64 {
	m := metricByName(rm, name)
	if m == nil {
		return 0
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		return 0
	}
	var total float64
	for _, dp := range h.DataPoints {
		if attrsMatch(dp.Attributes, want) {
			total += dp.Sum
		}
	}
	return total
}

// SF1: a call whose context ends while the SDK waits to retry a 429 is a
// throttle, not a timeout or a cancellation. The 429 arrived; the context
// error is only how the wait was cut short. It counts as one error request and
// one rate_limit failure, with the attempt's own duration (to its headers) and
// not the backoff sleep, and the context error is not a second request.
func TestContextEndedDuringSDKBackoffStillCountsTheThrottle(t *testing.T) {
	const ctxLife = 600 * time.Millisecond
	for name, newCtx := range map[string]func() (context.Context, context.CancelFunc){
		"deadline": func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), ctxLife)
		},
		"cancel": func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(ctxLife, cancel)
			return ctx, cancel
		},
	} {
		t.Run(name, func(t *testing.T) {
			tel := installAITelemetry(t)
			srv := newStubEndpoint(t, "", func(_ int64, w http.ResponseWriter) bool {
				// A wait far longer than the context lives: the SDK is asleep
				// when the context ends.
				w.Header().Set("Retry-After-Ms", "10000")
				http.Error(w, `{"error":{"message":"slow down","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, http.StatusTooManyRequests)
				return true
			})
			ctx, cancel := newCtx()
			defer cancel()
			if _, err := newTestParser(t, srv).ParseFilename(ctx, "x"); err == nil {
				t.Fatal("ParseFilename succeeded against a 429 and a dead context")
			}
			if got := srv.requests.Load(); got != 1 {
				t.Fatalf("server saw %d requests, want 1 (the context ended in the SDK's backoff)", got)
			}
			rm := tel.collect(t)
			task := map[string]string{"task": TaskFilenameParse}
			if got := counterSum(rm, "ai.requests", task); got != 1 {
				t.Errorf("ai.requests = %d, want 1 (no second request for the context error)", got)
			}
			if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": "error"}); got != 1 {
				t.Errorf("ai.requests{outcome=error} = %d, want 1 (the 429), not timeout or canceled", got)
			}
			if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskFilenameParse, "reason": "rate_limit"}); got != 1 {
				t.Errorf("ai.failures{reason=rate_limit} = %d, want 1", got)
			}
			if got := counterSum(rm, "ai.failures", task); got != 1 {
				t.Errorf("ai.failures = %d in total, want 1 (no timeout failure besides the throttle)", got)
			}
			if got := histogramCount(rm, "ai.request.duration", task); got != 1 {
				t.Fatalf("duration observations = %d, want 1", got)
			}
			if sum := histogramSum(rm, "ai.request.duration", task); sum >= ctxLife.Seconds()/2 {
				t.Errorf("duration = %.3fs, want the attempt's own time, well under the %s the SDK slept", sum, ctxLife)
			}
			for _, sp := range tel.spans.Ended() {
				if sp.Name() != "ai."+TaskFilenameParse {
					continue
				}
				got := map[string]string{}
				for _, kv := range sp.Attributes() {
					got[string(kv.Key)] = kv.Value.String()
				}
				if got["ai.outcome"] != outcomeError || got["ai.failure_reason"] != reasonRateLimit {
					t.Errorf("span classification = %v, want outcome=error reason=rate_limit", got)
				}
				if got["http.response.status_code"] != "429" {
					t.Errorf("span http.response.status_code = %q, want 429 (the attempt the call ended in)", got["http.response.status_code"])
				}
			}
		})
	}
}

// spanAttrsFor returns the attributes of the one ended span named name.
func spanAttrsFor(t *testing.T, tel *aiTelemetry, name string) map[string]string {
	t.Helper()
	var found []map[string]string
	for _, sp := range tel.spans.Ended() {
		if sp.Name() != name {
			continue
		}
		got := map[string]string{}
		for _, kv := range sp.Attributes() {
			got[string(kv.Key)] = kv.Value.String()
		}
		found = append(found, got)
	}
	if len(found) != 1 {
		t.Fatalf("%d spans named %s, want 1", len(found), name)
	}
	return found[0]
}

// stalledResponse answers with status, the given headers and the first bytes
// of a body that promises more, then holds the connection until release is
// closed or the client goes away. The SDK is left reading the body when the
// caller's context ends.
func stalledResponse(release <-chan struct{}, started func(), status int, hdr map[string]string) func(int64, http.ResponseWriter) bool {
	return func(_ int64, w http.ResponseWriter) bool {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"par`))
		w.(http.Flusher).Flush()
		started()
		<-release
		return true
	}
}

// onceSignal returns a func that closes a channel the first time it is called,
// and the channel.
func onceSignal() (func(), <-chan struct{}) {
	ch := make(chan struct{})
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }, ch
}

// A request cut off by a context that was cancelled with a cause that is not a
// context error is still the caller giving up: canceled, no failure. net/http
// returns context.Cause(ctx) for such a request, so the transport error the
// attempt records is not a context error even though the SDK's own error is.
// errgroup.WithContext cancels with the first failing callback's error as the
// cause, so a sibling failure takes exactly this path.
func TestCancelWithNonContextCauseIsStillCanceled(t *testing.T) {
	cause := errors.New("scan stand-down lease lost")
	for name, run := range map[string]func(t *testing.T, p *OpenAIParser, arrived <-chan struct{}) error{
		"WithCancelCause": func(t *testing.T, p *OpenAIParser, arrived <-chan struct{}) error {
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			go func() {
				<-arrived
				time.Sleep(100 * time.Millisecond)
				cancel(cause)
			}()
			_, err := p.ParseFilename(ctx, "x")
			return err
		},
		"errgroup sibling fails": func(t *testing.T, p *OpenAIParser, arrived <-chan struct{}) error {
			g, gctx := errgroup.WithContext(context.Background())
			var callErr error
			g.Go(func() error {
				_, callErr = p.ParseFilename(gctx, "x")
				return nil
			})
			g.Go(func() error {
				<-arrived
				time.Sleep(100 * time.Millisecond)
				return errors.New("sibling failed")
			})
			_ = g.Wait()
			return callErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			tel := installAITelemetry(t)
			started, arrived := onceSignal()
			release := make(chan struct{})
			srv := newStubEndpoint(t, "", func(_ int64, w http.ResponseWriter) bool {
				started()
				<-release
				return true
			})
			t.Cleanup(func() { close(release) })

			if err := run(t, newTestParser(t, srv), arrived); err == nil {
				t.Fatal("ParseFilename succeeded though its context was cancelled")
			}
			if got := srv.requests.Load(); got != 1 {
				t.Fatalf("server saw %d requests, want 1", got)
			}
			rm := tel.collect(t)
			task := map[string]string{"task": TaskFilenameParse}
			if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": "canceled"}); got != 1 {
				t.Errorf("ai.requests{outcome=canceled} = %d, want 1", got)
			}
			if got := counterSum(rm, "ai.requests", task); got != 1 {
				t.Errorf("ai.requests = %d in total, want 1", got)
			}
			if got := counterSum(rm, "ai.failures", task); got != 0 {
				t.Errorf("ai.failures = %d, want 0 (the caller gave up, the backend did nothing wrong)", got)
			}
			if attrs := spanAttrsFor(t, tel, "ai."+TaskFilenameParse); attrs["ai.outcome"] != outcomeCanceled {
				t.Errorf("span ai.outcome = %q, want %q", attrs["ai.outcome"], outcomeCanceled)
			}
		})
	}
}

// A context that ends while the SDK is reading a response it does not retry is
// not a backoff: the call is a timeout (or cancellation), counted once, and the
// response's status is not mistaken for a throttle the SDK was waiting out.
// Covers a 2xx, a 4xx the SDK does not retry, and (mirroring the SDK, which
// lets x-should-retry decide over the status) a 429 marked x-should-retry:false.
func TestContextEndedReadingAnUnretriedResponseIsATimeout(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		hdr    map[string]string
	}{
		{"200", http.StatusOK, nil},
		{"400", http.StatusBadRequest, nil},
		{"429 with x-should-retry false", http.StatusTooManyRequests, map[string]string{"x-should-retry": "false", "Retry-After-Ms": "10000"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tel := installAITelemetry(t)
			started, _ := onceSignal()
			release := make(chan struct{})
			srv := newStubEndpoint(t, "", stalledResponse(release, started, c.status, c.hdr))
			t.Cleanup(func() { close(release) })

			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			if _, err := newTestParser(t, srv).ParseFilename(ctx, "x"); err == nil {
				t.Fatal("ParseFilename succeeded against a stalled body")
			}
			if got := srv.requests.Load(); got != 1 {
				t.Fatalf("server saw %d requests, want 1", got)
			}
			rm := tel.collect(t)
			task := map[string]string{"task": TaskFilenameParse}
			if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": "timeout"}); got != 1 {
				t.Errorf("ai.requests{outcome=timeout} = %d, want 1", got)
			}
			if got := counterSum(rm, "ai.requests", task); got != 1 {
				t.Errorf("ai.requests = %d in total, want 1", got)
			}
			if got := counterSum(rm, "ai.failures", map[string]string{"task": TaskFilenameParse, "reason": "timeout"}); got != 1 {
				t.Errorf("ai.failures{reason=timeout} = %d, want 1", got)
			}
			if got := counterSum(rm, "ai.failures", task); got != 1 {
				t.Errorf("ai.failures = %d in total, want 1", got)
			}
			if sum := histogramSum(rm, "ai.request.duration", task); sum < 0.3 {
				t.Errorf("duration = %.3fs, want the time spent reading until the deadline (about 0.4s)", sum)
			}
		})
	}
}

// The SDK retries any response marked x-should-retry:true, whatever its status,
// so a context that ends while it waits is a backoff on that attempt: one error
// request with the response's reason and status on the span, not a timeout.
// A 400 is used because its status alone is never retried.
func TestContextEndedInBackoffOfAnXShouldRetryResponse(t *testing.T) {
	tel := installAITelemetry(t)
	srv := newStubEndpoint(t, "", func(_ int64, w http.ResponseWriter) bool {
		w.Header().Set("x-should-retry", "true")
		w.Header().Set("Retry-After-Ms", "10000")
		http.Error(w, `{"error":{"message":"try again","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := newTestParser(t, srv).ParseFilename(ctx, "x"); err == nil {
		t.Fatal("ParseFilename succeeded against a 400 and a dead context")
	}
	if got := srv.requests.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want 1 (the context ended in the SDK's backoff)", got)
	}
	rm := tel.collect(t)
	task := map[string]string{"task": TaskFilenameParse}
	if got := counterSum(rm, "ai.requests", map[string]string{"task": TaskFilenameParse, "outcome": "error"}); got != 1 {
		t.Errorf("ai.requests{outcome=error} = %d, want 1 (the 400 the SDK was retrying)", got)
	}
	if got := counterSum(rm, "ai.requests", task); got != 1 {
		t.Errorf("ai.requests = %d in total, want 1", got)
	}
	if got := counterSum(rm, "ai.failures", task); got != 1 {
		t.Errorf("ai.failures = %d in total, want 1 (no timeout failure)", got)
	}
	attrs := spanAttrsFor(t, tel, "ai."+TaskFilenameParse)
	if attrs["ai.outcome"] != outcomeError || attrs["http.response.status_code"] != "400" {
		t.Errorf("span = %v, want outcome=error with http.response.status_code=400", attrs)
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
	long := strings.Repeat("m", 64)
	for _, c := range []struct{ in, want string }{
		{"gpt-5-mini", "gpt-5-mini"},
		// An empty model is "other", not a label of its own: see maxModelLabels.
		{"", "other"},
		{"two words", "other"},
		{"new\nline", "other"},
		{long, long},
		{strings.Repeat("m", 65), "other"},
	} {
		modelLabels.reset()
		modelLabels.setConfigured(modelPair{providerOpenAI, "gpt-5-mini"}, modelPair{providerOpenAI, long})
		if got := (callMeta{model: c.in}).normalized().model; got != c.want {
			t.Errorf("model %q folded to %q, want %q", c.in, got, c.want)
		}
	}
	modelLabels.reset()
	t.Cleanup(modelLabels.reset)
}

// The model label names the configured models, always, whichever call comes
// first, and folds every other model to "other" (owner decision 2026-10-11).
func TestModelLabelNamesConfiguredModels(t *testing.T) {
	t.Cleanup(modelLabels.reset)
	configured := []modelPair{
		{providerOpenAI, "gpt-5-mini"},
		{providerOpenAI, "text-embedding-3-large"},
		{providerLocal, "qwen2.5:7b-instruct"},
		{providerLocal, "bge-m3"},
		{providerLocal, "gpt-5-mini"},
	}
	label := func(p modelPair) string { return (callMeta{provider: p.provider, model: p.model}).normalized().model }

	// Configured models are named in every call order. The unconfigured ones
	// that arrive before them must not take their slots.
	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 0, 4, 1, 3}} {
		modelLabels.reset()
		modelLabels.setConfigured(configured...)
		for i := range 20 {
			if got := label(modelPair{providerLocal, "tried-" + string(rune('a'+i))}); got != modelOther {
				t.Fatalf("an unconfigured model = %q, want %q", got, modelOther)
			}
		}
		for _, i := range order {
			if got := label(configured[i]); got != configured[i].model {
				t.Errorf("order %v: configured %v labelled %q, want its name", order, configured[i], got)
			}
		}
	}

	// A configured name is per provider: gpt-5-mini sent by the local backend is
	// not the OpenAI pair.
	modelLabels.reset()
	modelLabels.setConfigured(modelPair{providerOpenAI, "gpt-5-mini"})
	if got := label(modelPair{providerLocal, "gpt-5-mini"}); got != modelOther {
		t.Errorf("a configured name under another provider = %q, want %q", got, modelOther)
	}
}

// The same settings name the same models on every start, independent of which
// call arrives first: two fresh label sets over one config, fed the calls in
// opposite orders, agree on every label.
func TestModelLabelRestartStability(t *testing.T) {
	cfg := &config.Config{
		OpenAIAPIKey:        "k",
		FilenameParseModel:  "fn-model",
		MetadataReviewModel: "review-model",
		CoverArtModel:       "fn-model",
		Dedup:               config.DedupConfig{ReviewModel: "dedup-model"},
		Embedding:           config.EmbeddingConfig{Enabled: true, Model: "emb-model"},
	}
	calls := []modelPair{
		{providerOpenAI, "zzz-unconfigured"}, {providerOpenAI, "emb-model"}, {providerOpenAI, "fn-model"},
		{providerOpenAI, "dedup-model"}, {providerOpenAI, "review-model"}, {providerOpenAI, "aaa-unconfigured"},
	}
	// dedup.review_model is set but no ai_* metric is ever recorded for it
	// (dedup reviews go through the Batch API), so it is not a configured
	// metric model and must label "other" like any unconfigured one.
	run := func(order []modelPair) map[modelPair]string {
		s := newModelLabelSet()
		s.source.Store(&modelSource{generation: func() uint64 { return 1 }, configured: func() []modelPair { return configuredModelPairs(cfg) }})
		got := map[modelPair]string{}
		for _, p := range order {
			got[p] = s.admit(p.provider, p.model)
		}
		return got
	}
	reversed := slices.Clone(calls)
	slices.Reverse(reversed)
	a, b := run(calls), run(reversed)
	for _, p := range calls {
		if a[p] != b[p] {
			t.Errorf("%v: %q on one start, %q on another", p, a[p], b[p])
		}
	}
	for _, name := range []string{"fn-model", "review-model", "emb-model"} {
		if a[modelPair{providerOpenAI, name}] != name {
			t.Errorf("configured %q labelled %q", name, a[modelPair{providerOpenAI, name}])
		}
	}
	for _, name := range []string{"zzz-unconfigured", "aaa-unconfigured", "dedup-model"} {
		if a[modelPair{providerOpenAI, name}] != modelOther {
			t.Errorf("unconfigured %q labelled %q", name, a[modelPair{providerOpenAI, name}])
		}
	}
}

// A settings change names the newly configured model from the next call on,
// and the one it replaced keeps its name until restart (its series is still
// exported, see maxModelLabels).
func TestModelLabelSettingsChange(t *testing.T) {
	t.Cleanup(modelLabels.reset)
	modelLabels.reset()
	old, next := modelPair{providerOpenAI, "model-old"}, modelPair{providerOpenAI, "model-new"}
	label := func(p modelPair) string { return (callMeta{provider: p.provider, model: p.model}).normalized().model }

	modelLabels.setConfigured(old)
	if got := label(old); got != old.model {
		t.Fatalf("configured model = %q", got)
	}
	if got := label(next); got != modelOther {
		t.Fatalf("not yet configured model = %q, want %q", got, modelOther)
	}
	modelLabels.setConfigured(next) // the admin saved a different model
	if got := label(next); got != next.model {
		t.Errorf("newly configured model = %q, want its name", got)
	}
	if got := label(old); got != old.model {
		t.Errorf("replaced model = %q, want it to keep its name until restart", got)
	}
}

// The named set never exceeds maxModelLabels over the process life, even when
// settings keep changing: a model configured after the cap is full is "other"
// until restart, and a model that was already named is not displaced for it.
func TestModelLabelCapHoldsAcrossSettingsChanges(t *testing.T) {
	t.Cleanup(modelLabels.reset)
	modelLabels.reset()
	label := func(m string) string { return (callMeta{provider: providerLocal, model: m}).normalized().model }
	for i := range maxModelLabels {
		modelLabels.setConfigured(modelPair{providerLocal, "model-" + string(rune('a'+i))})
		if got := label("model-" + string(rune('a'+i))); got != "model-"+string(rune('a'+i)) {
			t.Fatalf("model %d under the cap = %q", i, got)
		}
	}
	modelLabels.setConfigured(modelPair{providerLocal, "model-over"})
	if got := label("model-over"); got != modelOther {
		t.Errorf("a configured model past the cap = %q, want %q", got, modelOther)
	}
	if got := label("model-a"); got != "model-a" {
		t.Errorf("a named model lost its name when the cap was exceeded: %q", got)
	}
	// More pairs in one config than fit: the first ones in list order win,
	// whatever order calls arrive in.
	modelLabels.reset()
	var many []modelPair
	for i := range maxModelLabels + 3 {
		many = append(many, modelPair{providerLocal, "m-" + string(rune('a'+i))})
	}
	modelLabels.setConfigured(many...)
	for i, p := range slices.Backward(many) {
		want := p.model
		if i >= maxModelLabels {
			want = modelOther
		}
		if got := label(p.model); got != want {
			t.Errorf("configured pair %d of %d = %q, want %q", i, len(many), got, want)
		}
	}
}

// Every live-config path that changes a model setting moves config.Generation,
// which is what makes the set re-sync with no hook at the call sites.
func TestModelLabelFollowsLiveConfig(t *testing.T) {
	t.Cleanup(modelLabels.reset)
	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	modelLabels.reset() // the live config is the source again

	config.Mutate(func(c *config.Config) {
		c.OpenAIAPIKey = "k"
		c.OpenAIBaseURL = ""
		c.FilenameParseModel = "live-model-one"
		c.MetadataReviewModel = "live-model-one"
		c.CoverArtModel = "live-model-one"
	})
	label := func(m string) string { return (callMeta{provider: providerOpenAI, model: m}).normalized().model }
	if got := label("live-model-one"); got != "live-model-one" {
		t.Errorf("model in the live config = %q, want its name", got)
	}
	if got := label("live-model-two"); got != modelOther {
		t.Errorf("model not in the live config = %q, want %q", got, modelOther)
	}
	config.Mutate(func(c *config.Config) { c.FilenameParseModel = "live-model-two" })
	if got := label("live-model-two"); got != "live-model-two" {
		t.Errorf("model saved to the live config = %q, want its name", got)
	}
}

// Labels are asked for from many goroutines while settings change; -race checks
// the set's locking and that a pair is never both named and "other" for the
// same configuration.
func TestModelLabelConcurrentUse(t *testing.T) {
	t.Cleanup(modelLabels.reset)
	modelLabels.reset()
	modelLabels.setConfigured(modelPair{providerLocal, "model-a"}, modelPair{providerLocal, "model-b"})
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				if g == 0 && i%50 == 0 {
					modelLabels.setConfigured(modelPair{providerLocal, "model-a"}, modelPair{providerLocal, "model-b"})
				}
				if got := (callMeta{provider: providerLocal, model: "model-a"}).normalized().model; got != "model-a" {
					t.Errorf("configured model labelled %q", got)
					return
				}
				if got := (callMeta{provider: providerLocal, model: "model-z"}).normalized().model; got != modelOther {
					t.Errorf("unconfigured model labelled %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// The span carries the real model name: the label folds unconfigured models,
// and a trace attribute is not a series.
func TestSpanModelIsNotCapped(t *testing.T) {
	modelLabels.reset()
	t.Cleanup(modelLabels.reset)
	modelLabels.setConfigured(modelPair{providerLocal, "model-a"})
	spanModel := func(m callMeta) string {
		for _, kv := range m.normalized().spanAttributes() {
			if kv.Key == "ai.model" {
				return kv.Value.AsString()
			}
		}
		return ""
	}
	if got := spanModel(callMeta{provider: providerLocal, model: "model-extra"}); got != "model-extra" {
		t.Errorf("span ai.model for an unconfigured model = %q, want the real name", got)
	}
	if got := (callMeta{provider: providerLocal, model: "model-extra"}).normalized().model; got != modelOther {
		t.Errorf("label for an unconfigured model = %q, want %q", got, modelOther)
	}
	if got := spanModel(callMeta{provider: providerLocal, model: "free text here"}); got != modelOther {
		t.Errorf("span ai.model for free text = %q, want %q (the whitespace fold still applies)", got, modelOther)
	}
	// An empty model is labelled "other" but the span still says it was empty.
	if got := spanModel(callMeta{provider: providerLocal}); got != modelUnknown {
		t.Errorf("span ai.model for an empty model = %q, want %q", got, modelUnknown)
	}
	if got := (callMeta{provider: providerLocal}).normalized().model; got != modelOther {
		t.Errorf("label for an empty model = %q, want %q", got, modelOther)
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
// reach, with far more models than the label can name, and counts the exported
// series of each family against C2.
//
// Pool routing sends each endpoint's own model, so the number of models is the
// size of a growable pool, not a constant. The model label therefore names at
// most maxModelLabels distinct configured (provider, model) pairs over the
// process life and folds every other model, including an empty one, to "other"
// (see modelLabelSet). The worst case is (maxModelLabels named + 2 "other",
// one per provider) = 7 pairs x 9 tasks, and the 15 series per combination of
// the 12-bucket histogram make that 945. The test reaches that worst case on
// purpose: it configures exactly the cap under each provider's turn, then keeps
// changing settings to push more models in, and sends 40 unconfigured models
// and the empty model under both providers. It fails if the bucket list, the
// label set or maxModelLabels grows past the cap, and if anything but the
// named pairs and "other" ever becomes a label value.
func TestSeriesCardinalityBound(t *testing.T) {
	tel := installAITelemetry(t)
	ins := aiInstrumentsFor()
	ctx := context.Background()
	tasks := []string{TaskFilenameParse, TaskAudiobookParse, TaskBatchParse, TaskCoverArt, TaskCoverText,
		TaskAuthorDedupReview, TaskAuthorDiscovery, TaskMetadataReview, TaskEmbed}
	// maxModelLabels pairs, split over both providers, then settings changes
	// that configure more: none of those may add a name.
	var named []modelPair
	for i := range maxModelLabels {
		provider := providerOpenAI
		if i%2 == 1 {
			provider = providerLocal
		}
		named = append(named, modelPair{provider, "configured-" + string(rune('a'+i))})
	}
	modelLabels.setConfigured(named...)
	for _, p := range named {
		(callMeta{provider: p.provider, model: p.model}).normalized() // sync: the cap is now full
	}
	// Settings changes that configure more models once the cap is full. They
	// are recorded below like the named ones: a mutant that lets them take a
	// name must be visible as extra series, not only as a different label
	// from normalized().
	var later []modelPair
	for i := range 4 {
		modelLabels.setConfigured(modelPair{providerOpenAI, "configured-later-" + string(rune('a'+i))},
			modelPair{providerLocal, "configured-later-" + string(rune('a'+i))})
		for _, provider := range []string{providerOpenAI, providerLocal} {
			p := modelPair{provider, "configured-later-" + string(rune('a'+i))}
			later = append(later, p)
			if got := (callMeta{provider: p.provider, model: p.model}).normalized().model; got != modelOther {
				t.Errorf("%v configured past the cap was named %q", p, got)
			}
		}
	}
	const modelsOffered = 40 // a pool far larger than any real one
	record := func(m callMeta) {
		for _, task := range tasks {
			m.task = task
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
	for _, provider := range []string{providerOpenAI, providerLocal} {
		record(callMeta{provider: provider}) // the empty model
		for i := range modelsOffered {
			record(callMeta{provider: provider, model: "model-" + string(rune('a'+i%26)) + string(rune('a'+i/26))})
		}
	}
	for _, p := range append(slices.Clone(named), later...) {
		record(callMeta{provider: p.provider, model: p.model})
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
	// And the label values themselves: only the configured names that fit, and
	// "other".
	allowed := map[string]bool{modelOther: true}
	for _, p := range named {
		allowed[p.model] = true
	}
	for _, dp := range metricByName(rm, "ai.request.duration").Data.(metricdata.Histogram[float64]).DataPoints {
		if v, _ := dp.Attributes.Value(attrModel); !allowed[v.AsString()] {
			t.Errorf("model label value %q is neither a configured name nor %q", v.AsString(), modelOther)
		}
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
