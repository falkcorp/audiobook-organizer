// file: internal/ai/telemetry.go
// version: 2.1.0
// guid: c3d4e5f6-a7b8-0004-cdef-000000000004
// last-edited: 2026-10-11

package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

var aiTracer = otel.Tracer("audiobook-organizer/ai")

// aiTracerOverride lets a test route spans to a private recorder: the OTel
// global tracer provider delegates to the first provider set in the process,
// so a per-test provider cannot be installed through otel.SetTracerProvider.
// Nil in production.
var aiTracerOverride atomic.Pointer[trace.Tracer]

func tracer() trace.Tracer {
	if t := aiTracerOverride.Load(); t != nil {
		return *t
	}
	return aiTracer
}

// WithAISpan runs fn inside a span named opName. The span context is handed to
// fn, so spans fn starts (and the SDK's own HTTP spans) nest under it. The call
// is not OpenAI-specific: the same SDK reaches local OpenAI-compatible
// servers, which is why this is not named after one provider.
//
// A failure sets the span status to Error and error=true, and fn's error is
// returned unchanged with the zero T. The error itself is never recorded on the
// span (no RecordError, no error text in the status): a transport error
// formats as the request URL and the dial target, an endpoint's LAN address,
// and an API error carries the provider's raw response body (older SDK
// versions put both in its text), which would put them into trace data. A caller that wants a classification on the span adds it as attributes
// from inside fn (observe does). A cancellation is the caller giving up, not a
// failed call, so it leaves the status alone.
func WithAISpan[T any](ctx context.Context, opName string, fn func(context.Context) (T, error), attrs ...attribute.KeyValue) (T, error) {
	spanCtx, span := tracer().Start(ctx, opName, trace.WithAttributes(attrs...))
	defer span.End()

	result, err := fn(spanCtx)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			span.SetStatus(codes.Error, "ai call failed")
			span.SetAttributes(attribute.Bool("error", true))
		}
		var zero T
		return zero, err
	}
	return result, nil
}

// attemptRecord is one HTTP attempt the SDK made for a call.
type attemptRecord struct {
	status  int   // 0 when no response arrived
	quota   bool  // a 429 whose body carries a quota-exhaustion marker
	err     error // transport error, nil when a response arrived
	start   time.Time
	elapsed time.Duration // to the response headers, excluding the SDK's backoff
}

// attemptRecorder collects the HTTP attempts of one observed call. The SDK
// retries a 429, a 5xx and a transport error itself (two more times by
// default), inside the one SDK call observe wraps, so without this a throttle
// the SDK absorbs would never be counted and the call's duration would include
// the SDK's backoff sleeps.
type attemptRecorder struct {
	mu       sync.Mutex
	attempts []attemptRecord
}

func (r *attemptRecorder) add(a attemptRecord) {
	r.mu.Lock()
	r.attempts = append(r.attempts, a)
	r.mu.Unlock()
}

func (r *attemptRecorder) snapshot() []attemptRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.attempts)
}

type attemptRecorderKey struct{}

// attemptMiddleware is installed on every client (newOpenAIParser). The SDK
// runs it once per HTTP attempt, inside its retry loop. It records the attempt
// only when the request belongs to an observed call (a recorder is in the
// context): batch submissions and anything else sharing a client pass through
// untouched. It never reads a body except a 429's, and then only a bounded
// prefix that is put back.
func attemptMiddleware(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	rec, _ := req.Context().Value(attemptRecorderKey{}).(*attemptRecorder)
	if rec == nil {
		return next(req)
	}
	start := time.Now()
	res, err := next(req)
	a := attemptRecord{start: start, elapsed: time.Since(start), err: err}
	if res != nil {
		a.status = res.StatusCode
		if res.StatusCode == http.StatusTooManyRequests {
			a.quota = peekQuota429(res)
		}
	}
	rec.add(a)
	return res, err
}

// peekQuota429 reports whether a 429 response body names quota exhaustion
// (quota429Markers, the same strings isPermanentQuota429 checks on the SDK
// error), without consuming it: at most maxQuotaPeek bytes are read and the
// body is replaced by one that yields them again, so the SDK still sees the
// whole thing. Only the two marker fields are decoded; no text is kept.
func peekQuota429(res *http.Response) bool {
	if res.Body == nil {
		return false
	}
	const maxQuotaPeek = 4096
	head, readErr := io.ReadAll(io.LimitReader(res.Body, maxQuotaPeek))
	res.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), res.Body), res.Body}
	if readErr != nil {
		// The SDK meets the same read failure on the rest of the body; a
		// truncated head is not evidence of quota exhaustion.
		return false
	}

	var payload struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(head, &payload) != nil {
		return false
	}
	return slices.ContainsFunc(quota429Markers, func(m string) bool {
		return payload.Error.Type == m || payload.Error.Code == m
	})
}

// classifyAttempt is classifyCall for an attempt the SDK absorbed: it has a
// status and no error value, or a transport error.
func classifyAttempt(a attemptRecord) (outcome, reason string) {
	if a.err != nil {
		return classifyCall(a.err)
	}
	switch {
	case a.quota:
		reason = reasonQuota
	case a.status == http.StatusTooManyRequests:
		reason = reasonRateLimit
	default:
		reason = reasonOther
	}
	return outcomeError, reason
}

// observe is the one place a direct AI request is timed, counted and traced.
// fn is the single SDK call and nothing else (the caller reads the reply, and
// parses it, outside): a reply that arrives but cannot be used is not a
// transport failure, see recordReplyFailure.
//
// One request is one HTTP attempt. The SDK retries a 429, a 5xx and a
// transport error on its own inside fn, and attemptMiddleware records each
// attempt, so a throttle the SDK absorbs is counted as an error request with a
// failure and the duration is the attempt's, not the call's including backoff.
// Every attempt but the last was retried, so it is an "error" (or "timeout")
// request; the last is classified by the error fn returned. A call that
// reached no transport (no attempt recorded) counts once, timed end to end.
//
// It records: ai_requests_total (by outcome), ai_request_duration_seconds,
// ai_tokens_total from usage of the final reply (nothing when it carries no
// usage, rather than a false zero) and, for a failed attempt,
// ai_failures_total by reason. A cancellation is outcome "canceled" and not a
// failure: the caller gave up, the backend did nothing wrong. A deadline is
// outcome "timeout" with reason "timeout".
//
// usage may be nil (a call that has no token accounting).
func observe[T any](ctx context.Context, m callMeta, fn func(context.Context) (T, error), usage func(T) (in, out int64)) (T, error) {
	m = m.normalized()
	ins := aiInstrumentsFor()

	var (
		elapsed time.Duration
		end     time.Time
		inTok   int64
		outTok  int64
	)
	rec := &attemptRecorder{}
	res, err := WithAISpan(context.WithValue(ctx, attemptRecorderKey{}, rec), "ai."+m.task, func(spanCtx context.Context) (T, error) {
		start := time.Now()
		r, e := fn(spanCtx)
		end = time.Now()
		elapsed = end.Sub(start)
		span := trace.SpanFromContext(spanCtx)
		if n := len(rec.snapshot()); n > 1 {
			span.SetAttributes(attribute.Int("ai.http_attempts", n))
		}
		if e == nil && usage != nil {
			inTok, outTok = usage(r)
			if inTok > 0 || outTok > 0 {
				span.SetAttributes(
					attribute.Int64("ai.tokens.input", inTok),
					attribute.Int64("ai.tokens.output", outTok))
			}
		}
		if e != nil {
			// The classification only: never the error, which carries the
			// endpoint URL and the provider's raw body (see WithAISpan).
			outcome, reason := classifyCall(e)
			span.SetAttributes(attribute.String("ai.outcome", outcome))
			if reason != "" {
				span.SetAttributes(attribute.String("ai.failure_reason", reason))
			}
			if status, _ := aiHTTPFailure(e); status != 0 {
				span.SetAttributes(attribute.Int("http.response.status_code", status))
			}
		}
		return r, e
	}, m.spanAttributes()...)

	attempts := rec.snapshot()
	last := len(attempts) - 1
	for i, a := range attempts {
		if i == last {
			// The final attempt runs until fn returns, which includes reading
			// the reply body; an absorbed one is timed to its response headers.
			elapsed = end.Sub(a.start)
			break
		}
		// Retried by the SDK, so it failed.
		ao, ar := classifyAttempt(a)
		ins.duration.Record(ctx, a.elapsed.Seconds(), metric.WithAttributeSet(m.labels()))
		ins.requests.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrOutcome.String(ao))...))
		if ao != outcomeCanceled {
			ins.failures.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrReason.String(ar))...))
		}
	}

	outcome, reason := classifyCall(err)
	ins.duration.Record(ctx, elapsed.Seconds(), metric.WithAttributeSet(m.labels()))
	ins.requests.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrOutcome.String(outcome))...))
	if inTok > 0 {
		ins.tokens.Add(ctx, inTok, metric.WithAttributes(append(m.labelList(), attrDirection.String("input"))...))
	}
	if outTok > 0 {
		ins.tokens.Add(ctx, outTok, metric.WithAttributes(append(m.labelList(), attrDirection.String("output"))...))
	}
	if err != nil && outcome != outcomeCanceled {
		ins.failures.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrReason.String(reason))...))
	}
	return res, err
}

// recordReplyFailure counts a reply the model returned that could not be used:
// *ResultCountError (always wrapped in a *ReplyParseError) as count_mismatch,
// any other *ReplyParseError as reply_parse. Anything else is ignored.
//
// These arrive after the SDK call returned successfully, so the request itself
// was "ok" and observe has already counted it so; this adds only the failure.
// The caller's current span gets an "ai.reply_failure" EVENT carrying the
// reason, never a status or the error text: a *ReplyParseError embeds an
// excerpt of the model's reply, and a count mismatch is something the batch
// split recovers by design, which must not turn the scan operation's or the
// HTTP request's span red.
func recordReplyFailure(ctx context.Context, m callMeta, err error) {
	reason := replyFailureReason(err)
	if reason == "" {
		return
	}
	m = m.normalized()
	aiInstrumentsFor().failures.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrReason.String(reason))...))
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.AddEvent("ai.reply_failure", trace.WithAttributes(
			attribute.String("ai.failure_reason", reason),
			attribute.String("ai.task", m.task)))
	}
}

// replyFailureReason is "count_mismatch", "reply_parse" or "" for an error that
// is not a reply-quality failure. The count check comes first: the count error
// is wrapped in a ReplyParseError, which would otherwise match for both.
func replyFailureReason(err error) string {
	if err == nil {
		return ""
	}
	var countErr *ResultCountError
	if errors.As(err, &countErr) {
		return reasonCountMismatch
	}
	var parseErr *ReplyParseError
	if errors.As(err, &parseErr) {
		return reasonReplyParse
	}
	return ""
}
