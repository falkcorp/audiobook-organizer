// file: internal/plugins/maintenance/credits_backfill_coverage_test.go
// version: 1.0.0
// guid: 4b0e2a0e-6b8e-4d63-9c1e-7f7c5c1a2d31
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Tests for round 5 of the review of PR #4200 (credits backfill): a split
// credit never creates a narrator record on its own, whatever happens to the
// unsplit book that justified it (plan time and apply time); a split book whose
// creator is not in the cohort waits for it; a canceled dry run still files the
// books it saw; a book_ids run does not list books outside its ids; an
// apply-time spelling refusal does not leave a record nobody can find.
// Fixtures: Batman characters as people, Batman storylines as titles.

// cbCreatorRefusedFixture is the shape of the plan-time blocker: y names Renee
// Montoya alone (an unsplit credit, which creates the record without any
// opt-in) but its author is "Renée Montoya", so the accented spelling the
// record takes when x's split credit is counted (it ties 1-1, and the accent
// wins) would read back as the book's own author and refuse y's narrator side.
// x is the split book whose only creator is y.
func cbCreatorRefusedFixture(t *testing.T) *cbFixture {
	t.Helper()
	f := newCBFixture(t)
	renee := f.author("Renée Montoya")
	bruce := f.author("Bruce Wayne")
	f.narrator("Harvey Bullock")
	f.book("y", "Gotham Central: Unresolved", func(b *database.Book) {
		b.AuthorID = intp(renee)
		b.Narrator = strp("Renee Montoya")
	}, ba("y", [2]int{renee, 0}), nil)
	f.book("x", "Gotham Central: Corrigan", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renée Montoya and Harvey Bullock")
	}, ba("x", [2]int{bruce, 0}), nil)
	return f
}

// The split book's only creator is at risk of being refused by the spelling
// settle (the fixture above), and the split book's own spelling is what puts it
// at risk. The replan counted y as the creator before the settle ran, so x was
// kept as a fill and, with y refused, was the book that created the record from
// a split credit. The settle now runs to a fixpoint over the creators that
// survive: x can have no surviving creator, so it goes back to review under
// narrator_new_from_split, which takes its spelling out of the count, and y is
// then written as it names itself. No record is made from the split credit, and
// the dry run says exactly what the apply writes.
func TestCreditsBackfill_SplitBookWhoseCreatorTheSettleWouldRefuseGoesBackToReview(t *testing.T) {
	f := cbCreatorRefusedFixture(t)
	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorNewFromSplit); !slices.Equal(got, []string{"x"}) {
		t.Errorf("dry run %s = %v, want [x]: nothing creates Renée Montoya for it to link to", cbNarratorNewFromSplit, got)
	}
	if got := cbClassIDs(dry, cbNarratorFillFromColumn); !slices.Equal(got, []string{"y"}) {
		t.Errorf("dry run %s = %v, want [y]", cbNarratorFillFromColumn, got)
	}
	if !slices.Equal(dry.NarratorNames, []string{"Renee Montoya"}) {
		t.Errorf("dry run creates %v, want only the record y names", dry.NarratorNames)
	}

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v: a split credit created a record on its own", got)
	}
	if st := f.state("x"); len(st.Narrators) != 0 {
		t.Errorf("x was written: %v", st.Narrators)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, cbClassIDs(dry, cbNarratorFillFromColumn)) {
		t.Errorf("apply wrote %v, the dry run listed %v", got, cbClassIDs(dry, cbNarratorFillFromColumn))
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"x"}) {
		t.Errorf("apply %s = %v, want [x]", cbNarratorNewFromSplit, got)
	}
}

// With the opt-in the split book may create the record itself, so the refusal
// of the unsplit book changes nothing for it.
func TestCreditsBackfill_SettleRefusalOfTheOnlyCreatorDoesNotBlockAnAllowedSplitBook(t *testing.T) {
	f := cbCreatorRefusedFixture(t)
	res, err := f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowNewFromSplit}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"x"}) {
		t.Errorf("apply %s = %v, want [x]", cbNarratorFillFromColumn, got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renée Montoya"}) {
		t.Errorf("narrator records = %v", got)
	}
}

// cbFailOneMod fails ModifyBookCredits for one book.
type cbFailOneMod struct {
	database.BookCreditsModifier
	bad string
}

func (m cbFailOneMod) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if id == m.bad {
		return nil, database.BookCreditsEdit{}, errors.New("injected write failure")
	}
	return m.BookCreditsModifier.ModifyBookCredits(id, fn)
}

// The unsplit creator's write fails at apply time. The split book, which sorts
// first, must not have created the record from its split credit: creators are
// written before the books that depend on them, and a dependent only links to a
// record a creator's landed write left. It is filed under
// narrator_new_from_split, not written. The record the failed creator made
// stays (it was made for an unsplit credit) and is reported as created and
// unused, not lost.
func TestCreditsBackfill_ApplyTimeFailureOfTheCreatorSendsTheSplitBookBackToReview(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	env := creditsBackfillEnv{store: f.s, mod: cbFailOneMod{f.s, "z-solo"}, opID: cbTestOpID, workers: 2}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err == nil || res.Complete {
		t.Fatalf("a failed write must make the run incomplete: err=%v", err)
	}
	if got := cbClassIDs(res, cbWriteFailed); !slices.Equal(got, []string{"z-solo"}) {
		t.Errorf("%s = %v, want [z-solo]", cbWriteFailed, got)
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("%s = %v, want [a-split]: its creator did not land", cbNarratorNewFromSplit, got)
	}
	if st := f.state("a-split"); len(st.Narrators) != 0 {
		t.Errorf("a-split was written: %v", st.Narrators)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); len(got) != 0 {
		t.Errorf("%s = %v, want none written", cbNarratorFillFromColumn, got)
	}
	if res.NarratorsCreated != 0 || res.NarratorsCreatedUnused != 1 || !slices.Equal(res.UnusedNarratorNames, []string{"Renee Montoya"}) {
		t.Errorf("created=%d unused=%d %v, want 0 created and the one record the failed creator made reported unused",
			res.NarratorsCreated, res.NarratorsCreatedUnused, res.UnusedNarratorNames)
	}
}

// The same when the creator's write is refused for a reason other than a store
// error: the book changed between the plan and the write.
func TestCreditsBackfill_CreatorChangedSincePlanSendsTheSplitBookBackToReview(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	mod := &cbInterleavingModifier{BookCreditsModifier: f.s, bookID: "z-solo", edit: func() {
		b, err := f.s.GetBookByID("z-solo")
		if err != nil {
			t.Errorf("interleaved read: %v", err)
			return
		}
		b.Narrator = strp("Selina Kyle")
		if _, err := f.s.UpdateBook("z-solo", b); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}}
	env := creditsBackfillEnv{store: f.s, mod: mod, opID: cbTestOpID, workers: 2}
	res, _ := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if got := cbClassIDs(res, cbChangedSincePlan); !slices.Equal(got, []string{"z-solo"}) {
		t.Fatalf("%s = %v, want [z-solo]", cbChangedSincePlan, got)
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("%s = %v, want [a-split]", cbNarratorNewFromSplit, got)
	}
	if st := f.state("a-split"); len(st.Narrators) != 0 {
		t.Errorf("a-split was written: %v", st.Narrators)
	}
}

// The creator's narrator side is refused AT APPLY TIME (the store gives the
// created record a name the column cannot be respelled to): nothing credits the
// record, so the split book that depended on it is not written either, and the
// record is reported unused instead of being linked to by a split credit.
func TestCreditsBackfill_ApplyTimeSpellingRefusalOfTheCreatorSendsTheSplitBookBackToReview(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	env := creditsBackfillEnv{store: cbSpellingStore{f.s}, mod: f.s, opID: cbTestOpID, workers: 2}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err != nil {
		t.Fatalf("run: %v (a refused narrator side is not a failed book)", err)
	}
	if !cbHasID(res, cbNarratorSpellingUnreconcilable, "z-solo") {
		t.Fatalf("z-solo not refused at apply time: %s = %v", cbNarratorSpellingUnreconcilable, cbClassIDs(res, cbNarratorSpellingUnreconcilable))
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("%s = %v, want [a-split]: its creator did not land", cbNarratorNewFromSplit, got)
	}
	for _, id := range []string{"z-solo", "a-split"} {
		if st := f.state(id); len(st.Narrators) != 0 {
			t.Errorf("%s was written: %v", id, st.Narrators)
		}
	}
	if res.NarratorsCreated != 0 || res.NarratorsCreatedUnused != 1 {
		t.Errorf("created=%d unused=%d %v, want the one record nobody credits reported unused",
			res.NarratorsCreated, res.NarratorsCreatedUnused, res.UnusedNarratorNames)
	}
}

// The split book's own author side is still written when its narrator side
// goes back to review (as for a spelling refusal): only the narrator half waits.
func TestCreditsBackfill_SplitBookKeepsItsAuthorWriteWhenItsCreatorDoesNotLand(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	lucius := f.author("Lucius Fox")
	f.narrator("Harvey Bullock")
	f.book("z-solo", "Gotham Central: In the Line of Duty", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("z-solo", [2]int{bruce, 0}), nil)
	f.book("a-split", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(lucius)
		b.Narrator = strp("Renee Montoya and Harvey Bullock")
	}, ba("a-split", [2]int{bruce, 0}), nil)
	env := creditsBackfillEnv{store: f.s, mod: cbFailOneMod{f.s, "z-solo"}, opID: cbTestOpID, workers: 2}
	res, _ := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Fatalf("%s = %v, want [a-split]", cbNarratorNewFromSplit, got)
	}
	st := f.state("a-split")
	if !slices.Equal(cbAuthorIDs(st.Authors), []int{lucius, bruce}) || len(st.Narrators) != 0 {
		t.Errorf("a-split authors %v narrators %v: want the author side written and no narrator", cbAuthorIDs(st.Authors), st.Narrators)
	}
}

// --- a split book whose creator is not in the cohort waits for it ------------

// cbWaitingFixture is cbSplitFixture plus a plain book that sorts before the
// creator and takes the only place of a cohort of one.
func cbWaitingFixture(t *testing.T) *cbFixture {
	t.Helper()
	f := cbSplitFixture(t, "z-solo", "a-split")
	bruce := f.author("Dick Grayson")
	f.book("b-plain", "Arkham Asylum", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	return f
}

// A cohort that leaves the creator out cannot write the split book: it waits
// (narrator_waiting_on_creator), takes no place, and is not the book that
// creates the record. The dry run lists the same, and the cohorts one after the
// other end where one full apply ends.
func TestCreditsBackfill_SplitBookWaitsWhileTheCohortLeavesItsCreatorOut(t *testing.T) {
	f := cbWaitingFixture(t)
	dry, err := f.run(creditsBackfillParams{Limit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbAuthorFillFromAuthorID); !slices.Equal(got, []string{"b-plain"}) {
		t.Fatalf("limit-1 dry run %s = %v, want [b-plain]", cbAuthorFillFromAuthorID, got)
	}
	if got := cbClassIDs(dry, cbNarratorWaitingOnCreator); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("limit-1 dry run %s = %v, want [a-split]", cbNarratorWaitingOnCreator, got)
	}
	if got := cbClassIDs(dry, cbLimitReached); !slices.Equal(got, []string{"z-solo"}) {
		t.Errorf("limit-1 dry run %s = %v, want [z-solo] only: a waiting book does not use up a slot", cbLimitReached, got)
	}
	if dry.BooksEligible != 3 {
		t.Errorf("books_eligible = %d, want 3: a waiting book is still eligible", dry.BooksEligible)
	}

	wants := [][]string{{"b-plain"}, {"z-solo"}, {"a-split"}}
	for i, want := range wants {
		res, err := f.runWith(context.Background(), &cbReporter{id: "01COHORT" + string(rune('A'+i))}, creditsBackfillParams{Apply: true, Limit: 1}, 2)
		if err != nil {
			t.Fatal(err)
		}
		got := slices.Concat(cbClassIDs(res, cbNarratorFillFromColumn), cbClassIDs(res, cbAuthorFillFromAuthorID))
		// The author-only book of cohort 1 is also in every later cohort's
		// earlier-attempt count; only this cohort's own writes are listed.
		got = slices.DeleteFunc(got, func(id string) bool { return id == "b-plain" && i > 0 })
		if !slices.Equal(got, want) {
			t.Fatalf("cohort %d wrote %v, want %v", i+1, got, want)
		}
		if i == 0 {
			if st := f.state("a-split"); len(st.Narrators) != 0 {
				t.Fatalf("cohort 1 wrote the split book without its creator: %v", st.Narrators)
			}
			if got := cbClassIDs(res, cbNarratorWaitingOnCreator); !slices.Equal(got, []string{"a-split"}) {
				t.Errorf("cohort 1 %s = %v, want [a-split]", cbNarratorWaitingOnCreator, got)
			}
		}
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want Harvey Bullock and ONE Renee Montoya", got)
	}
	assertPlansClean(t, f, "z-solo", "a-split", "b-plain")
}

// A split book whose creator is in the cohort is written in it, creator first;
// a split book the cohort has no place for is limit_reached, not waiting.
func TestCreditsBackfill_SplitBookTakesThePlaceTheCreatorLeavesOrIsLimitReached(t *testing.T) {
	f := cbWaitingFixture(t)
	room, err := f.run(creditsBackfillParams{Limit: 3}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(room, cbNarratorFillFromColumn); !slices.Equal(got, []string{"a-split", "z-solo"}) {
		t.Errorf("limit-3 dry run %s = %v, want both", cbNarratorFillFromColumn, got)
	}
	tight, err := f.run(creditsBackfillParams{Limit: 2}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// b-plain and z-solo take the two places in sorted order; a-split's creator
	// is in the cohort, so it is outside the limit, not waiting.
	if got := cbClassIDs(tight, cbLimitReached); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("limit-2 dry run %s = %v, want [a-split]", cbLimitReached, got)
	}
	if got := cbClassIDs(tight, cbNarratorWaitingOnCreator); len(got) != 0 {
		t.Errorf("limit-2 dry run %s = %v, want none: its creator is in the cohort", cbNarratorWaitingOnCreator, got)
	}
}

// A book_ids run is a cohort too: a split book named alone, whose creator is
// outside the ids, waits; named with its creator it is written.
func TestCreditsBackfill_BookIDsRunWritesASplitBookOnlyWithItsCreator(t *testing.T) {
	f := cbSplitFixture(t, "s1", "s2")
	alone, err := f.run(creditsBackfillParams{Apply: true, BookIDs: []string{"s2"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(alone, cbNarratorWaitingOnCreator); !slices.Equal(got, []string{"s2"}) {
		t.Errorf("s2 alone: %s = %v, want [s2]", cbNarratorWaitingOnCreator, got)
	}
	if st := f.state("s2"); len(st.Narrators) != 0 {
		t.Fatalf("s2 was written without its creator in the cohort: %v", st.Narrators)
	}
	both, err := f.runWith(context.Background(), &cbReporter{id: "01COHORTB"}, creditsBackfillParams{Apply: true, BookIDs: []string{"s1", "s2"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(both, cbNarratorFillFromColumn); !slices.Equal(got, []string{"s1", "s2"}) {
		t.Errorf("s1+s2: %s = %v, want both", cbNarratorFillFromColumn, got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v", got)
	}
}

// --- a book_ids run does not list books outside its ids ----------------------

// A book outside the ids that cannot be read is no row of this run's result:
// not under read_error, not in the failed count. The run is still incomplete (an
// unreadable book can change what the whole-library plan counts) and says why:
// out_of_scope_read_errors.
func TestCreditsBackfill_BookIDsRunDoesNotListAnOutOfScopeReadError(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for _, id := range []string{"k-1", "k-2"} {
		f.book(id, "Knightfall "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp("Renee Montoya")
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	env := creditsBackfillEnv{store: failingReadStore{f.s, "k-2"}, workers: 2}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{BookIDs: []string{"k-1"}}, &cbReporter{id: cbTestOpID})
	if err == nil || res.Complete {
		t.Fatalf("the run must stay incomplete: err=%v complete=%v", err, res.Complete)
	}
	for _, c := range res.Classes {
		if slices.Contains(c.BookIDs, "k-2") {
			t.Errorf("out-of-scope k-2 listed under %s", c.Key)
		}
	}
	if res.OutOfScopeReadErrors != 1 {
		t.Errorf("out_of_scope_read_errors = %d, want 1", res.OutOfScopeReadErrors)
	}
	if got := res.Counts[cbReadError]; got != 0 {
		t.Errorf("read_error count = %d, want 0 for a book outside the ids", got)
	}
	// An in-scope unreadable book is still a read_error of the run.
	env = creditsBackfillEnv{store: failingReadStore{f.s, "k-1"}, workers: 2}
	res, _ = f.runEnv(context.Background(), env, creditsBackfillParams{BookIDs: []string{"k-1"}}, &cbReporter{id: cbTestOpID})
	if got := cbClassIDs(res, cbReadError); !slices.Equal(got, []string{"k-1"}) || res.OutOfScopeReadErrors != 0 {
		t.Errorf("in-scope read error: %s = %v, out_of_scope_read_errors = %d", cbReadError, got, res.OutOfScopeReadErrors)
	}
}

// --- a canceled dry run still files the split books it saw -------------------

func TestCreditsBackfill_CanceledDryRunFilesThePendingSplitBooksUnderTheirPhaseOneVerdict(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Harvey Bullock")
	f.book("a-split", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Selina Kyle and Harvey Bullock")
	}, ba("a-split", [2]int{bruce, 0}), nil)
	for _, id := range []string{"b-1", "b-2", "b-3", "b-4", "b-5", "b-6"} {
		f.book(id, "Arkham "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &cbReporter{id: cbTestOpID, cancel: cancel, cancelAfter: 4}
	res, err := f.runWith(ctx, rep, creditsBackfillParams{}, 1)
	if err == nil || res.Complete {
		t.Fatalf("a canceled run must be incomplete: err=%v", err)
	}
	if res.BooksScanned == 0 {
		t.Fatal("nothing was scanned: the fixture no longer exercises the cancel")
	}
	if got := cbClassIDs(res, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("%s = %v, want [a-split]: it was scanned and is in no class otherwise", cbNarratorNewFromSplit, got)
	}
}

// --- an apply-time spelling refusal leaves no unlisted record ----------------

// A narrator-only book whose created record cannot be credited under the
// spelling the store gave it keeps no row (nothing is written), and the record
// the store made is reported as created and unused, so it can be found and
// purged (maintenance.purge-empty-narrators) instead of sitting unlisted.
func TestCreditsBackfill_ApplyTimeSpellingRefusalReportsTheRecordNobodyCredits(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("ap", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("ap", [2]int{bruce, 0}), nil)
	env := creditsBackfillEnv{store: cbSpellingStore{f.s}, mod: f.s, opID: cbTestOpID, workers: 1}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err != nil {
		t.Fatal(err)
	}
	if !cbHasID(res, cbNarratorSpellingUnreconcilable, "ap") {
		t.Errorf("ap not filed under %s", cbNarratorSpellingUnreconcilable)
	}
	if rows := cbJournalRowsOf(t, f, "ap"); len(rows) != 0 {
		t.Errorf("journal rows for a book that was not written: %d", len(rows))
	}
	if res.NarratorsCreated != 0 || res.NarratorsCreatedUnused != 1 || len(res.UnusedNarratorNames) != 1 {
		t.Errorf("created=%d unused=%d %v, want the record reported unused", res.NarratorsCreated, res.NarratorsCreatedUnused, res.UnusedNarratorNames)
	}
}
