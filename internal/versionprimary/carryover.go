// file: internal/versionprimary/carryover.go
// version: 1.0.0
// guid: 5f80b709-4872-485c-875e-59b84753da2e
// last-edited: 2026-09-24

package versionprimary

import (
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Metadata carry-over: when the content winner is not the metadata-best
// eligible member, the donor's value is written into each of the winner's
// EMPTY fields. A non-empty winner value is never overwritten (owner
// decision 2026-09-24); a field where both are set and differ is reported as
// a conflict for the owner to decide later.
//
// Only plain text columns are carried. Left out on purpose:
//   - author_id and series_id: each has a second home (the book_authors join,
//     the series row and sequence) and undo needs id refs for them, so
//     copying the column alone would leave the book disagreeing with itself.
//     A gap there is reported as NotCarried.
//   - itunes_persistent_id, duration, bitrate, codec, file_path, the hashes,
//     library_state: they describe a particular FILE, not the work, and the
//     donor's file is not the winner's.

// carryField is one carried column: its JSON name (the history field name)
// and the accessor for it.
type carryField struct {
	name string
	get  func(*database.Book) **string
}

var carryFields = []carryField{
	{"description", func(b *database.Book) **string { return &b.Description }},
	{"narrator", func(b *database.Book) **string { return &b.Narrator }},
	{"publisher", func(b *database.Book) **string { return &b.Publisher }},
	{"language", func(b *database.Book) **string { return &b.Language }},
	{"genre", func(b *database.Book) **string { return &b.Genre }},
	{"cover_url", func(b *database.Book) **string { return &b.CoverURL }},
	{"isbn10", func(b *database.Book) **string { return &b.ISBN10 }},
	{"isbn13", func(b *database.Book) **string { return &b.ISBN13 }},
	{"asin", func(b *database.Book) **string { return &b.ASIN }},
	{"subtitle", func(b *database.Book) **string { return &b.Subtitle }},
	{"edition", func(b *database.Book) **string { return &b.Edition }},
}

// FieldFill is one empty winner field the donor would fill.
type FieldFill struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// FieldConflict is a field set on both, to different values. Not written.
type FieldConflict struct {
	Field  string `json:"field"`
	Winner string `json:"winner"`
	Donor  string `json:"donor"`
}

// CarryPlan is what carry-over would do for one winner/donor pair.
type CarryPlan struct {
	DonorID    string          `json:"donor_id"`
	Fills      []FieldFill     `json:"fills,omitempty"`
	Conflicts  []FieldConflict `json:"conflicts,omitempty"`
	NotCarried []string        `json:"not_carried,omitempty"`
}

// PlanCarryOver compares winner and donor field by field.
func PlanCarryOver(winner, donor *database.Book) CarryPlan {
	p := CarryPlan{DonorID: donor.ID}
	for _, f := range carryFields {
		w, d := *f.get(winner), *f.get(donor)
		switch {
		case !nonEmpty(d):
		case !nonEmpty(w):
			p.Fills = append(p.Fills, FieldFill{Field: f.name, Value: *d})
		case strings.TrimSpace(*w) != strings.TrimSpace(*d):
			p.Conflicts = append(p.Conflicts, FieldConflict{Field: f.name, Winner: *w, Donor: *d})
		}
	}
	if winner.AuthorID == nil && donor.AuthorID != nil {
		p.NotCarried = append(p.NotCarried, "author_id="+strconv.Itoa(*donor.AuthorID))
	}
	if winner.SeriesID == nil && donor.SeriesID != nil {
		p.NotCarried = append(p.NotCarried, "series_id="+strconv.Itoa(*donor.SeriesID))
	}
	return p
}

// ApplyCarryOver writes the donor's value into each of dst's fields that is
// still empty. It re-checks emptiness on dst itself, so a value another
// writer set since the plan is kept. Returns the fields it filled.
func ApplyCarryOver(dst, donor *database.Book) []string {
	var filled []string
	for _, f := range carryFields {
		d := *f.get(donor)
		if !nonEmpty(d) || nonEmpty(*f.get(dst)) {
			continue
		}
		v := *d
		*f.get(dst) = &v
		filled = append(filled, f.name)
	}
	return filled
}
