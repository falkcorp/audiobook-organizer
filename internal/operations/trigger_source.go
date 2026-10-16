// file: internal/operations/trigger_source.go
// version: 1.0.1
// guid: 3a7f9e21-b4c6-4d8a-9e0f-1b2c3d4e5f6a
// last-edited: 2026-10-16

package operations

import "context"

type triggerSourceKeyType struct{}

var triggerSourceKey = triggerSourceKeyType{}

// TriggerManual marks an operation as user-initiated.
// Task functions should emit AlwaysShow activity entries when this source is active.
const TriggerManual = "manual"

// TriggerScheduled marks an operation as scheduler-initiated (interval tick,
// startup task, or maintenance window). Task functions should suppress intermediate
// activity entries and emit only the summary.
const TriggerScheduled = "scheduled"

// WithTriggerSource returns a context carrying the given trigger source.
// Called inside triggerOperation / triggerOperationWithID before invoking the task fn.
func WithTriggerSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, triggerSourceKey, source)
}

// TriggerSourceFromContext returns the trigger source stored in ctx,
// defaulting to TriggerScheduled when absent.
func TriggerSourceFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(triggerSourceKey).(string); ok && v != "" {
		return v
	}
	return TriggerScheduled
}
