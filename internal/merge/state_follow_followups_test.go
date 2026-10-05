// file: internal/merge/state_follow_followups_test.go
// version: 1.0.0
// guid: 1056ccb5-89ca-4487-9ff7-533be24d6942
// last-edited: 2026-10-05

package merge

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// sfFaultStore wraps the fixture's PebbleStore (embedded, so every capability
// still resolves to it) and injects the faults the #3762 follow-ups need.
type sfFaultStore struct {
	*database.PebbleStore

	mu sync.Mutex
	// failStateFollowJournal fails a sibling-move journal write that carries
	// a state follow (the persist hook's write and every one after it); the
	// pre-write journal, which has none, still lands.
	failStateFollowJournal bool
	// failPendingRepair fails every pending user-state repair write.
	failPendingRepair bool
	// failStateOn fails SetUserBookState for this book.
	failStateOn string
	// failGetBookOnce fails the next GetBookByID of each id, once.
	failGetBookOnce map[string]bool
}

func (s *sfFaultStore) SetRaw(key string, value []byte) error {
	s.mu.Lock()
	failJournal, failRepair := s.failStateFollowJournal, s.failPendingRepair
	s.mu.Unlock()
	if failJournal && strings.HasPrefix(key, siblingJournalPrefix) && strings.Contains(string(value), `"state_follows"`) {
		return fmt.Errorf("injected journal write failure for %s", key)
	}
	if failRepair && strings.HasPrefix(key, PendingUserStateRepairPrefix) {
		return fmt.Errorf("injected repair-record write failure for %s", key)
	}
	return s.PebbleStore.SetRaw(key, value)
}

func (s *sfFaultStore) SetUserBookState(st *database.UserBookState) error {
	s.mu.Lock()
	fail := s.failStateOn != "" && st.BookID == s.failStateOn
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected SetUserBookState failure for %s", st.BookID)
	}
	return s.PebbleStore.SetUserBookState(st)
}

func (s *sfFaultStore) GetBookByID(id string) (*database.Book, error) {
	s.mu.Lock()
	fail := s.failGetBookOnce[id]
	delete(s.failGetBookOnce, id)
	s.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("injected GetBookByID failure for %s", id)
	}
	return s.PebbleStore.GetBookByID(id)
}

// sfProgress is the user's ProgressPct on bookID when they have a state and a
// position there, else 0.
func sfProgress(t *testing.T, s *database.PebbleStore, userID, bookID string) int {
	t.Helper()
	st, err := s.GetUserBookState(userID, bookID)
	require.NoError(t, err)
	pos, err := s.ListUserPositionsForBook(userID, bookID)
	require.NoError(t, err)
	if st == nil || len(pos) == 0 {
		return 0
	}
	return st.ProgressPct
}

// sfCurrentBook is where bookID's ABS id resolves to now.
func sfCurrentBook(t *testing.T, s *database.PebbleStore, bookID string) string {
	t.Helper()
	ids := database.AsSyncIdentityStore(s)
	sid, has, err := ids.GetSyncIDForBook(bookID)
	require.NoError(t, err)
	require.True(t, has, "book %s has a sync id", bookID)
	item, err := ids.ResolveSyncItem(sid)
	require.NoError(t, err)
	require.NotNil(t, item)
	return item.CurrentBookID
}

// sfFlagHolderMerge is the shape the follow-ups are about: survivor k (the
// better recording, not organized) in group H; loser l (a worse recording a
// user is 60% through) in group G with its organized sibling ls, which takes
// the merged group's primary flag. The merge sends l's state to ls.
type sfFlagHolderMerge struct {
	f        *vptest.Fixture
	fs       *sfFaultStore
	svc      *Service
	user     *database.User
	k, l, ls string
}

func newFlagHolderMerge(t *testing.T) *sfFlagHolderMerge {
	t.Helper()
	f := siblingFixture(t)
	m := &sfFlagHolderMerge{f: f}
	m.k = f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
	m.l = f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "false", State: "imported"})
	m.ls = f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "true"})
	setAudio(t, f, m.k, "m4b", 128)
	setAudio(t, f, m.l, "mp3", 64)
	setAudio(t, f, m.ls, "mp3", 64)
	m.user = seedSyncUser(t, f.S)
	seedProgress(t, f.S, m.user.ID, m.l, 60)
	_, err := database.AsSyncIdentityStore(f.S).MintOrGetSyncID(m.l)
	require.NoError(t, err)
	m.fs = &sfFaultStore{PebbleStore: f.S}
	m.svc = NewService(m.fs)
	return m
}

func (m *sfFlagHolderMerge) merge(t *testing.T) *Result {
	t.Helper()
	res, err := m.svc.MergeBooks([]string{m.l, m.k}, "")
	require.NoError(t, err)
	require.Equal(t, m.k, res.PrimaryID, "audio quality picks the survivor")
	require.Equal(t, m.ls, res.GroupPrimaryID, "the organized sibling holds the flag")
	require.Equal(t, m.ls, res.StateHolderID)
	return res
}

// Owner decision 2026-10-05 13:00: a lower-quality copy holding progress,
// merged with a higher-quality copy holding none. The higher-quality copy
// survives, and the progress ends up on the flag holder.
func TestMergeBooks_LowerQualityCopyWithProgress(t *testing.T) {
	m := newFlagHolderMerge(t)
	m.merge(t)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.ls), "the progress is on the flag holder")
	require.Zero(t, sfProgress(t, m.f.S, m.user.ID, m.k))
	has, err := BookHasCarryableUserState(m.f.S, m.l)
	require.NoError(t, err)
	require.False(t, has, "nothing is left on the retired copy for the purge to drop")
	require.Equal(t, m.ls, sfCurrentBook(t, m.f.S, m.l))
}

// Item 3: when the journal cannot record the follow, the state still moves to
// the flag holder (unjournaled, with its own repair record) instead of being
// left on the retired loser for the purge.
func TestFollowOntoFlagHolder_JournalWriteFailsStillMoves(t *testing.T) {
	m := newFlagHolderMerge(t)
	m.fs.failStateFollowJournal = true
	m.merge(t)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.ls), "the state followed the flag holder")
	has, err := BookHasCarryableUserState(m.f.S, m.l)
	require.NoError(t, err)
	require.False(t, has)
	require.Equal(t, m.ls, sfCurrentBook(t, m.f.S, m.l))
	require.Empty(t, pendingKeys(t, m.f.S), "the move completed, so its repair record is gone")
}

// Item 3, both halves: the journal write fails, the move fails and no repair
// record can be written. The merge must report the failure and the state must
// still be on the loser, where the purge guard keeps it.
func TestFollowOntoFlagHolder_NothingHoldsTheMoveReportsFailure(t *testing.T) {
	m := newFlagHolderMerge(t)
	m.fs.failStateFollowJournal = true
	m.fs.failPendingRepair = true
	m.fs.failStateOn = m.ls
	_, err := m.svc.MergeBooks([]string{m.l, m.k}, "")
	require.ErrorContains(t, err, "listening state could not be carried")
	has, herr := BookHasCarryableUserState(m.f.S, m.l)
	require.NoError(t, herr)
	require.True(t, has, "the user's state is still on the loser")
}

// Item 2 + NIT 1: merge, whole undo (the flag holder goes back, l's state is
// refollowed onto the survivor, journaled), then the per-loser undo
// UnmergeAuto runs puts the state back on l and clears its redirect. The
// journal's UndoneAt is the whole undo's, not rewritten by the second.
func TestUndoSiblingMove_RefollowIsJournaled(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)

	_, err := m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.k), "the whole undo puts l's state on the survivor")
	require.Equal(t, m.k, sfCurrentBook(t, m.f.S, m.l))
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.Equal(t, SiblingJournalUndone, j.Status)
	require.NotNil(t, j.UndoneAt)
	undoneAt := *j.UndoneAt
	require.Len(t, j.StateFollows, 2)
	require.True(t, j.StateFollows[0].Undone)
	require.Equal(t, StateFollow{LoserID: m.l, HolderID: m.k}, StateFollow{LoserID: j.StateFollows[1].LoserID, HolderID: j.StateFollows[1].HolderID},
		"the refollow onto the survivor is journaled")
	require.False(t, j.StateFollows[1].Undone)

	time.Sleep(5 * time.Millisecond)
	_, err = m.svc.UndoSiblingMoveForLoser(res.SiblingJournalID, m.l)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.l), "the state is back on the loser")
	require.Zero(t, sfProgress(t, m.f.S, m.user.ID, m.k), "and off the survivor")
	require.Equal(t, m.l, sfCurrentBook(t, m.f.S, m.l), "the redirect is cleared")
	j = journalByID(t, m.svc, res.SiblingJournalID)
	require.True(t, j.StateFollows[1].Undone)
	require.Equal(t, undoneAt, *j.UndoneAt, "a later per-loser undo keeps UndoneAt")
}

// Item 4: a follow is marked undone only once its refollow landed, so a
// whole undo that fails at the refollow is completed by a retry.
func TestUndoSiblingMove_RefollowFailureIsRetried(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)

	m.fs.failGetBookOnce = map[string]bool{m.l: true}
	_, err := m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.ErrorContains(t, err, "injected GetBookByID failure")
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.False(t, j.StateFollows[0].Undone, "not marked undone before the refollow landed")
	require.NotEmpty(t, j.LastError)

	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.k), "the retry put l's state on the survivor")
	require.Equal(t, m.k, sfCurrentBook(t, m.f.S, m.l))
	j = journalByID(t, m.svc, res.SiblingJournalID)
	require.True(t, j.StateFollows[0].Undone)
	require.Len(t, j.StateFollows, 2, "one refollow entry, not one per attempt")
}

// NIT 2: a survivor merged away since the merge. The refollow goes to the
// live book the survivor's chain leads to.
func TestUndoSiblingMove_SurvivorMergedAwayFollowsItsChain(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)
	n := m.f.Book(t, vptest.Spec{ID: "n", Group: "X", Primary: "true"})
	setAudio(t, m.f, n, "m4b", 256)
	_, err := m.svc.MergeBooks([]string{m.k, n}, n)
	require.NoError(t, err)

	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, n), "l's state reached the live end of k's chain")
	require.Equal(t, n, sfCurrentBook(t, m.f.S, m.l))
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.Contains(t, strings.Join(j.Warnings, "\n"), "merged away")
}

// NIT 2: a survivor deleted outright leads to no live book. The state stays
// on the loser, held by a pending repair the sweep completes later.
func TestUndoSiblingMove_SurvivorGoneLeavesPendingRepair(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)
	require.NoError(t, SoftDeleteBook(m.f.S, m.k))

	_, err := m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.l), "the state stays on the loser")
	require.Contains(t, pendingKeys(t, m.f.S), pendingRepairKey(m.l, m.k))
	has, err := BookHasCarryableUserState(m.f.S, m.l)
	require.NoError(t, err)
	require.True(t, has, "the purge keeps the loser while it holds the state")
}

// Item 6: the sync redirect leads to the flag holder; ResolveMergeSurvivor
// corrects it to the merge's survivor from the journal, and stops correcting
// once an undo reversed the follow.
func TestResolveMergeSurvivor_CorrectsFlagHolderRedirect(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)

	got, err := ResolveSurvivor(m.f.S, m.l)
	require.NoError(t, err)
	require.Equal(t, m.ls, got, "the state chain ends at the flag holder")
	follows, err := FlagHolderFollowsFrom(m.f.S)
	require.NoError(t, err)
	require.Equal(t, FlagFollow{SurvivorID: m.k, HolderID: m.ls, JournalID: res.SiblingJournalID}, follows[m.l])
	got, err = ResolveMergeSurvivor(m.f.S, m.l, follows)
	require.NoError(t, err)
	require.Equal(t, m.k, got, "the work went to the survivor")

	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	follows, err = FlagHolderFollowsFrom(m.f.S)
	require.NoError(t, err)
	require.NotContains(t, follows, m.l, "a reversed follow is not a correction")
	got, err = ResolveMergeSurvivor(m.f.S, m.l, follows)
	require.NoError(t, err)
	require.Equal(t, m.k, got)
}

// Item 5: HiddenFromABS says, with a reason, whenever the flag holder is a
// book Audiobookshelf does not list; and the three fields are always in the
// JSON.
func TestMergeBooks_HiddenFromABSReasons(t *testing.T) {
	t.Run("explicit primary not organized", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		res, err := NewService(f.S).MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
		require.Equal(t, HiddenFromABSExplicitPrimaryNotOrganized, res.HiddenFromABS)
	})
	t.Run("no organized candidate", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true", State: "imported"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true", State: "imported"})
		setAudio(t, f, k, "m4b", 128)
		setAudio(t, f, l, "mp3", 64)
		res, err := NewService(f.S).MergeBooks([]string{l, k}, "")
		require.NoError(t, err)
		require.Equal(t, k, res.PrimaryID)
		require.Empty(t, res.GroupPrimaryID)
		require.Equal(t, HiddenFromABSNoOrganizedCandidate, res.HiddenFromABS)
	})
	t.Run("organized flag holder is listed", func(t *testing.T) {
		m := newFlagHolderMerge(t)
		res := m.merge(t)
		require.Empty(t, res.HiddenFromABS)
	})
	t.Run("organized but quarantined", func(t *testing.T) {
		f := siblingFixture(t)
		k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
		_, err := f.S.ModifyBook(k, func(b *database.Book) error {
			now := time.Now()
			b.QuarantinedAt = &now
			return nil
		})
		require.NoError(t, err)
		res, err := NewService(f.S).MergeBooks([]string{l, k}, k)
		require.NoError(t, err)
		require.Equal(t, HiddenFromABSQuarantined, res.HiddenFromABS)
	})
	t.Run("always serialized", func(t *testing.T) {
		data, err := json.Marshal(Result{PrimaryID: "p"})
		require.NoError(t, err)
		for _, key := range []string{`"group_primary_id":""`, `"hidden_from_abs":""`, `"state_holder_id":""`} {
			require.Contains(t, string(data), key)
		}
	})
}
