// file: internal/database/dedup_automerge_journal.go
// version: 1.3.0
// guid: 8b2f7d14-6c93-4a05-9e21-3f8a5c0d7e46
// last-edited: 2026-10-26

package database

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
)

// autoMergeJournalGen counts the writes to the auto-merge journal in this
// process. It is package-level because several EmbeddingStore values wrap the
// one Pebble DB (every NewEmbeddingStoreFromStore call builds another), and a
// reader holding a snapshot must see a write made through any of them.
var autoMergeJournalGen atomic.Uint64

// AutoMergeJournalGeneration is how many times this process has written an
// auto-merge journal entry. A snapshot of the journal (OpenAutoMergeLosers)
// records it BEFORE it scans; if it differs afterwards, an entry was written
// during or after the scan and the snapshot may be missing one.
func AutoMergeJournalGeneration() uint64 { return autoMergeJournalGen.Load() }

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

	// Siblings are the loser's live version-group siblings the merge carried
	// into the winner's group (merge.Result.MovedSiblings), kept so an
	// operator can see what the undo will put back. UnmergeAuto restores them
	// through SiblingJournalID, never from this list directly, so the
	// sibling journal's refusals (undone, aborted, superseded by a later
	// merge) apply to it too.
	Siblings []AutoMergeJournalSibling `json:"siblings,omitempty"`

	// SiblingJournalID is the merge's sibling-move journal
	// (merge.Result.SiblingJournalID), set whenever the merge wrote one:
	// when siblings moved with this loser or another, or when the group's
	// flag holder is not the survivor and this loser's user state followed
	// it (the journal's StateFollows).
	SiblingJournalID string `json:"sibling_journal_id,omitempty"`

	// IntoGroupID is the merge's version group (merge.Result.VersionGroupID).
	// UnmergeAuto clears the trash-restore redirect the merge left on the
	// loser's old group once the loser is back in it.
	IntoGroupID string `json:"into_group_id,omitempty"`

	// Provisional marks the entry written before the merge, which names the
	// books but no snapshot. The post-merge patch clears it. UnmergeAuto
	// refuses an entry still provisional: its merge failed, or the patch
	// could not be written, and either way it records nothing to revert to.
	// Entries written before this field existed read as not provisional,
	// which is what every patched one of them is.
	Provisional bool `json:"provisional,omitempty"`

	// UndoneAt is when UnmergeAuto reverted this entry (UnixNano), zero
	// before. UnmergeAuto refuses an entry already undone: its reverts
	// rewrite both books' whole rows from the pre-merge snapshots, so a
	// replay after the books were merged again would revert that later merge.
	UndoneAt int64 `json:"undone_at,omitempty"`
}

// AutoMergeJournalSibling records one sibling a merge moved: from FromGroupID
// into IntoGroupID, with its IsPrimaryVersion exactly as stored before (nil
// included, so an undo restores the pointer, not a boolean).
type AutoMergeJournalSibling struct {
	BookID      string `json:"book_id"`
	FromGroupID string `json:"from_group_id"`
	IntoGroupID string `json:"into_group_id"`
	WasPrimary  *bool  `json:"was_primary,omitempty"`
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
	// After the commit, so a reader that sees the old generation either read
	// before the write or will see the write on its next scan.
	autoMergeJournalGen.Add(1)
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

// AutoMergeOpenLosers is a snapshot of the auto-merge journal's open entries:
// the id of every book that is the loser of an auto-merge whose undo
// (Engine.UnmergeAuto) has not run, mapped to the journal key that holds it.
// Gen is AutoMergeJournalGeneration read before the scan.
//
// An entry is open while UndoneAt is zero. A PROVISIONAL entry counts too:
// it is written before the merge and patched after it, so the loser can be
// soft-deleted while the entry is still provisional, and UnmergeAuto's own
// refusal for one tells the operator to restore the loser from the trash.
// Dropping the loser then would leave nothing to restore.
//
// Winners is the other book UnmergeAuto needs: an entry that recorded a
// pre-merge snapshot of its winner (WinnerPreMergeTS != 0) makes UnmergeAuto
// refuse, before it touches anything, when the winner row no longer exists. A
// winner that was soft-deleted later and then purged would make the whole undo
// impossible, so a trashed winner of an open, finalized entry is held too. A
// provisional entry cannot be undone (UnmergeAuto refuses it), so its winner is
// not held.
type AutoMergeOpenLosers struct {
	Gen     uint64
	Losers  map[string]string
	Winners map[string]string
}

// OpenAutoMergeLosers scans the auto-merge journal once. Unlike
// ListAutoMergeJournalEntries it does NOT skip a row it cannot decode: the
// caller (the trash purge) decides what may be deleted from the answer, and a
// row that cannot be read might name the book. It returns the error and the
// caller fails closed.
func (s *EmbeddingStore) OpenAutoMergeLosers() (*AutoMergeOpenLosers, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	out := &AutoMergeOpenLosers{Gen: AutoMergeJournalGeneration(), Losers: map[string]string{}, Winners: map[string]string{}}
	prefix := []byte(dedupAutoMergeJournalPfx)
	upper := append([]byte(dedupAutoMergeJournalPfx), 0xff)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("iter automerge journal: %w", err)
	}
	defer func() { _ = iter.Close() }()
	for iter.First(); iter.Valid(); iter.Next() {
		var entry AutoMergeJournalEntry
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			return nil, fmt.Errorf("automerge journal entry %s cannot be decoded, so the books it holds cannot be told: %w", iter.Key(), err)
		}
		if entry.UndoneAt != 0 || entry.LoserID == "" {
			continue
		}
		out.Losers[entry.LoserID] = string(iter.Key())
		if !entry.Provisional && entry.WinnerPreMergeTS != 0 && entry.WinnerID != "" {
			out.Winners[entry.WinnerID] = string(iter.Key())
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("scan automerge journal: %w", err)
	}
	return out, nil
}
