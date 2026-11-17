// file: internal/dedup/merge_journaled_phase_boundary_test.go
// version: 1.0.0
// guid: 91c5e7b2-4d08-4a63-b1f9-6e3a0d8c2b47
// last-edited: 2026-11-17

package dedup

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// failRedirectStore fails the raw write of a trash-restore redirect, the merge's
// first write after it takes its version-group locks.
type failRedirectStore struct{ database.Store }

func (s *failRedirectStore) Unwrap() database.Store { return s.Store }
func (s *failRedirectStore) SetRaw(key string, value []byte) error {
	// merge.groupRedirectPrefix; unexported there, and a rename must break this test.
	if strings.HasPrefix(key, "merge:group-redirect:") {
		return errors.New("injected: redirect write failed")
	}
	return s.Store.SetRaw(key, value)
}

// movesAfterFirstReadStore reads a book and then moves it to another version group,
// as a concurrent writer would between MergeBooks' planning read and its locks.
type movesAfterFirstReadStore struct {
	database.Store
	f     *vptest.Fixture
	id    string
	to    string
	moved bool
}

func (s *movesAfterFirstReadStore) Unwrap() database.Store { return s.Store }
func (s *movesAfterFirstReadStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.Store.GetBookByID(id)
	if id == s.id && !s.moved {
		s.moved = true
		to := s.to
		if _, merr := s.f.S.ModifyBook(id, func(r *database.Book) error {
			r.VersionGroupID = &to
			return nil
		}); merr != nil {
			return nil, merr
		}
	}
	return b, err
}

// The read-only phase of MergeBooks ends before the version-group locks. A failure
// from there on is NOT proof that nothing was written, even when the merge in fact
// wrote nothing (a changed membership) or only a redirect (a failed redirect write
// is itself a write), so the journaled merge leaves its rows provisional and
// pinned. Closing one on the strength of either would release the pin of a merge
// that may have written.
func TestMergeBooksJournaled_FailuresAfterTheLocksLeaveTheRowProvisionalAndPinned(t *testing.T) {
	cases := map[string]func(f *vptest.Fixture, l string) database.Store{
		"redirect write fails": func(f *vptest.Fixture, _ string) database.Store { return &failRedirectStore{Store: f.S} },
		"membership changes after the planning read": func(f *vptest.Fixture, l string) database.Store {
			return &movesAfterFirstReadStore{Store: f.S, f: f, id: l, to: "Z"}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			f := vptest.New(t)
			var svc database.Store = f.S
			engine, es := engineOn(t, f, svc)
			k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
			l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
			baseline := newestBefore(t, f.S, []string{k, l})
			// Swap the service's store for the faulting one; the engine reads through f.S.
			engine.mergeService = merge.NewService(mk(f, l))

			_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
			require.Error(t, err)
			if name == "redirect write fails" {
				require.ErrorContains(t, err, "injected: redirect write failed", "precondition: the fault fired")
			} else {
				require.ErrorIs(t, err, versionprimary.ErrMembershipChanged, "precondition: the fault fired")
			}
			var refused *merge.RefusedBeforeWriteError
			require.False(t, errors.As(err, &refused), "not a proven-before-write refusal")
			require.Len(t, keys, 1)

			e, gerr := es.GetAutoMergeJournalEntry(keys[0])
			require.NoError(t, gerr)
			require.True(t, e.Provisional)
			require.Zero(t, e.AbortedAt, "the row is NOT closed")
			require.ErrorContains(t, engine.UnmergeAuto(keys[0]), "never finalized")

			// Its pin is intact: whatever the merge wrote above the baseline, the
			// first such snapshot of each book survives a prune.
			editTitle(t, f.S, k, 3)
			editTitle(t, f.S, l, 3)
			for _, id := range []string{k, l} {
				want := firstSnapshotAbove(t, f.S, id, baseline[id])
				require.Contains(t, pinnedAfterPrune(t, f.S, id, 1), want, fmt.Sprintf("%s lost its snapshot", id))
			}
		})
	}
}
