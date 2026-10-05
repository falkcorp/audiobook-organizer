// file: internal/merge/primary_handoff_test.go
// version: 1.2.0
// guid: 4c1e8b27-6a9f-4d53-b0e2-7f3a5d91c846
// last-edited: 2026-10-05

package merge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A loser that was its old group's primary and is pulled into the keep
// book's group hands its old group's flag on to the sibling it leaves behind.
// The loser's siblings do NOT follow it: a merge only moves the books it was
// given (see the leftGroups comment in MergeBooksWithOptions).
//
// Until 2026-10-05 this test merged a KEEP book that had a sibling into the
// loser's group and asserted that outcome -- which was the bug: the keep book
// was split from its own versions. See
// TestMergeBooks_KeepBookStaysInItsOwnGroup.
func TestMergeBooks_LeftGroupPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "b", Group: "g-b", Primary: "true"})
	bSib := f.Book(t, vptest.Spec{ID: "bsib", Group: "g-b", Primary: "false"})

	res, err := NewService(f.S).MergeBooks([]string{b, a}, a)
	require.NoError(t, err)
	require.Equal(t, "g-a", res.VersionGroupID)

	f.RequireSinglePrimary(t, "g-a", a)
	require.Equal(t, "g-a", f.GroupOf(t, b), "the loser joins the keep book's group")
	require.Equal(t, "g-b", f.GroupOf(t, bSib), "the loser's sibling stays where it was")
	f.RequireSinglePrimary(t, "g-b", bSib)
}

// Regression for the 2026-10-05 prod split (POST /audiobooks/link ->
// dedup.book-merge -> applyBookMergeReroute, which lists the losers BEFORE
// keep_id). The merge used to reuse the group of the first live grouped book
// in input order -- the loser's -- so the keep book left its own group (and
// its organized_source sibling) for the loser's single-member group, and the
// hand-off elected the sibling primary of the group the keep book abandoned.
// Each title ended up with two version groups.
func TestMergeBooks_KeepBookStaysInItsOwnGroup(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Group: "g-keep", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-keep", Primary: "false", State: "organized_source"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Group: "g-loser", Primary: "true"})

	// Loser first, keep last: the exact order applyBookMergeReroute passes.
	res, err := NewService(f.S).MergeBooks([]string{loser, keep}, keep)
	require.NoError(t, err)
	require.Equal(t, keep, res.PrimaryID)
	require.Equal(t, "g-keep", res.VersionGroupID, "the keep book's group must win")

	require.Equal(t, "g-keep", f.GroupOf(t, keep))
	require.Equal(t, "g-keep", f.GroupOf(t, sib), "the keep book's sibling must not be split off")
	require.Equal(t, "false", f.Flag(t, sib))
	f.RequireSinglePrimary(t, "g-keep", keep)

	require.Equal(t, "g-keep", f.GroupOf(t, loser), "the loser joins the keep book's group")
	lb, err := f.S.GetBookByID(loser)
	require.NoError(t, err)
	require.True(t, lb.IsSoftDeleted(), "the loser is soft-deleted")
	require.Equal(t, "false", f.Flag(t, loser))

	left, err := f.S.GetBooksByVersionGroup("g-loser")
	require.NoError(t, err)
	require.Empty(t, left, "the loser's old group has no live members left")
}

// Same split with no explicit primary: the ELECTED winner's group wins
// regardless of input order. The loser has no file row, so it cannot win the
// election (a book with an audio route beats one without).
func TestMergeBooks_ElectedWinnerKeepsItsOwnGroup(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep", Group: "g-keep", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-keep", Primary: "false", State: "organized_source"})
	loser := f.Book(t, vptest.Spec{ID: "loser", Group: "g-loser", Primary: "true", NoFile: true})

	res, err := NewService(f.S).MergeBooks([]string{loser, keep}, "")
	require.NoError(t, err)
	require.Equal(t, keep, res.PrimaryID)
	require.Equal(t, "g-keep", res.VersionGroupID)
	require.Equal(t, "g-keep", f.GroupOf(t, sib))
	require.Equal(t, "g-keep", f.GroupOf(t, loser))
	f.RequireSinglePrimary(t, "g-keep", keep)
}

// A combine that absorbs its version group's primary hands that group's flag
// on; undoing the combine restores the shell as a non-primary member.
func TestCombineBooks_AbsorbedPrimaryHandsOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	s := f.Book(t, vptest.Spec{ID: "s"})
	a := f.Book(t, vptest.Spec{ID: "a", Group: "g-a", Primary: "true"})
	sib := f.Book(t, vptest.Spec{ID: "sib", Group: "g-a", Primary: "false"})

	svc := NewService(f.S)
	res, err := svc.CombineBooks([]string{s, a}, s, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.BooksDeleted)
	f.RequireSinglePrimary(t, "g-a", sib)

	_, err = svc.UndoCombine(res.JournalID)
	require.NoError(t, err)
	f.RequireSinglePrimary(t, "g-a", sib)
	require.Equal(t, "false", f.Flag(t, a))
}

// An ungrouped survivor joins the live participants' group with the most live
// members, whatever the input order: the smaller group is listed first here,
// which the old first-in-input-order rule would have picked.
func TestMergeBooks_UngroupedSurvivorJoinsLargestGroup(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	keep := f.Book(t, vptest.Spec{ID: "keep"})
	small := f.Book(t, vptest.Spec{ID: "small", Group: "g-small", Primary: "true"})
	big := f.Book(t, vptest.Spec{ID: "big", Group: "g-big", Primary: "true"})
	bigSib := f.Book(t, vptest.Spec{ID: "bigsib", Group: "g-big", Primary: "false"})

	res, err := NewService(f.S).MergeBooks([]string{small, big, keep}, keep)
	require.NoError(t, err)
	require.Equal(t, "g-big", res.VersionGroupID)
	require.Equal(t, "g-big", f.GroupOf(t, keep))
	require.Equal(t, "g-big", f.GroupOf(t, bigSib))
	f.RequireSinglePrimary(t, "g-big", keep)
}
