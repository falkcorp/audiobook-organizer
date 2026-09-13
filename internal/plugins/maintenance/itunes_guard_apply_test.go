// file: internal/plugins/maintenance/itunes_guard_apply_test.go
// version: 1.0.0
// guid: 5e2a7c90-4d1b-4f38-a6e2-9b3d0c8f1a57
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

const applyGuardMediaRoot = "/srv/media/Audiobooks"

func withApplyGuardMediaRoot(t *testing.T) {
	t.Helper()
	prev := config.Snapshot().ITunes
	config.Mutate(func(c *config.Config) { c.ITunes.MediaRoot = applyGuardMediaRoot })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.ITunes = prev }) })
}

// applyWriteTrap forwards reads to a real store and fails on any merge write.
type applyWriteTrap struct {
	database.Store
	t *testing.T
}

func (w *applyWriteTrap) trip(op string) error {
	w.t.Errorf("%s reached the store after an iTunes refusal", op)
	return fmt.Errorf("write trap: %s", op)
}
func (w *applyWriteTrap) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, w.trip("UpdateBook")
}
func (w *applyWriteTrap) DeleteBook(string) error { return w.trip("DeleteBook") }
func (w *applyWriteTrap) MoveBookFilesToBook([]string, string, string) error {
	return w.trip("MoveBookFilesToBook")
}
func (w *applyWriteTrap) MoveBookFilesToBookBulk([]database.BookFileMove, string) error {
	return w.trip("MoveBookFilesToBookBulk")
}

func requireAllLive(t *testing.T, store database.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "book %s must still exist", id)
		require.False(t, b.IsSoftDeleted(), "book %s must not be merged away", id)
	}
}

func TestApplyMultidisc_RefusesITunesMember(t *testing.T) {
	withApplyGuardMediaRoot(t)
	store := newApplyTestStore(t)
	a, b := ulid.Make().String(), ulid.Make().String()
	mkDupBook(t, store, a, "Disc 1", applyGuardMediaRoot+"/Author/Book/d1.mp3")
	mkDupBook(t, store, b, "Disc 2", "/srv/managed/Author/Book/d2.mp3")

	apply := ApplyMultidisc(store, merge.NewService(&applyWriteTrap{Store: store, t: t}))
	err := apply(context.Background(), multidiscItem(t, "/srv/any", []string{a, b}))
	require.ErrorIs(t, err, merge.ErrITunesProtected)
	requireAllLive(t, store, a, b)
}

func TestApplyDuplicateOf_RefusesITunesCanonical(t *testing.T) {
	withApplyGuardMediaRoot(t)
	store := newApplyTestStore(t)
	canonical, debris := "zzz"+ulid.Make().String(), "aaa"+ulid.Make().String()
	mkDupBook(t, store, canonical, "The Real Book", applyGuardMediaRoot+"/Author/book.mp3")
	mkDupBook(t, store, debris, "Debris", "/srv/managed/junk/d1.mp3")
	cands := &fakeCandidates{byEntity: map[string][]database.DedupCandidate{
		debris: {pendingCand(debris, canonical)},
	}}

	apply := ApplyDuplicateOf(store, merge.NewService(&applyWriteTrap{Store: store, t: t}), cands)
	err := apply(context.Background(), duplicateOfItem(t, "/srv/managed/junk", []string{debris}))
	require.ErrorIs(t, err, merge.ErrITunesProtected)
	requireAllLive(t, store, canonical, debris)
}

// merge-same-path-dupes counts an iTunes refusal in its own bucket, not as a
// merge failure and not as merged.
func TestMergeSamePath_ITunesRefusalIsCountedAsRefused(t *testing.T) {
	path := "/srv/managed/Author/Book.m4b"
	store := &mergeFakeStore{
		books: []database.BookCore{{ID: "a", FilePath: path}, {ID: "b", FilePath: path}},
		rows: map[string][]database.BookFile{
			"a": {{ID: "r1", BookID: "a", FilePath: path, FileHash: "H"}},
			"b": {{ID: "r2", BookID: "b", FilePath: path, FileHash: "H"}},
		},
	}
	refuse := func([]string, string) (int, error) {
		return 0, fmt.Errorf("merge: %w", &merge.ITunesProtectedError{BookID: "a", Path: path})
	}
	plan, err := planMergeSamePathDupes(context.Background(), store, refuse,
		mergeSamePathParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, 1, plan.RefusedITunes)
	require.Equal(t, 0, plan.MergeFailed)
	require.Equal(t, 0, plan.Merged)
	require.Contains(t, plan.summary(), "itunes=1")
}
