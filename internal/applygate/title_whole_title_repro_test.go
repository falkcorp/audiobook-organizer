// file: internal/applygate/title_whole_title_repro_test.go
// version: 1.0.0
// guid: 5c2d8e47-1a9b-4f36-b0d5-7e3a9c1f4b62
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// ctGate runs the whole gate (Evaluate) for a book with ten hours of files, a
// live author credit and a stored series position against a candidate with
// the same runtime and author and a passing score, so that every leg except
// the one under test agrees. Two agreements (runtime, author) are therefore
// always on the table: a title check that is merely neutral still lets the
// pair through, and only an agree-or-block title check decides the outcome.
func ctGate(book database.Book, pos string, cand metafetch.MetadataCandidate) Verdict {
	book.Duration = new(36000)
	book.Author = &database.Author{Name: "Bruce Wayne"}
	if pos != "" {
		book.SeriesPositionRaw = new(pos)
	}
	cand.Author = "Bruce Wayne"
	cand.DurationSec = 36000 + 600 // 1.7% off: inside the 5% agree band
	cand.Score = 0.95
	return Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
}

// TestEvaluate_SharedTitleSegmentRepro is the live wrong-apply: the candidate's
// main title ("Hush", "Batman") equals ONE segment of the stored title, and
// the part that tells the volumes apart was ignored. Every row here is a pair
// of different books that origin/main applied unattended.
func TestEvaluate_SharedTitleSegmentRepro(t *testing.T) {
	legends := "Legends of the Dark Knight"
	cases := []struct {
		name string
		book database.Book
		pos  string
		cand metafetch.MetadataCandidate
	}{
		{"Part 1 against Part 2, stale position",
			database.Book{Title: "Hush: Part 1", FilePath: "/lib/Bruce Wayne/Hush: Part 1.m4b"}, "2",
			metafetch.MetadataCandidate{Title: "Hush: Part 2", Series: legends, SeriesPosition: "2"}},
		{"Part 2 against Part 1, mirror",
			database.Book{Title: "Hush: Part 2", FilePath: "/lib/Bruce Wayne/Hush: Part 2.m4b"}, "1",
			metafetch.MetadataCandidate{Title: "Hush: Part 1", Series: legends, SeriesPosition: "1"}},
		{"Part I against Part II, stale position",
			database.Book{Title: "Hush: Part I", FilePath: "/lib/Bruce Wayne/Hush Part I.m4b"}, "2",
			metafetch.MetadataCandidate{Title: "Hush: Part II", Series: legends, SeriesPosition: "2"}},
		{"Part II against Part I, mirror",
			database.Book{Title: "Hush: Part II", FilePath: "/lib/Bruce Wayne/Hush Part II.m4b"}, "1",
			metafetch.MetadataCandidate{Title: "Hush: Part I", Series: legends, SeriesPosition: "1"}},
		{"another storyline in the same franchise, subtitle names the stored one",
			database.Book{Title: "Batman: Dark Victory"}, "1",
			metafetch.MetadataCandidate{Title: "Batman: The Long Halloween", Subtitle: "The Prelude to Dark Victory", Series: legends, SeriesPosition: "1"}},
		{"another storyline in the same franchise, mirror",
			database.Book{Title: "Batman: The Long Halloween"}, "1",
			metafetch.MetadataCandidate{Title: "Batman: Dark Victory", Series: legends, SeriesPosition: "1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ctGate(c.book, c.pos, c.cand)
			if v.Allowed {
				t.Fatalf("APPLIED unattended: stored %q, candidate %q; title check %+v", c.book.Title, c.cand.Title, titleCheck(v.Evidence))
			}
			if v.Reason != ReasonTitleDisagrees {
				t.Fatalf("refused as %q (%s), want %q", v.Reason, v.Detail, ReasonTitleDisagrees)
			}
		})
	}
}

// titleCheck returns the title check of an evidence verdict.
func titleCheck(ev EvidenceVerdict) CheckResult {
	for _, ch := range ev.Checks {
		if ch.Name == "title" {
			return ch
		}
	}
	return CheckResult{}
}
