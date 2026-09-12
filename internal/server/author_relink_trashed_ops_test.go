// file: internal/server/author_relink_trashed_ops_test.go
// version: 1.0.0
// guid: 6f2a9c41-7d38-4e05-b1a6-93c8e2d05f17
// last-edited: 2026-09-12

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/cache"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// entities.author-merge and the AI merge/alias apply relink an author's books
// and then DeleteAuthor it. DeleteAuthor sweeps the author out of EVERY
// book_authors row, trashed books included, so a relink list that skipped the
// trash (GetBooksByAuthorIDWithRoleCore) erased a trashed book's credit. These
// tests drive the registered op on a real store, where the trash, the junction
// sweep and the memdb behave as in production.

type relinkOpReporter struct {
	opsregistry.Reporter // nil: any method not overridden below panics
	logs                 []string
}

func (r *relinkOpReporter) UpdateProgress(_, _ int, _ string) error { return nil }
func (r *relinkOpReporter) IsCanceled() bool                        { return false }
func (r *relinkOpReporter) SetCurrentItem(string)                   {}
func (r *relinkOpReporter) Logger() *slog.Logger                    { return slog.New(slog.DiscardHandler) }
func (r *relinkOpReporter) Log(_ slog.Level, message string, _ ...slog.Attr) error {
	r.logs = append(r.logs, message)
	return nil
}

func (r *relinkOpReporter) logged(substr string) bool {
	for _, l := range r.logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func relinkOpDef(t *testing.T, register func(*opsregistry.Registry) error, id string) opsregistry.OperationDef {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	require.NoError(t, register(reg))
	def, ok := reg.Def(id)
	require.True(t, ok, "op %s not registered", id)
	return def
}

func newRelinkOpStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newRelinkOpServer(store database.Store) *Server {
	return &Server{
		store:        store,
		dedupCache:   cache.New[gin.H]("relink-trashed-test", time.Hour),
		authorsCache: cache.New[*audiobookspkg.AuthorWithCountListResponse]("relink-trashed-authors", time.Hour),
	}
}

// mkRelinkOpBook creates a book whose junction and legacy AuthorID both name
// authorID, soft-deleted when trashed.
func mkRelinkOpBook(t *testing.T, st *database.PebbleStore, title string, authorID int, trashed bool) string {
	t.Helper()
	b, err := st.CreateBook(&database.Book{
		Title:             title,
		FilePath:          "/relink-trashed-ops/" + title,
		AuthorID:          new(authorID),
		IsPrimaryVersion:  new(true),
		MarkedForDeletion: new(trashed),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{
		{BookID: b.ID, AuthorID: authorID, Role: "author", Position: 0},
	}))
	return b.ID
}

func junctionIDs(t *testing.T, st *database.PebbleStore, bookID string) []int {
	t.Helper()
	credits, err := st.GetBookAuthors(bookID)
	require.NoError(t, err)
	ids := make([]int, 0, len(credits))
	for _, c := range credits {
		ids = append(ids, c.AuthorID)
	}
	return ids
}

func requireTrashed(t *testing.T, st *database.PebbleStore, bookID string) *database.Book {
	t.Helper()
	full, err := st.GetBookByID(bookID)
	require.NoError(t, err)
	require.NotNil(t, full)
	require.True(t, full.IsSoftDeleted(), "book %s was restored from the trash", bookID)
	return full
}

// requireSourceDeleted checks the merged-away author row is gone by scanning
// every author row. GetAuthorByID is not usable here: it follows the author
// tombstone, so after a merge it answers the old id with the KEPT author.
func requireSourceDeleted(t *testing.T, st *database.PebbleStore, id int) {
	t.Helper()
	authors, err := st.GetAllAuthors()
	require.NoError(t, err)
	for _, a := range authors {
		require.NotEqual(t, id, a.ID, "merged-away author %d should have been deleted", id)
	}
}

// failSetBookAuthorsStore fails the junction rewrite for one book, standing in
// for any per-book failure the relink loop logs and skips.
type failSetBookAuthorsStore struct {
	*database.PebbleStore
	failBook string
}

func (f failSetBookAuthorsStore) SetBookAuthors(bookID string, authors []database.BookAuthor) error {
	if bookID == f.failBook {
		return errors.New("injected SetBookAuthors failure")
	}
	return f.PebbleStore.SetBookAuthors(bookID, authors)
}

func runAuthorMergeOp(t *testing.T, s *Server, keep database.Author, merge int) *relinkOpReporter {
	t.Helper()
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	params, err := json.Marshal(authorMergeOpParams{KeepID: keep.ID, MergeIDs: []int{merge}, KeepName: keep.Name})
	require.NoError(t, err)
	rep := &relinkOpReporter{}
	require.NoError(t, def.Run(context.Background(), params, rep))
	return rep
}

func TestAuthorMergeOp_MovesTrashedBookCredit(t *testing.T) {
	st := newRelinkOpStore(t)
	from, err := st.CreateAuthor("Merge Op From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Merge Op Into")
	require.NoError(t, err)
	live := mkRelinkOpBook(t, st, "merge-op-live", from.ID, false)
	trashed := mkRelinkOpBook(t, st, "merge-op-trashed", from.ID, true)

	runAuthorMergeOp(t, newRelinkOpServer(st), *into, from.ID)

	require.Equal(t, []int{into.ID}, junctionIDs(t, st, live))
	require.Equal(t, []int{into.ID}, junctionIDs(t, st, trashed), "trashed book lost its credit to the merge")
	full := requireTrashed(t, st, trashed)
	require.NotNil(t, full.AuthorID)
	require.Equal(t, into.ID, *full.AuthorID, "trashed book legacy AuthorID")

	requireSourceDeleted(t, st, from.ID)
}

// When a trashed book's relink fails, the source author must survive: the
// delete would otherwise erase the only credit that book has.
func TestAuthorMergeOp_KeepsSourceWhileTrashedBookStillCredits(t *testing.T) {
	st := newRelinkOpStore(t)
	from, err := st.CreateAuthor("Merge Op Held From")
	require.NoError(t, err)
	into, err := st.CreateAuthor("Merge Op Held Into")
	require.NoError(t, err)
	trashed := mkRelinkOpBook(t, st, "merge-op-held-trashed", from.ID, true)

	rep := runAuthorMergeOp(t, newRelinkOpServer(failSetBookAuthorsStore{PebbleStore: st, failBook: trashed}), *into, from.ID)

	kept, err := st.GetAuthorByID(from.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "source author deleted while a trashed book still credits it")
	require.Equal(t, from.ID, kept.ID, "source author deleted and tombstoned while a trashed book still credits it")
	require.Equal(t, []int{from.ID}, junctionIDs(t, st, trashed))
	requireTrashed(t, st, trashed)
	require.True(t, rep.logged("NOT deleted"), "refusal not reported; logs: %v", rep.logs)
}

// The AI apply rewrites the junction only (see the comment at its DeleteAuthor
// in ai_ops.go): the trashed book's junction credit moves, its legacy AuthorID
// still names the deleted author, and the tombstone resolves that id to KeepID.
func TestAIAuthorMergeApplyOp_MovesTrashedBookCredit(t *testing.T) {
	for _, action := range []string{"merge", "alias"} {
		t.Run(action, func(t *testing.T) {
			st := newRelinkOpStore(t)
			from, err := st.CreateAuthor("AI Apply From " + action)
			require.NoError(t, err)
			into, err := st.CreateAuthor("AI Apply Into " + action)
			require.NoError(t, err)
			trashed := mkRelinkOpBook(t, st, "ai-apply-trashed-"+action, from.ID, true)

			s := newRelinkOpServer(st)
			def := relinkOpDef(t, s.RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
			params, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{{
				GroupIndex: 0, Action: action, CanonicalName: into.Name, KeepID: into.ID, MergeIDs: []int{from.ID},
			}}})
			require.NoError(t, err)
			require.NoError(t, def.Run(context.Background(), params, &relinkOpReporter{}))

			require.Equal(t, []int{into.ID}, junctionIDs(t, st, trashed), "trashed book lost its credit to the AI %s", action)
			full := requireTrashed(t, st, trashed)
			require.NotNil(t, full.AuthorID)
			require.Equal(t, from.ID, *full.AuthorID, "AI apply is junction-only; legacy AuthorID is left for the tombstone")

			requireSourceDeleted(t, st, from.ID)
			canonical, err := st.GetAuthorTombstone(from.ID)
			require.NoError(t, err)
			require.Equal(t, into.ID, canonical, "tombstone must resolve the leftover legacy AuthorID")
		})
	}
}
