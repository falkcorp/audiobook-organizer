// file: internal/scheduler/author_split_relink_trashed_test.go
// version: 1.0.0
// guid: 3b8d5e72-0c4f-4a91-9e26-d71f84a3c6b0
// last-edited: 2026-09-12

package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// scheduler.author-split-scan relinks a composite author's books to the split
// authors and then DeleteAuthor's the composite. DeleteAuthor sweeps the author
// out of EVERY book_authors row, trashed books included, so a relink list that
// skipped the trash erased a trashed book's credit. Real store throughout.

type splitRelinkReporter struct {
	opsregistry.Reporter // nil: any method not overridden below panics
	logs                 []string
}

func (r *splitRelinkReporter) UpdateProgress(_, _ int, _ string) error { return nil }
func (r *splitRelinkReporter) IsCanceled() bool                        { return false }
func (r *splitRelinkReporter) SetCurrentItem(string)                   {}
func (r *splitRelinkReporter) Logger() *slog.Logger                    { return slog.New(slog.DiscardHandler) }
func (r *splitRelinkReporter) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.logs = append(r.logs, message)
	return nil
}

type failSplitSetBookAuthorsStore struct {
	*database.PebbleStore
	failBook string
}

func (f failSplitSetBookAuthorsStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	if bookID == f.failBook {
		return errors.New("injected SetBookAuthors failure")
	}
	return f.PebbleStore.SetBookAuthors(bookID, authors)
}

func newSplitRelinkStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// mkSplitTrashedBook creates a trashed book credited (junction + legacy) to authorID.
func mkSplitTrashedBook(t *testing.T, st *database.PebbleStore, authorID int) string {
	t.Helper()
	b, err := st.CreateBook(&database.Book{
		Title:             "split-scan-trashed",
		FilePath:          "/split-scan-trashed/book.m4b",
		AuthorID:          new(authorID),
		IsPrimaryVersion:  new(true),
		MarkedForDeletion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: authorID, Role: "author"}}))
	return b.ID
}

func runSplitScanOp(t *testing.T, store ExtraOpsStore) *splitRelinkReporter {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, NewExtraOpsRegistrar(store, ExtraOpsDeps{}).RegisterAuthorSplitScanOp(reg))
	def, ok := reg.Def("scheduler.author-split-scan")
	require.True(t, ok)
	rep := &splitRelinkReporter{}
	require.NoError(t, def.Run(context.Background(), nil, rep))
	return rep
}

func TestAuthorSplitScanOp_RelinksTrashedBook(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	trashed := mkSplitTrashedBook(t, st, composite.ID)

	runSplitScanOp(t, st)

	gone, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "composite author should have been deleted")
	alice, err := st.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.NotNil(t, alice)
	bob, err := st.GetAuthorByName("Bob Jones")
	require.NoError(t, err)
	require.NotNil(t, bob)

	credits, err := st.GetBookAuthors(trashed)
	require.NoError(t, err)
	ids := map[int]bool{}
	for _, c := range credits {
		ids[c.AuthorID] = true
	}
	require.Equal(t, map[int]bool{alice.ID: true, bob.ID: true}, ids, "trashed book junction after split")
	full, err := st.GetBookByID(trashed)
	require.NoError(t, err)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, alice.ID, *full.AuthorID)
	require.True(t, full.IsSoftDeleted(), "split must not restore the book from the trash")
}

// When the trashed book's relink fails, the composite must survive: deleting it
// would erase the only credit that book has.
func TestAuthorSplitScanOp_KeepsCompositeWhileTrashedBookStillCredits(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Carol White & Dan Black")
	require.NoError(t, err)
	trashed := mkSplitTrashedBook(t, st, composite.ID)

	rep := runSplitScanOp(t, failSplitSetBookAuthorsStore{PebbleStore: st, failBook: trashed})

	kept, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "composite deleted while a trashed book still credits it")
	credits, err := st.GetBookAuthors(trashed)
	require.NoError(t, err)
	require.Len(t, credits, 1)
	require.Equal(t, composite.ID, credits[0].AuthorID)
	full, err := st.GetBookByID(trashed)
	require.NoError(t, err)
	require.True(t, full.IsSoftDeleted())
	refused := false
	for _, l := range rep.logs {
		if strings.Contains(l, "NOT deleted") {
			refused = true
		}
	}
	require.True(t, refused, "refusal not reported; logs: %v", rep.logs)
}
