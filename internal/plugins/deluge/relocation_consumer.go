// file: internal/plugins/deluge/relocation_consumer.go
// version: 0.1.0
// guid: 495dc1bb-4fc7-4b3e-b17e-41224256517c
// last-edited: 2026-11-20

package deluge

import (
	"fmt"

	"github.com/falkcorp/mediacore/outbox"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// RelocationConsumerName is the outbox consumer that turns each
// books.book.relocated.v1 message into a books.deluge.path-update run. Books
// lists it in Manifest.OutboxConsumers and registers RelocationConsumer through
// Registrar.OutboxHandler (the events flip, F6-03).
const RelocationConsumerName = "books.deluge.book-relocated"

// RelocationConsumer is the durable relocation follow-up (05-events §5E.13,
// the OperationDef.Triggers row): each committed book move starts one Live
// books.deluge.path-update run, staged in the batch that advances the
// consumer's cursor, so the hand-off is effectively once. Live, because a move
// that is already committed wants the torrent to follow it; the kind's
// Preview default must never apply to an outbox start (COR2-5).
//
// It replaced NotifyDelugeAfterOrganize, deleted in the PR that registered this
// consumer, so an organize move never triggers Deluge twice. The version-swap and import moves are not replaced and stay
// in-process until the flip designs their events: a swap moves the
// from-version to .versions/<id>, which path-update skips (it acts on the
// Active version only), and a same-path swap writes no book row so stages
// nothing; an import writes a book_file row and its DelugeHash, never
// Book.FilePath.
func RelocationConsumer() outbox.Handler {
	return outbox.ConsumeIntoTask(database.BookRelocatedTopic(), RelocationConsumerName, pathUpdateKind,
		relocationRunParams, outbox.Live())
}

// relocationRunParams is the run's params: the book ID only. The run reads the
// book's path as stored when it runs, so a run started for an older move of a
// book that has moved again still sends the torrent to where the book is now.
func relocationRunParams(m outbox.Message[database.BookRelocatedV1]) (any, error) {
	if m.Payload.BookID == "" {
		return nil, fmt.Errorf("%w: %s message %q names no book", outbox.ErrPermanent, database.BookRelocatedTopicName, m.IdempotencyKey)
	}
	return pathUpdateCoreParams{BookID: m.Payload.BookID}, nil
}
