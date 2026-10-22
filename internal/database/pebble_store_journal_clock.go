// file: internal/database/pebble_store_journal_clock.go
// version: 1.0.0
// guid: b81c4f26-7a39-4d0e-95c2-6e1f0a3d8b57
// last-edited: 2026-10-22

// The journal clock: one strictly increasing timestamp source for every
// CreatedAt and RevertedAt the operation-change journal records.
//
// WHY. The order of the journal's rows is the order of their timestamps, and
// the undo engine decides from it which retire came before which (a survivor's
// marker before the row of the next retire, a stamp before a later write). The
// wall clock is not an order: it can be stepped back (NTP, a VM resume, a
// hand), and two writes can share an instant. A step back between a retire's
// marker and its row, or between two retires, would invert the order the
// journal says they happened in, and a revert decided from it would put a
// book in the wrong place without any row saying so. The journal therefore
// takes its time from this clock, not from time.Now.
//
// next returns max(wall, last+1ns): never equal to, and never before, a
// timestamp it handed out. The last value is a high-water mark kept in an
// atomic, moved forward by compare-and-swap, so concurrent writers each get a
// distinct, ordered value. It is persisted as system:journal_clock in the
// batch of every journal write that took a value from it, and at open the mark
// is seeded from that key plus journalClockMargin (writers commit out of
// order, so the persisted value can trail the largest value handed out by the
// writes in flight at a crash), or, when the key is missing, from the largest
// timestamp of the rows already in the journal, so a restart after a clock
// step back can never hand out a value at or below one already recorded.
//
// The persisted mark lags only by writes in flight; the margin covers it as
// long as a commit does not take longer than the margin. A store that was
// never given a journal write has no key and no rows and starts at the wall
// clock.

package database

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

const (
	journalClockKey = "system:journal_clock"
	// journalClockMargin is added to the persisted mark when it seeds the clock
	// at open.
	journalClockMargin = time.Second
)

// journalClock is the store's journal clock. The zero value reads the wall
// clock and starts its mark at zero.
type journalClock struct {
	hw atomic.Int64 // the last value handed out, UnixNano
	// wall is the time source; nil means time.Now. Set only before the store is
	// shared (SetJournalWallClock).
	wall func() time.Time
}

// next returns a timestamp strictly after every one next returned before.
func (c *journalClock) next() time.Time {
	now := time.Now
	if c.wall != nil {
		now = c.wall
	}
	wall := now().UnixNano()
	for {
		last := c.hw.Load()
		n := max(wall, last+1)
		if c.hw.CompareAndSwap(last, n) {
			return time.Unix(0, n)
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

// journalNow takes the next journal timestamp.
func (p *PebbleStore) journalNow() time.Time { return p.journalClock.next() }

// stageJournalClock records at in batch as the persisted mark.
func stageJournalClock(batch *GatedBatch, at time.Time) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], uint64(at.UnixNano()))
	if err := batch.Set([]byte(journalClockKey), v[:], nil); err != nil {
		return fmt.Errorf("stage journal clock: %w", err)
	}
	return nil
}

// seedJournalClock sets the mark at open (see the file comment). It writes
// nothing: the first journal write persists the mark.
func (p *PebbleStore) seedJournalClock() error {
	v, closer, err := p.db.Get([]byte(journalClockKey))
	switch {
	case err == nil:
		defer closer.Close()
		if len(v) != 8 {
			return fmt.Errorf("%s: %d bytes, want 8", journalClockKey, len(v))
		}
		p.journalClock.raise(int64(binary.BigEndian.Uint64(v)) + int64(journalClockMargin))
		return nil
	case !errors.Is(err, pebble.ErrNotFound):
		return fmt.Errorf("read %s: %w", journalClockKey, err)
	}
	// A row that does not decode has no readable time, and one corrupt row must
	// not keep the store from opening: it is skipped here (the readers that
	// need it fail on it, as they always did).
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opChangeKeyPrefix), UpperBound: []byte("opchange;")})
	if err != nil {
		return fmt.Errorf("seed the journal clock from the journal: %w", err)
	}
	defer iter.Close()
	var top int64
	for iter.First(); iter.Valid(); iter.Next() {
		var c OperationChange
		if json.Unmarshal(iter.Value(), &c) != nil {
			continue
		}
		top = max(top, c.CreatedAt.UnixNano())
		if c.RevertedAt != nil {
			top = max(top, c.RevertedAt.UnixNano())
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("seed the journal clock from the journal: %w", err)
	}
	p.journalClock.raise(top)
	return nil
}
