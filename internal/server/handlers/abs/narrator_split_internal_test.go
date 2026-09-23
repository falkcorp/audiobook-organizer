// file: internal/server/handlers/abs/narrator_split_internal_test.go
// version: 1.0.0
// guid: fad820b8-36c7-4108-a80e-6bcf9554b61e
// last-edited: 2026-09-22

package abs

import (
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func strp(s string) *string { return &s }

// 2026-09-22: a book whose cast came from a metadata provider has no junction
// rows and a ", "-joined Narrator column. The app showed ONE narrator named
// "Dorrie Sacks, Justin Thomas James, …". The column tier must yield people.
func TestResolveNarratorTiers_LegacyColumnSplitsIntoPeople(t *testing.T) {
	col := "Dorrie Sacks, Justin Thomas James, Gary Furlong, Ryan H. Reid, " +
		"Christopher Ragland, Tiana Camacho, Andrea Parsneau, Jeff Hays"
	got := resolveNarratorTiers(nil, nil, strp(col))
	want := []string{"Dorrie Sacks", "Justin Thomas James", "Gary Furlong", "Ryan H. Reid",
		"Christopher Ragland", "Tiana Camacho", "Andrea Parsneau", "Jeff Hays"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// "Surname, Given" is one person on both string tiers; splitting it would mint
// two phantom narrators.
func TestResolveNarratorTiers_SurnameFirstStaysOnePerson(t *testing.T) {
	for _, tc := range []struct {
		name         string
		json, column *string
	}{
		{"column", nil, strp("Le Guin, Ursula")},
		{"bare-string json", strp("Le Guin, Ursula"), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveNarratorTiers(nil, tc.json, tc.column)
			if !reflect.DeepEqual(got, []string{"Le Guin, Ursula"}) {
				t.Fatalf("got %q, want one person", got)
			}
		})
	}
}

// Other separators the shared splitter understands also split on the column.
func TestResolveNarratorTiers_LegacyColumnAmpersand(t *testing.T) {
	got := resolveNarratorTiers(nil, nil, strp("Kate Reading & Michael Kramer"))
	if !reflect.DeepEqual(got, []string{"Kate Reading", "Michael Kramer"}) {
		t.Fatalf("got %q", got)
	}
}

// The junction still wins outright; the column is not consulted.
func TestResolveNarratorTiers_JunctionStillWins(t *testing.T) {
	got := resolveNarratorTiers([]database.Narrator{{Name: "Only Junction"}}, nil, strp("A, B, C"))
	if !reflect.DeepEqual(got, []string{"Only Junction"}) {
		t.Fatalf("got %q", got)
	}
}
