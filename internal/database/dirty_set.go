// file: internal/database/dirty_set.go
// version: 1.0.0
// guid: 3e0f7a52-6b0e-4d3c-9c46-1d7a0b8f5e21
// last-edited: 2026-10-30

package database

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/cockroachdb/pebble/v2"
)

// A durable dirty set is a key family <prefix><subjectID> whose value begins
// with an 8-byte big-endian epoch. MarkDirty writes an entry with a fresh
// epoch; CompareAndDeleteRaw removes it only if the stored value is still the
// one the reader saw, so a subject re-marked while it was being processed
// keeps its entry and is processed again. The v3 Batch runner's ops.DirtySet
// source drains such a family (internal/operations/registry, dirtySetStore).

// dirtyEntryLock returns the mutex that serializes every write of one dirty
// entry. For the identification family that is the book's identification lock
// (metadataCacheLock of the book id), the lock MarkIdentDirty and
// ClearIdentDirty have always taken; any other family is striped over the same
// 64 mutexes by its whole key. Marker and compare-and-delete of one key always
// pick the same lock, which is the whole requirement.
func (p *PebbleStore) dirtyEntryLock(key string) *sync.Mutex {
	if id, ok := strings.CutPrefix(key, identDirtyPrefix); ok {
		return p.metadataCacheLock(id)
	}
	return p.metadataCacheLock(key)
}

// rejectDirtyEntryKey refuses the families whose rows carry derived state a
// bare Set/Delete would desynchronize (the same two families SetRaw routes
// through commitRawWrites / commitOpV2Row), plus the empty key.
func rejectDirtyEntryKey(key string) error {
	if key == "" {
		return errors.New("dirty set: empty key")
	}
	if err := rejectOpv2OpRawKey(key); err != nil {
		return err
	}
	if strings.HasPrefix(key, metadataCacheKeyPrefix) {
		return fmt.Errorf("dirty set: %q is a metadata_cache: row, which is restaged with the identification index and cannot be a dirty-set entry", key)
	}
	return nil
}

// MarkDirty records subjectID in the dirty set under prefix with a new epoch
// and returns that epoch. An entry already present is replaced by the newer
// epoch, so a drain that read the older one cannot clear it.
//
// It takes the entry's lock (dirtyEntryLock); for the identification family
// that is the book's metadataCacheLock stripe, so it may be called while
// holding the book's write stripe and must not be called while holding the
// cache stripe itself.
func (p *PebbleStore) MarkDirty(prefix, subjectID string) (uint64, error) {
	if prefix == "" || subjectID == "" {
		return 0, fmt.Errorf("MarkDirty: prefix %q and subject id %q must both be non-empty", prefix, subjectID)
	}
	key := prefix + subjectID
	if err := rejectDirtyEntryKey(key); err != nil {
		return 0, err
	}
	mu := p.dirtyEntryLock(key)
	mu.Lock()
	defer mu.Unlock()
	epoch := p.nextIdentEpoch()
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], epoch)
	if err := p.db.Set([]byte(key), b[:], pebble.Sync); err != nil {
		return 0, fmt.Errorf("mark %s: %w", key, err)
	}
	return epoch, nil
}

// CompareAndDeleteRaw deletes key only when its stored value equals expect
// byte for byte, and reports whether it did. A missing key or a different
// value (a re-mark carries a newer epoch) deletes nothing and is not an
// error. The read and the delete happen under the entry's lock, the one
// MarkDirty writes under, so no mark can land between them.
func (p *PebbleStore) CompareAndDeleteRaw(key string, expect []byte) (bool, error) {
	if err := rejectDirtyEntryKey(key); err != nil {
		return false, err
	}
	mu := p.dirtyEntryLock(key)
	mu.Lock()
	defer mu.Unlock()
	val, closer, err := p.db.Get([]byte(key))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", key, err)
	}
	same := bytes.Equal(val, expect)
	closer.Close()
	if !same {
		return false, nil
	}
	if err := p.db.Delete([]byte(key), pebble.Sync); err != nil {
		return false, fmt.Errorf("delete %s: %w", key, err)
	}
	return true, nil
}
