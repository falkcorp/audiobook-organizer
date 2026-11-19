// file: internal/plugins/maintenance/author_split_relink_trashed_test.go
// version: 1.2.3
// guid: 3b8d5e72-0c4f-4a91-9e26-d71f84a3c6b0
// last-edited: 2026-11-19

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// maintenance.author-split-scan (which absorbed scheduler.author-split-scan,
// 04-P4e) relinks a composite author's books to the split
// authors and then DeleteAuthor's the composite. DeleteAuthor sweeps the author
// out of EVERY book_authors row, trashed books included, so a relink list that
// skipped the trash erased a trashed book's credit. Real store throughout.

// splitFaultStore is a real PebbleStore that can fail the split's one per-book
// commit (credits and primary author together) for a chosen book and run a hook
// just before it, which is where another writer's change lands in production.
// Methods not overridden are the real store's. Its ModifyBookCredits is the
// capability the split's writer resolves, so every book goes through it.
type splitFaultStore struct {
	*database.PebbleStore
	failCommit   string // ModifyBookCredits fails for this book
	beforeCommit func(bookID string)
}

func (f splitFaultStore) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if f.beforeCommit != nil {
		f.beforeCommit(id)
	}
	if id == f.failCommit {
		return nil, database.BookCreditsEdit{}, errors.New("injected ModifyBookCredits failure")
	}
	return f.PebbleStore.ModifyBookCredits(id, fn)
}

func newSplitRelinkStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	st, err := database.NewPebbleStoreInMemory(t.TempDir())
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

// runSplitScanOp applies the split through the survivor's own Run, with the
// store handed over as the plugin's OpsStore. The reporter is returned so a
// test can read what the op said.
func runSplitScanOp(t *testing.T, store database.Store) *fakeReporter {
	t.Helper()
	p := &Plugin{deps: &fakeDeps{store: store}}
	rep := &fakeReporter{}
	require.NoError(t, p.authorSplitScanDef().Run(context.Background(), json.RawMessage(`{"dry_run":false}`), rep))
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

	rep := runSplitScanOp(t, splitFaultStore{PebbleStore: st, failCommit: trashed})

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

// ResumeRequeue is safe because an interrupted apply can be run again. The first
// pass here fails one book's commit: that book is left exactly as it was (credits
// and primary both still on the composite), so the composite must survive; the
// second, healthy pass is the requeue, and it splits the book, deletes the
// composite and credits nobody twice.
func TestAuthorSplitScan_RequeueAfterPartialApply(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Erin Gray & Finn Green")
	require.NoError(t, err)
	done := mkSplitTrashedBook(t, st, composite.ID)
	stuck := mkSplitTrashedBook(t, st, composite.ID)

	runSplitScanOp(t, splitFaultStore{PebbleStore: st, failCommit: stuck})

	kept, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.NotNil(t, kept, "the interrupted pass must leave the composite for the rerun")
	erin, err := st.GetAuthorByName("Erin Gray")
	require.NoError(t, err)
	require.NotNil(t, erin)
	finn, err := st.GetAuthorByName("Finn Green")
	require.NoError(t, err)
	require.NotNil(t, finn)
	half, err := st.GetBookByID(stuck)
	require.NoError(t, err)
	require.NotNil(t, half.AuthorID)
	require.Equal(t, composite.ID, *half.AuthorID, "the failed commit must not have moved the primary")
	require.Equal(t, map[int]int{composite.ID: 1}, creditCounts(t, st, stuck), "the failed commit must not have moved the credits")
	finished, err := st.GetBookByID(done)
	require.NoError(t, err)
	require.NotNil(t, finished.AuthorID)
	require.Equal(t, erin.ID, *finished.AuthorID, "the healthy book is fully split by the first pass")

	rep := runSplitScanOp(t, st)

	gone, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.Nil(t, gone, "the rerun should finish the split")
	for _, id := range []string{done, stuck} {
		credits, err := st.GetBookAuthors(id)
		require.NoError(t, err)
		ids := map[int]int{}
		for _, c := range credits {
			ids[c.AuthorID]++
		}
		require.Equal(t, map[int]int{erin.ID: 1, finn.ID: 1}, ids, "book %s credits after the rerun", id)
		full, err := st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, full.AuthorID)
		require.Equal(t, erin.ID, *full.AuthorID, "book %s primary after the rerun", id)
		require.True(t, full.IsSoftDeleted())
	}
	require.NotContains(t, strings.Join(rep.logs, "\n"), "NOT deleted")

	// A further pass has nothing left to split.
	require.Contains(t, strings.Join(runSplitScanOp(t, st).logs, "\n"), "Split 0 composite authors")
}

// The def carries the merged contract (04-P4e): the loser ID as a FormerID, the
// loser's gate and timeout, and the survivor's own concurrency key and resume
// policy.
func TestAuthorSplitScanDef_MergeContract(t *testing.T) {
	def := New(fakeDeps{}).authorSplitScanDef()
	require.Equal(t, []string{"scheduler.author-split-scan"}, def.FormerIDs)
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
	require.GreaterOrEqual(t, def.Timeout, 2*time.Hour)
	require.Equal(t, sdk.ResumeRequeue, def.ResumePolicy)
	require.Equal(t, "maintenance.author-split-scan", def.ConcurrencyKey)
	// The write set keeps it from interleaving with another writer of the rows
	// the apply rewrites.
	require.ElementsMatch(t, []sdk.Resource{sdk.ResBooks, sdk.ResAuthors}, def.Writes)
}
