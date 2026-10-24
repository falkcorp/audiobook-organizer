// file: internal/merge/combine_journal.go
// version: 1.33.0
// guid: 4e8b1c27-93d5-4f0a-a6e2-7c51d9b03f18
// last-edited: 2026-10-26

package merge

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/logger"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metastate"
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
// database.Store itself), so a Store-embedding decorator forwards it
// without any capability assertion:
//
//	merge:combine-journal:<ULID> -> CombineJournal JSON
//
// ULIDs sort chronologically, so a prefix scan lists oldest-first.

const combineJournalPrefix = "merge:combine-journal:"

// Combine journal statuses.
const (
	// CombineJournalPending is written BEFORE the combine mutates anything. A
	// journal left in this state names a merge that failed or crashed part
	// way, or one whose final journal write failed. A StepJournaled journal
	// (every writer today) names each step before or as it runs, so
	// UndoCombine replays it with retry tolerance: a step that never ran
	// leaves its row where it already is. A Pending journal that is not
	// StepJournaled (CombineBooks before it journaled each step) does not
	// record what ran and is refused.
	CombineJournalPending = "pending"
	// CombineJournalApplied marks a merge that completed.
	CombineJournalApplied = "applied"
	// CombineJournalUndone marks a combine that UndoCombine reversed.
	CombineJournalUndone = "undone"
	// CombineJournalUndoFailed marks an undo that passed every precondition
	// but hit a store error part way through. LastError says where. It is NOT
	// terminal: UndoCombine accepts it and re-runs the undo, treating every item
	// the failed attempt already restored as done (see undoPreconditions and
	// applyUndo, whose steps are each safe to re-run). A journal left here by
	// an undo that finished and could not record itself carries an applied
	// record (combineUndoAppliedKey); its re-run only records it undone.
	CombineJournalUndoFailed = "undo_failed"
)

// SurvivorNumbering values (CombineJournal.SurvivorNumbering).
const (
	// SurvivorNumberingUntouched: the merge stopped before any renumbering
	// of the survivor's own rows could run. Undo leaves their numbers alone:
	// a number there now was set by someone else, after.
	SurvivorNumberingUntouched = "untouched"
	// SurvivorNumberingMayChange: the merge's body completed, so a
	// play-order stamp may have renumbered the survivor's own rows.
	SurvivorNumberingMayChange = "may_change"
)

// survivorMayBeRenumbered reports whether undo restores the numbers of the
// survivor's own rows.
//
// Anything but SurvivorNumberingUntouched reads as "may change", including ""
// on a journal written before the field existed: the old rule, which always
// restored them. So the undo of such an older combine (every journal the
// backfill-merge-undo-pointers job gives a pointer to is one) also puts the
// survivor's own rows back to their pre-combine disc/track numbers, and a
// renumbering of them made after the combine is reverted with it. Reading ""
// as untouched instead would leave a play-order stamp the combine's own
// caller made (regroup's multidisc apply) in place after the undo, with no
// record to tell the two apart.
func (j *CombineJournal) survivorMayBeRenumbered() bool {
	return j.SurvivorNumbering != SurvivorNumberingUntouched
}

// CombineJournal is the undo record for one CombineBooks call.
type CombineJournal struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	SurvivorID string    `json:"survivor_id"`
	// Origin names the operation that wrote the journal when it is not
	// CombineBooks (empty): "split_book_merge" for dedup.MergeSplitBookClusterWithOptions.
	Origin string `json:"origin,omitempty"`

	// Absorbed holds one entry per book folded into the survivor.
	Absorbed []CombineAbsorbed `json:"absorbed"`

	// SurvivorFiles are file rows planOwnFile attached to the survivor for
	// its OWN FilePath (virtual-segment materialization). A created row stays
	// on the survivor on undo (it names the survivor's own audio); a row
	// reattached from another owner is moved back. No row is ever deleted.
	SurvivorFiles []CombineFileMove `json:"survivor_files,omitempty"`

	// SurvivorOwnFiles are the rows the survivor already owned before the
	// combine. They never move, but a play-order stamp can renumber them
	// once the combine's body is done (the regroup multidisc apply right
	// after CombineBooks returns, the split merge's step 2b), so undo
	// restores their DiscBefore/TrackBefore too -- only when
	// SurvivorNumbering says such a stamp could have run -- and refuses if
	// one was moved away.
	SurvivorOwnFiles []CombineFileMove `json:"survivor_own_files,omitempty"`
	// SurvivorNumbering records whether a renumbering of the survivor's own
	// rows can have followed this merge: SurvivorNumberingUntouched until the
	// writer persists SurvivorNumberingMayChange at the point a stamp can
	// run. A journal written before the field existed has "", read as may
	// change (the old rule).
	SurvivorNumbering string `json:"survivor_numbering,omitempty"`

	// Override is non-nil when the combine applied a metadata override to the
	// survivor; it records what the survivor held before.
	Override *CombineOverrideUndo `json:"override,omitempty"`
	// FilledEmpty records metadata a merge copied from absorbed books onto the
	// survivor where the survivor's field was EMPTY (never an overwrite).
	// Undo empties each such field again if it still holds the filled value.
	FilledEmpty *CombineFillEmpty `json:"filled_empty,omitempty"`
	// StepJournaled is set by writers that journal every step before or as
	// it runs (dedup.MergeSplitBookClusterWithOptions, and CombineBooks since
	// #4064). Only such a journal can be undone while still Pending: it names
	// everything done before a crash. A CombineBooks journal written before
	// then has it unset, and its Pending state stays non-undoable.
	StepJournaled bool `json:"step_journaled,omitempty"`

	// ITunesRemovals is always empty for a combine and is recorded so the
	// journal states that explicitly: CombineBooks queues NO iTunes-library
	// removals (only MergeBooks does). The absorbed books' PIDs are REASSIGNED
	// to the survivor, which now owns that audio; undo moves them back via
	// Absorbed[].ExternalIDs. Removal is deferred to a purge (audiobooks.RunPurge,
	// the nightly pass or the manual one), which enqueues removes for whatever PIDs a purged book still holds.
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
	// owns, and a purge with delete_files (audiobooks.RunPurge) os.Remove()s a
	// purged single-file book's FilePath. Leaving it set would let the purge
	// delete the survivor's audio from disk 30 days later.
	FilePath         string  `json:"file_path"`
	IsPrimaryVersion *bool   `json:"is_primary_version,omitempty"`
	VersionGroupID   *string `json:"version_group_id,omitempty"`
	// MarkedForDeletionAt is the soft-delete stamp the merge writes. It is
	// minted and journaled BEFORE the soft-delete, which writes exactly it
	// (re-journaled if the store's read-back differs), so a crash in between
	// cannot leave a retired book the journal does not name. Undo restores
	// only a row carrying it (retiredByJournal); an applied journal refuses
	// when the row no longer does (restored, re-deleted or purged since).
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
	// book materialized as a BookFile). Undo repoints it to FromBookID, the
	// book whose audio it names, like any moved row; it never deletes it (a
	// row whose FromBookID is the survivor stays on the survivor).
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

// dataUser is the account whose state and positions this record's undo reads
// and writes.
func (p CombineUserProgress) dataUser() string {
	if p.owner != "" {
		return p.owner
	}
	return p.UserID
}

// CombineUserProgress is one user's progress on both sides of one absorbed
// book's FollowMerge.
type CombineUserProgress struct {
	UserID string `json:"user_id"`
	// owner is the account that holds UserID's data now: UserID itself, or the
	// account an admin merged it into (database.ResolveMergedUser), set by
	// RestoreFollowedProgress. Every read and write of state goes to owner;
	// the survivor put-back's marker key stays keyed by UserID, as it was
	// written.
	owner               string
	AbsorbedState       *database.UserBookState `json:"absorbed_state,omitempty"`
	AbsorbedPositions   []database.UserPosition `json:"absorbed_positions,omitempty"`
	SurvivorStateBefore *database.UserBookState `json:"survivor_state_before,omitempty"`
	SurvivorPosBefore   []database.UserPosition `json:"survivor_positions_before,omitempty"`
	SurvivorStateAfter  *database.UserBookState `json:"survivor_state_after,omitempty"`
	SurvivorPosAfter    []database.UserPosition `json:"survivor_positions_after,omitempty"`
	// FollowID names this one follow of this user: a fresh ULID minted
	// with the after-snapshot (buildProgressJournal). The survivor
	// put-back's marker is keyed by it (survivorReconcileKey), so a later
	// follow of the same pair never reads an earlier one's marker. Empty on
	// a journal written before round 5 of the #4064 review, whose markers
	// keep the old timestamp key (followStamp).
	FollowID string `json:"follow_id,omitempty"`
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
	Applied        CombineOverride       `json:"applied"`
	TitleBefore    string                `json:"title_before"`
	NarratorBefore *string               `json:"narrator_before,omitempty"`
	AuthorIDBefore *int                  `json:"author_id_before,omitempty"`
	AuthorsBefore  []database.BookAuthor `json:"authors_before,omitempty"`
	AuthorIDAfter  *int                  `json:"author_id_after,omitempty"`
	// AuthorCreated is true when the combine itself created the author row
	// AuthorIDAfter names (database.MintAuthor reported it minted the row). It
	// is written in the same journal write as AuthorIDAfter, before the author
	// is linked. Nothing deletes that row on its account: the undo only warns
	// when no book credits it any more (createdAuthorNote), and
	// maintenance.purge-empty-authors can remove it. False for an author that
	// already existed, and absent from a journal written before the field: no
	// warning for either.
	AuthorCreated bool                                    `json:"author_created,omitempty"`
	FieldStates   map[string]*database.MetadataFieldState `json:"field_states_before,omitempty"`
	// FieldStatesAfter is each lock row as the override's lock write left it
	// (read back right after database.RecordUserOverrides), by field. The
	// undo puts a lock row back to FieldStates only while it still holds
	// this (restoreOverrideLocks), the way the fill-empty undo compares
	// values: a lock someone set since, including one set after an undo
	// whose final journal write failed and is re-run, stays. A field with no
	// entry (a journal from before the field, or a read-back that failed)
	// is compared with what the lock write writes: the override's value,
	// locked, as a person's lock (overrideLockAsWritten).
	FieldStatesAfter map[string]*database.MetadataFieldState `json:"field_states_after,omitempty"`
}

// overrideLockAsWritten is the lock row the override's lock write leaves for
// field, for a journal that does not record it (FieldStatesAfter): the
// override's value, locked, with no LockSource (database.RecordUserOverrides
// writes a person's lock). nil when the override did not set field.
func (o *CombineOverrideUndo) overrideLockAsWritten(field string) *database.MetadataFieldState {
	if st := o.FieldStatesAfter[field]; st != nil {
		return st
	}
	var value string
	switch field {
	case database.FieldKeyTitle:
		value = o.Applied.Title
	case database.FieldKeyNarrator:
		value = o.Applied.Narrator
	case database.FieldKeyAuthorName:
		value = o.Applied.Author
	}
	if value == "" {
		return nil
	}
	encoded, err := metastate.Encode(value)
	if err != nil {
		return nil
	}
	return &database.MetadataFieldState{Field: field, OverrideValue: encoded, OverrideLocked: true}
}

// sameLock reports whether two lock rows hold the same lock: the override
// value, the locked flag and who set it. A missing row is no lock.
// FetchedValue and UpdatedAt are not compared: a metadata fetch since the
// combine rewrites both without touching the lock, and must not make the
// undo keep a lock it would otherwise put back.
func sameLock(a, b *database.MetadataFieldState) bool {
	var za, zb database.MetadataFieldState
	if a != nil {
		za = *a
	}
	if b != nil {
		zb = *b
	}
	return strPtrEqual(za.OverrideValue, zb.OverrideValue) && za.OverrideLocked == zb.OverrideLocked && za.LockSource == zb.LockSource
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
// carries it (RawKVStore), so a Store-embedding decorator forwards it.
type CombineJournalWriter interface {
	SetRaw(key string, value []byte) error
}

// NewCombineJournal returns an empty Pending journal for survivorID with a
// fresh chronologically-sortable id. Callers outside this package that fold
// books into a survivor (dedup.MergeSplitBookClusterWithOptions) record their work in
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

// combineJournalGen counts combine-journal writes in this process. It exists
// for readers that cache a view of the journals (UndoShellIndex) and must know
// when the cache may be missing a journal written after it was built.
var combineJournalGen atomic.Uint64

// CombineJournalGeneration is how many combine-journal writes this process has
// made. It is bumped AFTER the write lands (and after a failed write too: a
// failed SetRaw may still have persisted), so a reader that reads it, then
// reads the journals, sees a changed value if any write finished after its
// read. Every journal writer (merge.Service combine, dedup split-book merge)
// holds the merge lock across the write, so a reader holding that lock reads
// an exact answer; without it, a write still in flight can go unseen.
func CombineJournalGeneration() uint64 { return combineJournalGen.Load() }

// WriteCombineJournal persists j under its key.
func WriteCombineJournal(db CombineJournalWriter, j *CombineJournal) error {
	defer combineJournalGen.Add(1)
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
	return readCombineJournal(ms.db, id)
}

// readCombineJournal is GetCombineJournal over any raw-key reader.
func readCombineJournal(kv journalKVReader, id string) (*CombineJournal, error) {
	if id == "" || strings.ContainsAny(id, ":/") {
		return nil, ErrCombineJournalNotFound
	}
	data, err := kv.GetRaw(combineJournalKey(id))
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
	return listCombineJournals(ms.db, limit)
}

// listCombineJournals is ListCombineJournals over any raw-key reader. It is
// the LENIENT lister: a row it cannot decode is skipped with a warning, which
// is right for a listing a person reads and for the pointer backfill. A caller
// that must not act on "cannot tell" (the purge's undo hold) uses
// scanCombineJournals and refuses on the unreadable rows.
func listCombineJournals(kv journalKVReader, limit int) ([]CombineJournal, error) {
	out, unreadable, err := scanCombineJournals(kv)
	if err != nil {
		return nil, err
	}
	for _, u := range unreadable {
		mlog.Warn("combine journal: skipping undecodable row key=%s err=%s", logger.SanitizeLogValue(u.Key), logger.SanitizeLogValue(u.Err))
	}
	sort.Slice(out, func(i, k int) bool { return out[i].ID > out[k].ID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// UnreadableCombineJournal is a combine-journal row whose value cannot be
// decoded, so nothing can be said about which shells its undo needs.
type UnreadableCombineJournal struct {
	// Key is the row's full store key (merge:combine-journal:<id>).
	Key string `json:"key"`
	// Err is the decode error.
	Err string `json:"error"`
}

// scanCombineJournals reads every combine-journal row once and returns the
// journals it could decode, unordered, and the rows it could not. An error is
// the scan itself failing.
func scanCombineJournals(kv journalKVReader) ([]CombineJournal, []UnreadableCombineJournal, error) {
	rows, err := kv.ScanPrefix(combineJournalPrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("list combine journals: %w", err)
	}
	out := make([]CombineJournal, 0, len(rows))
	var unreadable []UnreadableCombineJournal
	for _, r := range rows {
		var j CombineJournal
		if err := json.Unmarshal(r.Value, &j); err != nil {
			unreadable = append(unreadable, UnreadableCombineJournal{Key: r.Key, Err: err.Error()})
			continue
		}
		out = append(out, j)
	}
	return out, unreadable, nil
}

// CombineJournalCheckOp is the operation that lists unreadable combine-journal
// rows. UnreadableCombineJournalsError names it so the person who sees the
// purge refuse knows where to look.
const CombineJournalCheckOp = "maintenance.combine-journal-check"

// CombineJournalCheck is the outcome of CheckCombineJournals.
type CombineJournalCheck struct {
	// Rows is how many combine-journal rows exist.
	Rows int `json:"rows"`
	// Unreadable are the rows that cannot be decoded, by key.
	Unreadable []UnreadableCombineJournal `json:"unreadable"`
}

// CheckCombineJournals reads every combine-journal row and reports the ones
// that cannot be decoded. It writes nothing.
func CheckCombineJournals(kv journalKVReader) (CombineJournalCheck, error) {
	ok, unreadable, err := scanCombineJournals(kv)
	if err != nil {
		return CombineJournalCheck{}, err
	}
	sort.Slice(unreadable, func(i, k int) bool { return unreadable[i].Key < unreadable[k].Key })
	return CombineJournalCheck{Rows: len(ok) + len(unreadable), Unreadable: unreadable}, nil
}

// UnreadableCombineJournalsError is why the purge refuses while a combine
// journal row cannot be decoded: such a row may be an undo that still needs a
// shell the purge would delete, and it cannot say which. It names the keys and
// the operation that lists them. Repair is manual: fix or remove the row, then
// purge again.
type UnreadableCombineJournalsError struct {
	Rows []UnreadableCombineJournal
}

func (e *UnreadableCombineJournalsError) Error() string {
	const show = 5
	keys := make([]string, 0, show)
	for i, r := range e.Rows {
		if i == show {
			break
		}
		keys = append(keys, fmt.Sprintf("%q", r.Key))
	}
	more := ""
	if n := len(e.Rows) - len(keys); n > 0 {
		more = fmt.Sprintf(" and %d more", n)
	}
	return fmt.Sprintf("%d combine journal row(s) cannot be decoded (%s%s), so the purge cannot tell which trashed books a combine's undo still needs; "+
		"every purge is refused until they are repaired. List every unreadable row with the %s operation, fix or remove them, then purge again",
		len(e.Rows), strings.Join(keys, ", "), more, CombineJournalCheckOp)
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

// sameUserState reports whether b already holds everything a write of a
// would set: every field combineOnRestore decides (UpdatedAt, which the store
// stamps on each write, and the ids are not compared). A nil b holds nothing.
func sameUserState(a, b *database.UserBookState) bool {
	if b == nil {
		return false
	}
	return a.Status == b.Status && a.StatusManual == b.StatusManual &&
		a.ProgressPct == b.ProgressPct && timePtrEqual(a.FinishedAt, b.FinishedAt) &&
		a.LastSegmentID == b.LastSegmentID && a.TotalListenedSeconds == b.TotalListenedSeconds &&
		a.LastActivityAt.Equal(b.LastActivityAt) && a.HideFromContinueListening == b.HideFromContinueListening &&
		timePtrEqual(a.ProgressResetAt, b.ProgressResetAt) && slices.Equal(a.ProgressResetPositions, b.ProgressResetPositions)
}

// writeProgress replaces one user's progress on bookID with the snapshot.
// A nil state is written as a neutralized row, the same shape
// mergeUserProgressFor uses to drain a loser (there is no delete primitive).
// The position rows are replaced in one atomic write when the store can
// (replacePositions), so a crash or failed write never leaves the book with
// its old rows cleared and the snapshot's only partly written.
func writeProgress(db userPositionStore, userID, bookID string, st *database.UserBookState, pos []database.UserPosition, current *database.UserBookState) error {
	// The rows keep their original UpdatedAt (the ABS lastUpdate): restoring
	// with SetUserPosition stamped the undo time, so an undone book looked
	// freshly listened and won the next merge's newest-wins rule.
	if err := replacePositions(db, userID, bookID, pos); err != nil {
		return err
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
// with database.AsCapability, which looks through any store decorator
// (the same resolution AsSyncIdentityStore uses for RecordSyncMerge itself).
type syncMergeClearer interface {
	ClearSyncMerge(loserBookID, winnerBookID string) error
}

// combineUndoInProgress is the LastError an undo writes before its first
// write. A journal that still carries it after the undo returned is one whose
// undo was cut short (a crash, a kill); re-running the undo finishes it.
const combineUndoInProgress = "undo started and did not finish"

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
	// An undo that finished but could not record itself left its applied
	// record (combineUndoAppliedKey). Checked before the preconditions:
	// what the user changed after that undo would make them refuse, and the
	// journal would stay undo_failed for good. Only the status write is
	// retried; nothing is replayed.
	if j.Status == CombineJournalUndoFailed || j.Status == CombineJournalPending {
		applied, err := readCombineUndoApplied(ms.db, j.ID)
		if err != nil {
			return nil, fmt.Errorf("undo combine %s: %w", j.ID, err)
		}
		if applied != nil {
			return ms.recordAppliedUndo(j, applied)
		}
	}
	if reasons := ms.undoPreconditions(j); len(reasons) > 0 {
		return nil, &CombineUndoRefusedError{JournalID: j.ID, Reasons: reasons}
	}
	// Mark an Applied journal as a retry BEFORE the first write. undo_failed
	// was written only on an error return, so a crash (or a kill) part-way
	// through applyUndo left the journal Applied over a half-undone library,
	// and every re-run then refused on the rows the dead attempt had already
	// put back. As undo_failed, a re-run gets the retry tolerance of
	// undoPreconditions: each row may be in its post-combine OR its recorded
	// pre-combine state. A Pending (step-journaled) journal already has that
	// tolerance and keeps its status. A journal that cannot be marked is not
	// undone at all: nothing has been written yet.
	if j.Status == CombineJournalApplied {
		j.Status = CombineJournalUndoFailed
		j.LastError = combineUndoInProgress
		if err := ms.putCombineJournal(j); err != nil {
			return nil, fmt.Errorf("undo combine %s: journal not marked as an undo in progress, nothing undone: %w", j.ID, err)
		}
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
	// The undo is done. Say so in its own raw key before the journal's
	// status write: if that write fails, the journal still reads
	// undo_failed ("undo started and did not finish", written before the
	// first write), and a re-run must then only retry the status write
	// (recordAppliedUndo), never replay applyUndo over what the user has
	// done since -- a title, lock or author link re-picked to the combine's
	// own value reads, to a replay, as the combine's and is taken away
	// again. If this write fails too, the status write below is still made;
	// only when both fail does a re-run replay, and every step of applyUndo
	// is then safe to re-run against rows already put back (a row already
	// back is not written, the survivor's put-back recognises its marker,
	// a lock row is put back only while it still holds the combine's lock),
	// though a value re-picked to the combine's own is not told from it.
	applied := combineUndoApplied{UndoneAt: now, Warnings: res.Warnings}
	if err := writeCombineUndoApplied(ms.db, j.ID, applied); err != nil {
		mlog.Error("combine undo: completed but its applied record not written journal=%s err=%s", j.ID, logger.SanitizeLogValue(fmt.Sprint(err)))
	}
	if err := ms.markCombineUndone(j, applied); err != nil {
		// The undo itself is done. The reconcile markers and the applied
		// record are kept: a re-run finds the record and only retries this
		// write.
		mlog.Error("combine undo: completed but journal status not updated journal=%s err=%s", j.ID, logger.SanitizeLogValue(fmt.Sprint(err)))
	}
	mlog.Info("combine undone journal=%s survivor=%s restored=%d files_moved=%d warnings=%d",
		j.ID, logger.SanitizeLogValue(j.SurvivorID), len(res.RestoredBooks), res.FilesMoved, len(res.Warnings))
	return res, nil
}

// combineUndoAppliedPrefix keys the record an undo writes once applyUndo
// has succeeded, before the journal's status write:
// merge:combine-undo-applied:<journal id> -> combineUndoApplied JSON. It is
// deleted once the journal reads undone (markCombineUndone); one left behind
// next to an undone journal is never read (UndoCombine reads it only for an
// undo_failed or pending journal).
const combineUndoAppliedPrefix = "merge:combine-undo-applied:"

func combineUndoAppliedKey(journalID string) string { return combineUndoAppliedPrefix + journalID }

// combineUndoApplied is what the journal's status write records: when the
// undo finished and what it left as it is.
type combineUndoApplied struct {
	UndoneAt time.Time `json:"undone_at"`
	Warnings []string  `json:"warnings,omitempty"`
}

func writeCombineUndoApplied(db CombineJournalWriter, journalID string, a combineUndoApplied) error {
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("encode undo applied record: %w", err)
	}
	return db.SetRaw(combineUndoAppliedKey(journalID), data)
}

// readCombineUndoApplied returns the journal's applied record, nil when there
// is none. A record that cannot be read is an error, not "none": reading it
// as none would replay an undo that finished.
func readCombineUndoApplied(kv journalKVReader, journalID string) (*combineUndoApplied, error) {
	data, err := kv.GetRaw(combineUndoAppliedKey(journalID))
	if err != nil {
		return nil, fmt.Errorf("read undo applied record: %w", err)
	}
	if data == nil {
		return nil, nil
	}
	var a combineUndoApplied
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("decode undo applied record: %w", err)
	}
	return &a, nil
}

// markCombineUndone records j as undone with what the undo left as it is,
// then drops what only a re-run of the undo could need: the survivor
// put-backs' reconcile markers and the applied record. Both drops are best
// effort; a marker or record left behind is never read again.
func (ms *Service) markCombineUndone(j *CombineJournal, a combineUndoApplied) error {
	undoneAt := a.UndoneAt
	j.Status = CombineJournalUndone
	j.UndoneAt = &undoneAt
	j.LastError = ""
	j.Warnings = append(j.Warnings, a.Warnings...)
	if err := ms.putCombineJournal(j); err != nil {
		return err
	}
	for _, ab := range j.Absorbed {
		dropSurvivorReconcileMarkers(ms.db, j.SurvivorID, ab.BookID, ab.Progress)
	}
	if err := ms.db.DeleteRaw(combineUndoAppliedKey(j.ID)); err != nil {
		mlog.Warn("combine undo: applied record of journal=%s not dropped: %s", j.ID, logger.SanitizeLogValue(err.Error()))
	}
	return nil
}

// recordAppliedUndo finishes a re-run of an undo that completed but could not
// record itself: it writes the journal's status and nothing else. The result
// reports nothing restored and no warnings, since this run changed nothing;
// what the undo itself left as it is goes into the journal's warnings.
func (ms *Service) recordAppliedUndo(j *CombineJournal, a *combineUndoApplied) (*CombineUndoResult, error) {
	if err := ms.markCombineUndone(j, *a); err != nil {
		return nil, fmt.Errorf("undo combine %s: the undo had completed, and recording it failed again: %w", j.ID, err)
	}
	mlog.Info("combine undo recorded journal=%s survivor=%s: the undo had completed earlier and could not record itself; nothing replayed",
		j.ID, logger.SanitizeLogValue(j.SurvivorID))
	return &CombineUndoResult{JournalID: j.ID, SurvivorID: j.SurvivorID}, nil
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
	if j.Status == CombineJournalPending && !j.StepJournaled {
		return []string{"the combine never completed and its journal does not record which steps ran; no undo recorded"}
	}
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

	// listOwner is the absorbed book whose Files list fm is in, "" for the
	// survivor's lists (fileAlreadyUndone needs it for a row whose outside owner
	// was purged).
	checkFileL := func(fm CombineFileMove, lenient bool, listOwner string) {
		f, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
		if err == nil && f == nil && lenient && ms.fileAlreadyUndone(j, fm, listOwner) {
			return
		}
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Sprintf("load file %s: %v", fm.FileID, err))
		case f == nil:
			reasons = append(reasons, fmt.Sprintf("file %s (%s) is no longer on survivor %s (moved, deleted, or re-merged)", fm.FileID, fm.FilePath, j.SurvivorID))
		case f.Retired:
			// A row retired into another book's version since is that
			// version's, not the combine's: moving it back would rewrite a
			// version the owner keeps.
			reasons = append(reasons, fmt.Sprintf("file %s was retired into version %q since the combine; its retire is reversed first, never overwritten", fm.FileID, f.VersionID))
		case f.FilePath != fm.FilePath:
			reasons = append(reasons, fmt.Sprintf("file %s moved on disk since the combine (%s -> %s)", fm.FileID, fm.FilePath, f.FilePath))
		}
	}
	checkFile := func(fm CombineFileMove) { checkFileL(fm, retry, "") }
	for _, fm := range j.SurvivorFiles {
		checkFile(fm)
	}
	// A reattached row came from a book OUTSIDE the combine (planVirtualFile
	// moved it off its previous owner). Undo moves it back there. When that
	// book has since been purged the row has nowhere to go, and the undo does
	// not refuse: it gives the row to the absorbed book whose file it is, or
	// leaves it on the survivor when it is the survivor's own file, with a
	// warning (applyUndo's moveBack). Only a book that cannot be read refuses,
	// because then it is not known where the row belongs.
	inCombine := map[string]bool{j.SurvivorID: true}
	for _, a := range j.Absorbed {
		inCombine[a.BookID] = true
	}
	checkOwner := func(fm CombineFileMove) {
		if fm.Created || inCombine[fm.FromBookID] {
			return
		}
		if _, err := ms.db.GetBookByID(fm.FromBookID); err != nil {
			reasons = append(reasons, fmt.Sprintf("load book %s that file %s came from: %v", fm.FromBookID, fm.FileID, err))
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
		if files, err := ms.filesIncludingRetired(a.BookID); err != nil {
			reasons = append(reasons, fmt.Sprintf("load files of absorbed book %s: %v", a.BookID, err))
		} else {
			// On a retry the failed attempt may already have moved some of
			// this book's own rows back (a row the combine created included:
			// it is repointed, never deleted); those are expected. Any other
			// row is a file the book gained since, and still refuses.
			ownRows := map[string]bool{}
			if lenient {
				for _, fm := range a.Files {
					ownRows[fm.FileID] = true
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
			checkFileL(fm, lenient, a.BookID)
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
	return append(reasons, ms.retireReasons(j)...)
}

// filesIncludingRetired lists every book_file row keyed under bookID, the rows
// retired into it as well as its live ones, when the store can say
// (database.RetiredBookFileReader); otherwise the live rows, which are then all
// there are. The undo asks it wherever it decides whether a book "has gained"
// files or what a restored book owns: GetBookFiles alone leaves a retired
// version out.
func (ms *Service) filesIncludingRetired(bookID string) ([]database.BookFile, error) {
	if r, ok := database.AsCapability[database.RetiredBookFileReader](ms.db); ok {
		return r.GetBookFilesIncludingRetired(bookID)
	}
	return ms.db.GetBookFiles(bookID)
}

// combineRetireLinks returns the retire links of the combine's absorbed
// shells (ListCombineRetireLinks), in key order. A link is only a pointer:
// whether the retire it names still has anything to reverse is the
// RetireUndoer's to say.
func (ms *Service) combineRetireLinks(j *CombineJournal) ([]CombineRetireLink, error) {
	links, err := ListCombineRetireLinks(ms.db, j.ID)
	if err != nil {
		return nil, err
	}
	absorbed := make(map[string]bool, len(j.Absorbed))
	for _, a := range j.Absorbed {
		absorbed[a.BookID] = true
	}
	out := links[:0]
	for _, l := range links {
		if absorbed[l.ShellID] {
			out = append(out, l)
		}
	}
	return out, nil
}

// retireReasons is the read-only half of reversing the retires of the
// combine's absorbed shells: one reason per retire that cannot be reversed
// right now. A link with no live retire rows under it is dead and is not
// mentioned. With no undoer wired a link cannot be told live from dead, so it
// refuses (fail closed): the undo must never restore a shell a retire still
// holds the rows of.
func (ms *Service) retireReasons(j *CombineJournal) []string {
	links, err := ms.combineRetireLinks(j)
	if err != nil {
		return []string{fmt.Sprintf("read the retires of the absorbed books: %v", err)}
	}
	var reasons []string
	for _, l := range links {
		if ms.retireUndo == nil {
			reasons = append(reasons, fmt.Sprintf("absorbed book %s was retired into a version by op %s and this service cannot reverse a retire", l.ShellID, l.OpID))
			continue
		}
		live, why, err := ms.retireUndo.CheckRetireRevert(l.OpID, l.ShellID)
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Sprintf("check the retire of absorbed book %s (op %s): %v", l.ShellID, l.OpID, err))
		case live && len(why) > 0:
			reasons = append(reasons, fmt.Sprintf("retire op %s of absorbed book %s cannot be reversed: %s", l.OpID, l.ShellID, strings.Join(why, "; ")))
		}
	}
	return reasons
}

// fileAlreadyUndone reports whether a file row that is no longer on the
// survivor is exactly where a completed undo of fm puts it: back on its
// recorded owner at its recorded path. A row the combine created is also
// accepted when it is gone altogether: a step-journaled combine names a row
// before creating it, so a combine stopped in between leaves a record of a
// row that never came to exist (and an undo from before created rows were
// repointed deleted them). When the recorded owner is an outside book that has
// been purged, a completed undo put the row on the absorbed book whose list
// records it (listOwner, "" for a survivor list), so that is accepted too. Only
// consulted on a retry or a replay.
func (ms *Service) fileAlreadyUndone(j *CombineJournal, fm CombineFileMove, listOwner string) bool {
	if fm.FromBookID == j.SurvivorID {
		// Not on the survivor (the caller checked): only a created row that
		// was never written can be missing from its own owner.
		return fm.Created
	}
	f, err := ms.db.GetBookFileByID(fm.FromBookID, fm.FileID)
	if err != nil {
		return false
	}
	if f == nil {
		if fm.Created {
			return true
		}
		if listOwner == "" || listOwner == fm.FromBookID {
			return false
		}
		// Not on its recorded owner. If that book is gone, the undo's moveBack
		// gave the row to listOwner instead.
		if owner, err := ms.db.GetBookByID(fm.FromBookID); err != nil || owner != nil {
			return false
		}
		g, err := ms.db.GetBookFileByID(listOwner, fm.FileID)
		return err == nil && g != nil && g.FilePath == fm.FilePath
	}
	// A row retired since is not "back where the undo puts it".
	return !f.Retired && f.FilePath == fm.FilePath
}

// overrideAuthorsToRestore returns the author links the undo of an author
// override writes back: the links the override replaced (AuthorsBefore), then
// every link added to the survivor since the combine, each with a warning.
// The override left the survivor with exactly one link, (AuthorIDAfter,
// overrideAuthorRole), so every other current link the survivor did not have
// before is someone's later edit: a co-author added by hand or by a fetch, or
// the same author credited in another role (an author-narrated book's
// narrator row, a co-author row). Writing AuthorsBefore alone dropped it
// without a word; it is kept, listed after the restored authors, and the
// warning reaches the revert's Kept list. Only the exact link the override
// wrote is taken back: until the round-4 review every link to AuthorIDAfter
// was skipped whatever its role, so a later (A, narrator) row vanished on
// undo. Safe to re-run: after a first restore the same links read as "added
// since" again.
func (ms *Service) overrideAuthorsToRestore(survivorID string, o *CombineOverrideUndo) ([]database.BookAuthor, []string, error) {
	cur, err := ms.db.GetBookAuthors(survivorID)
	if err != nil {
		return nil, nil, fmt.Errorf("read survivor authors before restoring them: %w", err)
	}
	type link struct {
		author int
		role   string
	}
	restore := make([]database.BookAuthor, 0, len(o.AuthorsBefore)+len(cur))
	had := map[link]bool{}
	for _, a := range o.AuthorsBefore {
		restore = append(restore, a)
		had[link{a.AuthorID, a.Role}] = true
	}
	var warns []string
	for _, a := range cur {
		if (a.AuthorID == *o.AuthorIDAfter && a.Role == overrideAuthorRole) || had[link{a.AuthorID, a.Role}] {
			continue
		}
		had[link{a.AuthorID, a.Role}] = true
		a.BookID = survivorID
		a.Position = len(restore)
		restore = append(restore, a)
		warns = append(warns, fmt.Sprintf("author %d (%s) was linked to book %s after the combine; kept, listed after the authors the override replaced",
			a.AuthorID, a.Role, survivorID))
	}
	return restore, warns, nil
}

// createdAuthorNote is the warning about author id (name), an author a
// combine's override created (CombineOverrideUndo.AuthorCreated), once the
// survivor no longer credits it: after an undo has restored the survivor's
// links, or when the combine dropped its own author override after minting
// the row. It returns nothing while some book still credits the author.
//
// DESIGN DECISION (2026-10-18, round-4 review of #4064): the combine and its
// undo never delete that author. An earlier version deleted it here, behind a
// read-only check that no book credited it and it had no alias, and that had
// four holes: the check and the delete were not one step (a book linked in
// between lost the credit), a crash between restoring the links and the
// delete left the orphan anyway, the delete wrote no author_delete ledger
// row (no record of the name), and tombstones and series rows naming the id
// were left dangling. maintenance.purge-empty-authors
// (internal/plugins/maintenance/author_purge_empty.go) already removes an
// author no book credits when it is run, with the gates this lacked
// (references in every book state, series owners, file counts, a per-item
// re-check), and writes an author_delete ledger row naming the author before
// each delete. That row is a record, not an automatic undo
// (undo.NotRestorableLabel lists author_delete as record-only), but it keeps
// the name, which this delete did not. So the undo leaves the row and says
// so, and the crash case needs no special handling: an orphan is one more
// empty author for that op.
//
// "Credited" is database.VerifyAuthorUnlinked: any role (narrators are
// book_authors rows too) and any book state, the legacy Book.AuthorID
// included. A check that cannot be answered is a warning of its own.
func (ms *Service) createdAuthorNote(id int, name string) []string {
	err := database.VerifyAuthorUnlinked(ms.db, id)
	var msg string
	switch {
	case err == nil:
		msg = fmt.Sprintf("author %d (%q) was created by this combine and is no longer credited on any book; it is left in place, and maintenance.purge-empty-authors can remove it", id, name)
		mlog.Info("combine: %s", logger.SanitizeLogValue(msg))
	case errors.Is(err, database.ErrAuthorStillLinked):
		return nil
	default:
		msg = fmt.Sprintf("author %d (%q) was created by this combine and is left in place; whether any book still credits it could not be checked: %v", id, name, err)
		mlog.Warn("combine: %s", logger.SanitizeLogValue(msg))
	}
	return []string{msg}
}

// restoreOverrideLocks is applyUndo step 4's lock rows: each lock row the
// override wrote goes back to what it was before (o.FieldStates), but only
// while it still holds the lock the override wrote (overrideLockAsWritten).
//
//   - Already the before-lock (an earlier attempt of this undo restored it,
//     or the override's lock write never landed): nothing to write.
//   - Still the override's lock: put back. Only the lock fields (override
//     value, locked, lock source) are put back onto the row as it is now, so
//     a FetchedValue a metadata fetch wrote since stays; a row whose fetched
//     value is unchanged is written back whole, and a row that did not exist
//     before and holds nothing else is deleted, as before.
//   - Anything else is a lock someone set after the combine: kept, with a
//     warning. Until the round-4 review the rows were restored
//     unconditionally, so a re-run of an undo whose final journal write had
//     failed (the journal stays undo_failed, see UndoCombine) removed a lock
//     the user set after the undo.
//
// A journal without FieldStatesAfter is compared with what the lock write
// writes (the override's value, a person's lock). That cannot tell the
// combine's lock from a user who later locked the field to the very same
// value; both hold the value the undo is reverting away from, and the undo
// puts the field's column back too, so the lock goes with it. A lock that
// differs in any way is never clobbered.
//
// The reads and writes are one step under the book's metadata-state lock
// (database.LockMetadataState), as RecordUserOverrides writes, so a lock
// edit cannot land between the compare and the write.
func (ms *Service) restoreOverrideLocks(survivorID string, o *CombineOverrideUndo) ([]string, error) {
	if len(o.FieldStates) == 0 {
		return nil, nil
	}
	defer database.LockMetadataState(survivorID)()
	states, err := ms.db.GetMetadataFieldStates(survivorID)
	if err != nil {
		return nil, fmt.Errorf("read override locks before restoring them: %w", err)
	}
	current := map[string]*database.MetadataFieldState{}
	for i := range states {
		current[states[i].Field] = &states[i]
	}
	fields := make([]string, 0, len(o.FieldStates))
	for field := range o.FieldStates {
		fields = append(fields, field)
	}
	slices.Sort(fields)
	var warns []string
	for _, field := range fields {
		prior, cur := o.FieldStates[field], current[field]
		if sameLock(cur, prior) {
			continue
		}
		if want := o.overrideLockAsWritten(field); want == nil || !sameLock(cur, want) {
			warns = append(warns, fmt.Sprintf("the %s lock on book %s was changed after the combine; left as it is", field, survivorID))
			continue
		}
		var priorFetched *string
		if prior != nil {
			priorFetched = prior.FetchedValue
		}
		switch {
		case prior == nil && cur.FetchedValue == nil:
			if err := ms.db.DeleteMetadataFieldState(survivorID, field); err != nil {
				return warns, fmt.Errorf("remove override lock %s: %w", field, err)
			}
		case prior != nil && strPtrEqual(cur.FetchedValue, priorFetched):
			restored := *prior
			if err := ms.db.UpsertMetadataFieldState(&restored); err != nil {
				return warns, fmt.Errorf("restore override lock %s: %w", field, err)
			}
		default:
			restored := *cur
			restored.OverrideValue, restored.OverrideLocked, restored.LockSource = nil, false, ""
			if prior != nil {
				restored.OverrideValue, restored.OverrideLocked, restored.LockSource = prior.OverrideValue, prior.OverrideLocked, prior.LockSource
			}
			restored.UpdatedAt = time.Now()
			if err := ms.db.UpsertMetadataFieldState(&restored); err != nil {
				return warns, fmt.Errorf("restore override lock %s: %w", field, err)
			}
		}
	}
	return warns, nil
}

// retiredByJournal reports whether b is still the shell this combine retired:
// soft-deleted, carrying exactly the stamp the combine wrote and journaled.
// A live row (left live, never reached, or already restored by an earlier
// undo attempt) or a row re-deleted since is not.
func retiredByJournal(a CombineAbsorbed, b *database.Book) bool {
	return b != nil && b.IsSoftDeleted() && a.MarkedForDeletionAt != nil &&
		b.MarkedForDeletionAt != nil && b.MarkedForDeletionAt.Equal(*a.MarkedForDeletionAt)
}

// restoreSurvivorFileNumbers puts back the disc/track a row the survivor
// keeps carried before the combine (zero for a row the combine created), so
// a play-order stamp made after the combine is reversed with it.
func (ms *Service) restoreSurvivorFileNumbers(j *CombineJournal, fm CombineFileMove) error {
	f, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
	if err != nil || f == nil {
		return fmt.Errorf("reload survivor file %s: %v", fm.FileID, err)
	}
	if f.Retired {
		return fmt.Errorf("survivor file %s was retired into version %q since the combine; its numbers are not restored", fm.FileID, f.VersionID)
	}
	if f.DiscNumber == fm.DiscBefore && f.TrackNumber == fm.TrackBefore {
		return nil
	}
	f.DiscNumber, f.TrackNumber = fm.DiscBefore, fm.TrackBefore
	if err := ms.db.UpdateBookFile(f.ID, f); err != nil {
		return fmt.Errorf("restore disc/track of survivor file %s: %w", f.ID, err)
	}
	return nil
}

// primaryAuthorOf picks the book row's AuthorID from the author links left
// on a book, in position order: the first whose role names the book's
// author ("author", "primary", or an unlabelled older row); failing that,
// the first co-author (any other database.IsAuthorCreditRole role), which
// is still an author of the book; and nil when no link credits an author.
// A narrator, editor or translator link is never made the primary author,
// and a co-author never wins over an author: the fill-empty undo used to
// take whichever link came first, which could be any of them.
func primaryAuthorOf(links []database.BookAuthor) *int {
	var author, coAuthor *database.BookAuthor
	for i := range links {
		l := &links[i]
		if !database.IsAuthorCreditRole(l.Role) {
			continue
		}
		best := &coAuthor
		switch strings.ToLower(strings.TrimSpace(l.Role)) {
		case "author", "primary", "":
			best = &author
		}
		if *best == nil || l.Position < (*best).Position {
			*best = l
		}
	}
	if author == nil {
		author = coAuthor
	}
	if author == nil {
		return nil
	}
	id := author.AuthorID
	return &id
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
// listened to since is reconciled instead (reconcileTouchedSurvivorOnce): what
// the follow brought is taken back off it and what the user did there since
// stays, so the carried state ends up on exactly one book -- the absorbed
// one -- and its listened time is not counted on both. That is returned as a
// warning, not an error. Safe to run twice: a survivor already back at its
// before-state is left alone, and so is one this put-back (or the reconcile)
// already wrote and the user has written since. The plain put-back journals
// the reconcile's marker (survivorReconcileMarker) before its write for
// that: without it, a re-run after the user listened read the survivor as
// "touched since the follow" and reconciled it again, taking the carried
// listened seconds off a second time (round-4 review S2 of #4064). A re-run
// that skips on a marker says so in a warning, unless the marker records
// that the put-back's write landed (PostUpdatedAt): that put-back is
// complete, and the re-run is silent. The marker is not written in
// the same batch as the progress, so it can outlive a write that did not
// land; when it does is set out on survivorReconcileMarker. In every such
// case the re-run reads the put-back as landed and leaves the carried state
// on the survivor as well as on the absorbed book: listened time counted
// twice, never fewer seconds than listened.
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
		// The journal names the account as it was when the follow ran; an admin
		// may have merged that account into another since (database.ResolveMergedUser),
		// and its rows are the survivor account's now.
		p.owner = database.ResolveMergedUser(db, p.UserID)
		w, err := restoreAbsorbedSide(db, p.dataUser(), absorbedID, p.AbsorbedState, p.AbsorbedPositions)
		if err != nil {
			return warnings, err
		}
		if w != "" {
			warnings = append(warnings, w)
		}
		w, err = restoreSurvivorSide(db, p, survivorID, absorbedID)
		if w != "" {
			warnings = append(warnings, w)
		}
		if err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// restoreSurvivorSide puts one user's survivor progress back after the
// follow (RestoreFollowedProgress), returning a warning when it left
// something as it is.
//
// It takes the per-(user, book) stripe the ABS progress, play and session
// handlers and readstatus hold (database.LockUserBookState) before its first
// read of the survivor's state, positions and marker, and holds it until
// after its last write, so what it decides from -- a plain put-back
// (putBackSurvivor), a reconcile (reconcileTouchedSurvivorOnce), a skip, or
// nothing to do -- is what it writes over: no writer that takes the stripe
// can land in between. Lock order: the caller holds merge.LockMergeRMW and
// no user-state stripe (restoreAbsorbedSide released the absorbed book's
// before this runs), and nothing called under this one takes a user-state
// stripe, which is not reentrant. Since 2026-10-18 every writer of a user's
// state or positions takes the stripe (database.LockUserBookState's HELD BY
// list: the web reading heartbeat and its recompute, readstatus' rebuild,
// the iTunes position backfill, the merge follow), so none can land
// between these reads and the writes.
//
// Until round 6 of the #4064 review the reads came before the stripe; each
// writer took it, re-read and compared, and a change sent the whole decision
// round again (up to three attempts).
func restoreSurvivorSide(db UserProgressMerger, p CombineUserProgress, survivorID, absorbedID string) (string, error) {
	defer database.LockUserBookState(p.dataUser(), survivorID)()
	cur, err := db.GetUserBookState(p.dataUser(), survivorID)
	if err != nil {
		return "", fmt.Errorf("read progress user=%s book=%s: %w", p.UserID, survivorID, err)
	}
	curPos, err := db.ListUserPositionsForBook(p.dataUser(), survivorID)
	if err != nil {
		return "", fmt.Errorf("read positions user=%s book=%s: %w", p.UserID, survivorID, err)
	}
	if sameProgress(cur, p.SurvivorStateBefore, curPos, p.SurvivorPosBefore) {
		// Already restored (an earlier run of this undo got here).
		return "", nil
	}
	hasAfter := p.SurvivorStateAfter != nil || len(p.SurvivorPosAfter) > 0
	var key string
	var marker *survivorReconcileMarker
	if hasAfter {
		key = survivorReconcileKey(survivorID, absorbedID, p)
		marker, err = readSurvivorReconcileMarker(db, key)
		if err != nil {
			return "", err
		}
		if marker != nil && !marker.PostUpdatedAt.IsZero() {
			// An earlier run of this put-back (the put-back below, or the
			// reconcile) wrote this user's survivor state and recorded
			// that the write landed (recordPutBackLanded). The put-back is
			// complete: whatever is there now is that write or the user's
			// own listening since, and doing it again would take the
			// carried seconds off a second time (round-4 S2). Nothing is
			// written and nothing is reported, since nothing was left as
			// it is. Until round 6 of the #4064 review this warned, so a
			// re-run after a later step failed reported a complete undo as
			// done in part.
			return "", nil
		}
		if marker != nil && cur != nil && !cur.UpdatedAt.Equal(marker.PreUpdatedAt) {
			// The marker says an earlier run of this put-back was about to
			// write this user's survivor state, the state has been written
			// since, and the landed write was never recorded (a crash, or
			// recordPutBackLanded's marker write failed). What is there now
			// is taken as the put-back plus the user's own listening, and
			// nothing is written: doing it again would take the carried
			// seconds off a second time. It is reported, because the
			// marker is trusted on its word: the one case it is wrong (a
			// crash between the marker and the write, see
			// survivorReconcileMarker) leaves the carried listened time on
			// both books.
			mlog.Warn("merge: survivor put-back of user=%s on %s from %s already landed per its marker; left as is",
				logger.SanitizeLogValue(p.UserID), logger.SanitizeLogValue(survivorID), logger.SanitizeLogValue(absorbedID))
			return fmt.Sprintf(
				"user %s progress on survivor %s was put back by an earlier run of this undo and listened to since; left as is", p.UserID, survivorID), nil
		}
	}
	if !sameProgress(cur, p.SurvivorStateAfter, curPos, p.SurvivorPosAfter) {
		if !hasAfter {
			// No after-snapshot (the follow failed, or its re-read did):
			// what the follow wrote cannot be told from the user's own
			// listening, so nothing is taken off the survivor.
			return fmt.Sprintf(
				"user %s progress on survivor %s changed after the merge and no after-merge record exists to separate it; left as is", p.UserID, survivorID), nil
		}
		// The user has listened to the survivor since. Take back only
		// what the follow brought, keep what they did there since.
		if err := reconcileTouchedSurvivorOnce(db, p, survivorID, key, marker, cur, curPos); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"user %s progress on survivor %s changed after the merge; the carried state was taken back off it and their listening there since was kept", p.UserID, survivorID), nil
	}
	return "", putBackSurvivor(db, p, survivorID, key, cur)
}

// putBackSurvivor writes one user's survivor progress back to its
// before-follow snapshot whole: the survivor was exactly as the follow left
// it (cur) when restoreSurvivorSide decided so, and it still holds the
// survivor's user-state stripe it decided under.
//
// The same marker the reconcile writes goes first (key empty: no
// after-snapshot, so no marker), holding the state's UpdatedAt as read under
// the stripe, so a re-run that finds the state written since knows this
// put-back landed (restoreSurvivorSide). A write that fails drops the marker
// again, as the reconcile does; one that lands records the row it wrote in
// the marker (recordPutBackLanded), so a re-run knows it is complete and
// stays silent. Until round 5 of the #4064 review the
// marker, positions and state were written with no stripe, and a sync
// landing between the read and the state write was lost.
func putBackSurvivor(db UserProgressMerger, p CombineUserProgress, survivorID, key string, cur *database.UserBookState) error {
	if key != "" {
		var pre time.Time
		if cur != nil {
			pre = cur.UpdatedAt
		}
		data, err := json.Marshal(survivorReconcileMarker{PreUpdatedAt: pre})
		if err != nil {
			return fmt.Errorf("encode survivor put-back marker: %w", err)
		}
		if err := db.SetRaw(key, data); err != nil {
			return fmt.Errorf("journal survivor put-back marker user=%s book=%s: %w", p.UserID, survivorID, err)
		}
	}
	if err := writeProgress(db, p.dataUser(), survivorID, p.SurvivorStateBefore, p.SurvivorPosBefore, cur); err != nil {
		if key != "" {
			if note := withdrawPutBackMarker(db, key); note != "" {
				return fmt.Errorf("%w; %s", err, note)
			}
		}
		return err
	}
	if key != "" {
		var pre time.Time
		if cur != nil {
			pre = cur.UpdatedAt
		}
		recordPutBackLanded(db, p.dataUser(), survivorID, key, pre)
	}
	return nil
}

// recordPutBackLanded rewrites the marker at key once a put-back's state
// write has landed (putBackSurvivor, reconcileTouchedSurvivorOnce), adding
// the UpdatedAt of the row that write left (PostUpdatedAt) to the pre-write
// one. The caller still holds the survivor's user-state stripe, so the row
// read back here is that write's, unless a writer that does not take the
// stripe landed after it; either way the put-back had landed first. A
// re-run that finds PostUpdatedAt set knows the put-back is complete, and
// leaves the survivor alone without a warning (restoreSurvivorSide).
//
// Best effort: the put-back has landed, so a read-back or marker write that
// fails is logged, not returned. The marker then holds only PreUpdatedAt,
// and a re-run reads a written-since state as landed and says so in a
// warning, as every such re-run did before round 6 of the #4064 review.
func recordPutBackLanded(db UserProgressMerger, userID, survivorID, key string, pre time.Time) {
	st, err := db.GetUserBookState(userID, survivorID)
	if err != nil || st == nil {
		if err != nil {
			mlog.Warn("merge: survivor put-back of user=%s on %s landed, but its state could not be read back to record it (%s); a re-run will report it as listened to since",
				logger.SanitizeLogValue(userID), logger.SanitizeLogValue(survivorID), logger.SanitizeLogValue(err.Error()))
		}
		return
	}
	data, err := json.Marshal(survivorReconcileMarker{PreUpdatedAt: pre, PostUpdatedAt: st.UpdatedAt})
	if err == nil {
		err = db.SetRaw(key, data)
	}
	if err != nil {
		mlog.Warn("merge: survivor put-back marker %s not updated after its write landed (%s); a re-run will report it as listened to since",
			logger.SanitizeLogValue(key), logger.SanitizeLogValue(err.Error()))
	}
}

// withdrawPutBackMarker takes back the marker a put-back wrote before a
// write that failed, so a re-run does not read the put-back as landed: with
// the user listening on the survivor in between, it would skip, and the
// carried listened time would stay on the survivor as well as go back to the
// absorbed book. The marker is deleted; when the delete fails it is
// overwritten as not landed (survivorReconcileMarker.NotLanded), which
// readSurvivorReconcileMarker reads as no marker. Only when both raw writes
// fail does it stay, and the note returned (for the caller's error, so it
// lands in the journal's LastError) says what a re-run will then do.
func withdrawPutBackMarker(db UserProgressMerger, key string) string {
	derr := db.DeleteRaw(key)
	if derr == nil {
		return ""
	}
	data, err := json.Marshal(survivorReconcileMarker{NotLanded: true})
	if err == nil {
		if err = db.SetRaw(key, data); err == nil {
			mlog.Warn("merge: survivor put-back marker %s not dropped after a failed write (%s); marked not landed instead",
				logger.SanitizeLogValue(key), logger.SanitizeLogValue(derr.Error()))
			return ""
		}
	}
	mlog.Error("merge: survivor put-back marker %s neither dropped (%s) nor marked not landed (%s) after a failed write",
		logger.SanitizeLogValue(key), logger.SanitizeLogValue(derr.Error()), logger.SanitizeLogValue(err.Error()))
	return fmt.Sprintf("its put-back marker %s could be neither dropped (%v) nor marked not landed (%v): if the user listens to the survivor before the undo is run again, that run reads this put-back as done and the carried listened time stays on both books", key, derr, err)
}

// survivorReconcilePrefix keys the per-user marker a survivor put-back
// (putBackSurvivor, reconcileTouchedSurvivorOnce) writes before its state
// write: merge:survivor-reconcile:<survivor>:<absorbed>:<user>:f:<follow
// id>, the follow id (CombineUserProgress.FollowID) being minted per
// follow, so a later follow between the same books gets a fresh key and a
// stale marker can never apply to it. Older journals use the timestamp key
// survivorReconcileKey describes.
const survivorReconcilePrefix = "merge:survivor-reconcile:"

// survivorReconcileMarker records the survivor state's UpdatedAt as it was
// right before a put-back's state write: reconcileTouchedSurvivorOnce's, or the
// plain put-back in RestoreFollowedProgress (zero when the survivor had no
// state row). SetUserBookState stamps UpdatedAt on every write, so on a
// re-run a different UpdatedAt means that write (or a later one) landed.
//
// The marker goes in its own raw write, before the progress write, so it
// can outlive a write that did not land. A failed write withdraws it
// (withdrawPutBackMarker: delete, or failing that overwrite it as
// NotLanded). It is left standing, and read as landed once the user writes
// the survivor, in two cases only: the process dies between the marker and
// the write, or the write fails and both raw writes that would withdraw it
// fail as well. The second case is named in the put-back's error, so it
// reaches the journal's LastError. Until round 5 of the #4064 review a
// failed delete alone left it standing, and these docs called the window
// crash-only.
//
// Markers are dropped once nothing can re-run the put-back
// (dropSurvivorReconcileMarkers). The user_state_follow operation revert
// (audiobooks revertUserStateFollow) does not drop them: a revert whose row
// mark then fails is retried, and needs them. Its markers are inert (each key
// names a follow id no later follow reuses) and stay as small raw keys.
type survivorReconcileMarker struct {
	PreUpdatedAt time.Time `json:"pre_updated_at"`
	// PostUpdatedAt is the UpdatedAt of the row the put-back's state write
	// left, recorded under the same stripe once that write landed
	// (recordPutBackLanded); zero until then, and on markers written before
	// round 6 of the #4064 review. A re-run that finds it set knows the
	// put-back is complete, whatever the user has done since, and stays
	// silent.
	PostUpdatedAt time.Time `json:"post_updated_at,omitzero"`
	// NotLanded withdraws the marker: the write it was journaled for
	// failed, and deleting it failed too (withdrawPutBackMarker). Read as
	// no marker.
	NotLanded bool `json:"not_landed,omitempty"`
}

// followStamp is the newest stamp in a follow's after-snapshot of the
// survivor: the state row's UpdatedAt (SetUserBookState stamps it on the
// follow's write) or a carried position's. Only the marker key of a journal
// written before FollowID uses it (survivorReconcileKey).
func followStamp(p CombineUserProgress) int64 {
	var t time.Time
	if p.SurvivorStateAfter != nil {
		t = p.SurvivorStateAfter.UpdatedAt
	}
	for _, a := range p.SurvivorPosAfter {
		if a.UpdatedAt.After(t) {
			t = a.UpdatedAt
		}
	}
	return t.UnixNano()
}

// survivorReconcileKey is the key of the marker one user's survivor
// put-back writes for one follow, the same for the put-back, the reconcile
// and the drop: <survivor>:<absorbed>:<user>:f:<FollowID>.
//
// A journal written before FollowID existed keeps the old key, ending in the
// follow's newest timestamp (followStamp). That stamp is not unique: carried
// positions keep their UpdatedAt, so a re-combine of the same pair after an
// undo can produce the same one, and a marker the first undo failed to drop
// then made the second undo skip (round-5 review S1). The old key is kept
// anyway, rather than reading such a journal as having no marker: without
// one, a re-run after the user listened would take the carried listened
// time off the survivor a second time (fewer seconds than listened), where
// a wrong skip leaves it on both books. And it can only collide with
// another pre-FollowID follow's marker: every follow journaled since gets
// the f: key.
func survivorReconcileKey(survivorID, absorbedID string, p CombineUserProgress) string {
	if p.FollowID != "" {
		return fmt.Sprintf("%s%s:%s:%s:f:%s", survivorReconcilePrefix, survivorID, absorbedID, p.UserID, p.FollowID)
	}
	return fmt.Sprintf("%s%s:%s:%s:%d", survivorReconcilePrefix, survivorID, absorbedID, p.UserID, followStamp(p))
}

// dropSurvivorReconcileMarkers deletes the reconcile markers one follow's
// put-back may have written (one per user, keyed by survivorReconcileKey).
// Call it only once nothing can re-run that put-back: the journal that
// holds it is recorded as undone (UndoCombine, a sibling-move journal's
// follow marked Undone and persisted) or the put-back was a one-shot
// (a refused carry's reversal). A re-run without its marker could subtract
// or add the carried listened time again. Best effort: a marker left behind
// is inert (its key names a follow id no later follow reuses; for a journal
// from before FollowID, see survivorReconcileKey), it only takes space, so a
// failed delete is logged.
func dropSurvivorReconcileMarkers(db UserProgressMerger, survivorID, absorbedID string, progress []CombineUserProgress) {
	for _, p := range progress {
		if p.SurvivorStateAfter == nil && len(p.SurvivorPosAfter) == 0 {
			continue // no after-snapshot: no put-back wrote a marker
		}
		key := survivorReconcileKey(survivorID, absorbedID, p)
		if err := db.DeleteRaw(key); err != nil {
			mlog.Warn("merge: survivor reconcile marker %s not dropped: %s",
				logger.SanitizeLogValue(key), logger.SanitizeLogValue(err.Error()))
		}
	}
}

// readSurvivorReconcileMarker reads the marker at key. ScanPrefix is the
// store's only raw read on UserProgressMerger, so the exact key is matched
// among the rows it returns.
func readSurvivorReconcileMarker(db UserProgressMerger, key string) (*survivorReconcileMarker, error) {
	rows, err := db.ScanPrefix(key)
	if err != nil {
		return nil, fmt.Errorf("read survivor reconcile marker %s: %w", key, err)
	}
	for _, r := range rows {
		if r.Key != key {
			continue
		}
		var m survivorReconcileMarker
		if err := json.Unmarshal(r.Value, &m); err != nil {
			return nil, fmt.Errorf("decode survivor reconcile marker %s: %w", key, err)
		}
		if m.NotLanded {
			return nil, nil
		}
		return &m, nil
	}
	return nil, nil
}

// reconcileTouchedSurvivorOnce puts one user's survivor progress back to its
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
//
// Safe to run twice (#3772 review B1). Every rule above is idempotent except
// listened time, whose delta is taken against the after-snapshot: a second
// run would find the reconciled survivor matching neither snapshot and
// subtract or add the carried seconds again (double-counting them, or
// destroying the user's listened time). So before the state write a
// per-user marker is journaled at key (survivorReconcileMarker) with the
// state's UpdatedAt as it is then. A re-run that finds the marker and a
// different UpdatedAt knows the write landed, and restoreSurvivorSide leaves
// this user's survivor alone without calling here (since the round-4 review;
// the plain put-back writes the same marker). marker is what
// restoreSurvivorSide read at key: one standing with the same UpdatedAt (an
// earlier run journaled it and its write did not land) is reused, not
// written again, and this does the full reconcile.
//
// cur and curPos are what restoreSurvivorSide read, and it still holds the
// survivor's per-(user, book) stripe (database.LockUserBookState) it read
// them under, so every write here -- positions, marker, state -- replaces
// exactly what want and st below were built from (every listening-state
// writer takes the stripe since 2026-10-18; restoreSurvivorSide). Until
// 2026-10-06 the positions were written before the stripe was taken, so a
// client position that landed after the read was rewound to the
// before-snapshot's row (the review of #3777). A state write that fails
// withdraws the marker (withdrawPutBackMarker). When a marker can still
// outlive a write that did not land is set out on survivorReconcileMarker; a
// re-run then reads the marker and a new UpdatedAt as "landed" and skips,
// which may leave the carried seconds on the survivor once -- never fewer
// seconds than the user listened. UndoCombine (re-run when a later absorbed
// book fails) and the user_state_follow revert (retried after a failure)
// both come through here.
func reconcileTouchedSurvivorOnce(db UserProgressMerger, p CombineUserProgress, survivorID, key string, marker *survivorReconcileMarker, cur *database.UserBookState, curPos []database.UserPosition) error {
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
	if err := writePositionsDiff(db, p.dataUser(), survivorID, curPos, want); err != nil {
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
	st.UserID, st.BookID = p.dataUser(), survivorID
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
	// cur is the row the state write replaces (read under the stripe still
	// held), so the marker's PreUpdatedAt is its UpdatedAt.
	wroteMarker := false
	if marker == nil {
		data, err := json.Marshal(survivorReconcileMarker{PreUpdatedAt: cur.UpdatedAt})
		if err != nil {
			return fmt.Errorf("encode survivor reconcile marker: %w", err)
		}
		if err := db.SetRaw(key, data); err != nil {
			return fmt.Errorf("journal survivor reconcile marker user=%s book=%s: %w", p.UserID, survivorID, err)
		}
		wroteMarker = true
	}
	if err := db.SetUserBookState(&st); err != nil {
		// The write did not land: drop the marker this run wrote, so a
		// retry that then sees a client's write (a new UpdatedAt) does not
		// take it for this one and skip the listened-time reconcile. Best
		// effort: if the delete fails too, the retry keeps the current
		// listened time (never fewer seconds than the user listened).
		if wroteMarker {
			if note := withdrawPutBackMarker(db, key); note != "" {
				return fmt.Errorf("reconcile state user=%s book=%s: %w; %s", p.UserID, survivorID, err, note)
			}
		}
		return fmt.Errorf("reconcile state user=%s book=%s: %w", p.UserID, survivorID, err)
	}
	recordPutBackLanded(db, p.dataUser(), survivorID, key, cur.UpdatedAt)
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
//
// The reads and the writes are one step under the per-(user, book) stripe
// (database.LockUserBookState; lock order: merge lock, then this stripe), so
// a device sync cannot land between the read the combine is built from and
// the write that would rewind it. The caller must not hold a user-state
// stripe, and none does: RestoreFollowedProgress holds none when it calls
// this, and takes the survivor's (restoreSurvivorSide) only after this has
// returned and released the absorbed book's.
func restoreAbsorbedSide(db userPositionStore, userID, absorbedID string, snapSt *database.UserBookState, snapPos []database.UserPosition) (string, error) {
	defer database.LockUserBookState(userID, absorbedID)()
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
	if sameProgress(nil, nil, curPos, positions) && (st == nil || sameUserState(st, cur)) {
		// The combined result is what is there already: the snapshot adds
		// nothing (a re-run after an earlier attempt combined it, or newer
		// listening that already covers it). Nothing is written, and nothing
		// is reported: the message below is for a put-back that changed the
		// book.
		return "", nil
	}
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
// (carryPosition) -- each such write replaces one row and removes nothing.
// When want drops a segment that have holds, the whole set is replaced at
// once (replacePositions), because there is no single-row delete and a
// clear followed by separate writes could stop half way with the user's
// positions gone.
func writePositionsDiff(db userPositionStore, userID, bookID string, have, want []database.UserPosition) error {
	wantBy := make(map[string]database.UserPosition, len(want))
	for _, w := range want {
		wantBy[w.SegmentID] = w
	}
	haveBy := make(map[string]database.UserPosition, len(have))
	for _, h := range have {
		haveBy[h.SegmentID] = h
		if _, ok := wantBy[h.SegmentID]; !ok {
			return replacePositions(db, userID, bookID, want)
		}
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

// replacePositions makes bookID's position rows for userID exactly want,
// keeping each row's UpdatedAt. With database.UserPositionReplacer (the
// production store, through any decorator) it is one atomic batch. A store
// without it gets the old clear-then-write sequence, which is not atomic.
func replacePositions(db userPositionStore, userID, bookID string, want []database.UserPosition) error {
	if r, ok := database.AsCapability[database.UserPositionReplacer](db); ok {
		if err := r.ReplaceUserPositions(userID, bookID, want); err != nil {
			return fmt.Errorf("replace positions user=%s book=%s: %w", userID, bookID, err)
		}
		return nil
	}
	if err := db.ClearUserPositions(userID, bookID); err != nil {
		return fmt.Errorf("clear positions user=%s book=%s: %w", userID, bookID, err)
	}
	for _, p := range sortPositionsOldestFirst(want) {
		if err := carryPosition(db, userID, bookID, p); err != nil {
			return fmt.Errorf("restore position %s user=%s book=%s: %w", p.SegmentID, userID, bookID, err)
		}
	}
	return nil
}

// combineOnRestore combines a follow's before-snapshot of one user's progress
// on bookID with what is on bookID now, losing neither. Both sides are the
// SAME book, so this is not the two-book merge rule (planUserStateMerge)
// although it shares its parts:
//
//   - which side is newer (whose state stands): the later lastUpdate, the
//     current side on a tie (restoreOrder). A side with nothing carryable is
//     never the newer one. Undated positions do not decide this.
//   - positions: the union by segment; where both have a segment, the newer
//     UpdatedAt wins (the current row on a tie). When either row of a
//     segment has no timestamp (a legacy row written before positions were
//     stamped reads as the zero time), the farther-ahead position in that
//     segment wins instead, the current row on a tie (owner decision
//     2026-10-05: with no time to compare, the farther-ahead position wins;
//     a legacy 4000s row is not rewound to a dated 3500s one). Positions in
//     different segments are never compared with each other.
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
//     OR'd, in both directions: it has no timestamp of its own to say
//     whether the reset came after the user hid the book, and the owner
//     rule is that hide is never destroyed by a merge or its undo.
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
	// The current row wins a tie whichever side's state stands: the
	// snapshot's rows go in first, the current rows are added with winsTie.
	// A reset on the newer side since the older side's last update leaves
	// the older side's rows out.
	snapRows, nowRows := snap.positions, now.positions
	if resetSince {
		if newerIsNow {
			snapRows = nil
		} else {
			nowRows = nil
		}
	}
	add(snapRows, false)
	add(nowRows, true)
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
// and whether the newer one is now. It decides whose STATE stands (status,
// the reset, finished-is-sticky's exception for a newer deliberate status),
// so it orders by time only: the later lastUpdate (a state's LastActivityAt
// and its dated positions), the current side on a tie. A side with nothing
// carryable is never the newer one.
//
// Undated positions do not take part here. Until 2026-10-05 any undated
// position on either side switched the whole comparison to "farther-ahead
// position wins", and the furthest position was taken across segments: a
// legacy undated row could hand the older side the status, overriding a
// newer deliberate mark-unfinished, and 4000s into segment 1 could outrank
// 100s into segment 3. The farther-ahead rule for undated rows is a rule
// about POSITIONS, and it is applied per segment, by positionWins.
func restoreOrder(snap, now userStateSide) (older, newer userStateSide, newerIsNow bool) {
	switch {
	case !hasCarryableState(now.state, now.positions):
		return now, snap, false
	case !hasCarryableState(snap.state, snap.positions):
		return snap, now, true
	}
	if lastUpdate(snap.state, snap.positions).After(lastUpdate(now.state, now.positions)) {
		return now, snap, false
	}
	return snap, now, true
}

// positionWins reports whether p replaces prev for ONE segment (the caller
// only ever compares two rows of the same segment): the newer UpdatedAt, or
// -- when either has no timestamp -- the farther-ahead position (owner
// decision 2026-10-05); winsTie settles an equal comparison in p's favour.
func positionWins(p, prev database.UserPosition, winsTie bool) bool {
	if p.UpdatedAt.IsZero() || prev.UpdatedAt.IsZero() {
		return p.PositionSeconds > prev.PositionSeconds || (winsTie && p.PositionSeconds == prev.PositionSeconds)
	}
	return p.UpdatedAt.After(prev.UpdatedAt) || (winsTie && p.UpdatedAt.Equal(prev.UpdatedAt))
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

// restoreAbsorbedRow is applyUndo step 1 for one absorbed book: under its
// membership lock, it restores the row only when this combine retired it
// (retiredByJournal), and reports whether it did. The lock is released by a
// defer, so a panic anywhere between the acquisition and the write (a store
// that crashes mid-write) cannot leak the group stripes and wedge every later
// merge of the group.
func (ms *Service) restoreAbsorbedRow(a CombineAbsorbed) (restored bool, warning string, err error) {
	gid := ""
	if a.VersionGroupID != nil {
		gid = strings.TrimSpace(*a.VersionGroupID)
	}
	// Membership lock: the shell's current group (the no-group sentinel when
	// it has none) and the group it is restored into, in ONE acquisition
	// (merge lock, then group stripes, then the book's write lock), held
	// across the incumbent read and the whole-row restore.
	unlockGroup, _, err := versionprimary.LockBookGroups(ms.db, []string{a.BookID}, gid)
	if err != nil {
		return false, "", fmt.Errorf("lock version groups of absorbed book %s: %w", a.BookID, err)
	}
	defer unlockGroup()
	b, err := ms.db.GetBookByID(a.BookID)
	if err != nil || b == nil {
		return false, "", fmt.Errorf("reload absorbed book %s: %v", a.BookID, err)
	}
	if !retiredByJournal(a, b) {
		return false, "", nil
	}
	b.MarkedForDeletion = nil
	b.MarkedForDeletionAt = nil
	// The library state is decided in step 2b, once this book's files are
	// back: at this point they are still on the survivor, so the restore rule
	// would read no files and answer "imported" for every shell.
	// MergedIntoBookID is the journal's to restore, not cleared here.
	b.FilePath = a.FilePath
	b.VersionGroupID = a.VersionGroupID
	b.IsPrimaryVersion = a.IsPrimaryVersion
	if gid != "" && a.IsPrimaryVersion != nil && *a.IsPrimaryVersion {
		// Another member may have become the group's primary while this one
		// was soft-deleted. One primary per group, always, by the same rule
		// as a trash restore (versionprimary.Incumbent: an explicit-true
		// member, else the one nil-flag member every visibility path reads
		// as primary). Read and written under the group's hand-off lock,
		// taken above.
		incumbent, ierr := versionprimary.IncumbentExcept(ms.db, gid, a.BookID)
		if ierr != nil {
			return false, "", fmt.Errorf("read version group %s of absorbed book %s: %w", gid, a.BookID, ierr)
		}
		versionprimary.YieldToIncumbent(b, incumbent)
		if b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion {
			warning = fmt.Sprintf("book %s was primary of version group %s, but %s is primary now; restored as a non-primary member",
				a.BookID, gid, incumbent)
		}
	}
	// UpdateBook on purpose: this is the journal's whole-row restore of the
	// absorbed shell, not a column write, so it is not routed through
	// ModifyBook.
	if _, err := ms.db.UpdateBook(a.BookID, b); err != nil {
		return false, warning, fmt.Errorf("restore absorbed book %s: %w", a.BookID, err)
	}
	return true, warning, nil
}

// applyUndo performs the reversal. Preconditions have passed.
func (ms *Service) applyUndo(j *CombineJournal) (*CombineUndoResult, error) {
	res := &CombineUndoResult{JournalID: j.ID, SurvivorID: j.SurvivorID}

	// 0. Reverse the retires of the absorbed shells first (retired-versions
	//    plan 6.5), so the restore below sees each shell as the combine left
	//    it. undoPreconditions has already proved every live one reversible.
	//    A retry finds the ones this attempt reversed dead (no pending rows)
	//    and RevertRetire does nothing for them.
	links, err := ms.combineRetireLinks(j)
	if err != nil {
		return res, err
	}
	for _, l := range links {
		if ms.retireUndo == nil {
			return res, fmt.Errorf("shell %s was retired by op %s and no retire undoer is wired", l.ShellID, l.OpID)
		}
		if err := ms.retireUndo.RevertRetire(l.OpID, l.ShellID); err != nil {
			return res, fmt.Errorf("reverse the retire of absorbed shell %s (op %s): %w", l.ShellID, l.OpID, err)
		}
	}

	// 1. Restore the absorbed rows first, so the file moves below land on live
	//    books. Re-fetch and patch only the fields the combine changed:
	//    UpdateBook is a full-column replace.
	//
	//    Only a row THIS combine retired is the journal's to restore: one
	//    still soft-deleted with exactly the stamp the combine wrote
	//    (retiredByJournal). The combine changes an absorbed row in that one
	//    write and nowhere else (the soft-delete clears FilePath in the same
	//    write), so a live absorbed book was never touched by it: a book the
	//    merge left live (LeftLive), one a crash stopped before its
	//    retirement (a pending step-journaled journal), or one a failed undo
	//    already restored. Such a row may have been edited since (linked into
	//    another group, renamed, organized), and writing the journal's
	//    before-values over it would undo that edit, not the combine. Its
	//    files still come back (step 2); its row is left as it is, here and in
	//    step 2b.
	retired := map[string]bool{}
	for _, a := range j.Absorbed {
		restored, warning, err := ms.restoreAbsorbedRow(a)
		if warning != "" {
			res.Warnings = append(res.Warnings, warning)
		}
		if err != nil {
			return res, err
		}
		if restored {
			retired[a.BookID] = true
			res.RestoredBooks = append(res.RestoredBooks, a.BookID)
		}
	}

	// 2. Files: move rows back to the book they came from, carry sync_file
	//    inos, restore disc/track.
	//
	//    A row the combine CREATED (a virtual single-file book materialized as
	//    a BookFile, planVirtualFile) is repointed too, never deleted: book
	//    file rows are not deleted, they are moved to the book they belong
	//    to. FromBookID is the book whose FilePath the row materialized,
	//    which is that row's owner after the undo: an absorbed book gets back
	//    the row for its own audio (its FilePath, restored in step 1, names
	//    the same file), and the survivor's own materialized row
	//    (planOwnFile, FromBookID == the survivor) stays where it is.
	absorbedIDs := map[string]bool{}
	for _, a := range j.Absorbed {
		absorbedIDs[a.BookID] = true
	}
	// listOwner is the absorbed book whose Files list moves came from, or "" for
	// the survivor's own list (SurvivorFiles). It decides where a row goes when
	// the outside book it was taken from has been purged (see below).
	moveBack := func(moves []CombineFileMove, listOwner string) error {
		// Decided row by row, never by the journal's status: only a row on
		// the survivor now is one the merge moved (or created) and has not
		// been moved back. Its numbers are restored while it is still there,
		// then it moves. A row already on its recorded owner is not written
		// at all: either the merge never moved it (it stopped first, or left
		// its book live), or an earlier attempt of this undo already moved
		// it back, numbers first. Either way a number it carries now was set
		// after, by someone else, and stays. Every step is safe to re-run.
		byOwner := map[string][]string{}
		var owners []string
		for _, fm := range moves {
			cur, err := ms.db.GetBookFileByID(j.SurvivorID, fm.FileID)
			if err != nil {
				return fmt.Errorf("load file %s: %w", fm.FileID, err)
			}
			if cur == nil {
				// Not on the survivor: back on its owner already, never
				// moved, or (a created row) never created.
				continue
			}
			if cur.Retired {
				return fmt.Errorf("file %s was retired into version %q since the combine; it is not moved back", fm.FileID, cur.VersionID)
			}
			if fm.Created && fm.FromBookID == j.SurvivorID {
				// The survivor's own materialized row stays where it is. Its
				// numbers (zero before) are put back only when a renumbering
				// could have followed the merge.
				if j.survivorMayBeRenumbered() {
					if err := ms.restoreSurvivorFileNumbers(j, fm); err != nil {
						return err
					}
				}
				continue
			}
			if fm.Created && !absorbedIDs[fm.FromBookID] {
				// No book of this combine to give it to: it stays on the
				// survivor, which owns the audio now, and the undo says so.
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"file row %s (created by the combine for book %s) has no book to go back to; left on survivor %s",
					fm.FileID, fm.FromBookID, j.SurvivorID))
				continue
			}
			if !fm.Created && fm.FromBookID != j.SurvivorID && !absorbedIDs[fm.FromBookID] {
				// A row reattached from a book outside the combine. If that
				// book has been purged since, the row has no book to go back
				// to, and the undo says so rather than refusing the whole undo
				// for it. Where it goes depends on whose audio it is. In an
				// absorbed book's list it is that book's own file (the combine
				// planned it from the book's FilePath, which step 1 restored),
				// so it goes to that book, as a created row does; leaving it on
				// the survivor would give the survivor audio it never had. In
				// the survivor's list the survivor's FilePath names the file,
				// so it stays.
				owner, err := ms.db.GetBookByID(fm.FromBookID)
				if err != nil {
					return fmt.Errorf("load book %s that file %s came from: %w", fm.FromBookID, fm.FileID, err)
				}
				if owner == nil {
					if listOwner == "" {
						res.Warnings = append(res.Warnings, fmt.Sprintf(
							"file row %s (%s) came from book %s, which no longer exists; left on survivor %s",
							fm.FileID, fm.FilePath, fm.FromBookID, j.SurvivorID))
						continue
					}
					res.Warnings = append(res.Warnings, fmt.Sprintf(
						"file row %s (%s) came from book %s, which no longer exists; returned to absorbed book %s, whose file it is",
						fm.FileID, fm.FilePath, fm.FromBookID, listOwner))
					fm.FromBookID = listOwner
				}
			}
			if cur.DiscNumber != fm.DiscBefore || cur.TrackNumber != fm.TrackBefore {
				cur.DiscNumber, cur.TrackNumber = fm.DiscBefore, fm.TrackBefore
				if err := ms.db.UpdateBookFile(cur.ID, cur); err != nil {
					return fmt.Errorf("restore disc/track of file %s: %w", cur.ID, err)
				}
			}
			if _, ok := byOwner[fm.FromBookID]; !ok {
				owners = append(owners, fm.FromBookID)
			}
			byOwner[fm.FromBookID] = append(byOwner[fm.FromBookID], fm.FileID)
		}
		for _, owner := range owners {
			ids := byOwner[owner]
			if err := ms.db.MoveBookFilesToBook(ids, j.SurvivorID, owner); err != nil {
				return fmt.Errorf("move %d file(s) back to %s: %w", len(ids), owner, err)
			}
			FollowFileMove(ms.db, j.SurvivorID, owner, ids)
			res.FilesMoved += len(ids)
		}
		return nil
	}
	for _, a := range j.Absorbed {
		if err := moveBack(a.Files, a.BookID); err != nil {
			return res, err
		}
	}
	if err := moveBack(j.SurvivorFiles, ""); err != nil {
		return res, err
	}
	// The survivor's own rows never moved. Their numbers are restored only
	// when the journal records that a renumbering could have run: a merge
	// stopped before that point never renumbered them, and a number there
	// now is someone else's later choice.
	if j.survivorMayBeRenumbered() {
		for _, fm := range j.SurvivorOwnFiles {
			if err := ms.restoreSurvivorFileNumbers(j, fm); err != nil {
				return res, err
			}
		}
	}

	// 2b. Library state, now that each absorbed book owns its files again:
	//     the shared restore rule (database.RestoreLibraryStateFromTrash). The
	//     combine never relabels the state, so this keeps it, except that
	//     "organized" needs the files back inside the library root and
	//     outside the iTunes library, and a row a later trash path labelled
	//     "deleted" gets back what that path recorded.
	//     Only for a row step 1 restored: a live row keeps its own state.
	env := TrashRestoreEnv()
	for _, a := range j.Absorbed {
		if !retired[a.BookID] {
			continue
		}
		files, err := ms.filesIncludingRetired(a.BookID)
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
	for _, a := range slices.Backward(j.Absorbed) {
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
			restore, warns, err := ms.overrideAuthorsToRestore(j.SurvivorID, o)
			if err != nil {
				return res, err
			}
			if err := ms.db.SetBookAuthors(j.SurvivorID, restore); err != nil {
				return res, fmt.Errorf("restore survivor authors: %w", err)
			}
			res.Warnings = append(res.Warnings, warns...)
			// The author the combine created is never deleted here (see
			// createdAuthorNote): when nothing credits it any more, the undo
			// says so and leaves it for maintenance.purge-empty-authors.
			if o.AuthorCreated {
				res.Warnings = append(res.Warnings, ms.createdAuthorNote(*o.AuthorIDAfter, o.Applied.Author)...)
			}
		}
		warns, err := ms.restoreOverrideLocks(j.SurvivorID, o)
		res.Warnings = append(res.Warnings, warns...)
		if err != nil {
			return res, err
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
					b.AuthorID = primaryAuthorOf(remaining)
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
	//    survivor is recomputed once more after the disc/track restores, and
	//    the restored absorbed books because a FilePath restore can change
	//    them.
	for _, id := range append([]string{j.SurvivorID}, res.RestoredBooks...) {
		if err := ms.db.RecomputeBookAggregates(id); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("recompute aggregates of %s: %v", id, err))
		}
	}
	return res, nil
}
