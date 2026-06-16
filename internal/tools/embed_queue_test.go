// file: internal/tools/embed_queue_test.go
// version: 1.0.0
// guid: a3b4c5d6-e7f8-9012-abcd-012345678901
// last-edited: 2026-06-15

package tools_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/tools"
	"github.com/stretchr/testify/assert"
)

func TestEmbedQueue_DrainNowCallsDrainFn(t *testing.T) {
	var drainCalled atomic.Bool
	q := tools.NewEmbedQueue(tools.EmbedQueueConfig{
		Capacity: 100,
		DrainFn: func(ctx context.Context) error {
			drainCalled.Store(true)
			return nil
		},
		Debounce: 10 * time.Minute,
	})
	q.Enqueue("book-1")
	q.Enqueue("book-2")
	err := q.DrainNow(context.Background())
	assert.NoError(t, err)
	assert.True(t, drainCalled.Load())
}

func TestEmbedQueue_EnqueueDropsWhenFull(t *testing.T) {
	q := tools.NewEmbedQueue(tools.EmbedQueueConfig{
		Capacity: 2,
		DrainFn:  func(ctx context.Context) error { return nil },
		Debounce: 10 * time.Minute,
	})
	q.Enqueue("a")
	q.Enqueue("b")
	q.Enqueue("c") // should drop, not block
	assert.Equal(t, 2, q.Len())
}

func TestEmbedQueue_DebounceFiresDrain(t *testing.T) {
	var drainCalled atomic.Bool
	q := tools.NewEmbedQueue(tools.EmbedQueueConfig{
		Capacity: 100,
		DrainFn: func(ctx context.Context) error {
			drainCalled.Store(true)
			return nil
		},
		Debounce:      50 * time.Millisecond,
		AllowPeriodic: true,
	})
	q.Start(context.Background())
	q.Enqueue("book-1")
	time.Sleep(200 * time.Millisecond)
	assert.True(t, drainCalled.Load())
	q.Stop()
}
