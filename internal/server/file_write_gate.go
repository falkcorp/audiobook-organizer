// file: internal/server/file_write_gate.go
// version: 1.1.0
// guid: d02fb725-e59d-4f45-8b4c-d21c5260a330
// last-edited: 2026-09-13

package server

import "context"

// fileWriteGate bounds total concurrent writes to audio files across every
// operation. Per-operation worker pools are still useful for queueing and
// progress, but must not multiply the filesystem/TagLib load when operations
// overlap.
type fileWriteGate struct {
	slots chan struct{}
}

func newFileWriteGate(limit int) *fileWriteGate {
	if limit < 1 {
		limit = 1
	}
	return &fileWriteGate{slots: make(chan struct{}, limit)}
}

func (g *fileWriteGate) acquire(ctx context.Context) (func(), error) {
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// tryAcquire takes a slot only if one is free right now. It never blocks.
//
// It exists for fan-out INSIDE a holder: a batch-apply worker already holds
// one slot for its book and wants extra per-file writers. A blocking acquire
// there would deadlock once every slot is held by a book waiting for more
// (8 books x 1 slot, each waiting for a 2nd). A holder that takes extras only
// when they are free can never wait on itself, and the total of concurrent
// writers still never exceeds the gate's limit.
func (g *fileWriteGate) tryAcquire() (func(), bool) {
	select {
	case g.slots <- struct{}{}:
		return func() { <-g.slots }, true
	default:
		return nil, false
	}
}
