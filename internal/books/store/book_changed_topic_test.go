// file: internal/books/store/book_changed_topic_test.go
// version: 0.1.0
// guid: 6e1b8d37-4c92-4a05-b7f3-2d9a0c85e614
// last-edited: 2026-11-22

package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/stretchr/testify/require"
)

// TestBookChangedTopicSpec: books.book.changed.v1 is gated by books:view, is
// a realtime stream, and does NOT coalesce. The hub's coalescing keeps the
// latest envelope per key, so a book created and then updated in one window
// would arrive as "updated" alone and a client patching rows in place would
// never add the row (05-realtime 11, amended in F6-03): created must survive
// to the wire.
func TestBookChangedTopicSpec(t *testing.T) {
	s := BookChangedTopicSpec()
	require.Equal(t, "books.book.changed.v1", s.Name)
	require.Equal(t, rbac.Need("books:view"), s.Permission)
	require.NotNil(t, s.Realtime, "the topic is a realtime stream")
	require.Zero(t, s.Realtime.Coalesce, "created must not be folded into a later updated")
	require.Nil(t, s.Notify)
	require.Nil(t, s.Durable, "an advisory frame, not an outbox message")
}

// TestPublishBookChanges: every committed write publishes one frame through the
// scope it was given (Deps.Events, not a hub), keyed by the book, with the kind
// the funnel reported: created stays created, then updated, then deleted, in
// commit order. Stopping the publisher stops the frames, and a zero scope (a
// boot without the identity flip) publishes nothing and does not panic.
func TestPublishBookChanges(t *testing.T) {
	bus, err := events.NewBus(events.BusOptions{})
	require.NoError(t, err)
	require.NoError(t, bus.Declare("books", BookChangedTopicSpec()))

	var mu sync.Mutex
	var seen []events.Envelope[BookChangedV1]
	require.NoError(t, bus.Subscribe("books", events.On(BookChangedTopic(), "books.changes-probe", func(_ context.Context, e events.Envelope[BookChangedV1]) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, e)
	})))
	bus.Freeze()
	require.NoError(t, bus.Open())
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	got := func(n int) []events.Envelope[BookChangedV1] {
		t.Helper()
		require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= n }, 5*time.Second, 5*time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]events.Envelope[BookChangedV1](nil), seen...)
	}

	s := setupTestPebbleStore(t)
	stop := PublishBookChanges(bus.Scope("books"))
	b, err := s.CreateBook(&Book{Title: "Year One", FilePath: "/tmp/year-one.m4b", Format: "m4b"})
	require.NoError(t, err)
	b.Title = "Year Two"
	_, err = s.UpdateBook(b.ID, b)
	require.NoError(t, err)
	require.NoError(t, s.DeleteBook(b.ID))

	frames := got(3)
	require.Len(t, frames, 3)
	for i, kind := range []BookChangeKind{BookChangeCreated, BookChangeUpdated, BookChangeDeleted} {
		require.Equal(t, BookChangedV1{Kind: kind, ID: b.ID}, frames[i].Payload, "frame %d", i)
		require.Equal(t, b.ID, frames[i].Key, "the key is the book, so the hub can narrow a stream to one")
	}

	stop()
	_, err = s.CreateBook(&Book{Title: "Dark Victory", FilePath: "/tmp/dark-victory.m4b", Format: "m4b"})
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	require.Len(t, seen, 3, "a stopped publisher publishes nothing")
	mu.Unlock()

	t.Cleanup(PublishBookChanges(events.Scope{}))
	_, err = s.CreateBook(&Book{Title: "The Long Halloween", FilePath: "/tmp/long-halloween.m4b", Format: "m4b"})
	require.NoError(t, err, "a zero scope publishes nothing")
}
