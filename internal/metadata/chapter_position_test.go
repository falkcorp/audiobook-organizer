// file: internal/metadata/chapter_position_test.go
// version: 1.1.0
// guid: 3f8b2d61-7c4e-4a19-9e05-b6d1c8a2f437
// last-edited: 2026-10-03

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestChapterPosition reads the position from the stripped pieces, one case
// per ChapterKeyKind plus the shapes the trailing-number rule got wrong.
func TestChapterPosition(t *testing.T) {
	cases := []struct {
		stem string
		kind ChapterKeyKind
		want ChapterPos
		ok   bool
	}{
		{"01 - My Book", ChapterKeyLeading, ChapterPos{Parts: []int{1}}, true},
		{"98", ChapterKeyLeading, ChapterPos{Parts: []int{98}}, true},
		{"My Book - Chapter 12", ChapterKeyMarker, ChapterPos{Parts: []int{12}}, true},
		{"Chapter 3 - 1984", ChapterKeyMarker, ChapterPos{Parts: []int{3}}, true},
		{"My Book Disc 2", ChapterKeyMarker, ChapterPos{Disc: 2}, true},
		{"Disc 2 - My Story 07", ChapterKeyMarker, ChapterPos{Disc: 2, Parts: []int{7}}, true},
		{"My Book Part 3 of 12", ChapterKeyMarker, ChapterPos{Parts: []int{3}}, true},
		{"Book 3 of 12", ChapterKeyOfTotal, ChapterPos{Parts: []int{3}}, true},
		{"My Book (03 of 12)", ChapterKeyOfTotal, ChapterPos{Parts: []int{3}}, true},
		{"My Story 01", ChapterKeyTrailingNumber, ChapterPos{Parts: []int{1}}, true},
		{"01 Genesis 001", ChapterKeyLeading, ChapterPos{Parts: []int{1, 1}}, true},
		// Leading disc-track / book-chapter pairs (2026-10-03).
		{"8-02 Rubicon", ChapterKeyLeading, ChapterPos{Parts: []int{8, 2}, LeadPair: true}, true},
		{"01_07-Star Wars Darth Plagueis", ChapterKeyLeading, ChapterPos{Parts: []int{1, 7}, LeadPair: true}, true},
		{"02_001", ChapterKeyLeading, ChapterPos{Parts: []int{2, 1}, LeadPair: true}, true},
		// "_" around "of" is the of-total shape too.
		{"02_Eldest_002_of_349", ChapterKeyLeading, ChapterPos{Parts: []int{2, 2}}, true},
		// A leading number plus a trailing "(n of m)" is two Parts but no pair.
		{"001 - Arrival (1 of 8)", ChapterKeyLeading, ChapterPos{Parts: []int{1, 1}}, true},
		// A date is no chapter numbering at all.
		{"01-05-1945", ChapterKeyNone, ChapterPos{}, false},
		{"12_25_2019 Speech", ChapterKeyNone, ChapterPos{}, false},
		{"Eldest_002_of_349", ChapterKeyOfTotal, ChapterPos{Parts: []int{2}}, true},
		// A year is no pair: the first number of a pair has 1-2 digits.
		{"2016 - Reality Alternatives", ChapterKeyLeading, ChapterPos{Parts: []int{2016}}, true},
		{"My Book", ChapterKeyNone, ChapterPos{}, false},
		{"Mistborn Book 2", ChapterKeyNone, ChapterPos{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.stem, func(t *testing.T) {
			_, kind := ChapterGroupKey(tc.stem)
			require.Equal(t, tc.kind, kind, "key kind")
			got, ok := ChapterPosition(tc.stem)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestChapterPos_DiscIsTheMajorKey: disc 1's last track sorts before disc
// 2's first, however the track numbers compare.
func TestChapterPos_DiscIsTheMajorKey(t *testing.T) {
	d1t9, _ := ChapterPosition("Disc 1 - Story 09")
	d2t1, _ := ChapterPosition("Disc 2 - Story 01")
	require.Equal(t, -1, d1t9.Compare(d2t1))
	require.Equal(t, 1, d2t1.Compare(d1t9))
	require.Equal(t, 0, d1t9.Compare(d1t9))
	short := ChapterPos{Parts: []int{1}}
	long := ChapterPos{Parts: []int{1, 2}}
	require.Equal(t, -1, short.Compare(long))
}

func TestDiscFolder(t *testing.T) {
	cases := []struct {
		name, rest string
		disc       int
		ok         bool
	}{
		{"CD1", "", 1, true},
		{"Disc 2", "", 2, true},
		{"Disk_03", "", 3, true},
		{"My Book - Disc 3", "My Book", 3, true},
		{"My Book (CD 2)", "My Book", 2, true},
		{"Discworld", "", 0, false},
		{"My Book", "", 0, false},
		{"CD0", "", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, disc, ok := DiscFolder(tc.name)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.disc, disc)
			require.Equal(t, tc.rest, rest)
		})
	}
}

// TestChapterGroupKey_PairsAndUnderscoreOf: every file of a set numbered with
// a leading pair or "_of_" shares one key. Before 2026-10-03 the second
// number and "_002_of" stayed in the key, so all 349 Eldest chapters had 349
// different keys and none of them grouped.
func TestChapterGroupKey_PairsAndUnderscoreOf(t *testing.T) {
	for _, set := range [][]string{
		{"02_Eldest_002_of_349", "02_Eldest_003_of_349", "02_Eldest_349_of_349"},
		{"8-02 Rubicon (Amaranthe 08) — 02", "8-03 Rubicon (Amaranthe 08) — 03", "8-62 Rubicon (Amaranthe 08) — 62"},
		{"01_07-Star Wars Darth Plagueis", "01_12-Star Wars Darth Plagueis", "13_13-Star Wars Darth Plagueis"},
	} {
		first, _ := ChapterGroupKey(set[0])
		require.NotEmpty(t, first, set[0])
		for _, stem := range set[1:] {
			k, _ := ChapterGroupKey(stem)
			require.Equal(t, first, k, stem)
		}
	}
	k, _ := ChapterGroupKey("02_Eldest_002_of_349")
	require.Equal(t, "eldest|of 349", k)
}
