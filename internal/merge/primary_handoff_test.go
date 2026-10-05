// file: internal/merge/primary_handoff_test.go
// version: 1.3.0
// guid: 4c1e8b27-6a9f-4d53-b0e2-7f3a5d91c846
// last-edited: 2026-10-05

package merge

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A loser that was its old group's primary and is pulled into the keep
// book's group hands its old group's flag on to the sibling it leaves behind.
// The loser's siblings do NOT follow it: a merge only moves the books it was
// given (see the resolveVersionGroup doc comment).
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

// With an ungrouped survivor and two live groups of EQUAL live-member count,
// the smallest group ID wins in either input order (resolveVersionGroup's
// tie-break), so the outcome never depends on how the caller listed the IDs.
func TestMergeBooks_UngroupedSurvivorTieGoesToSmallestGroupID(t *testing.T) {
	for _, order := range [][]string{{"g2", "g1", "keep"}, {"g1", "g2", "keep"}, {"keep", "g2", "g1"}} {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			f := vptest.New(t)
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = f.Root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })

			keep := f.Book(t, vptest.Spec{ID: "keep"})
			f.Book(t, vptest.Spec{ID: "g2", Group: "grp-2", Primary: "true"})
			f.Book(t, vptest.Spec{ID: "g1", Group: "grp-1", Primary: "true"})

			res, err := NewService(f.S).MergeBooks(order, keep)
			require.NoError(t, err)
			require.Equal(t, "grp-1", res.VersionGroupID, "a tie goes to the smallest group ID")
			require.Equal(t, "grp-1", f.GroupOf(t, keep))
			f.RequireSinglePrimary(t, "grp-1", keep)
		})
	}
}

// A soft-deleted loser passes the first half of the soft-deleted guard when a
// LIVE participant shares its group, but the merge resolves to the survivor's
// group: here survivor S is in H while live loser L and soft-deleted D are in
// G, so D is not a replay into the resolved group and must be refused by
// requireReplayedLosersInGroup -- before anything is written. Without that
// check D (a book some earlier merge already retired) would be pulled into H.
// Every input order is tried against the same store, which must be unchanged
// after each refusal.
func TestMergeBooks_SoftDeletedLoserNotInSurvivorGroup_Refused(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })

	s := f.Book(t, vptest.Spec{ID: "s", Group: "H", Primary: "true"})
	sSib := f.Book(t, vptest.Spec{ID: "ssib", Group: "H", Primary: "false"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	d := f.Book(t, vptest.Spec{ID: "d", Group: "G", Primary: "false"})
	f.SoftDelete(t, d)

	type snap struct {
		Book  database.Book
		Files []database.BookFile
	}
	snapshot := func() map[string]snap {
		out := map[string]snap{}
		for _, id := range []string{s, sSib, l, d} {
			b, err := f.S.GetBookByID(id)
			require.NoError(t, err)
			require.NotNil(t, b, "book %s", id)
			files, err := f.S.GetBookFiles(id)
			require.NoError(t, err)
			out[id] = snap{Book: *b, Files: files}
		}
		return out
	}
	before := snapshot()

	svc := NewService(f.S)
	for _, order := range [][]string{
		{s, l, d}, {s, d, l}, {l, s, d}, {l, d, s}, {d, s, l}, {d, l, s},
	} {
		res, err := svc.MergeBooks(order, s)
		require.Nil(t, res, "order %v", order)
		var sd *SoftDeletedInputError
		require.True(t, errors.As(err, &sd), "order %v: want SoftDeletedInputError, got %v", order, err)
		require.Equal(t, d, sd.BookID, "order %v", order)
		require.False(t, sd.AsPrimary, "order %v", order)
		require.Equal(t, before, snapshot(), "order %v: a refused merge must write nothing", order)
	}
}
