// file: core/internal/logjournal/store.go
// version: 1.0.0
// guid: e41b7d08-6a52-4c93-8f1d-3b9a0c5e2d76
// last-edited: 2026-11-13

// Package logjournal is core's native system-log journal (05-logging §5): the
// one implementation of log.JournalStore and log.Importer, the batching tap
// that feeds it from the logger, and the settings and schedule sections that
// configure it. It keeps records in the core/log owner's keyspace under
//
//	journal/<ULID>      one record, a codec.JSON row of version 1
//
// where the ULID's 48 time bits are the record's own Time in milliseconds, so
// a scan in key order is a scan in time order and retention is a range delete
// from the oldest key. The journal has no migration steps and no level stamp:
// it is a plain keyspace with nothing before it to migrate (05-logging §5;
// the cutover of an app's pre-core rows is that app's step, over Importer).
//
// Dependency position: L4, beside the other core/internal stores. It imports
// log (L0), storage and tasks' schedule type, and nothing that imports it.
package logjournal

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the journal's keyspace. It is registered with
// StorageSpec by the kernel (F5-18); nothing here registers it.
const Owner storage.Owner = "core/log"

// StorageSpec is the journal's storage declaration: a plain keyspace. It has no
// Migrations and no Level, because the journal never changes shape under a
// level (a future row version is a new codec version, not a step); and it is
// not Serialized, because every write is an append of a fresh key or a delete
// of an old one and no write reads first.
func StorageSpec() storage.Spec { return storage.Spec{Keyspace: true} }

const famJournal = "journal"

// Bounds of Query and Prune. A call is bounded in rows examined, not only in
// rows returned, so a filter that matches nothing cannot hold the store for the
// whole journal; the cursor carries the work over to the next call.
const (
	// DefaultLimit is the page size of a query that names none.
	DefaultLimit = 50
	// MaxLimit caps a query's page size (the route caps it lower, 05-logging §5).
	MaxLimit = 1000
	// MaxPruneBatch caps one Prune call's delete batch.
	MaxPruneBatch = 10_000
	// scanBudget is the most rows one Query examines.
	scanBudget = 20_000
	// maxTracked bounds the per-millisecond entropy memory.
	maxTracked = 4096
)

var (
	// ErrBadCursor is a query cursor this store did not issue.
	ErrBadCursor = errors.New("logjournal: not a journal cursor")
)

const cursorPrefix = "j1."

// Options of New. Zero values are production: the wall clock and crypto/rand.
type Options struct {
	// Now stamps a record whose Time is zero (the tap stamps its own, so this
	// is the rare direct writer).
	Now func() time.Time
	// Entropy fills a fresh millisecond's 80 random bits.
	Entropy func([]byte) error
}

// Store is the journal over its keyspace. It is safe for concurrent use.
type Store struct {
	ks      storage.Keyspace
	now     func() time.Time
	entropy func([]byte) error

	mu   sync.Mutex
	last map[uint64][entropyLen]byte // per-millisecond entropy high-water mark

	corrupt atomic.Uint64
}

var (
	_ log.JournalStore = (*Store)(nil)
	_ log.Importer     = (*Importer)(nil)
)

// New returns the store over ks, the core/log keyspace.
func New(ks storage.Keyspace, opts ...Options) *Store {
	s := &Store{ks: ks, now: time.Now, entropy: func(b []byte) error { _, err := crand.Read(b); return err },
		last: make(map[uint64][entropyLen]byte)}
	for _, o := range opts {
		if o.Now != nil {
			s.now = o.Now
		}
		if o.Entropy != nil {
			s.entropy = o.Entropy
		}
	}
	return s
}

// Corrupt counts the rows Query and Prune have skipped because they did not
// decode (a damaged row, or one a newer build wrote). A journal that skips a
// row says so here rather than failing every read.
func (s *Store) Corrupt() uint64 { return s.corrupt.Load() }

func journalKey(ulid string) []byte { return keys.Join(keys.Str(famJournal), keys.Str(ulid)) }

// journalBounds is the half-open key range of the whole journal family.
func journalBounds() (start, end []byte) {
	start = keys.Prefix(keys.Str(famJournal))
	return start, keys.PrefixEnd(start)
}

// ulidOfKey is the ULID of a journal key, or "" for a key that is not one.
func ulidOfKey(k []byte) string {
	segs, err := keys.Split(k)
	if err != nil || len(segs) != 2 || string(segs[0]) != famJournal {
		return ""
	}
	return string(segs[1])
}

// floorKey is the smallest key of millisecond ms.
func floorKey(ms uint64) []byte { return journalKey(makeULID(ms, [entropyLen]byte{})) }

// nextULID is a ULID for ms that sorts after every ULID this store has issued
// for ms: the entropy of a millisecond's first record is random, each later
// one's is the previous plus one. That is what makes records of an equal Time
// come back in the reverse of their append order. When the increment would
// overflow 80 bits the entropy is drawn again.
func (s *Store) nextULID(ms uint64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, seen := s.last[ms]
	if seen && !increment(&e) {
		seen = false
	}
	if !seen {
		var b [entropyLen]byte
		if err := s.entropy(b[:]); err != nil {
			return "", fmt.Errorf("logjournal: entropy: %w", err)
		}
		// Leave headroom, so an increment cannot overflow straight away.
		b[0] &= 0x7f
		e = b
	}
	if len(s.last) >= maxTracked {
		clear(s.last)
	}
	s.last[ms] = e
	return makeULID(ms, e), nil
}

// increment adds one to e as a big-endian integer; false on overflow.
func increment(e *[entropyLen]byte) bool {
	for i := entropyLen - 1; i >= 0; i-- {
		e[i]++
		if e[i] != 0 {
			return true
		}
	}
	return false
}

// Append writes recs in one durable batch. A record with a zero Time is stamped
// with the store's clock; a record whose time cannot be keyed (before 1970,
// past year 9999) rejects the whole call with ErrInvalidTime before anything is
// written, so a batch is all or nothing. A cancelled ctx writes nothing.
func (s *Store) Append(ctx context.Context, recs []log.Record) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(recs) == 0 {
		return nil
	}
	type staged struct {
		ms  uint64
		row row
	}
	rows := make([]staged, len(recs))
	now := s.now()
	for i, r := range recs {
		t := r.Time
		if t.IsZero() {
			t = now
		}
		ms, err := msOf(t)
		if err != nil {
			return err
		}
		rw, err := toRow(r, t)
		if err != nil {
			return err
		}
		rows[i] = staged{ms: ms, row: rw}
	}
	b := s.ks.NewBatch()
	defer discard(b, &err)
	for _, st := range rows {
		ulid, err := s.nextULID(st.ms)
		if err != nil {
			return err
		}
		raw, err := rowCodec().Encode(st.row)
		if err != nil {
			return fmt.Errorf("logjournal: encode: %w", err)
		}
		if err := b.Set(journalKey(ulid), raw); err != nil {
			return err
		}
	}
	return b.Commit(ctx, storage.Sync)
}

// cursorOf is the cursor that resumes below key k.
func cursorOf(k []byte) string { return cursorPrefix + ulidOfKey(k) }

// parseCursor is the ULID a cursor names.
func parseCursor(c string) (string, error) {
	ulid, ok := strings.CutPrefix(c, cursorPrefix)
	if !ok {
		return "", ErrBadCursor
	}
	if _, _, err := parseULID(ulid); err != nil {
		return "", ErrBadCursor
	}
	return ulid, nil
}

// Query returns one page, newest first by the record's millisecond and, inside
// a millisecond, in reverse append order. Since is inclusive and Until
// exclusive, judged on the record's own Time. The page holds at most Limit
// records (DefaultLimit when zero, at most MaxLimit); a call also examines at
// most scanBudget rows, and when that ends it first, Next resumes from where
// it stopped, so a page may be short while Next is set. Next is "" only when no
// older record matches.
func (s *Store) Query(ctx context.Context, q log.JournalQuery) (log.JournalPage, error) {
	if err := ctx.Err(); err != nil {
		return log.JournalPage{}, err
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	start, end := journalBounds()
	if !q.Since.IsZero() {
		ms := q.Since.UnixMilli()
		if ms > 0 {
			if uint64(ms) > maxMS {
				return log.JournalPage{}, nil
			}
			start = floorKey(uint64(ms))
		}
	}
	if !q.Until.IsZero() {
		ms := q.Until.UnixMilli()
		switch {
		case ms < 0:
			return log.JournalPage{}, nil
		case uint64(ms) < maxMS:
			if e := floorKey(uint64(ms) + 1); bytes.Compare(e, end) < 0 {
				end = e
			}
		}
	}
	if q.Cursor != "" {
		ulid, err := parseCursor(q.Cursor)
		if err != nil {
			return log.JournalPage{}, err
		}
		if c := journalKey(ulid); bytes.Compare(c, end) < 0 {
			end = c
		}
	}
	if bytes.Compare(start, end) >= 0 {
		return log.JournalPage{}, nil
	}
	text := strings.ToLower(q.Text)
	match := func(r log.Record) bool {
		if r.Level < q.MinLevel {
			return false
		}
		if q.Module != "" && r.Module != q.Module {
			return false
		}
		if !q.Since.IsZero() && r.Time.Before(q.Since) {
			return false
		}
		if !q.Until.IsZero() && !r.Time.Before(q.Until) {
			return false
		}
		return text == "" || strings.Contains(strings.ToLower(r.Message), text)
	}

	var page log.JournalPage
	var lastReturned, lastExamined []byte
	examined := 0
	more := false
	for kv, err := range s.ks.Scan(ctx, storage.ScanOptions{Start: start, End: end, Reverse: true}) {
		if err != nil {
			return log.JournalPage{}, err
		}
		if examined >= scanBudget {
			more = true // the budget ended the call; resume where it stopped
			break
		}
		examined++
		lastExamined = append(lastExamined[:0], kv.Key...)
		r, ok := s.decode(kv)
		if !ok || !match(r) {
			continue
		}
		if len(page.Records) == limit {
			more = true // a match beyond the page
			break
		}
		page.Records = append(page.Records, r)
		lastReturned = append(lastReturned[:0], kv.Key...)
	}
	if more {
		if len(page.Records) == limit {
			page.Next = cursorOf(lastReturned)
		} else {
			page.Next = cursorOf(lastExamined)
		}
	}
	return page, nil
}

// decode is the record of a scanned row. A row that does not decode is skipped
// and counted: one row of a newer build, or one damaged, must not hide the
// journal around it.
func (s *Store) decode(kv storage.KV) (log.Record, bool) {
	rw, err := rowCodec().Decode(kv.Value)
	if err != nil {
		s.corrupt.Add(1)
		return log.Record{}, false
	}
	r, err := rw.record()
	if err != nil {
		s.corrupt.Add(1)
		return log.Record{}, false
	}
	return r, true
}

// discard releases a batch that did not commit and folds Close's error into
// *err; after a Commit, Close is a no-op that returns nil.
func discard(b *storage.Batch, err *error) { *err = errors.Join(*err, b.Close()) }

// Prune deletes up to limit records older than before (exclusive), oldest
// first, in one durable batch, and reports whether older records remain.
// limit is clamped to MaxPruneBatch; limit zero or less deletes nothing and
// reports whether any record is older. A record's key holds only its
// millisecond, so keys of earlier milliseconds are older without being read
// and keys of before's own millisecond are decoded and judged on Time.
func (s *Store) Prune(ctx context.Context, before time.Time, limit int) (deleted int, more bool, err error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if limit > MaxPruneBatch {
		limit = MaxPruneBatch
	}
	bms := before.UnixMilli()
	if bms < 0 {
		return 0, false, nil
	}
	first, end := journalBounds()
	if uint64(bms) < maxMS {
		// Everything of before's own millisecond is judged below.
		end = floorKey(uint64(bms) + 1)
	}
	older := func(kv storage.KV) bool {
		ulid := ulidOfKey(kv.Key)
		kms, _, perr := parseULID(ulid)
		if perr != nil {
			return false // not a journal row; leave it
		}
		if int64(kms) < bms {
			return true
		}
		rw, derr := rowCodec().Decode(kv.Value)
		if derr != nil {
			s.corrupt.Add(1)
			return false
		}
		return rw.Time.Before(before)
	}
	var doomed [][]byte
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: first, End: end}) {
		if serr != nil {
			return 0, false, serr
		}
		if !older(kv) {
			continue
		}
		if len(doomed) >= max(limit, 0) {
			more = true
			break
		}
		doomed = append(doomed, bytes.Clone(kv.Key))
	}
	if len(doomed) == 0 {
		return 0, more, nil
	}
	b := s.ks.NewBatch()
	defer discard(b, &err)
	for _, k := range doomed {
		if err := b.Delete(k); err != nil {
			return 0, false, err
		}
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return 0, false, err
	}
	return len(doomed), more, nil
}
