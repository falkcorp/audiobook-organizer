// file: internal/itunes/service/fs_regroup_booklength_test.go
// version: 1.0.0
// guid: a2f464e2-f0d6-478e-a1f5-e3831ba95b30
// last-edited: 2026-08-05

package itunesservice

import (
	"fmt"
	"testing"
)

// mkSeries builds N single-file books in ONE flat folder, each `sec` long, named
// like numbered sequels — the exact shape found in production.
func mkSeries(folder, stem string, n, sec int) []ShatterBook {
	out := make([]ShatterBook, 0, n)
	for i := 1; i <= n; i++ {
		name := stem
		if i > 1 {
			name = fmt.Sprintf("%s %d", stem, i)
		}
		out = append(out, ShatterBook{
			BookID:      fmt.Sprintf("b%02d", i),
			FilePath:    fmt.Sprintf("%s/%s.m4b", folder, name),
			FileCount:   1,
			IsPrimary:   true,
			Title:       name,
			DurationSec: sec,
		})
	}
	return out
}

// 🔴 THE NEAR-MISS. A 2026-08-05 production dry-run found 41 of 43
// regroup.multidisc candidates were iTunes AUTHOR folders holding a whole
// catalogue — `iTunes Media/Audiobooks/<Author>/` — not chapter sets. The
// distinct-stem guard could not see it, because numbered sequels share one stem.
//
// Approving those would have merged entire series into single books, and the apply
// path hard-deletes absorbed rows. Each of these files is a full novel.
func TestClassify_DoesNotCollapseANumberedSeriesOfFullLengthBooks(t *testing.T) {
	folder := "/mnt/bigdata/books/itunes/iTunes Media/Audiobooks/William D. Arand (Super Sales on Super H"
	books := mkSeries(folder, "Super Sales on Super Heroes", 6, 9*3600) // 9h each

	groups, _ := ClassifyShatteredFolders(books)
	for _, g := range groups {
		if g.Kind == KindMultidisc && g.Confident {
			t.Fatalf("planned a CONFIDENT collapse of %d full-length books into one:\n  folder=%s\n  action=%s",
				len(g.Members), g.FolderRef, g.ProposedAction)
		}
	}
}

// The same shape at chapter length MUST still collapse — the guard has to
// discriminate, not simply disable the feature.
func TestClassify_StillCollapsesAChapterSet(t *testing.T) {
	folder := "/lib/Some Author/Some Novel"
	books := mkSeries(folder, "Some Novel", 8, 20*60) // 20 min each

	groups, _ := ClassifyShatteredFolders(books)
	found := false
	for _, g := range groups {
		if g.Kind == KindMultidisc && g.Confident {
			found = true
		}
	}
	if !found {
		t.Fatal("a genuine chapter set of 20-minute files was no longer collapsed — " +
			"the guard must discriminate by length, not disable collapsing")
	}
}

func TestMembersAreBookLength_RequiresAMajority(t *testing.T) {
	long := memberInfo{book: ShatterBook{DurationSec: 5 * 3600}}
	short := memberInfo{book: ShatterBook{DurationSec: 10 * 60}}

	// One long member among many short ones must NOT veto: an omnibus track or a
	// mis-tagged file cannot be allowed to block a real chapter set.
	if membersAreBookLength([]memberInfo{long, short, short, short}) {
		t.Error("a single long member vetoed a chapter set")
	}
	if !membersAreBookLength([]memberInfo{long, long, long, short}) {
		t.Error("a majority of book-length members failed to veto")
	}
	if membersAreBookLength(nil) {
		t.Error("empty input reported book-length")
	}
}

// 🔑 Unknown duration is not evidence. A library with missing durations must not
// have every collapse silently vetoed — the guard fires on positive evidence only.
func TestMembersAreBookLength_TreatsUnknownDurationAsNotBookLength(t *testing.T) {
	unknown := memberInfo{book: ShatterBook{DurationSec: 0}}
	if membersAreBookLength([]memberInfo{unknown, unknown, unknown}) {
		t.Fatal("members with unknown duration were treated as book-length")
	}
}

// The threshold must sit in the empty band between chapters and novels.
func TestBookLengthSec_SitsBetweenChaptersAndNovels(t *testing.T) {
	if bookLengthSec <= 45*60 {
		t.Fatalf("bookLengthSec = %ds — long chapters would be mistaken for books", bookLengthSec)
	}
	if bookLengthSec >= 4*3600 {
		t.Fatalf("bookLengthSec = %ds — short novels would be mistaken for chapters", bookLengthSec)
	}
}
