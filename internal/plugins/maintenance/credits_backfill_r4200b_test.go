// file: internal/plugins/maintenance/credits_backfill_r4200b_test.go
// version: 1.0.0
// guid: 455a0c2b-ac28-4cc4-8d6c-c371cb613ece
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Tests for the adversarial re-review of PR #4200 (round b). Fixtures: Batman
// characters and storylines only.

// A snapshot that names a join member other than AuthorID is a second signal
// against AuthorID whether or not it carries a usable id: with id 0, or an id
// that resolves to no record, the name is all that is left of who the book is
// filed under, and it is compared with the join members' record names. Before,
// only a snapshot with a resolvable id was held, so the filing author flipped
// and the refresh overwrote the snapshot, the only evidence.
func TestCreditsBackfillR4200b_UnresolvableSnapshotNamingAJoinMemberIsHeldNotWritten(t *testing.T) {
	type fixture struct {
		name string
		snap func(dick int) *database.Author
		// tied: the join holds Dick and AuthorID at one position (the tie-break
		// site) instead of Dick alone (the prepend site).
		tied bool
	}
	for _, tc := range []fixture{
		{"id 0, prepend site", func(int) *database.Author { return &database.Author{ID: 0, Name: "Dick Grayson"} }, false},
		{"dangling id, prepend site", func(int) *database.Author { return &database.Author{ID: 987654, Name: "Dick Grayson"} }, false},
		{"id 0, name spelled loosely", func(int) *database.Author { return &database.Author{ID: 0, Name: "  dick  grayson "} }, false},
		{"id 0, tie-break site", func(int) *database.Author { return &database.Author{ID: 0, Name: "Dick Grayson"} }, true},
		{"dangling id, tie-break site", func(int) *database.Author { return &database.Author{ID: 987654, Name: "Dick Grayson"} }, true},
		{"resolvable id (control)", func(dick int) *database.Author { return &database.Author{ID: dick, Name: "Dick Grayson"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCBFixture(t)
			jason := f.author("Jason Todd")
			dick := f.author("Dick Grayson")
			join := ba("bk", [2]int{dick, 0})
			if tc.tied {
				join = ba("bk", [2]int{dick, 0}, [2]int{jason, 0})
			}
			f.book("bk", "A Lonely Place of Dying", func(b *database.Book) {
				b.AuthorID = intp(jason)
				b.Author = tc.snap(dick)
			}, join, nil)
			before := f.state("bk")

			dry, err := f.run(creditsBackfillParams{}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !cbHasID(dry, cbAuthorIDSnapshotConflict, "bk") {
				t.Errorf("dry run: author_id_snapshot_conflict = %v, want [bk]", cbClassIDs(dry, cbAuthorIDSnapshotConflict))
			}
			if got := cbClassIDs(dry, cbFilingAuthorChanges); len(got) != 0 {
				t.Errorf("dry run: filing_author_changes = %v: the held book would still flip its filing author", got)
			}
			if _, err := f.run(creditsBackfillParams{Apply: true}, 1); err != nil {
				t.Fatal(err)
			}
			cbUntouched(t, f, "bk", before)
		})
	}
}

// The name comparison is not a name lookup and holds only a contradiction: a
// nameless snapshot that names AuthorID itself, an author outside the join or
// nobody at all is no second signal, and the plan's rule stands (AuthorID is
// prepended).
func TestCreditsBackfillR4200b_UnresolvableSnapshotThatContradictsNothingIsStillPrepended(t *testing.T) {
	for name, snap := range map[string]*database.Author{
		"names AuthorID itself": {ID: 0, Name: "Jason Todd"},
		"names an outsider":     {ID: 0, Name: "Tim Drake"},
		"blank name":            {ID: 0, Name: "   "},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCBFixture(t)
			jason := f.author("Jason Todd")
			dick := f.author("Dick Grayson")
			f.author("Tim Drake")
			f.book("bk", "A Death in the Family", func(b *database.Book) {
				b.AuthorID = intp(jason)
				b.Author = snap
			}, ba("bk", [2]int{dick, 0}), nil)
			res, err := f.run(creditsBackfillParams{Apply: true}, 1)
			if err != nil {
				t.Fatal(err)
			}
			if cbHasID(res, cbAuthorIDSnapshotConflict, "bk") {
				t.Fatalf("held as a snapshot conflict although the snapshot contradicts nothing")
			}
			if got := cbAuthorIDs(f.state("bk").Authors); !slices.Equal(got, []int{jason, dick}) {
				t.Errorf("join = %v, want [%d %d] (AuthorID prepended)", got, jason, dick)
			}
		})
	}
}

// A narrator refused in settleNewSpellings loses its narrator side only. The
// same book alone in the cohort is written on both sides; it must not lose its
// author-side fill because two cohort siblings happen to spell the new
// narrator differently.
func TestCreditsBackfillR4200b_SpellingRefusalKeepsTheBooksAuthorSideWrite(t *testing.T) {
	setup := func(withSiblings bool) (*cbFixture, int) {
		f := newCBFixture(t)
		bruce := f.author("Bruce Wayne")
		lucius := f.author("Lucius Fox")
		if withSiblings {
			for _, id := range []string{"r1-a", "r1-b"} {
				f.book(id, "Gotham Central "+id, func(b *database.Book) {
					b.AuthorID = intp(bruce)
					b.Narrator = strp("Renee Montoya")
				}, ba(id, [2]int{bruce, 0}), nil)
			}
		}
		f.book("r1-c", "Gotham Central: Daylight", func(b *database.Book) {
			b.AuthorID = intp(lucius)
			b.NarratorsJSON = strp(`["Read by Renée Montoya"]`)
		}, nil, nil)
		return f, lucius
	}

	// Control: alone, r1-c is written on both sides.
	f0, lucius0 := setup(false)
	res0, err := f0.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ids := cbAuthorIDs(f0.state("r1-c").Authors); !slices.Equal(ids, []int{lucius0}) {
		t.Fatalf("control: join = %v, want [%d]", ids, lucius0)
	}
	if !cbHasID(res0, cbNarratorFillFromNarratorsJSON, "r1-c") {
		t.Fatalf("control: the narrator side was not written: %v", cbClassIDs(res0, cbNarratorFillFromNarratorsJSON))
	}

	for _, apply := range []bool{false, true} {
		f, lucius := setup(true)
		before := f.state("r1-c")
		res, err := f.run(creditsBackfillParams{Apply: apply}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if !cbHasID(res, cbNarratorSpellingJSONConflict, "r1-c") {
			t.Fatalf("apply=%v: r1-c is not refused as narrator_spelling_json_conflict (%v): the fixture proves nothing",
				apply, cbClassIDs(res, cbNarratorSpellingJSONConflict))
		}
		if !cbHasID(res, cbAuthorFillFromAuthorID, "r1-c") {
			t.Errorf("apply=%v: r1-c is not listed under author_fill_from_author_id: the refusal dropped its author side", apply)
		}
		if cbHasID(res, cbNarratorFillFromNarratorsJSON, "r1-c") {
			t.Errorf("apply=%v: r1-c is listed as a narrator fill although its narrator side was refused", apply)
		}
		if !apply {
			cbUntouched(t, f, "r1-c", before)
			continue
		}
		got := f.state("r1-c")
		if ids := cbAuthorIDs(got.Authors); !slices.Equal(ids, []int{lucius}) {
			t.Errorf("apply: r1-c join = %v, want [%d]: the author-side fill was dropped", ids, lucius)
		}
		if len(got.Narrators) != 0 || cbStr(got.Narrator, "") != "" || cbStr(got.NarratorsJSON, "") != `["Read by Renée Montoya"]` {
			t.Errorf("apply: r1-c narrator side was written: rows %v column %q json %q",
				got.Narrators, cbStr(got.Narrator, ""), cbStr(got.NarratorsJSON, ""))
		}
	}
}

// A book whose only write was the refused narrator side leaves the cohort, as
// before: nothing is written and the refusal is the book's only class.
func TestCreditsBackfillR4200b_SpellingRefusalOfANarratorOnlyBookWritesNothing(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for _, id := range []string{"a", "b"} {
		f.book(id, "Gotham Central "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp("Renee Montoya")
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	f.book("c", "Gotham Central: Daylight", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.NarratorsJSON = strp(`["Read by Renée Montoya"]`)
	}, ba("c", [2]int{bruce, 0}), nil)
	before := f.state("c")
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cbHasID(res, cbNarratorSpellingJSONConflict, "c") {
		t.Fatalf("c is not refused: %v", cbClassIDs(res, cbNarratorSpellingJSONConflict))
	}
	cbUntouched(t, f, "c", before)
	if res.BooksToWrite != 2 {
		t.Errorf("books_to_write = %d, want 2 (a and b)", res.BooksToWrite)
	}
}

// The spelling the run gives a new narrator record is decided over every
// eligible book, not over the slice a limit cut: a full dry run, a first cohort
// and the cohort after it all agree, and no later cohort respells what the
// first wrote.
func TestCreditsBackfillR4200b_NewNarratorSpellingIsCountedOverTheWholeLibraryNotTheCohort(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for id, n := range map[string]string{"g-1": "Renee Montoya", "g-2": "Renée Montoya", "g-3": "Renée Montoya"} {
		f.book(id, "No Man's Land "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp(n)
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbNarratorColumnRespelled); !slices.Equal(got, []string{"g-1"}) {
		t.Fatalf("full dry run: narrator_column_respelled = %v, want [g-1]", got)
	}
	// The dry run of the first cohort alone predicts the same record.
	dry1, err := f.run(creditsBackfillParams{Limit: 1}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry1, cbNarratorColumnRespelled); !slices.Equal(got, []string{"g-1"}) {
		t.Errorf("limit-1 dry run: narrator_column_respelled = %v, want [g-1]: the cohort counted only itself", got)
	}
	if _, err := f.runWith(context.Background(), &cbReporter{id: "01COHORT1"}, creditsBackfillParams{Apply: true, Limit: 1}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runWith(context.Background(), &cbReporter{id: "01COHORT2"}, creditsBackfillParams{Apply: true}, 2); err != nil {
		t.Fatal(err)
	}
	all, err := f.s.ListNarrators()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range all {
		names = append(names, n.Name)
	}
	if !slices.Equal(names, []string{"Renée Montoya"}) {
		t.Errorf("narrator records after the cohorts = %v, want [Renée Montoya] (the full dry run's spelling)", names)
	}
	for _, id := range []string{"g-1", "g-2", "g-3"} {
		if got := cbStr(f.state(id).Narrator, ""); got != "Renée Montoya" {
			t.Errorf("%s narrator column = %q, want Renée Montoya", id, got)
		}
	}
}

// Respelling a moved name lands on the credit that names the person, not on
// the first text that merely contains the name.
func TestCreditsBackfillR4200b_RespellingNeverLandsInsideAnotherName(t *testing.T) {
	pair := [][2]string{{"Renee Montoya", "Renée Montoya"}}
	for _, tc := range []struct{ name, text, want string }{
		{"hyphenated surname first", "Renee Montoya-Sawyer and Renee Montoya", "Renee Montoya-Sawyer and Renée Montoya"},
		{"hyphenated surname last", "Renee Montoya and Renee Montoya-Sawyer", "Renée Montoya and Renee Montoya-Sawyer"},
		{"longer name first", "Anne Renee Montoya and Renee Montoya", "Anne Renee Montoya and Renée Montoya"},
		{"possessive is another word", "Renee Montoya's Choice, Renee Montoya", "Renee Montoya's Choice, Renée Montoya"},
		{"read by prefix", "Read by Renee Montoya", "Read by Renée Montoya"},
		{"role credit", "Renee Montoya - translator and Bruce Wayne", "Renée Montoya - translator and Bruce Wayne"},
		{"only inside a longer name", "Renee Montoya-Sawyer", "Renee Montoya-Sawyer"},
		{"absent", "Bruce Wayne", "Bruce Wayne"},
	} {
		if got := respellCreditText(tc.text, pair); got != tc.want {
			t.Errorf("%s: respellCreditText(%q) = %q, want %q", tc.name, tc.text, got, tc.want)
		}
	}
	if got := respellCreditText("Bruce Wayne", [][2]string{{"", "Renée Montoya"}}); got != "Bruce Wayne" {
		t.Errorf("an empty old name changed the text: %q", got)
	}
	raw := `["Renee Montoya-Sawyer", "Renee Montoya"]`
	if got, want := respellNarratorsJSON(raw, pair), `["Renee Montoya-Sawyer","Renée Montoya"]`; got != want {
		t.Errorf("respellNarratorsJSON(%s) = %s, want %s", raw, got, want)
	}
}

// A moved name that is the start of another credited name is respelled on the
// right credit, so the book is written instead of being refused as
// narrator_spelling_unreconcilable.
func TestCreditsBackfillR4200b_NameThatPrefixesAnotherCreditedNameIsNotAFalseRefusal(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.narrator("Renee Montoya-Sawyer")
	f.book("r3", "Gotham Central: Corrigan II", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya-Sawyer and Renee Montoya")
	}, ba("r3", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorSpellingUnreconcilable); len(got) != 0 {
		t.Errorf("narrator_spelling_unreconcilable = %v, want none", got)
	}
	if want, got := "Renee Montoya-Sawyer and Renée Montoya", cbStr(f.state("r3").Narrator, ""); got != want {
		t.Errorf("column = %q, want %q", got, want)
	}
}

// The same inside NarratorsJSON: the entry that is the name is respelled, the
// entry that merely contains it is not, and the book is written.
func TestCreditsBackfillR4200b_JSONEntryContainingTheNameIsNotRespelled(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.narrator("Renee Montoya-Sawyer")
	f.book("r4", "Gotham Central: Corrigan III", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya and Renee Montoya-Sawyer")
		b.NarratorsJSON = strp(`["Renee Montoya-Sawyer", "Renee Montoya"]`)
	}, ba("r4", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	st := f.state("r4")
	if got := cbStr(st.NarratorsJSON, ""); got == `["Renée Montoya-Sawyer","Renee Montoya"]` {
		t.Fatalf("corrupted JSON written: %q", got)
	}
	if got := cbClassIDs(res, cbNarratorSpellingJSONConflict); len(got) != 0 {
		t.Errorf("narrator_spelling_json_conflict = %v, want none", got)
	}
	if want, got := `["Renee Montoya-Sawyer","Renée Montoya"]`, cbStr(st.NarratorsJSON, ""); got != want {
		t.Errorf("NarratorsJSON = %q, want %q", got, want)
	}
	if want, got := "Renée Montoya and Renee Montoya-Sawyer", cbStr(st.Narrator, ""); got != want {
		t.Errorf("column = %q, want %q", got, want)
	}
}
