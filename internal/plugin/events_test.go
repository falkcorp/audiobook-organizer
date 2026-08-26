// file: internal/plugin/events_test.go
// version: 1.0.0

package plugin

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEventBus_SubscribeAndPublish(t *testing.T) {
	bus := NewEventBus()
	var called atomic.Bool

	bus.Subscribe(EventBookImported, func(ctx context.Context, evt Event) error {
		called.Store(true)
		assert.Equal(t, "book-1", evt.BookID)
		return nil
	})

	bus.Publish(context.Background(), NewEvent(EventBookImported, "book-1", nil))
	time.Sleep(50 * time.Millisecond) // handlers run in goroutines
	assert.True(t, called.Load())
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	var count atomic.Int32

	for range 3 {
		bus.Subscribe(EventMetadataApplied, func(ctx context.Context, evt Event) error {
			count.Add(1)
			return nil
		})
	}

	bus.Publish(context.Background(), NewEvent(EventMetadataApplied, "book-1", nil))
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(3), count.Load())
}

func TestEventBus_NoSubscribers(t *testing.T) {
	bus := NewEventBus()
	// Should not panic
	bus.Publish(context.Background(), NewEvent(EventBookDeleted, "book-1", nil))
}

func TestEventBus_HandlerErrorDoesNotPanic(t *testing.T) {
	bus := NewEventBus()
	bus.Subscribe(EventScanCompleted, func(ctx context.Context, evt Event) error {
		return assert.AnError
	})
	// Should not panic
	bus.Publish(context.Background(), NewEvent(EventScanCompleted, "", nil))
	time.Sleep(50 * time.Millisecond)
}

func TestEventBus_SubscriberCount(t *testing.T) {
	bus := NewEventBus()
	assert.Equal(t, 0, bus.SubscriberCount(EventBookImported))
	bus.Subscribe(EventBookImported, func(ctx context.Context, evt Event) error { return nil })
	bus.Subscribe(EventBookImported, func(ctx context.Context, evt Event) error { return nil })
	assert.Equal(t, 2, bus.SubscriberCount(EventBookImported))
}

func TestNewEvent(t *testing.T) {
	evt := NewEvent(EventFileOrganized, "book-1", map[string]any{"old_path": "/a", "new_path": "/b"})
	assert.Equal(t, EventFileOrganized, evt.Type)
	assert.Equal(t, "book-1", evt.BookID)
	assert.Equal(t, "/a", evt.Data["old_path"])
	require.WithinDuration(t, time.Now(), evt.Timestamp, time.Second)
}
