// file: core/internal/httpmw/telemetry.go
// version: 1.0.0
// guid: 8eea62e4-d3d4-438b-8c9a-12b63a3dcf84
// last-edited: 2026-11-08

package httpmw

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

// durationBuckets is the semconv advice for http.server.request.duration,
// given explicitly at creation (05-http-api §4.2 stage 2).
func durationBuckets() []float64 {
	return []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
}

// instruments are stage 2's three semconv histograms, created once on the
// "httpapi" meter, and core's own http families on the "<namespace>.http"
// scope (§11). A nil instrument records nothing.
type instruments struct {
	duration  *httpconv.ServerRequestDuration
	reqSize   *httpconv.ServerRequestBodySize
	respSize  *httpconv.ServerResponseBodySize
	inflight  metric.Int64UpDownCounter
	panics    metric.Int64Counter
	limited   metric.Int64Counter
	crossOrig metric.Int64Counter
	refused   metric.Int64Counter
}

func (in *instruments) init(d Deps) error {
	var errs []error
	if d.Meter != nil {
		dur, err := httpconv.NewServerRequestDuration(d.Meter, metric.WithExplicitBucketBoundaries(durationBuckets()...))
		errs = append(errs, err)
		in.duration = &dur
		req, err := httpconv.NewServerRequestBodySize(d.Meter)
		errs = append(errs, err)
		in.reqSize = &req
		resp, err := httpconv.NewServerResponseBodySize(d.Meter)
		errs = append(errs, err)
		in.respSize = &resp
	}
	if s := d.Metrics; s != nil {
		m, p := s.Meter(), s.Prefix()
		var err error
		in.inflight, err = m.Int64UpDownCounter(p+".inflight_requests", metric.WithUnit("{request}"),
			metric.WithDescription("Requests the HTTP chain is serving now, by owner (05-http-api §11)."))
		errs = append(errs, err)
		in.panics, err = m.Int64Counter(p+".panics", metric.WithUnit("{panic}"),
			metric.WithDescription("Handler panics stage 3 recovered, by owner and route."))
		errs = append(errs, err)
		in.limited, err = m.Int64Counter(p+".rate_limited", metric.WithUnit("{request}"),
			metric.WithDescription("Requests stage 10 refused with 429, by rate class."))
		errs = append(errs, err)
		in.crossOrig, err = m.Int64Counter(p+".cross_origin_rejected", metric.WithUnit("{request}"),
			metric.WithDescription("Unsafe cross-origin requests stage 7 refused, by owner."))
		errs = append(errs, err)
		in.refused, err = m.Int64Counter(p+".peer_refused", metric.WithUnit("{request}"),
			metric.WithDescription("Requests stage 0 refused because the peer is outside the allow-list."))
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// enter counts the request in flight under owner, once per request: the
// router calls it when it hands the request to an owner.
func (c *Chain) enter(ctx context.Context, st *State, owner string) {
	if c.inst.inflight == nil || !st.inflight.CompareAndSwap(nil, &owner) {
		return
	}
	c.inst.inflight.Add(ctx, 1, metric.WithAttributes(attribute.String("owner", owner)))
}

// leave removes what enter counted.
func (c *Chain) leave(ctx context.Context, st *State) {
	if p := st.inflight.Load(); p != nil && c.inst.inflight != nil {
		c.inst.inflight.Add(ctx, -1, metric.WithAttributes(attribute.String("owner", *p)))
	}
}

func (in *instruments) peerRefused(ctx context.Context) {
	if in.refused != nil {
		in.refused.Add(ctx, 1)
	}
}

func (in *instruments) panicked(ctx context.Context, owner, route string) {
	if in.panics != nil {
		if route == "" {
			route = "unmatched"
		}
		in.panics.Add(ctx, 1, metric.WithAttributes(attribute.String("owner", owner), attribute.String("route", route)))
	}
}

func (in *instruments) rateLimited(ctx context.Context, class string) {
	if in.limited != nil {
		in.limited.Add(ctx, 1, metric.WithAttributes(attribute.String("class", class)))
	}
}

func (in *instruments) crossOrigin(ctx context.Context, owner string) {
	if in.crossOrig != nil {
		in.crossOrig.Add(ctx, 1, metric.WithAttributes(attribute.String("owner", owner)))
	}
}

// countingBody counts the request body bytes the handler read.
type countingBody struct {
	io.ReadCloser
	n atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n.Add(int64(n))
	return n, err
}

// telemetry is stage 2 (05-http-api §4.2): the server span with redacted URL
// attributes and the three semconv HTTP histograms, recorded for every
// request with the route the State carries after the handler returned.
// server.address is Options.ServerName, never the request's Host, and
// server.port comes from the listener (MOD10-2), so no client can mint a
// series. The attribute set is otelgin's, which these families replace.
func (c *Chain) telemetry(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		st := From(r.Context())
		ctx := r.Context()
		if c.deps.Propagator != nil {
			ctx = c.deps.Propagator.Extract(ctx, propagation.HeaderCarrier(r.Header))
		}
		var span trace.Span
		if c.deps.Tracer != nil {
			ctx, span = c.deps.Tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(c.requestAttrs(r, st)...))
		}
		var body *countingBody
		if r.Body != nil && r.Body != http.NoBody {
			body = &countingBody{ReadCloser: r.Body}
			r.Body = body
		}
		hw := newHookWriter(w, r, nil)
		if st != nil {
			// The router counts the request in flight once it knows the
			// owner (enter); this removes it, whatever happened inside.
			defer c.leave(context.WithoutCancel(ctx), st)
		}
		next.ServeHTTP(hw, r.WithContext(ctx))

		route := ""
		if st != nil {
			route = st.Route()
		}
		status := hw.Status()
		respBytes := hw.n
		if st != nil && st.compressed.Load() {
			respBytes = st.uncompressed.Load()
		}
		var reqBytes int64
		if body != nil {
			reqBytes = body.n.Load()
		}
		if r.ContentLength > reqBytes {
			reqBytes = r.ContentLength
		}
		rctx := context.WithoutCancel(ctx)
		attrs := c.metricAttrs(r, status, route)
		if span != nil {
			if route != "" {
				span.SetName(r.Method + " " + route)
				span.SetAttributes(semconv.HTTPRoute(route))
			} else {
				span.SetName(r.Method)
			}
			span.SetAttributes(semconv.HTTPResponseStatusCode(status))
			if respBytes > 0 {
				span.SetAttributes(semconv.HTTPResponseBodySize(int(respBytes)))
			}
			if status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			if err := r.Context().Err(); err != nil {
				span.SetStatus(codes.Error, err.Error())
				attrs = append(attrs, semconv.ErrorTypeKey.String(errorType(err)))
			}
			span.End()
		} else if err := r.Context().Err(); err != nil {
			attrs = append(attrs, semconv.ErrorTypeKey.String(errorType(err)))
		}
		set := attribute.NewSet(attrs...)
		if c.inst.duration != nil {
			c.inst.duration.Inst().Record(rctx, time.Since(start).Seconds(), metric.WithAttributeSet(set))
			c.inst.reqSize.Inst().Record(rctx, reqBytes, metric.WithAttributeSet(set))
			c.inst.respSize.Inst().Record(rctx, respBytes, metric.WithAttributeSet(set))
		}
		if c.deps.Observe != nil {
			c.deps.Observe(route)
		}
	})
}

// errorType is error.type for a request whose context ended early.
func errorType(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context.Canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "context.DeadlineExceeded"
	}
	return "_OTHER"
}

// listenerPort is server.port: the local port of the connection the request
// arrived on (http.LocalAddrContextKey), -1 when unknown. Like otelgin it
// is omitted for the scheme's default port (80, 443).
func listenerPort(r *http.Request) int {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || addr == nil {
		return -1
	}
	port := -1
	switch a := addr.(type) {
	case *net.TCPAddr:
		port = a.Port
	case *net.UDPAddr:
		port = a.Port
	default:
		if _, p, err := net.SplitHostPort(a.String()); err == nil {
			port = atoiOr(p, -1)
		}
	}
	if port <= 0 || (r.TLS != nil && port == 443) || (r.TLS == nil && port == 80) {
		return -1
	}
	return port
}

func atoiOr(s string, def int) int {
	n := 0
	if s == "" {
		return def
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	return n
}

// standardMethod is the semconv method value: a known method upper-cased,
// "_OTHER" otherwise, so no client can mint a series with an invented verb.
func standardMethod(m string) string {
	switch u := strings.ToUpper(m); u {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return u
	}
	return "_OTHER"
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// protocol splits r.Proto ("HTTP/1.1") into network.protocol.name and
// .version as otelgin did ("http", "1.1"; HTTP/3 reports "HTTP/3.0").
func protocol(r *http.Request) (name, version string) {
	name, version, _ = strings.Cut(r.Proto, "/")
	return strings.ToLower(name), version
}

// metricAttrs is the attribute set of one request's three histograms:
// otelgin's keys, with server.address fixed and server.port from the
// listener, and http.route only for a routed request.
func (c *Chain) metricAttrs(r *http.Request, status int, route string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 9)
	attrs = append(attrs,
		semconv.HTTPRequestMethodKey.String(standardMethod(r.Method)),
		semconv.URLScheme(scheme(r)),
		semconv.ServerAddress(c.deps.ServerName))
	if p := listenerPort(r); p > 0 {
		attrs = append(attrs, semconv.ServerPort(p))
	}
	name, version := protocol(r)
	if name != "" {
		attrs = append(attrs, semconv.NetworkProtocolName(name))
	}
	if version != "" {
		attrs = append(attrs, semconv.NetworkProtocolVersion(version))
	}
	attrs = append(attrs, semconv.HTTPResponseStatusCode(status))
	if route != "" {
		attrs = append(attrs, semconv.HTTPRoute(route))
	}
	return attrs
}

// requestAttrs are the span's start attributes: otelgin's request set with
// the URL redacted by the kernel's Redactor (SEC-22) and the request ID
// (§4.7).
func (c *Chain) requestAttrs(r *http.Request, st *State) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		semconv.ServerAddress(c.deps.ServerName),
		semconv.HTTPRequestMethodKey.String(standardMethod(r.Method)),
		semconv.URLScheme(scheme(r)),
		semconv.URLPath(c.deps.Redactor.Path(r.URL.Path)),
	}
	if standardMethod(r.Method) != r.Method {
		attrs = append(attrs, semconv.HTTPRequestMethodOriginal(r.Method))
	}
	if r.URL.RawQuery != "" {
		attrs = append(attrs, semconv.URLQuery(strings.TrimPrefix(c.deps.Redactor.URL("?"+r.URL.RawQuery), "?")))
	}
	if p := listenerPort(r); p > 0 {
		attrs = append(attrs, semconv.ServerPort(p))
	}
	if host, port, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		attrs = append(attrs, semconv.NetworkPeerAddress(host))
		if n := atoiOr(port, -1); n > 0 {
			attrs = append(attrs, semconv.NetworkPeerPort(n))
		}
	}
	if ua := r.UserAgent(); ua != "" {
		attrs = append(attrs, semconv.UserAgentOriginal(ua))
	}
	if st != nil {
		if st.client.IsValid() {
			attrs = append(attrs, semconv.ClientAddress(st.client.String()))
		}
		if st.requestID != "" {
			attrs = append(attrs, attribute.String("http.request_id", st.requestID))
		}
	}
	name, version := protocol(r)
	if name != "" && name != "http" {
		attrs = append(attrs, semconv.NetworkProtocolName(name))
	}
	if version != "" {
		attrs = append(attrs, semconv.NetworkProtocolVersion(version))
	}
	return attrs
}
