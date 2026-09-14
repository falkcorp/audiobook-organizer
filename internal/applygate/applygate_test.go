// file: internal/applygate/applygate_test.go
// version: 1.3.0
// guid: 7c1a9e40-3b5f-4d2e-8f61-a0d4c7e9b213
// last-edited: 2026-09-13

package applygate

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/util"
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
	// 10 h of files; a matching candidate carries the same runtime, so the
	// evidence leg has its two agreements (runtime and title).
	book := &database.Book{Title: "Big Cats 1", Duration: intp(36000)}
	good := metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}

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
	other := metafetch.MetadataCandidate{Title: "Small Dogs 1", SeriesPosition: "1", Score: 0.99}
	if v := Evaluate(heard, &other, nil); v.Allowed || v.Reason != ReasonTranscriptionMismatch {
		t.Fatalf("transcription mismatch: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}

// TestTranscriptionConfirms_RealReviewCases: seven books the owner clicked
// Apply on in the review lane on 2026-09-13, verbatim. All seven were refused
// as transcription_mismatch by origin/main's rule (the fail-before test is
// util.TestTranscriptMatch_OldRuleRefusedAllSeven).
//
// unreviewed is TranscriptionConfirms (the gate, metadata.upgrade, an
// unpinned batch apply): it ANDs origin/main's rule, so it refuses all seven,
// exactly as main did; each is applied by an owner clicking Apply on its row.
// reviewed is ReviewedTranscriptionAgrees, the annotation on that row apply.
func TestTranscriptionConfirms_RealReviewCases(t *testing.T) {
	cases := []struct {
		name                    string
		candTitle, candAuthor   string
		heardTitle, heardAuthor string
		unreviewed, reviewed    bool
	}{
		{"Blood of Elves", "Blood of Elves", "Andrzej Sapkowski", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish", false, true},
		// The candidate's own title carries the series suffix; trailers are
		// stripped from the transcribed side only.
		{"A Cry of Honor", "A Cry of Honor (Book #4 in the Sorcerer's Ring)", "Morgan Rice", "A Cry of Honor", "Morgan Rice", false, false},
		{"Witness to a Trial", "Witness to a Trial", "John Grisham", "Witness to a Trial A short story prequel to The Whistler", "John Grisham", false, true},
		{"Knaves Over Queens", "Knaves Over Queens", "George R. R. Martin", "Naves Over Queens", "George R. R. Martin, assisted", false, true},
		{"Sojourn", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator", false, true},
		// Heard "book one"; the review row carried no series position.
		{"This Gilded Abyss", "This Gilded Abyss", "Rebecca Thorne", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne", false, false},
		{"Mistborn", "Mistborn", "Brandon Sanderson", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's", false, true},
		{"same author, different title", "The Well of Ascension", "Brandon Sanderson", "Mistborn", "Brandon Sanderson For Beth Sanderson, who's", false, false},
		{"different volume of the same series", "Big Cats 3", "Ann Author", "Big Cats 1", "Ann Author", false, false},
		{"completely different author", "Blood of Elves", "Morgan Rice", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish", false, false},
		// Both rules agree on a clean exact match.
		{"clean exact match", "Mistborn", "Brandon Sanderson", "Mistborn", "Brandon Sanderson", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{TranscribedTitle: strp(tc.heardTitle), TranscribedAuthor: strp(tc.heardAuthor)}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor}
			if got := TranscriptionConfirms(book, c); got != tc.unreviewed {
				t.Errorf("TranscriptionConfirms = %v, want %v", got, tc.unreviewed)
			}
			if got := ReviewedTranscriptionAgrees(book, c); got != tc.reviewed {
				t.Errorf("ReviewedTranscriptionAgrees = %v, want %v", got, tc.reviewed)
			}
		})
	}
}

// mainRefusedCorpus crosses candidate and heard titles/authors that exercise
// every place the shared matcher is looser than origin/main: trailer strips,
// number words, silent letters, one-typo titles and surnames, initials, and
// credit text after the author.
var mainRefusedCorpus = struct {
	titles, authors []string
}{
	titles: []string{
		"Blood of Elves", "Witness to a Trial", "Witness to a Trial A short story prequel to The Whistler",
		"Knaves Over Queens", "Naves Over Queens", "Sojourn", "This Gilded Abyss",
		"This Gilded Abyss, book one of the Gilded Abyss trilogy", "Ready Player One", "Ready Player 1",
		"Children of Dune Messiah", "Chilren of Dune Messiah", "Dune", "Dune, book two of the Dune Chronicles",
		"Harry Potter", "Harry Potter book 2", "The Expanse", "The Expanse, volume 3", "Mistborn",
	},
	authors: []string{
		"Andrzej Sapkowski", "Andrzej Sapkowski Translated from the Polish", "George R. R. Martin",
		"George R. R. Martin, assisted", "R. A. Salvatore", "R.A. Salvator", "Robert Salvatore",
		"Brandon Sanderson", "Brandon Sanderson For Beth Sanderson, who's", "Frank Herbert", "",
	},
}

// TestUnreviewedPathsRefuseEveryPairMainRefused: over the whole corpus, with
// and without a series position, every pair origin/main's rule refused is
// refused by TranscriptionConfirms and blocked by Evaluate/EvaluateInBatch
// (the gate metadata.upgrade and an unpinned batch apply run) as
// transcription_mismatch, and never earns the relaxed MinScoreAudioConfirmed
// floor. A score of 0.99 keeps the score leg from being the reason.
func TestUnreviewedPathsRefuseEveryPairMainRefused(t *testing.T) {
	refused := 0
	for _, ct := range mainRefusedCorpus.titles {
		for _, ht := range mainRefusedCorpus.titles {
			for _, ca := range mainRefusedCorpus.authors {
				for _, ha := range mainRefusedCorpus.authors {
					if util.MainTranscriptionConfirms(ct, ca, ht, ha) {
						continue
					}
					for _, pos := range []string{"", "1", "2", "3"} {
						refused++
						book := &database.Book{TranscribedTitle: strp(ht), TranscribedAuthor: strp(ha)}
						c := &metafetch.MetadataCandidate{Title: ct, Author: ca, SeriesPosition: pos, Score: 0.99}
						if TranscriptionConfirms(book, c) {
							t.Fatalf("TranscriptionConfirms accepted %q/%q (pos %q) ~ heard %q/%q, which origin/main refused", ct, ca, pos, ht, ha)
						}
						for _, v := range []Verdict{Evaluate(book, c, nil), EvaluateInBatch(book, c, nil, nil)} {
							if v.Allowed || v.AudioConfirmed || v.ScoreFloor != MinScore || v.ScoreReason != ReasonTranscriptionMismatch {
								t.Fatalf("gate on %q/%q (pos %q) ~ heard %q/%q: allowed=%v audio=%v floor=%v score_reason=%q; origin/main refused it",
									ct, ca, pos, ht, ha, v.Allowed, v.AudioConfirmed, v.ScoreFloor, v.ScoreReason)
							}
						}
					}
				}
			}
		}
	}
	if refused < 1000 {
		t.Fatalf("corpus produced only %d refused pairs; it no longer exercises the rule", refused)
	}
}

// TestTranscriptionConfirms_ReviewerFalsePositivesRefuse: the pairs the
// adversarial review measured as wrongly confirmed by the first matcher.
// TranscriptionConfirms is what metadata.upgrade and an unpinned batch apply
// trust with no human in the loop, so every one must refuse.
func TestTranscriptionConfirms_ReviewerFalsePositivesRefuse(t *testing.T) {
	cases := []struct {
		name                           string
		candTitle, candAuthor          string
		heardTitle, heardAuthor, intro string
	}{
		{"subtitle vs series title", "Mistborn: The Hero of Ages", "Brandon Sanderson", "Mistborn", "Brandon Sanderson", ""},
		{"series title vs subtitle", "Mistborn", "Brandon Sanderson", "Mistborn: The Hero of Ages", "Brandon Sanderson", ""},
		{"first book vs second", "Foundation", "Isaac Asimov", "Foundation and Empire", "Isaac Asimov", ""},
		{"second book vs first", "Foundation and Empire", "Isaac Asimov", "Foundation", "Isaac Asimov", ""},
		{"one letter apart", "The Witches", "Roald Dahl", "The Witcher", "Roald Dahl", ""},
		{"same surname, other author", "Sleeping Beauties", "Stephen King", "Sleeping Beauties", "Owen King", ""},
		{"surname only in the intro", "The Return of the King", "Stephen King", "The Return of the King", "J. R. R. Tolkien",
			"The Return of the King, by J.R.R. Tolkien. Narrated by Rob Inglis."},
		{"narrator surname in the intro", "Alex Cross", "James Patterson", "Alex Cross", "Rob Inglis",
			"Alex Cross, narrated by Patterson Joseph."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{TranscribedTitle: strp(tc.heardTitle), TranscribedAuthor: strp(tc.heardAuthor)}
			if tc.intro != "" {
				book.IntroTranscription = strp(tc.intro)
			}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor}
			if TranscriptionConfirms(book, c) {
				t.Errorf("TranscriptionConfirms confirmed %q/%q against heard %q/%q", tc.candTitle, tc.candAuthor, tc.heardTitle, tc.heardAuthor)
			}
		})
	}
}
