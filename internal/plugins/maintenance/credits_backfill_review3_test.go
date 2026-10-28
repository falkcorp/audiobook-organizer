// file: internal/plugins/maintenance/credits_backfill_review3_test.go
// version: 1.0.2
// guid: 2c9f6b13-7a4e-48d0-b5e1-0d83a7c4f956
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Tests for the third adversarial review of PR #4200 (credits backfill):
// a spelling move respells only the matched name, NarratorsJSON follows it,
// a nameless snapshot that names someone else is held, and a new narrator's
// spelling is the same on every run. Fixtures: Batman characters as people,
// Batman storylines as titles, paths under /lib/Gotham (cbFixture.book).
// Nothing here comes from a real library.

func cbStr(s *string, none string) string {
	if s == nil {
		return none
	}
	return *s
}

// --- SF1: a spelling move changes the matched name, not the column ----------

// A narrator name linked to a record under another spelling is respelled
// INSIDE the Narrator column as stored. The column used to be rebuilt from the
// cleaned names, which dropped the "Read by" prefix, the role credit and the
// co-narrating author. A control book whose record already has the exact
// spelling keeps its column and is not listed as respelled.
func TestCreditsBackfill_SpellingMoveRespellsOnlyTheMatchedNameInTheColumn(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	renee := f.narrator("Renée Montoya")
	f.narrator("Leslie Thompkins")
	const moved = "Read by Renee Montoya, Bruce Wayne, Lucius Fox - translator"
	const control = "Read by Leslie Thompkins, Bruce Wayne, Lucius Fox - translator"
	f.book("sp-moved", "Gotham Central: Unresolved", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp(moved)
	}, ba("sp-moved", [2]int{bruce, 0}), nil)
	f.book("sp-control", "Gotham Central: Control", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp(control)
	}, ba("sp-control", [2]int{bruce, 0}), nil)

	// The dry run says which books an apply would respell, and writes nothing.
	dry, err := f.run(creditsBackfillParams{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorColumnRespelled); !slices.Equal(got, []string{"sp-moved"}) {
		t.Fatalf("dry run %s = %v, want [sp-moved]", cbNarratorColumnRespelled, got)
	}
	if got := cbStr(f.state("sp-moved").Narrator, "<nil>"); got != moved {
		t.Fatalf("the dry run wrote the column: %q", got)
	}

	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorColumnRespelled); !slices.Equal(got, []string{"sp-moved"}) {
		t.Fatalf("%s = %v, want [sp-moved]", cbNarratorColumnRespelled, got)
	}
	st := f.state("sp-moved")
	if want := "Read by Renée Montoya, Bruce Wayne, Lucius Fox - translator"; cbStr(st.Narrator, "<nil>") != want {
		t.Fatalf("column = %q, want %q (only the name respelled)", cbStr(st.Narrator, "<nil>"), want)
	}
	if ids := cbNarratorIDs(st.Narrators); !slices.Equal(ids, []int{renee}) {
		t.Fatalf("junction = %v, want the existing record %d", ids, renee)
	}
	if got := cbStr(f.state("sp-control").Narrator, "<nil>"); got != control {
		t.Fatalf("control column = %q, want it untouched", got)
	}
	assertPlansClean(t, f, "sp-moved", "sp-control")

	if r := cbRevert(t, f); r.Restored != 2 {
		t.Fatalf("revert = %+v, want 2 restored", r)
	}
	if got := cbStr(f.state("sp-moved").Narrator, "<nil>"); got != moved {
		t.Fatalf("after the revert the column = %q, want the pre-image", got)
	}
}

// The fold key also folds punctuation and spaces: "Jean Paul Valley" links to
// the record "Jean-Paul Valley", and only that name changes in the column.
func TestCreditsBackfill_PunctuationFoldedNameIsRespelledInPlace(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	jp := f.narrator("Jean-Paul Valley")
	f.book("pf-1", "Knightfall: Azrael", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Narrated by Jean Paul Valley")
	}, ba("pf-1", [2]int{bruce, 0}), nil)

	if _, err := f.run(creditsBackfillParams{Apply: true}, 1); err != nil {
		t.Fatal(err)
	}
	st := f.state("pf-1")
	if got := cbStr(st.Narrator, "<nil>"); got != "Narrated by Jean-Paul Valley" {
		t.Fatalf("column = %q", got)
	}
	if ids := cbNarratorIDs(st.Narrators); !slices.Equal(ids, []int{jp}) {
		t.Fatalf("junction = %v, want the record %d", ids, jp)
	}
	assertPlansClean(t, f, "pf-1")
}

// --- SF2: NarratorsJSON is respelled in the same write -----------------------

// A fold-linked name no longer sends a NarratorsJSON book to review: the JSON
// entry is respelled with the column and the junction in one journaled write,
// so the three agree, and the revert restores all of it.
func TestCreditsBackfill_FoldLinkedNarratorsJSONIsRespelledInTheSameWrite(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	jp := f.narrator("Jean-Paul Valley")
	renee := f.narrator("Renée Montoya")
	f.book("fj-only", "Gotham Central: Soft Targets", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.NarratorsJSON = strp(`["Renee Montoya"]`)
	}, ba("fj-only", [2]int{bruce, 0}), nil)
	f.book("fj-both", "Knightfall: Broken Bat", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Read by Jean Paul Valley")
		b.NarratorsJSON = strp(`["Jean Paul Valley"]`)
	}, ba("fj-both", [2]int{bruce, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cbHasID(res, cbNarratorFillFromNarratorsJSON, "fj-only") || !cbHasID(res, cbNarratorFillFromColumn, "fj-both") {
		t.Fatalf("books not written: counts %v", res.Counts)
	}
	for _, id := range []string{"fj-only", "fj-both"} {
		if !cbHasID(res, cbNarratorsJSONRespelled, id) {
			t.Errorf("%s not listed under %s", id, cbNarratorsJSONRespelled)
		}
	}
	if !cbHasID(res, cbNarratorColumnRespelled, "fj-both") || cbHasID(res, cbNarratorColumnRespelled, "fj-only") {
		t.Errorf("%s = %v, want exactly the book whose column text was edited", cbNarratorColumnRespelled, cbClassIDs(res, cbNarratorColumnRespelled))
	}
	for _, key := range []string{ccNarratorsJSONDisagrees, cbNarratorSpellingJSONConflict, cbNarratorSpellingUnreconcilable, cbNarratorsJSONUnjoinable} {
		if got := cbClassIDs(res, key); len(got) != 0 {
			t.Errorf("%s lists %v, want none", key, got)
		}
	}

	only, both := f.state("fj-only"), f.state("fj-both")
	if cbStr(only.NarratorsJSON, "") != `["Renée Montoya"]` || cbStr(only.Narrator, "") != "Renée Montoya" ||
		!slices.Equal(cbNarratorIDs(only.Narrators), []int{renee}) {
		t.Fatalf("fj-only: json %q column %q junction %v", cbStr(only.NarratorsJSON, ""), cbStr(only.Narrator, ""), cbNarratorIDs(only.Narrators))
	}
	if cbStr(both.NarratorsJSON, "") != `["Jean-Paul Valley"]` || cbStr(both.Narrator, "") != "Read by Jean-Paul Valley" ||
		!slices.Equal(cbNarratorIDs(both.Narrators), []int{jp}) {
		t.Fatalf("fj-both: json %q column %q junction %v", cbStr(both.NarratorsJSON, ""), cbStr(both.Narrator, ""), cbNarratorIDs(both.Narrators))
	}
	assertPlansClean(t, f, "fj-only", "fj-both")

	if r := cbRevert(t, f); r.Restored != 2 {
		t.Fatalf("revert = %+v, want 2 restored", r)
	}
	only, both = f.state("fj-only"), f.state("fj-both")
	if cbStr(only.NarratorsJSON, "") != `["Renee Montoya"]` || only.Narrator != nil || len(only.Narrators) != 0 {
		t.Fatalf("fj-only after the revert: json %q column %v junction %v", cbStr(only.NarratorsJSON, ""), only.Narrator, cbNarratorIDs(only.Narrators))
	}
	if cbStr(both.NarratorsJSON, "") != `["Jean Paul Valley"]` || cbStr(both.Narrator, "") != "Read by Jean Paul Valley" {
		t.Fatalf("fj-both after the revert: json %q column %q", cbStr(both.NarratorsJSON, ""), cbStr(both.Narrator, ""))
	}
}

// A spelling move that cannot be settled is this op's own refusal, with its own
// class: a dry run can size it, and it is not folded into the census keys of
// the same name. Here the record's name carries a separator, so replacing the
// name in the column does not read back as one person.
func TestCreditsBackfill_UnreconcilableSpellingMoveHasItsOwnClassAndWritesNothing(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Dick & Grayson")
	f.book("uj-sep", "Batman: Year Two", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Dick Grayson")
	}, ba("uj-sep", [2]int{bruce, 0}), nil)

	dry, err := f.run(creditsBackfillParams{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]*creditsBackfillResult{"dry run": dry, "apply": res} {
		if got := cbClassIDs(r, cbNarratorSpellingUnreconcilable); !slices.Equal(got, []string{"uj-sep"}) {
			t.Errorf("%s: %s = %v, want [uj-sep]", name, cbNarratorSpellingUnreconcilable, got)
		}
		for _, key := range []string{ccNarratorsJSONDisagrees, cbNarratorsJSONUnjoinable} {
			if got := cbClassIDs(r, key); len(got) != 0 {
				t.Errorf("%s: census key %s lists %v, want none (this op's refusals have their own keys)", name, key, got)
			}
		}
	}
	if st := f.state("uj-sep"); len(st.Narrators) != 0 {
		t.Errorf("uj-sep was written: junction %v", cbNarratorIDs(st.Narrators))
	}
}

// A NarratorsJSON entry that agrees with the column only by case is the same
// person to the cleaner (util.NormalizeAuthor), so the respelling reaches it
// too: the book is written, the entry takes the record's spelling, and the book
// plans clean. (It used to be refused as a JSON conflict, because the respelling
// matched the exact spelling only.)
func TestCreditsBackfill_NarratorsJSONEntryThatDiffersOnlyByCaseIsRespelledToo(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.book("uj-json", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
		b.NarratorsJSON = strp(`["RENEE MONTOYA"]`)
	}, ba("uj-json", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"uj-json"}) {
		t.Fatalf("%s = %v, want [uj-json]", cbNarratorFillFromColumn, got)
	}
	st := f.state("uj-json")
	if got := cbStr(st.NarratorsJSON, ""); got != `["Renée Montoya"]` {
		t.Errorf("NarratorsJSON = %q, want the entry respelled to the record", got)
	}
	if got := cbStr(st.Narrator, ""); got != "Renée Montoya" {
		t.Errorf("column = %q", got)
	}
	assertPlansClean(t, f, "uj-json")
}

// The backstop of the respelling: a NarratorsJSON that, respelled, would still
// match neither the slots nor the column is the JSON-conflict refusal. (The plan
// only keeps a book whose JSON agrees with its credit, and a respelling reaches
// every entry naming the person, so a planned book does not get here; the check
// stays for a plan built any other way.)
func TestCreditsBackfill_SettleNarratorSpellingRefusesAJSONThatStaysInConflict(t *testing.T) {
	raw := `["Harvey Bullock"]`
	pl := &cbPlan{
		requested:  []string{"Renee Montoya"},
		columnText: "Renee Montoya",
		rawJSON:    &raw,
		jsonNames:  []string{"Harvey Bullock"},
	}
	slots := []cbNarratorSlot{{Name: "Renée Montoya", ID: 7}}
	if _, _, refuse := settleNarratorSpelling(pl, slots); refuse != cbNarratorSpellingJSONConflict {
		t.Errorf("refuse = %q, want %q", refuse, cbNarratorSpellingJSONConflict)
	}
}

// --- N2: a fold-shared new narrator's spelling does not depend on the worker -

// Books naming one NEW person under spellings of one fold key get one record
// that takes the most frequent spelling (ties: mixed case, then the accented
// form, then the lexically smallest), the
// same on every run and in the dry run, whichever worker gets there first.
func TestCreditsBackfill_FoldSharedNewNarratorTakesTheMostFrequentSpelling(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names []string
		want  string
	}{
		{"most frequent wins", []string{"Renee Montoya", "Renée Montoya", "Renée Montoya"}, "Renée Montoya"},
		{"tie goes to the accented spelling", []string{"Renée Montoya", "Renee Montoya"}, "Renée Montoya"},
		{"tie goes to mixed case over all capitals", []string{"RENEE MONTOYA", "Renee Montoya"}, "Renee Montoya"},
		{"tie of equals goes to the lexically smallest", []string{"renee Montoya", "Renee Montoya"}, "Renee Montoya"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCBFixture(t)
			bruce := f.author("Bruce Wayne")
			var ids, differing []string
			for i, n := range tc.names {
				id := "ns-" + string(rune('a'+i))
				ids = append(ids, id)
				// The census compares spellings by their normalized form, so a
				// column that differs from the record only in case keeps its text
				// and is not listed as respelled.
				if util.NormalizeAuthor(n) != util.NormalizeAuthor(tc.want) {
					differing = append(differing, id)
				}
				f.book(id, "No Man's Land "+id, func(b *database.Book) {
					b.AuthorID = intp(bruce)
					b.Narrator = strp(n)
				}, ba(id, [2]int{bruce, 0}), nil)
			}
			dry, err := f.run(creditsBackfillParams{}, 4)
			if err != nil {
				t.Fatal(err)
			}
			if got := cbClassIDs(dry, cbNarratorColumnRespelled); !slices.Equal(got, differing) {
				t.Fatalf("dry run %s = %v, want %v (the books whose spelling differs from the one the record takes)",
					cbNarratorColumnRespelled, got, differing)
			}
			res, err := f.run(creditsBackfillParams{Apply: true}, 4)
			if err != nil {
				t.Fatal(err)
			}
			if got := cbClassIDs(res, cbNarratorColumnRespelled); !slices.Equal(got, differing) {
				t.Fatalf("apply %s = %v, want %v", cbNarratorColumnRespelled, got, differing)
			}
			all, err := f.s.ListNarrators()
			if err != nil {
				t.Fatal(err)
			}
			if len(all) != 1 || all[0].Name != tc.want {
				t.Fatalf("narrator records = %+v, want one named %q", all, tc.want)
			}
			for i, id := range ids {
				// A column that differs only in case already reads as the record's
				// name to the census, so it keeps its text.
				want := tc.want
				if util.NormalizeAuthor(tc.names[i]) == util.NormalizeAuthor(tc.want) {
					want = tc.names[i]
				}
				if got := cbStr(f.state(id).Narrator, "<nil>"); got != want {
					t.Errorf("%s column = %q, want %q", id, got, want)
				}
			}
			assertPlansClean(t, f, ids...)
		})
	}
}

// --- N3: the index is built after the stand-down hold ------------------------

// hookScan is a scan controller whose acquire runs a callback: it stands for a
// library scan that was still creating records when the hold was requested.
type hookScan struct{ onAcquire func() }

func (h hookScan) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	h.onAcquire()
	return func() {}, nil
}
func (hookScan) RenewScanStandDown(string) bool { return true }
func (hookScan) ScanStandDownValid(string) bool { return true }

// A narrator record created while the run waits for the scan to stand down is
// seen by the plan: the index is built after the hold, so the name is linked to
// the record and no second one is created.
func TestCreditsBackfill_IndexIsBuiltAfterTheScanStandDown(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("ix-1", "Gotham Central: Corrigan", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("ix-1", [2]int{bruce, 0}), nil)
	renee := 0
	env := creditsBackfillEnv{store: f.s, mod: f.s, opID: cbTestOpID, workers: 1,
		scan: hookScan{onAcquire: func() { renee = f.narrator("Renée Montoya") }}}
	res, err := f.runEnv(context.Background(), env, creditsBackfillParams{Apply: true}, &cbReporter{id: cbTestOpID})
	if err != nil {
		t.Fatal(err)
	}
	if renee == 0 {
		t.Fatal("the scan hold was never requested")
	}
	if res.NarratorsCreated != 0 {
		t.Fatalf("narrators_created = %d, want 0: the plan did not see the record made while the hold was acquired", res.NarratorsCreated)
	}
	if ids := cbNarratorIDs(f.state("ix-1").Narrators); !slices.Equal(ids, []int{renee}) {
		t.Fatalf("junction = %v, want the record %d", ids, renee)
	}
}

// --- SF3: a snapshot with no usable id still names a different author --------

// A snapshot whose id is 0 or resolves to nothing but whose NAME is another
// person's still decides who the organizer files the book under. Filling the
// empty join from AuthorID would flip the filing author and the snapshot
// refresh would overwrite the evidence, so the book is held. The name is
// compared with AuthorID's record name; no author is looked up by it.
func TestCreditsBackfill_SnapshotWithoutAResolvableIDIsComparedByName(t *testing.T) {
	f := newCBFixture(t)
	jason := f.author("Jason Todd")
	f.book("sn-zero", "A Death in the Family", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: 0, Name: "Dick Grayson"}
	}, nil, nil)
	f.book("sn-dangle", "A Lonely Place of Dying", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: 987654, Name: "Dick Grayson"}
	}, nil, nil)
	// Controls: the same person under a different case and spacing is no
	// contradiction, and neither is a snapshot with no name at all.
	f.book("sn-same", "Under the Red Hood", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: 0, Name: "  jason  TODD "}
	}, nil, nil)
	f.book("sn-blank", "Red Hood and the Outlaws", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: 0, Name: ""}
	}, nil, nil)

	for _, apply := range []bool{false, true} {
		res, err := f.run(creditsBackfillParams{Apply: apply}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := cbClassIDs(res, "author_id_snapshot_conflict"); !slices.Equal(got, []string{"sn-dangle", "sn-zero"}) {
			t.Fatalf("apply=%v: author_id_snapshot_conflict = %v, want [sn-dangle sn-zero]", apply, got)
		}
		for _, id := range []string{"sn-zero", "sn-dangle"} {
			for _, key := range []string{cbAuthorFillFromAuthorID, cbAuthorSnapshotRefreshed} {
				if cbHasID(res, key, id) {
					t.Errorf("apply=%v: %s is in %s: its filing author would flip", apply, id, key)
				}
			}
		}
	}
	for id, want := range map[string]database.Author{
		"sn-zero":   {ID: 0, Name: "Dick Grayson"},
		"sn-dangle": {ID: 987654, Name: "Dick Grayson"},
	} {
		st := f.state(id)
		if len(st.Authors) != 0 {
			t.Errorf("%s was written: join %v", id, cbAuthorIDs(st.Authors))
		}
		if st.Author == nil || *st.Author != want {
			t.Errorf("%s snapshot = %+v, want it left as %+v", id, st.Author, want)
		}
	}
	for _, id := range []string{"sn-same", "sn-blank"} {
		if ids := cbAuthorIDs(f.state(id).Authors); !slices.Equal(ids, []int{jason}) {
			t.Errorf("%s join = %v, want it filled from AuthorID", id, ids)
		}
	}
}
