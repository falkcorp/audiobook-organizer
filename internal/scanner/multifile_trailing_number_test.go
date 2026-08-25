// file: internal/scanner/multifile_trailing_number_test.go
// version: 1.1.0
// guid: 5e1c9a74-3b62-4d8f-9a10-2c7e4b6d8f03
// last-edited: 2026-08-24

package scanner

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/trackseq"
	"github.com/stretchr/testify/require"
)

// The production case, measured on 2026-08-24.
//
// /mnt/bigdata/books/newbooks/audiobooks/Terry Pratchett Carpe Jugulum/ holds 80
// files named "Pratchett 001.mp3".."Pratchett 080.mp3" with no album tags. Before
// the trailing-number pattern, extractSeqNumber returned 0 for EVERY one of them,
// so the pattern quorum at step 2 failed and the folder was not grouped. The scan
// then wrote 80 separate Book rows -- titled "Pratchett 001".."Pratchett 080",
// each with the folder name "Terry Pratchett Carpe Jugulum" as its author, each in
// its own version group with is_primary_version=false.
//
// This is the assertion that the folder collapses to ONE book.
func TestDetectMultiFileGroupHandlesATrailingNumber(t *testing.T) {
	infos := make([]MultiFileInfo, 80)
	for i := range infos {
		infos[i] = MultiFileInfo{Path: fmt.Sprintf("/lib/Terry Pratchett Carpe Jugulum/Pratchett %03d.mp3", i+1)}
	}

	ok, sorted := DetectMultiFileGroup(infos, DefaultMultiFileConfig())
	require.True(t, ok,
		"80 sequentially-named files in one folder did not group; the scan writes one Book per track")
	require.Len(t, sorted, 80)
	require.Contains(t, sorted[0].Path, "Pratchett 001",
		"the group must be ordered by the detected sequence number")
	require.Contains(t, sorted[79].Path, "Pratchett 080")
}

func TestExtractSeqNumberReadsATrailingNumber(t *testing.T) {
	for _, tc := range []struct {
		stem string
		want int
	}{
		{"Pratchett 001", 1},
		{"Pratchett 080", 80},
		{"Carpe Jugulum 03", 3},
		{"Foo_12", 12},
		{"The Hierarchies-7", 7},
	} {
		got, _ := extractSeqNumber(tc.stem)
		require.Equalf(t, tc.want, got, "extractSeqNumber(%q)", tc.stem)
	}
}

// The trailing pattern is LAST in priority order, so a keyword-anchored form must
// still win. Without the ordering, "Part 1 of 8" would extract 8 -- the total --
// as its sequence number, and every file in a folder would sort identically.
func TestTrailingNumberDoesNotOutrankTheKeywordPatterns(t *testing.T) {
	for _, tc := range []struct {
		stem      string
		wantNum   int
		wantTotal int
	}{
		{"Part 1 of 8", 1, 8},
		{"Chapter 04", 4, 0},
		{"Track 11", 11, 0},
		{"Book Title (3 of 9)", 3, 9},
	} {
		num, tot := extractSeqNumber(tc.stem)
		require.Equalf(t, tc.wantNum, num, "number for %q", tc.stem)
		require.Equalf(t, tc.wantTotal, tot, "total for %q", tc.stem)
	}
}

// The negative that keeps the loosened pattern honest: three DIFFERENT books whose
// titles happen to end in a year must not be welded into one audiobook. The
// density check is what rejects them -- three numbers spanning 1999..2011 is not a
// sequence -- and this test fails if that guard is ever loosened alongside the
// pattern.
func TestTrailingNumberDoesNotGroupUnrelatedTitlesEndingInAYear(t *testing.T) {
	infos := []MultiFileInfo{
		{Path: "/lib/mixed/Some Book 1999.mp3"},
		{Path: "/lib/mixed/Another Book 2004.mp3"},
		{Path: "/lib/mixed/Third Book 2011.mp3"},
	}
	ok, _ := DetectMultiFileGroup(infos, DefaultMultiFileConfig())
	require.False(t, ok,
		"three unrelated books whose titles end in a year were grouped into one audiobook")
}

// TestExtractSeqNumberMatchesTheSharedCorpus is half of the control that was
// missing when this package and the repair-side classifier
// (itunesservice.trackNum) each kept their own private copy of the same
// judgement and silently drifted apart.
//
// The corpus lives in internal/trackseq. Asserting THIS package's entry point
// against it means a future change to the shared vocabulary that breaks the
// importer fails here, and a future private patch applied only here fails too.
// The other half is the same corpus asserted through trackNum.
func TestExtractSeqNumberMatchesTheSharedCorpus(t *testing.T) {
	for _, c := range trackseq.Corpus {
		num, total := extractSeqNumber(c.Stem)
		if !c.OK {
			require.Zerof(t, num, "extractSeqNumber(%q) invented a sequence number", c.Stem)
			continue
		}
		require.Equalf(t, c.Num, num, "extractSeqNumber(%q) number", c.Stem)
		require.Equalf(t, c.Total, total, "extractSeqNumber(%q) total", c.Stem)
	}
}
