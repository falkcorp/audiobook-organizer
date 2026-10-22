// file: internal/database/pebble_store_book_hash_index.go
// version: 2.0.1
// guid: 9a4d7e21-3b86-4c05-8f19-e5c2a7d0b634
// last-edited: 2026-10-22

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// The file-hash indexes are MULTI-valued: one key per (hash, book),
//
//	book_hash:<h>\x00<id>   book_originalhash:<h>\x00<id>   book_organizedhash:<h>\x00<id>
//
// with an empty value, the shape of book_atpath:. They replace the single-valued
// book:hash:<h> / book:originalhash:<h> / book:organizedhash:<h> keys, which held
// ONE book id per hash (last writer wins). Two books holding the same file
// (a combine's shell and its keeper, a duplicate import) shared that key, and
// whichever writer touched it last decided who a lookup found. Every guard bolted
// onto it (compare-and-delete in DeleteBook and UpdateBook) only narrowed the
// damage: the purge of about 2,520 combine shells on 2026-10-21 erased live
// books' entries, and a missing entry is silent, since the scanner and the
// importers asking "is this file already in the library" get "no". With one key
// per book each writer touches only its own keys, so no writer can erase
// another's entry and there is nothing to compare.
//
// The new family lives OUTSIDE the book: range (like book_atpath:), so a scan of
// book rows never walks it.
//
// Writers (all in the same batch as the book row): CreateBook sets its keys,
// UpdateBook deletes the old pair when a hash changed and sets the current pair
// every time (a self-heal), DeleteBook deletes its own keys. Trashed books keep
// theirs.
//
// Readers (GetBookByFileHash and its two twins) scan the keys for the hash and
// POINT-VERIFY every candidate: the row must exist, decode and still hold the
// hash. An extra key (a writer that lost a race with a delete, a stale key from
// an old binary) is therefore harmless. The choice among holders is
// deterministic: the live holder with the smallest id, else the trashed holder
// with the smallest id.
//
// Trust. Until the index is known complete (the sentinel bookHashIndexSentinel)
// readers return the UNION of the new keys and the legacy single-valued key, so
// a book not yet backfilled is still found the way it always was. The legacy key
// is no longer written, so it only ever names books that held the hash when the
// binary changed; the verification step drops the stale ones. RepairBookHashIndex
// (the maintenance.book-hash-index-repair op, enqueued with ensure=true on every
// boot) writes every missing key, then, if every book row decoded, sets the
// sentinel and deletes the legacy keys. The order is the invariant: new keys
// complete, then sentinel, then legacy deletion. A book row that cannot be
// decoded keeps the sentinel unset (its hashes cannot be indexed), so lookups
// stay on the union and the op fails until the row is rewritten or removed.
//
// Rollback: a binary from before this change neither writes nor reads the new
// keys, so books it creates or re-hashes have none, and the legacy keys it
// would read were deleted by the sweep. After rolling FORWARD again the boot
// ensure re-runs the repair and writes what is missing; until it finishes a
// lookup can miss such a book.

var bookHashIndexLog = logger.New("database.book-hash-index")

const (
	// bookHashIndexSentinel is set once every book row's hash keys exist.
	bookHashIndexSentinel = "system:backfill:book_hash_index_v1_done"

	bookHashSampleCap = 20
	// bookHashUnreadableSampleCap bounds the ids named for undecodable rows.
	bookHashUnreadableSampleCap = 5
)

// bookHashKinds are the three indexes, in report order.
var bookHashKinds = [...]struct {
	name         string
	prefix       string // the multi-valued family
	legacyPrefix string // the retired single-valued family
	ofRow        func(*bookHashRow) string
	ofBook       func(*Book) string
}{
	{"hash", "book_hash:", "book:hash:",
		func(r *bookHashRow) string { return derefStr(r.FileHash) },
		func(b *Book) string { return derefStr(b.FileHash) }},
	{"originalhash", "book_originalhash:", "book:originalhash:",
		func(r *bookHashRow) string { return derefStr(r.OriginalFileHash) },
		func(b *Book) string { return derefStr(b.OriginalFileHash) }},
	{"organizedhash", "book_organizedhash:", "book:organizedhash:",
		func(r *bookHashRow) string { return derefStr(r.OrganizedFileHash) },
		func(b *Book) string { return derefStr(b.OrganizedFileHash) }},
}

const (
	bookHashKindFile = iota
	bookHashKindOriginal
	bookHashKindOrganized
)

// bookHashRow is the part of a book row the hash indexes depend on.
type bookHashRow struct {
	FileHash          *string `json:"file_hash"`
	OriginalFileHash  *string `json:"original_file_hash"`
	OrganizedFileHash *string `json:"organized_file_hash"`
	MarkedForDeletion *bool   `json:"marked_for_deletion"`
}

// bookHashKey is the index key for one (hash, book) pair of an index kind.
func bookHashKey(kind int, hash, id string) []byte {
	prefix := bookHashKinds[kind].prefix
	k := make([]byte, 0, len(prefix)+len(hash)+1+len(id))
	k = append(k, prefix...)
	k = append(k, hash...)
	k = append(k, 0)
	k = append(k, id...)
	return k
}

// bookHashBounds is the scan range holding exactly hash's keys:
// [prefix<h>\x00, prefix<h>\x01). A file hash is hex and cannot contain NUL.
func bookHashBounds(kind int, hash string) (lower, upper []byte) {
	prefix := bookHashKinds[kind].prefix
	lower = make([]byte, 0, len(prefix)+len(hash)+1)
	lower = append(lower, prefix...)
	lower = append(lower, hash...)
	upper = append(append([]byte{}, lower...), 1)
	lower = append(lower, 0)
	return lower, upper
}

// stageBookHashKeys stages the hash keys of book id into batch (id is the row's
// key, not book.ID, which a caller may leave empty): the set of keys
// for the hashes it holds now and, when old is non-nil, the delete of the keys
// for hashes it held and no longer does. UpdateBook passes the row it read,
// CreateBook passes nil. The Set is unconditional, so a row whose key went
// missing heals on its next update.
func stageBookHashKeys(batch *GatedBatch, id string, old, book *Book) error {
	for i := range bookHashKinds {
		cur := bookHashKinds[i].ofBook(book)
		if old != nil {
			if prev := bookHashKinds[i].ofBook(old); prev != "" && prev != cur {
				if err := batch.Delete(bookHashKey(i, prev, id), nil); err != nil {
					return err
				}
			}
		}
		if cur != "" {
			if err := batch.Set(bookHashKey(i, cur, id), []byte{}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// stageBookHashKeyDeletes stages the delete of every hash key book id owns.
func stageBookHashKeyDeletes(batch *GatedBatch, id string, book *Book) error {
	for i := range bookHashKinds {
		if h := bookHashKinds[i].ofBook(book); h != "" {
			if err := batch.Delete(bookHashKey(i, h, id), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// bookHashIndexTrusted reports whether the sentinel exists. Only true is
// cached, so a long-lived process picks up a completion without a restart. A
// read error other than not-found is returned, never read as "not built".
func (p *PebbleStore) bookHashIndexTrusted() (bool, error) {
	if p.bookHashTrusted.Load() {
		return true, nil
	}
	_, closer, err := p.db.Get([]byte(bookHashIndexSentinel))
	switch {
	case err == nil:
		closer.Close()
		p.bookHashTrusted.Store(true)
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", bookHashIndexSentinel, err)
	}
}

// holdersOfHash returns every book that verifiably holds hash through index
// kind, ascending by id, live and trashed alike. unreadable is the first error
// from a candidate whose row could not be read or decoded: the list then may
// not be complete, and the callers decide what that means.
func (p *PebbleStore) holdersOfHash(kind int, hash string) (holders []*Book, unreadable error, err error) {
	ids, err := p.bookHashCandidates(kind, hash)
	if err != nil {
		return nil, nil, err
	}
	trusted, err := p.bookHashIndexTrusted()
	if err != nil {
		return nil, nil, err
	}
	if !trusted {
		legacy, found, err := p.readBookHashKey([]byte(bookHashKinds[kind].legacyPrefix + hash))
		if err != nil {
			return nil, nil, err
		}
		if found {
			ids = append(ids, legacy)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	for _, id := range ids {
		b, err := p.GetBookByID(id)
		if err != nil {
			if unreadable == nil {
				unreadable = fmt.Errorf("book %s holds the %s index key for %s: %w", id, bookHashKinds[kind].name, hash, err)
			}
			continue
		}
		if b != nil && bookHashKinds[kind].ofBook(b) == hash {
			holders = append(holders, b)
		}
	}
	return holders, unreadable, nil
}

// bookByHash resolves hash through index kind: the live holder with the
// smallest id, else the trashed holder with the smallest id, else nil. A
// candidate that cannot be read is skipped unless nothing else resolves, in
// which case its error is returned (a lookup that cannot tell must not answer
// "no such book").
func (p *PebbleStore) bookByHash(kind int, hash string) (*Book, error) {
	if hash == "" {
		return nil, nil
	}
	holders, unreadable, err := p.holdersOfHash(kind, hash)
	if err != nil {
		return nil, err
	}
	var trashed *Book
	for _, b := range holders {
		if !markedForDeletionFlag(b.MarkedForDeletion) {
			return b, nil
		}
		if trashed == nil {
			trashed = b
		}
	}
	if trashed != nil {
		return trashed, nil
	}
	return nil, unreadable
}

// BooksByFileHash returns every book that holds file hash hash (live and
// trashed), ascending by id. Unlike GetBookByFileHash, which answers with ONE
// holder, this is for callers that must see all of them (is there a holder
// other than this book). A holder that cannot be read is an error: the list
// would be incomplete.
func (p *PebbleStore) BooksByFileHash(hash string) ([]*Book, error) {
	if hash == "" {
		return nil, nil
	}
	holders, unreadable, err := p.holdersOfHash(bookHashKindFile, hash)
	if err != nil {
		return nil, err
	}
	if unreadable != nil {
		return nil, unreadable
	}
	return holders, nil
}

// bookHashCandidates is the ids of the keys stored for hash (unverified).
func (p *PebbleStore) bookHashCandidates(kind int, hash string) ([]string, error) {
	lower, upper := bookHashBounds(kind, hash)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var ids []string
	for ok := iter.First(); ok; ok = iter.Next() {
		if id := iter.Key()[len(lower):]; len(id) > 0 {
			ids = append(ids, string(id))
		}
	}
	return ids, iter.Error()
}

// BookHashKindReport is one index's figures.
type BookHashKindReport struct {
	// Kind is "hash", "originalhash" or "organizedhash".
	Kind string `json:"kind"`
	// Checked is how many (book, hash) pairs the book rows hold under this
	// index: one per book that holds a hash, trashed books included.
	Checked int `json:"checked"`
	OK      int `json:"ok"`
	// Missing: the book holds the hash but its key is absent.
	Missing int `json:"missing"`
	// Written: keys written (a repair only).
	Written int `json:"written"`
}

// BookHashIndexIssue is one missing key, for the sample in a report.
type BookHashIndexIssue struct {
	Kind   string `json:"kind"`
	Hash   string `json:"hash"`
	BookID string `json:"book_id"`
}

// BookHashIndexReport is the outcome of one check or repair.
type BookHashIndexReport struct {
	// Apply is true for a repair, false for the read-only check.
	Apply     bool `json:"apply"`
	Books     int  `json:"books"`
	LiveBooks int  `json:"live_books"`
	// Kinds has one entry per index, in the order hash, originalhash, organizedhash.
	Kinds []BookHashKindReport `json:"kinds"`
	// Samples is the first few missing keys (bookHashSampleCap).
	Samples []BookHashIndexIssue `json:"samples,omitempty"`
	// Unreadable is how many book rows could not be decoded; their hashes are
	// not indexed and, while any exist, the sentinel is not set.
	Unreadable       int      `json:"unreadable"`
	SampleUnreadable []string `json:"sample_unreadable,omitempty"`
	// Trusted is whether the sentinel is set when the run ends.
	Trusted bool `json:"trusted"`
	// LegacySwept is true when a repair deleted the legacy single-valued keys.
	LegacySwept bool `json:"legacy_swept"`
}

// Missing is how many keys were absent, over all three indexes.
func (r BookHashIndexReport) Missing() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Missing
	}
	return n
}

// Written is how many keys a repair wrote.
func (r BookHashIndexReport) Written() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Written
	}
	return n
}

// BookHashIndexProgress is called with the phase ("scanning books") and how
// many items of it are done and how many there are (0 when unknown). It runs
// inside worker goroutines, concurrently, and may be nil.
type BookHashIndexProgress func(phase string, done, total int)

// CheckBookHashIndex reads the whole library and reports which keys of the
// three file-hash indexes are missing. It writes nothing.
func (p *PebbleStore) CheckBookHashIndex(ctx context.Context, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	return p.reconcileBookHashIndex(ctx, false, progress)
}

// RepairBookHashIndex is CheckBookHashIndex that also writes every missing key.
// When every book row decoded it then sets the sentinel and deletes the legacy
// single-valued keys, in that order (see the file comment). It never deletes a
// new-family key and never writes a book row. Idempotent.
func (p *PebbleStore) RepairBookHashIndex(ctx context.Context, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	return p.reconcileBookHashIndex(ctx, true, progress)
}

type bookHashRowBatch []struct {
	id    string
	value []byte
}

const (
	bookHashBatchRows  = 512
	bookHashProgressEv = 5000
)

// bookHashLocal is one worker's tally.
type bookHashLocal struct {
	kinds         [len(bookHashKinds)]BookHashKindReport
	live          int
	unreadable    int
	unreadableIDs []string
	samples       []BookHashIndexIssue
}

// reconcileBookHashIndex reads every book row once and makes sure each hash a
// row holds has its key. The iterator is one sequential read; the decoding and
// the point reads and writes, which are the cost, are spread over the workers.
// Workers touch disjoint rows, and a key is a pure function of (hash, id), so
// no two workers write the same key. A row changed or deleted after it was read
// can leave an extra key behind, which readers verify and ignore.
func (p *PebbleStore) reconcileBookHashIndex(ctx context.Context, apply bool, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	rep := BookHashIndexReport{Apply: apply, Kinds: make([]BookHashKindReport, len(bookHashKinds))}
	for i := range bookHashKinds {
		rep.Kinds[i].Kind = bookHashKinds[i].name
	}

	workers := runtime.NumCPU()
	g, gctx := errgroup.WithContext(ctx)
	batches := make(chan bookHashRowBatch, workers)
	var scanned atomic.Int64
	var mergeMu sync.Mutex

	for range workers {
		g.Go(func() error {
			var local bookHashLocal
			var wb *GatedBatch
			pending := 0
			flush := func() error {
				if wb == nil {
					return nil
				}
				defer func() { wb.Close(); wb, pending = nil, 0 }()
				if err := wb.Commit(pebble.Sync); err != nil {
					return fmt.Errorf("write book hash index keys: %w", err)
				}
				return nil
			}
			defer func() {
				if wb != nil {
					wb.Close()
				}
			}()
			for b := range batches {
				for _, r := range b {
					if err := gctx.Err(); err != nil {
						return err
					}
					var row bookHashRow
					if err := json.Unmarshal(r.value, &row); err != nil {
						local.unreadable++
						if len(local.unreadableIDs) < bookHashUnreadableSampleCap {
							local.unreadableIDs = append(local.unreadableIDs, r.id)
						}
						continue
					}
					if !markedForDeletionFlag(row.MarkedForDeletion) {
						local.live++
					}
					for i := range bookHashKinds {
						h := bookHashKinds[i].ofRow(&row)
						if h == "" {
							continue
						}
						k := &local.kinds[i]
						k.Checked++
						key := bookHashKey(i, h, r.id)
						_, closer, err := p.db.Get(key)
						if err == nil {
							closer.Close()
							k.OK++
							continue
						}
						if !errors.Is(err, pebble.ErrNotFound) {
							return fmt.Errorf("read index key %q: %w", key, err)
						}
						k.Missing++
						if len(local.samples) < bookHashSampleCap {
							local.samples = append(local.samples, BookHashIndexIssue{Kind: bookHashKinds[i].name, Hash: h, BookID: r.id})
						}
						if !apply {
							continue
						}
						if wb == nil {
							wb = p.db.NewBatch()
						}
						if err := wb.Set(key, []byte{}, nil); err != nil {
							return err
						}
						k.Written++
						if pending++; pending >= bookHashBatchRows {
							if err := flush(); err != nil {
								return err
							}
						}
					}
				}
			}
			if err := flush(); err != nil {
				return err
			}
			mergeMu.Lock()
			defer mergeMu.Unlock()
			rep.LiveBooks += local.live
			rep.Unreadable += local.unreadable
			for _, id := range local.unreadableIDs {
				if len(rep.SampleUnreadable) < bookHashUnreadableSampleCap {
					rep.SampleUnreadable = append(rep.SampleUnreadable, id)
				}
			}
			for i := range local.kinds {
				k := &rep.Kinds[i]
				k.Checked += local.kinds[i].Checked
				k.OK += local.kinds[i].OK
				k.Missing += local.kinds[i].Missing
				k.Written += local.kinds[i].Written
			}
			for _, s := range local.samples {
				if len(rep.Samples) < bookHashSampleCap {
					rep.Samples = append(rep.Samples, s)
				}
			}
			return nil
		})
	}

	g.Go(func() error {
		defer close(batches)
		var cur bookHashRowBatch
		send := func() error {
			if len(cur) == 0 {
				return nil
			}
			select {
			case batches <- cur:
				cur = nil
				return nil
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		if err := forEachBookRow(p.db, func(id string, value []byte) error {
			if err := gctx.Err(); err != nil {
				return err
			}
			cur = append(cur, struct {
				id    string
				value []byte
			}{id, bytes.Clone(value)})
			n := int(scanned.Add(1))
			if progress != nil && n%bookHashProgressEv == 0 {
				progress("scanning books", n, 0)
			}
			if len(cur) >= bookHashBatchRows {
				return send()
			}
			return nil
		}); err != nil {
			return fmt.Errorf("scan book rows: %w", err)
		}
		return send()
	})

	if err := g.Wait(); err != nil {
		return rep, err
	}
	rep.Books = int(scanned.Load())
	slices.SortFunc(rep.Samples, func(a, b BookHashIndexIssue) int {
		return cmpStrings(a.Kind+"\x00"+a.Hash+"\x00"+a.BookID, b.Kind+"\x00"+b.Hash+"\x00"+b.BookID)
	})
	slices.Sort(rep.SampleUnreadable)

	trusted, err := p.bookHashIndexTrusted()
	if err != nil {
		return rep, err
	}
	if apply && rep.Unreadable == 0 {
		// New keys complete (every batch above committed with Sync), then the
		// sentinel, then the legacy deletion. Never in another order.
		if !trusted {
			if err := p.db.Set([]byte(bookHashIndexSentinel), []byte("1"), pebble.Sync); err != nil {
				return rep, fmt.Errorf("set %s: %w", bookHashIndexSentinel, err)
			}
			p.bookHashTrusted.Store(true)
			trusted = true
		}
		swept, err := p.sweepLegacyBookHashKeys()
		if err != nil {
			return rep, err
		}
		rep.LegacySwept = swept
	}
	rep.Trusted = trusted
	return rep, nil
}

func cmpStrings(a, b string) int { return bytes.Compare([]byte(a), []byte(b)) }

// sweepLegacyBookHashKeys deletes the retired single-valued keys. It only
// writes when one exists, so the boot ensure does not add range tombstones on
// every start. Callers must have set the sentinel first.
func (p *PebbleStore) sweepLegacyBookHashKeys() (bool, error) {
	swept := false
	for i := range bookHashKinds {
		lower := []byte(bookHashKinds[i].legacyPrefix)
		upper := bareRowUpperBound(bookHashKinds[i].legacyPrefix)
		iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
		if err != nil {
			return swept, err
		}
		found := iter.First()
		ierr := iter.Error()
		iter.Close()
		if ierr != nil {
			return swept, ierr
		}
		if !found {
			continue
		}
		if err := p.db.DeleteRange(lower, upper, pebble.Sync); err != nil {
			return swept, fmt.Errorf("delete legacy %s keys: %w", bookHashKinds[i].legacyPrefix, err)
		}
		swept = true
		bookHashIndexLog.Info("deleted the legacy single-valued keys %s", bookHashKinds[i].legacyPrefix)
	}
	return swept, nil
}

// readBookHashKey returns what a legacy single-valued index key holds.
func (p *PebbleStore) readBookHashKey(key []byte) (value string, present bool, err error) {
	v, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read index key %q: %w", key, err)
	}
	defer closer.Close()
	return string(v), true, nil
}
