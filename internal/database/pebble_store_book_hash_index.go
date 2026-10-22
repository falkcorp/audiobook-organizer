// file: internal/database/pebble_store_book_hash_index.go
// version: 1.0.0
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
)

// The three single-valued file-hash indexes, book:hash:<h>, book:originalhash:<h>
// and book:organizedhash:<h>, each hold ONE book id per hash (last writer wins;
// GetBookByFileHash, GetBookByOriginalHash and GetBookByOrganizedHash read
// them). DeleteBook used to delete them unconditionally, so removing a book
// erased the entry of the live book that owned the same hash. The purge of
// about 2,520 combine shells on 2026-10-21 may have done exactly that, and a
// missing entry is silent: the scanner and importers asking "is this file
// already in the library" get "no".
//
// CheckBookHashIndex reports, and RepairBookHashIndex repairs, every entry a
// LIVE book's hash should have and does not:
//
//   - missing: no key at all;
//   - dangling: the key names a book whose row is gone;
//   - wrong holder: the key names a book that exists but does not hold that
//     hash (it was changed since) ;
//   - trashed owner: the key names a trashed book that holds the hash while a
//     live book also holds it.
//
// A repair points the key at the live holder with the smallest id (a stable
// choice, so two runs agree). It never touches a key that already names a live
// holder, never deletes a key, and never writes a book row. A key naming a row
// that cannot be decoded is counted Unreadable and left alone: overwriting it
// would be a guess. A key that changed between the check and the write is left
// as it is and counted Raced; the next run sees it.
//
// A key with NO live holder (a dangling entry for a hash nobody holds) is not
// examined: the pass is driven by the live books' hashes, which is where the
// damage is.

// BookHashKindReport is one index's figures.
type BookHashKindReport struct {
	// Kind is "hash", "originalhash" or "organizedhash".
	Kind string `json:"kind"`
	// Checked is how many distinct hashes live books hold under this index.
	Checked int `json:"checked"`
	OK      int `json:"ok"`
	Missing int `json:"missing"`
	// Dangling: the key names a book that no longer exists.
	Dangling int `json:"dangling"`
	// WrongHolder: the key names a book that does not hold the hash.
	WrongHolder int `json:"wrong_holder"`
	// TrashedOwner: the key names a trashed book while a live one holds it too.
	TrashedOwner int `json:"trashed_owner"`
	// Unreadable: the key names a row that cannot be decoded; left alone.
	Unreadable int `json:"unreadable"`
	// Repointed: keys written (a repair only).
	Repointed int `json:"repointed"`
	// Raced: keys that changed between the check and the write; left alone.
	Raced int `json:"raced"`
}

func (k BookHashKindReport) broken() int {
	return k.Missing + k.Dangling + k.WrongHolder + k.TrashedOwner
}

// BookHashIndexIssue is one broken entry, for the sample in a report.
type BookHashIndexIssue struct {
	Kind  string `json:"kind"`
	Hash  string `json:"hash"`
	Class string `json:"class"`
	// Names is what the key held ("" when it was absent).
	Names string `json:"names,omitempty"`
	// LiveHolder is the book the key is (or would be) pointed at.
	LiveHolder string `json:"live_holder"`
}

// BookHashIndexReport is the outcome of one check or repair.
type BookHashIndexReport struct {
	// Apply is true for a repair, false for the read-only check.
	Apply     bool `json:"apply"`
	Books     int  `json:"books"`
	LiveBooks int  `json:"live_books"`
	// Kinds has one entry per index, in the order hash, originalhash, organizedhash.
	Kinds []BookHashKindReport `json:"kinds"`
	// Samples is the first few broken entries (bookHashSampleCap).
	Samples []BookHashIndexIssue `json:"samples,omitempty"`
}

// Broken is how many entries are (or, before a repair, were) broken, over all
// three indexes. Unreadable is not counted: those are left alone.
func (r BookHashIndexReport) Broken() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.broken()
	}
	return n
}

// Repointed is how many keys a repair wrote.
func (r BookHashIndexReport) Repointed() int {
	n := 0
	for _, k := range r.Kinds {
		n += k.Repointed
	}
	return n
}

const bookHashSampleCap = 20

// BookHashIndexProgress is called with the phase ("scanning books", "checking
// entries") and how many items of it are done and how many there are (0 when
// unknown). It runs inside worker goroutines, concurrently, and may be nil.
type BookHashIndexProgress func(phase string, done, total int)

var bookHashKinds = [...]struct {
	name   string
	prefix string
	of     func(*bookHashRow) string
}{
	{"hash", "book:hash:", func(r *bookHashRow) string { return derefStr(r.FileHash) }},
	{"originalhash", "book:originalhash:", func(r *bookHashRow) string { return derefStr(r.OriginalFileHash) }},
	{"organizedhash", "book:organizedhash:", func(r *bookHashRow) string { return derefStr(r.OrganizedFileHash) }},
}

// bookHashRow is the part of a book row the hash indexes depend on.
type bookHashRow struct {
	FileHash          *string `json:"file_hash"`
	OriginalFileHash  *string `json:"original_file_hash"`
	OrganizedFileHash *string `json:"organized_file_hash"`
	MarkedForDeletion *bool   `json:"marked_for_deletion"`
}

// CheckBookHashIndex reads the whole library and reports which entries of the
// three file-hash indexes are broken. It writes nothing.
func (p *PebbleStore) CheckBookHashIndex(ctx context.Context, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	return p.runBookHashIndex(ctx, false, progress)
}

// RepairBookHashIndex is CheckBookHashIndex that also points each broken key
// at its live holder (see the file comment for what it does and never does).
// The report carries the figures as they were found, plus what was written.
func (p *PebbleStore) RepairBookHashIndex(ctx context.Context, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	return p.runBookHashIndex(ctx, true, progress)
}

// bookHashHolders maps, per index, a hash to the ids of the live books holding it.
type bookHashHolders [len(bookHashKinds)]map[string][]string

func newBookHashHolders() *bookHashHolders {
	var h bookHashHolders
	for i := range h {
		h[i] = map[string][]string{}
	}
	return &h
}

type bookHashRowBatch []struct {
	id    string
	value []byte
}

const (
	bookHashBatchRows  = 512
	bookHashTaskChunk  = 1000
	bookHashProgressEv = 5000
)

// collectLiveBookHashes reads every book row once and returns the live books'
// holders. The iterator is one sequential read; the JSON decoding, which is
// the CPU cost, is spread over the workers.
func (p *PebbleStore) collectLiveBookHashes(ctx context.Context, progress BookHashIndexProgress) (holders *bookHashHolders, books, live int, err error) {
	workers := runtime.NumCPU()
	g, gctx := errgroup.WithContext(ctx)
	batches := make(chan bookHashRowBatch, workers)

	var scanned, liveN atomic.Int64
	merged := newBookHashHolders()
	var mergeMu sync.Mutex

	for range workers {
		g.Go(func() error {
			local := newBookHashHolders()
			for b := range batches {
				for _, r := range b {
					var row bookHashRow
					if err := json.Unmarshal(r.value, &row); err != nil {
						// An undecodable book row holds no hash we can read; the
						// other book scans fail on it loudly, this one must not
						// turn a repair of the rest into a failure.
						continue
					}
					if markedForDeletionFlag(row.MarkedForDeletion) {
						continue
					}
					liveN.Add(1)
					for i := range bookHashKinds {
						if h := bookHashKinds[i].of(&row); h != "" {
							local[i][h] = append(local[i][h], r.id)
						}
					}
				}
			}
			mergeMu.Lock()
			defer mergeMu.Unlock()
			for i := range local {
				for h, ids := range local[i] {
					merged[i][h] = append(merged[i][h], ids...)
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
		return nil, 0, 0, err
	}
	for i := range merged {
		for _, ids := range merged[i] {
			slices.Sort(ids)
		}
	}
	return merged, int(scanned.Load()), int(liveN.Load()), nil
}

// bookHashTask is one (index, hash) pair a live book holds.
type bookHashTask struct {
	kind    int
	hash    string
	holders []string
}

func (p *PebbleStore) runBookHashIndex(ctx context.Context, apply bool, progress BookHashIndexProgress) (BookHashIndexReport, error) {
	rep := BookHashIndexReport{Apply: apply, Kinds: make([]BookHashKindReport, len(bookHashKinds))}
	for i := range bookHashKinds {
		rep.Kinds[i].Kind = bookHashKinds[i].name
	}
	holders, books, live, err := p.collectLiveBookHashes(ctx, progress)
	if err != nil {
		return rep, err
	}
	rep.Books, rep.LiveBooks = books, live

	var tasks []bookHashTask
	for i := range holders {
		rep.Kinds[i].Checked = len(holders[i])
		for h, ids := range holders[i] {
			tasks = append(tasks, bookHashTask{kind: i, hash: h, holders: ids})
		}
	}
	// A stable order, so the sample and the progress read the same run to run.
	slices.SortFunc(tasks, func(a, b bookHashTask) int {
		if a.kind != b.kind {
			return a.kind - b.kind
		}
		return bytes.Compare([]byte(a.hash), []byte(b.hash))
	})

	var mu sync.Mutex
	var done atomic.Int64
	total := len(tasks)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for start := 0; start < total; start += bookHashTaskChunk {
		chunk := tasks[start:min(start+bookHashTaskChunk, total)]
		g.Go(func() error {
			local := make([]BookHashKindReport, len(bookHashKinds))
			var samples []BookHashIndexIssue
			for _, t := range chunk {
				if err := gctx.Err(); err != nil {
					return err
				}
				issue, err := p.checkOrRepairBookHash(t, apply, &local[t.kind])
				if err != nil {
					return err
				}
				if issue != nil && len(samples) < bookHashSampleCap {
					samples = append(samples, *issue)
				}
			}
			mu.Lock()
			for i := range local {
				k := &rep.Kinds[i]
				k.OK += local[i].OK
				k.Missing += local[i].Missing
				k.Dangling += local[i].Dangling
				k.WrongHolder += local[i].WrongHolder
				k.TrashedOwner += local[i].TrashedOwner
				k.Unreadable += local[i].Unreadable
				k.Repointed += local[i].Repointed
				k.Raced += local[i].Raced
			}
			for _, s := range samples {
				if len(rep.Samples) < bookHashSampleCap {
					rep.Samples = append(rep.Samples, s)
				}
			}
			mu.Unlock()
			if n := int(done.Add(int64(len(chunk)))); progress != nil {
				progress("checking entries", n, total)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return rep, err
	}
	slices.SortFunc(rep.Samples, func(a, b BookHashIndexIssue) int {
		if c := bytes.Compare([]byte(a.Kind), []byte(b.Kind)); c != 0 {
			return c
		}
		return bytes.Compare([]byte(a.Hash), []byte(b.Hash))
	})
	return rep, nil
}

// checkOrRepairBookHash classifies one entry into out and, when apply is set
// and it is broken, points the key at the live holder with the smallest id.
// It returns the issue for the sample (nil when the entry is fine or left
// alone).
func (p *PebbleStore) checkOrRepairBookHash(t bookHashTask, apply bool, out *BookHashKindReport) (*BookHashIndexIssue, error) {
	kind := bookHashKinds[t.kind]
	key := []byte(kind.prefix + t.hash)
	names, present, err := p.readBookHashKey(key)
	if err != nil {
		return nil, err
	}
	class := ""
	switch {
	case !present:
		class = "missing"
		out.Missing++
	case slices.Contains(t.holders, names):
		out.OK++
		return nil, nil
	default:
		row, found, rerr := p.readBookHashRow(names)
		switch {
		case rerr != nil:
			// Cannot decode what the key names: a guess would overwrite
			// someone's entry. Counted, left alone.
			out.Unreadable++
			return nil, nil
		case !found:
			class = "dangling"
			out.Dangling++
		case markedForDeletionFlag(row.MarkedForDeletion) && kind.of(row) == t.hash:
			class = "trashed_owner"
			out.TrashedOwner++
		default:
			class = "wrong_holder"
			out.WrongHolder++
		}
	}
	chosen := t.holders[0]
	issue := &BookHashIndexIssue{Kind: kind.name, Hash: t.hash, Class: class, Names: names, LiveHolder: chosen}
	if !apply {
		return issue, nil
	}
	raced, err := p.repointBookHashKey(key, names, present, chosen)
	if err != nil {
		return nil, err
	}
	if raced {
		out.Raced++
	} else {
		out.Repointed++
	}
	return issue, nil
}

// readBookHashKey returns what a hash index key holds.
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

// readBookHashRow reads the hash-relevant part of book id's row. found is
// false when there is no row; err is a decode failure only.
func (p *PebbleStore) readBookHashRow(id string) (row *bookHashRow, found bool, err error) {
	v, closer, gerr := p.db.Get([]byte("book:" + id))
	if errors.Is(gerr, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if gerr != nil {
		return nil, false, gerr
	}
	defer closer.Close()
	var r bookHashRow
	if err := json.Unmarshal(v, &r); err != nil {
		return nil, true, err
	}
	return &r, true, nil
}

// repointBookHashKey sets key to chosen if it still holds what the check read
// (observed, or nothing when it was absent). raced is true, and nothing is
// written, when it changed meanwhile.
func (p *PebbleStore) repointBookHashKey(key []byte, observed string, wasPresent bool, chosen string) (raced bool, err error) {
	cur, present, err := p.readBookHashKey(key)
	if err != nil {
		return false, err
	}
	if present != wasPresent || cur != observed {
		return true, nil
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(key, []byte(chosen), nil); err != nil {
		return false, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, fmt.Errorf("repoint index key %q: %w", key, err)
	}
	return false, nil
}
