// file: internal/server/file_write_gate.go
// version: 1.0.0
// guid: d02fb725-e59d-4f45-8b4c-d21c5260a330
// last-edited: 2026-08-27

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
