// file: internal/books/store/pebble_store_journal_clock.go
// version: 3.1.1
// guid: b81c4f26-7a39-4d0e-95c2-6e1f0a3d8b57
// last-edited: 2026-11-09

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
// and never before, a sequence it handed out. It runs under a short mutex that
// is NOT held across a commit, so journal batches stamp, build and commit
// concurrently; each fsync overlaps the others (group commit). Rows of one list
// still stamp and commit in order, because everything that decides from a list's
// journal and then writes it holds the list's own lock (undo.LockList) around
// the stamp and the commit; rows of different lists have no order between them
// to keep.
//
// PERSISTENCE: A CEILING. system:journal_clock holds a ceiling, a value at or
// above every sequence ever handed out. stamp never hands out a sequence at or
// past the ceiling it last persisted: when the next value would reach it, stamp
// first persists a new ceiling (that value plus journalClockWindow) with ONE
// synchronous write, under its mutex, then hands the value out. A ceiling write
// therefore happens at most once per window of sequence time (about once a
// minute, sooner only when the wall clock jumps ahead), and no journal batch
// carries the key. The guarantee, whichever batch commits first or last: the
// persisted value is >= the sequence of every row that has committed (a row's
// sequence was below the ceiling before the row was built). A merge operator
// that max-merges the mark would also avoid the lock, but it needs a stable
// merger name in every opened store's on-disk options for one key; the ceiling
// needs no format change.
//
// OPEN. The clock starts at the persisted ceiling exactly (no margin, no scan:
// the ceiling is already above every sequence), so the first stamp after a
// restart is past every row the previous run could have written, and a wall
// clock that stepped back while the process was down changes nothing. A clean
// Close persists the actual high-water mark in place of the ceiling, so a
// restart does not leave the clock a window ahead; after a crash it starts at
// most one window ahead of the last sequence. When the key is missing or
// malformed (a journal written before the clock existed, or a damaged key; a
// malformed key never stops the open) one scan of the journal finds the largest
// stamp of any row. The constructor writes nothing (05-storage §5S.4 step 12):
// the scan's result is held in memory, the first stamp persists a ceiling past
// it, and books' boot repairs rewrite the key (repairJournalClock). A store
// that has never held a journal row starts at the wall clock.
//
// BACKFILL OF THE LIST INDEX. The opchange_by_list: entry of every
// user_content_follow row that lacks one (rows an earlier build wrote) is
// staged by books' Migration LegacyCap+2 (backfillOpChangeListIndexStep,
// booksspec.go), gated on its OWN completion key, not on the clock key: a
// store whose clock key was written by a build that did not stage the entries
// still gets its entries. Until release R1 the open-time scan did both.

package store

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

var journalClockLog = logger.New("database.journal-clock")

const (
	// journalClockKey holds the persisted ceiling (see PERSISTENCE).
	journalClockKey = "system:journal_clock"
	// opChangeByListBackfillKey marks the list-index backfill done (see
	// BACKFILL OF THE LIST INDEX).
	opChangeByListBackfillKey = "system:backfill:opchange_by_list_index_v1_done"
	// journalClockWindow is how far past the sequence in hand a persisted ceiling
	// reaches: one synchronous ceiling write per window of sequence time.
	journalClockWindow = time.Minute
	// journalScanLogEvery is how many journal rows the open-time scan reads
	// between progress lines.
	journalScanLogEvery = 50_000
)

// journalClock is the store's journal clock. The zero value reads the wall
// clock and starts at zero.
type journalClock struct {
	// mu guards hw and reserved. It is held for one stamp, and for the ceiling
	// write when a stamp needs one; never across a batch commit.
	mu       sync.Mutex
	hw       int64 // the last sequence handed out, UnixNano
	reserved int64 // the ceiling last persisted: every sequence handed out is below it
	// wall is the time source; nil means time.Now. Set only before the store is
	// shared (SetJournalWallClock).
	wall func() time.Time
}

// journalAfterStamp, when non-nil, runs in a journal batch right after it took
// its sequence(s) and before it commits; a non-nil error aborts the batch before
// anything is written. Test-only: it lets a test hold a writer between stamping
// and committing.
var journalAfterStamp func() error

// stamp returns the wall time and a sequence strictly after every one stamp
// returned before. When the sequence would reach the persisted ceiling, persist
// is called with a new ceiling first and must make it durable; its error is
// returned and nothing is handed out. A nil persist keeps no ceiling (a clock
// that is not backed by a store).
func (c *journalClock) stamp(persist func(ceiling int64) error) (wall time.Time, seq int64, err error) {
	now := time.Now
	if c.wall != nil {
		now = c.wall
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	wall = now()
	n := max(wall.UnixNano(), c.hw+1)
	if persist != nil && n >= c.reserved {
		ceiling := n + int64(journalClockWindow)
		if err := persist(ceiling); err != nil {
			return time.Time{}, 0, err
		}
		c.reserved = ceiling
	}
	c.hw = n
	return wall, n, nil
}

// seed starts the clock at ceiling, a value already persisted and at or above
// every sequence handed out before it.
func (c *journalClock) seed(ceiling int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hw = max(c.hw, ceiling)
	c.reserved = max(c.reserved, ceiling)
}

// seedUnpersisted starts the clock at top, the largest stamp a journal scan
// found, when no persisted ceiling covers it: the next stamp is past top and
// persists a ceiling before it is handed out.
func (c *journalClock) seedUnpersisted(top int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hw = max(c.hw, top)
}

// persistHigh persists the high-water mark as the ceiling, unless a ceiling at
// or above it is persisted already.
func (c *journalClock) persistHigh(persist func(ceiling int64) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reserved > 0 && c.reserved >= c.hw {
		return nil
	}
	if err := persist(c.hw); err != nil {
		return err
	}
	c.reserved = max(c.reserved, c.hw)
	return nil
}

// settle lowers the persisted ceiling to the high-water mark (a clean Close),
// so a restart starts at the actual mark and not a window past it.
func (c *journalClock) settle(persist func(ceiling int64) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hw == 0 || c.reserved <= c.hw {
		return nil
	}
	if err := persist(c.hw); err != nil {
		return err
	}
	c.reserved = c.hw
	return nil
}

// SetJournalWallClock replaces the wall clock the journal clock reads, for a
// test that steps time back. It is not safe once writers are running.
func (p *PebbleStore) SetJournalWallClock(now func() time.Time) { p.journalClock.wall = now }

// stampJournal takes the next journal sequence, persisting a new ceiling first
// when it needs one.
func (p *PebbleStore) stampJournal() (time.Time, int64, error) {
	return p.journalClock.stamp(p.persistJournalCeiling)
}

// persistJournalCeiling makes ceiling the durable value of the clock key.
func (p *PebbleStore) persistJournalCeiling(ceiling int64) error {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], uint64(ceiling))
	if err := p.db.Set([]byte(journalClockKey), v[:], pebble.Sync); err != nil {
		return fmt.Errorf("persist the journal clock ceiling: %w", err)
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

// readClockKey reads the clock key: the ceiling when it is well-formed, and
// whether the key is good, absent or damaged.
func (p *PebbleStore) readClockKey() (ceiling int64, state clockKeyState, err error) {
	v, closer, err := p.db.Get([]byte(journalClockKey))
	switch {
	case err == nil:
		defer closer.Close()
		if len(v) != 8 {
			journalClockLog.Warn("journal clock key %s is malformed (%d bytes, want 8); seeding the clock from the journal instead", journalClockKey, len(v))
			return 0, clockKeyMalformed, nil
		}
		return int64(binary.BigEndian.Uint64(v)), clockKeyOK, nil
	case errors.Is(err, pebble.ErrNotFound):
		return 0, clockKeyMissing, nil
	}
	return 0, clockKeyMissing, fmt.Errorf("read %s: %w", journalClockKey, err)
}

type clockKeyState int

const (
	clockKeyOK clockKeyState = iota
	clockKeyMissing
	clockKeyMalformed
)

// seedJournalClock starts the clock at construction and writes nothing (a
// books constructor commits no write, 05-storage §5S.4 step 12). A good clock
// key seeds it. An absent or malformed key is recovered by one scan of the
// journal for its largest stamp, held in memory only: nothing persisted is
// at or above it yet, so the first stamp persists a ceiling past it, and the
// key itself is rewritten by the boot repairs (repairJournalClock, books'
// start hook, 06 §6.5.2 step 2). A crash before either write rescans.
func (p *PebbleStore) seedJournalClock() error {
	ceiling, state, err := p.readClockKey()
	if err != nil {
		return err
	}
	if state == clockKeyOK {
		p.journalClock.seed(ceiling)
		return nil
	}
	top, rows, err := p.scanJournalTop()
	if err != nil {
		return err
	}
	p.journalClock.seedUnpersisted(top)
	p.journalClockRepair.Store(rows > 0 || state == clockKeyMalformed)
	return nil
}

// repairJournalClock persists the clock key the construction-time scan
// recovered (HEAD wrote it at open): the high-water mark, unless a stamp has
// persisted a ceiling since. A store without a journal row and with no key
// gets none, as at HEAD.
func (p *PebbleStore) repairJournalClock() error {
	if !p.journalClockRepair.Load() {
		return nil
	}
	if err := p.journalClock.persistHigh(p.persistJournalCeiling); err != nil {
		return err
	}
	p.journalClockRepair.Store(false)
	return nil
}

// scanJournalTop is the one-time read of the journal: the largest stamp of any
// row and the number of rows read. Progress is logged every
// journalScanLogEvery rows. A row that does not decode has no readable time,
// and one corrupt row must not keep the store from opening: it is skipped
// here (the readers that need it fail on it, as they always did).
func (p *PebbleStore) scanJournalTop() (top int64, rows int, err error) {
	started := time.Now()
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(opChangeKeyPrefix), UpperBound: []byte("opchange;")})
	if err != nil {
		return 0, 0, fmt.Errorf("scan the journal at open: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var c journalScanRow
		if json.Unmarshal(iter.Value(), &c) != nil {
			continue
		}
		rows++
		if rows%journalScanLogEvery == 0 {
			journalClockLog.Info("scanning the operation journal at open (one time): %d rows read, %s elapsed", rows, time.Since(started).Round(time.Millisecond))
		}
		top = max(top, c.CreatedSeq, c.RevertedSeq, c.CreatedAt.UnixNano())
		if c.RevertedAt != nil {
			top = max(top, c.RevertedAt.UnixNano())
		}
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return 0, 0, fmt.Errorf("scan the journal at open: %w", err)
	}
	if err := iter.Close(); err != nil {
		return 0, 0, fmt.Errorf("scan the journal at open: %w", err)
	}
	if rows > 0 {
		journalClockLog.Info("journal scan at open done: %d rows read, took %s", rows, time.Since(started).Round(time.Millisecond))
	}
	return top, rows, nil
}
