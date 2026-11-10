// file: core/audit/query.go
// version: 1.0.1
// guid: 925fec3f-eec9-4c97-be35-9a4fb673023e
// last-edited: 2026-11-10

package audit

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math"
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Query returns the records that match f, newest first (descending seq),
// at most f.Limit of them (DefaultQueryLimit when 0, MaxQueryLimit at most).
// f.BeforeSeq is the cursor: only records with a smaller seq are returned; the
// next page's cursor is the last record's Seq. The whole query reads one
// snapshot, so a record is never half visible. A store error is yielded once,
// last (the route answers 503).
//
// The most selective index serves the query: the target (Module, then Kind,
// then ID, as far as they are set), else the actor, else the action prefix,
// else every record. f is then applied to each record in full, so an index
// only ever narrows the search. A single-actor or single-target query reads
// its index backwards and stops at the limit; a prefix over several actions or
// targets collects the matching seqs (keys only) and sorts them, because seq
// order is not key order across runs.
func (s *Store) Query(ctx context.Context, f Filter) iter.Seq2[Record, error] {
	return func(yield func(Record, error) bool) {
		limit := f.Limit
		switch {
		case limit <= 0:
			limit = DefaultQueryLimit
		case limit > MaxQueryLimit:
			limit = MaxQueryLimit
		}
		before := f.BeforeSeq
		if before == 0 {
			before = math.MaxUint64
		}
		snap, err := s.ks.Snapshot()
		if err != nil {
			yield(Record{}, fmt.Errorf("audit: query: %w", err))
			return
		}
		// The iterator has no error to return once the consumer may have
		// stopped (yielding again would panic), so a failed Close is logged.
		defer func() {
			if cerr := snap.Close(); cerr != nil {
				s.log.Warn("audit: close query snapshot", "err", cerr)
			}
		}()

		n := 0
		// take applies the filter to rec and yields it; false ends the query.
		take := func(rec Record) bool {
			if !matches(f, rec) {
				return true
			}
			n++
			return yield(rec, nil) && n < limit
		}
		fail := func(err error) { yield(Record{}, fmt.Errorf("audit: query: %w", err)) }

		prefix, single, ok := indexPrefix(f)
		switch {
		case !ok:
			return // an action prefix outside the grammar's alphabet matches nothing
		case prefix == nil:
			// Every record, newest first: the values are the records.
			end := keys.PrefixEnd(recPrefix())
			if before != math.MaxUint64 {
				end = recKey(before)
			}
			for kv, err := range snap.Scan(ctx, storage.ScanOptions{Start: recPrefix(), End: end, Reverse: true}) {
				if err != nil {
					fail(err)
					return
				}
				rec, err := recordCodec().Decode(kv.Value)
				if err != nil {
					fail(err)
					return
				}
				if !take(rec) {
					return
				}
			}
		case single:
			// One run of one actor or one target: its keys end in the seq, so a
			// reverse scan is newest first.
			end := keys.PrefixEnd(prefix)
			if before != math.MaxUint64 {
				end = append(slices.Clone(prefix), keys.Join(keys.U64(before))...)
			}
			for kv, err := range snap.Scan(ctx, storage.ScanOptions{Start: prefix, End: end, Reverse: true, KeysOnly: true}) {
				if err != nil {
					fail(err)
					return
				}
				seq, err := seqOfIndexKey(kv.Key)
				if err != nil {
					fail(err)
					return
				}
				rec, err := loadRecord(ctx, snap, seq)
				if err != nil {
					fail(err)
					return
				}
				if !take(rec) {
					return
				}
			}
		default:
			// Several runs: collect the seqs, newest first.
			var seqs []uint64
			for kv, err := range snap.Scan(ctx, storage.ScanOptions{Start: prefix, End: keys.PrefixEnd(prefix), KeysOnly: true}) {
				if err != nil {
					fail(err)
					return
				}
				seq, err := seqOfIndexKey(kv.Key)
				if err != nil {
					fail(err)
					return
				}
				if seq < before {
					seqs = append(seqs, seq)
				}
			}
			slices.SortFunc(seqs, func(a, b uint64) int {
				switch {
				case a > b:
					return -1
				case a < b:
					return 1
				}
				return 0
			})
			for _, seq := range seqs {
				rec, err := loadRecord(ctx, snap, seq)
				if err != nil {
					fail(err)
					return
				}
				if !take(rec) {
					return
				}
			}
		}
	}
}

// loadRecord reads and decodes rec/<seq> from snap. A missing record under an
// index row is a store error: record and index rows are written, and pruned,
// in one batch.
func loadRecord(ctx context.Context, snap *storage.Snapshot, seq uint64) (Record, error) {
	raw, err := snap.Get(ctx, recKey(seq))
	if errors.Is(err, storage.ErrNotFound) {
		return Record{}, fmt.Errorf("index row of seq %d has no record", seq)
	}
	if err != nil {
		return Record{}, err
	}
	return recordCodec().Decode(raw)
}

// indexPrefix picks the index that serves f. nil is "no index: scan the
// records"; single is true when the prefix ends where the seq starts (one
// actor, one full target), so a reverse scan of it is already newest first;
// ok is false when no record can match.
func indexPrefix(f Filter) (prefix []byte, single, ok bool) {
	if t := f.Target; t.Module != "" {
		segs := []keys.Seg{keys.Str("idx"), keys.Str("target"), keys.Str(t.Module)}
		single = false
		if t.Kind != "" {
			segs = append(segs, keys.Str(t.Kind))
			if t.ID != "" {
				segs = append(segs, keys.Str(t.ID))
				single = true
			}
		}
		return keys.Prefix(segs...), single, true
	}
	if f.ActorUserID != "" {
		return keys.Prefix(keys.Str("idx"), keys.Str("actor"), keys.Str(f.ActorUserID)), true, true
	}
	if f.ActionPrefix != "" {
		for i := 0; i < len(f.ActionPrefix); i++ {
			c := f.ActionPrefix[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '.' {
				return nil, false, false
			}
		}
		// The action segment is not closed: the prefix is bytes of it.
		return append(keys.Prefix(keys.Str("idx"), keys.Str("action")), f.ActionPrefix...), false, true
	}
	return nil, false, true
}

// matches applies every part of f to rec.
func matches(f Filter, rec Record) bool {
	switch {
	case !f.Since.IsZero() && rec.Time.Before(f.Since):
		return false
	case !f.Until.IsZero() && rec.Time.After(f.Until):
		return false
	case f.ActorUserID != "" && rec.Actor.UserID != f.ActorUserID:
		return false
	case f.ActionPrefix != "" && !strings.HasPrefix(rec.Action, f.ActionPrefix):
		return false
	case f.Outcome != "" && rec.Outcome != f.Outcome:
		return false
	case f.Target.Module != "" && rec.Target.Module != f.Target.Module:
		return false
	case f.Target.Kind != "" && rec.Target.Kind != f.Target.Kind:
		return false
	case f.Target.ID != "" && rec.Target.ID != f.Target.ID:
		return false
	}
	return true
}
