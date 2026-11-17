// file: internal/applygate/title_owner_review_pins_test.go
// version: 1.0.1
// guid: 4e7a91d3-2b58-4c06-a9f1-0d63b8e5c274
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// UNDER OWNER REVIEW. Every test in this file pins today's outcome for a pair
// whose title leg is NEUTRAL (the two titles share text but neither equals the
// other and nothing conflicts) and which applies only because the runtimes
// agree within 5 percent. The rule is deliberate: a neutral title confirms
// nothing, and a runtime within the window is what carries the apply (see
// tally in evidence.go). Whether that is enough for these shapes is the owner's
// call: the replay found that 49 of the 61 applies that depend on a neutral
// title are of this kind, so changing the rule changes most of what applies
// that depends on a neutral title (todo.d/2026-11-17-applygate-neutral-title-review.md).
//
// These tests are not a claim that the outcome is right. They exist so that the
// outcome cannot change by accident: if the owner decides one of these shapes
// must be refused, the change is one deliberate edit to the rule and to the row
// here, not a side effect of another title-rule change.

type neutralPin struct {
	name string
	book database.Book
	pos  string
	cand metafetch.MetadataCandidate
	// the verdict reason at a runtime 5 percent off, 7 percent off, and none
	// ("" is an apply)
	want [3]string
}

func TestUnderOwnerReview_NeutralTitleAppliesOnAnAgreeingRuntime(t *testing.T) {
	pins := []neutralPin{
		{
			// a franchise word added in front of the stored title
			name: "a franchise word in front of the stored title",
			book: database.Book{Title: "Hush", FilePath: "/lib/Bruce Wayne/Hush/Hush.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Batman: Hush"},
			want: [3]string{"", ReasonInsufficientEvidence, ReasonInsufficientEvidence},
		},
		{
			// B1b: the stored subtitle names the book and the candidate is only
			// the franchise
			name: "B1b stored subtitle names the book, candidate is the franchise",
			book: database.Book{Title: "Batman", Subtitle: new("Dark Victory"), FilePath: "/lib/Bruce Wayne/Batman/Batman.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Batman"},
			want: [3]string{"", ReasonInsufficientEvidence, ReasonInsufficientEvidence},
		},
		{
			// M1a: the stored title is "franchise: book" and the candidate is the
			// franchise alone; the file name carries the same text
			name: "M1a stored franchise and book, candidate is the franchise",
			book: database.Book{Title: "Batman: Dark Victory", FilePath: "/lib/Bruce Wayne/Batman/Batman - Dark Victory.m4b"},
			cand: metafetch.MetadataCandidate{Title: "Batman"},
			want: [3]string{"", ReasonInsufficientEvidence, ReasonRuntimeUnknownOverwrite},
		},
	}
	for _, p := range pins {
		for i, name := range []string{"runtime 5% off", "runtime 7% off", "no book runtime"} {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				v := step1bRun(i, p.book, p.pos, p.cand)
				if title := step1bTitleOutcome(v); title != "neutral" {
					t.Fatalf("title leg %q, want neutral: %s", title, v.Detail)
				}
				got := ""
				if !v.Allowed {
					got = v.Reason
				}
				if got != p.want[i] {
					t.Fatalf("reason %q, want %q: %s", got, p.want[i], v.Detail)
				}
			})
		}
	}
}

// M2a (under owner review): a book whose stored title is blank and whose only
// text is its file name and series. The candidate's title shares nothing with
// either, and its narrator equals the book's narrator. The title leg is
// UNKNOWN (nothing to compare), which does not need an agreeing runtime, so
// the pair applies at every runtime, with no runtime at all included.
func TestUnderOwnerReview_BlankStoredTitleWithNothingToCompareApplies(t *testing.T) {
	book := database.Book{Title: "", Series: &database.Series{Name: "Batman"}, Narrator: new("Alfred Pennyworth"),
		FilePath: "/lib/Bruce Wayne/Batman/Batman.m4b"}
	cand := metafetch.MetadataCandidate{Title: "Hush", Narrator: "Alfred Pennyworth"}
	for i, name := range []string{"runtime 5% off", "runtime 7% off", "no book runtime"} {
		t.Run(name, func(t *testing.T) {
			v := step1bRun(i, book, "", cand)
			if title := step1bTitleOutcome(v); title != "unknown" {
				t.Fatalf("title leg %q, want unknown: %s", title, v.Detail)
			}
			if !v.Allowed {
				t.Fatalf("refused (%s), want an apply: %s", v.Reason, v.Detail)
			}
		})
	}
}
