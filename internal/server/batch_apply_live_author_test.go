// file: internal/server/batch_apply_live_author_test.go
// version: 1.0.1
// guid: 6b0e3f27-94c1-4a8d-b2e5-1d7c9a4f0e63
// last-edited: 2026-09-27
//
// The certainty gate must judge a candidate against the book's LIVE author
// (AuthorID and the book_authors join), never the denormalized Book.Author
// snapshot persisted inside the book row. On 2026-09-27 prod refused 27/27
// books with author_not_in_path although the API's author_name was exactly
// the candidate's author (Valis / Philip K. Dick): the snapshot was stale or
// nil, and their folders are ".../Unknown Author/<title>", so the path could
// not help either.

package server

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// liveAuthorBooks is fakeBooks with an author table and book_authors joins,
// so a book's live author can differ from its embedded snapshot.
type liveAuthorBooks struct {
	fakeBooks
	authors   map[int]*database.Author
	joins     map[string][]database.BookAuthor
	authorErr error
	joinErr   error
}

func (f liveAuthorBooks) GetAuthorByID(id int) (*database.Author, error) {
	if f.authorErr != nil {
		return nil, f.authorErr
	}
	return f.authors[id], nil
}

func (f liveAuthorBooks) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if f.joinErr != nil {
		return nil, f.joinErr
	}
	return f.joins[bookID], nil
}

const (
	pkdID     = 101
	unknownID = 54846
)

// valisFixture is the prod shape: AuthorID and the join name Philip K. Dick,
// the folder is the organizer's "Unknown Author" placeholder, and snapshot is
// whatever the book row happens to carry (nil, or a stale placeholder).
func valisFixture(snapshot *database.Author) (liveAuthorBooks, metafetch.MetadataCandidate) {
	id := pkdID
	tenHours := 36000
	book := &database.Book{
		ID: "valis", Title: "Valis", AuthorID: &id, Author: snapshot,
		FilePath: "/lib/Unknown Author/Valis/Valis.m4b", Duration: &tenHours,
	}
	books := liveAuthorBooks{
		fakeBooks: fakeBooks{"valis": book},
		authors: map[int]*database.Author{
			pkdID:     {ID: pkdID, Name: "Philip K. Dick"},
			unknownID: {ID: unknownID, Name: "Unknown Author"},
		},
		joins: map[string][]database.BookAuthor{
			"valis": {{BookID: "valis", AuthorID: pkdID, Role: "author", Position: 0}},
		},
	}
	cand := metafetch.MetadataCandidate{Title: "Valis", Author: "Philip K. Dick", Source: "Audible", Score: 0.95, DurationSec: 36000}
	return books, cand
}

func authorCheck(t *testing.T, v *applygate.Verdict) applygate.CheckResult {
	t.Helper()
	if v == nil {
		t.Fatal("gate did not run")
	}
	for _, ch := range v.Evidence.Checks {
		if ch.Name == "author_evidence" {
			return ch
		}
	}
	t.Fatalf("no author_evidence check in %+v", v.Evidence.Checks)
	return applygate.CheckResult{}
}

func TestGateUsesLiveAuthor_NotStaleSnapshot(t *testing.T) {
	cases := map[string]*database.Author{
		"nil snapshot":   nil,
		"stale snapshot": {ID: unknownID, Name: "Unknown Author"},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			books, cand := valisFixture(snap)
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			plan := planCachedApply(svc, books, "valis", nil, nil)
			ch := authorCheck(t, plan.Gate)
			if ch.Outcome == applygate.OutcomeBlock {
				t.Fatalf("author_evidence blocked (%s: %s); the live author is the candidate's", ch.Reason, ch.Detail)
			}
			if ch.Outcome != applygate.OutcomeAgree {
				t.Errorf("author_evidence = %s, want agree: the live author names the candidate's surname", ch.Outcome)
			}
		})
	}
}

// The op-results path judges the same book the same way.
func TestGateUsesLiveAuthor_OpResultPath(t *testing.T) {
	books, cand := valisFixture(nil)
	cr := CandidateResult{Candidate: &cand, Book: CandidateBookInfo{ID: "valis", Title: "Valis"}}
	plan := planOpResultApply(books, "valis", cr, nil)
	if ch := authorCheck(t, plan.Gate); ch.Outcome != applygate.OutcomeAgree {
		t.Fatalf("author_evidence = %s (%s: %s), want agree", ch.Outcome, ch.Reason, ch.Detail)
	}
}

// A live author that is NOT the candidate's still blocks: the fix reads the
// right author, it does not stop checking.
func TestGateUsesLiveAuthor_WrongLiveAuthorStillBlocks(t *testing.T) {
	books, cand := valisFixture(&database.Author{ID: pkdID, Name: "Philip K. Dick"})
	cand.Author = "Stephen King"
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	plan := planCachedApply(svc, books, "valis", nil, nil)
	if ch := authorCheck(t, plan.Gate); ch.Outcome != applygate.OutcomeBlock || ch.Reason != applygate.ReasonAuthorNotInPath {
		t.Fatalf("author_evidence = %s/%s, want block %s", ch.Outcome, ch.Reason, applygate.ReasonAuthorNotInPath)
	}
}

// The live placeholder author is "no author", exactly as a nil snapshot was:
// a book filed under "Unknown Author" does not gain an author to protect.
func TestGateUsesLiveAuthor_PlaceholderIsNoAuthor(t *testing.T) {
	books, cand := valisFixture(nil)
	id := unknownID
	books.fakeBooks["valis"].AuthorID = &id
	books.joins["valis"] = []database.BookAuthor{{BookID: "valis", AuthorID: unknownID, Role: "author"}}
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	plan := planCachedApply(svc, books, "valis", nil, nil)
	for _, o := range plan.Gate.Evidence.Overwrites {
		if o == "author" {
			t.Fatalf("placeholder author counted as an author the candidate would overwrite: %v", plan.Gate.Evidence.Overwrites)
		}
	}
}

// A co-author credited only in the join is a live author too.
func TestGateUsesLiveAuthor_JoinCoAuthor(t *testing.T) {
	books, cand := valisFixture(nil)
	books.authors[202] = &database.Author{ID: 202, Name: "Roger Zelazny"}
	books.joins["valis"] = append(books.joins["valis"], database.BookAuthor{BookID: "valis", AuthorID: 202, Role: "author", Position: 1})
	cand.Author = "Roger Zelazny"
	svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
	plan := planCachedApply(svc, books, "valis", nil, nil)
	if ch := authorCheck(t, plan.Gate); ch.Outcome != applygate.OutcomeAgree {
		t.Fatalf("author_evidence = %s (%s), want agree through the join co-author", ch.Outcome, ch.Detail)
	}
}

// An unreadable author must refuse the book, not evaluate it as authorless:
// "no author" loosens the gate (nothing to overwrite, so an unknown runtime
// no longer blocks).
func TestGateUsesLiveAuthor_ReadFailureRefuses(t *testing.T) {
	for name, mut := range map[string]func(*liveAuthorBooks){
		"author read": func(b *liveAuthorBooks) { b.authorErr = errors.New("pebble: closed") },
		"join read":   func(b *liveAuthorBooks) { b.joinErr = errors.New("pebble: closed") },
	} {
		t.Run(name, func(t *testing.T) {
			books, cand := valisFixture(nil)
			mut(&books)
			svc := &fakeApplySvc{candidates: candidateJSON(t, cand)}
			out := applyCachedCandidateForBookTimed(svc, books, nil, "valis", false, nil, metafetch.NewApplyPhaseTimings(), nil, rowPin(cand), "")
			if out.Applied || out.Reason != applySkipAuthorsUnreadable {
				t.Fatalf("outcome %+v, want %s", out, applySkipAuthorsUnreadable)
			}
			if len(svc.appliedIDs) != 0 {
				t.Fatalf("applied %v with an unreadable author", svc.appliedIDs)
			}
		})
	}
}

// fetchTimeIdentity accepts the author the fetch recorded in any form the
// book's current author takes (snapshot, live primary, live joined), and
// still refuses an author the book no longer has.
func TestFetchTimeIdentity_LiveAuthorForms(t *testing.T) {
	book := &database.Book{ID: "valis", Title: "Valis"}
	live := []string{"Philip K. Dick"}
	for _, recorded := range []string{"", "Philip K. Dick"} {
		if err := fetchTimeIdentity("Valis", recorded, book, live); err != nil {
			t.Errorf("recorded %q: %v, want nil", recorded, err)
		}
	}
	if err := fetchTimeIdentity("Valis", "Stephen King", book, live); !errors.Is(err, metafetch.ErrStaleMetadataCache) {
		t.Errorf("recorded Stephen King: %v, want ErrStaleMetadataCache", err)
	}
}

// The dry run reports an unreadable author as blocked, not as "nothing to
// apply": the apply would refuse the book and a retry may succeed.
func TestPreviewBulkApplyRow_AuthorsUnreadableIsBlocked(t *testing.T) {
	cand := metafetch.MetadataCandidate{Title: "Valis", Score: 0.95}
	plan := cachedApplyPlan{Book: &database.Book{ID: "valis", Title: "Valis"}, Candidate: &cand,
		Reason: applySkipAuthorsUnreadable, Err: errors.New("read author 101 of valis: pebble: closed")}
	row := previewBulkApplyRow(blockingPreview{&fakeApplySvc{}}, "valis", plan, true)
	if row.Verdict != previewVerdictBlocked || row.Reason != applySkipAuthorsUnreadable {
		t.Fatalf("verdict %q reason %q, want blocked/%s", row.Verdict, row.Reason, applySkipAuthorsUnreadable)
	}
}
