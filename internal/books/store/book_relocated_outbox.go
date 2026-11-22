// file: internal/books/store/book_relocated_outbox.go
// version: 0.1.0
// guid: 52c91bc1-ba16-47fa-8ce6-641d38b61233
// last-edited: 2026-11-20

package store

import (
	"errors"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// BookRelocatedTopicName is the durable topic books stages when a book's
// file_path changes (05-events §5E.13, the OperationDef.Triggers row): the
// relocation follow-up, which a books consumer turns into a
// books.deluge.path-update run.
const BookRelocatedTopicName = "books.book.relocated.v1"

// bookRelocatedPartitions is the topic's partition count. It is fixed for the
// topic's lifetime (05-events §5E.4), so it is spelled out instead of taking
// the zero default: changing it later stops the topic.
const bookRelocatedPartitions = 16

// BookRelocatedV1 is the payload of books.book.relocated.v1: the original book
// whose torrents follow, and the book (its organized copy) they follow to. The
// message key is BookID, so one book's relocations are sequenced in commit
// order (05-events 5E.5.2 "Partitioning").
//
// It names books, never paths. A consumer reads the target's path as stored
// when it runs, so a replay after the copy moved again follows the copy and no
// value that is stale by design is in the message. No spec names the fields
// (05-events 5E.13 specifies the topic by its trigger), and nothing had staged
// the topic before F6-03, so no stored row constrains them.
type BookRelocatedV1 struct {
	BookID       string `json:"book_id"`
	TargetBookID string `json:"target_book_id"`
}

// BookRelocatedTopic is the typed topic. A function, not a package variable
// (R5).
func BookRelocatedTopic() outbox.Topic[BookRelocatedV1] {
	return outbox.NewTopic[BookRelocatedV1](BookRelocatedTopicName)
}

// BookRelocatedTopicSpec is the topic's catalog entry, for books'
// Registrar.Topics at the events flip (F6-03): durable, observable by anyone
// who may view the library.
func BookRelocatedTopicSpec() events.TopicSpec {
	return BookRelocatedTopic().Spec(events.TopicSpec{Permission: rbac.Need("books:view")}, bookRelocatedPartitions)
}

// relocationStager stages one relocation into the batch that moves the book.
type relocationStager func(b *storage.Batch, ev BookRelocatedV1) error

// errRelocationNeedsCoreBatch refuses a book move whose batch cannot carry the
// outbox message: a raw-engine batch is not a core storage batch, and staging
// in a second batch would break the one-commit rule (F6-03 STOP 3).
var errRelocationNeedsCoreBatch = errors.New("books store: a relocation outbox is installed but this store's batch is not a core storage batch")

// SetRelocationOutbox makes CreateBookWithRelocation stage
// books.book.relocated.v1 through w, in the batch that writes the new row. It
// is the only writer that does: a book's file_path changing through
// UpdateBook or ModifyBook stages nothing, as it moved no torrent before the
// events flip (the Deluge hook ran only after a copy-organize). Until it is
// called the store stages nothing: books' Register calls it once, before the
// store takes writes, with the outbox's books Writer (F6-03). The outbox must
// be built over the same core storage DB as the store's LegacyRange, or every
// organized copy fails (Batch.Bind refuses a foreign handle).
func (p *PebbleStore) SetRelocationOutbox(w outbox.Writer) {
	t := BookRelocatedTopic()
	fn := relocationStager(func(b *storage.Batch, ev BookRelocatedV1) error {
		return outbox.Stage(w, b, t, ev.BookID, ev)
	})
	p.relocation.Store(&fn)
}

// stageBookRelocated stages the relocation of book originalID's torrents to
// book targetID's path into batch, the batch that creates targetID. It stages
// nothing when no outbox is installed or originalID is empty (the write is not
// a copy-organize). An error must discard the batch: the copy and its
// follow-up commit together or not at all.
func (p *PebbleStore) stageBookRelocated(batch *GatedBatch, originalID, targetID string) error {
	fn := p.relocation.Load()
	if fn == nil || originalID == "" {
		return nil
	}
	if batch.lb == nil {
		return errRelocationNeedsCoreBatch
	}
	return (*fn)(batch.lb, BookRelocatedV1{BookID: originalID, TargetBookID: targetID})
}
