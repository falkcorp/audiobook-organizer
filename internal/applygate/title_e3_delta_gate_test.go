// file: internal/applygate/title_e3_delta_gate_test.go
// version: 1.0.0
// guid: 8b3d5f27-6a14-4c90-b2e8-1f7a9c04d653
// last-edited: 2026-11-18

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The reviewer pairs of the delta review of the E3 rule (D1' credit forms of a
// self-titled candidate, M1' rip junk, m10 and D3 year numbers), each run
// through the whole gate at the three runtimes step1bRun uses: rt5 (5 percent
// off, inside the window), rt7 (outside it) and none (the book has no runtime).
// Each cell is the verdict reason ("" is an apply) and the title leg's outcome.
//
// D1': a candidate whose title is its own series name says no volume by its
// bare title, and says none by the title with its author's name beside it
// ("Batman - Bruce Wayne"): the credit is not a volume. Both are neutral beside
// a stored title. A file name that stands in for a blank title does not agree
// through either form (the series-name block refuses it, as main does).
// A form that states the position ("Batman 4 Batman Bruce Wayne") still agrees.
func TestGate_E3DeltaPairs(t *testing.T) {
	ins := ReasonInsufficientEvidence
	neutral := [3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ReasonRuntimeUnknownOverwrite, "neutral"}}
	// A blank stored title with a self-titled candidate has no stored text to
	// be neutral about: the file name restating the candidate's own title is
	// the series-name block's to refuse, as it is on main.
	refused := [3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}}
	bat := metafetch.MetadataCandidate{Title: "Batman", Series: "Batman", SeriesPosition: "4"}
	rows := []step1bRow{
		{"D1'a", "refuse", database.Book{Title: "Batman - Bruce Wayne"}, "", bat, neutral},
		{"D1'b", "refuse", database.Book{Title: "Bruce Wayne - Batman"}, "", bat, neutral},
		// The path pair, no runtime included: a blank stored title and a file
		// name that is the credit form no longer agrees.
		{"D1'c", "refuse", database.Book{Title: "", FilePath: "/lib/Bruce Wayne/Batman - Bruce Wayne.m4b"}, "", bat, refused},
		{"D1'd", "refuse", database.Book{Title: "", FilePath: "/lib/Bruce Wayne/Bruce Wayne - Batman.m4b"}, "", bat, refused},
		// Controls: the form that states the position still agrees, and a bare
		// file name equal to the title (not a credit form) is still the title.
		{"D1'e", "same", database.Book{Title: "Batman 4 Batman Bruce Wayne"}, "", bat,
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {ReasonRuntimeUnknownOverwrite, "agree"}}},
		{"D1'f", "same", database.Book{Title: "", FilePath: "/lib/Bruce Wayne/Batman 4 Batman/Batman 4 Batman.m4b"}, "", bat,
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {"", "agree"}}},

		// M1': rip junk is judged by what is left of the title, so the folder
		// name no longer stands in for a title that says something. "Robin X
		// [64kbps]" keeps its rip words in the comparison and shares too little
		// with "Robin"; "Hush: Dark Victory [64k]" continues past "Hush" and is
		// neutral, which an agreeing runtime alone can apply.
		{"M1'a", "refuse", database.Book{Title: "Robin X [64kbps]", FilePath: "/lib/Bruce Wayne/Robin/Robin X [64kbps].m4b"}, "", metafetch.MetadataCandidate{Title: "Robin"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"M1'b", "refuse", database.Book{Title: "Hush: Dark Victory [64k]", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ReasonRuntimeUnknownOverwrite, "neutral"}}},
		// Control: a title that is nothing but rip junk is blank, and the file
		// name stands in for it.
		{"M1'c", "same", database.Book{Title: "[64kbps]", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {ReasonRuntimeUnknownOverwrite, "agree"}}},

		// m10 and D3: a year-like number that differs is a different title.
		{"m10a", "refuse", database.Book{Title: "Gotham (2033)"}, "", metafetch.MetadataCandidate{Title: "Gotham (2034)"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"m10b", "refuse", database.Book{Title: "Gotham 2033"}, "", metafetch.MetadataCandidate{Title: "Gotham (2034)"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"D3a", "refuse", database.Book{Title: "Gotham 2033"}, "", metafetch.MetadataCandidate{Title: "Gotham 2"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"D3b", "refuse", database.Book{Title: "Gotham 2033"}, "", metafetch.MetadataCandidate{Title: "Gotham: Book 2"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		// Control: a year the other side has no number to set against is a tag.
		{"D3c", "same", database.Book{Title: "Gotham 2033"}, "", metafetch.MetadataCandidate{Title: "Gotham: The Long Night"},
			[3]step1bWant{{"", "neutral"}, {ins, "neutral"}, {ReasonRuntimeUnknownOverwrite, "neutral"}}},
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
