// file: internal/merge/pointer_backfill.go
// version: 1.1.1
// guid: 56e834dc-3c76-4989-9ce4-9db933749d76
// last-edited: 2026-10-18

package merge

import (
	"errors"
	"fmt"
	"time"

	ulid "github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Undo-pointer backfill.
//
// Every combine and sibling move made since undo_pointer.go landed journals
// its pointer at the chokepoint. Journals written before that have none, so
// POST /operations/:id/revert cannot reach them; the retired
// POST /merge/undo/:journal_id and /merge/sibling-undo/:journal_id routes were
// their only way back. The maintenance job backfill-merge-undo-pointers gives
// each such journal a pointer of its own through BackfillJournalPointer.
//
// Each backfilled journal gets a FRESH correlation id, never the backfill
// job's own operation id. Under the job's id, reverting the job would undo
// every historical combine in one click, and the revert of one combine would
// be indistinguishable from the revert of all of them.

// Verdicts of BackfillJournalPointer.
const (
	// PointerBackfillWritten: the pointer was written (apply).
	PointerBackfillWritten = "written"
	// PointerBackfillWouldWrite: the journal has no pointer and would get one
	// (preview).
	PointerBackfillWouldWrite = "would_write"
	// PointerBackfillHasPointer: a live pointer already names the journal (a
	// chokepoint wrote it, the split-books fixer wrote it, or an earlier
	// backfill did). Nothing is written.
	PointerBackfillHasPointer = "has_pointer"
	// PointerBackfillNotUndoable: the journal is in a state no undo accepts
	// (undone, aborted, a CombineBooks journal left pending, or a refused
	// combine's emptied journal), so a pointer to it would only offer a revert
	// that does nothing or refuses. Nothing is written.
	PointerBackfillNotUndoable = "not_undoable"
)

// PointerBackfillItem is one journal the backfill considered, and what it did.
type PointerBackfillItem struct {
	// ChangeType is CombineJournalPointerType or SiblingJournalPointerType.
	ChangeType string `json:"change_type"`
	JournalID  string `json:"journal_id"`
	// SurvivorID is the book the pointer goes on: the one the chokepoint
	// would have written it on, and the one GET /audiobooks/:id/changes
	// lists it under.
	SurvivorID string `json:"survivor_id"`
	Status     string `json:"journal_status"`
	Verdict    string `json:"verdict,omitempty"`
	// Reason says why a journal was skipped.
	Reason string `json:"reason,omitempty"`
	// OperationID is the id to revert: the one written (written), or the one
	// the live pointer is under (has_pointer). Empty for a preview.
	OperationID string `json:"operation_id,omitempty"`
}

// journalPointerReader lists a book's undo-ledger rows. Every database.Store
// carries it; the backfill resolves it with database.AsCapability.
type journalPointerReader interface {
	GetBookChanges(bookID string) ([]*database.OperationChange, error)
}

var _ journalPointerReader = database.Store(nil)

// opChangeScanner reads the whole undo ledger in one pass
// (PebbleStore.ScanOperationChanges, which is not on Store).
type opChangeScanner interface {
	ScanOperationChanges(fn func(*database.OperationChange) error) error
}

var _ opChangeScanner = (*database.PebbleStore)(nil)

// PointerBackfillIndex is the set of live merge journal pointers, read in ONE
// pass over the undo ledger outside the merge lock, and the instant it is
// valid for (asOf).
//
// Why it can be trusted for a journal created before asOf without taking the
// lock for a ledger read: every chokepoint writes its journal (CreatedAt is
// set inside that write's lock hold) and its pointer inside ONE hold of the
// merge lock. NewPointerBackfillIndex takes that lock to stamp asOf, so every
// merge whose journal is older than asOf had released the lock, its pointer
// committed, before the scan began. A journal created at or after asOf (a
// merge that ran during the backfill) is checked against the ledger under the
// lock instead.
//
// The order is wall-clock (CreatedAt), not a monotonic sequence. Neither the
// journal ULIDs nor CreatedAt survive a backward clock step as an order, and a
// persisted monotonic counter would need a new field on both journal kinds and
// a process epoch, so the cost is documented instead: if the clock steps back
// while a merge runs during the backfill, that merge's journal can read as
// older than asOf, be trusted to a scan that ran before its pointer
// committed, and get a second pointer under a fresh id. Nothing is lost: both
// operations then name the same journal, reverting either undoes the merge,
// and the other finds it already undone (undo.ErrAlreadyRestored).
type PointerBackfillIndex struct {
	asOf time.Time
	live map[string]string // pointerKey -> operation id
}

func pointerKey(changeType, journalID string) string { return changeType + "\x00" + journalID }

// NewPointerBackfillIndex stamps asOf under the merge lock (held only for
// the stamp), then scans the undo ledger outside it.
func (ms *Service) NewPointerBackfillIndex() (*PointerBackfillIndex, error) {
	scanner, _ := database.AsCapability[opChangeScanner](ms.db)
	if scanner == nil {
		return nil, errors.New("the store cannot scan the undo ledger; refusing to backfill undo pointers")
	}
	mergeSerializeMu.Lock()
	asOf := time.Now().UTC()
	mergeSerializeMu.Unlock()

	idx := &PointerBackfillIndex{asOf: asOf, live: map[string]string{}}
	err := scanner.ScanOperationChanges(func(c *database.OperationChange) error {
		if c.Voided || c.FieldName != JournalPointerField || c.OperationID == "" ||
			(c.ChangeType != CombineJournalPointerType && c.ChangeType != SiblingJournalPointerType) {
			return nil
		}
		idx.live[pointerKey(c.ChangeType, c.NewValue)] = c.OperationID
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan the undo ledger for merge journal pointers: %w", err)
	}
	return idx, nil
}

// ListPointerBackfillCandidates returns every combine and sibling-move
// journal, newest first within each kind, as items with no verdict yet.
func (ms *Service) ListPointerBackfillCandidates() ([]PointerBackfillItem, error) {
	combines, err := ms.ListCombineJournals(0)
	if err != nil {
		return nil, err
	}
	siblings, err := ms.ListSiblingMoveJournals(0)
	if err != nil {
		return nil, err
	}
	out := make([]PointerBackfillItem, 0, len(combines)+len(siblings))
	for _, j := range combines {
		out = append(out, PointerBackfillItem{ChangeType: CombineJournalPointerType, JournalID: j.ID, SurvivorID: j.SurvivorID, Status: j.Status})
	}
	for _, j := range siblings {
		out = append(out, PointerBackfillItem{ChangeType: SiblingJournalPointerType, JournalID: j.ID, SurvivorID: j.SurvivorID, Status: j.Status})
	}
	return out, nil
}

// BackfillJournalPointer decides whether the journal item names needs an undo
// pointer and, unless dryRun, writes it under a fresh correlation id. The
// returned item carries the verdict.
//
// It runs under the merge lock (LockMergeRMW), which every chokepoint holds
// across its journal and pointer writes, so a merge in flight cannot get a
// second pointer from here. Under the lock it does a point read of the
// journal (so a status that changed since the listing, such as an undo, is
// seen) and, only for a journal created at or after idx's asOf, one
// ledger read of the survivor; an older journal is decided from idx, read
// outside the lock (see PointerBackfillIndex). A pointer written here is
// added to idx.
//
// The caller must not hold the merge lock.
func (ms *Service) BackfillJournalPointer(idx *PointerBackfillIndex, item PointerBackfillItem, dryRun bool) (PointerBackfillItem, error) {
	if idx == nil {
		return item, errors.New("no pointer index; build one with NewPointerBackfillIndex")
	}
	reader, _ := database.AsCapability[journalPointerReader](ms.db)
	writer, _ := database.AsCapability[JournalPointerWriter](ms.db)
	if reader == nil || writer == nil {
		return item, errors.New("the store has no undo ledger; refusing to backfill undo pointers")
	}

	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	st, err := ms.pointerBackfillState(item)
	if err != nil {
		return item, err
	}
	item.SurvivorID, item.Status = st.survivor, st.status
	if st.reason != "" {
		item.Verdict, item.Reason = PointerBackfillNotUndoable, st.reason
		return item, nil
	}

	key := pointerKey(item.ChangeType, item.JournalID)
	opID := idx.live[key]
	if opID == "" && !st.createdAt.Before(idx.asOf) {
		// Created during this run: idx cannot have seen its pointer.
		changes, err := reader.GetBookChanges(st.survivor)
		if err != nil {
			return item, fmt.Errorf("read the undo ledger of %s: %w", st.survivor, err)
		}
		opID = livePointerOperation(changes, item.ChangeType, item.JournalID)
	}
	if opID != "" {
		item.Verdict, item.OperationID = PointerBackfillHasPointer, opID
		return item, nil
	}
	if dryRun {
		item.Verdict = PointerBackfillWouldWrite
		return item, nil
	}
	opID = ulid.Make().String()
	if _, err := RecordJournalPointer(writer, opID, st.survivor, item.ChangeType, item.JournalID); err != nil {
		return item, err
	}
	idx.live[key] = opID
	item.Verdict, item.OperationID = PointerBackfillWritten, opID
	return item, nil
}

// backfillJournalState is a journal as BackfillJournalPointer re-reads it.
type backfillJournalState struct {
	survivor, status string
	createdAt        time.Time
	// reason says why no undo would accept the journal ("" when one would).
	reason string
}

// pointerBackfillState re-reads the journal.
func (ms *Service) pointerBackfillState(item PointerBackfillItem) (backfillJournalState, error) {
	switch item.ChangeType {
	case CombineJournalPointerType:
		j, err := ms.GetCombineJournal(item.JournalID)
		if err != nil {
			return backfillJournalState{}, err
		}
		return backfillJournalState{j.SurvivorID, j.Status, j.CreatedAt, combineNotUndoableReason(j)}, nil
	case SiblingJournalPointerType:
		j, err := ms.GetSiblingMoveJournal(item.JournalID)
		if err != nil {
			return backfillJournalState{}, err
		}
		return backfillJournalState{j.SurvivorID, j.Status, j.CreatedAt, siblingNotUndoableReason(j)}, nil
	default:
		return backfillJournalState{}, fmt.Errorf("%q is not a merge journal pointer type", item.ChangeType)
	}
}

// combineNotUndoableReason mirrors the status gate of undoPreconditions, plus
// the emptied journal of a refused combine, which is stored as Applied with
// nothing in it (CombineBooksWithOptions and
// dedup.MergeSplitBookClusterWithOptions both rewrite it that way).
func combineNotUndoableReason(j *CombineJournal) string {
	switch {
	case j.SurvivorID == "":
		return "the journal names no survivor"
	case j.Status == CombineJournalApplied:
		if len(j.Absorbed) == 0 && len(j.SurvivorFiles) == 0 && len(j.SurvivorOwnFiles) == 0 &&
			j.Override == nil && j.FilledEmpty == nil {
			return "the journal records nothing to undo (a combine refused before its first write)"
		}
		return ""
	case j.Status == CombineJournalUndoFailed:
		return ""
	case j.Status == CombineJournalPending && j.StepJournaled:
		return ""
	case j.Status == CombineJournalPending:
		return "a CombineBooks journal left pending does not record what ran, so no undo accepts it"
	default:
		return fmt.Sprintf("journal status %q is not undoable", j.Status)
	}
}

// siblingNotUndoableReason mirrors the status gate of UndoSiblingMove.
func siblingNotUndoableReason(j *SiblingMoveJournal) string {
	switch {
	case j.SurvivorID == "":
		return "the journal names no survivor"
	case j.Status == SiblingJournalPending, j.Status == SiblingJournalApplied:
		return ""
	default:
		return fmt.Sprintf("journal status %q is not undoable", j.Status)
	}
}

// livePointerOperation returns the operation id of a live (not voided) pointer
// row of changeType naming journalID, or "". A void rewrites the row in place
// (same key), so a voided pointer is one row with Voided set, never a live row
// beside it.
func livePointerOperation(changes []*database.OperationChange, changeType, journalID string) string {
	for _, c := range changes {
		if c == nil || c.Voided || c.ChangeType != changeType || c.FieldName != JournalPointerField || c.NewValue != journalID {
			continue
		}
		if c.OperationID != "" {
			return c.OperationID
		}
	}
	return ""
}
