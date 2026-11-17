// file: internal/applygate/title_roles_test.go
// version: 1.0.1
// guid: 3f8a21c6-7d54-4e09-b1a3-c5e96d20f748
// last-edited: 2026-11-17

package applygate

import (
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// A form of the candidate may be built from the candidate's credit (its author
// or narrator) and a form of the book from the book's series name. Equal text
// is not equal meaning: "Owls" + "Dark Hush" (a title and a narrator) is the
// text "owls dark hush", which is also the book's series "Owls Dark" in front
// of the title "Hush". The first version of the credit forms let that match, so
// two different books agreed. A credit is a credit and a series is a series: a
// form with a credit in it never equals a form with a series in it.

func titleOnlyBook(title string, series string) database.Book {
	b := database.Book{Title: title, Author: &database.Author{Name: "Bruce Wayne"}}
	if series != "" {
		b.Series = &database.Series{Name: series}
	}
	return b
}

// TestCheckTitle_ACreditNeverMatchesASeries is the reproduction of the
// collision, each pair one that must not agree.
func TestCheckTitle_ACreditNeverMatchesASeries(t *testing.T) {
	for _, c := range []struct {
		name string
		book database.Book
		cand metafetch.MetadataCandidate
	}{
		{"narrator after the title spells series then title",
			titleOnlyBook("Hush", "Owls Dark"),
			metafetch.MetadataCandidate{Title: "Owls", Narrator: "Dark Hush", Author: "Bruce Wayne"}},
		{"author after the title spells series then title",
			titleOnlyBook("Hush", "Owls Dark"),
			metafetch.MetadataCandidate{Title: "Owls", Author: "Dark Hush"}},
		{"narrator before the title spells series then title",
			titleOnlyBook("Hush Dark", "Owls"),
			metafetch.MetadataCandidate{Title: "Dark", Narrator: "Owls Hush", Author: "Bruce Wayne"}},
		// The collision AddedTextNeverAgrees printed: the narrator is the book's
		// series, and the candidate adds a subtitle the credit form leaves out.
		{"the printed pair: narrator equals the book's series, subtitle added",
			titleOnlyBook("Riddler", "Owls Dark"),
			metafetch.MetadataCandidate{Title: "Riddler", Subtitle: "Mirage", Series: "Hush 2", Narrator: "Owls Dark", Author: "Bruce Wayne"}},
		// The forms that carry the series and the position as well as a credit:
		// "S N T X" and "X S N T". Each pair is built so the title beside the
		// credit is not itself the stored title, so only the long form could
		// match, and only by letting a credit stand for part of a series name.
		{"series, position, title, then the narrator spells the book's series then its title",
			titleOnlyBook("Returns", "Dark 2 Hush"),
			metafetch.MetadataCandidate{Title: "Hush", Series: "Dark", SeriesPosition: "2", Narrator: "Returns", Author: "Bruce Wayne"}},
		{"the narrator, then series, position and title spells the book's series then its title",
			titleOnlyBook("2 Hush", "Owls Dark"),
			metafetch.MetadataCandidate{Title: "Hush", Series: "Dark", SeriesPosition: "2", Narrator: "Owls", Author: "Bruce Wayne"}},
		{"the same with the author as the credit",
			titleOnlyBook("2 Hush", "Owls Dark"),
			metafetch.MetadataCandidate{Title: "Hush", Series: "Dark", SeriesPosition: "2", Author: "Owls"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := checkTitle(&c.book, &c.cand)
			if got.Outcome == OutcomeAgree {
				t.Fatalf("a credit matched a series: AGREE (%s): book %q series %q, candidate %q credits %q/%q",
					got.Detail, c.book.Title, seriesName(&c.book), c.cand.Title, c.cand.Author, c.cand.Narrator)
			}
		})
	}
}
