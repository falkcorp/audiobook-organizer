// file: internal/ai/telemetry.go
// version: 2.3.1
// guid: c3d4e5f6-a7b8-0004-cdef-000000000004
// last-edited: 2026-10-19

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
	status int   // 0 when no response arrived
	quota  bool  // a 429 whose body carries a quota-exhaustion marker
	err    error // transport error, nil when a response arrived
	// cutOff is true when the attempt's own request context was already done
	// when the transport returned its error. It is what separates "the caller
	// ended this attempt" from "the attempt failed and the SDK will retry it",
	// and unlike inspecting err it does not depend on what the transport put in
	// the error: net/http returns context.Cause(ctx) for a cancelled request,
	// which is not a context error when the context was cancelled with a cause
	// (errgroup.WithContext does exactly that, with the first failing callback's
	// error).
	cutOff bool
	// retryHeader is the response's x-should-retry value, which the SDK honors
	// over the status ("true" or "false"; empty when absent or another value).
	retryHeader string
	start       time.Time
	elapsed     time.Duration // to the response headers, excluding the SDK's backoff
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

// attemptMiddleware is installed on every parser client (newOpenAIParser); the
// embedding clients build their own and do not carry it, which is harmless only
// because embedWithRetry passes WithMaxRetries(0) per request, so the SDK never
// absorbs an attempt there. The SDK runs it once per HTTP attempt, inside its
// retry loop. It records the attempt
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
	a := attemptRecord{
		start:   start,
		elapsed: time.Since(start),
		err:     err,
		cutOff:  err != nil && req.Context().Err() != nil,
	}
	if res != nil {
		a.status = res.StatusCode
		switch v := res.Header.Get("x-should-retry"); v {
		case "true", "false":
			a.retryHeader = v
		}
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

// endedInBackoff reports whether err, a context error, ended the call while the
// SDK slept before retrying a: the response (or transport error) a had already
// arrived and was being retried, so a is the attempt err really describes, not
// the context error. Without this a throttle that the caller's deadline or
// cancel cuts off mid-backoff would be counted as a timeout or a cancellation
// with the backoff sleep inside its duration, and the 429 would never be seen.
//
// The retry decision is the SDK's shouldRetry (openai-go/v3
// internal/requestconfig/requestconfig.go), which this mirrors: no response is
// retried; otherwise an x-should-retry header of "true" or "false" decides, and
// without one the status does (408, 409, 429 and 5xx are retried). So a response
// the SDK does not retry (a 2xx, a 4xx other than those, or any status with
// x-should-retry: false) is not a backoff: a context error after it came from
// reading its body. A transport error is a backoff unless the caller's context
// had already ended the attempt (attemptRecord.cutOff).
func endedInBackoff(a attemptRecord, err error) bool {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if a.err != nil {
		return !a.cutOff
	}
	switch a.retryHeader {
	case "true":
		return true
	case "false":
		return false
	}
	return a.status == http.StatusRequestTimeout ||
		a.status == http.StatusConflict ||
		a.status == http.StatusTooManyRequests ||
		a.status >= http.StatusInternalServerError
}

// classifyFinal classifies the call as a whole from the attempts recorded and
// the error fn returned. inBackoff is true when the call ended while the SDK
// waited to retry its last attempt (see endedInBackoff): the classification is
// then that attempt's, and no further request was made.
func classifyFinal(attempts []attemptRecord, err error) (outcome, reason string, inBackoff bool) {
	if n := len(attempts); n > 0 && endedInBackoff(attempts[n-1], err) {
		outcome, reason = classifyAttempt(attempts[n-1])
		return outcome, reason, true
	}
	outcome, reason = classifyCall(err)
	return outcome, reason, false
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
// One exception: when the caller's context ends while the SDK is waiting to
// retry, fn returns the context error but the last attempt got a response, so
// that attempt is classified by its own response and timed to its headers, and
// the context error is not a further request (classifyFinal).
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
		spanAttempts := rec.snapshot()
		if n := len(spanAttempts); n > 1 {
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
			outcome, reason, inBackoff := classifyFinal(spanAttempts, e)
			span.SetAttributes(attribute.String("ai.outcome", outcome))
			if reason != "" {
				span.SetAttributes(attribute.String("ai.failure_reason", reason))
			}
			status, _ := aiHTTPFailure(e)
			if inBackoff {
				// The context error describes no response; the attempt the
				// classification is of does.
				status = spanAttempts[len(spanAttempts)-1].status
			}
			if status != 0 {
				span.SetAttributes(attribute.Int("http.response.status_code", status))
			}
		}
		return r, e
	}, m.spanAttributes()...)

	attempts := rec.snapshot()
	outcome, reason, inBackoff := classifyFinal(attempts, err)
	// The attempts to count as retried (absorbed) ones. Normally the last is
	// left out: it is the attempt fn's own result describes, timed until fn
	// returned (which includes reading the reply body). Ended in backoff, the
	// last attempt was retried too and is the one the classification is of.
	retried := attempts
	if len(attempts) > 0 && !inBackoff {
		retried = attempts[:len(attempts)-1]
		elapsed = end.Sub(attempts[len(attempts)-1].start)
	}
	for _, a := range retried {
		ao, ar := classifyAttempt(a)
		ins.duration.Record(ctx, a.elapsed.Seconds(), metric.WithAttributeSet(m.labels()))
		ins.requests.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrOutcome.String(ao))...))
		if ao != outcomeCanceled {
			ins.failures.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrReason.String(ar))...))
		}
	}
	if inBackoff {
		return res, err
	}

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
	if _, ok := errors.AsType[*ResultCountError](err); ok {
		return reasonCountMismatch
	}
	if _, ok := errors.AsType[*ReplyParseError](err); ok {
		return reasonReplyParse
	}
	return ""
}
