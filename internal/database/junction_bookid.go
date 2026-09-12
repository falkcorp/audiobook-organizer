// file: internal/database/junction_bookid.go
// version: 1.1.0
// guid: 073892d4-fddf-47bf-a5aa-be1665175519
// last-edited: 2026-09-12

package database

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

// The book_authors and book_narrators junctions are stored one Pebble key per
// book: book_authors:<bookID> / book_narrators:<bookID>, value = a JSON array of
// rows. Each row ALSO carries a book_id field, and the two are not allowed to
// disagree: memdb's primary index on both tables is a non-AllowMissing compound
// {BookID, AuthorID|NarratorID}, so a row whose BookID is empty is rejected
// ("object missing primary index") and one whose BookID names another book is
// indexed under the wrong book.
//
// Until 2026-09-12 nothing enforced it. SetBookAuthors / SetBookNarrators wrote
// caller rows to Pebble verbatim, and two callers omitted book_id: the
// narrator-split in POST /operations/optimize-database, and PUT
// /audiobooks/:id/narrators, which binds client JSON straight through. The
// Pebble write succeeded, the setter returned nil -- and from then on every
// UpsertBookToMemDB for that book reloaded the row from Pebble, failed the
// insert, and aborted the WHOLE transaction (book row, authors, narrators,
// files), so memdb's copy of the book went stale on every later update. Warmup
// dropped the row too, flagging the table incomplete for the process lifetime.
//
// The key is the source of truth: it is what GetBookAuthors(bookID) looks up.
// So the store stamps the key's book ID onto every row -- on write (the
// setters), on read (the getters, so a row damaged before this fix cannot
// poison a memdb reload), at warmup, and once, durably, via migration 63
// (RepairJunctionBookIDs below).

// stampBookAuthorsInPlace forces every row's BookID to bookID. It returns how
// many rows carried a NON-EMPTY BookID naming a different book -- the case a
// setter logs, because an empty one is just an omitted field while a different
// one means the caller built rows for another book and asked to store them
// here.
func stampBookAuthorsInPlace(bookID string, rows []BookAuthor) (mismatched int) {
	for i := range rows {
		if rows[i].BookID == bookID {
			continue
		}
		if rows[i].BookID != "" {
			mismatched++
		}
		rows[i].BookID = bookID
	}
	return mismatched
}

// stampBookNarratorsInPlace is the narrator twin of stampBookAuthorsInPlace.
func stampBookNarratorsInPlace(bookID string, rows []BookNarrator) (mismatched int) {
	for i := range rows {
		if rows[i].BookID == bookID {
			continue
		}
		if rows[i].BookID != "" {
			mismatched++
		}
		rows[i].BookID = bookID
	}
	return mismatched
}

// JunctionBookIDRepair reports what RepairJunctionBookIDs did.
type JunctionBookIDRepair struct {
	AuthorKeysScanned    int `json:"author_keys_scanned"`
	AuthorKeysRepaired   int `json:"author_keys_repaired"`
	AuthorRowsRepaired   int `json:"author_rows_repaired"`
	NarratorKeysScanned  int `json:"narrator_keys_scanned"`
	NarratorKeysRepaired int `json:"narrator_keys_repaired"`
	NarratorRowsRepaired int `json:"narrator_rows_repaired"`
	// Undecodable counts junction values that are not a JSON row array. They
	// are left exactly as found (this repair has no correct value to write for
	// them) and logged by key.
	Undecodable int `json:"undecodable"`
}

// RepairJunctionBookIDs rewrites every book_authors:<id> and
// book_narrators:<id> value whose rows carry a book_id other than <id>,
// stamping <id> onto them, then replays the repaired sets into memdb if memdb
// is live (see memdbAcceptsDirectWrites for why not during warmup).
//
// It is deterministic and lossless: the correct value is in the key, and only
// the book_id field changes. Keys whose rows already agree are not written, so
// a second run scans, finds nothing and writes nothing (the MigrationFunc
// idempotency contract).
//
// One sequential iterator per prefix, the same shape as
// SweepHollowOperationsV2: the per-key work is a JSON decode and a string
// compare, there is no I/O to overlap, and the writes go into one batch, so a
// worker pool would add contention and nothing else.
func (p *PebbleStore) RepairJunctionBookIDs() (res JunctionBookIDRepair, err error) {
	defer recoverPebbleClosed("RepairJunctionBookIDs", &err)

	batch := p.db.NewBatch()
	defer batch.Close()

	authorFixes := map[string][]BookAuthor{}
	if err := p.scanJunction("book_authors:", &res.AuthorKeysScanned, &res.Undecodable, func(bookID string, val []byte) ([]byte, error) {
		var rows []BookAuthor
		if err := json.Unmarshal(val, &rows); err != nil {
			return nil, err
		}
		fixed := 0
		for i := range rows {
			if rows[i].BookID != bookID {
				fixed++
			}
		}
		if fixed == 0 {
			return nil, nil
		}
		stampBookAuthorsInPlace(bookID, rows)
		out, err := json.Marshal(rows)
		if err != nil {
			return nil, err
		}
		// Count only once the rewrite is certain to be staged: these counters
		// are what migration 63 reports in the startup log.
		res.AuthorKeysRepaired++
		res.AuthorRowsRepaired += fixed
		authorFixes[bookID] = rows
		return out, nil
	}, batch); err != nil {
		return res, err
	}

	narratorFixes := map[string][]BookNarrator{}
	if err := p.scanJunction("book_narrators:", &res.NarratorKeysScanned, &res.Undecodable, func(bookID string, val []byte) ([]byte, error) {
		var rows []BookNarrator
		if err := json.Unmarshal(val, &rows); err != nil {
			return nil, err
		}
		fixed := 0
		for i := range rows {
			if rows[i].BookID != bookID {
				fixed++
			}
		}
		if fixed == 0 {
			return nil, nil
		}
		stampBookNarratorsInPlace(bookID, rows)
		out, err := json.Marshal(rows)
		if err != nil {
			return nil, err
		}
		res.NarratorKeysRepaired++
		res.NarratorRowsRepaired += fixed
		narratorFixes[bookID] = rows
		return out, nil
	}, batch); err != nil {
		return res, err
	}

	if len(authorFixes) == 0 && len(narratorFixes) == 0 {
		return res, nil
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return res, fmt.Errorf("commit junction book_id repair: %w", err)
	}
	// Pebble is now correct. Bring memdb in line ONLY when it is live.
	//
	// At startup this migration runs while the async warmup is still in
	// flight, and memSync then BUFFERS every write-through for replay. That
	// buffer is capped (memPendingOpCap, 50,000), and overflowing it abandons
	// memdb for the whole process. One write per repaired key would blow that
	// cap on a library where tens of thousands of books were damaged, so the
	// first boot after this fix could run with memdb off. They would also be
	// redundant: the warmup closures stamp the key's book ID themselves
	// (memdb_warmup.go), so whether warmup's iterator saw the damaged value or
	// the repaired one, it loads the same stamped rows. When memdb is
	// disabled or abandoned the writes would be dropped anyway.
	if !p.memdbAcceptsDirectWrites() {
		return res, nil
	}
	for bookID, rows := range authorFixes {
		p.ReplaceBookAuthorsInMemDB(bookID, rows)
	}
	for bookID, rows := range narratorFixes {
		p.ReplaceBookNarratorsInMemDB(bookID, rows)
	}
	return res, nil
}

// memdbAcceptsDirectWrites reports whether a memSync issued now would be
// applied straight to a published memdb: no warmup is buffering and memdb is
// published. It reads the same state memSyncWithStore branches on; it adds
// no flag. The answer can go stale the moment the lock is released, and both
// ways that can happen are safe for RepairJunctionBookIDs: a warmup that
// starts afterwards (Reset) re-scans the committed, repaired keys, and a
// memdb that gets abandoned simply drops the writes.
func (p *PebbleStore) memdbAcceptsDirectWrites() bool {
	p.memPending.mu.Lock()
	defer p.memPending.mu.Unlock()
	return p.memPending.state == memPendingInactive && p.mem() != nil
}

// scanJunction iterates every key under prefix. For each, fix receives the
// book ID taken from the key and the raw value, and returns either nil (leave
// the key alone) or the replacement value, which is staged on batch. A value
// fix cannot decode is counted in undecodable, logged, and left untouched.
func (p *PebbleStore) scanJunction(prefix string, scanned, undecodable *int, fix func(bookID string, val []byte) ([]byte, error), batch *pebble.Batch) error {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: prefixUpperBound([]byte(prefix)),
	})
	if err != nil {
		return fmt.Errorf("pebble iterate %s: %w", prefix, err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key()) // string() copies; the iterator reuses its buffer
		bookID := strings.TrimPrefix(key, prefix)
		*scanned++
		if bookID == "" {
			continue // no book ID to stamp; nothing a key-derived repair can do
		}
		newVal, fixErr := fix(bookID, iter.Value())
		if fixErr != nil {
			*undecodable++
			slog.Warn("junction book_id repair: undecodable value left as-is", "key", key, "error", fixErr)
			continue
		}
		if newVal == nil {
			continue
		}
		if err := batch.Set([]byte(key), newVal, nil); err != nil {
			_ = iter.Close()
			return fmt.Errorf("stage %s: %w", key, err)
		}
	}
	return iter.Close()
}
