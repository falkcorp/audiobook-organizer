// file: internal/ai/telemetry.go
// version: 1.0.1
// guid: c3d4e5f6-a7b8-0004-cdef-000000000004
// last-edited: 2026-08-20

package ai

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var aiTracer = otel.Tracer("audiobook-organizer/ai")

// WithOpenAISpan wraps an OpenAI API call with OTEL instrumentation.
// Creates a span with the given operation name and optional attributes.
//
// Usage:
//
//	result, err := WithOpenAISpan(ctx, "parse_filename",
//	  func(spanCtx context.Context) (*ParsedMetadata, error) {
//	    return p.parseFilenameWithoutInstrumentation(spanCtx)
//	  },
//	  attribute.String("filename", filename),
//	)
func WithOpenAISpan(ctx context.Context, opName string, fn func(context.Context) (any, error), attrs ...attribute.KeyValue) (any, error) {
	_, span := aiTracer.Start(ctx, opName, trace.WithAttributes(attrs...))
	defer span.End()

	result, err := fn(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return nil, err
	}
	return result, nil
}

// RecordOpenAIMetric records a counter metric for OpenAI API usage.
// This is a placeholder for future metrics instrumentation.
func RecordOpenAIMetric(metricName string, value int64, attrs ...attribute.KeyValue) {
	// TODO: implement when metrics are fully configured
	// Will use otel.Meter to record API call counts, token usage, etc.
}
