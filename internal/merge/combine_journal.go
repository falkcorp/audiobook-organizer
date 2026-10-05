// file: internal/merge/combine_journal.go
// version: 1.11.0
// guid: 4e8b1c27-93d5-4f0a-a6e2-7c51d9b03f18
// last-edited: 2026-10-05

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
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
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
	// FilledEmpty records metadata a merge copied from absorbed books onto the
	// survivor where the survivor's field was EMPTY (never an overwrite).
	// Undo empties each such field again if it still holds the filled value.
	FilledEmpty *CombineFillEmpty `json:"filled_empty,omitempty"`
	// StepJournaled is set by writers that re-write the journal after EVERY
	// step (dedup.MergeSplitBookClusterWithOptions). Only such a journal can
	// be undone while still Pending: it names everything done before a crash.
	// CombineBooks writes Pending once up front and Applied at the end, so its
	// Pending journal does not record what ran and stays non-undoable.
	StepJournaled bool `json:"step_journaled,omitempty"`

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
	// LeftLive is set when the merge could not retire this book after some of
	// its steps ran (a split-book merge keeps such a book in the journal so
	// undo still moves its files, ids and progress back). Undo does not
	// require it to be soft-deleted.
	LeftLive bool `json:"left_live,omitempty"`
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

// CombineFillEmpty is the set of survivor fields a merge filled because they
// were empty. Each non-nil field is a value written; its before-value was
// empty (nil, or no book_authors rows for Authors).
type CombineFillEmpty struct {
	ASIN           *string               `json:"asin,omitempty"`
	Narrator       *string               `json:"narrator,omitempty"`
	SeriesID       *int                  `json:"series_id,omitempty"`
	SeriesSequence *int                  `json:"series_sequence,omitempty"`
	AuthorID       *int                  `json:"author_id,omitempty"`
	Authors        []database.BookAuthor `json:"authors,omitempty"`
}

// Empty reports whether nothing was filled.
func (f *CombineFillEmpty) Empty() bool {
	return f == nil || (f.ASIN == nil && f.Narrator == nil && f.SeriesID == nil &&
		f.SeriesSequence == nil && f.AuthorID == nil && len(f.Authors) == 0)
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
// snapshotPair snapshots every user's progress on BOTH books of a follow.
// Per user, not all-or-nothing: a user whose state or positions on either
// book cannot be read is left out of the snapshot and returned in skipped, so
// one corrupt row costs only that user (who must then NOT be followed: never
// move progress that is not journaled), not every other user's undo.
func snapshotPair(db userPositionStore, users []database.User, absorbedID, survivorID string) (s progressSnap, followable []database.User, skipped map[string]error) {
	s = progressSnap{
		absState: map[string]*database.UserBookState{}, survState: map[string]*database.UserBookState{},
		absPos: map[string][]database.UserPosition{}, survPos: map[string][]database.UserPosition{},
	}
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		aSt, aPos, err := readUserProgress(db, u.ID, absorbedID)
		if err == nil {
			var sSt *database.UserBookState
			var sPos []database.UserPosition
			if sSt, sPos, err = readUserProgress(db, u.ID, survivorID); err == nil {
				if aSt != nil {
					s.absState[u.ID] = aSt
				}
				if len(aPos) > 0 {
					s.absPos[u.ID] = aPos
				}
				if sSt != nil {
					s.survState[u.ID] = sSt
				}
				if len(sPos) > 0 {
					s.survPos[u.ID] = sPos
				}
				followable = append(followable, u)
				continue
			}
		}
		if skipped == nil {
			skipped = map[string]error{}
		}
		skipped[u.ID] = err
	}
	return s, followable, skipped
}

// readUserProgress reads one user's state and positions on one book.
func readUserProgress(db userPositionStore, userID, bookID string) (*database.UserBookState, []database.UserPosition, error) {
	st, err := db.GetUserBookState(userID, bookID)
	if err != nil {
		return nil, nil, fmt.Errorf("read progress state user=%s book=%s: %w", userID, bookID, err)
	}
	pos, err := db.ListUserPositionsForBook(userID, bookID)
	if err != nil {
		return nil, nil, fmt.Errorf("read positions user=%s book=%s: %w", userID, bookID, err)
	}
	return st, pos, nil
}

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
	// carryPosition keeps each row's original UpdatedAt (the ABS lastUpdate):
	// restoring with SetUserPosition stamped the undo time, so an undone book
	// looked freshly listened and won the next merge's newest-wins rule.
	for _, p := range sortPositionsOldestFirst(pos) {
		if err := carryPosition(db, userID, bookID, p); err != nil {
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
		if err := db.SetUserBookState(drainedUserState(*current)); err != nil {
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
	// A Pending journal is a merge that stopped part-way (a crash, or a
	// journal that could not be finalized). It records every step done so
	// far, and undo replays exactly those, with the same tolerance as a retry:
	// a step that never ran leaves its row where it already is.
	pending := j.Status == CombineJournalPending && j.StepJournaled
	retry := j.Status == CombineJournalUndoFailed || pending
	if j.Status != CombineJournalApplied && !retry {
		return []string{fmt.Sprintf("journal status is %q, only %q (or %q / %q, to replay) combines can be undone", j.Status, CombineJournalApplied, CombineJournalUndoFailed, CombineJournalPending)}
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

	checkFileL := func(fm CombineFileMove, lenient bool) {
		f, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
		if err == nil && f == nil && lenient && ms.fileAlreadyUndone(j, fm) {
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
	checkFile := func(fm CombineFileMove) { checkFileL(fm, retry) }
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
		// A book the merge left live (LeftLive) was never retired, and in a
		// partial journal a later step may not have run: both are checked with
		// replay tolerance.
		lenient := retry || a.LeftLive
		b, err := ms.db.GetBookByID(a.BookID)
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Sprintf("load absorbed book %s: %v", a.BookID, err))
			continue
		case b == nil:
			reasons = append(reasons, fmt.Sprintf("absorbed book %s no longer exists (purged)", a.BookID))
			continue
		case !b.IsSoftDeleted() && lenient:
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
			if lenient {
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
			checkFileL(fm, lenient)
		}
		for _, x := range a.ExternalIDs {
			owner, err := ms.db.GetBookByExternalID(x.Source, x.ExternalID)
			switch {
			case err != nil:
				reasons = append(reasons, fmt.Sprintf("load %s id %s: %v", x.Source, x.ExternalID, err))
			case lenient && owner == a.BookID:
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

// RestoreFollowedProgress reverses one absorbed book's journaled follow
// (FollowAbsorbedJournaled, or CombineBooks' per-absorbed step): the ABS sync
// redirect absorbed -> survivor is cleared when syncRedirected, a pending
// user-state repair for the pair is dropped (the absorbed book is coming back,
// and the sweep would otherwise drain it onto the survivor again), every
// user's absorbed-book state and positions are written back, and the
// survivor's are put back to their before-state. A survivor a user has
// listened to since is reconciled instead (reconcileTouchedSurvivor): what
// the follow brought is taken back off it and what the user did there since
// stays, so the carried state ends up on exactly one book -- the absorbed
// one -- and its listened time is not counted on both. That is returned as a
// warning, not an error. Safe to run twice: a survivor already back at its
// before-state is left alone.
//
// The absorbed side gets the same care (restoreAbsorbedSide): the absorbed
// book is often still live when this runs (CarryStateBeforeHardDelete puts a
// refused carry back onto the duplicate it keeps), so a client can write a
// new position to it after the follow drained it -- and that write is what
// makes the carry incomplete. The before-snapshot is then combined with what
// is there, newer kept, instead of written over it.
//
// Shared by UndoCombine and the operation revert of a user_state_follow row.
func RestoreFollowedProgress(db UserProgressMerger, survivorID, absorbedID string, syncRedirected bool, progress []CombineUserProgress) ([]string, error) {
	var warnings []string
	if syncRedirected && database.AsSyncIdentityStore(db) != nil {
		if clearer, _ := database.AsCapability[syncMergeClearer](db); clearer == nil {
			warnings = append(warnings, fmt.Sprintf("store cannot clear sync redirects; %s still redirects to the survivor for ABS clients", absorbedID))
		} else if err := clearer.ClearSyncMerge(absorbedID, survivorID); err != nil {
			return warnings, fmt.Errorf("clear sync redirect %s -> %s: %w", absorbedID, survivorID, err)
		}
	}
	if err := db.DeleteRaw(pendingRepairKey(absorbedID, survivorID)); err != nil {
		return warnings, fmt.Errorf("drop pending user-state repair %s -> %s: %w", absorbedID, survivorID, err)
	}
	for _, p := range progress {
		w, err := restoreAbsorbedSide(db, p.UserID, absorbedID, p.AbsorbedState, p.AbsorbedPositions)
		if err != nil {
			return warnings, err
		}
		if w != "" {
			warnings = append(warnings, w)
		}
		curSurv, err := db.GetUserBookState(p.UserID, survivorID)
		if err != nil {
			return warnings, fmt.Errorf("read progress user=%s book=%s: %w", p.UserID, survivorID, err)
		}
		curSurvPos, err := db.ListUserPositionsForBook(p.UserID, survivorID)
		if err != nil {
			return warnings, fmt.Errorf("read positions user=%s book=%s: %w", p.UserID, survivorID, err)
		}
		if sameProgress(curSurv, p.SurvivorStateBefore, curSurvPos, p.SurvivorPosBefore) {
			// Already restored (an earlier attempt of this undo got here).
			continue
		}
		if !sameProgress(curSurv, p.SurvivorStateAfter, curSurvPos, p.SurvivorPosAfter) {
			if p.SurvivorStateAfter == nil && len(p.SurvivorPosAfter) == 0 {
				// No after-snapshot (the follow failed, or its re-read did):
				// what the follow wrote cannot be told from the user's own
				// listening, so nothing is taken off the survivor.
				warnings = append(warnings, fmt.Sprintf(
					"user %s progress on survivor %s changed after the merge and no after-merge record exists to separate it; left as is", p.UserID, survivorID))
				continue
			}
			// The user has listened to the survivor since. Take back only
			// what the follow brought, keep what they did there since.
			if err := reconcileTouchedSurvivor(db, p, survivorID, curSurv, curSurvPos); err != nil {
				return warnings, err
			}
			warnings = append(warnings, fmt.Sprintf(
				"user %s progress on survivor %s changed after the merge; the carried state was taken back off it and their listening there since was kept", p.UserID, survivorID))
			continue
		}
		if err := writeProgress(db, p.UserID, survivorID, p.SurvivorStateBefore, p.SurvivorPosBefore, curSurv); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// reconcileTouchedSurvivor puts one user's survivor progress back to its
// before-follow state when the user has listened there since the follow,
// keeping that listening. It is a three-way merge per field with the
// after-follow snapshot as the base: a field still equal to what the follow
// left goes back to its before value; a field the user changed since keeps
// the user's value.
//
//   - positions, per segment: a row still equal to the follow's goes back to
//     the before row (or is removed when the survivor had none there); a row
//     written since stays; a segment the follow left but the user cleared
//     since (a reset) is not brought back. A before row the follow did not
//     leave and is not there now is put back (never dropped).
//   - status, manual flag, progress %, FinishedAt (one group, as the client
//     writes them together), last played, hide, the reset tombstone: each
//     goes back to before when unchanged since the follow.
//   - listened time: before + what the user added since the follow, so the
//     carried seconds -- which go back to the absorbed book -- are not on
//     both books. A counter that went DOWN since (a reset) keeps its value.
//   - LastSegmentID names the newest remaining position's segment.
func reconcileTouchedSurvivor(db userPositionStore, p CombineUserProgress, survivorID string, cur *database.UserBookState, curPos []database.UserPosition) error {
	afterBy := map[string]database.UserPosition{}
	for _, a := range p.SurvivorPosAfter {
		afterBy[a.SegmentID] = a
	}
	beforeBy := map[string]database.UserPosition{}
	for _, b := range p.SurvivorPosBefore {
		beforeBy[b.SegmentID] = b
	}
	var want []database.UserPosition
	seen := map[string]bool{}
	for _, c := range curPos {
		seen[c.SegmentID] = true
		a, inAfter := afterBy[c.SegmentID]
		if !inAfter || a.PositionSeconds != c.PositionSeconds || !a.UpdatedAt.Equal(c.UpdatedAt) {
			want = append(want, c) // written since the follow
			continue
		}
		if b, ok := beforeBy[c.SegmentID]; ok {
			want = append(want, b)
		}
	}
	for _, b := range p.SurvivorPosBefore {
		if _, inAfter := afterBy[b.SegmentID]; !inAfter && !seen[b.SegmentID] {
			want = append(want, b)
		}
	}
	if err := writePositionsDiff(db, p.UserID, survivorID, curPos, want); err != nil {
		return err
	}
	if cur == nil {
		return nil
	}
	var before, after database.UserBookState
	if p.SurvivorStateBefore != nil {
		before = *p.SurvivorStateBefore
	}
	if p.SurvivorStateAfter != nil {
		after = *p.SurvivorStateAfter
	}
	st := *cur
	st.UserID, st.BookID = p.UserID, survivorID
	if cur.Status == after.Status && cur.StatusManual == after.StatusManual && cur.ProgressPct == after.ProgressPct {
		st.Status, st.StatusManual, st.ProgressPct, st.FinishedAt = before.Status, before.StatusManual, before.ProgressPct, before.FinishedAt
	}
	if cur.LastActivityAt.Equal(after.LastActivityAt) {
		st.LastActivityAt = before.LastActivityAt
	}
	if cur.HideFromContinueListening == after.HideFromContinueListening {
		st.HideFromContinueListening = before.HideFromContinueListening
	}
	if timePtrEqual(cur.ProgressResetAt, after.ProgressResetAt) && slices.Equal(cur.ProgressResetPositions, after.ProgressResetPositions) {
		st.ProgressResetAt, st.ProgressResetPositions = before.ProgressResetAt, before.ProgressResetPositions
	}
	if added := cur.TotalListenedSeconds - after.TotalListenedSeconds; added >= 0 {
		st.TotalListenedSeconds = before.TotalListenedSeconds + added
	}
	st.LastSegmentID = before.LastSegmentID
	var newest *database.UserPosition
	for i := range want {
		if newest == nil || want[i].UpdatedAt.After(newest.UpdatedAt) {
			newest = &want[i]
		}
	}
	if newest != nil {
		st.LastSegmentID = newest.SegmentID
	}
	if err := db.SetUserBookState(&st); err != nil {
		return fmt.Errorf("reconcile state user=%s book=%s: %w", p.UserID, survivorID, err)
	}
	return nil
}

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// restoreAbsorbedSide puts one user's before-snapshot (snapSt, snapPos) back
// on absorbedID without rewinding anything written there since the follow.
//
//   - Already there (sameProgress): nothing to write.
//   - Nothing carryable there (the drained row the follow left, or no row):
//     the snapshot is written back whole, as before (writeProgress).
//   - Anything else is progress the snapshot does not hold: a client wrote to
//     the book after the follow drained it, or the follow stopped part way
//     for this user. The two are combined (combineOnRestore) -- the newer
//     side wins the position, nothing on either side is dropped -- and
//     written (writePositionsDiff): rows the combined set keeps as they are
//     are not rewritten, and the book's rows are cleared first only when the
//     combined set drops one (a reset kept over older positions), so the
//     rows on the book always match the state's LastSegmentID. A newer write
//     is reported as a warning.
func restoreAbsorbedSide(db userPositionStore, userID, absorbedID string, snapSt *database.UserBookState, snapPos []database.UserPosition) (string, error) {
	cur, err := db.GetUserBookState(userID, absorbedID)
	if err != nil {
		return "", fmt.Errorf("read progress user=%s book=%s: %w", userID, absorbedID, err)
	}
	curPos, err := db.ListUserPositionsForBook(userID, absorbedID)
	if err != nil {
		return "", fmt.Errorf("read positions user=%s book=%s: %w", userID, absorbedID, err)
	}
	if sameProgress(cur, snapSt, curPos, snapPos) {
		return "", nil
	}
	if !hasCarryableState(cur, curPos) {
		return "", writeProgress(db, userID, absorbedID, snapSt, snapPos, cur)
	}
	snap := userStateSide{state: snapSt, positions: snapPos}
	now := userStateSide{state: cur, positions: curPos}
	st, positions := combineOnRestore(userID, absorbedID, snap, now)
	if err := writePositionsDiff(db, userID, absorbedID, curPos, positions); err != nil {
		return "", err
	}
	if st != nil {
		if err := db.SetUserBookState(st); err != nil {
			return "", fmt.Errorf("restore state user=%s book=%s: %w", userID, absorbedID, err)
		}
	}
	if lastUpdate(cur, curPos).After(lastUpdate(snapSt, snapPos)) {
		return fmt.Sprintf("user %s progress on %s was written after the follow; combined with the put-back state, newer position kept", userID, absorbedID), nil
	}
	return "", nil
}

// writePositionsDiff makes bookID's position rows for userID equal want,
// given that have is what is there now. A row want keeps unchanged is not
// rewritten; a changed or new row is written keeping its UpdatedAt
// (carryPosition). Only when want drops a segment that have holds are the
// book's rows cleared first and every wanted row written, because there is
// no single-row delete.
func writePositionsDiff(db userPositionStore, userID, bookID string, have, want []database.UserPosition) error {
	wantBy := make(map[string]database.UserPosition, len(want))
	for _, w := range want {
		wantBy[w.SegmentID] = w
	}
	haveBy := make(map[string]database.UserPosition, len(have))
	dropped := false
	for _, h := range have {
		haveBy[h.SegmentID] = h
		if _, ok := wantBy[h.SegmentID]; !ok {
			dropped = true
		}
	}
	if dropped {
		if err := db.ClearUserPositions(userID, bookID); err != nil {
			return fmt.Errorf("clear positions user=%s book=%s: %w", userID, bookID, err)
		}
		haveBy = nil
	}
	for _, w := range sortPositionsOldestFirst(want) {
		if h, ok := haveBy[w.SegmentID]; ok && h.PositionSeconds == w.PositionSeconds && h.UpdatedAt.Equal(w.UpdatedAt) {
			continue
		}
		if err := carryPosition(db, userID, bookID, w); err != nil {
			return fmt.Errorf("restore position %s user=%s book=%s: %w", w.SegmentID, userID, bookID, err)
		}
	}
	return nil
}

// combineOnRestore combines a follow's before-snapshot of one user's progress
// on bookID with what is on bookID now, losing neither. Both sides are the
// SAME book, so this is not the two-book merge rule (planUserStateMerge)
// although it shares its parts:
//
//   - which side is newer: the later lastUpdate. A side with nothing
//     carryable is never the newer one. When either side has no timestamp at
//     all (a legacy row written before positions were stamped reads as the
//     zero time), the side whose furthest position is farther ahead is the
//     newer one, the current side on a tie (owner decision 2026-10-05: with
//     no time to compare, the farther-ahead position wins).
//   - positions: the union by segment; where both have a segment, the newer
//     UpdatedAt wins (the current row on a tie). When either row of a
//     segment has no timestamp, the farther-ahead position wins instead (the
//     current row on a tie), by the same owner decision: a legacy 4000s row
//     is not rewound to a dated 3500s one.
//   - state: the newer side's row (status, progress %, manual flag) stands;
//     finished is sticky -- an older Finished is kept over a newer
//     unfinished status -- EXCEPT over a newer deliberate status: a newer
//     side whose status is manual (StatusManual, set by readstatus' mark
//     unfinished/in progress, which stamps LastActivityAt) and was set after
//     the older side's last update wins, because on one book the user's
//     later explicit choice is their state. Last played is the later, hide
//     is OR'd, the reset tombstone is the later and its discarded positions
//     are unioned. A current state that is only the drained residue of the
//     follow counts as no state: it would otherwise win with an empty status.
//   - listened time: the larger of the two (owner-confirmed 2026-10-05: the
//     farther-ahead time). Both sides are the same book's counter; the
//     drained side restarted it at zero.
//   - a newer side that RESET its progress after the older side's last
//     update keeps the reset: the older side's positions and listened time
//     are not brought back over it, its positions are left out of the result
//     (the caller clears them off the book: writePositionsDiff) and added to
//     the reset's discarded positions, with the older side's own discarded
//     positions, so offline replay cannot resurrect them. Hide is still
//     OR'd: the follow drained hide off this book, so a reset made here
//     after it did not un-hide anything the user had hidden.
//
// Clock skew: position UpdatedAt is stamped by this server when the write
// arrives (SetUserPosition uses time.Now; only carried copies keep an earlier
// stamp, which was itself a server stamp), and LastActivityAt and
// ProgressResetAt are server-stamped too (readstatus, the ABS progress
// handlers). So a client's clock never orders the two sides; there is no
// client-supplied time to prefer or reject. The one ordering a server stamp
// gets wrong is an offline backlog replayed late: it is stamped at receipt,
// so it counts as newer than a listen it predates. That is the reset
// tombstone's job (ProgressResetPositions), which is why the reset path
// unions the discarded positions.
//
// The state's LastSegmentID names the newest position's segment.
func combineOnRestore(userID, bookID string, snap, now userStateSide) (*database.UserBookState, []database.UserPosition) {
	if now.state != nil && !hasCarryableState(now.state, nil) {
		now.state = nil
	}
	older, newer, newerIsNow := restoreOrder(snap, now)
	resetSince := newer.state != nil && newer.state.ProgressResetAt != nil &&
		newer.state.ProgressResetAt.After(lastUpdate(older.state, older.positions))

	bySeg := map[string]database.UserPosition{}
	var segs []string
	add := func(ps []database.UserPosition, winsTie bool) {
		for _, p := range ps {
			prev, ok := bySeg[p.SegmentID]
			if !ok {
				segs = append(segs, p.SegmentID)
				bySeg[p.SegmentID] = p
				continue
			}
			if positionWins(p, prev, winsTie) {
				bySeg[p.SegmentID] = p
			}
		}
	}
	if !resetSince {
		add(older.positions, false)
	}
	// The current row wins a tie: added last with winsTie when now is the
	// newer side; when it is the older side its rows went in first and the
	// snapshot's must beat them outright.
	add(newer.positions, newerIsNow)
	positions := make([]database.UserPosition, 0, len(segs))
	var newest *database.UserPosition
	for _, sg := range segs {
		p := bySeg[sg]
		positions = append(positions, p)
		if newest == nil || p.UpdatedAt.After(newest.UpdatedAt) {
			newest = &positions[len(positions)-1]
		}
	}

	if older.state == nil && newer.state == nil {
		return nil, positions
	}
	var st database.UserBookState
	if newer.state != nil {
		st = *newer.state
	} else {
		st = *older.state
	}
	st.UserID, st.BookID = userID, bookID
	if !resetSince {
		if fin := olderFinished(older, newer); fin != nil && !newerDeliberateStatus(older, newer) {
			st.Status = database.UserBookStatusFinished
			st.StatusManual = fin.StatusManual
			st.FinishedAt = fin.FinishedAt
			st.ProgressPct = fin.ProgressPct
		}
		for _, s := range []*database.UserBookState{older.state, newer.state} {
			if s != nil && s.TotalListenedSeconds > st.TotalListenedSeconds {
				st.TotalListenedSeconds = s.TotalListenedSeconds
			}
		}
	}
	for _, s := range []*database.UserBookState{older.state, newer.state} {
		if s == nil {
			continue
		}
		if s.LastActivityAt.After(st.LastActivityAt) {
			st.LastActivityAt = s.LastActivityAt
		}
		if s.HideFromContinueListening {
			st.HideFromContinueListening = true
		}
		if s.ProgressResetAt != nil && (st.ProgressResetAt == nil || s.ProgressResetAt.After(*st.ProgressResetAt)) {
			t := *s.ProgressResetAt
			st.ProgressResetAt = &t
		}
	}
	var discarded *database.UserBookState
	if resetSince && len(older.positions) > 0 {
		d := database.UserBookState{}
		for _, p := range sortPositionsOldestFirst(older.positions) {
			d.ProgressResetPositions = append(d.ProgressResetPositions, p.PositionSeconds)
		}
		discarded = &d
	}
	st.ProgressResetPositions = unionResetPositions(older.state, discarded, newer.state)
	switch {
	case newest != nil:
		st.LastSegmentID = newest.SegmentID
	case resetSince:
		st.LastSegmentID = ""
	}
	return &st, positions
}

// restoreOrder returns snap and now as (older, newer) for combineOnRestore,
// and whether the newer one is now.
func restoreOrder(snap, now userStateSide) (older, newer userStateSide, newerIsNow bool) {
	switch {
	case !hasCarryableState(now.state, now.positions):
		return now, snap, false
	case !hasCarryableState(snap.state, snap.positions):
		return snap, now, true
	}
	su, nu := lastUpdate(snap.state, snap.positions), lastUpdate(now.state, now.positions)
	if su.IsZero() || nu.IsZero() || undatedPosition(snap.positions) || undatedPosition(now.positions) {
		if furthestPosition(snap.positions) > furthestPosition(now.positions) {
			return now, snap, false
		}
		return snap, now, true
	}
	if su.After(nu) {
		return now, snap, false
	}
	return snap, now, true
}

// positionWins reports whether p replaces prev for one segment: the newer
// UpdatedAt, or -- when either has no timestamp -- the farther-ahead
// position; winsTie settles an equal comparison in p's favour.
func positionWins(p, prev database.UserPosition, winsTie bool) bool {
	if p.UpdatedAt.IsZero() || prev.UpdatedAt.IsZero() {
		return p.PositionSeconds > prev.PositionSeconds || (winsTie && p.PositionSeconds == prev.PositionSeconds)
	}
	return p.UpdatedAt.After(prev.UpdatedAt) || (winsTie && p.UpdatedAt.Equal(prev.UpdatedAt))
}

func undatedPosition(ps []database.UserPosition) bool {
	for i := range ps {
		if ps[i].UpdatedAt.IsZero() {
			return true
		}
	}
	return false
}

func furthestPosition(ps []database.UserPosition) float64 {
	var f float64
	for i := range ps {
		f = max(f, ps[i].PositionSeconds)
	}
	return f
}

// olderFinished is the Finished state finished-is-sticky keeps: the newer
// side's when it is finished, else the older side's, else nil.
func olderFinished(older, newer userStateSide) *database.UserBookState {
	switch {
	case isFinished(newer.state):
		return newer.state
	case isFinished(older.state):
		return older.state
	}
	return nil
}

// newerDeliberateStatus reports whether the newer side holds a manual
// (user-set) status that is not Finished and was set after the older side's
// last update: the user un-finished the book after it was finished, so the
// sticky Finished must not override it.
func newerDeliberateStatus(older, newer userStateSide) bool {
	n := newer.state
	return n != nil && n.StatusManual && !isFinished(n) &&
		n.LastActivityAt.After(lastUpdate(older.state, older.positions))
}

// applyUndo performs the reversal. Preconditions have passed.
func (ms *Service) applyUndo(j *CombineJournal) (*CombineUndoResult, error) {
	res := &CombineUndoResult{JournalID: j.ID, SurvivorID: j.SurvivorID}

	// 1. Restore the absorbed rows first, so the file moves below land on live
	//    books. Re-fetch and patch only the fields the combine changed:
	//    UpdateBook is a full-column replace.
	for _, a := range j.Absorbed {
		gid := ""
		if a.VersionGroupID != nil {
			gid = strings.TrimSpace(*a.VersionGroupID)
		}
		// Membership lock: the shell's current group (the no-group sentinel
		// when it has none) and the group it is restored into, in ONE
		// acquisition (merge lock, then group stripes, then the book's write
		// lock), held across the incumbent read and the whole-row restore.
		unlockGroup, _, err := versionprimary.LockBookGroups(ms.db, []string{a.BookID}, gid)
		if err != nil {
			return res, fmt.Errorf("lock version groups of absorbed book %s: %w", a.BookID, err)
		}
		b, err := ms.db.GetBookByID(a.BookID)
		if err != nil || b == nil {
			unlockGroup()
			return res, fmt.Errorf("reload absorbed book %s: %v", a.BookID, err)
		}
		b.MarkedForDeletion = nil
		b.MarkedForDeletionAt = nil
		// The library state is decided in step 2b, once this book's files are
		// back: at this point they are still on the survivor, so the restore
		// rule would read no files and answer "imported" for every shell.
		// MergedIntoBookID is the journal's to restore, not cleared here.
		b.FilePath = a.FilePath
		b.VersionGroupID = a.VersionGroupID
		b.IsPrimaryVersion = a.IsPrimaryVersion
		if gid != "" && a.IsPrimaryVersion != nil && *a.IsPrimaryVersion {
			// Another member may have become the group's primary while this
			// one was soft-deleted. One primary per group, always, by the
			// same rule as a trash restore (versionprimary.Incumbent: an
			// explicit-true member, else the one nil-flag member every
			// visibility path reads as primary). Read and written under the
			// group's hand-off lock, taken above.
			incumbent, ierr := versionprimary.IncumbentExcept(ms.db, gid, a.BookID)
			if ierr != nil {
				unlockGroup()
				return res, fmt.Errorf("read version group %s of absorbed book %s: %w", gid, a.BookID, ierr)
			}
			versionprimary.YieldToIncumbent(b, incumbent)
			if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"book %s was primary of version group %s, but %s is primary now; restored as a non-primary member",
					a.BookID, gid, incumbent))
			}
		}
		// UpdateBook on purpose: this is the journal's whole-row restore of the
		// absorbed shell, not a column write, so it is not routed through ModifyBook.
		_, err = ms.db.UpdateBook(a.BookID, b)
		unlockGroup()
		if err != nil {
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

	// 2b. Library state, now that each absorbed book owns its files again:
	//     the shared restore rule (database.RestoreLibraryStateFromTrash). The
	//     combine never relabels the state, so this keeps it, except that
	//     "organized" needs the files back inside the library root and
	//     outside the iTunes library, and a row a later trash path labelled
	//     "deleted" gets back what that path recorded.
	env := TrashRestoreEnv()
	for _, a := range j.Absorbed {
		files, err := ms.db.GetBookFiles(a.BookID)
		if err != nil {
			return res, fmt.Errorf("read files of absorbed book %s: %w", a.BookID, err)
		}
		written, err := ms.db.ModifyBook(a.BookID, func(b *database.Book) error {
			database.RestoreLibraryStateFromTrash(b, files, env)
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("restore library state of absorbed book %s: %w", a.BookID, err)
		}
		if written == nil {
			return res, fmt.Errorf("restore library state of absorbed book %s: not found", a.BookID)
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
		warns, err := RestoreFollowedProgress(ms.db, j.SurvivorID, a.BookID, a.SyncRedirected, a.Progress)
		res.Warnings = append(res.Warnings, warns...)
		if err != nil {
			return res, err
		}
	}

	// 4. Survivor metadata override.
	if o := j.Override; o != nil {
		// ModifyBook: only the overridden columns are put back, on the row as
		// it is under the write lock; the survivor's other columns keep what
		// any later writer committed.
		fresh, err := ms.db.ModifyBook(j.SurvivorID, func(b *database.Book) error {
			changed := false
			if o.Applied.Title != "" {
				b.Title = o.TitleBefore
				changed = true
			}
			if o.Applied.Narrator != "" {
				b.Narrator = o.NarratorBefore
				changed = true
			}
			if o.AuthorIDAfter != nil {
				b.AuthorID = o.AuthorIDBefore
				changed = true
			}
			if !changed {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("restore survivor metadata: %w", err)
		}
		if fresh == nil {
			return res, fmt.Errorf("reload survivor %s: not found", j.SurvivorID)
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

	// 4b. Fill-empty metadata. Each filled field goes back to empty only if it
	//     still holds the value the merge wrote; a field someone set since is
	//     theirs and is left, with a warning.
	if f := j.FilledEmpty; !f.Empty() {
		// Remove only the author rows the merge added; any co-author added
		// since is the user's and stays (becoming the primary author link).
		var remaining []database.BookAuthor
		if f.AuthorID != nil && len(f.Authors) > 0 {
			cur, err := ms.db.GetBookAuthors(j.SurvivorID)
			if err != nil {
				return res, fmt.Errorf("read survivor authors: %w", err)
			}
			added := map[string]bool{}
			for _, a := range f.Authors {
				added[fmt.Sprintf("%d|%s", a.AuthorID, a.Role)] = true
			}
			for _, a := range cur {
				if !added[fmt.Sprintf("%d|%s", a.AuthorID, a.Role)] {
					remaining = append(remaining, a)
				}
			}
		}
		authorsCleared := false
		_, err := ms.db.ModifyBook(j.SurvivorID, func(b *database.Book) error {
			changed := false
			if f.ASIN != nil {
				if b.ASIN != nil && *b.ASIN == *f.ASIN {
					b.ASIN, changed = nil, true
				} else {
					res.Warnings = append(res.Warnings, "survivor ASIN changed after the merge; left as is")
				}
			}
			if f.Narrator != nil {
				if b.Narrator != nil && *b.Narrator == *f.Narrator {
					b.Narrator, changed = nil, true
				} else {
					res.Warnings = append(res.Warnings, "survivor narrator changed after the merge; left as is")
				}
			}
			if f.SeriesID != nil {
				if b.SeriesID != nil && *b.SeriesID == *f.SeriesID {
					b.SeriesID, changed = nil, true
					if f.SeriesSequence != nil && b.SeriesSequence != nil && *b.SeriesSequence == *f.SeriesSequence {
						b.SeriesSequence = nil
					}
				} else {
					res.Warnings = append(res.Warnings, "survivor series changed after the merge; left as is")
				}
			}
			if f.AuthorID != nil {
				if b.AuthorID != nil && *b.AuthorID == *f.AuthorID {
					b.AuthorID, changed, authorsCleared = nil, true, true
					if len(remaining) > 0 {
						id := remaining[0].AuthorID
						b.AuthorID = &id
					}
				} else {
					res.Warnings = append(res.Warnings, "survivor author changed after the merge; left as is")
				}
			}
			if !changed {
				return database.ErrSkipBookWrite
			}
			return nil
		})
		if err != nil {
			return res, fmt.Errorf("restore survivor fill-empty metadata: %w", err)
		}
		if authorsCleared && len(f.Authors) > 0 {
			for i := range remaining {
				remaining[i].Position = i
			}
			if err := ms.db.SetBookAuthors(j.SurvivorID, remaining); err != nil {
				return res, fmt.Errorf("restore survivor authors (fill-empty): %w", err)
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
