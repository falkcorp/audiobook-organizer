// file: internal/server/handlers/versions.go
// version: 1.7.0
// guid: 7e3c1a92-4b8d-4f60-9a2e-1c0d5f8b6a47
// last-edited: 2026-09-13

package handlers

import (
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/gin-gonic/gin"
	ulid "github.com/oklog/ulid/v2"
)

// VersionBookReader reads a book and its version-group siblings.
type VersionBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBooksByVersionGroup(groupID string) ([]database.Book, error)
}

// VersionBookWriter creates books and changes them. Every change goes through
// ModifyBook, a read-modify-write of the stored row under the book's lock, so
// a write here can never put back fields another writer (or this handler's own
// MoveBookFilesToBook aggregate recount) committed after the handler read the
// row. There is deliberately no UpdateBook: a whole-row write of a copy read
// earlier is exactly the lost update the split handlers used to make.
type VersionBookWriter interface {
	CreateBook(book *database.Book) (*database.Book, error)
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// VersionBookFileStore reads book files and moves them between books.
type VersionBookFileStore interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
	MoveBookFilesToBook(fileIDs []string, sourceBookID, targetBookID string) error
}

// VersionBookAuthorStore reads and replaces a book's author links.
type VersionBookAuthorStore interface {
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
}

// VersionExternalIDStore reads external ID mappings and moves one to another
// book. ReassignExternalID rewrites the primary key and both reverse keys in
// one atomic batch, so a failure leaves the mapping where it was. It replaced
// a DeleteRaw of the old reverse key followed by a separate create, where a
// failed create left the ID indexed under neither book.
type VersionExternalIDStore interface {
	GetExternalIDsForBook(bookID string) ([]database.ExternalIDMapping, error)
	ReassignExternalID(source, externalID, newBookID string) error
}

// VersionBookPathChecker reports which live books sit at a path. The split
// endpoints use it to refuse a new book on a path another book owns.
type VersionBookPathChecker interface {
	LiveBookIDsAtPath(path string) ([]string, error)
}

// VersionBookDeleter deletes a book. The one-book split uses it to remove the
// book it just created when moving the files into it fails.
type VersionBookDeleter interface {
	DeleteBook(id string) error
}

// VersionsStore is the narrow database interface VersionsHandler requires.
// It lists only the database.Store methods the version-grouping handlers
// actually call, including the external-ID methods used by
// reassignExternalIDsForFiles.
//
// Split into the interfaces above on 2026-08-18; this name is their
// composition. On 2026-09-13 UpdateBook, CreateExternalIDMapping and DeleteRaw
// were replaced by ModifyBook and ReassignExternalID (see the two declarations
// for why), which also removed the raw-key deleter from this handler entirely.
type VersionsStore interface {
	VersionBookReader
	VersionBookWriter
	VersionBookFileStore
	VersionBookAuthorStore
	VersionExternalIDStore
	VersionBookPathChecker
	VersionBookDeleter
}

// VersionsHandler handles audiobook version-group endpoints: listing, linking,
// setting primary, fetching a group, and split/move operations on segments.
type VersionsHandler struct {
	store VersionsStore
	// groupLocks serializes the set-primary and link calls that touch the
	// same version group; see lockGroups. The server builds one handler, so
	// every request shares them.
	groupLocks [versionGroupLockStripes]sync.Mutex
}

// versionGroupLockStripes is how many locks the version-group IDs hash onto.
// Striping keeps the set fixed-size; two groups sharing a stripe only
// serialize with each other, which is harmless.
const versionGroupLockStripes = 64

// lockGroups locks the stripes of the given version groups ("" is skipped) in
// stripe order, so two calls locking overlapping sets cannot deadlock, and
// returns the unlock.
//
// It serializes this handler's own writers of a group: two set-primary calls
// on one group each read the members, promote their book and demote the rest,
// and interleaved they demoted each other's promotion and left no primary.
// Writers outside this handler (maintenance jobs, reconcile) do not take these
// locks; against them the per-row checks inside each ModifyBook are the guard.
func (h *VersionsHandler) lockGroups(groupIDs ...string) func() {
	var idx []int
	for _, g := range groupIDs {
		if g == "" {
			continue
		}
		f := fnv.New32a()
		_, _ = f.Write([]byte(g))
		idx = append(idx, int(f.Sum32()%versionGroupLockStripes))
	}
	slices.Sort(idx)
	idx = slices.Compact(idx)
	for _, i := range idx {
		h.groupLocks[i].Lock()
	}
	return func() {
		for j := len(idx) - 1; j >= 0; j-- {
			h.groupLocks[idx[j]].Unlock()
		}
	}
}

// NewVersionsHandler constructs a VersionsHandler backed by the given store.
func NewVersionsHandler(store VersionsStore) *VersionsHandler {
	return &VersionsHandler{store: store}
}

// ListAudiobookVersions lists all versions of an audiobook
func (h *VersionsHandler) ListAudiobookVersions(c *gin.Context) {
	id := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := h.store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	if book.VersionGroupID == nil {
		httputil.RespondWithOK(c, gin.H{"versions": []any{book}})
		return
	}

	books, err := h.store.GetBooksByVersionGroup(*book.VersionGroupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to fetch versions")
		return
	}

	httputil.RespondWithOK(c, gin.H{"versions": books})
}

// LinkAudiobookVersion links two audiobooks as versions of one work.
//
// The result is one version group holding both books -- and, when both were
// already grouped, every live member of both groups, since a sibling of either
// book is a version of the same work -- with exactly one primary:
//
//   - the target group's (id's group's) existing primary, when it has one;
//   - otherwise the incoming side's existing primary;
//   - otherwise an election over every member.
//
// Each choice among several candidates takes the earliest-created, tie-broken
// by ID (reconcile.electPrimaryFor's rule), so a rerun picks the same book.
// The winner is written first and every other member is then written as
// non-primary in the same ModifyBook that moves it into the group, so no
// member ever joins the group still claiming to be primary.
//
// A soft-deleted book, or linking a book to itself, is refused before any
// write. A member whose group changed after it was read, or that was deleted
// meanwhile, stops the link: every member already written is put back into
// the group and primary flag it had, newest first, so a failure part-way can
// not strand an incoming member's old group without its primary. A member
// the undo cannot restore is named in the response.
//
// Links and set-primary calls on the same groups are serialized (lockGroups).
func (h *VersionsHandler) LinkAudiobookVersion(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		OtherID string `json:"other_id" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if req.OtherID == id {
		httputil.RespondWithBadRequest(c, "cannot link an audiobook to itself")
		return
	}

	book1, err := h.store.GetBookByID(id)
	if err != nil || book1 == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}
	book2, err := h.store.GetBookByID(req.OtherID)
	if err != nil || book2 == nil {
		httputil.RespondWithNotFound(c, "audiobook", req.OtherID)
		return
	}
	for _, b := range []*database.Book{book1, book2} {
		if b.IsSoftDeleted() {
			httputil.RespondWithErrorFields(c, http.StatusBadRequest,
				fmt.Sprintf("audiobook %s is deleted; restore it before linking it as a version", b.ID),
				"link_deleted_book", map[string]any{"book_id": b.ID})
			return
		}
	}

	g1, g2 := versionGroupOf(book1), versionGroupOf(book2)
	defer h.lockGroups(g1, g2)()
	if g1 != "" && g1 == g2 {
		httputil.RespondWithOK(c, gin.H{"version_group_id": g1})
		return
	}

	// The target side keeps its group ID; the incoming side joins it.
	targetGroup := g1
	targetBook, incomingBook, incomingGroup := book1, book2, g2
	if targetGroup == "" && g2 != "" {
		targetGroup = g2
		targetBook, incomingBook, incomingGroup = book2, book1, ""
	}
	if targetGroup == "" {
		targetGroup = ulid.Make().String()
	}

	targetMembers, err := h.liveGroupMembers(versionGroupOf(targetBook), targetBook)
	if err != nil {
		httputil.InternalError(c, "failed to read the target version group; nothing was linked", err)
		return
	}
	incomingMembers, err := h.liveGroupMembers(incomingGroup, incomingBook)
	if err != nil {
		httputil.InternalError(c, "failed to read the incoming version group; nothing was linked", err)
		return
	}

	winner := electVersionPrimary(primaryCandidates(targetMembers))
	if winner == nil {
		winner = electVersionPrimary(primaryCandidates(incomingMembers))
	}
	all := append(append([]database.Book{}, targetMembers...), incomingMembers...)
	if winner == nil {
		winner = electVersionPrimary(all)
	}

	// The group each member was read in: a member whose stored group differs
	// by the time its write runs has been moved by someone else, and is not
	// ours to move.
	readGroup := make(map[string]string, len(all))
	for i := range all {
		readGroup[all[i].ID] = versionGroupOf(&all[i])
	}
	order := []string{winner.ID}
	var rest []string
	for i := range all {
		if all[i].ID != winner.ID {
			rest = append(rest, all[i].ID)
		}
	}
	slices.Sort(rest)
	order = append(order, rest...)

	var written []versionFlagWrite
	for _, bid := range order {
		wantPrimary := bid == winner.ID
		var w *versionFlagWrite
		_, mErr := h.modifyBook(bid, func(cur *database.Book) error {
			w = nil
			if cur.IsSoftDeleted() {
				return fmt.Errorf("book %s was deleted during the link", cur.ID)
			}
			if g := versionGroupOf(cur); g != readGroup[cur.ID] {
				return fmt.Errorf("book %s moved from version group %q to %q during the link", cur.ID, readGroup[cur.ID], g)
			}
			if versionGroupOf(cur) == targetGroup && cur.IsPrimaryVersion != nil && *cur.IsPrimaryVersion == wantPrimary {
				return database.ErrSkipBookWrite
			}
			w = &versionFlagWrite{id: cur.ID, prevGroup: cloneStringPtr(cur.VersionGroupID), prevFlag: cloneBoolPtr(cur.IsPrimaryVersion)}
			gid := targetGroup
			cur.VersionGroupID = &gid
			cur.IsPrimaryVersion = &wantPrimary
			return nil
		})
		if mErr == nil {
			if w != nil {
				written = append(written, *w)
			}
			continue
		}

		stuck := h.undoLinkWrites(targetGroup, written)
		versionsLog.Error("link %s + %s: stopped at book %s: %v (unrestored: %v)",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(req.OtherID), bid, mErr, stuck)
		msg := fmt.Sprintf("failed to link book %s into version group %s (%v); every book already written was put back into its old group", bid, targetGroup, mErr)
		if len(stuck) > 0 {
			msg = fmt.Sprintf("failed to link book %s into version group %s (%v), and %s could not be put back into their old groups",
				bid, targetGroup, mErr, strings.Join(stuck, ", "))
		}
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError, msg, "link_failed", map[string]any{
			"version_group_id":    targetGroup,
			"failed_book_id":      bid,
			"unrestored_book_ids": stuck,
		})
		return
	}

	httputil.RespondWithOK(c, gin.H{"version_group_id": targetGroup, "primary_book_id": winner.ID})
}

// versionFlagWrite is one book's version group and primary flag as they were
// before a handler wrote them, for the undo.
type versionFlagWrite struct {
	id        string
	prevGroup *string
	prevFlag  *bool
}

// undoLinkWrites puts every book a failed link wrote back into the group and
// primary flag it had, newest first. A book that has left targetGroup since
// was moved by someone else and is left alone. It returns the IDs it could
// not restore.
func (h *VersionsHandler) undoLinkWrites(targetGroup string, written []versionFlagWrite) []string {
	var stuck []string
	for i := len(written) - 1; i >= 0; i-- {
		w := written[i]
		if _, err := h.modifyBook(w.id, func(cur *database.Book) error {
			if versionGroupOf(cur) != targetGroup {
				return fmt.Errorf("book %s left version group %s after the link wrote it", cur.ID, targetGroup)
			}
			cur.VersionGroupID = w.prevGroup
			cur.IsPrimaryVersion = w.prevFlag
			return nil
		}); err != nil {
			versionsLog.Error("link: undoing the write of book %s failed: %v", w.id, err)
			stuck = append(stuck, w.id)
		}
	}
	return stuck
}

func cloneStringPtr(p *string) *string {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// liveGroupMembers returns the live members of groupID, always including
// self (which the caller has checked is live). An empty groupID is a book
// with no group: its only member is itself.
func (h *VersionsHandler) liveGroupMembers(groupID string, self *database.Book) ([]database.Book, error) {
	if groupID == "" {
		return []database.Book{*self}, nil
	}
	members, err := h.store.GetBooksByVersionGroup(groupID)
	if err != nil {
		return nil, err
	}
	out := make([]database.Book, 0, len(members)+1)
	hasSelf := false
	for _, m := range members {
		if m.IsSoftDeleted() {
			continue
		}
		if m.ID == self.ID {
			hasSelf = true
		}
		out = append(out, m)
	}
	if !hasSelf {
		out = append(out, *self)
	}
	return out, nil
}

// versionGroupOf returns b's version group ID, "" for none.
func versionGroupOf(b *database.Book) string {
	if b == nil || b.VersionGroupID == nil {
		return ""
	}
	return *b.VersionGroupID
}

// The *bool is_primary_version flag has two readings, and this file needs
// both:
//
//   - isExplicitPrimary: set and true. Used to skip a write that would change
//     nothing ("already primary").
//   - countsAsPrimary: nil or true. This is how the store and memdb read the
//     flag when they decide what the library shows as a primary
//     (PebbleStore's summary filter, the memdb summaries and reads, stats,
//     series and iTunes paths), so a member with a nil flag IS a primary to
//     the rest of the system. Anything that must leave a group with one
//     primary -- the set-primary demotes, the link election -- uses this
//     reading, or a nil member survives as a second primary.
func isExplicitPrimary(b *database.Book) bool {
	return b != nil && b.IsPrimaryVersion != nil && *b.IsPrimaryVersion
}

func countsAsPrimary(b *database.Book) bool {
	return b != nil && (b.IsPrimaryVersion == nil || *b.IsPrimaryVersion)
}

// primaryCandidates returns a group's members that are primaries: the
// explicit ones when there are any (an explicit flag is the stronger claim),
// else the ones the store counts as primary because their flag is nil.
func primaryCandidates(books []database.Book) []database.Book {
	var explicit, implicit []database.Book
	for _, b := range books {
		switch {
		case isExplicitPrimary(&b):
			explicit = append(explicit, b)
		case countsAsPrimary(&b):
			implicit = append(implicit, b)
		}
	}
	if len(explicit) > 0 {
		return explicit
	}
	return implicit
}

// electVersionPrimary picks the earliest-created book, tie-broken by ID: the
// rule reconcile.electPrimaryFor uses, so a rerun converges on the same book.
// nil for no books.
func electVersionPrimary(books []database.Book) *database.Book {
	var best *database.Book
	for i := range books {
		b := &books[i]
		if best == nil {
			best = b
			continue
		}
		switch {
		case b.CreatedAt != nil && best.CreatedAt != nil && !b.CreatedAt.Equal(*best.CreatedAt):
			if b.CreatedAt.Before(*best.CreatedAt) {
				best = b
			}
		case b.CreatedAt != nil && best.CreatedAt == nil:
			best = b
		case b.CreatedAt == nil && best.CreatedAt != nil:
		case b.ID < best.ID:
			best = b
		}
	}
	return best
}

// modifyBook is ModifyBook with a missing row reported as an error: the
// store returns (nil, nil) for a book that does not exist, which a caller
// must not read as a successful write.
func (h *VersionsHandler) modifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	b, err := h.store.ModifyBook(id, fn)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("book %s not found", id)
	}
	return b, nil
}

// SetAudiobookPrimary makes an audiobook the one primary of its version group.
//
// The new primary is promoted first and the others are then demoted, in book
// ID order, each through ModifyBook on the stored row. Every member whose flag
// is not an explicit false is demoted: the store counts a nil flag as primary
// (see countsAsPrimary), so skipping nil members left a second primary.
//
// If a demote fails, the writes already made are undone in reverse (each
// demoted book gets back the exact flag it had, nil included, and the new
// primary its old flag), so the group ends as it began and the request fails.
// If an undo write also fails the response names every book whose flag could
// not be restored, and the group can be left in between.
//
// Calls on the same group are serialized (lockGroups): two concurrent calls
// used to demote each other's promotion and leave no primary. Writers outside
// this handler do not take that lock. A soft-deleted book is refused: a
// deleted row must never be its group's only primary.
func (h *VersionsHandler) SetAudiobookPrimary(c *gin.Context) {
	id := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	book, err := h.store.GetBookByID(id)
	if err != nil || book == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}
	if book.IsSoftDeleted() {
		httputil.RespondWithErrorFields(c, http.StatusBadRequest,
			fmt.Sprintf("audiobook %s is deleted; a deleted book cannot be a primary version", id),
			"primary_deleted_book", map[string]any{"book_id": id})
		return
	}

	groupID := versionGroupOf(book)
	if groupID == "" {
		if _, err := h.modifyBook(id, func(cur *database.Book) error {
			if isExplicitPrimary(cur) {
				return database.ErrSkipBookWrite
			}
			t := true
			cur.IsPrimaryVersion = &t
			return nil
		}); err != nil {
			httputil.InternalError(c, "failed to update audiobook", err)
			return
		}
		httputil.RespondWithOK(c, gin.H{"message": "audiobook set as primary"})
		return
	}

	defer h.lockGroups(groupID)()
	books, err := h.store.GetBooksByVersionGroup(groupID)
	if err != nil {
		httputil.InternalError(c, "failed to fetch versions; nothing was changed", err)
		return
	}
	var others []string
	for _, b := range books {
		if b.ID != id {
			others = append(others, b.ID)
		}
	}
	slices.Sort(others)

	// 1. Promote. The previous flag is kept for the undo.
	var prevFlag *bool
	promoted := false
	if _, err := h.modifyBook(id, func(cur *database.Book) error {
		if cur.IsSoftDeleted() {
			return fmt.Errorf("book %s was deleted", cur.ID)
		}
		if versionGroupOf(cur) != groupID {
			return fmt.Errorf("book %s left version group %s", cur.ID, groupID)
		}
		if isExplicitPrimary(cur) {
			return database.ErrSkipBookWrite
		}
		prevFlag = cloneBoolPtr(cur.IsPrimaryVersion)
		t := true
		cur.IsPrimaryVersion = &t
		promoted = true
		return nil
	}); err != nil {
		httputil.InternalError(c, "failed to promote the audiobook; nothing was changed", err)
		return
	}

	// 2. Demote every other member the store counts as primary: an explicit
	// true, or a nil flag.
	var demoted []versionFlagWrite
	for _, oid := range others {
		var d *versionFlagWrite
		_, dErr := h.modifyBook(oid, func(cur *database.Book) error {
			d = nil
			if versionGroupOf(cur) != groupID || !countsAsPrimary(cur) {
				return database.ErrSkipBookWrite
			}
			d = &versionFlagWrite{id: cur.ID, prevFlag: cloneBoolPtr(cur.IsPrimaryVersion)}
			f := false
			cur.IsPrimaryVersion = &f
			return nil
		})
		if dErr == nil {
			if d != nil {
				demoted = append(demoted, *d)
			}
			continue
		}

		// 3. Undo, newest write first, restoring each flag exactly (nil stays
		// nil).
		var stuck []string
		for i := len(demoted) - 1; i >= 0; i-- {
			prev := demoted[i].prevFlag
			if _, uErr := h.modifyBook(demoted[i].id, func(cur *database.Book) error {
				cur.IsPrimaryVersion = cloneBoolPtr(prev)
				return nil
			}); uErr != nil {
				versionsLog.Error("set-primary %s: undoing the demote of %s failed: %v", logger.SanitizeLogValue(id), demoted[i].id, uErr)
				stuck = append(stuck, demoted[i].id)
			}
		}
		if promoted {
			if _, uErr := h.modifyBook(id, func(cur *database.Book) error {
				cur.IsPrimaryVersion = prevFlag
				return nil
			}); uErr != nil {
				versionsLog.Error("set-primary %s: undoing its promotion failed: %v", logger.SanitizeLogValue(id), uErr)
				stuck = append(stuck, id)
			}
		}
		msg := fmt.Sprintf("failed to demote book %s (%v); the change was rolled back and the group's primary is unchanged", oid, dErr)
		if len(stuck) > 0 {
			msg = fmt.Sprintf("failed to demote book %s (%v), and the rollback could not restore the primary flag of %s; the group may have more than one primary",
				oid, dErr, strings.Join(stuck, ", "))
		}
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError, msg, "set_primary_failed",
			map[string]any{"version_group_id": groupID, "failed_book_id": oid, "unrestored_book_ids": stuck})
		return
	}

	httputil.RespondWithOK(c, gin.H{"message": "audiobook set as primary"})
}

// GetVersionGroup gets all audiobooks in a version group
func (h *VersionsHandler) GetVersionGroup(c *gin.Context) {
	groupID := c.Param("id")

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	books, err := h.store.GetBooksByVersionGroup(groupID)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to fetch version group")
		return
	}

	httputil.RespondWithOK(c, gin.H{"audiobooks": books})
}

// SplitVersion moves selected segments from a book into a new version (a new book
// in the same version group).
//
// Order, and why:
//  1. Every check that can refuse happens before any write: the source is
//     live, every segment ID is one of its files, and the group is readable.
//  2. A source with no group gets one, as its primary (a group with no primary
//     hides its books from the default library view).
//  3. The new book is created and the rows are moved with ONE
//     MoveBookFilesToBook, which recounts the aggregates (duration, size, file
//     count) of both books. If the move fails nothing moved: the new book is
//     deleted and a group minted in step 2 is taken off the source again.
//  4. The moved files' external IDs (iTunes PIDs) move to the new book, as
//     the other three move paths do, so a PID does not keep naming a book that
//     no longer holds its file.
//  5. Only then are the two FilePaths settled, each in a ModifyBook closure
//     that sets FilePath and nothing else, on the stored row. These writes
//     used to be whole-row UpdateBook calls of the copies read before the
//     move, which put the pre-move totals back over the recount.
//
// A failure in steps 4-5 does not undo the move; it is reported in
// "warnings" with status 207.
//
// Every ModifyBook writes the store's copy-on-write version snapshot, so the
// source's and the new book's rows before the split stay in their history.
func (h *VersionsHandler) SplitVersion(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs []string `json:"segment_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// 1. Source book, and the refusals.
	sourceBook, err := h.store.GetBookByID(id)
	if err != nil || sourceBook == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}
	if sourceBook.IsSoftDeleted() {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("audiobook %s is deleted; restore it before splitting it", id))
		return
	}
	sourceFiles, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files; nothing was written", err)
		return
	}
	segmentIDs, ok := selectSourceFiles(c, sourceBook.ID, sourceFiles, req.SegmentIDs)
	if !ok {
		return
	}

	versionGroupID := versionGroupOf(sourceBook)
	existing := 1 // the source alone, when it has no group yet
	if versionGroupID != "" {
		existingVersions, err := h.store.GetBooksByVersionGroup(versionGroupID)
		if err != nil {
			// The member count names the new row ("Version N"). An unreadable
			// group must fail the split, not number the row as if it were empty.
			httputil.InternalError(c, "failed to read version group", err)
			return
		}
		existing = len(existingVersions)
	}

	// 2. Give a groupless source a group, as its primary.
	minted := ""
	var prevPrimary *bool
	if versionGroupID == "" {
		newGID := ulid.Make().String()
		updated, err := h.modifyBook(id, func(cur *database.Book) error {
			if g := versionGroupOf(cur); g != "" {
				// Grouped by someone else since the read: split into that group.
				return database.ErrSkipBookWrite
			}
			if cur.IsPrimaryVersion != nil {
				v := *cur.IsPrimaryVersion
				prevPrimary = &v
			}
			gid := newGID
			t := true
			cur.VersionGroupID = &gid
			cur.IsPrimaryVersion = &t
			return nil
		})
		if err != nil {
			httputil.InternalError(c, "failed to update source book version group", err)
			return
		}
		versionGroupID = versionGroupOf(updated)
		if versionGroupID == newGID {
			minted = newGID
		}
	}

	// 3. Create the new book, then move the rows into it.
	newTitle := fmt.Sprintf("%s (Version %d)", sourceBook.Title, existing+1)
	primaryFlag := false
	gid := versionGroupID
	createdBook, err := h.store.CreateBook(&database.Book{
		Title:            newTitle,
		AuthorID:         sourceBook.AuthorID,
		SeriesID:         sourceBook.SeriesID,
		SeriesSequence:   sourceBook.SeriesSequence,
		FilePath:         "", // Set from the moved rows in step 4.
		Format:           sourceBook.Format,
		WorkID:           sourceBook.WorkID,
		Narrator:         sourceBook.Narrator,
		Language:         sourceBook.Language,
		Publisher:        sourceBook.Publisher,
		VersionGroupID:   &gid,
		IsPrimaryVersion: &primaryFlag,
	})
	if err != nil {
		h.unmintSplitGroup(id, minted, prevPrimary)
		httputil.InternalError(c, "failed to create new version", err)
		return
	}
	if createdBook == nil {
		h.unmintSplitGroup(id, minted, prevPrimary)
		httputil.RespondWithInternalError(c, "failed to create new version: the store returned no book")
		return
	}

	// DB-only: nothing is touched on disk, so the iTunes guard has nothing to
	// guard here.
	if err := h.store.MoveBookFilesToBook(segmentIDs, sourceBook.ID, createdBook.ID); err != nil {
		// The move batch is atomic: no row moved and no row names the new book.
		dErr := h.store.DeleteBook(createdBook.ID)
		h.unmintSplitGroup(id, minted, prevPrimary)
		if dErr != nil {
			versionsLog.Error("split-version: move into %s failed (%v) and deleting it failed: %v", createdBook.ID, err, dErr)
			httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
				fmt.Sprintf("failed to move files into the new version; no file was moved, and the empty new book %s could not be deleted (%v): %v",
					createdBook.ID, dErr, err),
				"split_move_failed", map[string]any{"created_book_id": createdBook.ID})
			return
		}
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"failed to move files into the new version; no file was moved and the new book was deleted: "+err.Error(),
			"split_move_failed", nil)
		return
	}

	// 4. The files have moved and that is not undone. A step that fails from
	// here is reported in "warnings" with status 207.
	var warnings []string
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		warnings = append(warnings, msg)
		versionsLog.Warn("split-version %s -> %s: %s", logger.SanitizeLogValue(id), createdBook.ID, logger.SanitizeLogValue(msg))
	}
	moving := make(map[string]bool, len(segmentIDs))
	for _, fid := range segmentIDs {
		moving[fid] = true
	}
	var movedFiles []database.BookFile
	for _, f := range sourceFiles {
		if moving[f.ID] {
			movedFiles = append(movedFiles, f)
		}
	}
	if xErr := h.reassignExternalIDsForFiles(sourceBook.ID, createdBook.ID, movedFiles); xErr != nil {
		warn("not every external ID (iTunes PID) of the moved files was moved to the new book; the ones that failed still name the source: %v", xErr)
	}
	h.setPathFromFiles(createdBook.ID, "new book", warn)
	h.setPathFromFiles(sourceBook.ID, "source book", warn)

	// Re-read the new book: the recount and the path write happened after
	// CreateBook returned.
	if fresh, gErr := h.store.GetBookByID(createdBook.ID); gErr == nil && fresh != nil {
		createdBook = fresh
	}

	body := gin.H{
		"book":             createdBook,
		"version_group_id": versionGroupID,
		"segments_moved":   len(segmentIDs),
	}
	if len(warnings) > 0 {
		body["warnings"] = warnings
		c.JSON(http.StatusMultiStatus, body)
		return
	}
	httputil.RespondWithOK(c, body)
}

// selectSourceFiles checks that every requested segment ID is one of the
// source's files and returns them de-duplicated in request order, or answers
// the request with 400 naming the unknown IDs and returns ok=false.
func selectSourceFiles(c *gin.Context, sourceID string, files []database.BookFile, segmentIDs []string) ([]string, bool) {
	have := make(map[string]bool, len(files))
	for _, f := range files {
		have[f.ID] = true
	}
	seen := make(map[string]bool, len(segmentIDs))
	var ids, unknown []string
	for _, fid := range segmentIDs {
		if seen[fid] {
			continue
		}
		seen[fid] = true
		if !have[fid] {
			unknown = append(unknown, fid)
			continue
		}
		ids = append(ids, fid)
	}
	if len(unknown) > 0 {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("segment_ids not on book %s: %s", sourceID, strings.Join(unknown, ", ")))
		return nil, false
	}
	return ids, true
}

// unmintSplitGroup takes a version group SplitVersion gave the source off it
// again after the split failed, restoring its primary flag. A no-op when no
// group was minted, or when the source's group has changed since.
func (h *VersionsHandler) unmintSplitGroup(sourceID, minted string, prevPrimary *bool) {
	if minted == "" {
		return
	}
	if _, err := h.modifyBook(sourceID, func(cur *database.Book) error {
		if versionGroupOf(cur) != minted {
			return database.ErrSkipBookWrite
		}
		cur.VersionGroupID = nil
		cur.IsPrimaryVersion = prevPrimary
		return nil
	}); err != nil {
		versionsLog.Error("split-version: could not take version group %s back off source %s after the failed split: %v",
			minted, logger.SanitizeLogValue(sourceID), err)
	}
}

// setPathFromFiles sets bookID's FilePath from the rows it now holds: the one
// file's path, or the files' common folder. Only FilePath is written, on the
// stored row, so the aggregate recount the move just wrote stays. No rows
// means no write. Failures go to warn.
func (h *VersionsHandler) setPathFromFiles(bookID, label string, warn func(string, ...any)) {
	files, err := h.store.GetBookFiles(bookID)
	if err != nil {
		warn("could not list the %s's files to set its path: %v", label, err)
		return
	}
	if len(files) == 0 {
		return
	}
	path := files[0].FilePath
	if len(files) > 1 {
		path = filesCommonDir(files)
	}
	if _, err := h.modifyBook(bookID, func(cur *database.Book) error {
		if cur.FilePath == path {
			return database.ErrSkipBookWrite
		}
		cur.FilePath = path
		return nil
	}); err != nil {
		warn("could not set the %s's path to %q: %v", label, path, err)
	}
}

// SplitSegmentsToBooks splits selected segments out of a multi-file book into
// independent new books (one per segment), extracting titles from filenames.
// Unlike SplitVersion, the new books are NOT version-linked to the source.
func (h *VersionsHandler) SplitSegmentsToBooks(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs []string `json:"segment_ids" binding:"required"`
		// AsOneBook moves the selected files into ONE new standalone book
		// instead of one book per file. See splitSegmentsToOneBook.
		AsOneBook bool `json:"as_one_book"`
		// Title names the new book when AsOneBook is set; empty means
		// "<source title> (split)". Ignored otherwise (titles come from the
		// file names).
		Title string `json:"title"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	sourceBook, err := h.store.GetBookByID(id)
	if err != nil || sourceBook == nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	if req.AsOneBook {
		h.splitSegmentsToOneBook(c, sourceBook, req.SegmentIDs, req.Title)
		return
	}

	// Every requested ID must be a file of the source: an unknown ID used to
	// be skipped, so the split silently did less than asked.
	allFiles, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files; nothing was written", err)
		return
	}
	fileMap := make(map[string]database.BookFile, len(allFiles))
	for _, f := range allFiles {
		fileMap[f.ID] = f
	}
	segmentIDs, ok := selectSourceFiles(c, sourceBook.ID, allFiles, req.SegmentIDs)
	if !ok {
		return
	}
	authors, err := h.store.GetBookAuthors(sourceBook.ID)
	if err != nil {
		httputil.InternalError(c, "failed to read the source book's authors; nothing was written", err)
		return
	}

	// One new book per selected file. Each file is create -> move -> authors
	// -> external IDs, and the first failure stops the split: later files are
	// not touched, and the response is an error naming what was done, never a
	// 200. A failed move deletes that file's new book (the move batch is
	// atomic, so nothing points at it) and leaves its external IDs where they
	// were; the reassign itself is atomic per mapping, so a failed one leaves
	// that ID on the source rather than on neither book.
	var createdBooks []any
	fail := func(fileID, msg string, extra map[string]any) {
		fields := map[string]any{"created_books": createdBooks, "failed_file_id": fileID, "count": len(createdBooks)}
		maps.Copy(fields, extra)
		versionsLog.Error("split-to-books %s: stopped at file %s after %d book(s): %s",
			logger.SanitizeLogValue(sourceBook.ID), logger.SanitizeLogValue(fileID), len(createdBooks), logger.SanitizeLogValue(msg))
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError, msg, "split_failed", fields)
	}
	for _, fileID := range segmentIDs {
		f := fileMap[fileID]

		// Extract title from file name
		// e.g. "01 ASoIaF 1 - A Game of Thrones.m4b" → "A Game of Thrones"
		title := extractTitleFromSegmentFilename(filepath.Base(f.FilePath))
		if title == "" {
			title = sourceBook.Title + " (split)"
		}

		// BookFile.Duration is SECONDS by convention; only ~2% of rows are
		// milliseconds (iTunes importer). Normalize per row on the file's implied
		// bitrate instead of dividing unconditionally, which was turning correct
		// values into near-zero.
		durationSec := database.NormalizeDurationSec(f.FileSize, f.Duration)
		newBook := &database.Book{
			Title:     title,
			AuthorID:  sourceBook.AuthorID,
			SeriesID:  sourceBook.SeriesID,
			FilePath:  f.FilePath,
			Format:    f.Format,
			Narrator:  sourceBook.Narrator,
			Language:  sourceBook.Language,
			Publisher: sourceBook.Publisher,
			Duration:  &durationSec,
			FileSize:  &f.FileSize,
		}

		created, createErr := h.store.CreateBook(newBook)
		if createErr != nil || created == nil {
			fail(fileID, fmt.Sprintf("failed to create the book for file %s: %v", fileID, createErr), nil)
			return
		}

		// Move the file to the new book (DB-only; nothing on disk moves).
		if mErr := h.store.MoveBookFilesToBook([]string{fileID}, sourceBook.ID, created.ID); mErr != nil {
			if dErr := h.store.DeleteBook(created.ID); dErr != nil {
				fail(fileID, fmt.Sprintf("failed to move file %s into its new book (%v); the empty new book %s could not be deleted: %v",
					fileID, mErr, created.ID, dErr), map[string]any{"created_book_id": created.ID})
				return
			}
			fail(fileID, fmt.Sprintf("failed to move file %s into its new book; it was not moved and the new book was deleted: %v", fileID, mErr), nil)
			return
		}
		createdBooks = append(createdBooks, created)

		if len(authors) > 0 {
			newAuthors := make([]database.BookAuthor, 0, len(authors))
			for _, ba := range authors {
				newAuthors = append(newAuthors, database.BookAuthor{BookID: created.ID, AuthorID: ba.AuthorID, Role: ba.Role})
			}
			if sErr := h.store.SetBookAuthors(created.ID, newAuthors); sErr != nil {
				fail(fileID, fmt.Sprintf("file %s moved to book %s, but copying the authors failed: %v", fileID, created.ID, sErr), nil)
				return
			}
		}

		// Reassign external ID mappings (iTunes PIDs) that belong to the moved file
		if xErr := h.reassignExternalIDsForFiles(sourceBook.ID, created.ID, []database.BookFile{f}); xErr != nil {
			fail(fileID, fmt.Sprintf("file %s moved to book %s, but its external IDs were not all moved; those not moved stay on the source: %v",
				fileID, created.ID, xErr), nil)
			return
		}
	}

	// Source path from its remaining files: FilePath only, on the stored row,
	// so the moves' aggregate recounts stay.
	var warnings []string
	h.setPathFromFiles(sourceBook.ID, "source book", func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	})
	if len(warnings) > 0 {
		fail("", strings.Join(warnings, "; "), nil)
		return
	}

	httputil.RespondWithOK(c, gin.H{
		"created_books": createdBooks,
		"count":         len(createdBooks),
	})
}

// splitSegmentsToOneBook is SplitSegmentsToBooks with as_one_book: the
// selected book_file rows move, as a set, into ONE new book. The new book is
// standalone -- no version group, not primary -- and inherits the source's
// author, series, narrator, language and publisher the way the per-file split
// does. Nothing moves on disk: the rows keep their paths, and the new book's
// FilePath is derived from them.
//
// The move is one MoveBookFilesToBook call, which rewrites every row under
// the new book in one atomic batch (a row is never on both books) and
// recomputes the aggregates -- duration, size, file count -- of BOTH books
// once. The new book is therefore created with no Duration/FileSize and gets
// them from that recompute rather than a hand sum here.
//
// Refused with 400, before anything is written: a selected ID that is not a
// file of the source (an unknown ID must not silently shrink the split), and
// a selection of every file (the source would be left with none -- that is a
// rename of the book, not a split).
func (h *VersionsHandler) splitSegmentsToOneBook(c *gin.Context, sourceBook *database.Book, segmentIDs []string, title string) {
	allFiles, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		httputil.InternalError(c, "failed to list book files", err)
		return
	}
	fileMap := make(map[string]database.BookFile, len(allFiles))
	for _, f := range allFiles {
		fileMap[f.ID] = f
	}

	seen := make(map[string]bool, len(segmentIDs))
	var ids []string
	var selected []database.BookFile
	var unknown []string
	for _, fid := range segmentIDs {
		if seen[fid] {
			continue
		}
		seen[fid] = true
		f, ok := fileMap[fid]
		if !ok {
			unknown = append(unknown, fid)
			continue
		}
		ids = append(ids, fid)
		selected = append(selected, f)
	}
	if len(unknown) > 0 {
		httputil.RespondWithBadRequest(c, fmt.Sprintf("segment_ids not on book %s: %s", sourceBook.ID, strings.Join(unknown, ", ")))
		return
	}
	if len(selected) == len(allFiles) {
		httputil.RespondWithBadRequest(c, "segment_ids selects every file of the book; a split must leave the source at least one file")
		return
	}

	title = strings.TrimSpace(title)
	if title == "" {
		title = sourceBook.Title + " (split)"
	}
	newPath, ok := h.splitTargetPath(c, sourceBook, selected)
	if !ok {
		return
	}
	created, err := h.store.CreateBook(&database.Book{
		Title:     title,
		AuthorID:  sourceBook.AuthorID,
		SeriesID:  sourceBook.SeriesID,
		FilePath:  newPath,
		Format:    sourceBook.Format,
		Narrator:  sourceBook.Narrator,
		Language:  sourceBook.Language,
		Publisher: sourceBook.Publisher,
		// ABS lists only library_state "organized" books, so a new book with
		// no state would vanish from ABS while its source still shows.
		LibraryState: sourceBook.LibraryState,
	})
	if err != nil {
		httputil.InternalError(c, "failed to create book", err)
		return
	}

	// Create and move are two writes (CreateBook and the file-row batch are
	// separate commits), so a failed move leaves a book with no files. Delete
	// it before answering, so a retry never piles up empty books; the move
	// batch is atomic, so no row points at it. Authors are copied only after
	// the move, so the cleanup has nothing else to undo (DeleteBook removes
	// book_authors anyway).
	if err := h.store.MoveBookFilesToBook(ids, sourceBook.ID, created.ID); err != nil {
		if dErr := h.store.DeleteBook(created.ID); dErr != nil {
			versionsLog.Error("split-to-one-book: move into %s failed (%v) and deleting it failed: %v", created.ID, err, dErr)
			httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
				fmt.Sprintf("failed to move files into the new book; no file was moved, and the empty new book %s could not be deleted (%v): %v",
					created.ID, dErr, err),
				"split_move_failed", map[string]any{"created_book_id": created.ID})
			return
		}
		httputil.RespondWithErrorFields(c, http.StatusInternalServerError,
			"failed to move files into the new book; no file was moved and the new book was deleted: "+err.Error(),
			"split_move_failed", nil)
		return
	}

	// From here on the files have moved and that is not undone. A step that
	// fails is reported in "warnings" with status 207, so the operator sees
	// that the split landed but is not whole, rather than a 200 over a log line.
	var warnings []string
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		warnings = append(warnings, msg)
		versionsLog.Warn("split-to-one-book %s -> %s: %s", logger.SanitizeLogValue(sourceBook.ID), created.ID, logger.SanitizeLogValue(msg))
	}

	if authors, aErr := h.store.GetBookAuthors(sourceBook.ID); aErr != nil {
		warn("could not read the source book's authors to copy them onto the new book: %v", aErr)
	} else if len(authors) > 0 {
		newAuthors := make([]database.BookAuthor, 0, len(authors))
		for _, ba := range authors {
			newAuthors = append(newAuthors, database.BookAuthor{BookID: created.ID, AuthorID: ba.AuthorID, Role: ba.Role})
		}
		if sErr := h.store.SetBookAuthors(created.ID, newAuthors); sErr != nil {
			warn("could not copy the source book's authors onto the new book: %v", sErr)
		}
	}

	if xErr := h.reassignExternalIDsForFiles(sourceBook.ID, created.ID, selected); xErr != nil {
		warn("external IDs of the moved files were not all moved to the new book: %v", xErr)
	}

	h.keepSplitSourceAFolder(sourceBook, created.ID, warn)

	// Re-read the new book: the move's aggregate recompute has written its
	// Duration/FileSize since CreateBook returned.
	if fresh, gErr := h.store.GetBookByID(created.ID); gErr == nil && fresh != nil {
		created = fresh
	}

	body := gin.H{
		"book":           created,
		"source_book_id": sourceBook.ID,
		"segments_moved": len(ids),
	}
	if len(warnings) > 0 {
		body["warnings"] = warnings
		c.JSON(http.StatusMultiStatus, body)
		return
	}
	httputil.RespondWithOK(c, body)
}

// keepSplitSourceAFolder settles the split source's FilePath after its files
// moved out. It stays a folder, as a multi-file book's FilePath is everywhere
// else: unchanged (no write at all) while every remaining file is still
// inside it, the usual case. Otherwise it becomes the remaining files' common
// folder, never one file's path, and only when no other live book sits there.
// Every failure is reported through warn.
//
// The write is a ModifyBook that sets FilePath alone on the stored row: the
// move's aggregate recompute has just rewritten its Duration/FileSize, and
// writing back the row read before the move would put the old totals back.
func (h *VersionsHandler) keepSplitSourceAFolder(sourceBook *database.Book, createdID string, warn func(string, ...any)) {
	remaining, err := h.store.GetBookFiles(sourceBook.ID)
	if err != nil {
		warn("could not list the source book's remaining files to check its path: %v", err)
		return
	}
	if len(remaining) == 0 || allFilesWithin(sourceBook.FilePath, remaining) {
		return
	}
	target := filepath.Dir(remaining[0].FilePath)
	if len(remaining) > 1 {
		target = filesCommonDir(remaining)
	}
	occupants, err := h.store.LiveBookIDsAtPath(target)
	if err != nil {
		warn("source book path left at %q: could not check whether %q is free: %v", sourceBook.FilePath, target, err)
		return
	}
	others := slices.DeleteFunc(occupants, func(id string) bool { return id == sourceBook.ID })
	if slices.Contains(others, createdID) {
		warn("source book path left at %q: its remaining files' folder %q is the path of the new book %s",
			sourceBook.FilePath, target, createdID)
		return
	}
	if len(others) > 0 {
		warn("source book path left at %q: its remaining files' folder %q already belongs to book(s) %s",
			sourceBook.FilePath, target, strings.Join(others, ", "))
		return
	}
	if _, err := h.modifyBook(sourceBook.ID, func(cur *database.Book) error {
		cur.FilePath = target
		return nil
	}); err != nil {
		warn("source book path left at %q: could not update it to %q: %v", sourceBook.FilePath, target, err)
	}
}

// allFilesWithin reports whether every file is dir itself or inside it. An
// empty dir contains nothing.
func allFilesWithin(dir string, files []database.BookFile) bool {
	if dir == "" {
		return false
	}
	dir = filepath.Clean(dir)
	for _, f := range files {
		p := filepath.Clean(f.FilePath)
		if p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			return false
		}
	}
	return true
}

// splitTargetPath picks the FilePath of the book splitSegmentsToOneBook is
// about to create, or answers the request and returns ok=false when there is
// no path the new book can own.
//
// A book's FilePath is also its single-valued book:path:<path> lookup key
// (GetBookByFilePath, which the scanner, the organizer's collision checks,
// the iTunes import and autoscan all read), so the new book must never be
// given a path another live book sits on:
//
//   - One file: the file's own path, the scanner's convention for a
//     single-file book (createSingleFileBookFile).
//   - More than one: the ONE folder they are all in. Files from two folders
//     are refused rather than given their common ancestor: that ancestor is
//     typically the source book's own folder, and the new book would take the
//     source's lookup key (the book:path: index is last-writer-wins).
//   - Refused when that path is the source's FilePath, or when any live book
//     already sits there (LiveBookIDsAtPath, the multi-valued index, not the
//     single key). A lookup error refuses too: LiveBookIDsAtPath fails closed,
//     and treating its error as "free" would re-open exactly this hole.
//
// Every refusal happens before anything is written.
func (h *VersionsHandler) splitTargetPath(c *gin.Context, sourceBook *database.Book, selected []database.BookFile) (string, bool) {
	newPath := selected[0].FilePath
	if len(selected) > 1 {
		newPath = filepath.Dir(selected[0].FilePath)
		dirs := []string{newPath}
		for _, f := range selected[1:] {
			if d := filepath.Dir(f.FilePath); !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
		if len(dirs) > 1 {
			httputil.RespondWithErrorFields(c, http.StatusBadRequest,
				"the selected files are in more than one folder; a book's path is one folder, so split one folder at a time",
				"split_files_span_folders", map[string]any{"folders": dirs})
			return "", false
		}
	}
	if filepath.Clean(newPath) == filepath.Clean(sourceBook.FilePath) {
		httputil.RespondWithErrorFields(c, http.StatusBadRequest,
			fmt.Sprintf("the new book's path %q is the source book's own path; two books cannot share it", newPath),
			"split_path_taken", map[string]any{"path": newPath, "book_ids": []string{sourceBook.ID}})
		return "", false
	}
	occupants, err := h.store.LiveBookIDsAtPath(newPath)
	if err != nil {
		httputil.InternalError(c, "could not check whether the new book's path is free; nothing was written", err)
		return "", false
	}
	if len(occupants) > 0 {
		httputil.RespondWithErrorFields(c, http.StatusBadRequest,
			fmt.Sprintf("another book already has the path %q; two books cannot share it", newPath),
			"split_path_taken", map[string]any{"path": newPath, "book_ids": occupants})
		return "", false
	}
	return newPath, true
}

var versionsLog = logger.New("handlers.versions")

// MoveSegments moves segments from one book to another within the same version group.
func (h *VersionsHandler) MoveSegments(c *gin.Context) {
	id := c.Param("id")

	var req struct {
		SegmentIDs   []string `json:"segment_ids" binding:"required"`
		TargetBookID string   `json:"target_book_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}
	if len(req.SegmentIDs) == 0 {
		httputil.RespondWithBadRequest(c, "segment_ids must not be empty")
		return
	}

	if h.store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// 1. Get source and target books
	sourceBook, err := h.store.GetBookByID(id)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", id)
		return
	}

	targetBook, err := h.store.GetBookByID(req.TargetBookID)
	if err != nil {
		httputil.RespondWithNotFound(c, "audiobook", req.TargetBookID)
		return
	}

	// 2. Verify both books are in the same version group
	if sourceBook.VersionGroupID == nil || targetBook.VersionGroupID == nil {
		httputil.RespondWithBadRequest(c, "both books must be in a version group")
		return
	}
	if *sourceBook.VersionGroupID != *targetBook.VersionGroupID {
		httputil.RespondWithBadRequest(c, "books must be in the same version group")
		return
	}

	// 3. Verify the files belong to the source book
	sourceFiles, err := h.store.GetBookFiles(id)
	if err != nil {
		httputil.RespondWithInternalError(c, "failed to list source book files")
		return
	}
	sourceFileMap := make(map[string]bool, len(sourceFiles))
	for _, f := range sourceFiles {
		sourceFileMap[f.ID] = true
	}
	for _, segID := range req.SegmentIDs {
		if !sourceFileMap[segID] {
			httputil.RespondWithBadRequest(c, fmt.Sprintf("file %s does not belong to source book", segID))
			return
		}
	}

	// 4. Collect the files being moved (for external ID reassignment)
	var movedFiles []database.BookFile
	movedSet := make(map[string]bool, len(req.SegmentIDs))
	for _, sid := range req.SegmentIDs {
		movedSet[sid] = true
	}
	for _, f := range sourceFiles {
		if movedSet[f.ID] {
			movedFiles = append(movedFiles, f)
		}
	}

	// 5. Move files
	if err := h.store.MoveBookFilesToBook(req.SegmentIDs, id, req.TargetBookID); err != nil {
		httputil.InternalError(c, "failed to move files", err)
		return
	}

	// 6. Reassign external ID mappings (iTunes PIDs) for moved files
	if xErr := h.reassignExternalIDsForFiles(id, req.TargetBookID, movedFiles); xErr != nil {
		versionsLog.Warn("move-segments: external IDs not all moved from %s to %s: %v",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(req.TargetBookID), xErr)
	}

	httputil.RespondWithOK(c, gin.H{
		"segments_moved": len(req.SegmentIDs),
		"source_book_id": id,
		"target_book_id": req.TargetBookID,
	})
}

// reassignExternalIDsForFiles reassigns external ID mappings (iTunes PIDs) from a
// source book to a target book for the given moved files. Reimplemented from the
// server-package *Server.reassignExternalIDsForFiles, backed by the narrow
// VersionsStore interface.
//
// It returns every read and reassign failure joined, so a caller can report
// them; the reassignment carries on past each one. No failure loses a mapping.
func (h *VersionsHandler) reassignExternalIDsForFiles(sourceBookID, targetBookID string, files []database.BookFile) error {
	if h.store == nil {
		return nil
	}

	mappings, err := h.store.GetExternalIDsForBook(sourceBookID)
	if err != nil {
		return fmt.Errorf("read external IDs of book %s: %w", sourceBookID, err)
	}
	if len(mappings) == 0 {
		return nil
	}

	// Build lookup sets from the moved files
	movedPaths := make(map[string]bool, len(files))
	movedPIDs := make(map[string]bool, len(files))
	for _, f := range files {
		if f.FilePath != "" {
			movedPaths[f.FilePath] = true
		}
		if f.ITunesPersistentID != "" {
			movedPIDs[f.ITunesPersistentID] = true
		}
	}

	// Collect only the mappings that belong to the moved files
	var toMove []database.ExternalIDMapping
	for _, m := range mappings {
		if (m.FilePath != "" && movedPaths[m.FilePath]) ||
			(m.ExternalID != "" && movedPIDs[m.ExternalID]) {
			toMove = append(toMove, m)
		}
	}
	if len(toMove) == 0 {
		return nil
	}

	// Reassign each mapping with ReassignExternalID: one atomic batch that
	// rewrites the primary key and swaps the reverse keys, so a failure leaves
	// the mapping on the source. A mapping whose primary row names some other
	// book is a stale reverse key: the ID already belongs to that book, so
	// there is nothing of the source's to move. It is logged and skipped, not
	// returned -- the caller has already moved the files, and failing the
	// request over a mapping that was never the source's turned a correct
	// split into a 500. Only a real write failure is returned.
	var errs []error
	skipped := 0
	for _, m := range toMove {
		if m.BookID != sourceBookID {
			skipped++
			versionsLog.Warn("external ID %s %s is indexed under book %s but belongs to book %s; left alone",
				logger.SanitizeLogValue(m.Source), logger.SanitizeLogValue(m.ExternalID), logger.SanitizeLogValue(sourceBookID), logger.SanitizeLogValue(m.BookID))
			continue
		}
		if rErr := h.store.ReassignExternalID(m.Source, m.ExternalID, targetBookID); rErr != nil {
			errs = append(errs, fmt.Errorf("reassign %s %s to book %s: %w", m.Source, m.ExternalID, targetBookID, rErr))
		}
	}

	versionsLog.Info("reassigned external ID mappings from book %s to %s: %d matched, %d skipped as another book's, %d failed",
		logger.SanitizeLogValue(sourceBookID), logger.SanitizeLogValue(targetBookID), len(toMove), skipped, len(errs))
	return errors.Join(errs...)
}

// filesCommonDir returns the common parent directory of the given files.
// Copied (pure, unexported) from the server package, which keeps its own copy
// because it is also used by server.go.
func filesCommonDir(files []database.BookFile) string {
	if len(files) == 0 {
		return ""
	}
	common := filepath.Dir(files[0].FilePath)
	for _, f := range files[1:] {
		fDir := filepath.Dir(f.FilePath)
		for common != fDir && !strings.HasPrefix(fDir, common+string(filepath.Separator)) {
			common = filepath.Dir(common)
			if common == "/" || common == "." {
				return common
			}
		}
	}
	return common
}

// extractTitleFromSegmentFilename extracts a probable book title from a segment
// filename. Copied (pure, unexported) from the server package, which keeps its
// own copy because it is also used by server.go.
func extractTitleFromSegmentFilename(filename string) string {
	// Strip extension
	name := strings.TrimSuffix(filename, filepath.Ext(filename))

	// Try to find title after " - " separator (common pattern)
	if _, after, ok := strings.Cut(name, " - "); ok {
		title := strings.TrimSpace(after)
		if title != "" {
			return title
		}
	}

	// Try after " – " (em dash)
	if _, after, ok := strings.Cut(name, " – "); ok {
		title := strings.TrimSpace(after)
		if title != "" {
			return title
		}
	}

	// Strip leading track numbers like "01 ", "01. "
	stripped := regexp.MustCompile(`^\d{1,3}[\s.\-]+`).ReplaceAllString(name, "")
	if stripped != "" {
		return strings.TrimSpace(stripped)
	}

	return name
}
