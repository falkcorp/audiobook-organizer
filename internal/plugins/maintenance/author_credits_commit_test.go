// file: internal/plugins/maintenance/author_credits_commit_test.go
// version: 1.0.0
// guid: 8d2f6a40-1b73-4c95-a6e8-35f0c9b24d71
// last-edited: 2026-11-19

package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The maintenance author jobs write a book's credits and its primary author in
// ONE commit (ModifyBookCredits) that reads the user's lock inside it. These
// tests run that commit against a real PebbleStore, because the guarantee is
// about the store's own write lock and batch, which a mock cannot show. Fixtures
// are Batman characters and storylines only.

// commitHookDB is a PebbleStore whose credits-and-row write first runs a hook:
// the instant a user's lock or edit lands after every check the caller made and
// before the commit takes the book's write lock.
type commitHookDB struct {
	*database.PebbleStore
	beforeCommit func(bookID string)
}

func (d commitHookDB) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if d.beforeCommit != nil {
		d.beforeCommit(id)
	}
	return d.PebbleStore.ModifyBookCredits(id, fn)
}

// noCreditsStore is a store whose book credits and row cannot be written as one
// commit (no database.BookCreditsModifier), though it can merge-delete: only the
// capability the credit write needs is missing.
type noCreditsStore struct {
	database.Store
	pebble *database.PebbleStore
}

func (n noCreditsStore) MergeDeleteAuthor(fromID, intoID int) (*database.AuthorMergeDeleteResult, error) {
	return n.pebble.MergeDeleteAuthor(fromID, intoID)
}

func lockBookAuthor(t *testing.T, s *database.PebbleStore, bookID string) {
	t.Helper()
	require.NoError(t, s.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: bookID, Field: database.FieldKeyAuthorName, OverrideLocked: true,
	}))
}

func creditIDs(t *testing.T, s *database.PebbleStore, bookID string) []int {
	t.Helper()
	cs, err := s.GetBookAuthors(bookID)
	require.NoError(t, err)
	var out []int
	for _, c := range cs {
		out = append(out, c.AuthorID)
	}
	return out
}

func primaryOf(t *testing.T, s *database.PebbleStore, bookID string) int {
	t.Helper()
	b, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, b)
	require.NotNil(t, b.AuthorID)
	return *b.AuthorID
}

// unlinkWorld is one book credited to a junk row and a real co-author.
func unlinkWorld(t *testing.T) (s *database.PebbleStore, junk, keep, other database.Author, book string) {
	t.Helper()
	s = newRelinkTrashedStore(t)
	j, err := s.CreateAuthor("Crime Alley Junk")
	require.NoError(t, err)
	k, err := s.CreateAuthor("Barbara Gordon")
	require.NoError(t, err)
	o, err := s.CreateAuthor("Selina Kyle")
	require.NoError(t, err)
	return s, *j, *k, *o, mkRelinkTrashedBook(t, s, "Year One", j.ID, false, k.ID)
}

func runUnlink(t *testing.T, db database.Store, junk database.Author) (unlinked int, held, lockHeld []string, err error) {
	t.Helper()
	return unlinkAndDeleteAuthorVia(context.Background(), t6g1HeadStore{db}, t6g1HeadWriter{db}, junk, map[int]bool{junk.ID: true}, nil, false, slog.Default())
}

// With no lock, the credits and the primary move in the one commit.
func TestUnlink_WritesCreditsAndPrimaryTogether(t *testing.T) {
	s, junk, keep, _, book := unlinkWorld(t)

	n, held, lockHeld, err := runUnlink(t, s, junk)

	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, held)
	require.Empty(t, lockHeld)
	require.Equal(t, []int{keep.ID}, creditIDs(t, s, book))
	require.Equal(t, keep.ID, primaryOf(t, s, book), "the co-author was not promoted with the credit removal")
}

// A lock the user saves after the unlink's own check and before its commit holds
// the book whole: credits and primary stay exactly as they were, the book is
// reported as held, and there is nothing to put back.
func TestUnlink_ALockSavedBeforeTheCommitHoldsTheBook(t *testing.T) {
	s, junk, keep, _, book := unlinkWorld(t)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) { lockBookAuthor(t, s, id) }}

	n, _, lockHeld, err := runUnlink(t, db, junk)

	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, []string{book}, lockHeld)
	require.Equal(t, []int{junk.ID, keep.ID}, creditIDs(t, s, book), "the locked book's credits changed")
	require.Equal(t, junk.ID, primaryOf(t, s, book), "the locked book's primary changed")
}

// The user's own edit, made after the unlink read the credits, is never written
// over. Round 1 wrote back the list it had read before; the commit now derives
// from the credits it finds, and refuses when they are not the ones it planned
// for (errUnlinkInputsChanged), so the edit stands.
func TestUnlink_AUserCreditEditBeforeTheCommitSurvives(t *testing.T) {
	s, junk, _, other, book := unlinkWorld(t)
	edited := []database.BookAuthor{
		{BookID: book, AuthorID: junk.ID, Role: "author", Position: 0},
		{BookID: book, AuthorID: other.ID, Role: "author", Position: 1},
	}
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) {
		require.NoError(t, s.SetBookAuthors(id, edited))
	}}

	_, _, _, err := runUnlink(t, db, junk)

	require.Error(t, err)
	require.ErrorIs(t, err, errUnlinkInputsChanged)
	require.Equal(t, []int{junk.ID, other.ID}, creditIDs(t, s, book), "the user's credit edit was overwritten")
	require.Equal(t, junk.ID, primaryOf(t, s, book))
}

// A lock and a credit edit that land together: still nothing is written and the
// edit stands (the compare refuses before the lock decision matters).
func TestUnlink_ALockAndAnEditTogetherWriteNothing(t *testing.T) {
	s, junk, _, other, book := unlinkWorld(t)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) {
		lockBookAuthor(t, s, id)
		require.NoError(t, s.SetBookAuthors(id, []database.BookAuthor{
			{BookID: id, AuthorID: junk.ID, Role: "author", Position: 0},
			{BookID: id, AuthorID: other.ID, Role: "author", Position: 1},
		}))
	}}

	n, _, _, err := runUnlink(t, db, junk)

	require.Error(t, err)
	require.Zero(t, n)
	require.Equal(t, []int{junk.ID, other.ID}, creditIDs(t, s, book), "the user's edit was overwritten")
}

// An edit to a field the lock does not cover, made after the read, survives the
// unlink: the commit hydrates the row it finds, it does not write a stale copy.
func TestUnlink_AnEditToAnotherFieldBeforeTheCommitSurvives(t *testing.T) {
	s, junk, keep, _, book := unlinkWorld(t)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		b.Title = "Year One (Annotated)"
		_, err = s.UpdateBook(id, b)
		require.NoError(t, err)
	}}

	n, _, _, err := runUnlink(t, db, junk)

	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := s.GetBookByID(book)
	require.NoError(t, err)
	require.Equal(t, "Year One (Annotated)", got.Title, "the concurrent title edit was lost")
	require.Equal(t, keep.ID, *got.AuthorID)
}

// A lock set that cannot be read inside the commit writes nothing and is an
// error, not a hold.
func TestUnlink_UnreadableLocksInsideTheCommitWriteNothing(t *testing.T) {
	s, junk, keep, _, book := unlinkWorld(t)
	db := unreadableLocksDB{PebbleStore: s, reads: map[string]int{}}

	n, _, lockHeld, err := runUnlink(t, db, junk)

	require.Error(t, err)
	require.ErrorIs(t, err, database.ErrFieldLocksUnavailable)
	require.Zero(t, n)
	require.Empty(t, lockHeld)
	require.Equal(t, []int{junk.ID, keep.ID}, creditIDs(t, s, book))
	require.Equal(t, junk.ID, primaryOf(t, s, book))
}

// unreadableLocksDB fails every field-state read after the first, per book: the
// caller's own check passes and the read inside the commit fails.
type unreadableLocksDB struct {
	*database.PebbleStore
	reads map[string]int
}

func (d unreadableLocksDB) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	d.reads[id]++
	if d.reads[id] > 1 {
		return nil, errors.New("field states: store closed")
	}
	return d.PebbleStore.GetMetadataFieldStates(id)
}

// A store that cannot write credits and row as one commit is refused: there is no
// fallback to two writes with a window between them. Nothing is written and the
// row is kept.
func TestUnlink_AStoreWithoutTheCreditsCommitIsRefused(t *testing.T) {
	s, junk, keep, _, book := unlinkWorld(t)
	hidden := noCreditsStore{Store: s, pebble: s}

	n, _, _, err := runUnlink(t, hidden, junk)

	require.Error(t, err)
	require.ErrorIs(t, err, database.ErrBookCreditsUnsupported)
	require.Zero(t, n)
	require.Equal(t, []int{junk.ID, keep.ID}, creditIDs(t, s, book))
	still, gerr := s.GetAuthorByID(junk.ID)
	require.NoError(t, gerr)
	require.NotNil(t, still)
	require.Equal(t, junk.ID, still.ID, "the row was deleted though the unlink did not run")
}

// relinkWorld is one book credited to a junk row (primary) and a real co-author,
// and the real author the junk credit moves to.
func relinkWorld(t *testing.T) (s *database.PebbleStore, junk, into, co database.Author, book database.BookCore) {
	t.Helper()
	s = newRelinkTrashedStore(t)
	j, err := s.CreateAuthor("Hush 2")
	require.NoError(t, err)
	i, err := s.CreateAuthor("Alfred Pennyworth")
	require.NoError(t, err)
	c, err := s.CreateAuthor("Harley Quinn")
	require.NoError(t, err)
	id := mkRelinkTrashedBook(t, s, "Hush 2: A Gotham Adventure", j.ID, false, c.ID)
	return s, *j, *i, *c, database.BookCore{ID: id, AuthorID: &j.ID}
}

// The title relink's credit move is one commit too: the credit and the primary
// land together, and the co-author is kept.
func TestRelinkBookCredit_MovesCreditAndPrimaryTogether(t *testing.T) {
	s, junk, into, co, book := relinkWorld(t)

	held, err := relinkBookCredit(s, book, junk, into)

	require.NoError(t, err)
	require.False(t, held)
	require.ElementsMatch(t, []int{co.ID, into.ID}, creditIDs(t, s, book.ID))
	require.Equal(t, into.ID, primaryOf(t, s, book.ID))
	got, err := s.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Author)
	require.Equal(t, into.ID, got.Author.ID, "the denormalized author names the old row")
}

// A lock saved after the caller's checks and before the commit holds the whole
// move: held, with credits and primary exactly as they were.
func TestRelinkBookCredit_ALockSavedBeforeTheCommitHoldsTheBook(t *testing.T) {
	s, junk, into, co, book := relinkWorld(t)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) { lockBookAuthor(t, s, id) }}

	held, err := relinkBookCredit(db, book, junk, into)

	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, []int{junk.ID, co.ID}, creditIDs(t, s, book.ID))
	require.Equal(t, junk.ID, primaryOf(t, s, book.ID))
}

// The credits are derived from what the book has at the commit, never from a
// list read earlier: a co-author the user added in between is still there after
// the move, where a write-back of the pre-read list would have dropped it.
func TestRelinkBookCredit_ACreditAddedBeforeTheCommitSurvives(t *testing.T) {
	s, junk, into, co, book := relinkWorld(t)
	added, err := s.CreateAuthor("Oracle")
	require.NoError(t, err)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) {
		require.NoError(t, s.SetBookAuthors(id, []database.BookAuthor{
			{BookID: id, AuthorID: junk.ID, Role: "author", Position: 0},
			{BookID: id, AuthorID: co.ID, Role: "author", Position: 1},
			{BookID: id, AuthorID: added.ID, Role: "author", Position: 2},
		}))
	}}

	held, err := relinkBookCredit(db, book, junk, into)

	require.NoError(t, err)
	require.False(t, held)
	require.ElementsMatch(t, []int{co.ID, added.ID, into.ID}, creditIDs(t, s, book.ID), "the user's added credit was lost")
}

// A primary another writer moved off the junk row meanwhile is left where it is:
// the credit moves, the primary does not follow a stale read.
func TestRelinkBookCredit_APrimaryMovedMeanwhileIsNotRepointed(t *testing.T) {
	s, junk, into, co, book := relinkWorld(t)
	db := commitHookDB{PebbleStore: s, beforeCommit: func(id string) {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		b.AuthorID = &co.ID
		_, err = s.UpdateBook(id, b)
		require.NoError(t, err)
	}}

	held, err := relinkBookCredit(db, book, junk, into)

	require.NoError(t, err)
	require.False(t, held)
	require.Equal(t, co.ID, primaryOf(t, s, book.ID), "the primary was repointed from a stale read")
}

// A store without the credits commit is refused: an error, nothing written.
func TestRelinkBookCredit_AStoreWithoutTheCreditsCommitIsRefused(t *testing.T) {
	s, junk, into, co, book := relinkWorld(t)
	held, err := relinkBookCredit(creditsRefused{PebbleStore: s}, book, junk, into)

	require.Error(t, err)
	require.False(t, held)
	require.Equal(t, []int{junk.ID, co.ID}, creditIDs(t, s, book.ID))
}

// creditsRefused answers ModifyBookCredits the way t6g1HeadWriter does for a
// store without the capability.
type creditsRefused struct{ *database.PebbleStore }

func (creditsRefused) ModifyBookCredits(string, func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	return nil, database.BookCreditsEdit{}, fmt.Errorf("%w", database.ErrBookCreditsUnsupported)
}

// A merge on a store that cannot write credits and row together is refused at the
// first book: no credit moves and the author is kept.
func TestMergeAuthorInto_AStoreWithoutTheCreditsCommitIsRefused(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Cap From", "Undo Cap Into", 0)
	hidden := noCreditsStore{Store: w.st, pebble: w.st}

	n, _, err := mergeAuthorIntoVia(context.Background(), t6g1HeadStore{hidden}, t6g1HeadWriter{hidden}, w.from, w.into, false, "op-merge-cap", slog.Default())

	require.Error(t, err)
	require.ErrorIs(t, err, database.ErrBookCreditsUnsupported)
	require.False(t, errors.Is(err, errAuthorMergeUserLocked))
	require.Zero(t, n)
	requireMergeHeld(t, w, w.books[0], "")
	requireMergeHeld(t, w, w.books[1], "")
}

// ---- the held bucket across the three callers ----

// logReporter captures the op's own log, so a test reads the counts the op
// reports (outcomes, books relinked) and not a mock's call list.
type logReporter struct {
	fakeReporter
	buf bytes.Buffer
}

func (r *logReporter) OpID() string { return "op-held" }
func (r *logReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&r.buf, nil))
}

func (r *logReporter) line(t *testing.T, contains string) string {
	t.Helper()
	for l := range strings.SplitSeq(r.buf.String(), "\n") {
		if strings.Contains(l, contains) {
			return l
		}
	}
	t.Fatalf("no log line containing %q; log was:\n%s", contains, r.buf.String())
	return ""
}

// heldMergeWorld: the junk row `from` and its twin `into`, with two books that
// credit `from` as a co-author beside their lead. The first book is open; the
// second is the one a test locks. Names are Gotham's.
type heldMergeWorld struct {
	st       *database.PebbleStore
	from     database.Author
	into     database.Author
	lead     database.Author
	open     string
	locked   string
	byBook   map[string][]int
	primarys map[string]int
}

func newHeldMergeWorld(t *testing.T, fromName, intoName string, intoBooks int) *heldMergeWorld {
	t.Helper()
	st := newRelinkTrashedStore(t)
	w := &heldMergeWorld{st: st, byBook: map[string][]int{}, primarys: map[string]int{}}
	w.from = *mustAuthorNamed(t, st, fromName)
	w.into = *mustAuthorNamed(t, st, intoName)
	w.lead = *mustAuthorNamed(t, st, "Barbara Gordon")
	w.open = mkRelinkTrashedBook(t, st, "Year One", w.lead.ID, false, w.from.ID)
	w.locked = mkRelinkTrashedBook(t, st, "The Long Halloween", w.lead.ID, false, w.from.ID)
	for i := range intoBooks {
		mkRelinkTrashedBook(t, st, "Dark Victory "+strconv.Itoa(i), w.into.ID, false)
	}
	for _, id := range []string{w.open, w.locked} {
		w.byBook[id] = creditIDs(t, st, id)
		w.primarys[id] = primaryOf(t, st, id)
	}
	return w
}

// requireBookAsItWas: the book still credits `from` and has the primary it had.
func (w *heldMergeWorld) requireBookAsItWas(t *testing.T, id string) {
	t.Helper()
	require.Equal(t, w.byBook[id], creditIDs(t, w.st, id), "book %s credits changed", id)
	require.Equal(t, w.primarys[id], primaryOf(t, w.st, id), "book %s primary changed", id)
}

func (w *heldMergeWorld) requireBookMoved(t *testing.T, id string) {
	t.Helper()
	require.NotContains(t, creditIDs(t, w.st, id), w.from.ID, "book %s still credits the merged author", id)
	require.Contains(t, creditIDs(t, w.st, id), w.into.ID, "book %s does not credit the survivor", id)
}

func (w *heldMergeWorld) requireFromKept(t *testing.T) {
	t.Helper()
	got, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, w.from.ID, got.ID, "the author was merged away though a locked book still credits it")
}

// midRunLock is a store whose lock read of one book sees it unlocked once (the
// merge's pre-scan) and locked from then on (the read inside the write): the
// user saved the lock in between.
func midRunLock(st *database.PebbleStore, book string) lateLockDB {
	return lateLockDB{PebbleStore: st, book: book, reads: new(int)}
}

type lateLockDB struct {
	*database.PebbleStore
	book  string
	reads *int
}

func (d lateLockDB) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	if id != d.book {
		return d.PebbleStore.GetMetadataFieldStates(id)
	}
	*d.reads++
	if *d.reads == 1 {
		return d.PebbleStore.GetMetadataFieldStates(id)
	}
	return []database.MetadataFieldState{{BookID: id, Field: database.FieldKeyAuthorName, OverrideLocked: true}}, nil
}

func outcomeCount(t *testing.T, line, key string) int {
	t.Helper()
	m := regexp.MustCompile(key + `:(\d+)`).FindStringSubmatch(line)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func counter(t *testing.T, line, key string) int {
	t.Helper()
	m := regexp.MustCompile(key + `=(\d+)`).FindStringSubmatch(line)
	require.NotNil(t, m, "no %s= in %q", key, line)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}

func runConjRepair(t *testing.T, st database.Store) (*logReporter, *conjRepairDeps) {
	t.Helper()
	deps := &conjRepairDeps{fakeDeps: fakeDeps{store: st}}
	rep := &logReporter{}
	require.NoError(t, New(deps).runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), rep))
	return rep, deps
}

// conjunction repair: a locked book holds the whole author. The row is counted
// held (not failed), no book moved, nothing was written so no cache is dropped.
func TestAuthorConjunctionRepair_AUserLockedBookHoldsTheAuthorAndIsNotAFailure(t *testing.T) {
	w := newHeldMergeWorld(t, "& Selina Kyle", "Selina Kyle", 0)
	lockBookAuthor(t, w.st, w.locked)

	rep, deps := runConjRepair(t, w.st)

	done := rep.line(t, "author-conjunction-repair: done")
	require.Equal(t, 1, outcomeCount(t, done, conjRepairHeldUserLocked), "outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, conjRepairFailed), "a held author was counted as failed: %s", done)
	require.Zero(t, counter(t, done, "books_relinked"))
	w.requireBookAsItWas(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
	require.Zero(t, deps.authorsInvalidated, "nothing was written, the author cache was dropped")
}

// conjunction repair, a lock saved mid-run: the book before it moved, so the run
// counts it and DOES drop the author cache even though no row was merged.
func TestAuthorConjunctionRepair_AMidRunLockCountsTheMovedBooksAndInvalidatesTheCache(t *testing.T) {
	w := newHeldMergeWorld(t, "& Selina Kyle", "Selina Kyle", 0)

	rep, deps := runConjRepair(t, midRunLock(w.st, w.locked))

	done := rep.line(t, "author-conjunction-repair: done")
	require.Equal(t, 1, outcomeCount(t, done, conjRepairHeldUserLocked), "outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, conjRepairFailed), "outcomes: %s", done)
	require.Equal(t, 1, counter(t, done, "books_relinked"))
	w.requireBookMoved(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
	require.Equal(t, 1, deps.authorsInvalidated, "books moved but the author cache was not dropped")
	require.Equal(t, 1, deps.dedupInvalidated)
}

// conjunction repair, control: unlocked, the row merges and counts as merged.
func TestAuthorConjunctionRepair_UnlockedBooksMergeAndInvalidateTheCache(t *testing.T) {
	w := newHeldMergeWorld(t, "& Selina Kyle", "Selina Kyle", 0)

	rep, deps := runConjRepair(t, w.st)

	done := rep.line(t, "author-conjunction-repair: done")
	require.Equal(t, 1, outcomeCount(t, done, conjRepairMerged), "outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, conjRepairHeldUserLocked))
	require.Equal(t, 2, counter(t, done, "books_relinked"))
	require.Equal(t, 1, deps.authorsInvalidated)
}

func runDupMerge(t *testing.T, st database.Store) (*logReporter, *conjRepairDeps) {
	t.Helper()
	deps := &conjRepairDeps{fakeDeps: fakeDeps{store: st}}
	rep := &logReporter{}
	require.NoError(t, New(deps).runAuthorDuplicateMerge(context.Background(),
		[]byte(`{"names":["Edward Nygma"],"dry_run":false}`), rep))
	return rep, deps
}

// duplicate merge: the same three-way split of held / failed / merged.
func TestAuthorDuplicateMerge_AUserLockedBookHoldsTheRowAndIsNotAFailure(t *testing.T) {
	w := newHeldMergeWorld(t, "EDWARD NYGMA", "Edward Nygma", 3)
	lockBookAuthor(t, w.st, w.locked)

	rep, deps := runDupMerge(t, w.st)

	done := rep.line(t, "author-duplicate-merge: done")
	require.Equal(t, 1, outcomeCount(t, done, authorDupRowHeldUserLocked), "row outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, authorDupRowFailed), "a held row was counted as failed: %s", done)
	require.Zero(t, counter(t, done, "books_relinked"))
	w.requireBookAsItWas(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
	require.Zero(t, deps.authorsInvalidated)
}

func TestAuthorDuplicateMerge_AMidRunLockCountsTheMovedBooksAndInvalidatesTheCache(t *testing.T) {
	w := newHeldMergeWorld(t, "EDWARD NYGMA", "Edward Nygma", 3)

	rep, deps := runDupMerge(t, midRunLock(w.st, w.locked))

	done := rep.line(t, "author-duplicate-merge: done")
	require.Equal(t, 1, outcomeCount(t, done, authorDupRowHeldUserLocked), "row outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, authorDupRowFailed), "row outcomes: %s", done)
	require.Equal(t, 1, counter(t, done, "books_relinked"))
	w.requireBookMoved(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
	require.Equal(t, 1, deps.authorsInvalidated, "books moved but the author cache was not dropped")
}

func TestAuthorDuplicateMerge_UnlockedBooksMergeAndInvalidateTheCache(t *testing.T) {
	w := newHeldMergeWorld(t, "EDWARD NYGMA", "Edward Nygma", 3)

	rep, deps := runDupMerge(t, w.st)

	done := rep.line(t, "author-duplicate-merge: done")
	require.Equal(t, 1, outcomeCount(t, done, authorDupRowMerged), "row outcomes: %s", done)
	require.Zero(t, outcomeCount(t, done, authorDupRowHeldUserLocked))
	require.Equal(t, 2, counter(t, done, "books_relinked"))
	require.Equal(t, 1, deps.authorsInvalidated)
}

func runStripMergeOnStore(t *testing.T, st database.Store) string {
	t.Helper()
	rep := &summaryReporter{}
	require.NoError(t, New(&fakeDeps{store: st}).runAuthorStripMerge(context.Background(), []byte(`{"apply":true}`), rep))
	return rep.summary(t)
}

// strip-merge: a numbered row folded into its real author is held whole by one
// locked book; the summary counts merges-held-user-locked, not failed.
func TestAuthorStripMerge_AUserLockedBookHoldsTheNumberedRowAndIsNotAFailure(t *testing.T) {
	w := newHeldMergeWorld(t, "001-147 Selina Kyle", "Selina Kyle", 1)
	lockBookAuthor(t, w.st, w.locked)

	summary := runStripMergeOnStore(t, w.st)

	wantSummary(t, summary, "merges-held-user-locked=1 ", "failed=0 ", "merged=0 ", "books-touched=0 ")
	w.requireBookAsItWas(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
}

func TestAuthorStripMerge_AMidRunLockCountsTheMovedBooksAsTouched(t *testing.T) {
	w := newHeldMergeWorld(t, "001-147 Selina Kyle", "Selina Kyle", 1)

	summary := runStripMergeOnStore(t, midRunLock(w.st, w.locked))

	wantSummary(t, summary, "merges-held-user-locked=1 ", "failed=0 ", "merged=0 ", "books-touched=1 ")
	w.requireBookMoved(t, w.open)
	w.requireBookAsItWas(t, w.locked)
	w.requireFromKept(t)
}

func TestAuthorStripMerge_UnlockedNumberedRowMerges(t *testing.T) {
	w := newHeldMergeWorld(t, "001-147 Selina Kyle", "Selina Kyle", 1)

	summary := runStripMergeOnStore(t, w.st)

	wantSummary(t, summary, "merged=1 ", "merges-held-user-locked=0 ", "failed=0 ", "books-touched=2 ")
}

// ---- journal rows of a relink that did not write are voided (m2) ----

// lastJournal is the row that stands per id: voiding rewrites a row under its id.
func lastJournal(f *relinkFixture, bookID, changeType string) (database.OperationChange, bool) {
	var out database.OperationChange
	found := false
	for _, c := range f.journal {
		if c.BookID == bookID && c.ChangeType == changeType {
			out, found = c, true
		}
	}
	return out, found
}

// newAuthorRelinkFixture is the Gotham shape where the real author has no row
// yet, so the relink creates it: only bk-t carries evidence ("Dick Grayson").
func newAuthorRelinkFixture() *relinkFixture {
	f := batmanRelinkFixture()
	f.books = f.books[:2]
	f.authors = []database.Author{{ID: 100, Name: "Hush 2"}, {ID: 200, Name: "01 Hush 2"}}
	f.fetched = map[string]string{"bk-t": "Dick Grayson"}
	return f
}

// The success control: the create row and the credits row both stand, and
// relink-journal-rows counts both.
func TestRelinkJournal_ASuccessKeepsBothRowsAndCountsThem(t *testing.T) {
	f := newAuthorRelinkFixture()
	_, summary := f.run(t, relinkAllParams)

	create, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkAuthorCreate)
	require.True(t, ok, "no create-author row was journaled")
	require.False(t, create.Voided)
	credits, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkCredits)
	require.True(t, ok, "no credits row was journaled")
	require.False(t, credits.Voided)
	wantSummary(t, summary, "relinked=1 ", "relink-new-authors=1 ", "relink-journal-rows=2 ")
}

// A book held at the commit: its credits row is voided (nothing moved), the
// create-author row STANDS (the author was created), and the count drops by one.
func TestRelinkJournal_AHeldBookVoidsItsCreditsRowOnly(t *testing.T) {
	f := newAuthorRelinkFixture()
	f.lockAtWrite = map[string]bool{"bk-t": true}
	_, summary := f.run(t, relinkAllParams)

	credits, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkCredits)
	require.True(t, ok, "the credits row was never journaled, so the void is untested")
	require.True(t, credits.Voided, "the credits row of a held book stands")
	create, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkAuthorCreate)
	require.True(t, ok)
	require.False(t, create.Voided, "the author was created, its row must stand")
	wantSummary(t, summary, "relink-skipped-user-locked=1 ", "relink-new-authors=1 ", "relink-journal-rows=1 ")
}

// A credit write that fails (an unreadable lock inside the commit): the credits
// row is voided and the book is a failure, not a hold.
func TestRelinkJournal_AFailedWriteVoidsItsCreditsRow(t *testing.T) {
	f := newAuthorRelinkFixture()
	f.lockErrAtWrite = map[string]error{"bk-t": errors.New("field states for bk-t: store closed")}
	_, summary := f.run(t, relinkAllParams)

	credits, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkCredits)
	require.True(t, ok)
	require.True(t, credits.Voided, "the credits row of a failed write stands")
	wantSummary(t, summary, "relink-failed=1 ", "relink-skipped-user-locked=0 ", "relink-journal-rows=1 ")
}

// An author creation that fails: the create-author row is voided (no author was
// made), and the book is a failure with no credits row at all.
func TestRelinkJournal_AFailedCreationVoidsItsRow(t *testing.T) {
	f := newAuthorRelinkFixture()
	f.createErr = errors.New("author store unavailable")
	_, summary := f.run(t, relinkAllParams)

	create, ok := lastJournal(f, "bk-t", ChangeTypeTitleRelinkAuthorCreate)
	require.True(t, ok, "the create-author row was never journaled, so the void is untested")
	require.True(t, create.Voided, "the create-author row stands though no author was created")
	_, credited := lastJournal(f, "bk-t", ChangeTypeTitleRelinkCredits)
	require.False(t, credited, "a credits row was journaled for a book whose author was never created")
	wantSummary(t, summary, "relink-failed=1 ", "relink-new-authors=0 ", "relink-journal-rows=0 ")
}
