// file: internal/trackseq/trackseq_test.go
// version: 1.0.0
// guid: b7f04c29-51ad-4e83-9a26-0c8d17b34f65
// last-edited: 2026-08-24

package trackseq

import (
	"fmt"
	"testing"
)

func TestExtractMatchesTheSharedCorpus(t *testing.T) {
	for _, c := range Corpus {
		t.Run(fmt.Sprintf("%q", c.Stem), func(t *testing.T) {
			num, total, ok := Extract(c.Stem)
			if ok != c.OK || num != c.Num || total != c.Total {
				t.Fatalf("Extract(%q) = (%d, %d, %v); want (%d, %d, %v)",
					c.Stem, num, total, ok, c.Num, c.Total, c.OK)
			}
		})
	}
}

// Ordering is the correctness property, so it gets its own assertion rather than
// riding on the corpus. If the trailing pattern is ever promoted above the
// keyword forms, this is what says so out loud.
func TestKeywordFormsOutrankTheTrailingNumber(t *testing.T) {
	num, total, ok := Extract("Part 1 of 8")
	if !ok || num != 1 || total != 8 {
		t.Fatalf("Extract(\"Part 1 of 8\") = (%d, %d, %v); the trailing-number pattern "+
			"outranked the keyword form and read the TOTAL as the sequence number", num, total, ok)
	}
}

// A zero ordinal carries no ordering information, and callers use "positive" as
// their in-a-sequence signal. Returning (0, true) would make a folder of "00"
// files look like a detected sequence whose numbers all collide.
func TestZeroIsNotASequenceNumber(t *testing.T) {
	for _, stem := range []string{"00", "Chapter 00", "Book 0"} {
		if _, _, ok := Extract(stem); ok {
			t.Errorf("Extract(%q) reported a usable sequence number; zero is not one", stem)
		}
	}
}
