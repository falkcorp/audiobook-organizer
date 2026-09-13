// file: internal/metafetch/apply_preview.go
// version: 1.3.0
// guid: 3d6a0f94-8b27-4c1e-a5d3-e9f2b7c04a18
// last-edited: 2026-09-13
//
// Read-only preview of ApplyMetadataCandidate, for the bulk-apply dry run.
//
// READ-ONLY IS THE CONTRACT. Nothing in this file may call a store method that
// writes: no UpdateBook, CreateAuthor, SetBookAuthors, CreateSeries,
// UpdateBookFile, PutMetadataCache, DeleteMetadataCache, change history, field
// state, and no file I/O. That is why the per-field diff is computed here
// rather than by running applyMetadataUnguarded on a copy of the book: the
// apply body resolves-or-CREATES author and series rows and rewrites the
// book_authors join, so "run it on a copy" writes to the library.
//
// KEEP IN STEP with applyMetadataUnguarded (service_apply.go). previewFields
// mirrors each of its conditions using the same predicates (IsBetterValue,
// IsBetterStringPtr, IsGarbageValue). A new field written by the apply body
// needs a row here, or the dry run will under-report what an apply changes.

package metafetch

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/policy"
)

// FieldChange is one field an apply would change.
type FieldChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// RenameMove is one file a rename would move.
type RenameMove struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RenamePreview says whether the apply's file sequel would move files.
type RenamePreview struct {
	WouldRename bool   `json:"would_rename"`
	Reason      string `json:"reason,omitempty"` // why not, when WouldRename is false
	// TargetBookID is the book whose files would move: the book itself, or its
	// library copy when the book lives under a protected (iTunes/import) path.
	TargetBookID string       `json:"target_book_id,omitempty"`
	Moves        []RenameMove `json:"moves,omitempty"`
	// Blocking is set when the apply's file sequel is KNOWN to fail to land
	// the rename: the post-apply plan cannot be computed (a broken pattern, or
	// two files of the book on one target -- organizer.ErrDuplicateRenameTarget),
	// or a planned target is held by a different file under a recorded,
	// unresolved collision. An apply in that state writes the database and
	// leaves the files behind, so RenamePreflight refuses it.
	Blocking string `json:"blocking,omitempty"`
}

// ErrApplyFileWorkWouldFail is returned by RenamePreflight when the database
// apply must not run because its file sequel cannot land.
var ErrApplyFileWorkWouldFail = fmt.Errorf("apply refused before any write: the file rename cannot land")

// ApplyRefusedReasonFileWorkWouldFail is the machine-readable reason every
// apply path reports when RenamePreflight refuses a book: the batch op's skip
// reason and the single-book endpoint's 409 body. One constant so the two
// cannot drift.
const ApplyRefusedReasonFileWorkWouldFail = "file_work_would_fail"

// RenamePreflight answers, BEFORE ApplyMetadataCandidate writes anything,
// whether the write-back rename that follows the apply is known to fail.
//
// The apply is database-first by design (the file sequel is slow and runs
// under its own locks), so a rename that fails after it leaves the database
// holding the new metadata while the files keep their old names -- what
// happened to three books on 2026-09-13. Rolling the database back afterwards
// would have to unwind author/series rows, provenance, cover and the iTunes
// enqueue; refusing up front unwinds nothing. The check is the same read-only
// plan the bulk-apply dry run shows (previewRename), so the preview and the
// refusal cannot disagree.
//
// A preview that cannot be built at all is not a refusal: ApplyMetadataCandidate
// runs the same reads (book, policy tag, field locks) and reports its own error
// for the same cause. It is logged so it is never silent.
//
// fields is the apply's field allowlist, exactly as passed to
// ApplyMetadataCandidate (nil or empty means every field). The single-book
// endpoint applies a user-selected subset, and a subset that leaves out the
// title or author plans a different rename than the whole candidate would, so
// the preflight must plan the same subset or it refuses applies that would
// have landed.
func (mfs *Service) RenamePreflight(id string, candidate MetadataCandidate, fields []string) error {
	pv, err := mfs.previewMetadataCandidate(id, candidate, fields, true)
	if err != nil {
		preflightLog.Warn("rename preflight could not preview book %s; leaving the decision to the apply: %s",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		return nil
	}
	if pv.Rename.Blocking != "" {
		return fmt.Errorf("%w: %s", ErrApplyFileWorkWouldFail, pv.Rename.Blocking)
	}
	return nil
}

var preflightLog = logger.New("metafetch.preflight")

// ApplyPreview is what ApplyMetadataCandidate would do to one book.
type ApplyPreview struct {
	BookID        string        `json:"book_id"`
	Changes       []FieldChange `json:"changes"`
	SkippedLocked []string      `json:"skipped_locked,omitempty"`
	Rename        RenamePreview `json:"rename"`
}

// ErrApplyPolicyBlocked is returned when the book carries policy:no-metadata,
// the same refusal ApplyMetadataCandidate makes.
var ErrApplyPolicyBlocked = fmt.Errorf("metadata application disabled by policy:no-metadata tag")

// CandidateMetadata converts a candidate to the BookMetadata the apply writes.
// Shared by ApplyMetadataCandidate and the preview so the two cannot disagree
// about which candidate fields reach the book.
func CandidateMetadata(candidate MetadataCandidate) metadata.BookMetadata {
	return metadata.BookMetadata{
		Title:          candidate.Title,
		Author:         candidate.Author,
		Narrator:       candidate.Narrator,
		Series:         candidate.Series,
		SeriesPosition: candidate.SeriesPosition,
		PublishYear:    candidate.Year,
		Publisher:      candidate.Publisher,
		ISBN:           candidate.ISBN,
		ISBN10:         candidate.ISBN10,
		ISBN13:         candidate.ISBN13,
		ASIN:           candidate.ASIN,
		Genre:          candidate.Genre,
		CoverURL:       candidate.CoverURL,
		Description:    candidate.Description,
		Language:       candidate.Language,
		DurationSec:    candidate.DurationSec,
		// Content-matcher SIGNAL fields — carried so a manual apply persists them
		// too, matching the auto-fetch path.
		Abridged:                candidate.Abridged,
		Subtitle:                candidate.Subtitle,
		PageCount:               candidate.PageCount,
		SeriesSecondary:         candidate.SeriesSecondary,
		SeriesSecondaryPosition: candidate.SeriesSecondaryPosition,
		// candidate.Year is a bare int with no kind attached; derive whether it
		// is an audiobook release year (Audible/Audnexus) from the source name so
		// ApplyMetadataToBook routes it to the same field as the auto-fetch path.
		PublishYearIsAudiobookRelease: metadata.SourceProducesAudiobookReleaseYear(candidate.Source),
	}
}

// PreviewMetadataCandidate reports what ApplyMetadataCandidate(id, candidate,
// nil) would change, after locked fields are stripped, and whether the file
// sequel would rename. writeBack is whether the caller's apply would run the
// file sequel at all (the batch op's write_back); the rename additionally
// requires auto_rename_on_apply, as in runApplyPipeline.
func (mfs *Service) PreviewMetadataCandidate(id string, candidate MetadataCandidate, writeBack bool) (*ApplyPreview, error) {
	return mfs.previewMetadataCandidate(id, candidate, nil, writeBack)
}

// previewMetadataCandidate is PreviewMetadataCandidate for
// ApplyMetadataCandidate(id, candidate, fields): the same field filter the
// apply runs (FilterApplyFields) is applied before the diff and the rename plan.
func (mfs *Service) previewMetadataCandidate(id string, candidate MetadataCandidate, fields []string, writeBack bool) (*ApplyPreview, error) {
	book, err := mfs.db.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}
	if tags, tagErr := mfs.db.GetBookTags(id); tagErr == nil && policy.EvaluatePolicy(tags).NoMetadataFetch {
		return nil, ErrApplyPolicyBlocked
	}

	meta := FilterApplyFields(CandidateMetadata(candidate), fields)
	NormalizeMetaSeries(&meta)
	locks, err := mfs.loadFieldLocks(id)
	if err != nil {
		return nil, fmt.Errorf("refusing to preview metadata for %s: %w", id, err)
	}
	meta, skipped := StripLockedFields(meta, locks.Set())

	changes, after := mfs.previewFields(book, meta)
	out := &ApplyPreview{BookID: id, Changes: changes, SkippedLocked: skipped}
	out.Rename = mfs.previewRename(book, after, writeBack)
	return out, nil
}

// previewAfter is the book as the apply would leave it, for the rename plan.
type previewAfter struct {
	book       database.Book
	authorName string
	seriesName string
}

// previewFields mirrors applyMetadataUnguarded without writing. See the file
// header: every branch here corresponds to one there.
func (mfs *Service) previewFields(book *database.Book, meta metadata.BookMetadata) ([]FieldChange, previewAfter) {
	after := *book // shallow copy: only pointers are reassigned below, never written through
	var changes []FieldChange
	add := func(field, oldV, newV string) {
		if oldV != newV {
			changes = append(changes, FieldChange{Field: field, Old: oldV, New: newV})
		}
	}

	if meta.Title != "" && meta.Title != "Untitled" && IsBetterValue(book.Title, meta.Title) &&
		!(book.Title != "" && !IsGarbageValue(book.Title) && len(meta.Title) < 3) {
		after.Title = meta.Title
	}
	add("title", book.Title, after.Title)

	if meta.Publisher != "" && IsBetterStringPtr(book.Publisher, meta.Publisher) {
		add("publisher", deref(book.Publisher), meta.Publisher)
		after.Publisher = new(meta.Publisher)
	}
	if meta.Language != "" && IsBetterStringPtr(book.Language, meta.Language) {
		add("language", deref(book.Language), meta.Language)
		after.Language = new(meta.Language)
	}
	if meta.PublishYear != 0 {
		if meta.PublishYearIsAudiobookRelease {
			add("audiobook_release_year", derefInt(book.AudiobookReleaseYear), strconv.Itoa(meta.PublishYear))
		} else if book.PrintYear == nil || *book.PrintYear == 0 {
			add("print_year", derefInt(book.PrintYear), strconv.Itoa(meta.PublishYear))
			after.PrintYear = new(meta.PublishYear)
		}
	}
	if meta.CoverURL != "" {
		add("cover_url", deref(book.CoverURL), meta.CoverURL)
	}
	if meta.Narrator != "" && !IsGarbageValue(meta.Narrator) && IsBetterStringPtr(book.Narrator, meta.Narrator) {
		add("narrator", deref(book.Narrator), meta.Narrator)
		after.Narrator = new(meta.Narrator)
	}

	// Author, with the apply body's narrator guard.
	oldAuthor := mfs.previewAuthorName(book)
	newAuthor := oldAuthor
	if a := meta.Author; a != "" && !IsGarbageValue(a) {
		if book.AuthorID != nil && book.Narrator != nil && strings.EqualFold(a, *book.Narrator) &&
			!strings.EqualFold(a, oldAuthor) {
			a = "" // the apply body keeps the DB author: the candidate's "author" is the narrator
		}
		if a != "" {
			newAuthor = a
		}
	}
	add("author", oldAuthor, newAuthor)

	if isbn13 := firstNonEmpty(meta.ISBN13, isbnOfLen(meta.ISBN, 13)); isbn13 != "" {
		add("isbn13", deref(book.ISBN13), isbn13)
		after.ISBN13 = &isbn13
	}
	if isbn10 := firstNonEmpty(meta.ISBN10, isbnOfLen(meta.ISBN, 10)); isbn10 != "" {
		add("isbn10", deref(book.ISBN10), isbn10)
		after.ISBN10 = &isbn10
	}
	if meta.ASIN != "" {
		add("asin", deref(book.ASIN), meta.ASIN)
	}
	if meta.Description != "" {
		add("description", truncate(deref(book.Description)), truncate(meta.Description))
	}
	if meta.Genre != "" {
		add("genre", deref(book.Genre), meta.Genre)
	}
	if meta.Abridged != nil {
		old := ""
		if book.Abridged != nil {
			old = strconv.FormatBool(*book.Abridged)
		}
		add("abridged", old, strconv.FormatBool(*meta.Abridged))
	}
	if meta.Subtitle != "" {
		add("subtitle", deref(book.Subtitle), meta.Subtitle)
	}
	if meta.PageCount > 0 {
		add("page_count", derefInt(book.PageCount), strconv.Itoa(meta.PageCount))
	}
	if meta.SeriesSecondary != "" && !IsGarbageValue(meta.SeriesSecondary) {
		add("series_secondary", deref(book.SeriesSecondary), meta.SeriesSecondary)
		if meta.SeriesSecondaryPosition != "" {
			add("series_secondary_position", deref(book.SeriesSecondaryPosition), meta.SeriesSecondaryPosition)
		}
	}
	if meta.DurationSec > 0 {
		add("audible_runtime_min", derefInt(book.AudibleRuntimeMin), strconv.Itoa(meta.DurationSec/60))
	}

	oldSeries := mfs.previewSeriesName(book)
	newSeries := oldSeries
	validSeries := meta.Series != "" && !IsGarbageValue(meta.Series)
	if validSeries {
		newSeries = meta.Series
	}
	add("series", oldSeries, newSeries)
	positionOnly := meta.Series == "" && book.SeriesID != nil
	if meta.SeriesPosition != "" && (validSeries || positionOnly) {
		oldPos := deref(book.SeriesPositionRaw)
		if oldPos == "" {
			oldPos = derefInt(book.SeriesSequence)
		}
		add("series_position", oldPos, meta.SeriesPosition)
		raw := meta.SeriesPosition
		after.SeriesPositionRaw = &raw
		if pos, err := strconv.Atoi(meta.SeriesPosition); err == nil {
			after.SeriesSequence = &pos
		}
	}
	return changes, previewAfter{book: after, authorName: newAuthor, seriesName: newSeries}
}

// previewRename mirrors the rename leg of runApplyPipeline: library copy for a
// protected book, the organizer's target paths, protected entries dropped, the
// rename checkpoint and the recorded-failure block. All of those are reads:
// the recorded-failure check is ApplyRenameBlockedReadOnly, which reports a
// stale record as not blocking without clearing it (ApplyRenameBlocked would
// clear it -- a write, and on behalf of an apply that may yet be refused).
func (mfs *Service) previewRename(book *database.Book, after previewAfter, writeBack bool) RenamePreview {
	switch {
	case !writeBack:
		return RenamePreview{Reason: "write_back is off: no file work"}
	case !config.AppConfig.AutoRenameOnApply:
		return RenamePreview{Reason: "auto_rename_on_apply is off"}
	}
	target, ok := mfs.existingLibraryCopy(book)
	if !ok || target == nil {
		return RenamePreview{Reason: "protected book with no library copy: rename and tag write are skipped"}
	}
	files, err := mfs.db.GetBookFiles(target.ID)
	if err != nil {
		return RenamePreview{TargetBookID: target.ID, Reason: "list book files failed: " + err.Error()}
	}
	files = dedupeBookFilesByPath(target.ID, files)
	if len(files) == 0 {
		return RenamePreview{TargetBookID: target.ID, Reason: "book has no file rows"}
	}

	// The book as the apply leaves it, on the target's identity. The ID is
	// deliberately not the real one: the organizer prefers the book_authors
	// join (by book ID) over the scalar author, and the join still names the
	// OLD author until the apply rewrites it, so a real ID would plan the move
	// with the pre-apply author.
	planned := after.book
	if target.ID != book.ID {
		planned.FilePath = target.FilePath
	}
	planned.ID = target.ID + "\x00preview"
	planned.AuthorID = nil
	planned.Author = &database.Author{Name: after.authorName}
	planned.Series = nil
	if after.seriesName != "" {
		planned.Series = &database.Series{Name: after.seriesName}
	}

	entries, err := newPathOrganizer(mfs.db).ComputeTargetPaths(&planned, files)
	if err != nil {
		// runApplyPipeline returns this same error after the DB apply.
		reason := "compute target paths failed: " + err.Error()
		return RenamePreview{TargetBookID: target.ID, Reason: reason, Blocking: reason}
	}
	entries = mfs.dropProtectedRenameEntries(target.ID, entries)
	var moves []RenameMove
	for _, e := range entries {
		if e.SourcePath != e.TargetPath {
			moves = append(moves, RenameMove{From: e.SourcePath, To: e.TargetPath})
		}
	}
	switch {
	case len(moves) == 0:
		return RenamePreview{TargetBookID: target.ID, Reason: "files already at their target paths"}
	case hasCheckpoint(mfs.db, target.ID, phaseRename):
		return RenamePreview{TargetBookID: target.ID, Reason: "rename checkpoint already set for this book", Moves: moves}
	case organizer.ApplyRenameBlockedReadOnly(mfs.db, target.ID, targetPathsOf(entries)):
		// A different file holds a planned target under a collision the
		// resolver could not settle, and it has not changed since. The rename
		// would be skipped after the DB apply, so the names would never match.
		reason := "blocked by a recorded rename failure"
		return RenamePreview{TargetBookID: target.ID, Reason: reason, Moves: moves, Blocking: reason}
	}
	return RenamePreview{WouldRename: true, TargetBookID: target.ID, Moves: moves}
}

func (mfs *Service) previewAuthorName(book *database.Book) string {
	if book.Author != nil && book.Author.Name != "" {
		return book.Author.Name
	}
	if book.AuthorID != nil {
		if a, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && a != nil {
			return a.Name
		}
	}
	return ""
}

func (mfs *Service) previewSeriesName(book *database.Book) string {
	if book.Series != nil && book.Series.Name != "" {
		return book.Series.Name
	}
	if book.SeriesID != nil {
		if s, err := mfs.db.GetSeriesByID(*book.SeriesID); err == nil && s != nil {
			return s.Name
		}
	}
	return ""
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// truncate keeps descriptions readable in a report of thousands of rows.
func truncate(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
