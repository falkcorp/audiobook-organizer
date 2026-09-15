// file: internal/merge/combine_journal.go
// version: 1.1.0
// guid: 4e8b1c27-93d5-4f0a-a6e2-7c51d9b03f18
// last-edited: 2026-09-14

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	ulid "github.com/oklog/ulid/v2"
)

// mlog is the merge package's logger. It is printf-style (format verbs, not
// key/value pairs) and routes through internal/logger's log-injection barrier,
// which the slog guard ratchet requires for new log calls.
var mlog = logger.New("merge")

// Combine undo journal.
//
// CombineBooks used to HARD-delete every absorbed book, so an approved
// review-queue "combine" or "duplicate-of" (prod runs with
// review_apply_enabled=true) could not be taken back. It now soft-deletes the
// absorbed shells and writes one CombineJournal per call recording everything
// the combine changed; UndoCombine replays that record backwards.
//
// WHY NOT dedup's AutoMergeJournalEntry. That format holds one winner/loser
// pair plus two book_ver snapshot timestamps, and its reverse
// (Engine.UnmergeAuto) re-applies book ROW snapshots. A combine's damage is
// elsewhere: file-row OWNERSHIP, external-ID mappings, sync_file inos, the ABS
// redirect and per-user progress. None of those live on the book row, so a row
// snapshot cannot reverse them. It also lives on the EmbeddingStore, which this
// package cannot reach.
//
// WHERE IT LIVES. Raw keys on the main store (database.RawKVStore, which is on
// database.Store itself), so the production indexedStore decorator forwards it
// without any capability assertion:
//
//	merge:combine-journal:<ULID> -> CombineJournal JSON
//
// ULIDs sort chronologically, so a prefix scan lists oldest-first.

const combineJournalPrefix = "merge:combine-journal:"

// Combine journal statuses.
const (
	// CombineJournalPending is written BEFORE the combine mutates anything. A
	// journal left in this state names a combine that failed or crashed part
	// way; it is never undoable automatically (the recorded moves may not all
	// have happened), but it tells an operator exactly what was attempted.
	CombineJournalPending = "pending"
	// CombineJournalApplied marks a combine that completed. Only these undo.
	CombineJournalApplied = "applied"
	// CombineJournalUndone marks a combine that UndoCombine reversed.
	CombineJournalUndone = "undone"
	// CombineJournalUndoFailed marks an undo that passed every precondition
	// but hit a store error part way through. LastError says where. It is NOT
	// terminal: UndoCombine accepts it and re-runs the undo, treating every item
	// the failed attempt already restored as done (see undoPreconditions and
	// applyUndo, whose steps are each safe to re-run).
	CombineJournalUndoFailed = "undo_failed"
)

// CombineJournal is the undo record for one CombineBooks call.
type CombineJournal struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	SurvivorID string    `json:"survivor_id"`
	// Origin names the operation that wrote the journal when it is not
	// CombineBooks (empty): "split_book_merge" for dedup.MergeSplitBookCluster.
	Origin string `json:"origin,omitempty"`

	// Absorbed holds one entry per book folded into the survivor.
	Absorbed []CombineAbsorbed `json:"absorbed"`

	// SurvivorFiles are file rows ensureOwnFile attached to the survivor for
	// its OWN FilePath (virtual-segment materialization). Created rows are
	// deleted on undo; a row reattached from another owner is moved back.
	SurvivorFiles []CombineFileMove `json:"survivor_files,omitempty"`

	// SurvivorOwnFiles are the rows the survivor already owned before the
	// combine. They never move, but the regroup multidisc apply stamps new
	// disc/track numbers on them right after the combine, so undo restores
	// their DiscBefore/TrackBefore too (and refuses if one was moved away).
	SurvivorOwnFiles []CombineFileMove `json:"survivor_own_files,omitempty"`

	// Override is non-nil when the combine applied a metadata override to the
	// survivor; it records what the survivor held before.
	Override *CombineOverrideUndo `json:"override,omitempty"`

	// ITunesRemovals is always empty for a combine and is recorded so the
	// journal states that explicitly: CombineBooks queues NO iTunes-library
	// removals (only MergeBooks does). The absorbed books' PIDs are REASSIGNED
	// to the survivor, which now owns that audio; undo moves them back via
	// Absorbed[].ExternalIDs. Removal is deferred to PurgeSoftDeletedBooks,
	// which enqueues removes for whatever PIDs a purged book still holds.
	ITunesRemovals []string `json:"itunes_removals"`

	UndoneAt  *time.Time `json:"undone_at,omitempty"`
	Warnings  []string   `json:"warnings,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// CombineAbsorbed records one absorbed book's pre-combine state.
type CombineAbsorbed struct {
	BookID string `json:"book_id"`
	// FilePath is the book row's pre-combine FilePath. The combine CLEARS it on
	// the soft-deleted shell: that path now belongs to a file row the survivor
	// owns, and PurgeSoftDeletedBooks with delete-files enabled os.Remove()s a
	// purged single-file book's FilePath. Leaving it set would let the purge
	// delete the survivor's audio from disk 30 days later.
	FilePath         string  `json:"file_path"`
	IsPrimaryVersion *bool   `json:"is_primary_version,omitempty"`
	VersionGroupID   *string `json:"version_group_id,omitempty"`
	// MarkedForDeletionAt is the soft-delete stamp the combine wrote, read
	// back from the store. Undo refuses if the row no longer carries it: the
	// book was restored, re-deleted, or purged by something else since.
	MarkedForDeletionAt *time.Time `json:"marked_for_deletion_at,omitempty"`

	// Files are the rows moved from this book onto the survivor.
	Files []CombineFileMove `json:"files,omitempty"`
	// ExternalIDs are the mappings reassigned from this book to the survivor.
	ExternalIDs []CombineExternalID `json:"external_ids,omitempty"`
	// SyncRedirected is true when the ABS sync layer was reachable, so a
	// redirect absorbed->survivor may have been recorded and undo must clear it.
	SyncRedirected bool `json:"sync_redirected"`
	// Progress snapshots every user's progress on the absorbed book and on the
	// survivor, before and after FollowMerge drained one onto the other.
	Progress []CombineUserProgress `json:"progress,omitempty"`
}

// CombineFileMove records one file row's move.
type CombineFileMove struct {
	FileID     string `json:"file_id"`
	FromBookID string `json:"from_book_id"`
	FilePath   string `json:"file_path"`
	// Created is true when the combine CREATED this row (a virtual single-file
	// book materialized as a BookFile); undo deletes a created row instead of
	// moving it, since it did not exist before.
	Created bool `json:"created,omitempty"`
	// DiscBefore/TrackBefore are the numbers the row carried before the
	// combine. The regroup multidisc apply stamps new numbers AFTER the combine;
	// undo restores these, which reverses that stamping too.
	DiscBefore  int `json:"disc_before"`
	TrackBefore int `json:"track_before"`
}

// CombineExternalID records one external-ID mapping moved to the survivor.
type CombineExternalID struct {
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
}

// CombineUserProgress is one user's progress on both sides of one absorbed
// book's FollowMerge.
type CombineUserProgress struct {
	UserID              string                  `json:"user_id"`
	AbsorbedState       *database.UserBookState `json:"absorbed_state,omitempty"`
	AbsorbedPositions   []database.UserPosition `json:"absorbed_positions,omitempty"`
	SurvivorStateBefore *database.UserBookState `json:"survivor_state_before,omitempty"`
	SurvivorPosBefore   []database.UserPosition `json:"survivor_positions_before,omitempty"`
	SurvivorStateAfter  *database.UserBookState `json:"survivor_state_after,omitempty"`
	SurvivorPosAfter    []database.UserPosition `json:"survivor_positions_after,omitempty"`
}

// CombineOverrideUndo records the survivor's metadata before an override.
type CombineOverrideUndo struct {
	Applied        CombineOverride                         `json:"applied"`
	TitleBefore    string                                  `json:"title_before"`
	NarratorBefore *string                                 `json:"narrator_before,omitempty"`
	AuthorIDBefore *int                                    `json:"author_id_before,omitempty"`
	AuthorsBefore  []database.BookAuthor                   `json:"authors_before,omitempty"`
	AuthorIDAfter  *int                                    `json:"author_id_after,omitempty"`
	FieldStates    map[string]*database.MetadataFieldState `json:"field_states_before,omitempty"`
}

// CombineUndoResult is what UndoCombine returns on success.
type CombineUndoResult struct {
	JournalID     string   `json:"journal_id"`
	SurvivorID    string   `json:"survivor_id"`
	RestoredBooks []string `json:"restored_books"`
	FilesMoved    int      `json:"files_moved"`
	Warnings      []string `json:"warnings,omitempty"`
}

// ErrCombineJournalNotFound is returned when no journal has the given id.
var ErrCombineJournalNotFound = errors.New("combine journal not found")

// CombineUndoRefusedError is returned when UndoCombine will not run because
// the library changed after the combine. Nothing has been written when it is
// returned: every check runs before the first mutation, so an undo is never
// partial on account of a stale journal.
type CombineUndoRefusedError struct {
	JournalID string
	Reasons   []string
}

func (e *CombineUndoRefusedError) Error() string {
	return fmt.Sprintf("combine %s cannot be undone: %s", e.JournalID, strings.Join(e.Reasons, "; "))
}

func combineJournalKey(id string) string { return combineJournalPrefix + id }

// CombineJournalWriter is the raw-key write a journal needs. database.Store
// carries it (RawKVStore), so the production indexedStore forwards it.
type CombineJournalWriter interface {
	SetRaw(key string, value []byte) error
}

// NewCombineJournal returns an empty Pending journal for survivorID with a
// fresh chronologically-sortable id. Callers outside this package that fold
// books into a survivor (dedup.MergeSplitBookCluster) record their work in
// this same format so UndoCombine can reverse it.
func NewCombineJournal(survivorID string) *CombineJournal {
	return &CombineJournal{
		ID:             ulid.Make().String(),
		Status:         CombineJournalPending,
		CreatedAt:      time.Now().UTC(),
		SurvivorID:     survivorID,
		ITunesRemovals: []string{},
	}
}

// WriteCombineJournal persists j under its key.
func WriteCombineJournal(db CombineJournalWriter, j *CombineJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("marshal combine journal %s: %w", j.ID, err)
	}
	if err := db.SetRaw(combineJournalKey(j.ID), data); err != nil {
		return fmt.Errorf("write combine journal %s: %w", j.ID, err)
	}
	return nil
}

func (ms *Service) putCombineJournal(j *CombineJournal) error {
	return WriteCombineJournal(ms.db, j)
}

// GetCombineJournal returns the journal with the given id, or
// ErrCombineJournalNotFound.
func (ms *Service) GetCombineJournal(id string) (*CombineJournal, error) {
	if id == "" || strings.ContainsAny(id, ":/") {
		return nil, ErrCombineJournalNotFound
	}
	data, err := ms.db.GetRaw(combineJournalKey(id))
	if err != nil {
		return nil, fmt.Errorf("read combine journal %s: %w", id, err)
	}
	if data == nil {
		return nil, ErrCombineJournalNotFound
	}
	var j CombineJournal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("decode combine journal %s: %w", id, err)
	}
	return &j, nil
}

// ListCombineJournals returns journals newest-first, capped at limit (0 = all).
func (ms *Service) ListCombineJournals(limit int) ([]CombineJournal, error) {
	rows, err := ms.db.ScanPrefix(combineJournalPrefix)
	if err != nil {
		return nil, fmt.Errorf("list combine journals: %w", err)
	}
	out := make([]CombineJournal, 0, len(rows))
	for _, r := range rows {
		var j CombineJournal
		if err := json.Unmarshal(r.Value, &j); err != nil {
			mlog.Warn("combine journal: skipping undecodable row key=%s err=%s", logger.SanitizeLogValue(r.Key), logger.SanitizeLogValue(fmt.Sprint(err)))
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID > out[k].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// snapshotProgress captures every user's state and positions on one book.
// Users with nothing recorded are omitted from the map.
func snapshotProgress(db userPositionStore, users []database.User, bookID string) (map[string]*database.UserBookState, map[string][]database.UserPosition, error) {
	states := map[string]*database.UserBookState{}
	positions := map[string][]database.UserPosition{}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		st, err := db.GetUserBookState(u.ID, bookID)
		if err != nil {
			return nil, nil, fmt.Errorf("read progress state user=%s book=%s: %w", u.ID, bookID, err)
		}
		pos, err := db.ListUserPositionsForBook(u.ID, bookID)
		if err != nil {
			return nil, nil, fmt.Errorf("read positions user=%s book=%s: %w", u.ID, bookID, err)
		}
		if st != nil {
			states[u.ID] = st
		}
		if len(pos) > 0 {
			positions[u.ID] = pos
		}
	}
	return states, positions, nil
}

// sameProgress reports whether two snapshots describe the same progress. It
// compares the fields a listening session changes; UpdatedAt stamps on
// positions are included because a re-listen at the same second still moves it.
func sameProgress(aState, bState *database.UserBookState, aPos, bPos []database.UserPosition) bool {
	if (aState == nil) != (bState == nil) {
		return false
	}
	if aState != nil {
		if aState.Status != bState.Status || aState.ProgressPct != bState.ProgressPct ||
			aState.LastSegmentID != bState.LastSegmentID ||
			aState.TotalListenedSeconds != bState.TotalListenedSeconds ||
			!aState.LastActivityAt.Equal(bState.LastActivityAt) {
			return false
		}
	}
	if len(aPos) != len(bPos) {
		return false
	}
	key := func(p database.UserPosition) string {
		return fmt.Sprintf("%s|%v|%d", p.SegmentID, p.PositionSeconds, p.UpdatedAt.UnixNano())
	}
	as := make([]string, len(aPos))
	bs := make([]string, len(bPos))
	for i := range aPos {
		as[i] = key(aPos[i])
		bs[i] = key(bPos[i])
	}
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// writeProgress replaces one user's progress on bookID with the snapshot.
// A nil state is written as a neutralized row, the same shape
// mergeUserProgressFor uses to drain a loser (there is no delete primitive).
func writeProgress(db userPositionStore, userID, bookID string, st *database.UserBookState, pos []database.UserPosition, current *database.UserBookState) error {
	if err := db.ClearUserPositions(userID, bookID); err != nil {
		return fmt.Errorf("clear positions user=%s book=%s: %w", userID, bookID, err)
	}
	for _, p := range pos {
		if err := db.SetUserPosition(userID, bookID, p.SegmentID, p.PositionSeconds); err != nil {
			return fmt.Errorf("restore position %s user=%s book=%s: %w", p.SegmentID, userID, bookID, err)
		}
	}
	switch {
	case st != nil:
		restored := *st
		restored.UserID = userID
		restored.BookID = bookID
		if err := db.SetUserBookState(&restored); err != nil {
			return fmt.Errorf("restore state user=%s book=%s: %w", userID, bookID, err)
		}
	case current != nil:
		drained := *current
		drained.Status = ""
		drained.StatusManual = false
		drained.ProgressPct = 0
		drained.TotalListenedSeconds = 0
		drained.LastSegmentID = ""
		if err := db.SetUserBookState(&drained); err != nil {
			return fmt.Errorf("neutralize state user=%s book=%s: %w", userID, bookID, err)
		}
	}
	return nil
}

// syncMergeClearer is the reverse of SyncFollower.RecordSyncMerge. Resolved
// with database.AsCapability, which looks through the indexedStore decorator
// (the same resolution AsSyncIdentityStore uses for RecordSyncMerge itself).
type syncMergeClearer interface {
	ClearSyncMerge(loserBookID, winnerBookID string) error
}

// UndoCombine reverses the combine recorded under journalID: the absorbed books
// are restored from soft-delete with their original FilePath, their file rows
// (and sync_file inos, disc/track numbers and external-ID mappings) move back,
// the ABS redirect is cleared, per-user progress is restored, and any survivor
// metadata override is rolled back.
//
// It runs under mergeSerializeMu, so it cannot interleave with any merge,
// combine, or other undo. Every precondition is checked before the first
// write; if the library changed since the combine (a file moved, deleted, or
// re-merged, an absorbed book purged or restored, a mapping reassigned, the
// overridden metadata edited) it returns *CombineUndoRefusedError naming every
// reason and writes nothing.
func (ms *Service) UndoCombine(journalID string) (*CombineUndoResult, error) {
	mergeSerializeMu.Lock()
	defer mergeSerializeMu.Unlock()

	j, err := ms.GetCombineJournal(journalID)
	if err != nil {
		return nil, err
	}
	if reasons := ms.undoPreconditions(j); len(reasons) > 0 {
		return nil, &CombineUndoRefusedError{JournalID: j.ID, Reasons: reasons}
	}

	res, applyErr := ms.applyUndo(j)
	now := time.Now().UTC()
	if applyErr != nil {
		j.Status = CombineJournalUndoFailed
		j.LastError = applyErr.Error()
		if perr := ms.putCombineJournal(j); perr != nil {
			mlog.Error("combine undo: could not record failure on journal journal=%s err=%s", j.ID, logger.SanitizeLogValue(fmt.Sprint(perr)))
		}
		return nil, fmt.Errorf("undo combine %s: %w", j.ID, applyErr)
	}
	j.Status = CombineJournalUndone
	j.UndoneAt = &now
	j.LastError = ""
	j.Warnings = append(j.Warnings, res.Warnings...)
	if err := ms.putCombineJournal(j); err != nil {
		// The undo itself is done; a stale "applied" status is caught by the
		// preconditions (the absorbed books are live again) if retried.
		mlog.Error("combine undo: completed but journal status not updated journal=%s err=%s", j.ID, logger.SanitizeLogValue(fmt.Sprint(err)))
	}
	mlog.Info("combine undone journal=%s survivor=%s restored=%d files_moved=%d warnings=%d",
		j.ID, logger.SanitizeLogValue(j.SurvivorID), len(res.RestoredBooks), res.FilesMoved, len(res.Warnings))
	return res, nil
}

// undoPreconditions returns every reason the undo must not run. Read-only.
//
// A journal in CombineJournalUndoFailed is a RETRY: an earlier attempt passed
// these same checks and then stopped part way through applyUndo. Every item it
// already reversed is in its post-undo state now, so on a retry each check
// accepts either the post-combine state (not yet reversed) or the exact
// pre-combine state the journal records (already reversed). Anything else is
// still a refusal. What a retry cannot tell apart is "reversed by the failed
// attempt" from "put back by hand to exactly the recorded state"; both leave
// the row where the undo wants it, so treating them alike is safe.
func (ms *Service) undoPreconditions(j *CombineJournal) []string {
	var reasons []string
	retry := j.Status == CombineJournalUndoFailed
	if j.Status != CombineJournalApplied && !retry {
		return []string{fmt.Sprintf("journal status is %q, only %q (or %q, to retry) combines can be undone", j.Status, CombineJournalApplied, CombineJournalUndoFailed)}
	}
	survivor, err := ms.db.GetBookByID(j.SurvivorID)
	switch {
	case err != nil:
		return []string{fmt.Sprintf("load survivor %s: %v", j.SurvivorID, err)}
	case survivor == nil:
		return []string{fmt.Sprintf("survivor %s no longer exists", j.SurvivorID)}
	case survivor.IsSoftDeleted():
		reasons = append(reasons, fmt.Sprintf("survivor %s has since been deleted or merged away", j.SurvivorID))
	}

	checkFile := func(fm CombineFileMove) {
		f, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
		if err == nil && f == nil && retry && ms.fileAlreadyUndone(j, fm) {
			return
		}
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Sprintf("load file %s: %v", fm.FileID, err))
		case f == nil:
			reasons = append(reasons, fmt.Sprintf("file %s (%s) is no longer on survivor %s (moved, deleted, or re-merged)", fm.FileID, fm.FilePath, j.SurvivorID))
		case f.FilePath != fm.FilePath:
			reasons = append(reasons, fmt.Sprintf("file %s moved on disk since the combine (%s -> %s)", fm.FileID, fm.FilePath, f.FilePath))
		}
	}
	for _, fm := range j.SurvivorFiles {
		checkFile(fm)
	}
	// A reattached row came from a book OUTSIDE the combine (attachVirtualFile
	// moved it off its previous owner). Undo moves it back there, so that
	// owner has to still exist.
	inCombine := map[string]bool{j.SurvivorID: true}
	for _, a := range j.Absorbed {
		inCombine[a.BookID] = true
	}
	checkOwner := func(fm CombineFileMove) {
		if fm.Created || inCombine[fm.FromBookID] {
			return
		}
		if b, err := ms.db.GetBookByID(fm.FromBookID); err != nil || b == nil {
			reasons = append(reasons, fmt.Sprintf("file %s came from book %s, which no longer exists", fm.FileID, fm.FromBookID))
		}
	}
	for _, fm := range j.SurvivorFiles {
		checkOwner(fm)
	}
	for _, a := range j.Absorbed {
		for _, fm := range a.Files {
			checkOwner(fm)
		}
	}
	for _, fm := range j.SurvivorOwnFiles {
		checkFile(fm)
	}

	for _, a := range j.Absorbed {
		b, err := ms.db.GetBookByID(a.BookID)
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Sprintf("load absorbed book %s: %v", a.BookID, err))
			continue
		case b == nil:
			reasons = append(reasons, fmt.Sprintf("absorbed book %s no longer exists (purged)", a.BookID))
			continue
		case !b.IsSoftDeleted() && retry:
			// Restored by the failed attempt (step 1 runs first).
		case !b.IsSoftDeleted():
			reasons = append(reasons, fmt.Sprintf("absorbed book %s is no longer soft-deleted (restored since)", a.BookID))
		case a.MarkedForDeletionAt == nil || b.MarkedForDeletionAt == nil || !b.MarkedForDeletionAt.Equal(*a.MarkedForDeletionAt):
			reasons = append(reasons, fmt.Sprintf("absorbed book %s was re-deleted since the combine", a.BookID))
		}
		if files, err := ms.db.GetBookFiles(a.BookID); err != nil {
			reasons = append(reasons, fmt.Sprintf("load files of absorbed book %s: %v", a.BookID, err))
		} else {
			// On a retry the failed attempt may already have moved some of
			// this book's own rows back; those are expected. Any other row is
			// a file the book gained since, and still refuses.
			ownRows := map[string]bool{}
			if retry {
				for _, fm := range a.Files {
					if !fm.Created {
						ownRows[fm.FileID] = true
					}
				}
			}
			foreign := 0
			for _, f := range files {
				if !ownRows[f.ID] {
					foreign++
				}
			}
			if foreign > 0 {
				reasons = append(reasons, fmt.Sprintf("absorbed book %s has since been given %d file(s)", a.BookID, foreign))
			}
		}
		for _, fm := range a.Files {
			checkFile(fm)
		}
		for _, x := range a.ExternalIDs {
			owner, err := ms.db.GetBookByExternalID(x.Source, x.ExternalID)
			switch {
			case err != nil:
				reasons = append(reasons, fmt.Sprintf("load %s id %s: %v", x.Source, x.ExternalID, err))
			case retry && owner == a.BookID:
				// Moved back by the failed attempt.
			case owner != j.SurvivorID:
				reasons = append(reasons, fmt.Sprintf("%s id %s now maps to %q, not the survivor", x.Source, x.ExternalID, owner))
			}
		}
	}

	if o := j.Override; o != nil && survivor != nil {
		// On a retry the failed attempt may already have rolled a field back
		// (step 4 writes the row before the authors and locks).
		if o.Applied.Title != "" && survivor.Title != o.Applied.Title && !(retry && survivor.Title == o.TitleBefore) {
			reasons = append(reasons, fmt.Sprintf("survivor title was edited since the combine (%q, combine set %q)", survivor.Title, o.Applied.Title))
		}
		if o.Applied.Narrator != "" && !strPtrIs(survivor.Narrator, o.Applied.Narrator) &&
			!(retry && strPtrEqual(survivor.Narrator, o.NarratorBefore)) {
			reasons = append(reasons, "survivor narrator was edited since the combine")
		}
		if o.AuthorIDAfter != nil && !intPtrEqual(survivor.AuthorID, o.AuthorIDAfter) &&
			!(retry && intPtrEqual(survivor.AuthorID, o.AuthorIDBefore)) {
			reasons = append(reasons, "survivor author was edited since the combine")
		}
	}
	return reasons
}

// fileAlreadyUndone reports whether a file row that is no longer on the
// survivor is exactly where a completed undo of fm puts it: gone, for a row
// the combine created, or back on its recorded owner at its recorded path.
// Only consulted on a retry.
func (ms *Service) fileAlreadyUndone(j *CombineJournal, fm CombineFileMove) bool {
	if fm.Created {
		return true
	}
	if fm.FromBookID == j.SurvivorID {
		return false
	}
	f, err := ms.db.GetBookFileByID(fm.FromBookID, fm.FileID)
	return err == nil && f != nil && f.FilePath == fm.FilePath
}

func strPtrIs(p *string, v string) bool { return p != nil && *p == v }

func strPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return (a == nil || *a == "") && (b == nil || *b == "")
	}
	return *a == *b
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// applyUndo performs the reversal. Preconditions have passed.
func (ms *Service) applyUndo(j *CombineJournal) (*CombineUndoResult, error) {
	res := &CombineUndoResult{JournalID: j.ID, SurvivorID: j.SurvivorID}
	syncStore := database.AsSyncIdentityStore(ms.db)
	clearer, _ := database.AsCapability[syncMergeClearer](ms.db)

	// 1. Restore the absorbed rows first, so the file moves below land on live
	//    books. Re-fetch and patch only the fields the combine changed:
	//    UpdateBook is a full-column replace.
	for _, a := range j.Absorbed {
		b, err := ms.db.GetBookByID(a.BookID)
		if err != nil || b == nil {
			return res, fmt.Errorf("reload absorbed book %s: %v", a.BookID, err)
		}
		b.MarkedForDeletion = nil
		b.MarkedForDeletionAt = nil
		b.FilePath = a.FilePath
		b.VersionGroupID = a.VersionGroupID
		b.IsPrimaryVersion = a.IsPrimaryVersion
		if a.IsPrimaryVersion != nil && *a.IsPrimaryVersion && a.VersionGroupID != nil && *a.VersionGroupID != "" {
			// Another member may have been elected primary while this one was
			// soft-deleted. One primary per group, always: restore as a
			// non-primary member and say so.
			if members, gerr := ms.db.GetBooksByVersionGroup(*a.VersionGroupID); gerr == nil {
				for _, m := range members {
					if m.ID != a.BookID && !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
						f := false
						b.IsPrimaryVersion = &f
						res.Warnings = append(res.Warnings, fmt.Sprintf(
							"book %s was primary of version group %s, but %s is primary now; restored as a non-primary member",
							a.BookID, *a.VersionGroupID, m.ID))
						break
					}
				}
			}
		}
		if _, err := ms.db.UpdateBook(a.BookID, b); err != nil {
			return res, fmt.Errorf("restore absorbed book %s: %w", a.BookID, err)
		}
		res.RestoredBooks = append(res.RestoredBooks, a.BookID)
	}

	// 2. Files: move rows back to the book they came from (or delete the rows
	//    the combine created), carry sync_file inos, restore disc/track.
	moveBack := func(moves []CombineFileMove) error {
		// Every step here is safe to re-run after a failed attempt: a created
		// row already removed is skipped, and a row already back on its owner
		// is not moved again (only its disc/track are re-checked).
		byOwner := map[string][]CombineFileMove{}
		var owners []string
		for _, fm := range moves {
			if fm.Created {
				cur, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
				if err != nil {
					return fmt.Errorf("load file row %s created by the combine: %w", fm.FileID, err)
				}
				if cur == nil {
					continue // removed by an earlier attempt
				}
				if err := ms.db.DeleteBookFile(fm.FileID); err != nil {
					return fmt.Errorf("remove file row %s created by the combine: %w", fm.FileID, err)
				}
				continue
			}
			if _, ok := byOwner[fm.FromBookID]; !ok {
				owners = append(owners, fm.FromBookID)
			}
			byOwner[fm.FromBookID] = append(byOwner[fm.FromBookID], fm)
		}
		for _, owner := range owners {
			group := byOwner[owner]
			ids := make([]string, 0, len(group))
			for _, fm := range group {
				cur, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
				if err != nil {
					return fmt.Errorf("load file %s: %w", fm.FileID, err)
				}
				if cur != nil {
					ids = append(ids, fm.FileID)
				}
			}
			if len(ids) > 0 {
				if err := ms.db.MoveBookFilesToBook(ids, j.SurvivorID, owner); err != nil {
					return fmt.Errorf("move %d file(s) back to %s: %w", len(ids), owner, err)
				}
				FollowFileMove(ms.db, j.SurvivorID, owner, ids)
				res.FilesMoved += len(ids)
			}
			for _, fm := range group {
				f, err := ms.db.GetBookFileByID(owner, fm.FileID)
				if err != nil || f == nil {
					return fmt.Errorf("reload moved-back file %s: %v", fm.FileID, err)
				}
				if f.DiscNumber == fm.DiscBefore && f.TrackNumber == fm.TrackBefore {
					continue
				}
				f.DiscNumber = fm.DiscBefore
				f.TrackNumber = fm.TrackBefore
				if err := ms.db.UpdateBookFile(f.ID, f); err != nil {
					return fmt.Errorf("restore disc/track of file %s: %w", f.ID, err)
				}
			}
		}
		return nil
	}
	for _, a := range j.Absorbed {
		if err := moveBack(a.Files); err != nil {
			return res, err
		}
	}
	if err := moveBack(j.SurvivorFiles); err != nil {
		return res, err
	}
	for _, fm := range j.SurvivorOwnFiles {
		f, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
		if err != nil || f == nil {
			return res, fmt.Errorf("reload survivor file %s: %v", fm.FileID, err)
		}
		if f.DiscNumber == fm.DiscBefore && f.TrackNumber == fm.TrackBefore {
			continue
		}
		f.DiscNumber, f.TrackNumber = fm.DiscBefore, fm.TrackBefore
		if err := ms.db.UpdateBookFile(f.ID, f); err != nil {
			return res, fmt.Errorf("restore disc/track of survivor file %s: %w", f.ID, err)
		}
	}

	// 3. External IDs, sync redirect, progress — per absorbed book, newest
	//    first, because each FollowMerge saw the survivor as the previous one
	//    left it.
	for i := len(j.Absorbed) - 1; i >= 0; i-- {
		a := j.Absorbed[i]
		for _, x := range a.ExternalIDs {
			if err := ms.db.ReassignExternalID(x.Source, x.ExternalID, a.BookID); err != nil {
				return res, fmt.Errorf("move %s id %s back to %s: %w", x.Source, x.ExternalID, a.BookID, err)
			}
		}
		if a.SyncRedirected && syncStore != nil {
			if clearer == nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("store cannot clear sync redirects; %s still redirects to the survivor for ABS clients", a.BookID))
			} else if err := clearer.ClearSyncMerge(a.BookID, j.SurvivorID); err != nil {
				return res, fmt.Errorf("clear sync redirect %s -> %s: %w", a.BookID, j.SurvivorID, err)
			}
		}
		for _, p := range a.Progress {
			curAbs, err := ms.db.GetUserBookState(p.UserID, a.BookID)
			if err != nil {
				return res, fmt.Errorf("read progress user=%s book=%s: %w", p.UserID, a.BookID, err)
			}
			if err := writeProgress(ms.db, p.UserID, a.BookID, p.AbsorbedState, p.AbsorbedPositions, curAbs); err != nil {
				return res, err
			}
			curSurv, err := ms.db.GetUserBookState(p.UserID, j.SurvivorID)
			if err != nil {
				return res, fmt.Errorf("read progress user=%s book=%s: %w", p.UserID, j.SurvivorID, err)
			}
			curSurvPos, err := ms.db.ListUserPositionsForBook(p.UserID, j.SurvivorID)
			if err != nil {
				return res, fmt.Errorf("read positions user=%s book=%s: %w", p.UserID, j.SurvivorID, err)
			}
			if sameProgress(curSurv, p.SurvivorStateBefore, curSurvPos, p.SurvivorPosBefore) {
				// Already restored (an earlier attempt of this undo got here).
				continue
			}
			if !sameProgress(curSurv, p.SurvivorStateAfter, curSurvPos, p.SurvivorPosAfter) {
				// The user has listened to the survivor since. Their newer
				// position there is theirs; leave it rather than rewind it.
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"user %s progress on survivor %s changed after the combine; left as is", p.UserID, j.SurvivorID))
				continue
			}
			if err := writeProgress(ms.db, p.UserID, j.SurvivorID, p.SurvivorStateBefore, p.SurvivorPosBefore, curSurv); err != nil {
				return res, err
			}
		}
	}

	// 4. Survivor metadata override.
	if o := j.Override; o != nil {
		fresh, err := ms.db.GetBookByID(j.SurvivorID)
		if err != nil || fresh == nil {
			return res, fmt.Errorf("reload survivor %s: %v", j.SurvivorID, err)
		}
		if o.Applied.Title != "" {
			fresh.Title = o.TitleBefore
		}
		if o.Applied.Narrator != "" {
			fresh.Narrator = o.NarratorBefore
		}
		if o.AuthorIDAfter != nil {
			fresh.AuthorID = o.AuthorIDBefore
		}
		if _, err := ms.db.UpdateBook(fresh.ID, fresh); err != nil {
			return res, fmt.Errorf("restore survivor metadata: %w", err)
		}
		if o.AuthorIDAfter != nil {
			if err := ms.db.SetBookAuthors(j.SurvivorID, o.AuthorsBefore); err != nil {
				return res, fmt.Errorf("restore survivor authors: %w", err)
			}
		}
		for field, prior := range o.FieldStates {
			if prior == nil {
				if err := ms.db.DeleteMetadataFieldState(j.SurvivorID, field); err != nil {
					return res, fmt.Errorf("remove override lock %s: %w", field, err)
				}
				continue
			}
			restored := *prior
			if err := ms.db.UpsertMetadataFieldState(&restored); err != nil {
				return res, fmt.Errorf("restore override lock %s: %w", field, err)
			}
		}
	}

	// 5. Aggregates. MoveBookFilesToBook recomputes both of its books; the
	//    survivor is recomputed once more after the created-row deletes, and
	//    the absorbed books because a FilePath restore can change them.
	for _, id := range append([]string{j.SurvivorID}, res.RestoredBooks...) {
		if err := ms.db.RecomputeBookAggregates(id); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("recompute aggregates of %s: %v", id, err))
		}
	}
	return res, nil
}
