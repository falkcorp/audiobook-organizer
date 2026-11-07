// file: core/metrics/trace_redact.go
// version: 1.0.0
// guid: 7188af3d-51e5-4732-bd81-f3468d1c7288
// last-edited: 2026-11-07

package metrics

import (
	"context"
	"errors"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/falkcorp/mediacore/log"
)

// errNoRedactor is New's refusal of tracing without a redactor (05-metrics
// §8: "Redactor nil while tracing is on | fail-closed (boot)").
var errNoRedactor = errors.New("metrics: Options.Tracing is set but Options.Redactor is nil: spans would export credentials unredacted")

// spanRedactionKinds are the kind label values of the span redaction counter
// (05-metrics §11): key, url, path.
var spanRedactionKinds = [...]log.RedactionKind{log.RedactKey, log.RedactURL, log.RedactPath}

// spanRedactor is the stateless half of the redacting span exporter: the
// immutable Redactor and the counter. One per Provider, built before the
// TracerProvider so the batcher never sees it change.
type spanRedactor struct {
	r     *log.Redactor
	count metric.Int64Counter
	kinds [len(spanRedactionKinds)]metric.AddOption
}

// newSpanRedactor builds the redactor half for p and creates
// <namespace>.metrics.span_redactions (Prometheus
// <namespace>_metrics_span_redactions_total{kind}) on core's metrics meter, the
// meter a Core("metrics") scope would use, so the instrument has one identity
// whichever creates it first. It is created only when tracing is on: a
// Provider without tracing has no span to redact and serves no such series.
func newSpanRedactor(p *Provider, r *log.Redactor) (*spanRedactor, error) {
	if r == nil {
		return nil, errNoRedactor
	}
	c, err := p.mp.Meter(p.ns+".metrics").Int64Counter(p.ns+".metrics.span_redactions",
		metric.WithUnit("{redaction}"),
		metric.WithDescription("Span names and attribute values the redacting span exporter replaced before export, by kind (key, url, path)."))
	if err != nil {
		return nil, err
	}
	sr := &spanRedactor{r: r, count: c}
	for i, k := range spanRedactionKinds {
		sr.kinds[i] = metric.WithAttributeSet(attribute.NewSet(attribute.String("kind", k.String())))
	}
	return sr, nil
}

// wrap returns next behind the redaction. It is the only way newTracerProvider
// gets a span exporter into a TracerProvider.
func (sr *spanRedactor) wrap(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return &redactingExporter{next: next, sr: sr}
}

// redactingExporter is the redacting span exporter of 05-metrics §3 (SEC-22).
// Before export, each span's name and every string attribute go through
// Redactor.StringKinds (the key deny-list, URL handling for URLKeys() and
// URL-shaped values); a non-string attribute under a deny-listed key becomes
// "[REDACTED]". The same applies to event and link attributes and to the
// status description, which carry error text (an http client error quotes the
// URL it fetched). A span with nothing to redact is passed on as it is.
type redactingExporter struct {
	next sdktrace.SpanExporter
	sr   *spanRedactor
}

func (e *redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	out := make([]sdktrace.ReadOnlySpan, len(spans))
	var n tally
	for i, s := range spans {
		out[i] = e.sr.span(s, &n)
	}
	for i, c := range n {
		if c > 0 {
			e.sr.count.Add(ctx, c, e.sr.kinds[i])
		}
	}
	return e.next.ExportSpans(ctx, out)
}

func (e *redactingExporter) Shutdown(ctx context.Context) error { return e.next.Shutdown(ctx) }

// tally counts redacted values per spanRedactionKinds entry.
type tally [len(spanRedactionKinds)]int64

func (t *tally) add(k log.RedactionKind) {
	for i, b := range spanRedactionKinds {
		if k&b != 0 {
			t[i]++
		}
	}
}

// redactedSpan overrides what the redaction changed. ReadOnlySpan has an
// unexported method, so a span is rewritten by embedding the original.
type redactedSpan struct {
	sdktrace.ReadOnlySpan
	name   string
	attrs  []attribute.KeyValue
	events []sdktrace.Event
	links  []sdktrace.Link
	status sdktrace.Status
}

func (s *redactedSpan) Name() string                     { return s.name }
func (s *redactedSpan) Attributes() []attribute.KeyValue { return s.attrs }
func (s *redactedSpan) Events() []sdktrace.Event         { return s.events }
func (s *redactedSpan) Links() []sdktrace.Link           { return s.links }
func (s *redactedSpan) Status() sdktrace.Status          { return s.status }

// span returns s redacted, or s itself when nothing in it needed redacting.
func (sr *spanRedactor) span(s sdktrace.ReadOnlySpan, n *tally) sdktrace.ReadOnlySpan {
	rs := &redactedSpan{ReadOnlySpan: s, name: s.Name(), status: s.Status()}
	changed := false
	if name, k := sr.r.StringKinds("", rs.name); k != 0 {
		rs.name, changed = name, true
		n.add(k)
	}
	var ch bool
	if rs.attrs, ch = sr.attrs(s.Attributes(), n); ch {
		changed = true
	}
	rs.events = s.Events()
	cloned := false
	for i, ev := range rs.events {
		if a, ch := sr.attrs(ev.Attributes, n); ch {
			if !cloned {
				rs.events, cloned = slices.Clone(rs.events), true
			}
			rs.events[i].Attributes = a
			changed = true
		}
	}
	rs.links = s.Links()
	cloned = false
	for i, l := range rs.links {
		if a, ch := sr.attrs(l.Attributes, n); ch {
			if !cloned {
				rs.links, cloned = slices.Clone(rs.links), true
			}
			rs.links[i].Attributes = a
			changed = true
		}
	}
	if d, k := sr.r.StringKinds("", rs.status.Description); k != 0 {
		rs.status.Description, changed = d, true
		n.add(k)
	}
	if !changed {
		return s
	}
	return rs
}

// attrs returns kvs redacted and whether anything changed; kvs itself is never
// written (the SDK's span owns it).
func (sr *spanRedactor) attrs(kvs []attribute.KeyValue, n *tally) ([]attribute.KeyValue, bool) {
	var out []attribute.KeyValue
	for i, kv := range kvs {
		red, k := sr.attr(kv)
		if k == 0 {
			continue
		}
		if out == nil {
			out = slices.Clone(kvs)
		}
		out[i] = red
		n.add(k)
	}
	if out == nil {
		return kvs, false
	}
	return out, true
}

// attr redacts one attribute and reports what it replaced.
func (sr *spanRedactor) attr(kv attribute.KeyValue) (attribute.KeyValue, log.RedactionKind) {
	key := string(kv.Key)
	switch kv.Value.Type() {
	case attribute.STRING:
		if s, k := sr.r.StringKinds(key, kv.Value.AsString()); k != 0 {
			return attribute.String(key, s), k
		}
	case attribute.STRINGSLICE:
		in := kv.Value.AsStringSlice()
		var kinds log.RedactionKind
		for i, v := range in {
			s, k := sr.r.StringKinds(key, v)
			in[i], kinds = s, kinds|k
		}
		if kinds != 0 {
			return attribute.StringSlice(key, in), kinds
		}
	default:
		// A number or bool carries no URL; only its key can make it secret.
		if s, k := sr.r.StringKinds(key, ""); k&log.RedactKey != 0 {
			return attribute.String(key, s), k
		}
	}
	return kv, 0
}
