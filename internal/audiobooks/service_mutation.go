// file: internal/audiobooks/service_mutation.go
// version: 1.14.0
// guid: e7b1f6a5-b8c9-0d12-ce3f-4a5b6c7d8e9f
// last-edited: 2026-10-03

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/fileops"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// errAlreadySoftDeleted is returned by DeleteAudiobook's soft-delete when the
// row, as re-read under its write stripe, is already deleted.
var errAlreadySoftDeleted = errors.New("audiobook already soft deleted")

// ErrInvalidAudiobookUpdate marks an UpdateAudiobook request that asks for
// something the endpoint does not do. Nothing is written for it; the handler
// answers 400 with the error text.
var ErrInvalidAudiobookUpdate = errors.New("invalid audiobook update")

// authorOverrideClears reports whether an author_name override asks for the
// author to be removed: a value that is JSON null or trims to "".
func authorOverrideClears(o OverridePayload) bool {
	if o.Clear || len(o.Value) == 0 {
		return false
	}
	switch v := decodeRawValue(o.Value).(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(v) == ""
	}
	return false
}

// UpdateAudiobook updates an audiobook with new metadata and handles overrides.
//
// "Sent" means the client named the field: req.Sent(key), from the request's
// raw payload keys and its overrides. It is the only test of that anywhere
// below. req.Updates carries the parsed values and nothing else; until
// 2026-10-03 it was pre-filled from the stored row, so a field the client
// never sent looked sent whenever the book had a value, and a title-only PUT
// rewrote the narrator junction, collapsed co-authors and rewrote the raw
// series position.
//
// Nothing outside the book row is written until the row commits: override
// history, the book_authors join and the book_narrators junction all follow
// the successful ModifyBook, so a refused or failed edit leaves no trace.
func (svc *AudiobookService) UpdateAudiobook(ctx context.Context, id string, req *UpdateAudiobookRequest) (*database.Book, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	currentBook, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, err
	}
	if currentBook == nil {
		return nil, fmt.Errorf("audiobook not found")
	}
	// before is the row as read. Everything below edits currentBook (with
	// author/series/narrator resolution and an os.Stat in between); the save
	// then merges only the fields that changed relative to before onto the row
	// re-read under the book's write lock, so a concurrent metadata apply's
	// fields survive this save.
	before, err := database.SnapshotBook(currentBook)
	if err != nil {
		return nil, err
	}
	// beforeAuthor/beforeSeries: the names a GET shows for the book as read
	// (the author by database.ShownCreditName, as the GET builds it).
	// authorLinked: the book has an author link (AuthorID or join rows).
	beforeAuthor, authorLinked := svc.shownAuthorName(before)
	_, beforeSeries := resolveAuthorAndSeriesNames(svc.store, before)

	// A book's author cannot be removed from this endpoint (see the
	// author_name resolution below). An author_name override of "" -- the
	// web editor's form of a clear -- used to lock the field at "" and record
	// a history row while the author stayed, so the lock claimed a change
	// that never happened. Refuse it before anything is written, when the
	// book has an author to clear. On a book with none (the user typed in
	// the empty box and deleted it again) it changes nothing, so it is
	// dropped: no lock, no history.
	skipOverride := map[string]bool{}
	if o, ok := req.Updates.Overrides[database.FieldKeyAuthorName]; ok && authorOverrideClears(o) {
		if beforeAuthor != "" || authorLinked {
			return nil, fmt.Errorf("%w: the author cannot be cleared; set a different author", ErrInvalidAudiobookUpdate)
		}
		skipOverride[database.FieldKeyAuthorName] = true
	}

	// overrideRecorded: history fields the override bookkeeping below
	// recorded a row for ("override", "user_edit"); the column diff after the
	// save skips them so one edit is not listed twice.
	overrideRecorded := map[string]bool{}
	noteOverride := func(lockKey string) {
		if lockKey == database.FieldKeySeriesName {
			overrideRecorded[database.HistoryFieldSeries] = true
			return
		}
		overrideRecorded[lockKey] = true
	}
	// pendingHistory: override history rows, written only after the book
	// commits (a row for an edit that then failed would tell a queued apply
	// the user changed a field they did not).
	type overrideRow struct {
		field    string
		old, new any
	}
	var pendingHistory []overrideRow
	recordOverride := func(field string, old, new any) {
		if fmt.Sprintf("%v", old) != fmt.Sprintf("%v", new) {
			pendingHistory = append(pendingHistory, overrideRow{field: field, old: old, new: new})
		}
	}

	now := time.Now()
	sent := req.Sent
	sentString := func(key string, v *string) bool { return v != nil && sent(key) }

	// Apply updates from req.Updates to currentBook first
	// This ensures extractors can read the updated fields
	if req.Updates.Title != "" {
		currentBook.Title = req.Updates.Title
	}
	if req.Updates.Format != "" {
		currentBook.Format = req.Updates.Format
	}
	if req.Updates.FilePath != "" && req.Updates.FilePath != currentBook.FilePath {
		// Validate the new path is inside an allowed directory before accepting
		// the change (go/path-injection), then confirm it exists.
		absNewPath, err := filepath.Abs(req.Updates.FilePath)
		if err != nil {
			return nil, fmt.Errorf("invalid new path: %w", err)
		}
		importPaths, err := svc.store.GetAllImportPaths()
		if err != nil {
			return nil, fmt.Errorf("failed to check allowed paths: %w", err)
		}
		if !fileops.IsAllowedPath(absNewPath, importPaths) {
			return nil, fmt.Errorf("new path is not in an allowed directory")
		}
		// Validate new file exists before accepting path change.
		// absNewPath is gated by fileops.IsAllowedPath above; CodeQL does not
		// model that custom allow-list barrier, so suppress the false positive.
		if _, err := os.Stat(absNewPath); err != nil { // lgtm[go/path-injection]
			return nil, fmt.Errorf("file does not exist at new path: %s", absNewPath)
		}
		slog.Info("audiobook_service FilePath changed for →", "id", id, "currentBook", currentBook.FilePath, "value2", absNewPath)
		currentBook.FilePath = absNewPath
	}
	// A top-level "" for a field that is already empty (nil or "") is not an
	// edit. The web editor sends description, publisher, language and
	// narrator as "" on EVERY save, whatever the user changed; applying that
	// turned nil into "" (a history row for nothing) and the extractor loop
	// below then locked the field at "", which stops every later metadata
	// fetch from filling it. Such a field is skipped entirely: no apply, no
	// lock, no history. blankNoop holds their lock keys.
	blankNoop := map[string]bool{}
	skipBlank := func(lockKey string, v, current *string) bool {
		if v == nil || strings.TrimSpace(*v) != "" || (current != nil && *current != "") {
			return false
		}
		blankNoop[lockKey] = true
		return true
	}
	if sentString("narrator", req.Updates.Narrator) {
		currentBook.Narrator = req.Updates.Narrator
	}
	if sentString("publisher", req.Updates.Publisher) && !skipBlank(database.FieldKeyPublisher, req.Updates.Publisher, currentBook.Publisher) {
		currentBook.Publisher = req.Updates.Publisher
	}
	if sentString("language", req.Updates.Language) && !skipBlank(database.FieldKeyLanguage, req.Updates.Language, currentBook.Language) {
		currentBook.Language = req.Updates.Language
	}
	if req.Updates.AudiobookReleaseYear != nil && sent("audiobook_release_year") {
		currentBook.AudiobookReleaseYear = req.Updates.AudiobookReleaseYear
	}
	if sentString("isbn10", req.Updates.ISBN10) && !skipBlank(database.FieldKeyISBN10, req.Updates.ISBN10, currentBook.ISBN10) {
		currentBook.ISBN10 = req.Updates.ISBN10
	}
	if sentString("isbn13", req.Updates.ISBN13) && !skipBlank(database.FieldKeyISBN13, req.Updates.ISBN13, currentBook.ISBN13) {
		currentBook.ISBN13 = req.Updates.ISBN13
	}
	// Description, genre, ASIN and series_position were parsed from the
	// top-level payload keys but never copied onto currentBook, so a PUT of
	// any of them -- a set or a clear -- changed nothing, and the extractor
	// below then locked the field at its OLD value. They only worked through
	// "overrides", which ApplyOverrideToPayload writes onto currentBook.
	if sentString("description", req.Updates.Description) && !skipBlank(database.FieldKeyDescription, req.Updates.Description, currentBook.Description) {
		currentBook.Description = req.Updates.Description
	}
	if sentString("genre", req.Updates.Genre) && !skipBlank(database.FieldKeyGenre, req.Updates.Genre, currentBook.Genre) {
		currentBook.Genre = req.Updates.Genre
	}
	if sentString("asin", req.Updates.ASIN) && !skipBlank(database.FieldKeyASIN, req.Updates.ASIN, currentBook.ASIN) {
		currentBook.ASIN = req.Updates.ASIN
	}
	// series_position as sent, raw and int together (an override, applied
	// below, wins). The value is read from the raw payload, not a parsed int:
	// "2.5" keeps its decimal in SeriesPositionRaw, which readers that order
	// or gate by position prefer, and null clears the position.
	if raw, ok := req.RawPayload[database.FieldKeySeriesPosition]; ok {
		applySeriesPosition(currentBook, decodeRawValue(raw))
	}
	if req.Updates.AuthorID != nil && sent("author_id") {
		currentBook.AuthorID = req.Updates.AuthorID
	}
	if req.Updates.SeriesID != nil && sent("series_id") {
		currentBook.SeriesID = req.Updates.SeriesID
	}

	payload := &AudiobookUpdate{
		Book: currentBook,
	}

	// Load and process metadata state
	state, err := svc.loadMetadataState(id)
	if err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to load metadata state", "err", err)
		return nil, fmt.Errorf("failed to load metadata state")
	}
	if state == nil {
		state = map[string]metadataFieldState{}
	}

	// Process overrides
	for field, override := range req.Updates.Overrides {
		if skipOverride[field] {
			continue
		}
		entry := state[field]
		oldOverrideValue := entry.OverrideValue
		if override.Clear {
			entry.OverrideValue = nil
			entry.OverrideLocked = false
			entry.UpdatedAt = now
			recordOverride(field, oldOverrideValue, nil)
		} else {
			if len(override.Value) > 0 {
				val := decodeRawValue(override.Value)
				entry.OverrideValue = val
				entry.OverrideLocked = override.Locked == nil || *override.Locked
				entry.UpdatedAt = now
				ApplyOverrideToPayload(payload, field, val)
				recordOverride(field, oldOverrideValue, val)
			} else if override.Locked != nil {
				entry.OverrideLocked = *override.Locked
				entry.UpdatedAt = now
			}
			if len(override.FetchedValue) > 0 {
				entry.FetchedValue = decodeRawValue(override.FetchedValue)
				if entry.UpdatedAt.IsZero() {
					entry.UpdatedAt = now
				}
			}
		}
		state[field] = entry
	}

	// Narrator, as sent (top-level or override). narratorClear: a narrator
	// "" that has something to clear -- the column, or the book_narrators
	// junction alone. GET fills the narrator from the junction when the
	// column is empty, so the editor shows those names and sends "" when the
	// user deletes them. A "" with nothing to clear is a blankNoop.
	narratorSent := sent(database.FieldKeyNarrator) && payload.Narrator != nil
	narratorClear := false
	if narratorSent && strings.TrimSpace(*payload.Narrator) == "" {
		hasCredit := before.Narrator != nil && *before.Narrator != ""
		if !hasCredit {
			junction, jErr := svc.store.GetBookNarrators(id)
			// An unreadable junction counts as non-empty: clearing an empty
			// junction is harmless, keeping a cleared cast is not.
			hasCredit = jErr != nil || len(junction) > 0
		}
		if hasCredit {
			narratorClear = true
		} else {
			payload.Narrator = before.Narrator
			blankNoop[database.FieldKeyNarrator] = true
		}
	} else if narratorSent && (before.Narrator == nil || *before.Narrator == "") {
		// GET shows a junction-only cast as the narrator ("A & B", the
		// junction names joined), and the editor re-sends that on every save.
		// The same text back is not an edit: no column write, no lock, no
		// junction rewrite. (blankNoop is the set of sent-but-not-edited
		// fields.)
		if shown := svc.junctionNarratorText(id); shown != "" && strings.TrimSpace(*payload.Narrator) == shown {
			payload.Narrator = before.Narrator
			blankNoop[database.FieldKeyNarrator] = true
		}
	}

	// Resolve author by name or ID — auto-split on " & " for multiple authors
	//
	// An author_name that trims to "" is ignored, as if it were not sent: a
	// book's author is not cleared from this endpoint. The web editor sends
	// author_name on EVERY save ("" for a book with no author), so "" cannot
	// be read as a deliberate clear, and it used to half-clear: author_id went
	// nil while the book_authors join (which the organizer trusts) and the
	// embedded Author (which reads prefer) kept the old author.
	//
	// The join rows are resolved here and written after the book commits.
	var resolvedAuthorName string
	var pendingAuthors []database.BookAuthor
	//
	// The value is the effective one: an override's (ApplyOverrideToPayload
	// put it on payload.AuthorName), else the top-level key. Reading only the
	// top-level key locked an override-only author that was never applied.
	authorName := ""
	if v := effectiveString(payload.AuthorName, req.Updates.AuthorName); v != nil && sent(database.FieldKeyAuthorName) && !skipOverride[database.FieldKeyAuthorName] {
		authorName = strings.TrimSpace(*v)
	}
	// The name the book already shows is not an author edit (the editor
	// re-sends it on every save). Re-resolving it would rewrite the join from
	// that one name, collapsing a co-authored book to its primary author, or
	// relink a join-only "A & B" book to a new author named "A & B". Only
	// while the book has an author link (AuthorID or join rows): a name shown
	// from a stale embedded object alone still goes through the lookup, which
	// relinks it.
	authorUnchanged := authorName != "" && authorName == beforeAuthor && authorLinked && !sent("author_id")
	if authorUnchanged {
		resolvedAuthorName = beforeAuthor
	}
	if authorName != "" && !authorUnchanged {
		// Split on " & " to support multiple authors
		authorNames := splitMultipleNames(authorName)
		var bookAuthors []database.BookAuthor
		var primaryAuthorID int
		for i, aName := range authorNames {
			aName = strings.TrimSpace(aName)
			if aName == "" {
				continue
			}
			// Creation gate. This is a direct user edit, so a rejected name
			// is reported rather than silently dropped -- see the error
			// returned below when nothing usable survives.
			normalizedName, nameOK := dedup.CleanAuthorNameForCreation(aName)
			if !nameOK {
				singleLog.Warn("UpdateAudiobook %s: author name %q rejected as unusable",
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(aName))
				continue
			}
			author, err := svc.store.GetAuthorByName(normalizedName)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve author")
			}
			if author == nil {
				author, err = svc.store.CreateAuthor(normalizedName)
				if err != nil {
					return nil, fmt.Errorf("failed to create author")
				}
			}
			role := "author"
			if i > 0 {
				role = "co-author"
			}
			bookAuthors = append(bookAuthors, database.BookAuthor{
				BookID: id, AuthorID: author.ID, Role: role, Position: i,
			})
			if i == 0 {
				primaryAuthorID = author.ID
			}
		}
		// Every part was rejected by the gate. Say so instead of writing
		// AuthorID = 0: that is not "no author", it is a reference to an
		// id no row has, and the caller asked for a specific change that
		// did not happen.
		if len(bookAuthors) == 0 {
			return nil, fmt.Errorf("no usable author name in %q", authorName)
		}
		// Set primary author on the book for backward compat
		payload.AuthorID = &primaryAuthorID
		resolvedAuthorName = authorName // Keep the combined name for display
		pendingAuthors = bookAuthors
	} else if payload.AuthorID != nil {
		if author, err := svc.store.GetAuthorByID(*payload.AuthorID); err == nil && author != nil {
			resolvedAuthorName = author.Name
			// Keep the book_authors join in step with an ID-based author change.
			// The name path above writes BOTH the scalar and the join; this
			// branch historically wrote only the scalar, leaving the join stale
			// -- and the organizer now trusts the join over the scalar (see
			// organizer.authorNameFromJoin), so a stale join would misfile the
			// book under the old author on the next organize. An ID-based set is
			// single-author by definition (same as typing one name), so the join
			// becomes exactly that one author at Position 0.
			//
			// Only when the client SENT author_id with a real positive id: this
			// branch also runs for every edit of a book that has an author, and
			// rewriting the join there collapsed co-authors on a title-only
			// save. Id 0 is the dangling-reference shape the read side already
			// treats as "no author", so it is never written into the join.
			if sent("author_id") && req.Updates.AuthorID != nil && *req.Updates.AuthorID > 0 {
				pendingAuthors = []database.BookAuthor{
					{BookID: id, AuthorID: *req.Updates.AuthorID, Role: "author", Position: 0},
				}
			}
		}
	}

	// Resolve series by name or ID
	//
	// series_name "" (or series_id null) removes the series: the link, the
	// embedded Series display object, and the position, which means nothing
	// without a series. Until 2026-10-03 only SeriesID was nilled; the stale
	// embedded Series survived the save (and the store's preserve-on-nil
	// guard), and every read prefers that object, so the book still showed
	// its series after a 200.
	var resolvedSeriesName string
	seriesName := ""
	// The effective value, as for the author: an override's, else the
	// top-level key's.
	seriesValue := effectiveString(payload.SeriesName, req.Updates.SeriesName)
	seriesNameSent := seriesValue != nil && sent(database.FieldKeySeriesName)
	if seriesNameSent {
		seriesName = strings.TrimSpace(*seriesValue)
	}
	clearSeries := seriesName == "" && (req.Updates.ClearSeries || seriesNameSent)
	// hadSeries: the book showed a series name on GET (same resolver). Only
	// a clear of a series the user could see is a user edit: it wipes the
	// position and is locked below. A SeriesID whose series row is gone shows
	// no name, so the editor sends "" for it like for any series-less book;
	// that link is dropped, but nothing is locked and the position stays.
	// Books hit by the old clear bug (nil SeriesID, stale embedded Series)
	// show the stale name, so a repeat clear repairs them.
	hadSeries := beforeSeries != ""
	seriesUnchanged := seriesName != "" && seriesName == beforeSeries && before.SeriesID != nil && !sent("series_id")
	switch {
	case seriesUnchanged:
		// The series the book already shows is not a series edit (the editor
		// re-sends it on every save). Looking it up by name again could
		// resolve -- or create -- a different series row of the same name
		// (the lookup is scoped by author) and move the book onto it. Only
		// while the book is linked by ID: a name shown from a stale embedded
		// object alone (nil SeriesID) goes through the lookup, which relinks
		// the book; skipping it would let the store drop that object.
		resolvedSeriesName = beforeSeries
	case seriesName != "":
		series, err := svc.store.GetSeriesByName(seriesName, payload.AuthorID)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve series")
		}
		if series == nil {
			series, err = svc.store.CreateSeries(seriesName, payload.AuthorID)
			if err != nil {
				return nil, fmt.Errorf("failed to create series")
			}
		}
		payload.SeriesID = &series.ID
		resolvedSeriesName = series.Name
	case clearSeries:
		// The embedded Series object goes with the link in the store, which
		// keeps Series consistent with SeriesID on every write
		// (PebbleStore.updateBookLockedMode).
		payload.SeriesID = nil
		if hadSeries {
			payload.SeriesSequence = nil
			payload.SeriesPositionRaw = nil
		}
	case payload.SeriesID != nil:
		if series, err := svc.store.GetSeriesByID(*payload.SeriesID); err == nil && series != nil {
			resolvedSeriesName = series.Name
		}
	}

	// Process direct field updates (non-override). Every key written here is a
	// lock key: the extractors are the WRITER side of database.UserLockableFields.
	//
	// Only a field the edit CHANGED is locked: the editor sends every field
	// on every save, and locking each one at its current value froze the
	// whole book against metadata fetches after any edit. A field the client
	// overrode explicitly is handled by the override loop above. An
	// author_name of "" is ignored (see above), so it locks nothing either,
	// even though the author_id fallback resolved the current author's name.
	lockAuthorName := resolvedAuthorName
	if authorName == "" {
		lockAuthorName = ""
	}
	fieldExtractors := userEditFieldExtractors(payload, lockAuthorName, resolvedSeriesName)
	beforeExtractors := userEditFieldExtractors(&AudiobookUpdate{Book: before}, beforeAuthor, beforeSeries)

	for field, extractor := range fieldExtractors {
		if !sent(field) {
			continue
		}
		if _, hasOverride := req.Updates.Overrides[field]; hasOverride {
			slog.Debug("UpdateAudiobook field has explicit override", "field", field)
			continue
		}
		if blankNoop[field] {
			continue
		}
		value, ok := extractor()
		if !ok {
			continue
		}
		if old, oldOK := beforeExtractors[field](); oldOK && fmt.Sprintf("%v", old) == fmt.Sprintf("%v", value) {
			continue // unchanged: not an edit of this field
		}
		entry := state[field]
		oldValue := entry.OverrideValue
		entry.OverrideValue = value
		entry.OverrideLocked = true
		entry.UpdatedAt = now
		state[field] = entry
		recordOverride(field, oldValue, value)
	}

	// Lock a series clear the way a set is locked, so a rescan or a queued
	// metadata apply cannot put the series back. The extractor above skips
	// series_name when it resolves to nothing, so the clear is handled here.
	// Only when the book showed a series: the web editor sends series_name ""
	// on every save, and locking every series-less book it saves would block
	// a later fetch from ever adding one. No override history row here: the
	// column diff after the save records the one "series" row (old name ->
	// ""), and the series_sequence row with it.
	//
	// series_position gets no lock of its own: a locked series_name already
	// protects and blanks the position (lockedColumns, StripLockedFields),
	// and a position lock would outlive a later re-set of the series, so a
	// fetch could never fill the new series' number. A position override
	// left from an earlier edit is dropped and unlocked instead, so its
	// stale number is not re-projected onto the book.
	if clearSeries && hadSeries {
		if _, hasOverride := req.Updates.Overrides[database.FieldKeySeriesName]; !hasOverride {
			entry := state[database.FieldKeySeriesName]
			entry.OverrideValue = ""
			entry.OverrideLocked = true
			entry.UpdatedAt = now
			state[database.FieldKeySeriesName] = entry
		}
		if _, hasOverride := req.Updates.Overrides[database.FieldKeySeriesPosition]; !hasOverride {
			if entry, ok := state[database.FieldKeySeriesPosition]; ok && (entry.OverrideLocked || entry.OverrideValue != nil) {
				entry.OverrideValue = nil
				entry.OverrideLocked = false
				entry.UpdatedAt = now
				state[database.FieldKeySeriesPosition] = entry
			}
		}
	}

	// Process unlock overrides
	for _, field := range req.Updates.UnlockOverrides {
		entry := state[field]
		entry.OverrideLocked = false
		entry.UpdatedAt = now
		state[field] = entry
	}

	// Sync the denormalized Author/Series display objects to the resolved IDs
	// BEFORE persisting. Read paths prefer the embedded object when non-nil and
	// only fall back to a GetAuthorByID/GetSeriesByID lookup when it is nil
	// (see resolveAuthorAndSeriesNames in helpers.go and
	// EnrichAudiobooksWithNames in service_query.go). currentBook — and thus
	// payload.Book — carries the *old* Author/Series struct loaded by
	// GetBookByID above; changing AuthorID/SeriesID here (via the direct
	// author_id/series_id fields at the top, or the name-resolution branches)
	// without refreshing the embedded object would persist a stale name.
	// UpdateBook's preserve-on-nil guard cannot fix this (the stale object is
	// non-nil), so the write side must honor its documented contract of setting
	// BOTH the ID and a fresh object. This mirrors the response-enrichment block
	// below, but must run before UpdateBook so the stored blob is correct too.
	//
	// Only when the link or the name actually moved: an edit that leaves the
	// author or series alone must not rewrite their stored objects (a
	// title-only save used to stamp the series object with the book's
	// author_id).
	if !authorUnchanged && payload.AuthorID != nil && resolvedAuthorName != "" &&
		(!sameIntPtr(payload.AuthorID, before.AuthorID) || before.Author == nil || before.Author.Name != resolvedAuthorName) {
		payload.Book.Author = &database.Author{ID: *payload.AuthorID, Name: resolvedAuthorName}
	}
	if !seriesUnchanged && payload.SeriesID != nil && resolvedSeriesName != "" &&
		(!sameIntPtr(payload.SeriesID, before.SeriesID) || before.Series == nil || before.Series.Name != resolvedSeriesName) {
		payload.Book.Series = &database.Series{ID: *payload.SeriesID, Name: resolvedSeriesName, AuthorID: payload.AuthorID}
	}

	// Save to database: this edit's changed fields only, onto the fresh row.
	// pre is the stored row this save merged onto, taken under the book's
	// write lock, so the history below credits this edit only with what it
	// changed -- not with a concurrent apply's fields.
	var pre *database.Book
	updatedBook, err := svc.store.ModifyBook(id, func(fresh *database.Book) error {
		snap, sErr := database.SnapshotBook(fresh)
		if sErr != nil {
			return sErr
		}
		pre = snap
		_, mErr := database.MergeBookChanges(fresh, before, payload.Book)
		return mErr
	})
	if err != nil {
		return nil, err
	}
	if updatedBook == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// The edit has committed. Everything below is written only now, and a
	// failure in it is logged, not returned: the edit itself landed.
	//
	// Known race: these join writes run outside the book's write stripe.
	// The store's per-book lock (PebbleStore.lockBook) is not exported, and
	// ModifyBook's callback cannot call the store's join writers (they would
	// re-enter the stripe). A concurrent writer that changes the same join
	// between the commit and these writes can be overwritten by them, or
	// overwrite them. Tracked in todo.d (EDIT-JOIN-WRITE-STRIPE).

	// The book_authors join resolved above.
	if pendingAuthors != nil {
		if err := svc.store.SetBookAuthors(id, pendingAuthors); err != nil {
			singleLog.Warn("UpdateAudiobook %s: the edit was saved but book_authors was not updated: %v",
				logger.SanitizeLogValue(id), err)
		}
	}

	// The narrator junction: only when the client sent the narrator and the
	// edit changed it (or cleared a junction-only cast).
	if narratorSent && !blankNoop[database.FieldKeyNarrator] && pre != nil {
		svc.syncEditedNarratorJunction(id, pre, updatedBook, narratorClear)
	}

	// Override history rows, before the column diff so they are never newer
	// than the "manual" rows that follow.
	if len(pendingHistory) > 0 {
		mss := newMetadataStateSvc(svc.store)
		for _, r := range pendingHistory {
			if mss.recordChange(id, r.field, "override", "user_edit", r.old, r.new) {
				noteOverride(r.field)
			}
		}
	}

	// A "manual" history row for EVERY field this edit changed, clears
	// included (database.RecordBookEditHistory). A queued metadata apply reads
	// this history to refuse overwriting a user's later edit; a changed field
	// with no row would be overwritten silently.
	//
	// Stamped AFTER the write commits (not with `now`, taken before it): a
	// queued apply's mark read between the two would otherwise sort after
	// these rows and miss this edit.
	//
	// The edit HAS committed, so a history failure does not abort the rest:
	// the metadata state, cache invalidation and the handler's file write-back
	// still run, and the request succeeds (a 500 for a landed edit would make
	// the user retry an edit that then changes, and records, nothing). The
	// failure is logged at Error, as batch.UpdateAudiobooks reports it.
	if pre != nil {
		if _, herr := database.RecordBookEditHistory(svc.store, pre, updatedBook,
			database.ChangeTypeManual, "manual", time.Now(), overrideRecorded); herr != nil {
			editHistoryLog.Error("UpdateAudiobook %s: the edit was saved but its change history was not fully recorded "+
				"(a queued metadata apply may not see it): %v", logger.SanitizeLogValue(id), herr)
		}
	}

	// Save metadata state
	if err := svc.saveMetadataState(id, state); err != nil {
		slog.Info("[ERROR] UpdateAudiobook failed to save metadata state", "err", err)
		return nil, fmt.Errorf("failed to persist metadata state")
	}

	svc.InvalidateBookCaches()

	// Enrich response with resolved names (not for an unchanged name: the
	// stored objects stand, and the shown name may be a join "A & B" that is
	// no author's name).
	if !authorUnchanged && resolvedAuthorName != "" && updatedBook.AuthorID != nil {
		updatedBook.Author = &database.Author{ID: *updatedBook.AuthorID, Name: resolvedAuthorName}
	}
	if !seriesUnchanged && resolvedSeriesName != "" && updatedBook.SeriesID != nil {
		updatedBook.Series = &database.Series{ID: *updatedBook.SeriesID, Name: resolvedSeriesName, AuthorID: payload.AuthorID}
	}

	return updatedBook, nil
}

// syncEditedNarratorJunction brings book_narrators in step with a narrator
// edit that has committed. pre is the row the edit merged onto, written is
// the row it wrote.
//
//   - A clear (cleared=true) empties the junction: the store's own sync runs
//     only for a non-empty credit, and ABS prefers the junction.
//   - An unchanged credit leaves the junction alone. Re-sending the same
//     narrator (the editor does it on every save) used to rewrite the
//     junction from a raw split, undoing the store's cleaned cast and
//     creating junk narrators such as "Narrated by X".
//   - A changed credit gets the cast util.CleanNarratorCredit reads from it,
//     the same cast the store's sync writes ("Narrated by" stripped, the
//     book's own authors dropped when a real narrator remains). A credit
//     naming only the book's authors is a self-read: those names go in as
//     narrators. A credit that is not a list of people empties the junction,
//     so the old cast does not outlive the edit.
//
// Narrator entities are created here, after the commit, and only for names
// that go into the junction.
func (svc *AudiobookService) syncEditedNarratorJunction(id string, pre, written *database.Book, cleared bool) {
	credit := ""
	if written.Narrator != nil {
		credit = *written.Narrator
	}
	preCredit := ""
	if pre.Narrator != nil {
		preCredit = *pre.Narrator
	}
	var names []string
	switch {
	case cleared:
	case credit == preCredit:
		return
	default:
		authors, aErr := database.LiveBookAuthorNames(svc.store, written)
		if aErr != nil {
			singleLog.Warn("UpdateAudiobook %s: narrator junction not updated, book authors unreadable: %v",
				logger.SanitizeLogValue(id), aErr)
			return
		}
		people, verdict := util.CleanNarratorCredit(credit, authors)
		switch verdict {
		case util.NarratorCreditPeople:
			names = people
		case util.NarratorCreditAllAuthors:
			// A self-read. The same cleaning with no authors to drop keeps
			// the author names and still strips "Narrated by" and the like,
			// so no junk narrator is created from the raw credit.
			names, _ = util.CleanNarratorCredit(credit, nil)
		}
	}
	var rows []database.BookNarrator
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		narrator, err := svc.store.GetNarratorByName(name)
		if err != nil || narrator == nil {
			narrator, err = svc.store.CreateNarrator(name)
			if err != nil || narrator == nil {
				singleLog.Warn("UpdateAudiobook %s: narrator %q not created: %v",
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(name), err)
				continue
			}
		}
		role := "narrator"
		if len(rows) > 0 {
			role = "co-narrator"
		}
		rows = append(rows, database.BookNarrator{BookID: id, NarratorID: narrator.ID, Role: role, Position: len(rows)})
	}
	if err := svc.store.SetBookNarrators(id, rows); err != nil {
		singleLog.Warn("UpdateAudiobook %s: the edit was saved but book_narrators was not updated: %v",
			logger.SanitizeLogValue(id), err)
	}
}

// junctionNarratorText is the narrator a GET shows for a book whose
// Narrator column is empty: its book_narrators names joined with " & "
// (enrichBookForResponse). "" when the junction is empty or unreadable.
func (svc *AudiobookService) junctionNarratorText(id string) string {
	rows, err := svc.store.GetBookNarrators(id)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		// A row whose narrator is gone is skipped, as the GET skips it.
		if n, nErr := svc.store.GetNarratorByID(r.NarratorID); nErr == nil && n != nil {
			names = append(names, n.Name)
		}
	}
	return database.ShownCreditName("", names)
}

// shownAuthorName is the author a GET shows for book (database.
// ShownCreditName over the resolved primary author and the book_authors
// names, the way enrichBookForResponse builds it), and whether the book has
// an author link at all (an AuthorID, dangling or not, or join rows).
func (svc *AudiobookService) shownAuthorName(book *database.Book) (string, bool) {
	primary, _ := resolveAuthorAndSeriesNames(svc.store, book)
	linked := book.AuthorID != nil
	rows, err := svc.store.GetBookAuthors(book.ID)
	if err != nil {
		return primary, linked
	}
	linked = linked || len(rows) > 0
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		// A row whose author is gone is skipped, as the GET skips it.
		if a, aErr := svc.store.GetAuthorByID(r.AuthorID); aErr == nil && a != nil {
			names = append(names, a.Name)
		}
	}
	return database.ShownCreditName(primary, names), linked
}

// effectiveString is an edit's value for a field the request can carry both
// at top level and as an override: the override's when there is one (it is
// applied after the top-level keys, as for every field), else the top-level
// value. nil when neither is present.
func effectiveString(override, topLevel *string) *string {
	if override != nil {
		return override
	}
	return topLevel
}

// applySeriesPosition sets a book's series position from a value as a client
// sent it: a JSON number (2 or 2.5), a numeric string ("2.5"), or null / ""
// to clear it. SeriesPositionRaw keeps the value as sent, decimal included;
// SeriesSequence gets its whole part. Anything else (a non-numeric string) is
// ignored. It reports whether the value was applied.
func applySeriesPosition(b *database.Book, value any) bool {
	var f float64
	var raw string
	switch v := value.(type) {
	case nil:
		b.SeriesSequence, b.SeriesPositionRaw = nil, nil
		return true
	case float64:
		f, raw = v, strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		f, raw = float64(v), strconv.Itoa(v)
	case string:
		raw = strings.TrimSpace(v)
		if raw == "" {
			b.SeriesSequence, b.SeriesPositionRaw = nil, nil
			return true
		}
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return false
		}
		f = parsed
	default:
		return false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return false
	}
	seq := int(math.Floor(f))
	b.SeriesSequence, b.SeriesPositionRaw = &seq, &raw
	return true
}

// DeleteAudiobook deletes an audiobook (soft or hard delete)
func (svc *AudiobookService) DeleteAudiobook(ctx context.Context, id string, opts *DeleteAudiobookOptions) (map[string]any, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}

	if opts == nil {
		opts = &DeleteAudiobookOptions{}
	}

	// Get the book first to access its hash.
	// PebbleStore returns nil, nil for a not-found book; check both.
	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		return nil, fmt.Errorf("audiobook not found")
	}

	// If soft delete requested, mark for deletion instead of hard delete
	if opts.SoftDelete {
		if book.IsSoftDeleted() ||
			(book.LibraryState != nil && strings.EqualFold(*book.LibraryState, "deleted")) {
			return nil, fmt.Errorf("audiobook already soft deleted")
		}

		// Set the three deletion columns on the row as it stands under the
		// book's write stripe, re-checking "already deleted" there, so a
		// concurrent writer's change is not reverted by a whole-struct replace
		// of the read above.
		now := time.Now()
		updated, err := svc.store.ModifyBook(id, func(row *database.Book) error {
			if row.IsSoftDeleted() ||
				(row.LibraryState != nil && strings.EqualFold(*row.LibraryState, "deleted")) {
				return errAlreadySoftDeleted
			}
			row.MarkedForDeletion = new(true)
			row.MarkedForDeletionAt = &now
			// The label overwrites the state; remember it for a restore.
			database.RememberLibraryStateBeforeTrash(row)
			row.LibraryState = new("deleted")
			return nil
		})
		if err != nil {
			return nil, err
		}
		if updated == nil {
			return nil, fmt.Errorf("audiobook not found")
		}
		book = updated
		// A soft-deleted primary hands its group's flag on.
		svc.handOffPrimary(updated)

		// Optionally block the hash
		blocked := false
		if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
			if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - soft delete"); err != nil {
				slog.Warn("failed to block hash during soft delete", "err", err)
			} else {
				blocked = true
			}
		}

		// Remove the book's tracks from iTunes. Soft-delete is treated
		// as "user no longer wants this in their library" and the
		// iTunes side should reflect that immediately.
		svc.enqueueITunesRemovesForBook(id, book)

		svc.InvalidateBookCaches()
		return map[string]any{
			"message":     "audiobook soft deleted",
			"blocked":     blocked,
			"soft_delete": true,
		}, nil
	}

	// Hard delete path
	//
	// Refuse a book that still owns book_file rows BEFORE any side effect
	// (hash block, iTunes removes). DeleteBook never deletes those rows, so a
	// hard delete would orphan every one of them; it refuses too
	// (database.ErrBookOwnsFiles), but only after the hash block below had
	// already been written. Fail closed on a read error.
	if owned, ownErr := svc.store.GetBookFiles(id); ownErr != nil {
		return nil, fmt.Errorf("hard delete %s: cannot read its book_file rows: %w", id, ownErr)
	} else if len(owned) > 0 {
		return nil, fmt.Errorf("hard delete %s: %w (%d row(s)); soft-delete it instead, or move its files to another book first",
			id, database.ErrBookOwnsFiles, len(owned))
	}

	// Optionally block the hash before deleting
	blocked := false
	if opts.BlockHash && book.FileHash != nil && *book.FileHash != "" {
		if err := svc.store.AddBlockedHash(*book.FileHash, "User deleted - prevent reimport"); err != nil {
			slog.Warn("failed to block hash before delete", "err", err)
			// Continue with delete even if blocking fails
		} else {
			blocked = true
		}
	}

	// Capture iTunes PIDs BEFORE the DB row vanishes so we can
	// enqueue iTunes removes after the hard delete succeeds.
	var itunesPIDs []string
	if svc.itunesEnqueuer != nil {
		itunesPIDs = svc.collectITunesPIDsForBook(id, book)
	}

	if err := svc.store.DeleteBook(id); err != nil {
		if err.Error() == "book not found" {
			return nil, fmt.Errorf("audiobook not found")
		}
		return nil, err
	}
	// A hard-deleted primary hands its group's flag on too; book is the row
	// read before the delete, so it still names the group.
	svc.handOffPrimary(book)

	if svc.itunesEnqueuer != nil {
		for _, pid := range itunesPIDs {
			svc.itunesEnqueuer.EnqueueRemove(pid)
		}
	}

	svc.InvalidateBookCaches()
	return map[string]any{
		"message": "audiobook deleted",
		"blocked": blocked,
	}, nil
}

// collectITunesPIDsForBook returns every PID stored on the book's
// book_files plus the legacy Book.ITunesPersistentID field. Used by
// hard-delete to pre-capture PIDs before the row is gone, and by the
// orphan-cleanup endpoint.
func (svc *AudiobookService) collectITunesPIDsForBook(bookID string, book *database.Book) []string {
	pids := []string{}
	files, _ := svc.store.GetBookFiles(bookID)
	for _, f := range files {
		if f.ITunesPersistentID != "" {
			pids = append(pids, f.ITunesPersistentID)
		}
	}
	if book != nil && book.ITunesPersistentID != nil && *book.ITunesPersistentID != "" {
		pids = append(pids, *book.ITunesPersistentID)
	}
	return pids
}

// enqueueITunesRemovesForBook is a soft-delete helper: it pulls the
// PIDs and enqueues each via the wired batcher. No-op if the batcher
// isn't wired.
func (svc *AudiobookService) enqueueITunesRemovesForBook(bookID string, book *database.Book) {
	if svc.itunesEnqueuer == nil {
		return
	}
	for _, pid := range svc.collectITunesPIDsForBook(bookID, book) {
		svc.itunesEnqueuer.EnqueueRemove(pid)
	}
}

// userEditFieldExtractors is the WRITER side of the field-lock vocabulary. Each
// key here becomes a MetadataFieldState row with OverrideLocked=true when the
// user edits that field directly (UpdateAudiobook), and that row is what every
// guard -- the scanner's rescan overlay, every metafetch apply path, the bulk
// fetch handler -- consults through database.LockedUserFields. The keys are the
// database.FieldKey* constants so the writer and the readers cannot drift: for
// two weeks in 2026-08 the scanner guarded "author"/"series"/"series_sequence"
// while this map wrote author_name/series_name/series_position, and every
// curated author, series and position was clobbered on rescan.
//
// TestUserEditFieldExtractorsAreInTheLockVocabulary pins that every key here is
// in database.UserLockableFields.
func userEditFieldExtractors(payload *AudiobookUpdate, resolvedAuthorName, resolvedSeriesName string) map[string]func() (any, bool) {
	return map[string]func() (any, bool){
		database.FieldKeyTitle: func() (any, bool) {
			return payload.Title, true
		},
		database.FieldKeyAuthorName: func() (any, bool) {
			if resolvedAuthorName == "" {
				return nil, false
			}
			return resolvedAuthorName, true
		},
		database.FieldKeySeriesName: func() (any, bool) {
			if resolvedSeriesName == "" {
				return nil, false
			}
			return resolvedSeriesName, true
		},
		database.FieldKeyNarrator: func() (any, bool) {
			if payload.Narrator == nil {
				return nil, false
			}
			return *payload.Narrator, true
		},
		database.FieldKeyPublisher: func() (any, bool) {
			if payload.Publisher == nil {
				return nil, false
			}
			return *payload.Publisher, true
		},
		database.FieldKeyLanguage: func() (any, bool) {
			if payload.Language == nil {
				return nil, false
			}
			return *payload.Language, true
		},
		database.FieldKeyAudiobookReleaseYear: func() (any, bool) {
			if payload.AudiobookReleaseYear == nil {
				return nil, false
			}
			return *payload.AudiobookReleaseYear, true
		},
		database.FieldKeyISBN10: func() (any, bool) {
			if payload.ISBN10 == nil {
				return nil, false
			}
			return *payload.ISBN10, true
		},
		database.FieldKeyISBN13: func() (any, bool) {
			if payload.ISBN13 == nil {
				return nil, false
			}
			return *payload.ISBN13, true
		},
		database.FieldKeyASIN: func() (any, bool) {
			if payload.ASIN == nil {
				return nil, false
			}
			return *payload.ASIN, true
		},
		database.FieldKeyGenre: func() (any, bool) {
			if payload.Genre == nil {
				return nil, false
			}
			return *payload.Genre, true
		},
		database.FieldKeyDescription: func() (any, bool) {
			if payload.Description == nil {
				return nil, false
			}
			return *payload.Description, true
		},
		database.FieldKeySeriesPosition: func() (any, bool) {
			// The raw position when it carries a decimal the int drops, so
			// the lock holds the value as entered ("2.5", not 2).
			if payload.SeriesPositionRaw != nil {
				if raw := strings.TrimSpace(*payload.SeriesPositionRaw); raw != "" {
					if _, err := strconv.Atoi(raw); err != nil {
						return raw, true
					}
				}
			}
			if payload.SeriesSequence == nil {
				return nil, false
			}
			return *payload.SeriesSequence, true
		},
	}
}

// ApplyOverrideToPayload applies an override value to the update payload. The
// field names are the lock vocabulary (database.FieldKey*); a key this switch
// does not handle is stored as state but not projected onto the row.
func ApplyOverrideToPayload(payload *AudiobookUpdate, field string, value any) {
	switch field {
	case database.FieldKeyTitle:
		if v, ok := value.(string); ok {
			payload.Title = v
		}
	case database.FieldKeyAuthorName:
		if v, ok := value.(string); ok {
			payload.AuthorName = &v
		}
	case database.FieldKeySeriesName:
		if v, ok := value.(string); ok {
			payload.SeriesName = &v
		}
	case database.FieldKeyNarrator:
		if v, ok := value.(string); ok {
			payload.Narrator = new(v)
		}
	case database.FieldKeyPublisher:
		if v, ok := value.(string); ok {
			payload.Publisher = new(v)
		}
	case database.FieldKeyLanguage:
		if v, ok := value.(string); ok {
			payload.Language = new(v)
		}
	case database.FieldKeyAudiobookReleaseYear:
		switch v := value.(type) {
		case float64:
			year := int(v)
			payload.AudiobookReleaseYear = &year
		case int:
			year := v
			payload.AudiobookReleaseYear = &year
		}
	case database.FieldKeyISBN10:
		if v, ok := value.(string); ok {
			payload.ISBN10 = new(v)
		}
	case database.FieldKeyISBN13:
		if v, ok := value.(string); ok {
			payload.ISBN13 = new(v)
		}
	case database.FieldKeyASIN:
		if v, ok := value.(string); ok {
			payload.ASIN = new(v)
		}
	case database.FieldKeyGenre:
		if v, ok := value.(string); ok {
			payload.Genre = new(v)
		}
	case database.FieldKeyDescription:
		if v, ok := value.(string); ok {
			payload.Description = new(v)
		}
	case database.FieldKeySeriesPosition:
		// JSON numbers decode as float64; the UI also sends the position as
		// a string ("3", "2.5"). Both the raw position (decimal kept) and the
		// int are set; null or "" clears both (applySeriesPosition).
		applySeriesPosition(payload.Book, value)
	}
}

// editHistoryLog reports a user edit whose change history was not recorded.
var editHistoryLog = logger.New("audiobooks.edit-history")
