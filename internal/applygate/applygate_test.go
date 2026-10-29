// file: internal/applygate/applygate_test.go
// version: 1.5.7
// guid: 7c1a9e40-3b5f-4d2e-8f61-a0d4c7e9b213
// last-edited: 2026-10-29

package applygate

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// snap is the fixture book's Author as its live credit. The gate never reads
// Book.Author itself (see Authors); these fixtures predate that and use the
// field as a convenient place to say "this book's author is X".
func snap(b *database.Book) Authors {
	if b == nil || b.Author == nil {
		return nil
	}
	return Authors{b.Author.Name}
}

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
		tc{"decimal matches decimal", database.Book{SeriesPositionRaw: new("1.5"), Title: "Big Cats: A Novella"},
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
		tc{"series position vs title disagree on book", database.Book{Title: "Big Cats 3", SeriesSequence: new(1)},
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
	book := &database.Book{Title: "Big Cats 1", Duration: new(36000)}
	good := metafetch.MetadataCandidate{Title: "Big Cats 1", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}

	if v := Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), &good, nil); !v.Allowed {
		t.Fatalf("matching 0.95 candidate refused: %+v", v)
	}

	// The owner's failure: a near-perfect score on the wrong volume.
	wrong := metafetch.MetadataCandidate{Title: "Big Cats 3", SeriesPosition: "3", Score: 0.99}
	if v := Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), &wrong, nil); v.Allowed || v.Reason != ReasonSequenceMismatch {
		t.Fatalf("0.99 wrong-volume candidate: allowed=%v reason=%q, want blocked %q", v.Allowed, v.Reason, ReasonSequenceMismatch)
	}

	low := good
	low.Score = 0.89
	if v := Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), &low, nil); v.Allowed || v.Reason != ReasonScoreBelowFloor {
		t.Fatalf("0.89 candidate: allowed=%v reason=%q, want %q", v.Allowed, v.Reason, ReasonScoreBelowFloor)
	}
	atFloor := good
	atFloor.Score = MinScore
	if v := Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), &atFloor, nil); !v.Allowed {
		t.Fatalf("candidate exactly at the floor refused: %+v", v)
	}

	if v := Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), &good, errors.New("hash drift")); v.Allowed || v.Reason != ReasonIdentityStale {
		t.Fatalf("stale identity: allowed=%v reason=%q", v.Allowed, v.Reason)
	}

	// Audio confirmation relaxes the floor to 0.85 ...
	heard := &database.Book{Title: "Big Cats 1", TranscribedTitle: new("Big Cats 1")}
	// (the confirmation needs the candidate's runtime to fit the book's files:
	// 10 h of measured files here, the candidate's 10 h in `good`)
	relaxed := good
	relaxed.Score = 0.86
	if v := Evaluate(heard, snap(heard), completeFilesRuntime(36000), &relaxed, nil); !v.Allowed || !v.AudioConfirmed {
		t.Fatalf("audio-confirmed 0.86 refused: %+v", v)
	}
	// ... and a transcribed title the candidate does not match refuses outright.
	other := metafetch.MetadataCandidate{Title: "Small Dogs 1", SeriesPosition: "1", Score: 0.99}
	if v := Evaluate(heard, snap(heard), database.ComputeBookRuntime(heard, nil), &other, nil); v.Allowed || v.Reason != ReasonTranscriptionMismatch {
		t.Fatalf("transcription mismatch: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
}

// TestTranscriptionConfirms_RealReviewCases: seven books the owner clicked
// Apply on in the review lane on 2026-09-13, verbatim. All seven were refused
// as transcription_mismatch by origin/main's rule (the fail-before test is
// util.TestTranscriptMatch_OldRuleRefusedAllSeven).
//
// unreviewed is TranscriptionConfirms (the gate, metadata.upgrade, an
// unpinned batch apply): it ANDs util.MainTranscriptionConfirms (origin/main's
// rule plus the owner's 2026-09-14 initials fold), so it refuses six of the
// seven as main did; only Sojourn, refused on initials spacing alone, now
// confirms. The six are applied by an owner clicking Apply on their rows.
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
		// Owner decision 2026-09-14: initials spacing and punctuation are
		// equal, so "R.A." ~ "R. A." and the unreviewed rule confirms (it
		// was false). The surname still passes only the matcher's one typo.
		{"Sojourn", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Salvator", true, true},
		{"different initials", "Sojourn", "R. A. Salvatore", "Sojourn", "J.R. Salvator", false, false},
		{"initials folded, different surname", "Sojourn", "R. A. Salvatore", "Sojourn", "R.A. Smith", false, false},
		// A real short first name is not initials (2026-09-14 review): the
		// full unreviewed gate must refuse each of these.
		{"Ra is a name, not R.A.", "Sojourn", "R. A. Salvatore", "Sojourn", "Ra Salvatore", false, true},
		{"R.A. is not the name Ra", "Sojourn", "Ra Salvatore", "Sojourn", "R.A. Salvatore", false, true},
		{"Ed is a name, not E. D.", "Sojourn", "E. D. McBain", "Sojourn", "Ed McBain", false, true},
		{"Jo is a name, not J. O.", "Sojourn", "J. O. Nesbo", "Sojourn", "Jo Nesbo", false, true},
		{"K. I. M. is not the name Kim", "Sojourn", "Kim Stanley", "Sojourn", "K. I. M. Stanley", false, true},
		{"Al is a name, not A. L.", "Sojourn", "A. L. Franken", "Sojourn", "Al Franken", false, false},
		// Heard "book one"; the review row carried no series position.
		{"This Gilded Abyss", "This Gilded Abyss", "Rebecca Thorne", "This Gilded Abyss, book one of the Gilded Abyss trilogy", "Rebecca Thorne", false, false},
		{"Knightfall", "Knightfall", "Bruce Wayne", "Knightfall", "Bruce Wayne For Martha Wayne, who's", false, true},
		{"same author, different title", "The Who Rules the Night", "Bruce Wayne", "Knightfall", "Bruce Wayne For Martha Wayne, who's", false, false},
		{"different volume of the same series", "Big Cats 3", "Ann Author", "Big Cats 1", "Ann Author", false, false},
		{"completely different author", "Blood of Elves", "Morgan Rice", "Blood of Elves", "Andrzej Sapkowski Translated from the Polish", false, false},
		// Both rules agree on a clean exact match.
		{"clean exact match", "Knightfall", "Bruce Wayne", "Knightfall", "Bruce Wayne", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{TranscribedTitle: new(tc.heardTitle), TranscribedAuthor: new(tc.heardAuthor)}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor, DurationSec: 36000}
			if got := TranscriptionConfirms(book, c); got != tc.unreviewed {
				t.Errorf("TranscriptionConfirms = %v, want %v", got, tc.unreviewed)
			}
			if got := ReviewedTranscriptionAgrees(book, c); got != tc.reviewed {
				t.Errorf("ReviewedTranscriptionAgrees = %v, want %v", got, tc.reviewed)
			}
			// The floor follows the UNREVIEWED rule only: a pair the matcher
			// alone agrees on must keep MinScore, never earn 0.85.
			c.Score = 0.86
			// Measured files that fit the candidate's runtime: the confirmation
			// is the matcher's alone to give or refuse in this table.
			v := Evaluate(book, snap(book), completeFilesRuntime(36000), c, nil)
			if v.TranscriptionAgreesOnReview != tc.reviewed {
				t.Errorf("verdict annotation = %v, want %v", v.TranscriptionAgreesOnReview, tc.reviewed)
			}
			if tc.unreviewed {
				if !v.AudioConfirmed || v.ScoreFloor != MinScoreAudioConfirmed || v.ScoreReason != "" {
					t.Errorf("confirmed pair at 0.86: audio=%v floor=%v score_reason=%q, want the relaxed floor to pass it",
						v.AudioConfirmed, v.ScoreFloor, v.ScoreReason)
				}
			} else if v.AudioConfirmed || v.ScoreFloor != MinScore || v.ScoreReason != ReasonTranscriptionMismatch {
				t.Errorf("unconfirmed pair at 0.86: audio=%v floor=%v score_reason=%q, want floor %v and %q",
					v.AudioConfirmed, v.ScoreFloor, v.ScoreReason, MinScore, ReasonTranscriptionMismatch)
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
		"Harry Potter", "Harry Potter book 2", "The Expanse", "The Expanse, volume 3", "Knightfall",
	},
	authors: []string{
		"Andrzej Sapkowski", "Andrzej Sapkowski Translated from the Polish", "George R. R. Martin",
		"George R. R. Martin, assisted", "R. A. Salvatore", "R.A. Salvator", "Robert Salvatore",
		"Bruce Wayne", "Bruce Wayne For Martha Wayne, who's", "Frank Herbert", "",
	},
}

// TestTranscriptionConfirms_Basics: the plain shapes of the unreviewed rule --
// title and author both heard, title alone when the heard author is absent or
// too short to use, a last-name-only heard author, and refusal on a mismatch
// or when nothing was transcribed.
func TestTranscriptionConfirms_Basics(t *testing.T) {
	cases := []struct {
		name                    string
		heardTitle, heardAuthor *string
		candTitle, candAuthor   string
		want                    bool
	}{
		{"title and author match", new("Silver Harbor Lights"), new("Ann Author"), "Silver Harbor Lights", "Ann Author", true},
		{"title match, no heard author", new("Big Cats"), nil, "Big Cats", "Bob Writer", true},
		{"title match, heard author too short", new("Big Cats"), new("F"), "Big Cats", "Bob Writer", true},
		{"heard author is a surname substring", new("The Glass Road"), new("Penwright"), "The Glass Road", "Carl Penwright", true},
		{"title mismatch", new("Small Boats"), new("Ann Author"), "Silver Harbor Lights", "Ann Author", false},
		{"author mismatch", new("Silver Harbor Lights"), new("Dana Other"), "Silver Harbor Lights", "Ann Author", false},
		{"nothing transcribed", nil, nil, "Big Cats", "Bob Writer", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			book := &database.Book{TranscribedTitle: tc.heardTitle, TranscribedAuthor: tc.heardAuthor}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor, Score: 0.87}
			if got := TranscriptionConfirms(book, c); got != tc.want {
				t.Errorf("TranscriptionConfirms = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTranscriptionConfirms_InitialsFold: initials spacing and punctuation are
// equal (owner decision 2026-09-14), so "R.A." and "RA" confirm against
// "R. A."; different initials, a different surname, a misspelled surname that
// is not a prefix of the real one, and a real short first name all refuse.
func TestTranscriptionConfirms_InitialsFold(t *testing.T) {
	cases := []struct {
		heardAuthor string
		want        bool
	}{
		{"R.A. Harrowgat", true},
		{"RA Harrowgate", true},
		{"J.R. Harrowgat", false},
		{"R.A. Brook", false},
		// A misspelled surname that is not a prefix of the real one.
		{"R.A. Harrowdale", false},
		// A real short first name is not initials.
		{"Ra Harrowgate", false},
	}
	for _, tc := range cases {
		book := &database.Book{TranscribedTitle: new("Big Cats"), TranscribedAuthor: new(tc.heardAuthor)}
		c := &metafetch.MetadataCandidate{Title: "Big Cats", Author: "R. A. Harrowgate", SeriesPosition: "2"}
		if got := TranscriptionConfirms(book, c); got != tc.want {
			t.Errorf("TranscriptionConfirms on heard %q = %v, want %v", tc.heardAuthor, got, tc.want)
		}
	}
}

// TestTranscriptionConfirms_RefusesEveryPairMainRefused: the unreviewed rule
// refuses every pair util.MainTranscriptionConfirms refuses -- the real review
// shapes (a credit after the author, a spoken series trailer, a silent
// letter, a heard volume) and the short-first-name-versus-initials pairs. The
// fixture guard fails any pair main itself would confirm, since it would test
// nothing.
func TestTranscriptionConfirms_RefusesEveryPairMainRefused(t *testing.T) {
	pairs := []struct{ candTitle, candAuthor, pos, heardTitle, heardAuthor string }{
		{"River of Glass", "Anton Kowalczyk", "", "River of Glass", "Anton Kowalczyk Translated from the Polish"},
		{"A Song of Tides (Book #4 in the Tidal Crown)", "Mara Quill", "4", "A Song of Tides", "Mara Quill"},
		{"Witness to a Storm", "Jon Marsh", "", "Witness to a Storm A short story prequel to The Lantern", "Jon Marsh"},
		{"Knights Over Pawns", "Gary R. R. Stone", "", "Nights Over Pawns", "Gary R. R. Stone, assisted"},
		{"This Copper Vale", "Rhea Thornfield", "1", "This Copper Vale, book one of the Copper Vale trilogy", "Rhea Thornfield"},
		{"Big Cats", "Ann Author", "1", "Big Cats", "Ann Author For Beth Author, who's"},
		{"Sand Wells", "Bob Writer", "2", "Sand Wells, book two of the Sand Wells Chronicles", "Bob Writer"},
		{"Sam Archer", "J. K. Rowden", "2", "Sam Archer book 2", "J. K. Rowden"},
		{"The Reach", "James S. A. Coren", "3", "The Reach, volume 3", "James S. A. Coren"},
		{"Ready Pilot One", "Ernest Kline", "", "Ready Pilot 1", "Ernest Kline"},
		// A real short first name folds nothing: these are different
		// people from the initialled author (2026-09-14 review).
		{"Big Cats", "R. A. Harrowgate", "2", "Big Cats", "Ra Harrowgate"},
		{"Big Cats", "Ra Harrowgate", "2", "Big Cats", "R.A. Harrowgate"},
		{"Big Cats", "E. D. Mcrae", "2", "Big Cats", "Ed Mcrae"},
		{"Big Cats", "J. O. Nelmo", "2", "Big Cats", "Jo Nelmo"},
		{"Big Cats", "Kim Stanford", "2", "Big Cats", "K. I. M. Stanford"},
		{"Big Cats", "A. L. Frankel", "2", "Big Cats", "Al Frankel"},
		{"Big Cats", "R. A. Harrowgate", "2", "Big Cats", "R.A. Harrowdale"},
	}
	for _, p := range pairs {
		if util.MainTranscriptionConfirms(p.candTitle, p.candAuthor, p.heardTitle, p.heardAuthor) {
			t.Fatalf("fixture %q ~ %q: origin/main confirms it, so it tests nothing", p.candTitle, p.heardTitle)
		}
		book := &database.Book{TranscribedTitle: new(p.heardTitle), TranscribedAuthor: new(p.heardAuthor)}
		c := &metafetch.MetadataCandidate{Title: p.candTitle, Author: p.candAuthor, SeriesPosition: p.pos}
		if TranscriptionConfirms(book, c) {
			t.Errorf("TranscriptionConfirms confirmed %q ~ heard %q/%q, which origin/main refused", p.candTitle, p.heardTitle, p.heardAuthor)
		}
	}
}

// TestUnreviewedPathsRefuseEveryPairMainRefused: over the whole corpus, with
// and without a series position, every pair origin/main's rule refused is
// refused by TranscriptionConfirms and blocked by Evaluate/EvaluateInBatch
// (the gate metadata.upgrade and an unpinned batch apply run) as
// transcription_mismatch, and never earns the relaxed MinScoreAudioConfirmed
// floor. A score of 0.99 keeps the score leg from being the reason.
//
// About 175k gate evaluations: 672 s on one core under -race, the long pole of
// the whole CI test run. Each (candidate title, heard title) pair is its own
// parallel subtest; the gate functions are pure, and the refused tally is an
// atomic read after the group, once every subtest has finished.
func TestUnreviewedPathsRefuseEveryPairMainRefused(t *testing.T) {
	var refused, completed atomic.Int64
	t.Run("pairs", func(t *testing.T) {
		for ci, ct := range mainRefusedCorpus.titles {
			for hi, ht := range mainRefusedCorpus.titles {
				t.Run(fmt.Sprintf("c%02d_h%02d", ci, hi), func(t *testing.T) {
					t.Parallel()
					refused.Add(int64(refusedPairsFor(t, ct, ht)))
					completed.Add(1)
				})
			}
		}
	})
	// The floor is about the whole corpus: skip it when -run selected only
	// some pairs, or when a pair already failed (its own error says why).
	want := int64(len(mainRefusedCorpus.titles) * len(mainRefusedCorpus.titles))
	if completed.Load() != want {
		return
	}
	if n := refused.Load(); n < 1000 {
		t.Fatalf("corpus produced only %d refused pairs; it no longer exercises the rule", n)
	}
}

// refusedPairsFor checks every author pair and series position for one
// (candidate title, heard title) pair and returns how many origin/main refused.
func refusedPairsFor(t *testing.T, ct, ht string) int {
	t.Helper()
	refused := 0
	for _, ca := range mainRefusedCorpus.authors {
		for _, ha := range mainRefusedCorpus.authors {
			if util.MainTranscriptionConfirms(ct, ca, ht, ha) {
				continue
			}
			for _, pos := range []string{"", "1", "2", "3"} {
				refused++
				book := &database.Book{TranscribedTitle: new(ht), TranscribedAuthor: new(ha)}
				c := &metafetch.MetadataCandidate{Title: ct, Author: ca, SeriesPosition: pos, Score: 0.99}
				if TranscriptionConfirms(book, c) {
					t.Fatalf("TranscriptionConfirms accepted %q/%q (pos %q) ~ heard %q/%q, which origin/main refused", ct, ca, pos, ht, ha)
				}
				for _, v := range []Verdict{Evaluate(book, snap(book), database.ComputeBookRuntime(book, nil), c, nil), EvaluateInBatch(book, snap(book), database.ComputeBookRuntime(book, nil), c, nil, nil, nil)} {
					if v.Allowed || v.AudioConfirmed || v.ScoreFloor != MinScore || v.ScoreReason != ReasonTranscriptionMismatch {
						t.Fatalf("gate on %q/%q (pos %q) ~ heard %q/%q: allowed=%v audio=%v floor=%v score_reason=%q; origin/main refused it",
							ct, ca, pos, ht, ha, v.Allowed, v.AudioConfirmed, v.ScoreFloor, v.ScoreReason)
					}
				}
			}
		}
	}
	return refused
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
		{"subtitle vs series title", "Knightfall: The Knightsend", "Bruce Wayne", "Knightfall", "Bruce Wayne", ""},
		{"series title vs subtitle", "Knightfall", "Bruce Wayne", "Knightfall: The Knightsend", "Bruce Wayne", ""},
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
			book := &database.Book{TranscribedTitle: new(tc.heardTitle), TranscribedAuthor: new(tc.heardAuthor)}
			if tc.intro != "" {
				book.IntroTranscription = new(tc.intro)
			}
			c := &metafetch.MetadataCandidate{Title: tc.candTitle, Author: tc.candAuthor}
			if TranscriptionConfirms(book, c) {
				t.Errorf("TranscriptionConfirms confirmed %q/%q against heard %q/%q", tc.candTitle, tc.candAuthor, tc.heardTitle, tc.heardAuthor)
			}
		})
	}
}

// Owner decision 2026-10-29: a transcription match whose runtime does not
// confirm it (a fragment, a mismatch, or no comparable runtime) is never
// grounds for an unattended apply, however high the score. The runtime check
// is evaluated LIVE: these candidates carry no TranscriptionRuntimeCheck stamp
// and a boosted score, like a cached row from before the check existed.
// completeFilesRuntime is a runtime built from one fully measured file: the
// complete total the transcription runtime gate compares a candidate with.
func completeFilesRuntime(sec int) database.BookRuntime {
	return database.BookRuntime{Seconds: sec, Source: database.RuntimeSourceFiles, FilesCounted: 1, FilesKnown: 1}
}

func TestScoreGate_TranscriptionRuntimeUnverifiedRefuses(t *testing.T) {
	book := &database.Book{TranscribedTitle: new("Year One"), TranscribedAuthor: new("Alfred Pennyworth")}
	mk := func(candSec int) *metafetch.MetadataCandidate {
		return &metafetch.MetadataCandidate{Title: "Year One", Author: "Alfred Pennyworth", Score: 3.0, DurationSec: candSec}
	}
	const hr = 3600
	complete := completeFilesRuntime
	partial := database.BookRuntime{Seconds: 7 * hr, Source: database.RuntimeSourceFiles, FilesCounted: 2, FilesKnown: 1}

	if ok, floor, audio, reason := ScoreGate(book, complete(10*hr), mk(10*hr+30*60)); !ok || reason != "" || !audio || floor != MinScoreAudioConfirmed {
		t.Fatalf("control: a candidate within 10%% must pass confirmed, got ok=%v reason=%q audio=%v floor=%v", ok, reason, audio, floor)
	}
	for name, tc := range map[string]struct {
		rt      database.BookRuntime
		candSec int
	}{
		"fragment (1h of a 10h book)": {complete(hr), 10 * hr},
		"runtime mismatch":            {complete(10 * hr), 12 * hr},
		"partial local runtime":       {partial, 10 * hr},
		"unknown local runtime":       {database.BookRuntime{}, 10 * hr},
		"candidate without a runtime": {complete(10 * hr), 0},
	} {
		ok, floor, audio, reason := ScoreGate(book, tc.rt, mk(tc.candSec))
		if ok || reason != ReasonTranscriptionRuntimeUnverified {
			t.Errorf("%s: ok=%v reason=%q, want a %q refusal", name, ok, reason, ReasonTranscriptionRuntimeUnverified)
		}
		if audio || floor != MinScore {
			t.Errorf("%s: audio=%v floor=%v; an unverified match must not count as audio confirmation", name, audio, floor)
		}
	}

	// A stale stamp does not decide: the live runtime agrees, so it passes.
	stamped := mk(10 * hr)
	stamped.TranscriptionRuntimeCheck = &metafetch.TranscriptionRuntimeCheck{Verdict: metafetch.TranscriptionRuntimeFragment}
	if ok, _, _, reason := ScoreGate(book, complete(10*hr), stamped); !ok {
		t.Errorf("stamp must not decide the gate; got reason %q", reason)
	}

	// The refusal reaches the full verdict on the bulk and upgrade path
	// (EvaluateInBatch), and an owner review may override it.
	v := EvaluateInBatch(book, Authors{}, complete(hr), mk(10*hr), nil, nil, nil)
	if v.Allowed || v.Reason != ReasonTranscriptionRuntimeUnverified {
		t.Fatalf("EvaluateInBatch: allowed=%v reason=%q", v.Allowed, v.Reason)
	}
	if !v.OwnerReviewOverridable() {
		t.Error("an owner review must be able to override the refusal")
	}
}
