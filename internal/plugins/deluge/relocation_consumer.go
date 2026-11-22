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

// RelocationConsumer is the durable copy-organize follow-up (05-events 5E.13,
// the OperationDef.Triggers row): each organized copy of a book starts one Live
// books.deluge.path-update run that moves the original book's Active torrents to
// the copy's stored path, staged in the batch that advances the consumer's
// cursor, so the hand-off is effectively once. Live, because a copy that is
// already committed wants the torrent to follow it; the kind's Preview default
// must never apply to an outbox start (COR2-5).
//
// It replaces NotifyDelugeAfterOrganize, which ran after the single-book
// organize request's copy only. The bulk organize, library clone and
// metadata-apply copies, every in-place organize and every other write of a
// book's path moved no torrent before the events flip and move none now: only
// CreateBookWithRelocation stages the topic. The version-swap and import moves
// are not replaced and stay in-process: a swap moves the from-version to
// .versions/<id>, which path-update skips (it acts on the Active version only);
// an import writes a book_file row and its DelugeHash, never Book.FilePath.
func RelocationConsumer() outbox.Handler {
	return outbox.ConsumeIntoTask(database.BookRelocatedTopic(), RelocationConsumerName, pathUpdateKind,
		relocationRunParams, outbox.Live())
}

// relocationRunParams is the run's params: the original and the target book
// IDs. The run reads the target's path as stored when it runs, so a run started
// for a copy that has moved again still sends the torrent to where the copy is
// now, and no path from the message is ever used. A message missing either id is
// a permanent condition (a retry cannot supply it): it is refused to the dead
// letters, where it is visible, and starts no run.
func relocationRunParams(m outbox.Message[database.BookRelocatedV1]) (any, error) {
	if m.Payload.BookID == "" {
		return nil, fmt.Errorf("%w: %s message %q names no book", outbox.ErrPermanent, database.BookRelocatedTopicName, m.IdempotencyKey)
	}
	if m.Payload.TargetBookID == "" {
		return nil, fmt.Errorf("%w: %s message %q names no target book", outbox.ErrPermanent, database.BookRelocatedTopicName, m.IdempotencyKey)
	}
	return pathUpdateCoreParams{BookID: m.Payload.BookID, TargetBookID: m.Payload.TargetBookID}, nil
}
