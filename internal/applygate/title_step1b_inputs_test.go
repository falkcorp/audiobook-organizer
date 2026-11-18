// file: internal/applygate/title_step1b_inputs_test.go
// version: 1.1.0
// guid: 8b2d4f61-3c97-4a05-9e1b-d7f40c6a3e82
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The review of the first title-check fix predicted a list of inputs it said a
// whole-title rule would get wrong, and a second list it would get right. Each
// is run through the WHOLE gate (Evaluate, so the runtime, partial-book and
// sequence legs all take part) at three runtimes: 5 percent off the book's
// (rt5, inside the 5 percent window), 7 percent off (rt7, outside it) and none
// (the book has no runtime). The table is the rule's behaviour on those inputs
// as measured, one row per input, each cell the verdict reason ("" is an apply)
// and the title leg's outcome.
//
// "refuse" rows are pairs the review judged to be a different book or a wrong
// apply; "same" rows are the same book, where an apply is right. Rows whose
// rt5 cell is an apply with a "neutral" title are the neutral-within-5-percent
// rule at work and are pinned separately, as under owner review, in
// title_owner_review_pins_test.go.

type step1bWant struct {
	reason string // "" for an apply
	title  string // the title leg's outcome
}

type step1bRow struct {
	id   string
	kind string // "refuse" or "same"
	book database.Book
	pos  string
	cand metafetch.MetadataCandidate
	want [3]step1bWant // rt5, rt7, none
}

func step1bRun(runtime int, book database.Book, pos string, cand metafetch.MetadataCandidate) Verdict {
	book.Author = &database.Author{Name: "Bruce Wayne"}
	if pos != "" {
		book.SeriesPositionRaw = new(pos)
	}
	cand.Author = "Bruce Wayne"
	cand.Score = 0.95
	switch runtime {
	case 0:
		book.Duration = new(36000)
		cand.DurationSec = 36600
	case 1:
		book.Duration = new(36000)
		cand.DurationSec = 38520
	default:
		book.Duration = nil
		cand.DurationSec = 36600
	}
	return Evaluate(&book, snap(&book), database.ComputeBookRuntime(&book, nil), &cand, nil)
}

func step1bTitleOutcome(v Verdict) string {
	for _, c := range v.Evidence.Checks {
		if c.Name == "title" {
			return string(c.Outcome)
		}
	}
	return "-"
}

func TestGate_Step1bInputs(t *testing.T) {
	rows := []step1bRow{
		{"B1a", "refuse", database.Book{Title: "Hush", Subtitle: new("Part 2"), FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}}},
		{"B1b", "refuse", database.Book{Title: "Batman", Subtitle: new("Dark Victory"), FilePath: "/lib/Bruce Wayne/Batman/Batman.m4b"}, "", metafetch.MetadataCandidate{Title: "Batman"},
			[3]step1bWant{{"", "neutral"}, {ReasonInsufficientEvidence, "neutral"}, {ReasonInsufficientEvidence, "neutral"}}},
		{"B1c", "refuse", database.Book{Title: "Hush", Subtitle: new("Part 2")}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}}},
		{"B2a", "refuse", database.Book{Title: "Hush, Part 1 of 3"}, "", metafetch.MetadataCandidate{Title: "Hush, Part 3"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"B2b", "refuse", database.Book{Title: "Hush Part 1 of 2"}, "", metafetch.MetadataCandidate{Title: "Hush Part 2"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"B2c", "refuse", database.Book{Title: "Hush 1 of 2"}, "", metafetch.MetadataCandidate{Title: "Hush 2"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"B2d", "refuse", database.Book{Title: "Hush: Part 1 of 2"}, "", metafetch.MetadataCandidate{Title: "Hush: Part 2"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"M1a", "refuse", database.Book{Title: "Batman: Dark Victory", FilePath: "/lib/Bruce Wayne/Batman/Batman - Dark Victory.m4b"}, "", metafetch.MetadataCandidate{Title: "Batman"},
			[3]step1bWant{{"", "neutral"}, {ReasonInsufficientEvidence, "neutral"}, {ReasonRuntimeUnknownOverwrite, "neutral"}}},
		{"M1b", "refuse", database.Book{Title: "Batman: Dark Victory", FilePath: "/lib/Bruce Wayne/The Long Halloween/The Long Halloween.m4b"}, "", metafetch.MetadataCandidate{Title: "The Long Halloween"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonRuntimeUnknownOverwrite, "block"}}},
		{"M2a", "refuse", database.Book{Title: "", Series: &database.Series{Name: "Batman"}, Narrator: new("Alfred Pennyworth"), FilePath: "/lib/Bruce Wayne/Batman/Batman.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush", Narrator: "Alfred Pennyworth"},
			[3]step1bWant{{"", "unknown"}, {"", "unknown"}, {"", "unknown"}}},
		{"M3a", "refuse", database.Book{Title: "Hush I"}, "", metafetch.MetadataCandidate{Title: "Hush II"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}}},
		{"M3b", "refuse", database.Book{Title: "Hush II"}, "", metafetch.MetadataCandidate{Title: "Hush I"},
			[3]step1bWant{{ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}}},
		{"M4a", "same", database.Book{Title: "Knightfall (Batman, #1)"}, "", metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Batman, Book 1"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}}},
		{"M4b", "same", database.Book{Title: "Knightfall", Subtitle: new("A Batman Novel")}, "", metafetch.MetadataCandidate{Title: "Knightfall", Subtitle: "Batman, Book 1"},
			[3]step1bWant{{ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}, {ReasonTitleDisagrees, "block"}}},
		// The sequence leg now reads the stored subtitle (BookNumbers): the book
		// is volume 2 and the candidate states none, so it refuses first. The
		// title leg still blocks (the two subtitles differ).
		{"M4c", "same", database.Book{Title: "Hush", Subtitle: new("Batman, Book 2")}, "", metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Gotham Story"},
			[3]step1bWant{{ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}, {ReasonSequenceMissingOnCandidate, "block"}}},
		{"M5a", "same", database.Book{Title: "Hush"}, "", metafetch.MetadataCandidate{Title: "Hush", Subtitle: "A Novel"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {"", "agree"}}},
		{"M5b", "same", database.Book{Title: "Hush", Subtitle: new("Unabridged")}, "", metafetch.MetadataCandidate{Title: "Hush"},
			[3]step1bWant{{"", "agree"}, {"", "agree"}, {"", "agree"}}},
		{"S1", "refuse", database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush 1.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush 2"},
			[3]step1bWant{{ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}}},
		{"S2", "same", database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush 2.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush 2"},
			[3]step1bWant{{"", "neutral"}, {ReasonInsufficientEvidence, "neutral"}, {ReasonInsufficientEvidence, "neutral"}}},
		{"S3", "same", database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush 2"},
			[3]step1bWant{{"", "neutral"}, {ReasonInsufficientEvidence, "neutral"}, {ReasonInsufficientEvidence, "neutral"}}},
		{"S4", "same", database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush 1/"}, "", metafetch.MetadataCandidate{Title: "Hush 2"},
			[3]step1bWant{{ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}, {ReasonSequenceMismatch, "neutral"}}},
		{"W1", "refuse", database.Book{Title: "Who Am I"}, "", metafetch.MetadataCandidate{Title: "Who Am I 2"},
			[3]step1bWant{{"", "neutral"}, {ReasonInsufficientEvidence, "neutral"}, {ReasonInsufficientEvidence, "neutral"}}},
		{"M6a", "refuse", database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush - Part 1.m4b"}, "", metafetch.MetadataCandidate{Title: "Hush", Subtitle: "Part 2"},
			[3]step1bWant{{ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}, {ReasonPartialBook, "neutral"}}},
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
