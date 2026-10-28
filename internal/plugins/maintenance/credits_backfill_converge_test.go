// file: internal/plugins/maintenance/credits_backfill_converge_test.go
// version: 1.0.0
// guid: 7d1f0c52-3a9e-4b6c-8e04-5f2a9c6b1d73
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Tests for round 6 of the review of PR #4200 (credits backfill): the settle
// does not send a split book back to review for good when its own spelling is
// what refused its creator (a full apply, a dry run and the cohorts agree); a
// record a failed creator leaves behind is deleted again so a later run cannot
// link a split credit to it; narrator_creates_record lists creators only; a
// canceled apply is a known gap (todo.d). Fixtures: Batman characters as
// people, Batman storylines as titles.

// cbToWrite is the number of books a run would write (or wrote), whatever the class.
func cbToWrite(res *creditsBackfillResult) int { return res.BooksToWrite }

// The split book's spelling ("Renée Montoya") ties with its creator's
// ("Renee Montoya") and wins on the accent, which would read back as the
// creator's own author and refuse the creator. The creator's own vote alone
// decides the record's spelling, the split book is respelled to it, and both are
// written: the very result the run after a demotion would have produced, found
// in one pass.
func TestCreditsBackfill_FullApplyLeavesNothingForTheNextRun(t *testing.T) {
	f := cbCreatorRefusedFixture(t)
	rep := &cbReporter{id: "01FULLAPPLY"}
	full, err := f.runWith(context.Background(), rep, creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(full, cbNarratorFillFromColumn); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("full apply wrote %v, want [x y] (new_from_split %v)", got, cbClassIDs(full, cbNarratorNewFromSplit))
	}
	if got := cbClassIDs(full, cbNarratorNewFromSplit); len(got) != 0 {
		t.Errorf("%s = %v, want none: the creator survives under its own spelling", cbNarratorNewFromSplit, got)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want the creator's spelling", got)
	}
	// The log line says the settle needed a second pass for the one key.
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "spellings settled in 2 pass(es)") || !strings.Contains(logs, "1 key(s) settled by their creators alone") {
		t.Errorf("settle log line missing from %q", logs)
	}

	again, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n := cbToWrite(again); n != 0 {
		t.Errorf("a dry run after the full apply still lists %d book(s) to write: %v / %v", n,
			cbClassIDs(again, cbNarratorFillFromColumn), cbClassIDs(again, cbNarratorNewFromSplit))
	}
}

// The cohorts of a limit write what one full apply writes, in the same
// spelling: the union is the full apply's set (D65).
func TestCreditsBackfill_UnionOfCohortsEqualsOneFullApply(t *testing.T) {
	full := cbCreatorRefusedFixture(t)
	fullRes, err := full.runWith(context.Background(), &cbReporter{id: "01FULLAPPLY"}, creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}

	f := cbCreatorRefusedFixture(t)
	var written []string
	for _, id := range []string{"01COHORT1", "01COHORT2", "01COHORT3"} {
		res, err := f.runWith(context.Background(), &cbReporter{id: id}, creditsBackfillParams{Apply: true, Limit: 1}, 2)
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, cbClassIDs(res, cbNarratorFillFromColumn)...)
	}
	slices.Sort(written)
	if want := cbClassIDs(fullRes, cbNarratorFillFromColumn); !slices.Equal(written, want) {
		t.Errorf("cohorts wrote %v, one full apply wrote %v", written, want)
	}
	for _, id := range []string{"x", "y"} {
		a, b := full.state(id), f.state(id)
		if len(a.Narrators) != len(b.Narrators) {
			t.Errorf("%s: full apply left %v, cohorts left %v", id, a.Narrators, b.Narrators)
		}
	}
	xb, _ := f.s.GetBookByID("x")
	fb, _ := full.s.GetBookByID("x")
	if xb == nil || fb == nil || xb.Narrator == nil || fb.Narrator == nil || *xb.Narrator != *fb.Narrator {
		t.Errorf("x's narrator text differs between the cohorts and the full apply")
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, cbRecordNames(t, full)) {
		t.Errorf("records: cohorts %v, full apply %v", got, cbRecordNames(t, full))
	}
}

// A key none of whose creators exists is the only way a split book goes back to
// review once the creators vote alone: nobody creates the record.
func TestSettleLibrary_DependentWithNoCreatorAtAllIsDemoted(t *testing.T) {
	key := authorjunk.FoldKey("Renee Montoya")
	r := &cbRun{plans: map[string]*cbPlanned{
		"x": {id: "x", plan: &cbPlan{
			narratorWrite: true,
			narrators:     []cbNarratorSlot{{Name: "Renee Montoya"}},
			dependsOn:     []string{key},
		}},
		"y": {id: "y", plan: &cbPlan{authorWrite: true}},
	}}
	st := r.settleLibrary([]string{"x", "y"})
	if !slices.Equal(st.demoted, []string{"x"}) {
		t.Errorf("demoted = %v, want [x]", st.demoted)
	}
	if st.passes != 2 {
		t.Errorf("passes = %d, want 2 (one try at settling the key by its creators, which has none)", st.passes)
	}
	if st.boundHit {
		t.Error("the pass bound was hit")
	}
}

// --- narrator_creates_record lists the books that create a record ------------

func TestCreditsBackfill_CreatesRecordListsOnlyTheBookThatCreates(t *testing.T) {
	f := cbSplitFixture(t, "s1", "s2")
	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorCreatesRecord); !slices.Equal(got, []string{"s1"}) {
		t.Errorf("%s = %v, want [s1]: s2 only links to the record s1 creates", cbNarratorCreatesRecord, got)
	}
}

// --- the record a failed creator leaves is deleted again ---------------------

// cbOldRecordStore makes a store's CreateNarrator return the record dated far in
// the past, the way it would return a record somebody made before the run.
type cbOldRecordStore struct{ creditsBackfillStore }

func (s cbOldRecordStore) Unwrap() database.Store {
	st, _ := s.creditsBackfillStore.(database.Store)
	return st
}

func (s cbOldRecordStore) CreateNarrator(name string) (*database.Narrator, error) {
	n, err := s.creditsBackfillStore.CreateNarrator(name)
	if err != nil || n == nil {
		return n, err
	}
	out := *n
	out.CreatedAt = time.Now().Add(-48 * time.Hour)
	return &out, nil
}

// cbCreatorEdit is the edit that lands on z-solo between the plan and the write
// of the creator: the book no longer names Renee Montoya, so its write is
// refused as changed since the plan, after the record was made for it.
func cbCreatorEdit(t *testing.T, f *cbFixture, also func()) *cbInterleavingModifier {
	return &cbInterleavingModifier{BookCreditsModifier: f.s, bookID: "z-solo", edit: func() {
		b, err := f.s.GetBookByID("z-solo")
		if err != nil {
			t.Errorf("interleaved read: %v", err)
			return
		}
		b.Narrator = strp("Selina Kyle")
		if _, err := f.s.UpdateBook("z-solo", b); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
		if also != nil {
			also()
		}
	}}
}

// The creator's write is refused after CreateNarrator made the record. Left in
// place, the next run's plan finds it and links the split book to it, so the
// record ends up credited by the split credit alone. It is deleted at the end of
// the apply (nothing links it, this run made it), journaled, and reported; the
// next run finds no creator and sends the split book to review.
func TestCreditsBackfill_RecordOfAFailedCreatorIsDeletedNotLeftForTheSplitBook(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	env := creditsBackfillEnv{store: f.s, mod: cbCreatorEdit(t, f, nil), opID: "01RUNONE", workers: 2}
	res, _ := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: "01RUNONE"})
	if res.NarratorsCreatedUnused != 1 || res.UnusedNarratorsDeleted != 1 ||
		!slices.Equal(res.DeletedUnusedNarratorNames, []string{"Renee Montoya"}) || len(res.UnusedNarratorsKept) != 0 {
		t.Fatalf("unused=%d deleted=%d %v kept=%v, want the one record deleted",
			res.NarratorsCreatedUnused, res.UnusedNarratorsDeleted, res.DeletedUnusedNarratorNames, res.UnusedNarratorsKept)
	}
	// Selina Kyle is the record the interleaved edit's own narrator sync made.
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Selina Kyle"}) {
		t.Fatalf("narrator records after run 1 = %v, want Renee Montoya gone", got)
	}
	rows, err := f.s.GetOperationChanges("01RUNONE")
	if err != nil {
		t.Fatal(err)
	}
	deleted := 0
	for _, c := range rows {
		if c.ChangeType == "narrator_delete" && strings.HasSuffix(c.OldValue, ":Renee Montoya") {
			deleted++
		}
	}
	if deleted != 1 {
		t.Errorf("%d narrator_delete journal rows for the record, want 1", deleted)
	}

	res2, err := f.runWith(context.Background(), &cbReporter{id: "01RUNTWO"}, creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := cbClassIDs(res2, cbNarratorNewFromSplit); !slices.Equal(got, []string{"a-split"}) {
		t.Errorf("run 2 %s = %v, want [a-split]: nobody creates Renee Montoya now", cbNarratorNewFromSplit, got)
	}
	if st := f.state("a-split"); len(st.Narrators) != 0 {
		t.Errorf("a-split was written by run 2: %v", st.Narrators)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Selina Kyle"}) {
		t.Errorf("narrator records after run 2 = %v, want no record only a split credit uses", got)
	}
}

// A record that gained a link before the cleanup is kept, as
// purge-empty-narrators keeps one that gained a link before its delete.
func TestCreditsBackfill_UnusedRecordThatGainedALinkIsKept(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	alfred := f.author("Alfred Pennyworth")
	f.book("bystander", "Gotham Central: Soft Targets", func(b *database.Book) { b.AuthorID = intp(alfred) }, ba("bystander", [2]int{alfred, 0}), nil)
	link := func() {
		n, err := f.s.GetNarratorByName("Renee Montoya")
		if err != nil || n == nil {
			t.Errorf("record not found at the interleaved point: %v", err)
			return
		}
		if err := f.s.SetBookNarrators("bystander", []database.BookNarrator{{BookID: "bystander", NarratorID: n.ID, Role: "narrator"}}); err != nil {
			t.Errorf("link: %v", err)
		}
	}
	env := creditsBackfillEnv{store: f.s, mod: cbCreatorEdit(t, f, link), opID: "01RUNONE", workers: 2}
	res, _ := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: "01RUNONE"})
	if res.UnusedNarratorsDeleted != 0 || !slices.Equal(res.UnusedNarratorsKept, []string{"Renee Montoya (linked by 1 book(s))"}) {
		t.Errorf("deleted=%d kept=%v, want the linked record kept", res.UnusedNarratorsDeleted, res.UnusedNarratorsKept)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya", "Selina Kyle"}) {
		t.Errorf("narrator records = %v, want the linked record kept", got)
	}
}

// CreateNarrator is resolve-or-create: a record it returns that is older than
// the run was not made by it and is never deleted.
func TestCreditsBackfill_UnusedRecordOlderThanTheRunIsKept(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	env := creditsBackfillEnv{store: cbOldRecordStore{f.s}, mod: cbFailOneMod{f.s, "z-solo"}, opID: "01RUNONE", workers: 2}
	res, _ := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: "01RUNONE"})
	if res.UnusedNarratorsDeleted != 0 || !slices.Equal(res.UnusedNarratorsKept, []string{"Renee Montoya (existed before the run)"}) {
		t.Errorf("deleted=%d kept=%v, want the old record kept", res.UnusedNarratorsDeleted, res.UnusedNarratorsKept)
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock", "Renee Montoya"}) {
		t.Errorf("narrator records = %v, want the old record kept", got)
	}
}

// One worker writes in order and the split book sorts before its creator: the
// creator's write fails, and the split book, written second, finds no landed
// record. Neither book is written and the record the creator made is deleted
// again, so the library is left as the run found it. (With the split book
// written first and creating the record itself, the record would be credited by
// the split credit alone.)
func TestCreditsBackfill_FailedCreatorAndOneWorkerLeaveNoSplitOnlyRecord(t *testing.T) {
	f := cbSplitFixture(t, "z-solo", "a-split")
	env := creditsBackfillEnv{store: f.s, mod: cbFailOneMod{f.s, "z-solo"}, opID: cbTestOpID, workers: 1}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err == nil {
		t.Fatal("a failed write must make the run incomplete")
	}
	for _, id := range []string{"z-solo", "a-split"} {
		if st := f.state(id); len(st.Narrators) != 0 {
			t.Errorf("%s was written: %v", id, st.Narrators)
		}
	}
	if got := cbRecordNames(t, f); !slices.Equal(got, []string{"Harvey Bullock"}) {
		t.Errorf("narrator records = %v, want the record the failed creator made deleted again", got)
	}
	if res.UnusedNarratorsDeleted != 1 {
		t.Errorf("unused records deleted = %d, want 1", res.UnusedNarratorsDeleted)
	}
}
