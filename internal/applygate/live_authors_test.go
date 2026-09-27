// file: internal/applygate/live_authors_test.go
// version: 1.0.0
// guid: 0c5d2e81-7a4b-4f93-9e16-b8a3c1d7f042
// last-edited: 2026-09-27

package applygate

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func authorEvidence(t *testing.T, v EvidenceVerdict) CheckResult {
	t.Helper()
	for _, ch := range v.Checks {
		if ch.Name == "author_evidence" {
			return ch
		}
	}
	t.Fatalf("no author_evidence check: %+v", v.Checks)
	return CheckResult{}
}

// The evidence leg reads the authors it is given and ignores Book.Author: the
// prod Valis shape, a stale "Unknown Author" snapshot and a path under the
// placeholder folder, with the live author equal to the candidate's.
func TestCheckEvidence_ReadsLiveAuthorsNotSnapshot(t *testing.T) {
	book := &database.Book{
		Title: "Valis", FilePath: "/lib/Unknown Author/Valis/Valis.m4b",
		Author: &database.Author{Name: "Unknown Author"},
	}
	cand := &metafetch.MetadataCandidate{Title: "Valis", Author: "Philip K. Dick", DurationSec: 36000}
	rt := database.BookRuntime{Seconds: 36000, Source: database.RuntimeSourceBook}

	if ch := authorEvidence(t, CheckEvidence(book, Authors{"Philip K. Dick"}, rt, cand, false)); ch.Outcome != OutcomeAgree {
		t.Fatalf("live author Philip K. Dick: %s/%s %s, want agree", ch.Outcome, ch.Reason, ch.Detail)
	}
	// With no live author the snapshot must NOT stand in for one.
	book.Author = &database.Author{Name: "Philip K. Dick"}
	if ch := authorEvidence(t, CheckEvidence(book, nil, rt, cand, false)); ch.Outcome != OutcomeBlock || ch.Reason != ReasonAuthorNotInPath {
		t.Fatalf("no live author: %s/%s, want block %s (the snapshot is not evidence)", ch.Outcome, ch.Reason, ReasonAuthorNotInPath)
	}
}

// The placeholder is no author: it neither counts as an author the candidate
// would overwrite nor as evidence.
func TestCheckEvidence_PlaceholderLiveAuthorIsNoAuthor(t *testing.T) {
	book := &database.Book{Title: "Valis", FilePath: "/lib/Unknown Author/Valis/Valis.m4b"}
	cand := &metafetch.MetadataCandidate{Title: "Valis", Author: "Philip K. Dick"}
	v := CheckEvidence(book, Authors{"Unknown Author", " "}, database.BookRuntime{}, cand, false)
	for _, o := range v.Overwrites {
		if o == "author" {
			t.Fatalf("placeholder counted as an overwritten author: %v", v.Overwrites)
		}
	}
}

// Every live credit is protected: dropping a co-author is an overwrite, and a
// co-author is evidence.
func TestCheckEvidence_EveryLiveCredit(t *testing.T) {
	book := &database.Book{Title: "Good Omens", FilePath: "/lib/x/Good Omens.m4b"}
	live := Authors{"Terry Pratchett", "Neil Gaiman"}
	onlyFirst := &metafetch.MetadataCandidate{Title: "Good Omens", Author: "Terry Pratchett"}
	if got := overwrites(book, live, onlyFirst); len(got) != 1 || got[0] != "author" {
		t.Fatalf("overwrites = %v, want [author]: the candidate drops Gaiman", got)
	}
	second := &metafetch.MetadataCandidate{Title: "Good Omens", Author: "Neil Gaiman"}
	if ch := checkAuthorPath(book, live, second); ch.Outcome != OutcomeAgree {
		t.Fatalf("co-author as evidence: %s, want agree", ch.Outcome)
	}
}
