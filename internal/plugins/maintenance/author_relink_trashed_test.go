// file: internal/plugins/maintenance/author_relink_trashed_test.go
// version: 1.5.0
// guid: c5e8a1f4-2d67-4b93-8f0c-6a9d3e71b254
// last-edited: 2026-10-21

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Every relink-then-DeleteAuthor path in this package used to build its relink
// list from GetBooksByAuthorIDWithRoleCore, which excludes the trash. The
// DeleteAuthor that follows sweeps the author out of EVERY junction row,
// trashed books included, so a trashed book lost its credit and kept a legacy
// AuthorID naming the deleted row; restoring it produced a book with no
// author. These tests run the real store so the trash, the junction sweep and
// the memdb all behave as in production.

func newRelinkTrashedStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mkRelinkTrashedBook creates a book whose junction and legacy AuthorID both
// name legacy, with any extra co-authors after it, soft-deleted when trashed.
func mkRelinkTrashedBook(t *testing.T, s *database.PebbleStore, title string, legacy int, trashed bool, coAuthors ...int) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{
		Title:             title,
		FilePath:          "/relink-trashed/" + title,
		AuthorID:          new(legacy),
		IsPrimaryVersion:  new(true),
		MarkedForDeletion: new(trashed),
	})
	require.NoError(t, err)
	credits := []database.BookAuthor{{BookID: b.ID, AuthorID: legacy, Role: "author", Position: 0}}
	for i, id := range coAuthors {
		credits = append(credits, database.BookAuthor{BookID: b.ID, AuthorID: id, Role: "author", Position: i + 1})
	}
	require.NoError(t, s.SetBookAuthors(b.ID, credits))
	return b.ID
}

// assertCreditedTo checks the junction and legacy AuthorID both name want, and
// that the book is still in the trash (the rewrite must not restore it).
func assertCreditedTo(t *testing.T, s *database.PebbleStore, bookID string, want int, wantTrashed bool) {
	t.Helper()
	credits, err := s.GetBookAuthors(bookID)
	require.NoError(t, err)
	ids := make([]int, 0, len(credits))
	for _, c := range credits {
		ids = append(ids, c.AuthorID)
	}
	require.Equal(t, []int{want}, ids, "book %s junction credits", bookID)

	full, err := s.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.NotNil(t, full.AuthorID, "book %s legacy AuthorID was cleared", bookID)
	require.Equal(t, want, *full.AuthorID, "book %s legacy AuthorID", bookID)
	require.Equal(t, wantTrashed, full.IsSoftDeleted(), "book %s trash state changed", bookID)
}

func requireAuthorGone(t *testing.T, s *database.PebbleStore, id int) {
	t.Helper()
	a, err := s.GetAuthorByID(id)
	require.NoError(t, err)
	require.Nil(t, a, "author %d should have been deleted", id)
}

// mergeAuthorInto is the primitive behind author-conjunction-repair,
// author-duplicate-merge and author-strip-merge's merge branch. It removes the
// merged row with MergeDeleteAuthor: the old id redirects to the survivor, and the
// merged author's pen name resolves to the survivor (DeleteAuthor dropped it).
func TestMergeAuthorInto_MovesTrashedBookCredit(t *testing.T) {
	s := newRelinkTrashedStore(t)
	from, err := s.CreateAuthor("Relink From")
	require.NoError(t, err)
	into, err := s.CreateAuthor("Relink Into")
	require.NoError(t, err)
	_, err = s.CreateAuthorAlias(from.ID, "R. From", "pen_name")
	require.NoError(t, err)

	live := mkRelinkTrashedBook(t, s, "merge-live", from.ID, false)
	trashed := mkRelinkTrashedBook(t, s, "merge-trashed", from.ID, true)

	p := &Plugin{deps: fakeDeps{store: s}}
	n, _, err := p.mergeAuthorInto(context.Background(), *from, *into, false, "op-relink-merge", slog.Default())
	require.NoError(t, err)
	require.Equal(t, 2, n, "both the live and the trashed book must be relinked")

	redirected, err := s.GetAuthorByID(from.ID)
	require.NoError(t, err)
	require.NotNil(t, redirected)
	require.Equal(t, into.ID, redirected.ID, "author %d is merged away: its id redirects to the survivor", from.ID)
	penName, err := s.FindAuthorByAlias("R. From")
	require.NoError(t, err)
	require.NotNil(t, penName, "the merged author's pen name still resolves")
	require.Equal(t, into.ID, penName.ID)
	assertCreditedTo(t, s, live, into.ID, false)
	assertCreditedTo(t, s, trashed, into.ID, true)
}

// unlinkAndDeleteAuthor is author-strip-merge's delete branch: the junk credit
// is removed and the surviving co-author is promoted to the legacy primary.
func TestUnlinkAndDeleteAuthor_PromotesSurvivorOnTrashedBook(t *testing.T) {
	s := newRelinkTrashedStore(t)
	// The delete branch does not look at the name. A real junk name ("Track
	// 01") can no longer be minted -- CreateAuthor refuses it -- so the row
	// stands in for one that predates the creation gate.
	junk, err := s.CreateAuthor("Relink Junk")
	require.NoError(t, err)
	keep, err := s.CreateAuthor("Relink Survivor")
	require.NoError(t, err)

	trashed := mkRelinkTrashedBook(t, s, "strip-trashed", junk.ID, true, keep.ID)

	p := &Plugin{deps: fakeDeps{store: s}}
	n, held, err := p.unlinkAndDeleteAuthor(context.Background(), *junk,
		map[int]bool{junk.ID: true}, nil, false, slog.Default())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, held)

	requireAuthorGone(t, s, junk.ID)
	assertCreditedTo(t, s, trashed, keep.ID, true)
}

// Owner decision 2026-10-21: never leave a book authorless. A junk row that is
// a book's ONLY author credit keeps that credit, the row is not deleted, and
// the book is reported held. A book of the same row that has a real co-author
// is still unlinked (its credit removed, the co-author promoted).
func TestUnlinkAndDeleteAuthor_SoleCreditIsHeldAndRowKept(t *testing.T) {
	s := newRelinkTrashedStore(t)
	junk, err := s.CreateAuthor("Relink Junk")
	require.NoError(t, err)
	keep, err := s.CreateAuthor("Relink Survivor")
	require.NoError(t, err)

	sole := mkRelinkTrashedBook(t, s, "sole-credit", junk.ID, false)
	shared := mkRelinkTrashedBook(t, s, "shared-credit", junk.ID, false, keep.ID)

	p := &Plugin{deps: fakeDeps{store: s}}
	doomed := map[int]bool{junk.ID: true}

	// Dry run and apply agree.
	for _, dry := range []bool{true, false} {
		n, held, err := p.unlinkAndDeleteAuthor(context.Background(), *junk, doomed, nil, dry, slog.Default())
		require.NoError(t, err)
		require.Equal(t, 1, n, "only the co-authored book is unlinked (dry=%v)", dry)
		require.Equal(t, []string{sole}, held, "dry=%v", dry)
	}

	// The row is kept: a delete would sweep the sole book's only credit.
	got, err := s.GetAuthorByID(junk.ID)
	require.NoError(t, err)
	require.NotNil(t, got, "the junk row is kept while a book depends on it")
	assertCreditedTo(t, s, sole, junk.ID, false)
	assertCreditedTo(t, s, shared, keep.ID, false)

	// A book relinked to a real author in the same run is not at risk: a
	// run that has relinked it (dry-run prediction) may delete the row.
	n, held, err := p.unlinkAndDeleteAuthor(context.Background(), *junk, doomed, map[string]bool{sole: true}, true, slog.Default())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Empty(t, held)
}

// runAuthorSplitScan is the maintenance author-split op (maintenance/author.go).
func TestAuthorSplitScan_RelinksTrashedBook(t *testing.T) {
	s := newRelinkTrashedStore(t)
	composite, err := s.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)

	trashed := mkRelinkTrashedBook(t, s, "split-trashed", composite.ID, true)

	p := &Plugin{deps: fakeDeps{store: s}}
	require.NoError(t, p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dry_run":false}`), &opIDReporter{id: "op-dup-merge"}))

	requireAuthorGone(t, s, composite.ID)
	alice, err := s.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.NotNil(t, alice, "split did not create the first individual author")
	bob, err := s.GetAuthorByName("Bob Jones")
	require.NoError(t, err)
	require.NotNil(t, bob)

	credits, err := s.GetBookAuthors(trashed)
	require.NoError(t, err)
	ids := map[int]bool{}
	for _, c := range credits {
		ids[c.AuthorID] = true
	}
	require.Equal(t, map[int]bool{alice.ID: true, bob.ID: true}, ids, "trashed book junction after split")
	full, err := s.GetBookByID(trashed)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, alice.ID, *full.AuthorID)
	require.True(t, full.IsSoftDeleted(), "split must not restore the book from the trash")
}

// The dup-merge guard no longer holds an author back for dangling junction
// rows (book row gone): DeleteAuthor's sweep removes those anyway.
func TestAuthorDuplicateMerge_DanglingRefsDoNotHoldBack(t *testing.T) {
	authors, books, joins, counts, _ := authorDupFixture()
	var w authorDupWrites
	p := newAuthorDupPlugin(authors, books, joins, counts, nil, &w)
	store := p.deps.OpsStore().(*database.MockStore)
	store.GetAllAuthorBookRefCountsFunc = nil
	store.GetAllAuthorBookRefBucketsFunc = func() (map[int]database.AuthorRefBuckets, error) {
		return map[int]database.AuthorRefBuckets{
			1: {Live: 2},
			2: {Live: 1, Dangling: 2}, // two junction rows whose books no longer exist
		}, nil
	}
	require.NoError(t, p.runAuthorDuplicateMerge(context.Background(),
		[]byte(`{"names":["Raymond L. Weil"],"dry_run":false}`), &opIDReporter{id: "op-dup-merge"}))
	require.Equal(t, []int{2}, w.deletedAuthors, "dangling-only extra refs must not hold the row back")
}

// A trashed reference the relink list does not cover still holds back: the
// guard compares live + trashed against what the merge can move.
func TestAuthorDuplicateMerge_UnmovableTrashedRefStillHoldsBack(t *testing.T) {
	authors, books, joins, counts, _ := authorDupFixture()
	var w authorDupWrites
	p := newAuthorDupPlugin(authors, books, joins, counts, nil, &w)
	store := p.deps.OpsStore().(*database.MockStore)
	store.GetAllAuthorBookRefCountsFunc = nil
	store.GetAllAuthorBookRefBucketsFunc = func() (map[int]database.AuthorRefBuckets, error) {
		return map[int]database.AuthorRefBuckets{1: {Live: 2}, 2: {Live: 1, Trashed: 1}}, nil
	}
	require.NoError(t, p.runAuthorDuplicateMerge(context.Background(),
		[]byte(`{"names":["Raymond L. Weil"],"dry_run":false}`), &opIDReporter{id: "op-dup-merge"}))
	require.Empty(t, w.deletedAuthors)
	require.Zero(t, w.total())
}

// relinkAwareBooks turns a fixture's static books-by-author lister into one that
// reflects the fake store's writes, so the post-relink VerifyAuthorUnlinked
// re-read sees a book as unlinked once the op rewrote its junction without the
// author and moved its legacy primary. primaryWritten reports the AuthorID the
// op wrote to a book, if it wrote one; a nil AuthorID with written=true means
// "moved somewhere else" for fixtures that record only the book ID.
func relinkAwareBooks(
	list func(int) ([]database.BookCore, error),
	junctionWrites map[string][]database.BookAuthor,
	primaryWritten func(bookID string) (authorID *int, written bool),
) func(int) ([]database.BookCore, error) {
	return func(authorID int) ([]database.BookCore, error) {
		if list == nil {
			return nil, nil
		}
		books, err := list(authorID)
		if err != nil {
			return nil, err
		}
		var out []database.BookCore
		for _, b := range books {
			stillJunction := true
			if credits, rewritten := junctionWrites[b.ID]; rewritten {
				stillJunction = false
				for _, c := range credits {
					if c.AuthorID == authorID {
						stillJunction = true
					}
				}
			}
			primary := b.AuthorID
			if id, written := primaryWritten(b.ID); written {
				primary = id
			}
			stillLegacy := primary != nil && *primary == authorID
			if stillJunction || stillLegacy {
				out = append(out, b)
			}
		}
		return out, nil
	}
}

// writtenIDs adapts a fixture that records only the IDs of books it updated.
func writtenIDs(ids func() []string) func(string) (*int, bool) {
	return func(bookID string) (*int, bool) {
		if slices.Contains(ids(), bookID) {
			return nil, true
		}
		return nil, false
	}
}

// noMergeDeleteStore is a Store without the AuthorMergeDeleter capability:
// embedding the interface hides *PebbleStore's MergeDeleteAuthor, and it does
// not unwrap to the store underneath.
type noMergeDeleteStore struct{ database.Store }

// A store that cannot merge-delete makes every merge path here refuse before any
// credit moves. Falling back to DeleteAuthor would delete the merged author's
// aliases with it.
func TestMergePaths_AStoreWithoutTheMergeDeleteCapabilityRefuses(t *testing.T) {
	// Each path gets its own authors and book, so one path's mistake cannot
	// hide another's.
	world := func(t *testing.T, name string) (*database.PebbleStore, *database.Author, *database.Author, string) {
		t.Helper()
		s := newRelinkTrashedStore(t)
		from, err := s.CreateAuthor("Capless From " + name)
		require.NoError(t, err)
		into, err := s.CreateAuthor("Capless Into " + name)
		require.NoError(t, err)
		return s, from, into, mkRelinkTrashedBook(t, s, "capless-"+name, from.ID, false)
	}
	untouched := func(t *testing.T, s *database.PebbleStore, from *database.Author, book string) {
		t.Helper()
		still, err := s.GetAuthorByID(from.ID)
		require.NoError(t, err)
		require.NotNil(t, still)
		require.Equal(t, from.ID, still.ID, "the author was not deleted")
		assertCreditedTo(t, s, book, from.ID, false)
	}

	t.Run("mergeAuthorInto", func(t *testing.T) {
		s, from, into, book := world(t, "merge")
		hidden := noMergeDeleteStore{Store: s}
		p := &Plugin{deps: fakeDeps{store: hidden}}
		n, _, err := p.mergeAuthorInto(context.Background(), *from, *into, false, "op-capless", slog.Default())
		require.ErrorIs(t, err, database.ErrAuthorMergeDeleteUnsupported)
		require.Zero(t, n, "no credit moved")
		untouched(t, s, from, book)
	})
	t.Run("author-id-repair", func(t *testing.T) {
		s, from, into, book := world(t, "idrepair")
		hidden := noMergeDeleteStore{Store: s}
		p := &Plugin{deps: fakeDeps{store: hidden}}
		ch, wrote, ledger := p.authorIDMergeOne(hidden, false, "op-capless", "capless", *from, *into, slog.Default())
		require.Equal(t, authorIDMergeFailed, ch.Outcome)
		require.Contains(t, ch.Error, "AuthorMergeDeleter")
		require.False(t, wrote, "nothing written")
		require.Zero(t, ledger)
		untouched(t, s, from, book)
	})
}
