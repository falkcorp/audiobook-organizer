// file: internal/database/pebble_store_journal_clock.go
// version: 2.0.1
// guid: b81c4f26-7a39-4d0e-95c2-6e1f0a3d8b57
// last-edited: 2026-10-22

// The journal clock: one strictly increasing sequence for every row the
// operation-change journal writes or marks reverted.
//
// WHY. The order of the journal's rows is what the undo engine decides from: it
// reads which retire came before which (a survivor's marker before the row of
// the next retire, a stamp before a later write). The wall clock is not an
// order: it can be stepped back (NTP, a VM resume, a hand), and two writes can
// share an instant. A step back between a retire's marker and its row, or
// between two retires, would invert the order the journal says they happened in.
// So every row carries two stamps: CreatedAt and RevertedAt stay WALL times (what
// the owner sees, and what a file's mtime is compared with), and CreatedSeq and
// RevertedSeq are this clock's sequence, which the undo engine orders by
// (undo.CreatedKey, undo.RevertedKey; a row with no sequence falls back to its
// wall time).
//
// SEQUENCE. stamp returns the wall time and max(wall, last+1ns): never equal to,
// and never before, a sequence it handed out. The last value is a high-water mark
// in an atomic.
//
// PERSISTENCE. The mark is persisted as system:journal_clock in the batch of
// every journal write that took a sequence from it. A journal batch holds
// journalClock.mu from taking its sequence through its commit, so the batches
// commit in the order they stamped and the persisted value only ever moves
// forward: it is at or above every sequence a committed row carries, never the
// stale value of a writer that stamped first and committed last. Journal writes
// are low-throughput (a retire writes a few rows, a revert one batch per
// operation), so serializing them costs one commit at a time
// (BenchmarkJournalWrite_Serialized measures it). A max-merge operator would
// avoid the lock but needs a stable merger name registered in the store's
// options, a change to every opened store's on-disk options for one key; the
// mutex needs no format change.
//
// OPEN. The mark is seeded from the persisted key plus journalClockMargin (the
// margin is slack for a wall clock that stepped back while the process was down;
// the serialized commit means there are no in-flight stamps to cover), or, when
// the key is missing, by one scan of the journal: the largest stamp of any row.
// That first scan also stages the opchange_by_list: entry of every
// user_content_follow row that lacks one (rows an earlier build wrote), then
// persists the key, so it runs once. A malformed key does not stop the open: it is
// reported and the scan runs instead. A store that was never given a journal
// write has no key and no rows and starts at the wall clock.

package database

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var journalClockLog = logger.New("database.journal-clock")

const (
	journalClockKey = "system:journal_clock"
	// journalClockMargin is added to the persisted mark when it seeds the clock
	// at open.
	journalClockMargin = time.Second
)

// journalClock is the store's journal clock. The zero value reads the wall
// clock and starts its mark at zero.
type journalClock struct {
	// mu is held by a journal batch from stamp through commit (see PERSISTENCE).
	mu sync.Mutex
	hw atomic.Int64 // the last sequence handed out, UnixNano
	// wall is the time source; nil means time.Now. Set only before the store is
	// shared (SetJournalWallClock).
	wall func() time.Time
}

// lock takes the clock for one journal batch; call the result after its commit.
func (c *journalClock) lock() (unlock func()) {
	c.mu.Lock()
	return c.mu.Unlock
}

// journalAfterStamp, when non-nil, runs in a journal batch right after it took
// its sequence and before it commits, while the clock is held; a non-nil error
// aborts the batch before anything is written. Test-only.
var journalAfterStamp func() error

// stamp returns the wall time and a sequence strictly after every one stamp
// returned before.
func (c *journalClock) stamp() (wall time.Time, seq int64) {
	now := time.Now
	if c.wall != nil {
		now = c.wall
	}
	wall = now()
	w := wall.UnixNano()
	for {
		last := c.hw.Load()
		n := max(w, last+1)
		if c.hw.CompareAndSwap(last, n) {
			return wall, n
		}
	}
}

// raise moves the mark up to at least n.
func (c *journalClock) raise(n int64) {
	for {
		last := c.hw.Load()
		if n <= last || c.hw.CompareAndSwap(last, n) {
			return
		}
	}
}

// SetJournalWallClock replaces the wall clock the journal clock reads, for a
// test that steps time back. It is not safe once writers are running.
func (p *PebbleStore) SetJournalWallClock(now func() time.Time) { p.journalClock.wall = now }

// stageJournalClock records seq in batch as the persisted mark.
func stageJournalClock(batch *GatedBatch, seq int64) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], uint64(seq))
	if err := batch.Set([]byte(journalClockKey), v[:], nil); err != nil {
		return fmt.Errorf("stage journal clock: %w", err)
	}
	return nil
}

// journalScanRow is the part of a journal row the open-time scan needs.
type journalScanRow struct {
	ChangeType  string     `json:"change_type"`
	FieldName   string     `json:"field_name"`
	CreatedAt   time.Time  `json:"created_at"`
	RevertedAt  *time.Time `json:"reverted_at"`
	CreatedSeq  int64      `json:"created_seq"`
	RevertedSeq int64      `json:"reverted_seq"`
}

// seedJournalClock sets the mark at open (see OPEN in the file comment). With
// the key present it writes nothing; the first journal write persists the mark.
func (p *PebbleStore) seedJournalClock() error {
	v, closer, err := p.db.Get([]byte(journalClockKey))
	switch {
	case err == nil:
		ok := len(v) == 8
		if ok {
			p.journalClock.raise(int64(binary.BigEndian.Uint64(v)) + int64(journalClockMargin))
		}
		n := len(v)
		closer.Close()
		if ok {
			return nil
		}
		journalClockLog.Warn("journal clock key %s is malformed (%d bytes, want 8); seeding the clock from the journal instead", journalClockKey, n)
	case !errors.Is(err, pebble.ErrNotFound):
		return fmt.Errorf("read %s: %w", journalClockKey, err)
	}
	return p.scanJournalForClock()
}

// scanJournalForClock is the missing-key (or malformed-key) seed: one pass over
// the journal for the largest stamp, staging along the way every
// opchange_by_list: entry a user_content_follow row lacks, then persisting the
// mark. Idempotent: a second pass finds every entry and writes only the mark.
func (p *PebbleStore) scanJournalForClock() error {
	started := time.Now()
	// A row that does not decode has no readable time, and one corrupt row must
	// not keep the store from opening: it is skipped here (the readers that
	// need it fail on it, as they always did).
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opChangeKeyPrefix), UpperBound: []byte("opchange;")})
	if err != nil {
		return fmt.Errorf("seed the journal clock from the journal: %w", err)
	}
	var top int64
	rows := 0
	type entry struct{ key []byte }
	var missing []entry
	for iter.First(); iter.Valid(); iter.Next() {
		var c journalScanRow
		if json.Unmarshal(iter.Value(), &c) != nil {
			continue
		}
		rows++
		top = max(top, c.CreatedSeq, c.RevertedSeq, c.CreatedAt.UnixNano())
		if c.RevertedAt != nil {
			top = max(top, c.RevertedAt.UnixNano())
		}
		kind, id, ok := opChangeListRef(&OperationChange{ChangeType: c.ChangeType, FieldName: c.FieldName})
		if !ok || !opChangeListIndexable(id) {
			continue
		}
		key := append(opChangeByListListPrefix(kind, id), iter.Key()[len(opChangeKeyPrefix):]...)
		if _, closer, gerr := p.db.Get(key); gerr == nil {
			closer.Close()
			continue
		} else if !errors.Is(gerr, pebble.ErrNotFound) {
			iter.Close()
			return fmt.Errorf("seed the journal clock from the journal: read list entry: %w", gerr)
		}
		missing = append(missing, entry{key: key})
	}
	if err := iter.Error(); err != nil {
		iter.Close()
		return fmt.Errorf("seed the journal clock from the journal: %w", err)
	}
	iter.Close()
	p.journalClock.raise(top)
	if rows == 0 {
		return nil
	}
	const chunk = 2000
	for start := 0; start < len(missing) || start == 0; start += chunk {
		batch := p.db.NewBatch()
		end := min(start+chunk, len(missing))
		for _, m := range missing[start:end] {
			if err := batch.Set(m.key, nil, nil); err != nil {
				batch.Close()
				return fmt.Errorf("stage opchange_by_list entry: %w", err)
			}
		}
		if end == len(missing) {
			if err := stageJournalClock(batch, top); err != nil {
				batch.Close()
				return err
			}
		}
		err := batch.Commit(pebble.Sync)
		batch.Close()
		if err != nil {
			return fmt.Errorf("seed the journal clock from the journal: %w", err)
		}
		if end == len(missing) {
			break
		}
	}
	journalClockLog.Info("journal clock seeded from the journal: %d rows scanned, %d list index entries staged, took %s", rows, len(missing), time.Since(started))
	return nil
}
