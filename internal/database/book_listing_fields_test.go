// file: internal/database/book_listing_fields_test.go
// version: 1.0.1
// guid: 8e3a5d21-7c49-4f0b-b6d8-1a9e4c7f2b05
// last-edited: 2026-10-10

package database

import (
	"fmt"
	"reflect"
	"testing"
)

// TestGetBookListingFields_MemdbMatchesPebble: the memdb path and the Pebble
// fallback answer identically, soft-deleted books included and unknown ids
// absent; a memdb known to be short falls through to Pebble.
func TestGetBookListingFields_MemdbMatchesPebble(t *testing.T) {
	p := seedTrash(t, 6, 6)
	matched := "matched"
	b, err := p.GetBookByID("b007")
	if err != nil || b == nil {
		t.Fatal(err)
	}
	b.MetadataReviewStatus = &matched
	if _, err := p.UpdateBook(b.ID, b); err != nil {
		t.Fatal(err)
	}
	ids := []string{"b000", "b003", "b007", "b011", "nope"}
	for i := range 12 {
		ids = append(ids, fmt.Sprintf("b%03d", i))
	}

	mem, err := p.GetBookListingFields(ids)
	if err != nil {
		t.Fatal(err)
	}
	p.UseMemDB = false
	peb, err := p.GetBookListingFields(ids)
	p.UseMemDB = true
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mem, peb) {
		t.Fatalf("memdb %v\npebble %v", mem, peb)
	}
	if len(mem) != 12 || mem["b007"].MetadataReviewStatus != "matched" || mem["b000"].Title != "T0" {
		t.Fatalf("unexpected fields: %v", mem)
	}

	p.mem().recordLostRows(memTableBooks, 1)
	short, err := p.GetBookListingFields(ids)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(short, peb) {
		t.Fatal("a short memdb must fall through to the Pebble answer")
	}
}
