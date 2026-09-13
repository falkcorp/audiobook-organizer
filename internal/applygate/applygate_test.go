// file: internal/applygate/applygate_test.go
// version: 1.0.0
// guid: 7c1a9e40-3b5f-4d2e-8f61-a0d4c7e9b213
// last-edited: 2026-09-13

package applygate

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// TestCheckSequence is the table for the guard's decision rule. The Big Cats
// rows are the owner's reported failure: every cross pairing of 1/2/3 must
// block, and every matching pairing must pass.
func TestCheckSequence(t *testing.T) {
	type tc struct {
		name       string
		book       database.Book
		cand       metafetch.MetadataCandidate
		wantPass   bool
		wantReason string
	}
	var cases []tc
	for _, b := range []string{"1", "2", "3"} {
		for _, c := range []string{"1", "2", "3"} {
			want := b == c
			reason := ""
			if !want {
				reason = ReasonSequenceMismatch
			}
			cases = append(cases, tc{
				name:       "big cats book " + b + " vs candidate " + c,
				book:       database.Book{Title: "big cats " + b, FilePath: "/lib/A/Big Cats/big cats " + b + ".m4b"},
				cand:       metafetch.MetadataCandidate{Title: "Big Cats " + c, SeriesPosition: c},
				wantPass:   want,
				wantReason: reason,
			})
		}
	}
	cases = append(cases,
		tc{"candidate number only in series position", database.Book{Title: "Big Cats 3"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "3"}, true, ""},
		tc{"candidate number only in subtitle", database.Book{Title: "Big Cats 3"},
			metafetch.MetadataCandidate{Title: "Big Cats", Subtitle: "Big Cats, Book 3"}, true, ""},
		tc{"book has number, candidate none", database.Book{Title: "Big Cats 1"},
			metafetch.MetadataCandidate{Title: "Big Cats"}, false, ReasonSequenceMissingOnCandidate},
		tc{"neither has number", database.Book{Title: "Dune"},
			metafetch.MetadataCandidate{Title: "Dune"}, true, ""},
		tc{"only candidate has number", database.Book{Title: "Big Cats"},
			metafetch.MetadataCandidate{Title: "Big Cats 2"}, true, ""},
		tc{"roman on book, arabic on candidate", database.Book{Title: "Big Cats Volume III"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "3"}, true, ""},
		tc{"roman mismatch", database.Book{Title: "Big Cats Vol. II"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "3"}, false, ReasonSequenceMismatch},
		tc{"decimal is not its integer", database.Book{Title: "Big Cats 1.5"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "1"}, false, ReasonSequenceMismatch},
		tc{"decimal matches decimal", database.Book{SeriesPositionRaw: strp("1.5"), Title: "Big Cats: A Novella"},
			metafetch.MetadataCandidate{Title: "Big Cats: A Novella", SeriesPosition: "1.5"}, true, ""},
		tc{"year is not a number", database.Book{Title: "Big Cats (2019)"},
			metafetch.MetadataCandidate{Title: "Big Cats"}, true, ""},
		tc{"part marker is not a number", database.Book{Title: "Big Cats Part 1 of 3"},
			metafetch.MetadataCandidate{Title: "Big Cats"}, true, ""},
		tc{"disc 2 vs disc 3 does not block", database.Book{Title: "Big Cats Disc 2"},
			metafetch.MetadataCandidate{Title: "Big Cats Disc 3"}, true, ""},
		tc{"number from file name only", database.Book{Title: "Big Cats", FilePath: "/lib/A/Big Cats/02 - Big Cats.m4b"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "3"}, false, ReasonSequenceMismatch},
		tc{"number from folder only", database.Book{Title: "Big Cats", FilePath: "/lib/A/Big Cats Book 2"},
			metafetch.MetadataCandidate{Title: "Big Cats", SeriesPosition: "2"}, true, ""},
		tc{"series position vs title disagree on book", database.Book{Title: "Big Cats 3", SeriesSequence: intp(1)},
			metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3"}, false, ReasonBookSequenceConflict},
		tc{"title vs file disagree on book (earlier bad apply)", database.Book{Title: "Big Cats 3", FilePath: "/lib/A/Big Cats/Big Cats 1.m4b"},
			metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3"}, false, ReasonBookSequenceConflict},
		tc{"candidate position vs title disagree", database.Book{Title: "Big Cats 1"},
			metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "1"}, false, ReasonCandidateSequenceConflict},
	)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := CheckSequence(&c.book, &c.cand)
			if v.Pass != c.wantPass || v.Reason != c.wantReason {
				t.Fatalf("CheckSequence = pass %v reason %q (%s), want pass %v reason %q",
					v.Pass, v.Reason, v.Detail, c.wantPass, c.wantReason)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	book := &database.Book{Title: "Big Cats 1"}
	good := metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.95}

	if v := Evaluate(book, &good, nil); !v.Allowed {
		t.Fatalf("matching 0.95 candidate refused: %+v", v)
	}

	// The owner's failure: a near-perfect score on the wrong volume.
	wrong := metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3", Score: 0.99}
	if v := Evaluate(book, &wrong, nil); v.Allowed || v.Reason != ReasonSequenceMismatch {
		t.Fatalf("0.99 wrong-volume candidate: allowed=%v reason=%q, want blocked %q", v.Allowed, v.Reason, ReasonSequenceMismatch)
	}

	low := good
	low.Score = 0.89
	if v := Evaluate(book, &low, nil); v.Allowed || v.Reason != ReasonScoreBelowFloor {
		t.Fatalf("0.89 candidate: allowed=%v reason=%q, want %q", v.Allowed, v.Reason, ReasonScoreBelowFloor)
	}
	atFloor := good
	atFloor.Score = MinScore
	if v := Evaluate(book, &atFloor, nil); !v.Allowed {
		t.Fatalf("candidate exactly at the floor refused: %+v", v)
	}

	if v := Evaluate(book, &good, errors.New("hash drift")); v.Allowed || v.Reason != ReasonIdentityStale {
		t.Fatalf("stale identity: allowed=%v reason=%q", v.Allowed, v.Reason)
	}

	// Audio confirmation relaxes the floor to 0.85 ...
	heard := &database.Book{Title: "Big Cats 1", TranscribedTitle: strp("Big Cats 1")}
	relaxed := good
	relaxed.Score = 0.86
	if v := Evaluate(heard, &relaxed, nil); !v.Allowed || !v.AudioConfirmed {
		t.Fatalf("audio-confirmed 0.86 refused: %+v", v)
	}
	// ... and a transcribed title the candidate does not match refuses outright.
	other := metafetch.MetadataCandidate{Title: "Big Cats 1 Special Edition", SeriesPosition: "1", Score: 0.99}
	if v := Evaluate(heard, &other, nil); v.Allowed || v.Reason != ReasonTranscriptionMismatch {
		t.Fatalf("transcription mismatch: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}
