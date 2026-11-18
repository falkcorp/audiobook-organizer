// file: internal/plugins/maintenance/merge_cleanup_signals_test.go
// version: 1.0.0
// guid: ec1bddfa-2984-4990-956a-2707416c1633
// last-edited: 2026-11-18

package maintenance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// Every title, name and number here is invented.

// mcSecs is a complete runtime of n seconds.
func mcSecs(n int) database.BookRuntime {
	return database.BookRuntime{Seconds: n, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}
}

// mcPartial is a runtime that is a lower bound only: one of two files unmeasured.
func mcPartial(n int) database.BookRuntime {
	return database.BookRuntime{Seconds: n, Source: database.RuntimeSourceFiles, FilesCounted: 2, FilesKnown: 1}
}

func mcTestSide(id, title string, authors []string, secs int) mcSide {
	return mcSide{ID: id, Title: title, Authors: authors, Runtime: mcSecs(secs), Files: 1}
}

var (
	mcBruce  = []string{"bruce wayne"}
	mcSelina = []string{"selina kyle"}
)

func TestMergeCleanupTitleSimilarityEdges(t *testing.T) {
	// One substitution in ten letters is exactly the 0.9 threshold.
	require.InDelta(t, 0.9, mcTitleSimilarity("abcdefghij", "abcdefghix"), 1e-9)
	require.InDelta(t, 0.8, mcTitleSimilarity("abcdefghij", "abcdefghxx"), 1e-9)
	require.Equal(t, 1.0, mcTitleSimilarity("The Long Halloween", "long halloween"), "folded the way dedup folds")
	require.Equal(t, 0.0, mcTitleSimilarity("", "hush"))
}

func TestMergeCleanupSignalTitleEdges(t *testing.T) {
	cases := []struct {
		name        string
		l, t        string
		match, diff bool
	}{
		{"0.9 is a match", "abcdefghij", "abcdefghix", true, false},
		{"0.8 is neither", "abcdefghij", "abcdefghxx", false, false},
		{"0.5 is not a diff (strict)", "abcdefghij", "abcdexxxxx", false, false},
		{"far apart, no shared word", "year one", "hush", false, true},
		// sim is low but half the words are shared: overlap keeps it from a diff.
		{"far apart but overlapping words", "x", "x zzzzzzzzzzzzzz", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := mcComputeSignals(mcTestSide("l", c.l, mcBruce, 7200), mcTestSide("t", c.t, mcBruce, 7200))
			require.Equal(t, c.match, s.TitleMatch, "TITLE_MATCH")
			require.Equal(t, c.diff, s.TitleDiff, "TITLE_DIFF")
		})
	}
}

func TestMergeCleanupSignalAuthorAndSeries(t *testing.T) {
	l := mcTestSide("l", "Hush", mcBruce, 7200)
	for _, c := range []struct {
		name string
		t    []string
		want bool
	}{
		{"disjoint credit lists", mcSelina, true},
		{"one shared name is not different", []string{"selina kyle", "bruce wayne"}, false},
		{"empty target credits is missing evidence", nil, false},
	} {
		t.Run("author "+c.name, func(t *testing.T) {
			tt := mcTestSide("t", "Hush", c.t, 7200)
			require.Equal(t, c.want, mcComputeSignals(l, tt).AuthDiff)
		})
	}
	// Different credits when the loser has none is missing evidence too.
	require.False(t, mcComputeSignals(mcSide{ID: "l", Title: "Hush", Runtime: mcSecs(10)}, l).AuthDiff)

	one, two := 1, 2
	sid, other := 7, 8
	pos := func(series, seq *int) mcSide {
		s := mcTestSide("x", "Hush", mcBruce, 7200)
		s.SeriesID, s.SeriesSeq = series, seq
		return s
	}
	for _, c := range []struct {
		name string
		l, t mcSide
		want bool
	}{
		{"same series, different position", pos(&sid, &one), pos(&sid, &two), true},
		{"same series, same position", pos(&sid, &one), pos(&sid, &one), false},
		{"one side has no position", pos(&sid, &one), pos(&sid, nil), false},
		{"different series says nothing about position", pos(&sid, &one), pos(&other, &two), false},
		{"neither set", pos(nil, nil), pos(nil, nil), false},
	} {
		t.Run("series "+c.name, func(t *testing.T) {
			require.Equal(t, c.want, mcComputeSignals(c.l, c.t).SeriesPosDiff)
		})
	}
}

func TestMergeCleanupSignalHash(t *testing.T) {
	mk := func(files int, hash string, share int) mcSide {
		return mcSide{ID: "x", Title: "Hush", Authors: mcBruce, Runtime: mcSecs(100), Files: files, HashFile: hash, HashShare: share}
	}
	require.True(t, mcComputeSignals(mk(1, "h1", 2), mk(1, "h1", 2)).HashEq)
	require.True(t, mcComputeSignals(mk(1, "h1", 4), mk(1, "h1", 4)).HashEq, "shared by four is still evidence")
	require.False(t, mcComputeSignals(mk(1, "h1", 5), mk(1, "h1", 5)).HashEq, "shared by five proves nothing")
	require.False(t, mcComputeSignals(mk(2, "h1", 2), mk(1, "h1", 2)).HashEq, "multi-file books carry no single-file hash")
	require.False(t, mcComputeSignals(mk(1, "", 0), mk(1, "", 0)).HashEq, "an empty hash is no hash")
	require.False(t, mcComputeSignals(mk(1, "h1", 2), mk(1, "h2", 2)).HashEq)
}

func TestMergeCleanupSignalRuntimeEdges(t *testing.T) {
	// The longer side is 1000 s * 10 so the permille edges are exact.
	long := 100000
	at := func(permille int) mcSignals {
		return mcComputeSignals(mcTestSide("l", "Hush", mcBruce, long), mcTestSide("t", "Hush", mcBruce, long/1000*permille))
	}
	require.True(t, at(950).DurClose, "0.95 is close")
	require.False(t, at(949).DurClose)
	require.True(t, at(949).DurNear)
	require.True(t, at(800).DurNear, "0.80 is near")
	require.False(t, at(799).DurNear)
	require.True(t, at(799).DurFar)
	require.True(t, at(490).Dur2x, "0.49 is the lower 2x edge")
	require.False(t, at(489).Dur2x)
	require.True(t, at(510).Dur2x, "0.51 is the upper 2x edge")
	require.False(t, at(511).Dur2x)
	require.True(t, at(1000).DurClose)

	// Symmetric: the ratio is shorter over longer whichever side is shorter.
	sw := mcComputeSignals(mcTestSide("l", "Hush", mcBruce, 799*100), mcTestSide("t", "Hush", mcBruce, 100000))
	require.True(t, sw.DurFar)

	// DUR_EXACT is within 2 s.
	ex := func(delta int) mcSignals {
		return mcComputeSignals(mcTestSide("l", "Hush", mcBruce, 5000), mcTestSide("t", "Hush", mcBruce, 5000+delta))
	}
	require.True(t, ex(2).DurExact)
	require.False(t, ex(3).DurExact)

	// SIZE_CLOSE is within 0.1 percent of the larger side.
	sz := func(a, b int64) mcSignals {
		l := mcTestSide("l", "Hush", mcBruce, 5000)
		l.TotalBytes, l.BytesComplete = a, true
		tt := mcTestSide("t", "Hush", mcBruce, 5000)
		tt.TotalBytes, tt.BytesComplete = b, true
		return mcComputeSignals(l, tt)
	}
	require.True(t, sz(1_000_000, 1_001_000).SizeClose)
	require.False(t, sz(1_000_000, 1_002_000).SizeClose)
	l := mcTestSide("l", "Hush", mcBruce, 5000)
	l.TotalBytes = 1_000_000 // BytesComplete false: a partial byte count is no size
	require.False(t, mcComputeSignals(l, l).SizeClose)

	// No ratio is computed from an incomplete runtime.
	inc := mcTestSide("t", "Hush", mcBruce, 0)
	inc.Runtime = mcPartial(500)
	s := mcComputeSignals(mcTestSide("l", "Hush", mcBruce, long), inc)
	require.False(t, s.RuntimeKnown)
	require.False(t, s.DurFar || s.DurNear || s.DurClose || s.Dur2x || s.DurExact)
}

func TestMergeCleanupSignalNamesAreStable(t *testing.T) {
	s := mcComputeSignals(mcTestSide("l", "Year One", mcBruce, 7200), mcTestSide("t", "Hush", mcSelina, 7200))
	require.Equal(t, []string{"TITLE_DIFF", "AUTH_DIFF", "DUR_CLOSE", "DUR_EXACT"}, s.Names())
}

func TestMergeCleanupDecide(t *testing.T) {
	one, two := 1, 2
	sid := 7
	hour := 3600
	mk := func(title string, authors []string, secs int) mcSide { return mcTestSide("x", title, authors, secs) }
	cases := []struct {
		name       string
		l, t       mcSide
		kind, skip string
	}{
		{"title and author both differ", mk("Year One", mcBruce, 7*hour), mk("Hush", mcSelina, 7*hour+500), mcKindUnmerge, ""},
		{"title and series position differ", func() mcSide { s := mk("Year One", mcBruce, 7*hour); s.SeriesID, s.SeriesSeq = &sid, &one; return s }(),
			func() mcSide { s := mk("Hush", mcBruce, 7*hour+500); s.SeriesID, s.SeriesSeq = &sid, &two; return s }(), mcKindUnmerge, ""},
		{"author and series position differ", func() mcSide { s := mk("Hush", mcBruce, 7*hour); s.SeriesID, s.SeriesSeq = &sid, &one; return s }(),
			func() mcSide { s := mk("Hush", mcSelina, 7*hour+500); s.SeriesID, s.SeriesSeq = &sid, &two; return s }(), "", mcSkipSuspicious},
		{"one wrong-work signal is only suspicious", mk("Year One", mcBruce, 7*hour), mk("Hush", mcBruce, 7*hour+500), "", mcSkipSuspicious},
		{"two diffs but the hash matches", func() mcSide { s := mk("Year One", mcBruce, 7*hour); s.HashFile, s.HashShare = "h", 2; return s }(),
			func() mcSide { s := mk("Hush", mcSelina, 7*hour); s.HashFile, s.HashShare = "h", 2; return s }(), "", mcSkipSuspicious},
		{"two diffs, exact duration but different size still unmerges", func() mcSide {
			s := mk("Year One", mcBruce, 7*hour)
			s.TotalBytes, s.BytesComplete = 5_000_000, true
			return s
		}(),
			func() mcSide {
				s := mk("Hush", mcSelina, 7*hour+1)
				s.TotalBytes, s.BytesComplete = 9_000_000, true
				return s
			}(), mcKindUnmerge, ""},
		{"two diffs, exact duration AND size is not wrong-work", func() mcSide {
			s := mk("Year One", mcBruce, 7*hour)
			s.TotalBytes, s.BytesComplete = 5_000_000, true
			return s
		}(),
			func() mcSide {
				s := mk("Hush", mcSelina, 7*hour+1)
				s.TotalBytes, s.BytesComplete = 5_000_100, true
				return s
			}(), "", mcSkipSuspicious},
		{"two diffs with the target runtime incomplete holds the unmerge", mk("Year One", mcBruce, 7*hour),
			func() mcSide { s := mk("Hush", mcSelina, 0); s.Runtime = mcPartial(900); return s }(), mcKindUnmerge, mcSkipRuntimeIncomplete},
		{"two diffs with the loser runtime incomplete holds the unmerge",
			func() mcSide { s := mk("Year One", mcBruce, 0); s.Runtime = mcPartial(900); return s }(), mk("Hush", mcSelina, 7*hour), mcKindUnmerge, mcSkipRuntimeIncomplete},
		{"same title, target is 60 percent: swap", mk("Hush", mcBruce, 10*hour), mk("Hush", mcBruce, 6*hour), mcKindSwap, ""},
		{"same title, target exactly 0.80 is not shorter enough", mk("Hush", mcBruce, 10*hour), mk("Hush", mcBruce, 8*hour), "", mcSkipMergeLooksCorrect},
		{"same title, target 0.799 swaps", mk("Hush", mcBruce, 100000), mk("Hush", mcBruce, 79900), mcKindSwap, ""},
		{"same title, target is exactly half: 2x holds", mk("Hush", mcBruce, 20000), mk("Hush", mcBruce, 10000), mcKindSwap, mcSkipRuntimeAmbiguous},
		{"same title, target under thirty minutes holds", mk("Hush", mcBruce, 2*hour), mk("Hush", mcBruce, 1799), mcKindSwap, mcSkipRuntimeAmbiguous},
		{"same title, target exactly thirty minutes swaps", mk("Hush", mcBruce, 2*hour), mk("Hush", mcBruce, 1800), mcKindSwap, ""},
		{"same title, loser is the shorter side: the merge direction is right", mk("Hush", mcBruce, 6*hour), mk("Hush", mcBruce, 10*hour), "", mcSkipMergeLooksCorrect},
		{"same title, runtimes equal: a true copy", mk("Hush", mcBruce, 6*hour), mk("Hush", mcBruce, 6*hour), "", mcSkipMergeLooksCorrect},
		{"same title, incomplete runtime holds the swap", mk("Hush", mcBruce, 10*hour),
			func() mcSide { s := mk("Hush", mcBruce, 0); s.Runtime = mcPartial(900); return s }(), mcKindSwap, mcSkipRuntimeIncomplete},
		{"same title but different author is contradictory", mk("Hush", mcBruce, 10*hour), mk("Hush", mcSelina, 6*hour), "", mcSkipSuspicious},
		{"same file hash outranks a title that differs", func() mcSide { s := mk("Year One", mcBruce, 6*hour); s.HashFile, s.HashShare = "h", 2; return s }(),
			func() mcSide { s := mk("Hush", mcBruce, 6*hour); s.HashFile, s.HashShare = "h", 2; return s }(), "", mcSkipMergeLooksCorrect},
		{"title differs and there are no credits to compare", mk("Year One", mcBruce, 10*hour), mk("Hush", nil, 6*hour), "", mcSkipSuspicious},
		{"no signal at all", mk("abcdefghij", nil, 10*hour), mk("abcdefghxx", nil, 6*hour), "", mcSkipCannotTell},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := mcDecide(c.l, c.t)
			require.Equal(t, c.kind, v.Kind, "kind (signals %v)", v.Signals.Names())
			require.Equal(t, c.skip, v.Skip, "skip (signals %v)", v.Signals.Names())
			if v.Skip != "" {
				require.NotEmpty(t, v.Why, "a skip says why")
			}
		})
	}
}

// A differing ASIN is a supporting signal only: alone it neither decides nor
// holds a row (docs/plans: asin_conflict treats different ASINs as possible
// editions of one work).
func TestMergeCleanupASINDiffNeverDecides(t *testing.T) {
	l := mcTestSide("l", "Hush", mcBruce, 36000)
	l.ASIN = "B000000001"
	tt := mcTestSide("t", "Hush", mcBruce, 21600)
	tt.ASIN = "B000000002"
	s := mcComputeSignals(l, tt)
	require.True(t, s.ASINDiff)
	v := mcDecide(l, tt)
	require.Equal(t, mcKindSwap, v.Kind)
	require.Empty(t, v.Skip)
	tt.ASIN = ""
	require.False(t, mcComputeSignals(l, tt).ASINDiff, "one side without an ASIN is no difference")
}

// --- chain walker -----------------------------------------------------------

func mcBook(id string, merged string, trashed bool) *database.Book {
	b := &database.Book{ID: id, Title: id}
	if merged != "" {
		b.MergedIntoBookID = &merged
	}
	if trashed {
		yes := true
		b.MarkedForDeletion = &yes
	}
	return b
}

func mcReader(books ...*database.Book) func(string) (*database.Book, error) {
	m := map[string]*database.Book{}
	for _, b := range books {
		m[b.ID] = b
	}
	return func(id string) (*database.Book, error) { return m[id], nil }
}

func TestMergeCleanupWalkChain(t *testing.T) {
	t.Run("live end", func(t *testing.T) {
		l := mcBook("L", "T", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("T", "", false)))
		require.NoError(t, err)
		require.Equal(t, mcEndLive, c.End)
		require.Equal(t, "T", c.EndID)
		require.Equal(t, []string{"T"}, c.HopIDs())
	})
	t.Run("a merged-not-trashed hop is walked through, not returned as the survivor", func(t *testing.T) {
		l := mcBook("L", "B", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("B", "C", false), mcBook("C", "", false)))
		require.NoError(t, err)
		require.Equal(t, mcEndLive, c.End)
		require.Equal(t, "C", c.EndID, "merge.ResolveMergeSurvivor would stop at B")
		require.Equal(t, []string{"B", "C"}, c.HopIDs())
		require.Equal(t, "merged", c.Hops[0].State)
		require.Equal(t, "live", c.Hops[1].State)
	})
	t.Run("a trashed hop with a live book beyond it", func(t *testing.T) {
		l := mcBook("L", "X", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("X", "Y", true), mcBook("Y", "", false)))
		require.NoError(t, err)
		require.Equal(t, mcEndLive, c.End)
		require.Equal(t, "Y", c.EndID)
		require.True(t, c.ThroughTrashed)
		require.Equal(t, "trashed", c.Hops[0].State)
	})
	t.Run("dead end: the last book is trashed", func(t *testing.T) {
		l := mcBook("L", "X", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("X", "", true)))
		require.NoError(t, err)
		require.Equal(t, mcEndDead, c.End)
		require.Empty(t, c.EndID)
		require.Equal(t, "X", c.LastID())
	})
	t.Run("dead end: a hop that does not exist", func(t *testing.T) {
		l := mcBook("L", "gone", false)
		c, err := mcWalkChain(l, mcReader(l))
		require.NoError(t, err)
		require.Equal(t, mcEndDead, c.End)
		require.Equal(t, "missing", c.Hops[0].State)
	})
	t.Run("cycle through the loser", func(t *testing.T) {
		l := mcBook("L", "A", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("A", "L", false)))
		require.NoError(t, err)
		require.Equal(t, mcEndCycle, c.End)
		require.Equal(t, []string{"L", "A"}, c.CycleMembers)
	})
	t.Run("a tail leading into a cycle names only the cycle", func(t *testing.T) {
		l := mcBook("L", "A", false)
		c, err := mcWalkChain(l, mcReader(l, mcBook("A", "B", false), mcBook("B", "A", false)))
		require.NoError(t, err)
		require.Equal(t, mcEndCycle, c.End)
		require.Equal(t, []string{"A", "B"}, c.CycleMembers)
	})
	t.Run("ten targets is the cap and still resolves", func(t *testing.T) {
		books := []*database.Book{mcBook("L", "h1", false)}
		for i := 1; i <= 10; i++ {
			next := fmt.Sprintf("h%d", i+1)
			if i == 10 {
				next = ""
			}
			books = append(books, mcBook(fmt.Sprintf("h%d", i), next, false))
		}
		c, err := mcWalkChain(books[0], mcReader(books...))
		require.NoError(t, err)
		require.Equal(t, mcEndLive, c.End)
		require.Equal(t, "h10", c.EndID)
	})
	t.Run("eleven targets is over the cap", func(t *testing.T) {
		books := []*database.Book{mcBook("L", "h1", false)}
		for i := 1; i <= 11; i++ {
			next := fmt.Sprintf("h%d", i+1)
			if i == 11 {
				next = ""
			}
			books = append(books, mcBook(fmt.Sprintf("h%d", i), next, false))
		}
		c, err := mcWalkChain(books[0], mcReader(books...))
		require.NoError(t, err)
		require.Equal(t, mcEndOverCap, c.End)
		require.Empty(t, c.EndID)
	})
	t.Run("a read error is returned, never read as a dead end", func(t *testing.T) {
		l := mcBook("L", "A", false)
		_, err := mcWalkChain(l, func(string) (*database.Book, error) { return nil, fmt.Errorf("disk gone") })
		require.ErrorContains(t, err, "disk gone")
	})
}

// ---- edges the mutant pass found uncovered ----

// Each case is built so that the rule under test is the only thing that gives
// the expected answer: with it removed or loosened the verdict changes.
func TestMergeCleanupDecideEvidenceEdges(t *testing.T) {
	hour := 3600
	// Titles 0.8 alike: neither TITLE_MATCH nor TITLE_DIFF.
	mid1, mid2 := "abcdefghij", "abcdefghxx"
	mk := func(title string, authors []string, secs int) mcSide { return mcTestSide("x", title, authors, secs) }
	hashed := func(s mcSide) mcSide { s.HashFile, s.HashShare = "h", 2; return s }
	sized := func(s mcSide, n int64) mcSide { s.TotalBytes, s.BytesComplete = n, true; return s }
	cases := []struct {
		name       string
		l, t       mcSide
		kind, skip string
	}{
		{"two diffs, sizes match but the durations are far apart: still wrong work",
			sized(mk("Year One", mcBruce, 7*hour), 5_000_000), sized(mk("Hush", mcSelina, 7*hour+500), 5_000_100), mcKindUnmerge, ""},
		{"a close runtime and the same author is one work (a true copy)", mk(mid1, mcBruce, 10*hour), mk(mid2, mcBruce, 10*hour), "", mcSkipMergeLooksCorrect},
		{"a near runtime and the same author is one work", mk(mid1, mcBruce, 10*hour), mk(mid2, mcBruce, 8*hour+1800), "", mcSkipMergeLooksCorrect},
		{"a close runtime with no credits is no evidence of one work", mk(mid1, nil, 10*hour), mk(mid2, nil, 10*hour), "", mcSkipCannotTell},
		{"the same file hash alone makes one work", hashed(mk(mid1, nil, 10*hour)), hashed(mk(mid2, nil, 6*hour)), mcKindSwap, ""},
		{"the same file hash outranks a different title, even when the runtimes are far apart",
			hashed(mk("Year One", nil, 10*hour)), hashed(mk("Hush", nil, 6*hour)), mcKindSwap, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := mcDecide(c.l, c.t)
			require.Equal(t, c.kind, v.Kind, "kind (skip %q: %s)", v.Skip, v.Why)
			require.Equal(t, c.skip, v.Skip, "skip (%s)", v.Why)
		})
	}

	// A different author is a wrong-work signal, so a close runtime does not
	// make these one work; the reason must not claim "the same author".
	v := mcDecide(mk(mid1, mcBruce, 10*hour), mk(mid2, mcSelina, 10*hour))
	require.Equal(t, mcSkipSuspicious, v.Skip)
	require.NotContains(t, v.Why, "same author", "the credits differ")
}

func TestMergeCleanupSignalThresholdEdges(t *testing.T) {
	sigs := func(a, b string) mcSignals {
		return mcComputeSignals(mcTestSide("l", a, mcBruce, 7200), mcTestSide("t", b, mcBruce, 7200))
	}
	// Strictly under 0.5 alike and sharing no word: different, even when only
	// just under the line.
	s := sigs("abcdefghijk", "abcdexxxxxx")
	require.Greater(t, s.TitleSim, 0.4)
	require.Less(t, s.TitleSim, 0.5)
	require.True(t, s.TitleDiff, "sim %.3f, overlap %.3f", s.TitleSim, s.TitleOverlap)

	// A third of the words shared (Jaccard 1/3, just under 0.34) is still
	// different; three eighths (0.375) is not.
	s = sigs("aa bbbbbbbbbbbb", "aa cccccccccccc")
	require.Less(t, s.TitleSim, 0.5)
	require.InDelta(t, 1.0/3, s.TitleOverlap, 1e-9)
	require.True(t, s.TitleDiff)
	s = sigs("aa bb cc xxxxxxxx yyyyyyyy", "aa bb cc zzzzzzzz wwwwwwww vvvvvvvv")
	require.Less(t, s.TitleSim, 0.5)
	require.InDelta(t, 3.0/8, s.TitleOverlap, 1e-9)
	require.False(t, s.TitleDiff, "overlap %.3f is over the line", s.TitleOverlap)

	// No title is no evidence of a different one.
	s = mcComputeSignals(mcTestSide("l", "", mcBruce, 7200), mcTestSide("t", "Hush", mcBruce, 7200))
	require.False(t, s.TitleDiff)
}

func TestMergeCleanupSignalSharedHashNeedsBothCountsLow(t *testing.T) {
	side := func(share int) mcSide {
		s := mcTestSide("x", "Hush", mcBruce, 7200)
		s.HashFile, s.HashShare = "h", share
		return s
	}
	require.True(t, mcComputeSignals(side(mcHashShareMax), side(mcHashShareMax)).HashEq, "premise: at the limit still counts")
	require.False(t, mcComputeSignals(side(1), side(mcHashShareMax+1)).HashEq, "a hash the target's file shares with many books says nothing")
	require.False(t, mcComputeSignals(side(mcHashShareMax+1), side(1)).HashEq, "nor the loser's")
}

func TestMergeCleanupSignalRuntimeDirection(t *testing.T) {
	same := mcComputeSignals(mcTestSide("l", "Hush", mcBruce, 3600), mcTestSide("t", "Hush", mcBruce, 3600))
	require.False(t, same.TargetShorter, "equal runtimes: neither is shorter")
	require.Equal(t, 1000, same.RatioPermille)
	shorter := mcComputeSignals(mcTestSide("l", "Hush", mcBruce, 3600), mcTestSide("t", "Hush", mcBruce, 3599))
	require.True(t, shorter.TargetShorter)
}

func TestMergeCleanupSignalSizeNeedsBothTotals(t *testing.T) {
	side := func(complete bool) mcSide {
		s := mcTestSide("x", "Hush", mcBruce, 7200)
		s.TotalBytes, s.BytesComplete = 1_000_000, complete
		return s
	}
	require.True(t, mcComputeSignals(side(true), side(true)).SizeClose, "premise")
	require.False(t, mcComputeSignals(side(true), side(false)).SizeClose, "a total that is only a lower bound is not a size")
	require.False(t, mcComputeSignals(side(false), side(true)).SizeClose)
}

// A cycle that is only seen after the tenth target has been read is a cycle,
// not an over-long chain: the check after the loop catches it.
func TestMergeCleanupWalkChainCycleSeenAtTheCap(t *testing.T) {
	l := mcBook("L", "n1", false)
	books := []*database.Book{l}
	for i := 1; i <= mcMaxHops; i++ {
		next := fmt.Sprintf("n%d", i+1)
		if i == mcMaxHops {
			next = "n5"
		}
		books = append(books, mcBook(fmt.Sprintf("n%d", i), next, false))
	}
	c, err := mcWalkChain(l, mcReader(books...))
	require.NoError(t, err)
	require.Equal(t, mcEndCycle, c.End, "the tenth target points back at the fifth")
	require.Equal(t, []string{"n5", "n6", "n7", "n8", "n9", "n10"}, c.CycleMembers)
}

// "Through the trash" means a trashed hop sits on the path; a hop that is
// merged away but not trashed does not make the chain pass through it.
func TestMergeCleanupWalkChainThroughTrashedOnlyForTrashedHops(t *testing.T) {
	l := mcBook("L", "B", false)
	c, err := mcWalkChain(l, mcReader(l, mcBook("B", "C", false), mcBook("C", "", false)))
	require.NoError(t, err)
	require.Equal(t, mcEndLive, c.End)
	require.False(t, c.ThroughTrashed, "B is merged away but live: not the trash")

	l = mcBook("L", "B", false)
	c, err = mcWalkChain(l, mcReader(l, mcBook("B", "C", true), mcBook("C", "", false)))
	require.NoError(t, err)
	require.True(t, c.ThroughTrashed, "B is trashed and still points on")
}
