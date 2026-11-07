// file: core/metrics/trace_redact_test.go
// version: 1.1.0
// guid: 37db43f9-ebc0-4da7-b3e5-db3f12162464
// last-edited: 2026-11-07

package metrics

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/falkcorp/mediacore/log"
)

// Obviously fake credentials; every assertion checks they are absent.
const (
	spanFakeAPIKey = "abk_test_batcave000"
	spanFakeToken  = "tok_test_batsignal"
	spanFakeInvite = "invite_test_gotham_000"
	spanFakePass   = "pw_test_alfred_000"
)

// testSpanRedactor is a span redactor over the default Redactor, on a
// throwaway Provider's meter (for tests that call newTracerProvider directly).
func testSpanRedactor(t *testing.T) *spanRedactor {
	t.Helper()
	sr, err := newSpanRedactor(newTest(t, Options{AppID: "a"}), log.NewRedactor(nil))
	if err != nil {
		t.Fatal(err)
	}
	return sr
}

// memTracing builds a Provider whose one span exporter is an in-memory one,
// with tracing on, through the same newProvider path New takes.
func memTracing(t *testing.T, o Options) (*Provider, *tracetest.InMemoryExporter) {
	t.Helper()
	mem := tracetest.NewInMemoryExporter()
	e := grpcExporters()
	e.span = func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) { return mem, nil }
	o.Tracing = &OTLPTracing{Endpoint: "collector.example.invalid:4317"}
	p, err := newProvider(context.Background(), o, e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	if p.sdkTP == nil {
		t.Fatalf("tracing did not come up: Degraded = %v", p.Degraded())
	}
	return p, mem
}

func TestTrace_NoApikeyInSpanAttributes(t *testing.T) {
	p, mem := memTracing(t, Options{AppID: "a", MetricNamespace: "gotham", Prometheus: true, Redactor: log.NewRedactor(nil)})
	ctx := context.Background()
	tr := p.TracerProvider().Tracer("test")

	query := "apikey=" + spanFakeAPIKey + "&token=" + spanFakeToken + "&page=2"
	target := "/api/v1/books/1/cover?" + query
	invite := "/api/core/v1/auth/invites/" + spanFakeInvite

	_, s1 := tr.Start(ctx, "GET "+target, trace.WithAttributes(
		attribute.String("url.full", "https://bruce:"+spanFakePass+"@example.test"+target),
		attribute.String("url.path", "/api/v1/books/1/cover"),
		attribute.String("url.query", query),
		attribute.String("http.target", target),
		attribute.String("http.url", "http://example.test"+target),
		attribute.String("http.request.method", "GET"),
		attribute.StringSlice("http.request.header.authorization", []string{"Bearer " + spanFakeToken}),
		attribute.Int64("session_id", 4242),
	))
	s1.AddEvent("exception", trace.WithAttributes(
		attribute.String("exception.message", `Get "https://example.test/x?token=`+spanFakeToken+`": EOF`)))
	s1.End()

	_, s2 := tr.Start(ctx, "GET "+invite, trace.WithAttributes(
		attribute.String("url.full", "https://example.test"+invite),
		attribute.String("url.path", invite),
		attribute.String("url.query", ""),
		attribute.String("http.target", invite),
	))
	s2.End()

	_, s3 := tr.Start(ctx, invite)
	s3.End()

	_, plain := tr.Start(ctx, "GET /api/v1/books/{id}", trace.WithAttributes(attribute.String("url.path", "/api/v1/books/7")))
	plain.End()

	if err := p.sdkTP.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	spans := mem.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("exported %d spans, want 4", len(spans))
	}
	secrets := []string{spanFakeAPIKey, spanFakeToken, spanFakeInvite, spanFakePass}
	for _, s := range spans {
		check := func(where, v string) {
			for _, sec := range secrets {
				if strings.Contains(v, sec) {
					t.Errorf("span %q: %s carries %q: %s", s.Name, where, sec, v)
				}
			}
		}
		check("name", s.Name)
		for _, kv := range s.Attributes {
			check("attribute "+string(kv.Key), kv.Value.String())
		}
		for _, ev := range s.Events {
			for _, kv := range ev.Attributes {
				check("event attribute "+string(kv.Key), kv.Value.String())
			}
		}
		check("status", s.Status.Description)
	}
	// The harmless parts survive, and an unaffected span passes as it was.
	first := spans[0]
	if !strings.Contains(first.Name, "page=2") || !strings.Contains(first.Name, "[REDACTED]") {
		t.Errorf("first span name = %q", first.Name)
	}
	for _, kv := range first.Attributes {
		switch kv.Key {
		case "session_id":
			if kv.Value.AsString() != "[REDACTED]" {
				t.Errorf("deny-listed int attribute = %v", kv.Value.String())
			}
		case "http.request.method":
			if kv.Value.AsString() != "GET" {
				t.Errorf("harmless attribute changed: %v", kv.Value.String())
			}
		}
	}
	if last := spans[3]; last.Name != "GET /api/v1/books/{id}" || last.Attributes[0].Value.AsString() != "/api/v1/books/7" {
		t.Errorf("an unaffected span changed: %q %v", last.Name, last.Attributes)
	}

	// The counter is on core's metrics scope, labelled by kind.
	scraped := scrape(t, p)
	for _, kind := range []string{"key", "url", "path"} {
		if !strings.Contains(scraped, `gotham_metrics_span_redactions_total{kind="`+kind+`"}`) {
			t.Errorf("no span redaction series for kind %q in the scrape:\n%s", kind, scraped)
		}
	}
}

func TestNew_TracingWithoutRedactorFails(t *testing.T) {
	ctx := context.Background()
	p, err := New(ctx, Options{AppID: "a", Tracing: &OTLPTracing{Endpoint: "collector.example.invalid:4317"}})
	if err == nil {
		_ = p.Shutdown(ctx)
		t.Fatal("New with Tracing and no Redactor succeeded; spans would export credentials")
	}
	if !errors.Is(err, errNoRedactor) || !strings.Contains(err.Error(), "Redactor") {
		t.Errorf("New error = %v, want the redactor refusal", err)
	}
	// Tracing off: no exporter, nothing to redact, no redactor needed, and no
	// span redaction series.
	p, err = New(ctx, Options{AppID: "a", Prometheus: true})
	if err != nil {
		t.Fatalf("New without Tracing or Redactor: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	if strings.Contains(scrape(t, p), "span_redactions") {
		t.Error("a Provider without tracing serves a span redaction series")
	}
	// newTracerProvider refuses a nil redactor too, so no path builds an
	// unwrapped exporter.
	if _, _, err := newTracerProvider(ctx, &OTLPTracing{Endpoint: "collector.example.invalid:4317"},
		newResource("a", "v", "id", ""), grpcExporters().span, nil); !errors.Is(err, errNoRedactor) {
		t.Errorf("newTracerProvider(nil redactor) = %v, want the redactor refusal", err)
	}
}

// The status description carries error text (an http client error quotes
// the URL it fetched), so it is redacted too.
func TestRedactingExporter_StatusDescription(t *testing.T) {
	stub := tracetest.SpanStub{
		Name:   "op",
		Status: sdktrace.Status{Description: `Get "https://example.test/x?apikey=` + spanFakeAPIKey + `": EOF`},
	}
	var n tally
	got := testSpanRedactor(t).span(stub.Snapshot(), &n)
	if d := got.Status().Description; strings.Contains(d, spanFakeAPIKey) || !strings.Contains(d, "[REDACTED]") {
		t.Errorf("status description = %q", d)
	}
	if n != (tally{0, 1, 0}) {
		t.Errorf("tally = %v, want one url redaction", n)
	}
}

// The wrapper forwards Shutdown and never writes the SDK's own attribute
// slices.
func TestRedactingExporter_ForwardsAndCopies(t *testing.T) {
	mem := tracetest.NewInMemoryExporter()
	exp := testSpanRedactor(t).wrap(mem)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	ctx := context.Background()
	_, s := tp.Tracer("t").Start(ctx, "op", trace.WithAttributes(attribute.String("password", spanFakePass)))
	s.End()
	ro, ok := s.(sdktrace.ReadOnlySpan)
	if !ok {
		t.Fatal("span is not a ReadOnlySpan")
	}
	if got := ro.Attributes()[0].Value.AsString(); got != spanFakePass {
		t.Errorf("the SDK's span was written: %q", got)
	}
	if got := mem.GetSpans()[0].Attributes[0].Value.AsString(); got != "[REDACTED]" {
		t.Errorf("exported %q, want [REDACTED]", got)
	}
	if err := tp.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(mem.GetSpans()); n != 0 {
		t.Errorf("Shutdown did not reach the wrapped exporter (still %d spans held)", n)
	}
}

// Independent review M2: MAP and SLICE attribute values are recursed into,
// BYTESLICE is scanned as text, event names are redacted, and scalars follow
// Redactor.KeyDenied (a suffix alone spares a count or a flag).
func TestRedactingExporter_NestedValuesAndEventNames(t *testing.T) {
	stub := tracetest.SpanStub{
		Name: "op",
		Attributes: []attribute.KeyValue{
			attribute.Map("http.request.header", attribute.String("authorization", "Bearer "+spanFakeToken),
				attribute.String("accept", "*/*")),
			attribute.Slice("urls", attribute.StringValue("https://example.test/x?token="+spanFakeToken)),
			attribute.Slice("pairs", attribute.MapValue(attribute.String("api_key", spanFakeAPIKey))),
			attribute.ByteSlice("body", []byte(`{"password":"`+spanFakePass+`"}`)),
			attribute.Map("credentials", attribute.String("user", "bruce")),
			attribute.String("db.statement", `{"password":"`+spanFakePass+`"}`),
			attribute.Bool("cf_cookie", true),
			attribute.Int("prompt_tokens", 12),
			attribute.Int64Slice("completion_tokens", []int64{1, 2}),
			attribute.Bool("password", true),
		},
		Events: []sdktrace.Event{{Name: "fetched https://example.test/x?token=" + spanFakeToken}},
	}
	var n tally
	got := testSpanRedactor(t).span(stub.Snapshot(), &n)
	var dump strings.Builder
	for _, kv := range got.Attributes() {
		dump.WriteString(string(kv.Key) + "=" + kv.Value.String() + " ")
		if kv.Value.Type() == attribute.BYTESLICE {
			dump.WriteString(string(kv.Value.AsByteSlice()) + " ")
		}
	}
	for _, ev := range got.Events() {
		dump.WriteString(ev.Name)
	}
	for _, s := range []string{spanFakeToken, spanFakeAPIKey, spanFakePass} {
		if strings.Contains(dump.String(), s) {
			t.Errorf("%q leaked: %s", s, dump.String())
		}
	}
	want := map[string]string{"cf_cookie": "true", "prompt_tokens": "12", "completion_tokens": "[1,2]",
		"password": "[REDACTED]", "credentials": "[REDACTED]"}
	for _, kv := range got.Attributes() {
		if w, ok := want[string(kv.Key)]; ok && kv.Value.String() != w {
			t.Errorf("%s = %s, want %s", kv.Key, kv.Value.String(), w)
		}
	}
	if !strings.Contains(dump.String(), `accept`) {
		t.Errorf("harmless map entry lost: %s", dump.String())
	}
}

// Nesting deeper than maxSpanValueDepth fails closed. (The other fail-closed
// branch, a value type the exporter does not know, cannot be built outside
// the SDK: attribute.Type values come only from its constructors.)
func TestRedactingExporter_DeepValuesFailClosed(t *testing.T) {
	sr := testSpanRedactor(t)
	v := attribute.StringValue("Hush")
	for range maxSpanValueDepth + 2 {
		v = attribute.SliceValue(v)
	}
	if got, k := sr.value("titles", v, 0); k&log.RedactKey == 0 || strings.Contains(got.String(), "Hush") {
		t.Errorf("deep value = %s (%v), want failed closed", got.String(), k)
	}
	shallow := attribute.SliceValue(attribute.StringValue("Hush"))
	if got, k := sr.value("titles", shallow, 0); k != 0 || got.String() != shallow.String() {
		t.Errorf("shallow harmless value = %s (%v), want unchanged", got.String(), k)
	}
}
