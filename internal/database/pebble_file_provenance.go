// file: internal/database/pebble_file_provenance.go
// version: 1.1.0
// guid: 2e8b6d05-91af-4c37-8b14-6d3a7f9e2c50
// last-edited: 2026-08-21

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Key layout
//
//	file_prov:<book_file_id>:<ts>   -> FileEvent   (the chain, one per file)
//	file_prov_hash:<hash>:<ts>      -> FileEvent   (index: any hash -> events)
//	file_prov_orphan:<sha_full>:<ts>-> FileEvent   (seen before it had a row)
//
// <ts> is a zero-padded UnixNano, matching path_history above it, so a prefix
// scan returns events in chronological order for free.
//
// The hash index is written for BOTH the full and chunked SHA. They are
// different functions over the same bytes and rows in the wild carry one or
// the other, so indexing only one would silently fail to resolve half the
// library.
const (
	fileProvPrefix       = "file_prov:"
	fileProvHashPrefix   = "file_prov_hash:"
	fileProvOrphanPrefix = "file_prov_orphan:"
	// fileProvSeqPrefix indexes the store-wide sequence. Its value is the
	// PRIMARY KEY of the event, not another copy of it: the export reads
	// through the pointer, so re-chaining an event in place is picked up
	// automatically and the index costs ~60 bytes instead of ~600.
	fileProvSeqPrefix = "file_prov_seq:"
	// fileProvSeqCounterKey holds the last sequence number handed out. It is
	// incremented in the SAME batch as the event, so a crash cannot burn a
	// number — a burned number is indistinguishable from a deleted row, and
	// would make the gap detector cry wolf.
	fileProvSeqCounterKey = "counter:file_prov_seq"
	// fileProvExportCursorKey holds the highest sequence the JSONL export has
	// written. Durable so the export resumes rather than restarts.
	fileProvExportCursorKey = "cursor:file_prov_export"
)

// FileEventSeqRow pairs an event with the sequence slot it occupies.
type FileEventSeqRow struct {
	Seq   uint64
	Event FileEvent
}

// uniqueTSKey returns key(ts) for the first ts at or after the given one that
// is not already present. Two events recorded in the same nanosecond would
// otherwise silently overwrite each other, and this ledger's whole value is
// that it never loses an entry.
func (p *PebbleStore) uniqueTSKey(build func(ts int64) []byte, ts int64) []byte {
	for range 1000 {
		key := build(ts)
		if _, closer, err := p.db.Get(key); err != nil {
			// Not found (or unreadable) — the slot is ours.
			return key
		} else {
			closer.Close()
		}
		ts++
	}
	return build(ts)
}

// chainPrefix returns the keyspace an event belongs in: its book_file's chain,
// or the orphan bucket keyed by full SHA when it has no row yet.
func chainPrefix(e FileEvent) []byte {
	if e.BookFileID != "" {
		return fmt.Appendf(nil, "%s%s:", fileProvPrefix, e.BookFileID)
	}
	return fmt.Appendf(nil, "%s%s:", fileProvOrphanPrefix, e.Digest.SHA256Full)
}

// lastChainHash returns the Hash of the chain's newest event BY SEQUENCE.
//
// By sequence, not by key. Keys encode event time, and callers append out of
// time order routinely — a pre-write observation recorded after the fact, an
// orphan adopted into the middle. The chain records the order things were
// written, so its tip is the highest Seq, and a fresh append (which always
// takes the next Seq store-wide) links onto it without ever needing to
// re-chain what is already there.
//
// An unchained tip yields empty on purpose: it cannot vouch for what follows
// it, and VerifyFileChain expects the successor's PrevHash to be empty there.
func (p *PebbleStore) lastChainHash(prefix []byte) (string, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return "", err
	}
	defer iter.Close()

	var bestSeq uint64
	var bestHash string
	var seen bool
	for iter.First(); iter.Valid(); iter.Next() {
		var e FileEvent
		if err := json.Unmarshal(iter.Value(), &e); err != nil {
			// A corrupt row must not silently chain the next event to nothing
			// and call it fine; refuse rather than paper over it.
			return "", fmt.Errorf("lastChainHash: corrupt event in chain: %w", err)
		}
		if !seen || e.Seq >= bestSeq {
			bestSeq, bestHash, seen = e.Seq, e.Hash, true
		}
	}
	return bestHash, iter.Error()
}

// peekFileProvSeq reads the last sequence number handed out (0 = none yet).
func (p *PebbleStore) peekFileProvSeq() (uint64, error) {
	val, closer, err := p.db.Get([]byte(fileProvSeqCounterKey))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = closer.Close() }()
	n, perr := strconv.ParseUint(string(val), 10, 64)
	if perr != nil {
		return 0, fmt.Errorf("file provenance sequence counter is not a number: %w", perr)
	}
	return n, nil
}

// seqIndexKey renders the fixed-width sequence key so a scan is ordered.
func seqIndexKey(seq uint64) []byte {
	return fmt.Appendf(nil, "%s%020d", fileProvSeqPrefix, seq)
}

// AppendFileEvent durably records one event in the provenance chain.
//
// Serialized on fileProvMu. Two concurrent appends would otherwise read the
// same chain tip and the same sequence counter, then both write — forking the
// chain and duplicating a sequence number. The write rate here is a handful of
// events per file operation, so a single mutex costs nothing that matters.
func (p *PebbleStore) AppendFileEvent(e FileEvent) error {
	if e.Kind == "" {
		return fmt.Errorf("AppendFileEvent: kind is required")
	}
	if e.BookFileID == "" && e.Digest.SHA256Full == "" {
		// An orphan with no full hash can never be adopted, so it would be
		// write-only data. Refuse it rather than silently accumulating rows
		// nothing can ever read back.
		return fmt.Errorf("AppendFileEvent: an event with no book_file_id requires digest.sha256_full")
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}

	p.fileProvMu.Lock()
	defer p.fileProvMu.Unlock()

	prefix := chainPrefix(e)
	prevHash, err := p.lastChainHash(prefix)
	if err != nil {
		return fmt.Errorf("AppendFileEvent: read chain tip: %w", err)
	}
	lastSeq, err := p.peekFileProvSeq()
	if err != nil {
		return fmt.Errorf("AppendFileEvent: read sequence: %w", err)
	}

	e.Seq = lastSeq + 1
	e.PrevHash = prevHash
	e.Hash = e.ComputeHash()

	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("AppendFileEvent: marshal: %w", err)
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	ts := e.At.UnixNano()
	primary := p.uniqueTSKey(func(t int64) []byte {
		return fmt.Appendf(nil, "%s%019d", prefix, t)
	}, ts)
	if err := batch.Set(primary, data, nil); err != nil {
		return fmt.Errorf("AppendFileEvent: set chain: %w", err)
	}

	// Index every hash this event carries, so a lookup by a hash the file used
	// to have still resolves after the bytes have moved on.
	for _, h := range []string{e.Digest.SHA256Full, e.Digest.SHA256Chunk} {
		if h == "" {
			continue
		}
		key := p.uniqueTSKey(func(t int64) []byte {
			return fmt.Appendf(nil, "%s%s:%019d", fileProvHashPrefix, h, t)
		}, ts)
		if err := batch.Set(key, data, nil); err != nil {
			return fmt.Errorf("AppendFileEvent: set hash index: %w", err)
		}
	}

	// The sequence index and its counter ride in the SAME batch as the event.
	// Split across two commits, a crash between them burns a number, and a
	// burned number reads exactly like a deleted row.
	if err := batch.Set(seqIndexKey(e.Seq), primary, nil); err != nil {
		return fmt.Errorf("AppendFileEvent: set seq index: %w", err)
	}
	if err := batch.Set([]byte(fileProvSeqCounterKey),
		[]byte(strconv.FormatUint(e.Seq, 10)), nil); err != nil {
		return fmt.Errorf("AppendFileEvent: set seq counter: %w", err)
	}

	// Sync: a provenance record written before a mutation is worthless if it
	// is still sitting in a memtable when the process dies during that
	// mutation. This is the one place the durability cost is the point.
	return batch.Commit(pebble.Sync)
}

// scanEvents collects every FileEvent under a key prefix, oldest first.
func (p *PebbleStore) scanEvents(prefix []byte) ([]FileEvent, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []FileEvent
	for iter.First(); iter.Valid(); iter.Next() {
		var e FileEvent
		if err := json.Unmarshal(iter.Value(), &e); err != nil {
			// One corrupt row must not hide the rest of a file's history.
			continue
		}
		out = append(out, e)
	}
	return out, iter.Error()
}

// GetFileHistory returns every event for a book_file, oldest first.
func (p *PebbleStore) GetFileHistory(bookFileID string) ([]FileEvent, error) {
	if bookFileID == "" {
		return nil, fmt.Errorf("GetFileHistory: bookFileID is required")
	}
	return p.scanEvents(fmt.Appendf(nil, "%s%s:", fileProvPrefix, bookFileID))
}

// FindFileEventsByHash resolves any historical SHA to the events carrying it.
func (p *PebbleStore) FindFileEventsByHash(hash string) ([]FileEvent, error) {
	if hash == "" {
		return nil, fmt.Errorf("FindFileEventsByHash: hash is required")
	}
	return p.scanEvents(fmt.Appendf(nil, "%s%s:", fileProvHashPrefix, hash))
}

// AdoptOrphanEvents attaches observations made before the file had a row.
//
// The orphan rows are rewritten under the book_file's chain rather than
// copied, so a file observed on disk, then imported, reads back as one
// continuous history instead of two disconnected halves.
//
// This is the ONE sanctioned mutation in an otherwise append-only store, and
// it necessarily re-chains: an adopted event keeps its original At, so it
// sorts INTO the middle of the target chain, and every event after it must be
// re-linked or the chain would read as broken in key order. The whole target
// chain is therefore rewritten in place — same keys, same Seq values,
// recomputed PrevHash/Hash. Chains are a handful of events long, so this is
// cheap, and confining it to one chokepoint is what keeps "a broken link means
// something rewrote history" true everywhere else.
func (p *PebbleStore) AdoptOrphanEvents(bookFileID, sha256Full string) (int, error) {
	if bookFileID == "" || sha256Full == "" {
		return 0, fmt.Errorf("AdoptOrphanEvents: bookFileID and sha256Full are required")
	}

	p.fileProvMu.Lock()
	defer p.fileProvMu.Unlock()

	type pending struct {
		key   []byte
		event FileEvent
	}

	collect := func(prefix []byte) ([]pending, error) {
		iter, err := p.db.NewIter(&pebble.IterOptions{
			LowerBound: prefix,
			UpperBound: prefixEnd(prefix),
		})
		if err != nil {
			return nil, err
		}
		defer iter.Close()
		var out []pending
		for iter.First(); iter.Valid(); iter.Next() {
			var e FileEvent
			if err := json.Unmarshal(iter.Value(), &e); err != nil {
				continue
			}
			out = append(out, pending{key: append([]byte(nil), iter.Key()...), event: e})
		}
		return out, iter.Error()
	}

	orphanPfx := fmt.Appendf(nil, "%s%s:", fileProvOrphanPrefix, sha256Full)
	orphans, err := collect(orphanPfx)
	if err != nil {
		return 0, err
	}
	if len(orphans) == 0 {
		return 0, nil
	}

	chainPfx := fmt.Appendf(nil, "%s%s:", fileProvPrefix, bookFileID)
	existing, err := collect(chainPfx)
	if err != nil {
		return 0, err
	}

	// uniqueTSKey consults the DB, which cannot see keys minted earlier in
	// this same call, so track them locally as well or two orphans sharing a
	// nanosecond would land on the same key and one would vanish.
	taken := make(map[string]bool, len(existing)+len(orphans))
	for _, c := range existing {
		taken[string(c.key)] = true
	}
	mintKey := func(ts int64) []byte {
		for range 1000 {
			k := fmt.Appendf(nil, "%s%019d", chainPfx, ts)
			if !taken[string(k)] {
				if _, closer, gerr := p.db.Get(k); gerr != nil {
					taken[string(k)] = true
					return k
				} else {
					_ = closer.Close()
				}
			}
			ts++
		}
		return fmt.Appendf(nil, "%s%019d", chainPfx, ts)
	}

	batch := p.db.NewBatch()
	defer batch.Close()

	merged := append([]pending(nil), existing...)
	for _, o := range orphans {
		moved := o.event
		moved.BookFileID = bookFileID
		newKey := mintKey(moved.At.UnixNano())
		merged = append(merged, pending{key: newKey, event: moved})

		if err := batch.Delete(o.key, nil); err != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: delete orphan: %w", err)
		}
		// Re-point the sequence index at the event's new home. Skip events
		// that predate sequencing — they have no index entry to move.
		if moved.Seq != 0 {
			if err := batch.Set(seqIndexKey(moved.Seq), newKey, nil); err != nil {
				return 0, fmt.Errorf("AdoptOrphanEvents: repoint seq index: %w", err)
			}
		}
	}

	// Re-link in SEQUENCE order — the order things were written — not key
	// order. An adopted event keeps its original Seq, so it slots back into
	// the write history at the point it actually happened.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].event.Seq < merged[j].event.Seq
	})

	var prevHash string
	for i := range merged {
		e := &merged[i].event
		// An event that predates chaining stays unchained: minting a hash for
		// it now would assert an integrity guarantee that was never made.
		if e.Hash == "" {
			prevHash = ""
		} else {
			e.PrevHash = prevHash
			e.Hash = e.ComputeHash()
			prevHash = e.Hash
		}
		data, merr := json.Marshal(*e)
		if merr != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: marshal: %w", merr)
		}
		if err := batch.Set(merged[i].key, data, nil); err != nil {
			return 0, fmt.Errorf("AdoptOrphanEvents: set chain: %w", err)
		}
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("AdoptOrphanEvents: commit: %w", err)
	}
	return len(orphans), nil
}

// MaxFileEventSeq returns the highest sequence number handed out (0 = none).
func (p *PebbleStore) MaxFileEventSeq() (uint64, error) {
	return p.peekFileProvSeq()
}

// ScanFileEventsBySeq returns events in store-wide sequence order, starting
// after afterSeq, capped at limit (0 = uncapped).
//
// It reads through the sequence index rather than copying events into it, so
// an event re-chained by AdoptOrphanEvents is returned in its current form. A
// pointer whose target is missing is returned as a MISSING row rather than
// skipped — a dangling sequence entry is exactly the evidence of deletion this
// index exists to preserve, and silently dropping it would hide the thing.
func (p *PebbleStore) ScanFileEventsBySeq(afterSeq uint64, limit int) ([]FileEventSeqRow, error) {
	lower := seqIndexKey(afterSeq + 1)
	upper := prefixEnd([]byte(fileProvSeqPrefix))
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []FileEventSeqRow
	for iter.First(); iter.Valid(); iter.Next() {
		seq, perr := strconv.ParseUint(string(iter.Key()[len(fileProvSeqPrefix):]), 10, 64)
		if perr != nil {
			continue
		}
		row := FileEventSeqRow{Seq: seq}
		if val, closer, gerr := p.db.Get(append([]byte(nil), iter.Value()...)); gerr == nil {
			_ = json.Unmarshal(val, &row.Event)
			_ = closer.Close()
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, iter.Error()
}

// GetFileProvExportCursor returns the highest sequence already exported.
func (p *PebbleStore) GetFileProvExportCursor() (uint64, error) {
	val, closer, err := p.db.Get([]byte(fileProvExportCursorKey))
	if err == pebble.ErrNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = closer.Close() }()
	return strconv.ParseUint(string(val), 10, 64)
}

// SetFileProvExportCursor advances the export cursor. It never moves
// BACKWARDS: rewinding would make the next run re-append rows the JSONL
// already holds, and an append-only file cannot take that back.
func (p *PebbleStore) SetFileProvExportCursor(seq uint64) error {
	p.fileProvMu.Lock()
	defer p.fileProvMu.Unlock()
	cur, err := p.GetFileProvExportCursor()
	if err != nil {
		return err
	}
	if seq <= cur {
		return nil
	}
	return p.db.Set([]byte(fileProvExportCursorKey),
		[]byte(strconv.FormatUint(seq, 10)), pebble.Sync)
}

// SortFileEvents orders events chronologically. Callers that merge chains from
// more than one prefix scan need this; a single scan is already ordered.
func SortFileEvents(events []FileEvent) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
}
