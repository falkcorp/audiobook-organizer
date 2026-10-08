// file: internal/writeback/enqueuer.go
// version: 1.1.0
// guid: 5c255544-6862-47a8-bb9f-cce7630ecba5
// last-edited: 2026-10-07

package writeback

// Enqueuer is the batcher surface the outbox replays into. The iTunes
// write-back batcher that implemented it was removed on 2026-10-07 (iTunes is
// an import-only source); nothing in the tree imports this package any more,
// and it is slated for deletion.
type Enqueuer interface {
	Enqueue(bookID string)
}
