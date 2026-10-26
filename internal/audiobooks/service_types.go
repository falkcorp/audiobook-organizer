// file: internal/audiobooks/service_types.go
// version: 1.15.0
// guid: a3f9b2c1-d4e5-6f70-8a9b-0c1d2e3f4a5b
// last-edited: 2026-10-26

package audiobooks

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// AudiobooksListResponse represents the response for listing audiobooks
type AudiobooksListResponse struct {
	Items  []AudiobookDetail `json:"items"`
	Count  int               `json:"count"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

// AudiobookDetail extends database.Book with author and series names for response
type AudiobookDetail struct {
	*database.Book
	AuthorName *string `json:"author_name,omitempty"`
	SeriesName *string `json:"series_name,omitempty"`
}

// SoftDeletedBooksResponse represents the response for listing soft-deleted books
type SoftDeletedBooksResponse struct {
	Items  []database.Book `json:"items"`
	Count  int             `json:"count"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// PurgeResult represents the result of purging soft-deleted books
type PurgeResult struct {
	Attempted    int `json:"attempted"`
	Purged       int `json:"purged"`
	FilesDeleted int `json:"files_deleted"`
	// SkippedOwnsFiles counts books left soft-deleted because they still own
	// book_file rows: hard-deleting them would orphan those rows
	// (database.ErrBookOwnsFiles). They are named in SkippedOwnsFilesIDs, NOT
	// in Errors: a merge loser keeps its files for good, so it is skipped on
	// every run, and repeating one error line per book per run would bury the
	// real errors. The nightly job reports the count once per run.
	SkippedOwnsFiles    int      `json:"skipped_owns_files"`
	SkippedOwnsFilesIDs []string `json:"skipped_owns_files_ids,omitempty"`
	// The next three count the books a user still has listening state on
	// (merge.BookHasCarryableUserState: a position, a status, progress,
	// listened time or the hide flag). Such a book is never purged with its
	// state on it. Each count is named in its *IDs list and reported once
	// per run like SkippedOwnsFiles.
	//
	// CarriedToSibling: the state was carried, all or nothing, to a member
	// of the book's version group that Audiobookshelf lists, and the book
	// was then purged. These are also counted in Purged.
	CarriedToSibling    int      `json:"carried_to_sibling"`
	CarriedToSiblingIDs []string `json:"carried_to_sibling_ids,omitempty"`
	// KeptHasProgress: no version-group sibling Audiobookshelf lists exists
	// (a hidden copy -- not primary, not organized, quarantined -- never
	// receives the state, or it would be stranded out of sight), so the book
	// stays in the trash, shown as "has progress", until the owner restores
	// it or discards its progress (DiscardProgressAndPurge).
	KeptHasProgress    int      `json:"kept_has_progress"`
	KeptHasProgressIDs []string `json:"kept_has_progress_ids,omitempty"`
	// CarryFailed: a sibling exists but the carry did not complete (or the
	// sibling could not be looked up). Fail closed: every user's state is
	// back on the book and it stays in the trash; the reason is in Errors.
	CarryFailed    int      `json:"carry_failed"`
	CarryFailedIDs []string `json:"carry_failed_ids,omitempty"`
	// KeptUndoPossible counts trashed books an undo that can still run needs
	// (undoHold): the retired shell of a combine (merge.UndoShellIndex), the
	// shell a maintenance operation soft-deleted and journaled in the
	// operation ledger, or the loser of an auto-merge that was not undone.
	// Purging one would make that undo refuse or stop part way. The book stays
	// in the trash until the undo runs or is retired. This is the blocker the
	// purge gained on 2026-10-22; it never applies to a book that owns files
	// (those are SkippedOwnsFiles first).
	KeptUndoPossible    int      `json:"kept_undo_possible"`
	KeptUndoPossibleIDs []string `json:"kept_undo_possible_ids,omitempty"`
	// Failed counts books the purge could not decide or finish (unreadable
	// file rows, a store error on the delete); each has a line in Errors.
	Failed int `json:"failed"`
	// TrashedBefore is the instant the pass was bound to (PurgeOptions.
	// TrashedBefore): a preview returns the one it used, the dialog sends it
	// back with the purge, and a purge echoes the one it ran under. A book
	// trashed (or, without a stamp, written) after it is left alone. nil on a
	// pass with no bound (the nightly one).
	TrashedBefore *time.Time `json:"trashed_before,omitempty"`
	// TrashedAfterPreview counts the books the bound left out: trashed after
	// the preview (or restored and trashed again), or with no stamp to show
	// they were not. In a preview itself: trashed while it ran, or with no
	// trash time on record. Not touched, and not in any kept count. (A book
	// that was in the trash when the preview ran and only aged into the
	// retention window since is NotYetDue, not this.)
	TrashedAfterPreview int `json:"trashed_after_preview"`
	// NotYetDue counts the books the retention window (PurgeOptions.
	// OlderThanDays) left out because they were trashed less than that long ago.
	// Attempted counts them: it is the whole trash.
	NotYetDue int `json:"not_yet_due"`
	// NotInPreview counts the books a bound pass found it could purge but the
	// preview it is bound to had not listed as purgeable (it showed them as kept:
	// they owned files, an undo could still run, or they held listening state,
	// and that changed since). Not touched; a new preview lists them.
	NotInPreview int `json:"not_in_preview"`
	// FilesToDelete, on a preview, lists the files and empty directories a
	// purge WITH delete_files would remove for the books it would purge
	// (purgeFinish's own checks: inside the library root, not protected, named
	// by no book_file row or live book). FilesToDeleteCount is the exact count
	// when the list is capped; FilesKept counts the files the purge would leave
	// in place (protected, outside the root, still referenced, or also named by
	// a trashed book that is itself kept). Computed whether or not the preview
	// asked for delete_files.
	FilesToDelete      []string `json:"files_to_delete,omitempty"`
	FilesToDeleteCount int      `json:"files_to_delete_count"`
	FilesKept          int      `json:"files_kept"`
	// FilesPendingFromEarlierPass, on a preview, is how many files an earlier
	// pass decided to remove and did not get to (it crashed or was stopped: the
	// journal of pending file removals). A purge with delete_files carries
	// those out too, through the same checks, on top of FilesToDeleteCount; a
	// purge without it never does. They are not in FilesToDelete: they belong
	// to books that are already gone. It does NOT count FilesWaitingForFolder.
	FilesPendingFromEarlierPass int `json:"files_pending_from_earlier_pass,omitempty"`
	// FilesWaitingForFolder, on a preview, is how many of the journal's files a
	// pass could not decide because the file is not there and its folder is
	// missing or empty. No purge removes them: the records stay until the folder
	// holds something again or the owner forgets them
	// (ForgetWaitingFileRecords), so they are counted apart from
	// FilesPendingFromEarlierPass, which a purge with delete_files does act on.
	FilesWaitingForFolder int `json:"files_waiting_for_folder,omitempty"`
	// FilesReplayed, on a purge with delete_files, is how many of those files it
	// removed. They are NOT in FilesDeleted, which counts only the books this
	// pass purged; the lines about the ones it kept or could not remove are in
	// Errors, labelled "from an earlier pass".
	FilesReplayed int `json:"files_replayed,omitempty"`
	// fileCands are the paths a preview's purgeable books would remove, before
	// settlePreviewFiles decides which stay (two trashed rows can name one path,
	// and a kept row's file is its own). purgeable is the set of books the
	// preview says would be purged, kept by the service for the purge bound to
	// it.
	fileCands map[string]struct{}
	purgeable map[string]struct{}
	// startedAt is when the pass began. A preview's is kept with it
	// (purgePreviewRecord.started) so the purge bound to it can tell a book
	// that was already in the trash when the preview ran from one trashed since.
	startedAt time.Time
	// DryRun is set on a preview: nothing was deleted or moved. Purged is then
	// 0 and Purgeable is the number a real run would purge right now, the
	// books that would carry listening state to a sibling included
	// (CarriedToSibling counts those "would carry").
	DryRun    bool `json:"dry_run"`
	Purgeable int  `json:"purgeable"`
	// Truncated is set when the ID lists and Errors were cut to keep the result
	// small enough to store as an operation result; the counts are exact.
	Truncated bool     `json:"truncated,omitempty"`
	Errors    []string `json:"errors"`
	// StoppedEarly is set when the pass ended before it decided every book:
	// it was cancelled, ran past its time limit, or was refused before it
	// began (StopReason says which). The counts then cover only the books
	// decided, so a reader must not take them for the whole trash. It is a
	// flag and not a line in Errors, which Capped cuts.
	StoppedEarly bool   `json:"stopped_early,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
}

// AudiobookUpdate represents a partial update to an audiobook
type AudiobookUpdate struct {
	*database.Book
	AuthorName      *string                    `json:"author_name,omitempty"`
	SeriesName      *string                    `json:"series_name,omitempty"`
	Overrides       map[string]OverridePayload `json:"overrides,omitempty"`
	UnlockOverrides []string                   `json:"unlock_overrides,omitempty"`

	// ClearSeries asks for the book's series link to be removed, the same
	// as a SeriesName that trims to "". The JSON form is `"series_id": null`,
	// which SeriesID (an *int) cannot carry: nil there means "not sent".
	ClearSeries bool `json:"-"`
}

// OverridePayload represents metadata override information
type OverridePayload struct {
	Value        json.RawMessage `json:"value"`
	Locked       *bool           `json:"locked,omitempty"`
	FetchedValue json.RawMessage `json:"fetched_value,omitempty"`
	Clear        bool            `json:"clear,omitempty"`
}

// FieldFilter represents a field-specific search filter from advanced search.
type FieldFilter struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Negated bool   `json:"negated"`
	// Quoted is true when the user wrote the value in double quotes
	// (title:"a*b"). A quoted value is a literal substring: no regex, glob,
	// or comparison. See internal/querygrammar.
	Quoted bool `json:"quoted,omitempty"`
}

// ListFilters holds optional filters for listing audiobooks.
type ListFilters struct {
	IsPrimaryVersion *bool
	// ExcludeQuarantined drops quarantined books during the indexed scan so
	// pagination counts the post-quarantine set (a 500-page returns 500
	// non-quarantined books, and the total count matches). Set by the HTTP
	// layer from the inverse of ?show_quarantined.
	ExcludeQuarantined bool
	LibraryState       string
	Tag                string
	Tags               []string
	SortBy             string        // column sort key
	SortOrder          string        // "asc" or "desc"
	FieldFilters       []FieldFilter // advanced field-specific filters (book-global)
	PerUserFilters     []FieldFilter // per-user filters (read_status, progress_pct, last_played)
	UserID             string        // caller's user ID; required for PerUserFilters
	// Fingerprinting filters
	FingerprintStatus  string // "none", "partial", "complete", or "" for any
	CoveragePercentMin *int   // minimum coverage percentage (inclusive)
	CoveragePercentMax *int   // maximum coverage percentage (inclusive)
	// RestrictToIDs, when non-nil, limits the result to books whose ID is in
	// the set. It is ANDed with every other predicate, including the tag
	// intersection, so a caller can narrow an ID-shaped query (the
	// has_file_errors and quick-query fast paths) without losing the search,
	// field filters, sort or pagination the same request asked for.
	//
	// nil and empty are DIFFERENT, matching database.BookSummaryFilter: nil
	// means "no ID restriction", while non-nil-but-empty means "no book is
	// eligible" and correctly yields an empty page with a count of 0. Never
	// test this with len() alone.
	RestrictToIDs map[string]struct{}
}

// UpdateAudiobookRequest represents parameters for updating an audiobook
//
// RawPayload is the request body's top-level keys. It is what Sent reads, so
// a request built by hand must list in it every field it means to change:
// Updates holds parsed values only, and a field present there but absent from
// RawPayload (and from Overrides) is treated as not sent.
type UpdateAudiobookRequest struct {
	Updates             *AudiobookUpdate
	RawPayload          map[string]json.RawMessage
	ResolvingAuthorName string
	ResolvingSeriesName string
}

// Sent reports whether the client named field (a payload key, which is also
// its lock key): as a top-level key, or as an override that carries a value.
// An override that only clears or (un)locks the lock changes no field, so it
// does not count. UpdateAudiobook makes every "did the client send this"
// decision through it.
func (r *UpdateAudiobookRequest) Sent(field string) bool {
	if r == nil {
		return false
	}
	if _, ok := r.RawPayload[field]; ok {
		return true
	}
	if r.Updates != nil {
		if o, ok := r.Updates.Overrides[field]; ok && len(o.Value) > 0 && !o.Clear {
			return true
		}
	}
	return false
}

// DeleteAudiobookOptions contains options for deleting an audiobook
type DeleteAudiobookOptions struct {
	SoftDelete bool
	BlockHash  bool
}

// PerUserFieldNames is the set of search fields whose values come from
// per-user state (database.UserBookState) rather than book columns.
// Handlers use this to split incoming FieldFilters between the global
// pass (matchesCompiledFilters) and the per-user pass.
var PerUserFieldNames = map[string]struct{}{
	"read_status":  {},
	"progress_pct": {},
	"last_played":  {},
}

// IsPerUserField reports whether f targets per-user state.
func IsPerUserField(field string) bool {
	_, ok := PerUserFieldNames[field]
	return ok
}

// strippedMemdbFields enumerates Book fields that stripBookForMemdb()
// clears from memdb-resident copies. Predicate filters on these fields
// silently miss against memdb Books (which is the default code path in
// production) — callers must fetch the full Book from Pebble via
// GetBookByID and re-test those filters. See internal/database/memdb_strip.go.
var strippedMemdbFields = map[string]bool{
	"description":   true,
	"version_notes": true,
	"book_sig_v1":   true,
}

// strippedCompiledFieldNames returns the field names from a compiled filter
// slice, for log diagnostics only.
func strippedCompiledFieldNames(ff []compiledFilter) []string {
	out := make([]string, 0, len(ff))
	for _, f := range ff {
		out = append(out, f.Field)
	}
	return out
}
