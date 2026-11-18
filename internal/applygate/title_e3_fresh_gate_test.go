// file: internal/applygate/title_e3_fresh_gate_test.go
// version: 1.0.0
// guid: 4c7e1a95-2d38-4b60-9f14-e08b6a3d5c27
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The reviewer pairs of the fresh review of the E3 rule (blocker B2, majors M1
// and M2, documented class D1), each run through the whole gate at the same
// three runtimes step1bRun uses: rt5 (5 percent off, inside the window), rt7
// (outside it) and none (the book has no runtime). Each cell is the verdict
// reason ("" is an apply) and the title leg's outcome.
//
// B2b ("Gotham" against "Gotham Knights Detective Tales") is the stored-title-
// is-a-subset direction: the candidate adds words, and a neutral title may apply
// on an agreeing runtime. The opposite direction (B2a) is refused: a candidate
// that is one word of a longer stored title is another book of that author.
func TestGate_E3FreshPairs(t *testing.T) {
	ins := ReasonInsufficientEvidence
	rows := []step1bRow{
		{"B2a", "refuse", database.Book{Title: "Gotham Knights Detective Tales"}, "", metafetch.MetadataCandidate{Title: "Gotham"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"B2b", "same", database.Book{Title: "Gotham"}, "", metafetch.MetadataCandidate{Title: "Gotham Knights Detective Tales"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ins, "neutral"}}},
		{"M1a", "refuse", database.Book{Title: "Robin X", FilePath: "/lib/Bruce Wayne/Robin/Robin X.m4b"}, "", metafetch.MetadataCandidate{Title: "Robin"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ins, "neutral"}}},
		{"M1b", "same", database.Book{Title: "", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {"", "agree"}}},
		{"M2a", "refuse", database.Book{Title: "Hush", Subtitle: new("Batman, Book 2")}, "", metafetch.MetadataCandidate{Title: "Hush", Series: "Batman", SeriesPosition: "3"},
			[3]step1bWant{{ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}}},
		{"M2b", "same", database.Book{Title: "Hush", Subtitle: new("Batman, Book 2")}, "", metafetch.MetadataCandidate{Title: "Hush", Series: "Batman", SeriesPosition: "2"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {"", "agree"}}},
		{"M2c", "refuse", database.Book{Title: "Hush", Subtitle: new("Batman, Book 2")}, "", metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story"},
			[3]step1bWant{{ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}}},
		{"D1a", "refuse", database.Book{Title: "Batman"}, "", metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ins, "neutral"}}},
		// A form that states the position still agrees: "S N T" is the series,
		// the position and the title, here "batman 4 batman".
		{"D1b", "same", database.Book{Title: "Batman 4 Batman"}, "", metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {ReasonRuntimeUnknownOverwrite, "agree"}}},
		{"D1c", "refuse", database.Book{Title: "Batman", Series: &database.Series{Name: "Batman"}}, "", metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ins, "neutral"}}},
	}
	for _, r := range rows {
		for i, name := range []string{"runtime 5% off", "runtime 7% off", "no book runtime"} {
			t.Run(r.id+"/"+name, func(t *testing.T) {
				v := step1bRun(i, r.book, r.pos, r.cand)
				got := step1bWant{title: step1bTitleOutcome(v)}
				if !v.Allowed {
					got.reason = v.Reason
				}
				if got != r.want[i] {
					t.Fatalf("%s (%s) %s: got reason %q title %q, want reason %q title %q (detail: %s)",
						r.id, r.kind, name, got.reason, got.title, r.want[i].reason, r.want[i].title, v.Detail)
				}
			})
		}
	}
}
