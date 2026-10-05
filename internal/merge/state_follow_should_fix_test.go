// file: internal/merge/state_follow_should_fix_test.go
// version: 1.0.0
// guid: 0f3b7f8e-5a8c-4c71-9b3e-2d6a1c4e7b90
// last-edited: 2026-10-05

package merge

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// refollowsFor returns the refollow entries j holds for loser.
func refollowsFor(j *SiblingMoveJournal, loser string) []StateFollow {
	var out []StateFollow
	for i, f := range j.StateFollows {
		if f.LoserID == loser && isRefollow(j, i) {
			out = append(out, f)
		}
	}
	return out
}

// #3764 follow-up item 1: the survivor k is merged into the flag holder ls
// after the merge, so the whole undo's refollow target (k's chain) is ls --
// the holder of the very entry it is reversing. The refollow must be written
// as its own entry, not over that one, or the per-loser undo UnmergeAuto runs
// finds nothing to reverse and the loser comes back empty.
func TestUndoSiblingMove_RefollowOntoSameHolderIsItsOwnEntry(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)
	_, err := m.svc.MergeBooks([]string{m.k, m.ls}, m.ls)
	require.NoError(t, err)

	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.ls), "the refollow put l's state where k's chain ends")
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.True(t, j.StateFollows[0].Undone, "the merge's follow is reversed")
	refs := refollowsFor(j, m.l)
	require.Len(t, refs, 1, "the refollow is journaled as its own entry")
	require.Equal(t, m.ls, refs[0].HolderID)
	require.False(t, refs[0].Undone)

	_, err = m.svc.UndoSiblingMoveForLoser(res.SiblingJournalID, m.l)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.l), "the state is back on the loser")
	require.Zero(t, sfProgress(t, m.f.S, m.user.ID, m.ls), "and off the holder")
}

// A whole undo whose refollow landed but whose sibling restore failed is
// retried. The retry must not reverse the refollow (its holder ls is itself
// a sibling the undo moves back) and refollow it again: one refollow entry,
// and the per-loser undo still brings the state back to l.
func TestUndoSiblingMove_RetryDoesNotReverseRefollow(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)
	_, err := m.svc.MergeBooks([]string{m.k, m.ls}, m.ls)
	require.NoError(t, err)

	m.fs.mu.Lock()
	m.fs.failModifyBookOnce = map[string]bool{m.ls: true}
	m.fs.mu.Unlock()
	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.ErrorContains(t, err, "injected ModifyBook failure")
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.True(t, j.StateFollows[0].Undone, "the follow was reversed and refollowed before the restore failed")
	require.Len(t, refollowsFor(j, m.l), 1)

	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	j = journalByID(t, m.svc, res.SiblingJournalID)
	require.Len(t, j.StateFollows, 2, "the retry neither reversed nor rewrote the refollow")
	require.False(t, refollowsFor(j, m.l)[0].Undone)

	_, err = m.svc.UndoSiblingMoveForLoser(res.SiblingJournalID, m.l)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.l))
	require.Zero(t, sfProgress(t, m.f.S, m.user.ID, m.ls))
}

// #3764 follow-up item 2: the refollow fails AFTER its entry is appended
// (the move onto k fails and no repair record can hold it). The retry must
// reuse that entry, not append a second one, and the per-loser undo must
// still put the state back on l.
func TestUndoSiblingMove_RefollowFailureAfterAppendReusesEntry(t *testing.T) {
	m := newFlagHolderMerge(t)
	res := m.merge(t)

	m.fs.mu.Lock()
	m.fs.failPendingRepair, m.fs.failStateOn = true, m.k
	m.fs.mu.Unlock()
	_, err := m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.ErrorContains(t, err, "injected")
	j := journalByID(t, m.svc, res.SiblingJournalID)
	require.False(t, j.StateFollows[0].Undone, "not marked undone before the refollow landed")
	require.Len(t, refollowsFor(j, m.l), 1, "the failed attempt appended its refollow entry")

	m.fs.mu.Lock()
	m.fs.failPendingRepair, m.fs.failStateOn = false, ""
	m.fs.mu.Unlock()
	_, err = m.svc.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.k), "the retry put l's state on the survivor")
	j = journalByID(t, m.svc, res.SiblingJournalID)
	require.True(t, j.StateFollows[0].Undone)
	require.Len(t, j.StateFollows, 2, "the retry reused the refollow entry")
	require.Len(t, refollowsFor(j, m.l), 1)

	_, err = m.svc.UndoSiblingMoveForLoser(res.SiblingJournalID, m.l)
	require.NoError(t, err)
	require.Equal(t, 60, sfProgress(t, m.f.S, m.user.ID, m.l), "the state is back on the loser")
	require.Zero(t, sfProgress(t, m.f.S, m.user.ID, m.k))
}

// A journal written before StateFollow.Refollow existed: a later entry for
// the same loser is read as a refollow.
func TestIsRefollow_InfersLegacyEntries(t *testing.T) {
	j := &SiblingMoveJournal{StateFollows: []StateFollow{
		{LoserID: "a", HolderID: "h"}, {LoserID: "b", HolderID: "h"}, {LoserID: "a", HolderID: "s"},
	}}
	require.False(t, isRefollow(j, 0))
	require.False(t, isRefollow(j, 1))
	require.True(t, isRefollow(j, 2))
}

// #3764 follow-up item 3: l is restored from the trash and merged again into
// n2. Its old journal's flag follow no longer describes it; the work went to
// n2, which ResolveSurvivor already says.
func TestResolveMergeSurvivor_RestoredAndRemergedLoser(t *testing.T) {
	m := newFlagHolderMerge(t)
	m.merge(t)
	_, err := RestoreFromTrash(m.f.S, m.l, nil, nil)
	require.NoError(t, err)
	n2 := m.f.Book(t, vptest.Spec{ID: "n2", Group: "N", Primary: "true"})
	setAudio(t, m.f, n2, "m4b", 256)
	_, err = m.svc.MergeBooks([]string{m.l, n2}, n2)
	require.NoError(t, err)

	got, err := ResolveSurvivor(m.f.S, m.l)
	require.NoError(t, err)
	require.Equal(t, n2, got)
	follows, err := FlagHolderFollowsFrom(m.f.S)
	require.NoError(t, err)
	got, err = ResolveMergeSurvivor(m.f.S, m.l, follows)
	require.NoError(t, err)
	require.Equal(t, n2, got, "the re-merge, not the old flag follow, decides")
}

// Item 3, first half on its own: a newer journal naming the loser claims it
// even when its merge sent no state to a flag holder.
func TestFlagHolderFollows_NewerJournalClaimsLoser(t *testing.T) {
	f := siblingFixture(t)
	svc := NewService(f.S)
	older := &SiblingMoveJournal{ID: "01AAAAAAAAAAAAAAAAAAAAAAAA", Status: SiblingJournalApplied, SurvivorID: "k",
		IntoGroupID: "G1", Losers: []string{"l", "x"}, FlagHolderID: "h",
		StateFollows: []StateFollow{{LoserID: "l", HolderID: "h"}, {LoserID: "x", HolderID: "h"}}}
	newer := &SiblingMoveJournal{ID: "01BBBBBBBBBBBBBBBBBBBBBBBB", Status: SiblingJournalApplied, SurvivorID: "n2",
		IntoGroupID: "G2", Losers: []string{"l"}}
	aborted := &SiblingMoveJournal{ID: "01CCCCCCCCCCCCCCCCCCCCCCCC", Status: SiblingJournalAborted, SurvivorID: "z",
		IntoGroupID: "G3", Losers: []string{"x"}}
	for _, j := range []*SiblingMoveJournal{older, newer, aborted} {
		require.NoError(t, svc.putSiblingJournal(j))
	}
	follows, err := FlagHolderFollowsFrom(f.S)
	require.NoError(t, err)
	require.NotContains(t, follows, "l", "the newer journal claims l")
	require.Equal(t, FlagFollow{SurvivorID: "k", HolderID: "h", JournalID: older.ID, IntoGroupID: "G1"}, follows["x"],
		"an aborted journal claims nothing")
}

// Item 3, second half on its own: the override applies only while the loser
// still sits in the journal's group.
func TestResolveMergeSurvivor_OverrideOnlyInJournalGroup(t *testing.T) {
	m := newFlagHolderMerge(t)
	m.merge(t)
	follows, err := FlagHolderFollowsFrom(m.f.S)
	require.NoError(t, err)
	ff := follows[m.l]
	require.NotEmpty(t, ff.IntoGroupID)
	got, err := ResolveMergeSurvivor(m.f.S, m.l, follows)
	require.NoError(t, err)
	require.Equal(t, m.k, got)

	_, err = m.f.S.ModifyBook(m.l, func(b *database.Book) error {
		other := "elsewhere"
		b.VersionGroupID = &other
		return nil
	})
	require.NoError(t, err)
	got, err = ResolveMergeSurvivor(m.f.S, m.l, follows)
	require.NoError(t, err)
	require.Equal(t, m.ls, got, "out of the journal's group, the redirect is taken as is")
}

// #3764 follow-up item 4: a holder ABS does not list always has a reason.
func TestAbsHiddenReason_EveryUnlistedHolderHasAReason(t *testing.T) {
	f := siblingFixture(t)
	svc := NewService(f.S)
	listed := f.Book(t, vptest.Spec{ID: "listed", Group: "A", Primary: "true"})
	notPrimary := f.Book(t, vptest.Spec{ID: "np", Group: "A", Primary: "false"})
	trashed := f.Book(t, vptest.Spec{ID: "tr", Group: "B", Primary: "true"})
	require.NoError(t, SoftDeleteBook(f.S, trashed))
	quarantined := f.Book(t, vptest.Spec{ID: "q", Group: "C", Primary: "true"})
	_, err := f.S.ModifyBook(quarantined, func(b *database.Book) error {
		now := time.Now()
		b.QuarantinedAt = &now
		return nil
	})
	require.NoError(t, err)
	imported := f.Book(t, vptest.Spec{ID: "imp", Group: "D", Primary: "true", State: "imported"})

	for id, want := range map[string]string{
		listed:      "",
		notPrimary:  HiddenFromABSNotPrimary,
		trashed:     HiddenFromABSNotLive,
		quarantined: HiddenFromABSQuarantined,
		imported:    HiddenFromABSNoOrganizedCandidate,
	} {
		got, err := svc.absHiddenReason(id, HiddenFromABSNoOrganizedCandidate)
		require.NoError(t, err)
		require.Equal(t, want, got, id)
	}
	got, err := svc.absHiddenReason(imported, "")
	require.NoError(t, err)
	require.Equal(t, HiddenFromABSNotListed, got, "never \"\" for a row ABS does not list")
	data, _ := json.Marshal(got)
	require.NotEqual(t, `""`, string(data))
}
