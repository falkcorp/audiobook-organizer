// file: internal/books/store/book_changed_topic.go
// version: 0.1.0
// guid: 4b7e21c9-83d5-4f60-a1e2-6c09d5f3b748
// last-edited: 2026-11-22

package store

import (
	"context"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// BookChangedTopicName is the realtime topic of one committed book row write
// (05-realtime 4.7). It replaces hub A's batched books.changed frame: one frame
// per book, keyed by the book's ID, which the SPA folds back into batches
// (web/src/services/coreEvents/bookChanges.ts).
const BookChangedTopicName = "books.book.changed.v1"

// BookChangedV1 is the payload of books.book.changed.v1.
type BookChangedV1 struct {
	Kind BookChangeKind `json:"kind"`
	ID   string         `json:"id"`
}

// BookChangedTopic is the typed topic. A function, not a package variable (R5).
func BookChangedTopic() events.Topic[BookChangedV1] {
	return events.NewTopic[BookChangedV1](BookChangedTopicName)
}

// BookChangedTopicSpec is the topic's catalog entry, for books' Registrar.Topics
// at the events flip (F6-03): observable by anyone who may view the library, and
// exposed as a realtime stream.
//
// It does NOT coalesce. 05-realtime 11 first specified Coalesce: 250ms, but the
// hub's coalescing is latest-wins per key, so a book created and then updated
// inside the window would reach a client as "updated" alone: a client that
// patches rows in place would never add the new row. The SPA's batcher already
// folds a 250 ms window with the rule hub A's coalescer had (created stays
// created, deleted supersedes), so the server leaves every frame alone.
func BookChangedTopicSpec() events.TopicSpec {
	return BookChangedTopic().Spec(events.TopicSpec{
		Permission: rbac.Need("books:view"),
		Realtime:   &events.RealtimeMeta{},
	})
}

// PublishBookChanges makes every committed book row write publish
// books.book.changed.v1 on pub, from the library-generation funnel's observer
// (SetBookChangeObserver). The observer runs on the book writer's goroutine, and
// events.Publish never blocks and never returns an error, so a slow or absent
// subscriber costs the write nothing. A zero events.Scope (a boot without the
// identity flip has no bus) publishes nothing. It replaces the observer; the
// returned func removes it.
func PublishBookChanges(pub events.Publisher) (stop func()) {
	t := BookChangedTopic()
	SetBookChangeObserver(func(kind BookChangeKind, id string) {
		events.Publish(pub, context.Background(), t, id, BookChangedV1{Kind: kind, ID: id})
	})
	return func() { SetBookChangeObserver(nil) }
}
