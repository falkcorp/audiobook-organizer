// file: internal/database/dedup_automerge_journal.go
// version: 1.0.0
// guid: 8b2f7d14-6c93-4a05-9e21-3f8a5c0d7e46
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// dedupAutoMergeJournalPfx is the Pebble keyspace for auto-merge journal
// entries written by the dedup.auto-resolve Tier-1 op. Each apply-path merge
// writes exactly one entry so the merge can later be reversed via
// Engine.UnmergeAuto (which restores the pre-merge book_ver snapshots).
//
// Key layout: dedup:automerge:<unix-nano, 16-hex> -> AutoMergeJournalEntry JSON.
// The fixed-width hex suffix keeps range scans in chronological order.
const dedupAutoMergeJournalPfx = "dedup:automerge:"

// autoMergeJournalKey renders the fixed-width key for an entry's nanosecond
// timestamp so prefix scans return rows in a stable, chronological order.
func autoMergeJournalKey(unixNano int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupAutoMergeJournalPfx, uint64(unixNano)))
}

// AutoMergeJournalEntry records one Tier-1 auto-merge so it can be reversed.
//
// WinnerPreMergeTS / LoserPreMergeTS are the book_ver copy-on-write snapshot
// timestamps (nanoseconds) captured immediately after MergeBooks — the earliest
// snapshot newer than the pre-merge state for each side, i.e. the genuine
// pre-merge book record. UnmergeAuto reverts both books to those versions.
type AutoMergeJournalEntry struct {
	// Key is the full Pebble key this entry is stored at (dedup:automerge:...).
	// Populated on read so callers can pass it straight to UnmergeAuto.
	Key string `json:"key,omitempty"`

	// CandidateID is the dedup candidate that triggered the merge.
	CandidateID int64 `json:"candidate_id"`

	// WinnerID / LoserID are the surviving primary and the soft-deleted loser.
	WinnerID string `json:"winner_id"`
	LoserID  string `json:"loser_id"`

	// WinnerPreMergeTS / LoserPreMergeTS are book_ver snapshot timestamps
	// (UnixNano) restoring each side to its pre-merge state. Zero means no
	// pre-merge snapshot was located (UnmergeAuto skips that side).
	WinnerPreMergeTS int64 `json:"winner_pre_merge_ts"`
	LoserPreMergeTS  int64 `json:"loser_pre_merge_ts"`

	// Tag is the survivor provenance tag applied at merge time.
	Tag string `json:"tag"`

	// MergedAt is the wall-clock time (UnixNano) the merge was applied; this is
	// also the value embedded in Key.
	MergedAt int64 `json:"merged_at"`
}

// PutAutoMergeJournalEntry writes an auto-merge journal entry keyed by
// entry.MergedAt (nanoseconds). Mirrors UpsertLabeledExample's storage pattern.
func (s *EmbeddingStore) PutAutoMergeJournalEntry(entry AutoMergeJournalEntry) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	key := autoMergeJournalKey(entry.MergedAt)
	entry.Key = string(key)
	data, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("marshal automerge journal entry: %w", err)
	}
	if err := s.db.Set(key, data, pebble.Sync); err != nil {
		return "", fmt.Errorf("write automerge journal entry: %w", err)
	}
	return entry.Key, nil
}

// GetAutoMergeJournalEntry returns the entry at the given full key, or nil if
// absent.
func (s *EmbeddingStore) GetAutoMergeJournalEntry(key string) (*AutoMergeJournalEntry, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	val, closer, err := s.db.Get([]byte(key))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get automerge journal entry %s: %w", key, err)
	}
	defer func() { _ = closer.Close() }()
	var entry AutoMergeJournalEntry
	if err := json.Unmarshal(val, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal automerge journal entry %s: %w", key, err)
	}
	entry.Key = key
	return &entry, nil
}

// ListAutoMergeJournalEntries returns journal entries in chronological order,
// capped at limit (0 = unlimited). Intended for a follow-on admin "undo merge"
// listing; not used by the auto-resolve op itself.
func (s *EmbeddingStore) ListAutoMergeJournalEntries(limit int) ([]AutoMergeJournalEntry, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	prefix := []byte(dedupAutoMergeJournalPfx)
	upper := append([]byte(dedupAutoMergeJournalPfx), 0xff)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("iter automerge journal: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var out []AutoMergeJournalEntry
	for iter.First(); iter.Valid(); iter.Next() {
		var entry AutoMergeJournalEntry
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			continue // skip corrupt rows rather than abort the whole listing
		}
		entry.Key = string(iter.Key())
		out = append(out, entry)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}
