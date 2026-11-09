// file: internal/books/store/hnsw_verdict_watch.go
// version: 1.0.2
// guid: 4da46f59-ef13-459d-b8e5-3336484abafe
// last-edited: 2026-11-09

package store

import (
	"bytes"
	"sync/atomic"
)

// The Pebble side of the HNSW clean-stop marker (R1-15 follow-up review,
// MAJOR-1). The marker vouches that the snapshot on disk is the graph. The
// graph changes only through the HNSW store, so a write that reached Pebble
// and never the graph (a merge that flips IsPrimaryVersion, a cleanup op that
// deletes embedding rows, a re-upsert whose mirror was never made) used to
// leave a marker that let the next boot skip the reconcile while the rows'
// verdicts had moved.
//
// The reconcile classifies each embedding row (classifyBookRow and
// classifyAuthorRow in internal/dedup, then HNSWEmbeddingStore.Reconcile), and
// that verdict reads exactly these keys:
//
//   - emb:v:<type>:<id>, the embedding row: whether it exists (a graph key the
//     rows no longer list is tombstoned), its vector (the empty-vector skip
//     and the changed-vector rebuild) and its model (the stale-model skip,
//     compared with the model the process classifies against);
//   - book:<id>, the book row behind a book embedding: whether it exists (the
//     orphan skip), IsPrimaryVersion (the non-primary skip and the
//     is_primary_version metadata), SeriesID (series_id), and SeriesSequence,
//     or the Title when SeriesSequence is unset (series_sequence: dedup's
//     seriesNumberOf parses a number out of the title).
//
// Author embeddings are classified from their row alone, and no other key is
// read. The model is not a key: the marker records it (MarkCleanStop) and the
// boot compares it.
//
// Every write to Pebble goes through a GatedDB or one of its batches, so the
// watch sits there and needs no list of callers: a Set or Delete of one of
// those keys, a DeleteRange that reaches either family, or a batch that
// staged one, counts as a verdict write. The count is taken before the write
// reaches the engine, so a crash between the two leaves the count (and with
// it the refusal) ahead of Pebble, never behind it. A caller that knows a
// book-row write leaves every field above as it was says so (setJudged):
// UpdateBook compares the old row with the new, and the signature-sidecar
// migration only strips signature fields.
//
// The HNSW store reads the count (BindTruthSource): MarkCleanStop refuses
// after any verdict write since the store opened, the boot treats a snapshot
// as stale after one, and a verdict write made while a marker is on disk
// removes it (the hook).

// verdictEmbPrefix and verdictBookPrefix are the two key families the
// reconcile's verdict reads. A book row is "book:<id>" with no further colon
// (ValidateBookID); every "book:<index>:..." family shares the prefix and is
// not a row.
var (
	verdictEmbPrefix  = []byte(embVecPfx)
	verdictBookPrefix = []byte(bookRowPrefix)
)

// isVerdictKey reports whether key is an embedding row or a book row.
func isVerdictKey(key []byte) bool {
	if bytes.HasPrefix(key, verdictEmbPrefix) {
		return true
	}
	if !bytes.HasPrefix(key, verdictBookPrefix) || len(key) == len(verdictBookPrefix) {
		return false
	}
	return bytes.IndexByte(key[len(verdictBookPrefix):], ':') < 0
}

// rangeTouchesVerdictKeys reports whether [start, end) can hold a key of
// either family. Coarse on purpose: a range over book index keys counts too.
func rangeTouchesVerdictKeys(start, end []byte) bool {
	return rangesOverlap(start, end, verdictEmbPrefix, prefixEnd(verdictEmbPrefix)) ||
		rangesOverlap(start, end, verdictBookPrefix, prefixEnd(verdictBookPrefix))
}

func rangesOverlap(start1, end1, start2, end2 []byte) bool {
	return bytes.Compare(start1, end2) < 0 && bytes.Compare(start2, end1) < 0
}

// verdictWatch counts one GatedDB's verdict writes since it was created, and
// calls the hook (the bound HNSW store's) on each.
type verdictWatch struct {
	writes atomic.Int64
	hook   atomic.Pointer[func()]
}

// note records one verdict write. Callers note BEFORE the write reaches the
// engine.
func (w *verdictWatch) note() {
	w.writes.Add(1)
	if h := w.hook.Load(); h != nil {
		(*h)()
	}
}

// count is the number of verdict writes since the GatedDB was created.
func (w *verdictWatch) count() int64 { return w.writes.Load() }

// setHook installs fn as the hook; nil removes it. One hook per database: a
// later bind replaces an earlier one.
func (w *verdictWatch) setHook(fn func()) {
	if fn == nil {
		w.hook.Store(nil)
		return
	}
	w.hook.Store(&fn)
}

// annVerdictInputsChanged reports whether an update of a book row from old to
// cur changes anything the reconcile's verdict reads from it: the effective
// primary flag, SeriesID, SeriesSequence, or the Title when either side has
// no SeriesSequence (series_sequence then comes from the title).
func annVerdictInputsChanged(old, cur *Book) bool {
	if bookIsPrimary(old) != bookIsPrimary(cur) {
		return true
	}
	if !intPtrEq(old.SeriesID, cur.SeriesID) || !intPtrEq(old.SeriesSequence, cur.SeriesSequence) {
		return true
	}
	return (old.SeriesSequence == nil || cur.SeriesSequence == nil) && old.Title != cur.Title
}

// bookIsPrimary is the reconcile's reading of IsPrimaryVersion: unset counts
// as primary.
func bookIsPrimary(b *Book) bool {
	return b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
}
