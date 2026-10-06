// file: internal/audiobooks/trash_progress.go
// version: 1.4.0
// guid: 98f1136e-e723-43fd-9d77-fab344a4aa45
// last-edited: 2026-10-06

package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// Trash books that hold users' listening state (owner decision 2026-10-05).
//
// The nightly purge carries such a book's state to a live version-group
// sibling and then purges it (PurgeSoftDeletedBooks). With no live sibling it
// keeps the book; the trash listing flags it (TrashProgress) and the owner
// can restore it or drop its progress with it (DiscardProgressAndPurge).

var trashLog = logger.New("audiobooks.trash")

// purgeKind is what happened to one book in a purge pass.
type purgeKind int

const (
	purgeFailed purgeKind = iota
	purgePurged
	purgeOwnsFiles
	purgeCarried
	purgeKeptHasProgress
	purgeCarryFailed
)

// purgeOutcome is one book's result in a purge pass.
type purgeOutcome struct {
	kind         purgeKind
	filesDeleted int
	errs         []string
}

// add folds one book's outcome into r. The caller serializes calls.
func (r *PurgeResult) add(bookID string, o purgeOutcome) {
	r.FilesDeleted += o.filesDeleted
	r.Errors = append(r.Errors, o.errs...)
	switch o.kind {
	case purgePurged:
		r.Purged++
	case purgeCarried:
		r.Purged++
		r.CarriedToSibling++
		r.CarriedToSiblingIDs = append(r.CarriedToSiblingIDs, bookID)
	case purgeOwnsFiles:
		r.SkippedOwnsFiles++
		r.SkippedOwnsFilesIDs = append(r.SkippedOwnsFilesIDs, bookID)
	case purgeKeptHasProgress:
		r.KeptHasProgress++
		r.KeptHasProgressIDs = append(r.KeptHasProgressIDs, bookID)
	case purgeCarryFailed:
		r.CarryFailed++
		r.CarryFailedIDs = append(r.CarryFailedIDs, bookID)
	case purgeFailed:
		// counted by its Errors lines
	}
}

// sortIDs orders the ID lists and errors, which the worker pool fills in no
// particular order, so a result reads the same run to run.
func (r *PurgeResult) sortIDs() {
	slices.Sort(r.SkippedOwnsFilesIDs)
	slices.Sort(r.CarriedToSiblingIDs)
	slices.Sort(r.KeptHasProgressIDs)
	slices.Sort(r.CarryFailedIDs)
	slices.Sort(r.Errors)
}

// purgePartitions splits the soft-deleted books into disjoint work units:
// every book of one version group in one unit, each ungrouped book alone.
// A purge worker takes a whole unit, so two workers never carry state into
// the same group or delete from it at once. Units keep the listing's order.
func purgePartitions(books []database.Book) [][]database.Book {
	index := map[string]int{}
	var parts [][]database.Book
	for _, b := range books {
		key := "book:" + b.ID
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			key = "group:" + *b.VersionGroupID
		}
		if i, ok := index[key]; ok {
			parts[i] = append(parts[i], b)
			continue
		}
		index[key] = len(parts)
		parts = append(parts, []database.Book{b})
	}
	return parts
}

// liveSiblingFor picks the member of book's version group that its users'
// state may be carried to (merge.ListedLiveSibling): only one Audiobookshelf
// lists, never a hidden copy -- state carried onto a book ABS does not list
// (an imported or iTunes-only copy, a non-primary version, a quarantined
// one) is stranded where the user cannot see it, and the trashed row that
// showed "has progress" would be gone (the same rule as reconcile's
// SkippedKeepNotListed). Among several, the lowest id. "" when the book has
// no group or no listed member: the purge then keeps it.
func (svc *AudiobookService) liveSiblingFor(book *database.Book) (string, error) {
	return merge.ListedLiveSibling(svc.store, book)
}

// TrashProgressInfo is what the trash listing shows about a book's users'
// listening state.
type TrashProgressInfo struct {
	// HasProgress is merge.UserStateProbe.Has: the same test the purge uses
	// to decide a book cannot simply be deleted (bookmarks included), so the
	// flag and the purge never disagree.
	HasProgress bool `json:"has_progress"`
	// Summary is a short description per user, e.g. "reader: finished" or
	// "reader: 42%, at 1:02:03". Empty when HasProgress is false. The
	// service fills it for every user; the HTTP handler narrows it to the
	// viewer's own (ForViewer) unless the viewer may manage users.
	Summary string `json:"progress_summary,omitempty"`
	// OtherUsers is how many other users' progress ForViewer left out of
	// Summary.
	OtherUsers int `json:"progress_other_users,omitempty"`
	// Unknown is set when the state could not be read; HasProgress is then
	// false but means "not known", not "none".
	Unknown bool `json:"progress_unknown,omitempty"`
	// ListedCopyID names the version of the book Audiobookshelf lists that
	// the purge would carry the progress to (merge.ListedLiveSibling). Empty
	// when there is none: the purge then keeps the book. Only looked up for
	// a book with progress.
	ListedCopyID string `json:"listed_copy_id,omitempty"`
	// ListedCopyUnknown: whether such a version exists could not be read.
	ListedCopyUnknown bool `json:"listed_copy_unknown,omitempty"`
	// PurgeEligible: the nightly purge would take this book now (it is past
	// the configured retention, PurgeSoftDeletedAfterDays, and the nightly
	// purge is on). Only computed for a book with progress.
	PurgeEligible bool `json:"purge_eligible,omitempty"`
	// users is the per-user breakdown Summary was built from.
	users []userProgressLine
}

// userProgressLine is one user's part of a progress summary.
type userProgressLine struct {
	userID, line string
}

// ForViewer narrows the summary to what the viewer may see: their own
// progress line, with OtherUsers counting everyone else's, unless seeAll
// (the viewer may manage users), which keeps every user's line.
func (i TrashProgressInfo) ForViewer(viewerID string, seeAll bool) TrashProgressInfo {
	if seeAll || len(i.users) == 0 {
		return i
	}
	out := i
	out.Summary, out.OtherUsers = "", 0
	var own []string
	for _, u := range i.users {
		if viewerID != "" && u.userID == viewerID {
			own = append(own, u.line)
			continue
		}
		out.OtherUsers++
	}
	out.Summary = strings.Join(own, "; ")
	return out
}

// TrashProgress reports, for each book id, whether any user has listening
// state on it and a short summary of that state, and, for a book that has
// some, whether a version Audiobookshelf lists exists to carry it to and
// whether the nightly purge would take the book now. A read that fails
// marks the book Unknown rather than failing the whole listing.
func (svc *AudiobookService) TrashProgress(ctx context.Context, bookIDs []string) (map[string]TrashProgressInfo, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	out := make(map[string]TrashProgressInfo, len(bookIDs))
	if len(bookIDs) == 0 {
		return out, nil
	}
	probe, err := merge.NewUserStateProbe(svc.store)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users, err := svc.store.ListUsers()
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	for _, id := range bookIDs {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		has, err := probe.Has(id)
		if err != nil {
			trashLog.Warn("trash listing: cannot read listening state on %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
			out[id] = TrashProgressInfo{Unknown: true}
			continue
		}
		if !has {
			out[id] = TrashProgressInfo{}
			continue
		}
		lines, err := svc.progressSummary(users, id)
		if err != nil {
			trashLog.Warn("trash listing: cannot summarize listening state on %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		}
		info := TrashProgressInfo{HasProgress: true, users: lines}
		parts := make([]string, len(lines))
		for i := range lines {
			parts[i] = lines[i].line
		}
		info.Summary = strings.Join(parts, "; ")
		svc.fillCarryOutlook(id, &info)
		out[id] = info
	}
	return out, nil
}

// fillCarryOutlook sets info's ListedCopyID / ListedCopyUnknown and
// PurgeEligible for the trashed book id.
func (svc *AudiobookService) fillCarryOutlook(id string, info *TrashProgressInfo) {
	book, err := svc.store.GetBookByID(id)
	if err != nil || book == nil {
		info.ListedCopyUnknown = true
		if err != nil {
			trashLog.Warn("trash listing: cannot read %s to look for a listed copy: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		}
		return
	}
	info.PurgeEligible = purgeEligible(book, time.Now())
	sib, err := svc.liveSiblingFor(book)
	if err != nil {
		info.ListedCopyUnknown = true
		trashLog.Warn("trash listing: cannot look for a listed copy of %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		return
	}
	info.ListedCopyID = sib
}

// purgeEligible reports whether the nightly purge (runAutoPurgeSoftDeleted:
// on when PurgeSoftDeletedAfterDays > 0, taking books trashed longer ago
// than that) would take book at now.
func purgeEligible(book *database.Book, now time.Time) bool {
	days := config.AppConfig.PurgeSoftDeletedAfterDays
	if days <= 0 || book.MarkedForDeletionAt == nil {
		return false
	}
	return book.MarkedForDeletionAt.Before(now.AddDate(0, 0, -days))
}

// progressSummary describes each user's state on bookID in a few words,
// one line per user who has any ("name: ..."), bookmarks included.
func (svc *AudiobookService) progressSummary(users []database.User, bookID string) ([]userProgressLine, error) {
	var lines []userProgressLine
	marks := svc.bookmarkCounter(bookID)
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		st, err := svc.store.GetUserBookState(u.ID, bookID)
		if err != nil {
			return lines, err
		}
		pos, err := svc.store.ListUserPositionsForBook(u.ID, bookID)
		if err != nil {
			return lines, err
		}
		n, err := marks(u.ID)
		if err != nil {
			return lines, err
		}
		if d := describeUserState(st, pos, n); d != "" {
			name := u.Username
			if name == "" {
				name = u.ID
			}
			lines = append(lines, userProgressLine{userID: u.ID, line: name + ": " + d})
		}
	}
	return lines, nil
}

// bookmarkCounter returns a per-user count of the bookmarks under bookID's
// own sync id. A store without bookmarks or sync identity, or a book with no
// sync id, counts zero.
func (svc *AudiobookService) bookmarkCounter(bookID string) func(userID string) (int, error) {
	zero := func(string) (int, error) { return 0, nil }
	bs := database.AsBookmarkStore(svc.store)
	ids := database.AsSyncIdentityStore(svc.store)
	if bs == nil || ids == nil {
		return zero
	}
	syncID, found, err := ids.GetSyncIDForBook(bookID)
	if err != nil {
		return func(string) (int, error) { return 0, fmt.Errorf("read sync id: %w", err) }
	}
	if !found || syncID == "" {
		return zero
	}
	return func(userID string) (int, error) {
		marks, err := bs.ListBookmarks(userID, syncID)
		return len(marks), err
	}
}

// describeUserState is one user's state in a few words: "finished", "42%",
// "at 1:02:03", "hidden from continue listening", "2 bookmarks", joined by
// commas. "" when the user has nothing on the book.
func describeUserState(st *database.UserBookState, pos []database.UserPosition, bookmarks int) string {
	var bits []string
	if st != nil {
		switch {
		case st.Status == database.UserBookStatusFinished:
			bits = append(bits, "finished")
		case st.ProgressPct > 0:
			bits = append(bits, fmt.Sprintf("%d%%", st.ProgressPct))
		case st.Status != "":
			bits = append(bits, strings.ReplaceAll(st.Status, "_", " "))
		}
	}
	var latest *database.UserPosition
	for i := range pos {
		if latest == nil || pos[i].UpdatedAt.After(latest.UpdatedAt) {
			latest = &pos[i]
		}
	}
	if latest != nil && latest.PositionSeconds > 0 {
		bits = append(bits, "at "+formatClock(latest.PositionSeconds))
	}
	if st != nil && st.HideFromContinueListening {
		bits = append(bits, "hidden from continue listening")
	}
	if len(bits) == 0 && (st != nil && (st.TotalListenedSeconds > 0 || st.LastSegmentID != "") || len(pos) > 0) {
		bits = append(bits, "started")
	}
	switch {
	case bookmarks == 1:
		bits = append(bits, "1 bookmark")
	case bookmarks > 1:
		bits = append(bits, fmt.Sprintf("%d bookmarks", bookmarks))
	}
	return strings.Join(bits, ", ")
}

// formatClock renders seconds as h:mm:ss (or m:ss under an hour).
func formatClock(sec float64) string {
	d := time.Duration(sec) * time.Second
	h := int(d / time.Hour)
	m := int(d%time.Hour) / int(time.Minute)
	s := int(d%time.Minute) / int(time.Second)
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// ErrBookHasProgress is returned by a hard delete (DeleteAudiobook,
// "Purge now") of a book users have listening state on when there is no
// other version of it that Audiobookshelf lists to move the state to.
// Nothing was changed. For a book in the trash the owner may drop the state
// on purpose with DiscardProgressAndPurge.
var ErrBookHasProgress = merge.ErrBookHasListeningState

// ErrPurgeCarryFailed is returned by "Purge now" when moving the book's
// listening state to the version Audiobookshelf lists did not complete.
// The book was kept, with every user's state put back on it.
var ErrPurgeCarryFailed = errors.New("the listening progress could not be moved to the copy in the Audiobookshelf library, so the book was kept")

// purgeNow is "Purge now" for one book in the trash (DeleteAudiobook's hard
// path for a trashed book): exactly the nightly purge's path for that book
// (purgeOne, with a fresh strict user probe and the configured
// PurgeSoftDeletedDeleteFiles), so a book with listening state is purged
// only after the state is carried to a version Audiobookshelf lists, and
// is otherwise refused with ErrBookHasProgress.
func (svc *AudiobookService) purgeNow(book *database.Book) (map[string]any, error) {
	probe, err := merge.NewUserStateProbe(svc.store)
	if err != nil {
		return nil, fmt.Errorf("purge %s refused: cannot list users to check their listening state: %w", book.ID, err)
	}
	out := svc.purgeOne(book, config.AppConfig.PurgeSoftDeletedDeleteFiles, probe)
	detail := strings.Join(out.errs, "; ")
	switch out.kind {
	case purgePurged, purgeCarried:
		svc.InvalidateListCache()
		res := map[string]any{
			"message":        "audiobook purged",
			"blocked":        false,
			"files_deleted":  out.filesDeleted,
			"progress_moved": out.kind == purgeCarried,
		}
		if len(out.errs) > 0 {
			res["warnings"] = out.errs
		}
		return res, nil
	case purgeOwnsFiles:
		if detail == "" {
			detail = "the purge never deletes a book that owns file rows"
		}
		return nil, fmt.Errorf("purge %s: %w: %s", book.ID, database.ErrBookOwnsFiles, detail)
	case purgeKeptHasProgress:
		return nil, fmt.Errorf("purge %s: %w. Restore it, or use Discard progress and purge to delete it together with its progress", book.ID, ErrBookHasProgress)
	case purgeCarryFailed:
		return nil, fmt.Errorf("purge %s: %w: %s", book.ID, ErrPurgeCarryFailed, detail)
	default:
		return nil, fmt.Errorf("purge %s failed: %s", book.ID, detail)
	}
}

// ErrNotInTrash is returned by DiscardProgressAndPurge for a book that is
// not soft-deleted. Nothing was changed.
var ErrNotInTrash = errors.New("audiobook is not in the trash")

// ErrAudiobookNotFound is returned by DiscardProgressAndPurge for an id with
// no book row.
var ErrAudiobookNotFound = errors.New("audiobook not found")

// ErrAuditUnavailable is returned by DiscardProgressAndPurge when there is
// no activity log to record the action in. Nothing was changed: an
// owner-triggered destructive action is not run unrecorded.
var ErrAuditUnavailable = errors.New("activity log unavailable; refusing an unrecorded destructive action")

// ErrDiscardRefused is returned by DiscardProgressAndPurge when the state
// could not all be cleared or a pending user-state repair still names the
// book (merge.ErrDiscardIncomplete). The book was not purged.
var ErrDiscardRefused = merge.ErrDiscardIncomplete

// DiscardProgressResult is what DiscardProgressAndPurge did.
type DiscardProgressResult struct {
	BookID string `json:"book_id"`
	Title  string `json:"title"`
	// ProgressSummary is the state that was discarded, as the trash listing
	// showed it.
	ProgressSummary  string `json:"progress_summary,omitempty"`
	UsersCleared     int    `json:"users_cleared"`
	BookmarksCleared int    `json:"bookmarks_cleared"`
	FilesDeleted     int    `json:"files_deleted"`
	// Warnings are problems after the book was purged (a file left on disk,
	// the audit row not written). The purge itself succeeded.
	Warnings []string `json:"warnings,omitempty"`
	// progress is the per-user breakdown of ProgressSummary, for ForViewer.
	progress TrashProgressInfo
}

// ForViewer narrows ProgressSummary like TrashProgressInfo.ForViewer.
func (r DiscardProgressResult) ForViewer(viewerID string, seeAll bool) DiscardProgressResult {
	out := r
	out.ProgressSummary = r.progress.ForViewer(viewerID, seeAll).Summary
	return out
}

// activityRecorder writes one activity row. *activity.Service satisfies it.
type activityRecorder interface {
	Record(entry database.ActivityEntry) error
}

// recorder is the activity log DiscardProgressAndPurge records to: the test
// override when set, else the wired activity service, else nil.
func (svc *AudiobookService) recorder() activityRecorder {
	if svc.auditOverride != nil {
		return svc.auditOverride
	}
	if svc.activityService != nil {
		return svc.activityService
	}
	return nil
}

// DiscardProgressAndPurge is the owner's explicit "discard progress and
// purge" for one book in the trash: every user's listening state on it
// (book state, positions, bookmarks under its own sync id) is cleared and
// the book is hard-deleted through the purge's own delete path
// (purgeDeleteRow, then purgeFinish with the configured
// PurgeSoftDeletedDeleteFiles), in one hold of the merge lock
// (merge.DiscardUserStateThenHardDelete). actor names who asked, for the
// audit row.
//
// It refuses, changing nothing, a book that is not in the trash
// (ErrNotInTrash), one that still owns book_file rows
// (database.ErrBookOwnsFiles: the purge never deletes those), and any call
// when no activity log is wired (ErrAuditUnavailable). The trash and
// file-row checks run once up front and again under the merge lock in the
// precheck, before any state is cleared, so a restore or a file row that
// lands in between refuses with nothing discarded. (A file row written
// after the precheck, while the state is being cleared, still makes the
// final DeleteBook refuse; that window is the merge lock's, which file
// writers do not take.)
func (svc *AudiobookService) DiscardProgressAndPurge(ctx context.Context, id, actor string) (*DiscardProgressResult, error) {
	if svc.store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	book, err := svc.store.GetBookByID(id)
	if err != nil {
		return nil, fmt.Errorf("read audiobook %s: %w", id, err)
	}
	if book == nil {
		return nil, ErrAudiobookNotFound
	}
	if !book.IsSoftDeleted() {
		return nil, fmt.Errorf("%w: %s", ErrNotInTrash, id)
	}
	rec := svc.recorder()
	if rec == nil {
		return nil, ErrAuditUnavailable
	}
	owned, err := svc.store.GetBookFiles(id)
	if err != nil {
		return nil, fmt.Errorf("cannot read the book_file rows of %s: %w", id, err)
	}
	if len(owned) > 0 {
		return nil, fmt.Errorf("discard progress and purge %s: %w (%d row(s)); the purge never deletes those, so the book stays in the trash", id, database.ErrBookOwnsFiles, len(owned))
	}
	merger, ok := database.AsCapability[merge.UserProgressMerger](svc.store)
	if !ok {
		return nil, fmt.Errorf("the store cannot clear listening state")
	}

	res := &DiscardProgressResult{BookID: id, Title: book.Title}
	if info, perr := svc.TrashProgress(ctx, []string{id}); perr == nil {
		res.progress = info[id]
		res.ProgressSummary = info[id].Summary
	}

	precheck := func() error {
		// Re-read under the merge lock, before anything is cleared: a
		// restore (which takes the same lock) that ran since the check above
		// took the book out of the trash, and its state must stay.
		cur, gerr := svc.store.GetBookByID(id)
		if gerr != nil {
			return fmt.Errorf("re-read audiobook %s: %w", id, gerr)
		}
		if cur == nil || !cur.IsSoftDeleted() {
			return fmt.Errorf("%w: %s was restored meanwhile", ErrNotInTrash, id)
		}
		// And the ownership check again, here where nothing has been
		// cleared yet: a file row that landed since the check above would
		// make the delete refuse (DeleteBook: database.ErrBookOwnsFiles)
		// only AFTER every user's state was discarded.
		owned, ferr := svc.store.GetBookFiles(id)
		if ferr != nil {
			return fmt.Errorf("cannot re-read the book_file rows of %s: %w", id, ferr)
		}
		if len(owned) > 0 {
			return fmt.Errorf("discard progress and purge %s: %w (%d row(s)); the purge never deletes those, so the book stays in the trash", id, database.ErrBookOwnsFiles, len(owned))
		}
		book = cur
		return nil
	}
	cleared, err := merge.DiscardUserStateThenHardDelete(merger, id, precheck, func() error {
		return svc.purgeDeleteRow(book)
	})
	res.UsersCleared, res.BookmarksCleared = cleared.Users, cleared.Bookmarks
	if err != nil {
		return nil, err
	}
	res.FilesDeleted, res.Warnings = svc.purgeFinish(book, config.AppConfig.PurgeSoftDeletedDeleteFiles)
	svc.InvalidateListCache()

	if aerr := rec.Record(database.ActivityEntry{
		Timestamp: time.Now().UTC(),
		Tier:      "audit",
		Type:      "trash_discard_progress_purge",
		Level:     "info",
		Source:    "trash",
		BookID:    id,
		Summary:   fmt.Sprintf("discarded listening progress and purged %q from the trash (%d user(s), %d bookmark(s))", book.Title, res.UsersCleared, res.BookmarksCleared),
		Details: map[string]any{
			"actor":             actor,
			"book_id":           id,
			"title":             book.Title,
			"progress_summary":  res.ProgressSummary,
			"users_cleared":     res.UsersCleared,
			"bookmarks_cleared": res.BookmarksCleared,
			"files_deleted":     res.FilesDeleted,
		},
	}); aerr != nil {
		trashLog.Error("discard progress and purge: audit row not written for %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(aerr.Error()))
		res.Warnings = append(res.Warnings, fmt.Sprintf("the book was purged, but the activity log row was not written: %v", aerr))
	}
	return res, nil
}
