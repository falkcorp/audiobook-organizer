// file: internal/books/store/dedup_automerge_journal.go
// version: 1.7.0
// guid: 8b2f7d14-6c93-4a05-9e21-3f8a5c0d7e46
// last-edited: 2026-11-17

package store

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
)

// autoMergeJournalGen counts the writes to the auto-merge journal in this
// process. It is package-level because several EmbeddingStore values wrap the
// one Pebble DB (every NewEmbeddingStoreFromStore call builds another), and a
// reader holding a snapshot must see a write made through any of them.
var autoMergeJournalGen atomic.Uint64

// autoMergeJournalMu serialises every journal write for the same reason the
// generation is package-level: the several EmbeddingStore values over one DB must
// share it. It makes "write only if absent" and "close only the row I wrote" a
// read and a write that nothing can interleave with. Pebble holds an exclusive
// process lock on the DB, so no other process writes the journal.
var autoMergeJournalMu sync.Mutex

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

	// WinnerBaselineTS / LoserBaselineTS are, on a provisional entry written by
	// this version, the UnixNano of each book's newest book_ver snapshot when the
	// merge began (0: it had none), and BaselinesRecorded says they were taken, so
	// a 0 is a real baseline and not "unknown". The merge's pre-merge record is the
	// earliest snapshot newer than the baseline, so the snapshot prune keeps that
	// one snapshot per book while the entry is provisional
	// (store.collectAutoMergeSnapshotPins). The patch that finalizes the entry
	// drops all three: from then on the entry names the snapshots exactly. An
	// entry without BaselinesRecorded predates the fields; its floor is MergedAt.
	WinnerBaselineTS  int64 `json:"winner_baseline_ts,omitempty"`
	LoserBaselineTS   int64 `json:"loser_baseline_ts,omitempty"`
	BaselinesRecorded bool  `json:"baselines_recorded,omitempty"`

	// AbortedAt closes a provisional entry whose merge was refused before it wrote
	// anything (merge.RefusedBeforeWriteError): there is no merge to undo, and the
	// entry pins no snapshot. It stays Provisional on purpose, so a binary that
	// predates the field still refuses to undo it and still treats its loser as
	// open. UnmergeAuto and the snapshot pins both ignore an aborted entry.
	//
	// It does NOT release the loser from the trash purge: OpenAutoMergeLosers
	// skips only UndoneAt, so a closed row still holds its loser for as long as the
	// row exists. That is the conservative answer (the row is provisional, and a
	// provisional row's loser may have been soft-deleted), and it is deliberate.
	AbortedAt int64 `json:"aborted_at,omitempty"`

	// Participants is, on a provisional entry written by this version, EVERY book
	// of the merge with the baseline recorded for it, not only this entry's winner
	// and loser. The post-merge patch rewrites the merge's rows against the winner
	// MergeBooks actually elected, so a row whose patch write fails still describes
	// the whole cluster: while any such row is live the snapshot prune keeps one
	// snapshot for each participant (store.collectAutoMergeSnapshotPins), and the
	// trash purge keeps every participant (OpenAutoMergeLosers), because this row's
	// LoserID is only the loser of the PREDICTED winner. A row without the list (an
	// older one) behaves as before: its winner and loser only.
	// The patch drops it with the other baseline fields. A binary that predates the
	// field ignores it and still floors the winner and loser.
	Participants []AutoMergeJournalParticipant `json:"participants,omitempty"`

	// RowID identifies the write that created a provisional entry. It is how the
	// merge that wrote a row recognises it later: a refused merge closes its row
	// only while the stored row still carries the RowID that merge wrote
	// (CloseAutoMergeJournalEntry), so a different merge's row at the same key is
	// never overwritten. The patch drops it: a finalized row is nobody's to close.
	RowID string `json:"row_id,omitempty"`

	// UndoneAt is when UnmergeAuto reverted this entry (UnixNano), zero
	// before. UnmergeAuto refuses an entry already undone: its reverts
	// rewrite both books' whole rows from the pre-merge snapshots, so a
	// replay after the books were merged again would revert that later merge.
	UndoneAt int64 `json:"undone_at,omitempty"`
}

// AutoMergeJournalParticipant is one book of a merge and its baseline: the UnixNano
// of its newest book_ver snapshot when the merge began (0: it had none).
type AutoMergeJournalParticipant struct {
	BookID     string `json:"book_id"`
	BaselineTS int64  `json:"baseline_ts"`
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
//
// It replaces whatever is at the key. A merge's first write of a row must use
// PutAutoMergeJournalEntryIfAbsent instead, so two merges cannot share a key.
func (s *EmbeddingStore) PutAutoMergeJournalEntry(entry AutoMergeJournalEntry) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	autoMergeJournalMu.Lock()
	defer autoMergeJournalMu.Unlock()
	return s.putAutoMergeJournalEntryLocked(entry)
}

// putAutoMergeJournalEntryLocked writes the entry; the caller holds
// autoMergeJournalMu.
func (s *EmbeddingStore) putAutoMergeJournalEntryLocked(entry AutoMergeJournalEntry) (string, error) {
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

// PutAutoMergeJournalEntryIfAbsent writes the entry only when nothing is stored at
// its key. created=false (and a nil error) means the key is taken and nothing was
// written: the caller picks a new MergedAt and tries again. Keys come from a
// nanosecond clock, so two merges starting together can ask for the same one.
func (s *EmbeddingStore) PutAutoMergeJournalEntryIfAbsent(entry AutoMergeJournalEntry) (key string, created bool, err error) {
	if err := s.checkClosed(); err != nil {
		return "", false, err
	}
	autoMergeJournalMu.Lock()
	defer autoMergeJournalMu.Unlock()
	k := autoMergeJournalKey(entry.MergedAt)
	val, closer, gerr := s.db.Get(k)
	switch {
	case gerr == nil:
		_ = val
		_ = closer.Close()
		return string(k), false, nil
	case gerr != pebble.ErrNotFound:
		return "", false, fmt.Errorf("check automerge journal key %s: %w", k, gerr)
	}
	key, err = s.putAutoMergeJournalEntryLocked(entry)
	if err != nil {
		return "", false, err
	}
	return key, true, nil
}

// CloseAutoMergeJournalEntry sets AbortedAt on the entry at key, but only while
// the stored entry still carries rowID, i.e. is the row the caller wrote. It
// returns closed=false (nil error) when the key is empty or holds anything else,
// including a different merge's row or this merge's own row once it was patched,
// and writes nothing then. A blank rowID never matches.
func (s *EmbeddingStore) CloseAutoMergeJournalEntry(key, rowID string, abortedAt int64) (closed bool, err error) {
	if err := s.checkClosed(); err != nil {
		return false, err
	}
	if rowID == "" {
		return false, nil
	}
	autoMergeJournalMu.Lock()
	defer autoMergeJournalMu.Unlock()
	val, closer, gerr := s.db.Get([]byte(key))
	if gerr == pebble.ErrNotFound {
		return false, nil
	}
	if gerr != nil {
		return false, fmt.Errorf("get automerge journal entry %s: %w", key, gerr)
	}
	var entry AutoMergeJournalEntry
	uerr := json.Unmarshal(val, &entry)
	_ = closer.Close()
	if uerr != nil {
		return false, fmt.Errorf("unmarshal automerge journal entry %s: %w", key, uerr)
	}
	if entry.RowID != rowID {
		return false, nil
	}
	entry.AbortedAt = abortedAt
	if _, err := s.putAutoMergeJournalEntryLocked(entry); err != nil {
		return false, err
	}
	return true, nil
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
// An entry is open while UndoneAt is zero. A closed one (AbortedAt) is open here
// too, on purpose: it stays provisional and holds its loser from the purge for as
// long as it exists. A PROVISIONAL entry counts too:
// it is written before the merge and patched after it, so the loser can be
// soft-deleted while the entry is still provisional, and UnmergeAuto's own
// refusal for one tells the operator to restore the loser from the trash.
// Dropping the loser then would leave nothing to restore.
//
// A LIVE provisional entry (neither undone nor closed) also holds every other
// book in its Participants list, because its LoserID is only the loser of the
// winner PREDICTED before the merge: when the merge elected another book, the
// predicted winner is a real loser named by no entry's LoserID, and the entry's
// own LoserID is the actual winner. Holding an extra participant that was never
// soft-deleted is harmless, since the purge only asks about trashed books. An
// entry without a Participants list, or a closed one, holds its loser alone.
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
		if entry.UndoneAt != 0 {
			continue
		}
		key := string(iter.Key())
		if entry.LoserID != "" {
			out.Losers[entry.LoserID] = key
		}
		// A live provisional row's LoserID is a PREDICTION: its WinnerID is the
		// winner predicted before the merge, and when the merge elected another book
		// the row is the spare that takes the predicted winner's place, so it names
		// the actual winner as its loser and no row names the real loser the
		// prediction was. A row left provisional (its patch could not run) therefore
		// holds every book it lists. A closed row describes a merge that never wrote,
		// so it holds only its loser, as it always did. A loser key set by its own
		// row is never replaced by the participant key of another.
		if entry.Provisional && entry.AbortedAt == 0 {
			for _, p := range entry.Participants {
				if _, held := out.Losers[p.BookID]; !held && p.BookID != "" {
					out.Losers[p.BookID] = key
				}
			}
		}
		if !entry.Provisional && entry.WinnerPreMergeTS != 0 && entry.WinnerID != "" {
			out.Winners[entry.WinnerID] = key
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("scan automerge journal: %w", err)
	}
	return out, nil
}

// provisionalFloors is the floor of the range each side of a provisional entry
// pins from: the earliest snapshot above it is the one the entry's patch will
// name. Recorded baselines are used as they are (0 included, which is a book that
// had no snapshot). An entry from before baselines were recorded uses MergedAt,
// set after they would have been read and before the merge wrote anything.
func (e AutoMergeJournalEntry) provisionalFloors() (winner, loser int64) {
	if e.BaselinesRecorded {
		return e.WinnerBaselineTS, e.LoserBaselineTS
	}
	return e.MergedAt, e.MergedAt
}

// addProvisionalFloors records on pins the floor this provisional entry holds for
// each book it covers: its winner and loser as provisionalFloors gives them, and
// every other participant it lists. An entry without a participant list covers the
// winner and the loser, as entries always did.
func (e AutoMergeJournalEntry) addProvisionalFloors(pins *SnapshotPins) {
	wf, lf := e.provisionalFloors()
	pins.addFloor(e.WinnerID, wf)
	pins.addFloor(e.LoserID, lf)
	for _, p := range e.Participants {
		pins.addFloor(p.BookID, p.BaselineTS)
	}
}
