// file: internal/plugins/maintenance/credits_backfill_respell_case_test.go
// version: 1.0.0
// guid: 0f0c6a58-2f1c-4c53-8d0e-31b2f6d7a9b4
// last-edited: 2026-10-27

package maintenance

import (
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The respelling of a credit reaches every piece the cleaner reads as the
// person. The cleaner merges pieces by util.NormalizeAuthor (case and runs of
// spaces), so "Renee Montoya, RENEE MONTOYA" is one person named twice, and a
// respelling that only matched the exact piece left the other as written and
// the read-back refused the book.
// Fixtures: Batman characters as people, Batman storylines as titles.

func TestCreditsBackfill_RespellReachesEveryCaseVariantOfThePerson(t *testing.T) {
	pair := [][2]string{{"Renee Montoya", "Renée Montoya"}}
	for _, tc := range []struct{ name, text, want string }{
		{"two cases", "Renee Montoya, RENEE MONTOYA", "Renée Montoya, Renée Montoya"},
		// A variant can differ in byte length from the pair's old name (a doubled
		// space): each occurrence is replaced over its own bytes, not over the
		// length of the name the pair asked for.
		{"own length each", "RENEE MONTOYA, Read by renee  montoya", "Renée Montoya, Read by Renée Montoya"},
		{"another person is left alone", "Renee Montoya, Harvey Bullock", "Renée Montoya, Harvey Bullock"},
		{"a longer name is not the person", "Renee Montoya-Sawyer, RENEE MONTOYA", "Renee Montoya-Sawyer, Renée Montoya"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := respellCreditText(tc.text, pair); got != tc.want {
				t.Errorf("respellCreditText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// Through the plan and the read-back: a book whose column names the person in
// two cases is linked to the one accented record, the column is respelled in
// both pieces, and the book plans clean afterwards.
func TestCreditsBackfill_BookNamingThePersonInTwoCasesLinksToTheAccentedRecordAndPlansClean(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.book("cv", "Gotham Central: Unresolved", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya, RENEE MONTOYA")
	}, ba("cv", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"cv"}) {
		t.Fatalf("%s = %v, want [cv] (refused as %v)", cbNarratorFillFromColumn, got,
			cbClassIDs(res, cbNarratorSpellingUnreconcilable))
	}
	if got := cbStr(f.state("cv").Narrator, ""); got != "Renée Montoya, Renée Montoya" {
		t.Errorf("column = %q, want both pieces respelled", got)
	}
	if ids := cbNarratorIDs(f.state("cv").Narrators); len(ids) != 1 {
		t.Errorf("junction %v, want the one record", ids)
	}
	assertPlansClean(t, f, "cv")
}
