// file: internal/audiobooks/trash_progress.go
// version: 1.2.0
// guid: 98f1136e-e723-43fd-9d77-fab344a4aa45
// last-edited: 2026-10-05

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
// state may be carried to: only one Audiobookshelf lists
// (database.ABSLibraryFilter: the organized, non-quarantined primary, out of
// the trash), never a hidden copy -- state carried onto a book ABS does not
// list is stranded where the user cannot see it, and the trashed row that
// showed "has progress" would be gone (the same rule as reconcile's
// SkippedKeepNotListed). Among several, the lowest id. "" when the book has
// no group or no listed member: the purge then keeps it.
func (svc *AudiobookService) liveSiblingFor(book *database.Book) (string, error) {
	if book.VersionGroupID == nil || *book.VersionGroupID == "" {
		return "", nil
	}
	members, err := svc.store.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		return "", fmt.Errorf("list version group %s: %w", *book.VersionGroupID, err)
	}
	best := ""
	for i := range members {
		m := &members[i]
		if m.ID == "" || m.ID == book.ID || !siblingIsCarryTarget(m) {
			continue
		}
		if best == "" || m.ID < best {
			best = m.ID
		}
	}
	return best, nil
}

// siblingIsCarryTarget reports whether b may receive a trashed book's users'
// state: it is out of the trash and Audiobookshelf lists it.
func siblingIsCarryTarget(b *database.Book) bool {
	return b != nil && isLiveBook(b) && database.ABSLibraryFilter().Matches(b)
}

// errSiblingNotListed is the carry precheck's refusal: the chosen sibling is
// no longer one Audiobookshelf lists. Nothing was carried.
var errSiblingNotListed = errors.New("the version chosen to receive the listening state is no longer listed in Audiobookshelf")

// isLiveBook reports whether b is out of the trash: the soft-delete flag is
// unset and library_state is not the "deleted" label the soft delete writes.
func isLiveBook(b *database.Book) bool {
	if b.IsSoftDeleted() {
		return false
	}
	return b.LibraryState == nil || !strings.EqualFold(*b.LibraryState, "deleted")
}

// TrashProgressInfo is what the trash listing shows about a book's users'
// listening state.
type TrashProgressInfo struct {
	// HasProgress is merge.UserStateProbe.Has: the same test the purge uses
	// to decide a book cannot simply be deleted, so the flag and the purge
	// never disagree.
	HasProgress bool `json:"has_progress"`
	// Summary is a short description per user, e.g. "reader: finished" or
	// "reader: 42%, at 1:02:03". Empty when HasProgress is false.
	Summary string `json:"progress_summary,omitempty"`
	// Unknown is set when the state could not be read; HasProgress is then
	// false but means "not known", not "none".
	Unknown bool `json:"progress_unknown,omitempty"`
}

// TrashProgress reports, for each book id, whether any user has listening
// state on it and a short summary of that state. A read that fails marks the
// book Unknown rather than failing the whole listing.
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
		summary, err := svc.progressSummary(users, id)
		if err != nil {
			trashLog.Warn("trash listing: cannot summarize listening state on %s: %s", logger.SanitizeLogValue(id), logger.SanitizeLogValue(err.Error()))
		}
		out[id] = TrashProgressInfo{HasProgress: true, Summary: summary}
	}
	return out, nil
}

// progressSummary describes each user's state on bookID in a few words.
func (svc *AudiobookService) progressSummary(users []database.User, bookID string) (string, error) {
	var parts []string
	for _, u := range users {
		if u.ID == "" {
			continue
		}
		st, err := svc.store.GetUserBookState(u.ID, bookID)
		if err != nil {
			return strings.Join(parts, "; "), err
		}
		pos, err := svc.store.ListUserPositionsForBook(u.ID, bookID)
		if err != nil {
			return strings.Join(parts, "; "), err
		}
		if d := describeUserState(st, pos); d != "" {
			name := u.Username
			if name == "" {
				name = u.ID
			}
			parts = append(parts, name+": "+d)
		}
	}
	return strings.Join(parts, "; "), nil
}

// describeUserState is one user's state in a few words: "finished", "42%",
// "at 1:02:03", "hidden from continue listening", joined by commas. "" when
// the user has nothing on the book.
func describeUserState(st *database.UserBookState, pos []database.UserPosition) string {
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
// (database.ErrBookOwnsFiles: the purge never deletes those, and the check
// runs before any state is cleared), and any call when no activity log is
// wired (ErrAuditUnavailable).
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
	svc.InvalidateBookCaches()

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
