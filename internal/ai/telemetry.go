// file: internal/ai/telemetry.go
// version: 2.0.0
// guid: c3d4e5f6-a7b8-0004-cdef-000000000004
// last-edited: 2026-10-10

package ai

import (
	"context"
	"errors"
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
// A failure is recorded on the span (RecordError plus error=true) and fn's
// error is returned unchanged with the zero T.
func WithAISpan[T any](ctx context.Context, opName string, fn func(context.Context) (T, error), attrs ...attribute.KeyValue) (T, error) {
	spanCtx, span := tracer().Start(ctx, opName, trace.WithAttributes(attrs...))
	defer span.End()

	result, err := fn(spanCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ai call failed")
		span.SetAttributes(attribute.Bool("error", true))
		var zero T
		return zero, err
	}
	return result, nil
}

// observe is the one place a direct AI request is timed, counted and traced.
// fn is the single SDK call and nothing else (the caller reads the reply, and
// parses it, outside): a reply that arrives but cannot be used is not a
// transport failure, see recordReplyFailure.
//
// It records, per attempt that reaches the SDK: ai_requests_total (by outcome),
// ai_request_duration_seconds, ai_tokens_total from usage (nothing when the
// reply carries no usage, rather than a false zero) and, for a failed attempt,
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
		inTok   int64
		outTok  int64
	)
	res, err := WithAISpan(ctx, "ai."+m.task, func(spanCtx context.Context) (T, error) {
		start := time.Now()
		r, e := fn(spanCtx)
		elapsed = time.Since(start)
		if e == nil && usage != nil {
			inTok, outTok = usage(r)
			if inTok > 0 || outTok > 0 {
				trace.SpanFromContext(spanCtx).SetAttributes(
					attribute.Int64("ai.tokens.input", inTok),
					attribute.Int64("ai.tokens.output", outTok))
			}
		}
		return r, e
	}, m.spanAttributes()...)

	outcome, reason := classifyCall(err)
	set := metric.WithAttributeSet(m.labels())
	ins.duration.Record(ctx, elapsed.Seconds(), set)
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
// was "ok" and observe has already counted it so; this adds only the failure
// and marks the caller's current span. The span gets the reason, never the
// error text: a *ReplyParseError embeds an excerpt of the model's reply.
func recordReplyFailure(ctx context.Context, m callMeta, err error) {
	reason := replyFailureReason(err)
	if reason == "" {
		return
	}
	m = m.normalized()
	aiInstrumentsFor().failures.Add(ctx, 1, metric.WithAttributes(append(m.labelList(), attrReason.String(reason))...))
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		span.SetStatus(codes.Error, reason)
		span.SetAttributes(attribute.Bool("error", true), attribute.String("ai.failure_reason", reason))
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
