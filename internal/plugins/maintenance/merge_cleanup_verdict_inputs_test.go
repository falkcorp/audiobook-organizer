// file: internal/plugins/maintenance/merge_cleanup_verdict_inputs_test.go
// version: 1.2.0
// guid: 3b0c6a2e-5d71-4e0f-9a64-c1f2d8e7b905
// last-edited: 2026-11-18

package maintenance

import (
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// The inputs round 2 of the review found the verdict reading wrongly: credits,
// titles, sizes, hashes and the chapters that are off disk. Every case here
// pins the same rule: when a fact is missing or ambiguous the row is held, and
// is never classed unmerge or swap. Every name and title is invented.

// The parts are comparison keys (mcFoldName: lower-case, words sorted), not
// names to show.
func TestMergeCleanupCreditParts(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
		want []string
	}{
		{"one name", "Bruce Wayne", []string{"bruce wayne"}},
		{"case and punctuation fold", "BRUCE  WAYNE.", []string{"bruce wayne"}},
		{"a composite credit splits", "Bruce Wayne, Selina Kyle", []string{"bruce wayne", "kyle selina"}},
		{"and splits", "Bruce Wayne and Selina Kyle", []string{"bruce wayne", "kyle selina"}},
		{"an ampersand splits", "Bruce Wayne & Selina Kyle", []string{"bruce wayne", "kyle selina"}},
		{"surname first folds to given first", "Wayne, Bruce", []string{"bruce wayne"}},
		{"the same name twice is one name", "Bruce Wayne & BRUCE WAYNE", []string{"bruce wayne"}},
		{"nothing", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, mcCreditParts(c.in))
		})
	}
}

func TestMergeCleanupCompositeCreditAgainstSplitCreditIsNotAuthDiff(t *testing.T) {
	composite := mcSide{Authors: mcCreditParts("Bruce Wayne, Selina Kyle")}
	split := mcSide{Authors: mcCreditParts("Bruce Wayne")}
	require.False(t, mcComputeSignals(composite, split).AuthDiff, "the two lists share Bruce Wayne")
	other := mcSide{Authors: mcCreditParts("Alfred Pennyworth")}
	require.True(t, mcComputeSignals(composite, other).AuthDiff, "premise: disjoint lists still differ")
}

func TestMergeCleanupUnreliableMetadataHoldsInsteadOfDeciding(t *testing.T) {
	const hour = 3600
	// Two wrong-work signals and a runtime gap: an unmerge on clean data.
	clean := func() (mcSide, mcSide) {
		l := mcTestSide("L", "Year One", mcBruce, 7*hour)
		t := mcTestSide("T", "Hush", mcSelina, 9*hour)
		return l, t
	}
	l, tt := clean()
	require.Equal(t, mcKindUnmerge, mcDecide(l, tt).Kind, "premise: clean data is an unmerge")

	for _, c := range []struct {
		name string
		edit func(l, t *mcSide)
		skip string
	}{
		{"a placeholder title on the loser", func(l, _ *mcSide) { l.Title = "Unknown Title" }, mcSkipPlaceholder},
		{"a placeholder title on the target", func(_, t *mcSide) { t.Title = "Unknown Title" }, mcSkipPlaceholder},
		{"a placeholder credit", func(l, _ *mcSide) { l.CreditsPlaceholder = true }, mcSkipPlaceholder},
		{"a dangling credit", func(_, t *mcSide) { t.CreditsDangling = 1 }, mcSkipDanglingAuthor},
	} {
		t.Run(c.name, func(t *testing.T) {
			l, tg := clean()
			c.edit(&l, &tg)
			v := mcDecide(l, tg)
			require.Equal(t, c.skip, v.Skip)
			require.Empty(t, v.Kind, "a held row proposes nothing")
		})
	}

	t.Run("two placeholder titles are not a title match", func(t *testing.T) {
		l := mcTestSide("L", "Unknown Title", mcBruce, 6*hour)
		tg := mcTestSide("T", "Unknown Title", mcBruce, 2*hour)
		v := mcDecide(l, tg)
		require.False(t, v.Signals.TitleMatch)
		require.NotEqual(t, mcKindSwap, v.Kind, "two books named the placeholder are not one work")
		require.Equal(t, mcSkipPlaceholder, v.Skip)
	})

	t.Run("a dangling credit outranks a placeholder in the skip kind and both are listed", func(t *testing.T) {
		l, tg := clean()
		l.CreditsPlaceholder, tg.CreditsDangling = true, 1
		v := mcDecide(l, tg)
		require.Equal(t, mcSkipDanglingAuthor, v.Skip)
		require.Len(t, v.Extra, 1)
		require.Equal(t, mcSkipPlaceholder, v.Extra[0].Skip)
	})
}

func TestMergeCleanupTitleAuthorSwappedPairIsHeldNotUnmerged(t *testing.T) {
	const hour = 3600
	correct := mcTestSide("T", "Year One", mcBruce, 7*hour)
	correct.Credits = []string{"Bruce Wayne"}
	swapped := mcTestSide("L", "Bruce Wayne", []string{"year one"}, 7*hour+600)
	swapped.Credits = []string{"Year One"}

	for _, c := range []struct {
		name string
		l, t mcSide
	}{
		{"the loser is the swapped one", swapped, correct},
		{"the target is the swapped one", correct, swapped},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := mcDecide(c.l, c.t)
			require.True(t, v.Signals.Transposed)
			require.Equal(t, mcSkipTitleAuthorSwapped, v.Skip)
			require.Empty(t, v.Kind)
			require.Contains(t, v.Why, "Fix swapped title and author")
		})
	}

	t.Run("a book named after its own author is not transposed against itself", func(t *testing.T) {
		a := mcTestSide("A", "Bruce Wayne", mcBruce, 7*hour)
		a.Credits = []string{"Bruce Wayne"}
		b := a
		b.ID = "B"
		require.False(t, mcComputeSignals(a, b).Transposed, "the titles match: nothing is swapped")
	})
	// The production shape is one-directional: the junk "read by ..." title
	// matches nothing of the twin's, and the real title sits in the author field.
	// Asking for the mirror as well let exactly that pair through as a wrong-work
	// merge (TITLE_DIFF and AUTH_DIFF both fire on it).
	t.Run("a credit that is the other book's title is enough, whichever book is swapped", func(t *testing.T) {
		junk := mcTestSide("L", "read by Alfred Pennyworth", []string{"hush"}, 7*hour)
		junk.Credits = []string{"Hush"}
		twin := mcTestSide("T", "Hush", mcSelina, 9*hour)
		twin.Credits = []string{"Selina Kyle"}
		for _, c := range []struct {
			name string
			l, t mcSide
		}{
			{"the loser is the swapped one", junk, twin},
			{"the target is the swapped one", twin, junk},
		} {
			t.Run(c.name, func(t *testing.T) {
				require.True(t, mcComputeSignals(c.l, c.t).Transposed)
				v := mcDecide(c.l, c.t)
				require.Equal(t, mcSkipTitleAuthorSwapped, v.Skip)
				require.Empty(t, v.Kind, "never an unmerge: the two are one work with a damaged record")
			})
		}
	})
	t.Run("the other direction, as an earlier version pinned it, is held too", func(t *testing.T) {
		l := mcTestSide("L", "Bruce Wayne", []string{"selina kyle"}, 7*hour)
		l.Credits = []string{"Selina Kyle"}
		tg := mcTestSide("T", "Year One", mcBruce, 7*hour)
		tg.Credits = []string{"Bruce Wayne"}
		require.True(t, mcComputeSignals(l, tg).Transposed, "the target's credit is the loser's title")
		require.Equal(t, mcSkipTitleAuthorSwapped, mcDecide(l, tg).Skip)
	})
	t.Run("no credit of either book is the other's title", func(t *testing.T) {
		l := mcTestSide("L", "The Long Halloween", mcBruce, 7*hour)
		l.Credits = []string{"Bruce Wayne"}
		tg := mcTestSide("T", "Year One", mcSelina, 9*hour)
		tg.Credits = []string{"Selina Kyle"}
		require.False(t, mcComputeSignals(l, tg).Transposed)
	})
}

func TestMergeCleanupTransposedIgnoresAPlaceholderCredit(t *testing.T) {
	// "Unknown" is a placeholder author but a real-looking title: a book titled
	// Unknown beside a book credited to Unknown is not a title/author swap.
	a := mcTestSide("A", "Unknown", []string{"selina kyle"}, 7*3600)
	a.Credits = []string{"Selina Kyle"}
	b := mcTestSide("B", "Year One", []string{"unknown"}, 7*3600)
	b.Credits = []string{"Unknown"}
	require.False(t, mcTransposed(a, b))
	require.False(t, mcTransposed(b, a))
	require.False(t, mcComputeSignals(a, b).Transposed)
	// Premise: with a real name in its place the same shape is a swap.
	b.Credits = []string{"Bruce Wayne"}
	a.Title = "Bruce Wayne"
	require.True(t, mcTransposed(a, b))
	require.True(t, mcTransposed(b, a), "the question is symmetric")
}

func TestMergeCleanupAlternativeTitleStopsTitleDiffAndVetoesAnUnmerge(t *testing.T) {
	const hour = 3600
	l := mcTestSide("L", "Year One", mcBruce, 7*hour)
	l.AltTitles = []string{"Hush"}
	tg := mcTestSide("T", "Hush", mcSelina, 9*hour)
	s := mcComputeSignals(l, tg)
	require.False(t, s.TitleDiff, "the loser is also called Hush")
	require.True(t, s.TitleAltMatch)
	require.False(t, s.TitleMatch, "an alternative title is not the title")

	// AUTH_DIFF and SERIES_POS_DIFF are still two wrong-work signals, but an
	// alternative-title match vetoes the unmerge.
	id := 1
	one, two := 1, 2
	l.SeriesID, l.SeriesSeq, tg.SeriesID, tg.SeriesSeq = &id, &one, &id, &two
	v := mcDecide(l, tg)
	require.Empty(t, v.Kind)
	require.Equal(t, mcSkipSuspicious, v.Skip)

	t.Run("it never makes two books one work by itself", func(t *testing.T) {
		l := mcTestSide("L", "Year One", mcBruce, 10*hour)
		l.AltTitles = []string{"Hush"}
		tg := mcTestSide("T", "Hush", mcBruce, 4*hour)
		v := mcDecide(l, tg)
		require.NotEqual(t, mcKindSwap, v.Kind)
		require.Equal(t, mcSkipCannotTell, v.Skip)
	})
	t.Run("a placeholder alternative title is no evidence", func(t *testing.T) {
		l := mcTestSide("L", "Year One", mcBruce, 7*hour)
		l.AltTitles = []string{"Unknown Title"}
		tg := mcTestSide("T", "Unknown Title", mcSelina, 9*hour)
		require.False(t, mcComputeSignals(l, tg).TitleAltMatch)
		// A placeholder matches every other placeholder, so one on each side
		// would be a match on nothing.
		tg = mcTestSide("T", "Hush", mcSelina, 9*hour)
		tg.AltTitles = []string{"Unknown Title"}
		s := mcComputeSignals(l, tg)
		require.False(t, s.TitleAltMatch, "two placeholder alternative titles are not a match")
		require.True(t, s.TitleDiff, "and they do not stop TITLE_DIFF")
	})
	t.Run("a near-match through an alternative title is not TITLE_DIFF", func(t *testing.T) {
		// One letter apart: similar enough that the titles do not differ,
		// though they share no word.
		l := mcTestSide("L", "Year One", mcBruce, 7*hour)
		l.AltTitles = []string{"abcdefghij"}
		tg := mcTestSide("T", "abcdefghix", mcSelina, 9*hour)
		require.False(t, mcComputeSignals(l, tg).TitleDiff)
		l.AltTitles = nil
		require.True(t, mcComputeSignals(l, tg).TitleDiff, "premise: without the alternative title they differ")
	})
}

func TestMergeCleanupExactRuntimeWithIncompleteSizesIsHeld(t *testing.T) {
	const hour = 3600
	l := mcTestSide("L", "Year One", mcBruce, 7*hour)
	tg := mcTestSide("T", "Hush", mcSelina, 7*hour+1)
	// Both wrong-work signals fire and the runtimes are exact to the second:
	// the exclusion needs the byte totals to say the audio is not the same.
	l.TotalBytes, l.BytesComplete = 5_000_000, true
	tg.TotalBytes, tg.BytesComplete = 5_000_000, false
	v := mcDecide(l, tg)
	require.Equal(t, mcKindUnmerge, v.Kind, "the kind it would be")
	require.Equal(t, mcSkipSizeIncomplete, v.Skip)

	tg.BytesComplete = true
	tg.TotalBytes = 9_000_000
	v = mcDecide(l, tg)
	require.Equal(t, mcKindUnmerge, v.Kind)
	require.Empty(t, v.Skip, "complete sizes that differ: an unheld unmerge")

	t.Run("sizes do not matter when the runtimes are not exact", func(t *testing.T) {
		l := mcTestSide("L", "Year One", mcBruce, 7*hour)
		tg := mcTestSide("T", "Hush", mcSelina, 9*hour)
		v := mcDecide(l, tg)
		require.Equal(t, mcKindUnmerge, v.Kind)
		require.Empty(t, v.Skip)
	})
}

func TestMergeCleanupHashEqComparesTheOriginalHashToo(t *testing.T) {
	side := func(file, orig string, fileShare, origShare int) mcSide {
		s := mcTestSide("x", "Hush", mcBruce, 3600)
		s.HashFile, s.HashShare, s.HashOrig, s.HashOrigShare = file, fileShare, orig, origShare
		return s
	}
	source := side("src", "", 2, 0)
	organized := side("tagged", "src", 1, 2) // a tag-write kept the source's hash as the original
	require.True(t, mcComputeSignals(source, organized).HashEq, "the copy's original hash is the source's hash")
	require.True(t, mcComputeSignals(organized, source).HashEq, "in either direction")
	require.False(t, mcComputeSignals(source, side("other", "elsewhere", 1, 1)).HashEq)
	require.False(t, mcComputeSignals(source, side("tagged", "src", 1, mcHashShareMax+1)).HashEq, "a hash many books share says nothing")
	multi := organized
	multi.Files = 2
	require.False(t, mcComputeSignals(source, multi).HashEq, "a multi-file book has no single file hash")

	// The veto: two wrong-work signals, but the organized copy is its source.
	l := mcTestSide("L", "Year One", mcBruce, 7*3600)
	tg := mcTestSide("T", "Hush", mcSelina, 9*3600)
	l.HashFile, l.HashShare = "src", 2
	tg.HashFile, tg.HashShare, tg.HashOrig, tg.HashOrigShare = "tagged", 1, "src", 2
	v := mcDecide(l, tg)
	require.Equal(t, mcSkipSuspicious, v.Skip, "the same audio vetoes the unmerge")
	require.Empty(t, v.Kind)
}

// runtimeAll is a side whose chapters off disk add up to the runtime given.
func mcWithMissing(s mcSide, allSecs, missing int) mcSide {
	s.RuntimeAll = database.BookRuntime{Seconds: allSecs, Source: database.RuntimeSourceFiles,
		FilesCounted: s.Runtime.FilesCounted + missing, FilesKnown: s.Runtime.FilesCounted + missing, FilesMissingUnmatched: missing}
	return s
}

func TestMergeCleanupSwapIsDecidedOnWhatIsOnDisk(t *testing.T) {
	const hour = 3600
	// Four chapters of an hour are on disk and six more are recorded but
	// missing: the loser's full runtime is ten hours, its on-disk runtime four.
	// The target is one seven-hour file.
	loser := mcTestSide("L", "Hush", mcBruce, 4*hour)
	loser = mcWithMissing(loser, 10*hour, 6)
	target := mcTestSide("T", "Hush", mcBruce, 7*hour)

	v := mcDecide(loser, target)
	require.Equal(t, mcKindSwap, v.Kind, "the full runtime says the target is a fragment")
	require.Equal(t, mcSkipRuntimeIncludesMissing, v.Skip, "the missing chapters are what makes it one")
	require.Contains(t, v.Why, "off disk")
	require.False(t, v.Signals.TargetShorter, "the signals shown are the on-disk ones")
	require.Len(t, v.Extra, 1, "the on-disk verdict is listed beside it")
	require.Equal(t, mcSkipMergeLooksCorrect, v.Extra[0].Skip)

	t.Run("a missing row that changes nothing holds nothing", func(t *testing.T) {
		loser := mcWithMissing(mcTestSide("L", "Hush", mcBruce, 8*hour), 8*hour+60, 1)
		v := mcDecide(loser, mcTestSide("T", "Hush", mcBruce, 7*hour))
		require.Equal(t, mcSkipMergeLooksCorrect, v.Skip)
		require.Empty(t, v.Kind)
	})
	t.Run("an on-disk fragment that is also a fragment in full is an ordinary swap", func(t *testing.T) {
		loser := mcWithMissing(mcTestSide("L", "Hush", mcBruce, 10*hour), 10*hour+60, 1)
		v := mcDecide(loser, mcTestSide("T", "Hush", mcBruce, 4*hour))
		require.Equal(t, mcKindSwap, v.Kind)
		require.Empty(t, v.Skip)
	})
	t.Run("the target's missing rows count the same way", func(t *testing.T) {
		loser := mcTestSide("L", "Hush", mcBruce, 10*hour)
		target := mcWithMissing(mcTestSide("T", "Hush", mcBruce, 4*hour), 9*hour, 5)
		v := mcDecide(loser, target)
		require.Equal(t, mcSkipRuntimeIncludesMissing, v.Skip, "a swap on a target that is only a fragment on disk")
		require.Equal(t, mcKindSwap, v.Kind)
	})
}

func TestMergeCleanupMissingChaptersThatOnlyChangeWhyASwapIsHeldStillHoldIt(t *testing.T) {
	const hour = 3600
	// On disk the target is twenty minutes: a probe, so the swap is held as
	// runtime_ambiguous. With its three missing hours counted it is a three-hour
	// target of a ten-hour loser, an unheld swap. Both are swaps; the answer
	// still depends on audio the book does not have, so that is what holds it.
	loser := mcTestSide("L", "Hush", mcBruce, 10*hour)
	target := mcWithMissing(mcTestSide("T", "Hush", mcBruce, 1200), 3*hour, 4)
	v := mcDecide(loser, target)
	require.Equal(t, mcKindSwap, v.Kind)
	require.Equal(t, mcSkipRuntimeIncludesMissing, v.Skip)
	require.Len(t, v.Extra, 1)
	require.Equal(t, mcSkipRuntimeAmbiguous, v.Extra[0].Skip, "the on-disk hold is kept beside it")
	require.Contains(t, v.Why, "an unheld swap")
}
