// file: internal/plugins/maintenance/credits_backfill_cohorts_test.go
// version: 1.0.2
// guid: a1146b61-8486-4d68-8496-fefd5b154a64
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Tests for round 4 of the review of PR #4200 (credits backfill): cohorts and
// book_ids runs agree with the full dry run, a respelling lands on the credit
// the cleaner takes the person from, and the smaller spelling and apply-time
// fixes. Fixtures: Batman characters as people, Batman storylines as titles,
// paths under /lib/Gotham (cbFixture.book). Nothing here comes from a real
// library.

func cbRecordNames(t *testing.T, f *cbFixture) []string {
	t.Helper()
	ns, err := f.s.ListNarrators()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range ns {
		names = append(names, n.Name)
	}
	slices.Sort(names)
	return names
}

// --- SF1: a dry run predicts the cohorts, and a single full apply writes them -

// cbSplitFixture is two books of one cohort chain: s1 names Renee Montoya alone
// (an unsplit credit, which creates the record without any opt-in), s2 names
// her beside a narrator who has a record (a split credit, which creates one
// only with narrator_new_from_split). Planned against the records as they stood
// at the start of the run, s2 read as "needs a new record from a split" and was
// listed for review, but a cohort that comes after s1's cohort finds the record
// there and writes s2.
func cbSplitFixture(t *testing.T, s1ID, s2ID string) *cbFixture {
	t.Helper()
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Harvey Bullock")
	f.book(s1ID, "Gotham Central: In the Line of Duty", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba(s1ID, [2]int{bruce, 0}), nil)
	f.book(s2ID, "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya and Harvey Bullock")
	}, ba(s2ID, [2]int{bruce, 0}), nil)
	return f
}

// The dry run lists the split book as a write because the run itself creates
// the record for the unsplit book; the cohorts then write exactly what the dry
// run listed, and the end state is the same as one full apply's.
func TestCreditsBackfill_DryRunPredictsWhatTheCohortsWrite(t *testing.T) {
	f := cbSplitFixture(t, "s1", "s2")
	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Fatalf("dry run %s = %v, want [s1 s2]: the run creates Renee Montoya for s1, so s2 names no new person", cbNarratorFillFromColumn, got)
	}
	if got := cbClassIDs(dry, cbNarratorNewFromSplit); len(got) != 0 {
		t.Errorf("dry run %s = %v, want none: s2's new name is created by the run for s1", cbNarratorNewFromSplit, got)
	}
	if dry.BooksEligible != 2 {
		t.Errorf("books_eligible = %d, want 2", dry.BooksEligible)
	}

	c1, err := f.runWith(context.Background(), &cbReporter{id: "01COHORTA"}, creditsBackfillParams{Apply: true, Limit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(c1, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s1"}) {
		t.Fatalf("cohort 1 wrote %v, want [s1]", got)
	}
	c2, err := f.runWith(context.Background(), &cbReporter{id: "01COHORTB"}, creditsBackfillParams{Apply: true, Limit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(c2, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s2"}) {
		t.Fatalf("cohort 2 wrote %v, want [s2], the book the dry run listed as a write", got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want one Renee Montoya beside Harvey Bullock", got)
	}
}

// A single full apply writes the split book too, creating the record once.
func TestCreditsBackfill_SingleApplyWritesTheSplitBookWhoseNameTheRunCreates(t *testing.T) {
	f := cbSplitFixture(t, "s1", "s2")
	res, err := f.run(creditsBackfillParams{Apply: true}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Fatalf("apply %s = %v, want [s1 s2]", cbNarratorFillFromColumn, got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Fatalf("narrator records = %v, want Harvey Bullock and ONE Renee Montoya", got)
	}
	s1, s2 := f.state("s1").Narrators, f.state("s2").Narrators
	if len(s1) != 1 || len(s2) != 2 || s1[0].NarratorID != s2[0].NarratorID {
		t.Errorf("junctions s1=%v s2=%v: both books must credit the one Renee Montoya record first", s1, s2)
	}
	assertPlansClean(t, f, "s1", "s2")
}

// The split book sorts BEFORE the book that names the person alone, so a limit
// of one would put the split book in the first cohort. A split book is never the
// one to create a record without the owner's allow, so the unsplit book, which
// does create the record, takes the place; the split book is outside the limit
// (its creator is in the cohort, it has no room) and the second cohort links it
// to the record that exists by then. The two cohorts end where a full apply ends.
func TestCreditsBackfill_SplitBookIsNotTheOneToCreateTheRecordInTheFirstCohort(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	full, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(full, cbNarratorFillFromColumn); !slices.Equal(got, []string{"a-split", "z-solo"}) {
		t.Fatalf("full dry run %s = %v, want both books", cbNarratorFillFromColumn, got)
	}
	if got := cbClassIDs(full, cbNarratorWaitingOnCreator); len(got) != 0 {
		t.Errorf("full dry run %s = %v, want none: the creator is in the cohort", cbNarratorWaitingOnCreator, got)
	}
	dry, err := f.run(creditsBackfillParams{Limit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorFillFromColumn); !slices.Equal(got, []string{"z-solo"}) {
		t.Fatalf("limit-1 dry run %s = %v, want [z-solo], the book that creates the record", cbNarratorFillFromColumn, got)
	}
	if got := cbClassIDs(dry, cbLimitReached); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("limit-1 dry run %s = %v, want [a-split]: its creator is in the cohort and it has no room", cbLimitReached, got)
	}
	if got := cbClassIDs(dry, cbNarratorNewFromSplit); len(got) != 0 {
		t.Errorf("limit-1 dry run %s = %v, want none: the record is created, just not by this book", cbNarratorNewFromSplit, got)
	}
	for i, want := range [][]string{{"z-solo"}, {"a-split"}} {
		res, err := f.runWith(context.Background(), &cbReporter{id: "01COHORT" + string(rune('A'+i))}, creditsBackfillParams{Apply: true, Limit: 1}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, want) {
			t.Fatalf("cohort %d wrote %v, want %v", i+1, got, want)
		}
		if i == 0 {
			if st := f.state("a-split"); len(st.Narrators) != 0 {
				t.Fatalf("cohort 1 wrote the split book: %v", st.Narrators)
			}
		}
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want Harvey Bullock and ONE Renee Montoya", got)
	}
	assertPlansClean(t, f, "z-solo", "a-split")
}

// A split book that also names someone no other book creates stays under
// review: only the names the run itself creates count as existing.
func TestCreditsBackfill_SplitBookNamingAnUncreatedPersonStaysUnderReview(t *testing.T) {
	f := cbSplitFixture(t, "s1", "s2")
	dick := f.author("Dick Grayson")
	f.book("s3", "Gotham Central: Soft Targets", func(b *database.Book) {
		b.AuthorID = intp(dick)
		b.Narrator = strp("Renee Montoya and Selina Kyle")
	}, ba("s3", [2]int{dick, 0}), nil)
	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorNewFromSplit); !slices.Equal(got, []string{"s3"}) {
		t.Errorf("dry run %s = %v, want [s3]: Selina Kyle is created by no other book", cbNarratorNewFromSplit, got)
	}
	if got := cbClassIDs(dry, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("dry run %s = %v, want [s1 s2]", cbNarratorFillFromColumn, got)
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"s3"}) {
		t.Errorf("apply %s = %v, want [s3] (review, not written)", cbNarratorNewFromSplit, got)
	}
	if st := f.state("s3"); len(st.Narrators) != 0 {
		t.Errorf("s3 was written: %v", st.Narrators)
	}
}

// A split book whose fold-key twin ("Renée") is what the unsplit book names is
// covered too, and the spelling the record takes is settled over both books.
func TestCreditsBackfill_SplitBookNameCoveredByAFoldKeyTwinIsRespelledToTheRecord(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Harvey Bullock")
	for id, col := range map[string]string{
		"t1": "Renée Montoya", "t2": "Renée Montoya", "t3": "Renee Montoya and Harvey Bullock",
	} {
		f.book(id, "Gotham Central "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp(col)
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"t1", "t2", "t3"}) {
		t.Fatalf("apply %s = %v, want [t1 t2 t3]", cbNarratorFillFromColumn, got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renée Montoya"}) {
		t.Fatalf("narrator records = %v", got)
	}
	if got := cbStr(f.state("t3").Narrator, ""); got != "Renée Montoya and Harvey Bullock" {
		t.Errorf("t3 column = %q, want the record's spelling", got)
	}
	assertPlansClean(t, f, "t1", "t2", "t3")
}

// --- SF2: a book_ids run counts the whole library and writes its cohort ------

// A book_ids cohort of one book used to count spellings over that book alone,
// so it created the record under the spelling the full dry run rejected, and the
// later full apply respelled the majority to it. Now it plans the whole library:
// the record takes the spelling the full dry run shows, and only the cohort is
// reported and written.
func TestCreditsBackfill_BookIDsRunCountsSpellingsOverTheWholeLibrary(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for id, n := range map[string]string{"g-1": "Renee Montoya", "g-2": "Renée Montoya", "g-3": "Renée Montoya"} {
		f.book(id, "No Man's Land "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp(n)
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	full, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(full, cbNarratorColumnRespelled); !slices.Equal(got, []string{"g-1"}) {
		t.Fatalf("full dry run %s = %v, want [g-1]", cbNarratorColumnRespelled, got)
	}
	cohort, err := f.run(creditsBackfillParams{BookIDs: []string{"g-1"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(cohort, cbNarratorColumnRespelled); !slices.Equal(got, []string{"g-1"}) {
		t.Errorf("book_ids dry run %s = %v, want [g-1], as in the full dry run", cbNarratorColumnRespelled, got)
	}
	// Only the cohort is reported on.
	if cohort.BooksListed != 1 || cohort.BooksScanned != 1 || cohort.BooksEligible != 1 || cohort.BooksToWrite != 1 {
		t.Errorf("book_ids run listed=%d scanned=%d eligible=%d to_write=%d, want 1 each", cohort.BooksListed, cohort.BooksScanned, cohort.BooksEligible, cohort.BooksToWrite)
	}
	for _, c := range cohort.Classes {
		for _, id := range c.BookIDs {
			if id != "g-1" {
				t.Errorf("book_ids run lists %s under %s: only the cohort is reported", id, c.Key)
			}
		}
	}

	if _, err := f.runWith(context.Background(), &cbReporter{id: "01COHORTA"}, creditsBackfillParams{Apply: true, BookIDs: []string{"g-1"}}, 2); err != nil {
		t.Fatal(err)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Renée Montoya"}) {
		t.Fatalf("after the cohort: records %v, want the full run's spelling [Renée Montoya]", got)
	}
	for _, id := range []string{"g-2", "g-3"} {
		if got := cbStr(f.state(id).Narrator, ""); got != "Renée Montoya" || len(f.state(id).Narrators) != 0 {
			t.Errorf("%s outside the cohort was touched: column %q junction %v", id, got, f.state(id).Narrators)
		}
	}
	if _, err := f.runWith(context.Background(), &cbReporter{id: "01COHORTB"}, creditsBackfillParams{Apply: true}, 2); err != nil {
		t.Fatal(err)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Renée Montoya"}) {
		t.Errorf("after the full apply: records %v, want [Renée Montoya]", got)
	}
	for _, id := range []string{"g-1", "g-2", "g-3"} {
		if got := cbStr(f.state(id).Narrator, ""); got != "Renée Montoya" {
			t.Errorf("%s column = %q, want Renée Montoya: the cohort and the full run agree", id, got)
		}
	}
}

// A book_ids run counts the limit over its own books, and an id that is not a
// book is still counted as gone.
func TestCreditsBackfill_BookIDsRunLimitsAndCountsGoneOverItsOwnBooks(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for _, id := range []string{"b-1", "b-2", "b-3", "b-4"} {
		f.book(id, "Knightfall "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp("Renee Montoya")
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	res, err := f.run(creditsBackfillParams{Apply: true, BookIDs: []string{"b-2", "b-3", "b-4", "b-nope"}, Limit: 2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"b-2", "b-3"}) {
		t.Errorf("wrote %v, want [b-2 b-3]: the limit counts over the book_ids, b-1 is not one", got)
	}
	if got := cbClassIDs(res, cbLimitReached); !slices.Equal(got, []string{"b-4"}) {
		t.Errorf("%s = %v, want [b-4]", cbLimitReached, got)
	}
	if res.BooksGone != 1 || res.BooksListed != 4 {
		t.Errorf("gone=%d listed=%d, want 1 and 4", res.BooksGone, res.BooksListed)
	}
	if len(f.state("b-1").Narrators) != 0 {
		t.Errorf("b-1 is outside book_ids and was written")
	}
}

// --- SF3: a respelling lands on the credit the cleaner takes the person from -

// respellNarratorsJSON respells the entry whose person is the name, not another
// person's entry that holds the name as words, wherever either stands in the
// array. The book used to be refused as narrator_spelling_json_conflict.
func TestCreditsBackfill_RespellNarratorsJSONOnlyRespellsTheEntryNamingThePerson(t *testing.T) {
	pair := [][2]string{{"Jean Paul Valley", "Jean-Paul Valley"}}
	for _, tc := range []struct{ name, raw, want string }{
		{"junior first", `["Jean Paul Valley Jr.", "Jean Paul Valley"]`, `["Jean Paul Valley Jr.","Jean-Paul Valley"]`},
		{"junior last", `["Jean Paul Valley", "Jean Paul Valley Jr."]`, `["Jean-Paul Valley","Jean Paul Valley Jr."]`},
		{"read-by entry", `["Jean Paul Valley Jr.", "Read by Jean Paul Valley"]`, `["Jean Paul Valley Jr.","Read by Jean-Paul Valley"]`},
		{"no entry names the person: the first whole-word entry stands in", `["Jean Paul Valley (abridged)", "Dick Grayson"]`, `["Jean-Paul Valley (abridged)","Dick Grayson"]`},
		{"untouched byte for byte", `["Dick Grayson"]`, `["Dick Grayson"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := respellNarratorsJSON(tc.raw, pair); got != tc.want {
				t.Errorf("respellNarratorsJSON(%s) = %s, want %s", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCreditsBackfill_JSONEntryOfAnotherPersonDoesNotRefuseTheBook(t *testing.T) {
	const raw = `["Jean Paul Valley Jr.", "Jean Paul Valley"]`
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Jean-Paul Valley")
	f.narrator("Jean Paul Valley Jr.")
	f.book("c1", "Knightfall", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Jean Paul Valley Jr. and Jean Paul Valley")
		b.NarratorsJSON = strp(raw)
	}, ba("c1", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowNewFromSplit}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cbHasID(res, cbNarratorSpellingJSONConflict, "c1") || cbHasID(res, cbNarratorSpellingUnreconcilable, "c1") {
		t.Fatalf("c1 was refused: json_conflict=%v unreconcilable=%v", cbClassIDs(res, cbNarratorSpellingJSONConflict), cbClassIDs(res, cbNarratorSpellingUnreconcilable))
	}
	st := f.state("c1")
	if got := cbStr(st.Narrator, ""); got != "Jean Paul Valley Jr. and Jean-Paul Valley" {
		t.Errorf("column = %q", got)
	}
	if got := cbStr(st.NarratorsJSON, ""); got != `["Jean Paul Valley Jr.","Jean-Paul Valley"]` {
		t.Errorf("NarratorsJSON = %q", got)
	}
	assertPlansClean(t, f, "c1")
}

// A role credit naming the same person before the narrator piece is no narrator
// and is left alone; the narrator piece is the one respelled. The first piece
// that merely held the name used to be taken, and the read-back refused the book.
func TestCreditsBackfill_RoleCreditNamingTheSamePersonIsSkippedForTheNarratorPiece(t *testing.T) {
	pair := [][2]string{{"Jean Paul Valley", "Jean-Paul Valley"}}
	const text = "Jean Paul Valley - translator; Read by Jean Paul Valley"
	if got, want := respellCreditText(text, pair), "Jean Paul Valley - translator; Read by Jean-Paul Valley"; got != want {
		t.Fatalf("respellCreditText(%q) = %q, want %q", text, got, want)
	}

	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Jean-Paul Valley")
	f.book("c1b", "Knightquest", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp(text)
	}, ba("c1b", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cbHasID(res, cbNarratorSpellingUnreconcilable, "c1b") {
		t.Fatalf("c1b refused as %s", cbNarratorSpellingUnreconcilable)
	}
	if got := cbStr(f.state("c1b").Narrator, ""); got != "Jean Paul Valley - translator; Read by Jean-Paul Valley" {
		t.Errorf("column = %q", got)
	}
	assertPlansClean(t, f, "c1b")
}

// A column naming the same person twice ("X, Read by X") is one person to the
// cleaner, so both pieces are respelled, or the read-back would see two.
func TestCreditsBackfill_SamePersonTwiceInTheColumnIsRespelledInEveryPiece(t *testing.T) {
	pair := [][2]string{{"Renee Montoya", "Renée Montoya"}}
	if got, want := respellCreditText("Renee Montoya, Read by Renee Montoya", pair), "Renée Montoya, Read by Renée Montoya"; got != want {
		t.Fatalf("respellCreditText = %q, want %q", got, want)
	}

	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.book("c7", "Gotham Central: Soft Targets", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya, Read by Renee Montoya")
	}, ba("c7", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cbHasID(res, cbNarratorSpellingUnreconcilable, "c7") || !cbHasID(res, cbNarratorFillFromColumn, "c7") {
		t.Fatalf("c7: unreconcilable=%v fill=%v, want it written", cbClassIDs(res, cbNarratorSpellingUnreconcilable), cbClassIDs(res, cbNarratorFillFromColumn))
	}
	st := f.state("c7")
	if got := cbStr(st.Narrator, ""); got != "Renée Montoya, Read by Renée Montoya" || len(st.Narrators) != 1 {
		t.Errorf("column %q junction %v", got, st.Narrators)
	}
	assertPlansClean(t, f, "c7")
}

func TestCreditsBackfill_CreditNameOffsetPrefersThePieceTheCleanerTakesThePersonFrom(t *testing.T) {
	for _, tc := range []struct {
		text, name string
		want       int
	}{
		{"Read by Renee Montoyá", "Renee Montoya", -1},                    // a combining mark continues the word
		{"Renee Montoya, Renee Montoya", "Renee Montoya", 0},              // de-duplicated by the splitter
		{"J. R. R. Wayne & Read by J. R. R. Wayne", "J. R. R. Wayne", 0},  // initials
		{"Thomas Wayne Jr. and Read by Thomas Wayne", "Thomas Wayne", 29}, // the longer name's piece does not take it
		{"Narrated by Harvey Dent / Two-Face", "Harvey Dent", 12},         // slash split and prefix
		{"Harvey Dent with Harvey Dent - editor", "Harvey Dent", 0},       // a role piece is skipped
		{"Read by Harvey  Dent", "Harvey Dent", 8},                        // the cleaner reads a double space as one space
		{"Harvey Dent - editor; Read by Harvey Dent", "Harvey Dent", 30},  // the role piece first
		{"Thomas Wayne Jr. and Thomas Wayne", "Thomas Wayne", 21},         // exact piece, not the first holding the name
		{"Harvey Dent (abridged)", "Harvey Dent", 0},                      // no piece IS the name: whole words stand in
		{"Harvey Dent - editor", "Harvey Dent", -1},                       // the only piece is a role credit
	} {
		if got := creditNameOffset(tc.text, tc.name); got != tc.want {
			t.Errorf("creditNameOffset(%q, %q) = %d, want %d", tc.text, tc.name, got, tc.want)
		}
	}
}

// --- NIT1: the spelling a record takes, and when a column counts as respelled -

func TestCreditsBackfill_BetterSpellingPrefersMixedCaseThenAccentThenByteOrder(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"Renee Montoya", "RENEE MONTOYA"},
		{"Renee Montoya", "renee montoya"},
		{"Renée Montoya", "Renee Montoya"},
		{"Renée Montoya", "RENÉE MONTOYA"}, // mixed case beats the accent
		{"Renee Montoya", "renee Montoya"}, // byte order
		{"Renee Montoya", ""},
	} {
		if !betterSpelling(tc.a, tc.b) {
			t.Errorf("betterSpelling(%q, %q) = false, want true", tc.a, tc.b)
		}
		if tc.b != "" && betterSpelling(tc.b, tc.a) {
			t.Errorf("betterSpelling(%q, %q) = true, want false", tc.b, tc.a)
		}
	}
}

// Two books naming one new person in different case tie one to one. The record
// takes the mixed-case spelling, not the all-capitals one that sorts first, and
// the column that only differed in case is not listed as respelled: its text
// does not change.
func TestCreditsBackfill_CaseOnlyTieTakesMixedCaseAndListsNoRespelledColumn(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for id, n := range map[string]string{"a": "Renee Montoya", "b": "RENEE MONTOYA"} {
		f.book(id, "No Man's Land "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp(n)
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	for _, apply := range []bool{false, true} {
		res, err := f.run(creditsBackfillParams{Apply: apply}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := cbClassIDs(res, cbNarratorColumnRespelled); len(got) != 0 {
			t.Errorf("apply=%v: %s = %v, want none: no column text changed", apply, cbNarratorColumnRespelled, got)
		}
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Renee Montoya"}) {
		t.Errorf("records = %v, want [Renee Montoya]", got)
	}
}

// applySpelling files narrator_column_respelled only when the column text
// changes, not for a rename that normalizes to the same name.
func TestCreditsBackfill_ApplySpellingListsTheColumnOnlyWhenItsTextChanged(t *testing.T) {
	pl := &cbPlan{
		columnText:  "RENEE MONTOYA",
		requested:   []string{"RENEE MONTOYA"},
		authorNames: nil,
	}
	if refuse := pl.applySpelling([]cbNarratorSlot{{Name: "Renee Montoya"}}); refuse != "" {
		t.Fatalf("refused: %s", refuse)
	}
	if slices.Contains(pl.infos, cbNarratorColumnRespelled) {
		t.Errorf("infos %v list the column as respelled, but its text is unchanged", pl.infos)
	}
	if pl.column == nil || *pl.column != "RENEE MONTOYA" {
		t.Errorf("column = %v, want it unchanged", pl.column)
	}

	pl = &cbPlan{columnText: "Read by Renee Montoya", requested: []string{"Renee Montoya"}}
	if refuse := pl.applySpelling([]cbNarratorSlot{{Name: "Renée Montoya", ID: 7}}); refuse != "" {
		t.Fatalf("refused: %s", refuse)
	}
	if !slices.Contains(pl.infos, cbNarratorColumnRespelled) {
		t.Errorf("infos %v do not list a column whose text changed", pl.infos)
	}
}

// --- NIT4: an apply-time spelling refusal keeps the author write ------------

// cbSpellingStore creates narrator records under a spelling no column can be
// respelled to ("& Co" is a credit separator), as when a record of another
// spelling appears between the plan and the write.
type cbSpellingStore struct{ creditsBackfillStore }

func (s cbSpellingStore) CreateNarrator(name string) (*database.Narrator, error) {
	n, err := s.creditsBackfillStore.CreateNarrator(name)
	if err != nil || n == nil {
		return n, err
	}
	out := *n
	out.Name = n.Name + " & Co"
	return &out, nil
}

func TestCreditsBackfill_ApplyTimeSpellingRefusalKeepsTheAuthorWrite(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	lucius := f.author("Lucius Fox")
	f.book("ap", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(lucius)
		b.Narrator = strp("Renee Montoya")
	}, ba("ap", [2]int{bruce, 0}), nil)
	before := f.state("ap")
	env := creditsBackfillEnv{store: cbSpellingStore{f.s}, mod: f.s, opID: cbTestOpID, workers: 1}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err != nil {
		t.Fatalf("run: %v (a refused narrator side is not a failed book)", err)
	}
	if !res.Complete || res.Counts[cbWriteFailed] != 0 {
		t.Errorf("complete=%v write_failed=%v", res.Complete, cbClassIDs(res, cbWriteFailed))
	}
	if !cbHasID(res, cbNarratorSpellingUnreconcilable, "ap") {
		t.Errorf("ap not filed under %s: %v", cbNarratorSpellingUnreconcilable, cbClassIDs(res, cbNarratorSpellingUnreconcilable))
	}
	st := f.state("ap")
	if !slices.Equal(cbAuthorIDs(st.Authors), []int{lucius, bruce}) {
		t.Errorf("authors %v, want the author side written: AuthorID %d first", cbAuthorIDs(st.Authors), lucius)
	}
	if len(st.Narrators) != 0 || cbStr(st.Narrator, "") != "Renee Montoya" {
		t.Errorf("narrator side was written: column %q junction %v", cbStr(st.Narrator, ""), st.Narrators)
	}

	rows := cbJournalRowsOf(t, f, "ap")
	if len(rows) != 1 {
		t.Fatalf("journal rows: %d", len(rows))
	}
	var w undo.CreditsBackfillWrite
	if err := json.Unmarshal([]byte(rows[0].NewValue), &w); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Classes {
		if narratorSideClasses[c] {
			t.Errorf("journal row carries narrator class %s", c)
		}
	}
	// The record the refused fill created exists and no book credits it. The
	// book's journal row names it (it is informational: a revert never deletes a
	// record) so the record is not unaccounted for, and the result reports it as
	// unused for purge-empty-narrators.
	if len(w.CreatedNarrators) != 1 || !strings.HasPrefix(w.CreatedNarrators[0].Name, "Renee Montoya") {
		t.Errorf("journal row names created narrators %v, want the one record the refused fill created", w.CreatedNarrators)
	}
	if res.NarratorsCreated != 0 {
		t.Errorf("narrators_created = %d, want 0: no written book credits the record", res.NarratorsCreated)
	}
	if res.NarratorsCreatedUnused != 1 || len(res.UnusedNarratorNames) != 1 || !strings.HasPrefix(res.UnusedNarratorNames[0], "Renee Montoya") {
		t.Errorf("narrators_created_unused = %d %v, want the one record no book credits", res.NarratorsCreatedUnused, res.UnusedNarratorNames)
	}
	cbRevert(t, f)
	cbUntouched(t, f, "ap", before)
}

// --- the author-only book kept after a settle refusal -----------------------

// A book kept only for its author side after a settle refusal journals no
// narrator class or change, and the revert restores it byte for byte, the raw
// credit keys included.
func TestCreditsBackfill_AuthorOnlyBookAfterSettleRefusalJournalsNoNarratorSideAndRevertsExactly(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	lucius := f.author("Lucius Fox")
	for _, id := range []string{"r1-a", "r1-b"} {
		f.book(id, "Gotham Central "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp("Renee Montoya")
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	f.book("r1-c", "Gotham Central: Daylight", func(b *database.Book) {
		b.AuthorID = intp(lucius)
		b.NarratorsJSON = strp(`["Read by Renée Montoya"]`)
	}, nil, nil)
	before := f.state("r1-c")
	rawKeys := []string{"book_authors:r1-c", "book_narrators:r1-c"}
	raw := func(k string) string {
		v, err := f.s.GetRaw(k)
		if err != nil || v == nil {
			return "<missing>"
		}
		return string(v)
	}
	rawBefore := map[string]string{}
	for _, k := range rawKeys {
		rawBefore[k] = raw(k)
	}

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if cbHasID(res, cbNarratorCreatesRecord, "r1-c") {
		t.Errorf("r1-c listed under %s: its narrator side was refused", cbNarratorCreatesRecord)
	}
	rows := cbJournalRowsOf(t, f, "r1-c")
	if len(rows) != 1 {
		t.Fatalf("journal rows for r1-c: %d", len(rows))
	}
	var w undo.CreditsBackfillWrite
	if err := json.Unmarshal([]byte(rows[0].NewValue), &w); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Classes {
		if narratorSideClasses[c] {
			t.Errorf("journal row carries narrator class %s", c)
		}
	}
	if len(w.CreatedNarrators) != 0 || len(w.After.Narrators) != 0 ||
		cbStr(w.After.Narrator, "") != cbStr(before.Narrator, "") || cbStr(w.After.NarratorsJSON, "") != cbStr(before.NarratorsJSON, "") {
		t.Errorf("journal post-image changes the narrator side: %+v", w)
	}
	var pre undo.CreditsBackfillState
	if err := json.Unmarshal([]byte(rows[0].OldValue), &pre); err != nil {
		t.Fatal(err)
	}
	if !undo.SameCreditsBackfillState(pre, before) {
		t.Errorf("journal pre-image differs from the book as it was")
	}

	cbRevert(t, f)
	cbUntouched(t, f, "r1-c", before)
	for _, k := range rawKeys {
		if got := raw(k); got != rawBefore[k] {
			t.Errorf("%s after the revert = %s, want %s", k, got, rawBefore[k])
		}
	}
}

// --- NIT5: a fold-key twin in the join is a duplicate record ---------------

// AuthorID names "Renee Montoya", the join holds only the accented twin
// "Renée Montoya", and the snapshot (id 0) names the twin. The twin is the same
// person under another spelling, so the book is held as a duplicate name, not
// written with AuthorID prepended.
func TestCreditsBackfill_AccentTwinInTheJoinIsHeldAsDuplicateName(t *testing.T) {
	f := newCBFixture(t)
	plain := f.author("Renee Montoya")
	accent := f.author("Renée Montoya")
	f.book("c9", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(plain)
		b.Author = &database.Author{ID: 0, Name: "Renée Montoya"}
	}, ba("c9", [2]int{accent, 0}), nil)
	before := f.state("c9")
	for _, apply := range []bool{false, true} {
		res, err := f.run(creditsBackfillParams{Apply: apply}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !cbHasID(res, cbAuthorIDDuplicateName, "c9") {
			t.Errorf("apply=%v: %s = %v, want [c9]", apply, cbAuthorIDDuplicateName, cbClassIDs(res, cbAuthorIDDuplicateName))
		}
		if cbHasID(res, cbAuthorIDPrepended, "c9") {
			t.Errorf("apply=%v: c9 listed under %s", apply, cbAuthorIDPrepended)
		}
	}
	cbUntouched(t, f, "c9", before)
}
