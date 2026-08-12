// file: internal/database/activity_store_instrumented.go
// version: 1.1.0
// guid: b2c3d4e5-f6a7-0002-bcde-000000000002
// last-edited: 2026-08-11

package database

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var tracer = otel.Tracer("audiobook-organizer/database")

// InstrumentedActivityStorer wraps an ActivityStorer with OpenTelemetry tracing.
// Each database operation creates a span with relevant attributes (operation name, counts, errors).
type InstrumentedActivityStorer struct {
	store ActivityStorer
}

// NewInstrumentedActivityStorer wraps a store with OTEL instrumentation.
func NewInstrumentedActivityStorer(store ActivityStorer) *InstrumentedActivityStorer {
	return &InstrumentedActivityStorer{store: store}
}

// Record traces the Record operation.
func (i *InstrumentedActivityStorer) Record(entry ActivityEntry) (int64, error) {
	_, span := tracer.Start(context.Background(), "activity_store.record",
		trace.WithAttributes(
			attribute.String("tier", entry.Tier),
			attribute.String("type", entry.Type),
			attribute.String("level", entry.Level),
		))
	defer span.End()

	id, err := i.store.Record(entry)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}
	span.SetAttributes(attribute.Int64("entry_id", id))
	return id, nil
}

// Query traces the Query operation.
//
// The span is started FROM ctx and the returned ctx is what gets handed to the
// wrapped store. Starting from context.Background() here (as this did before
// Query became cancellable) would silently terminate the cancellation chain at
// this wrapper: the build and every test would still pass while an abandoned
// request kept scanning in production.
func (i *InstrumentedActivityStorer) Query(ctx context.Context, filter ActivityFilter) ([]ActivityEntry, int, error) {
	ctx, span := tracer.Start(ctx, "activity_store.query",
		trace.WithAttributes(
			attribute.String("tier", filter.Tier),
			attribute.String("source", filter.Source),
		))
	defer span.End()

	entries, total, err := i.store.Query(ctx, filter)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return nil, 0, err
	}
	span.SetAttributes(
		attribute.Int("entries_returned", len(entries)),
		attribute.Int("total_matching", total))
	return entries, total, nil
}

// Summarize traces the Summarize operation.
func (i *InstrumentedActivityStorer) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	_, span := tracer.Start(ctx, "activity_store.summarize",
		trace.WithAttributes(
			attribute.String("tier", tier),
			attribute.String("older_than", olderThan.Format(time.RFC3339)),
		))
	defer span.End()

	count, err := i.store.Summarize(ctx, olderThan, tier)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}
	span.SetAttributes(attribute.Int("entries_summarized", count))
	return count, nil
}

// Prune traces the Prune operation.
func (i *InstrumentedActivityStorer) Prune(olderThan time.Time, tier string) (int, error) {
	_, span := tracer.Start(context.Background(), "activity_store.prune",
		trace.WithAttributes(
			attribute.String("tier", tier),
			attribute.String("older_than", olderThan.Format(time.RFC3339)),
		))
	defer span.End()

	count, err := i.store.Prune(olderThan, tier)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}
	span.SetAttributes(attribute.Int("entries_pruned", count))
	return count, nil
}

// GetDistinctSources traces the GetDistinctSources operation.
//
// As with Query, the span starts from the caller's ctx and the derived ctx is
// passed down, so cancellation survives this wrapper.
func (i *InstrumentedActivityStorer) GetDistinctSources(ctx context.Context, filter ActivityFilter) ([]SourceCount, error) {
	ctx, span := tracer.Start(ctx, "activity_store.get_distinct_sources",
		trace.WithAttributes(
			attribute.String("tier", filter.Tier),
		))
	defer span.End()

	sources, err := i.store.GetDistinctSources(ctx, filter)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return nil, err
	}
	span.SetAttributes(attribute.Int("source_count", len(sources)))
	return sources, nil
}

// WipeAllActivity traces the WipeAllActivity operation.
func (i *InstrumentedActivityStorer) WipeAllActivity() (int64, error) {
	_, span := tracer.Start(context.Background(), "activity_store.wipe_all_activity")
	defer span.End()

	count, err := i.store.WipeAllActivity()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}
	span.SetAttributes(attribute.Int64("entries_wiped", count))
	return count, nil
}

// CompactByDay traces the CompactByDay operation.
func (i *InstrumentedActivityStorer) CompactByDay(ctx context.Context, olderThan time.Time) (CompactResult, error) {
	_, span := tracer.Start(ctx, "activity_store.compact_by_day",
		trace.WithAttributes(
			attribute.String("older_than", olderThan.Format(time.RFC3339)),
		))
	defer span.End()

	result, err := i.store.CompactByDay(ctx, olderThan)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return CompactResult{}, err
	}
	span.SetAttributes(
		attribute.Int("entries_deleted", result.EntriesDeleted),
		attribute.Int("days_compacted", result.DaysCompacted))
	return result, nil
}

// MigrateSystemActivityLogs traces the MigrateSystemActivityLogs operation.
func (i *InstrumentedActivityStorer) MigrateSystemActivityLogs() (int, error) {
	_, span := tracer.Start(context.Background(), "activity_store.migrate_system_activity_logs")
	defer span.End()

	count, err := i.store.MigrateSystemActivityLogs()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return 0, err
	}
	span.SetAttributes(attribute.Int("entries_migrated", count))
	return count, nil
}

// RecompactDigests traces the RecompactDigests operation.
func (i *InstrumentedActivityStorer) RecompactDigests(ctx context.Context) (RecompactResult, error) {
	_, span := tracer.Start(ctx, "activity_store.recompact_digests")
	defer span.End()

	result, err := i.store.RecompactDigests(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return RecompactResult{}, err
	}
	span.SetAttributes(
		attribute.Int("touched", result.Touched),
		attribute.Int("skipped", result.Skipped))
	return result, nil
}

// Close traces the Close operation.
func (i *InstrumentedActivityStorer) Close() error {
	_, span := tracer.Start(context.Background(), "activity_store.close")
	defer span.End()

	err := i.store.Close()
	if err != nil {
		span.RecordError(err)
		span.SetAttributes(attribute.Bool("error", true))
		return err
	}
	return nil
}
