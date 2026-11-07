// file: core/entity/entity_test.go
// version: 1.0.0
// guid: 064c459b-7a79-4f1c-9f27-b93eaddc0ac5
// last-edited: 2026-11-07

package entity

import "testing"

func TestRefString(t *testing.T) {
	r := Ref{Module: "library", Kind: "book", ID: "batman-year-one"}
	if got, want := r.String(), "library/book/batman-year-one"; got != want {
		t.Fatalf("Ref.String() = %q, want %q", got, want)
	}
}
