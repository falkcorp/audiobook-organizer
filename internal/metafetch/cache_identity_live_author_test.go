// file: internal/metafetch/cache_identity_live_author_test.go
// version: 1.0.0
// guid: 2a7f9c14-6b3e-4d85-a0c2-9e1d5b8f3a76
// last-edited: 2026-09-27

package metafetch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The identity leg accepts a row hashed from ANY form of the book's current
// author: the Book.Author snapshot (the batch fetch) or the live author (the
// UI search, which sends the API's author_name). A row hashed from an author
// the book does not have still fails closed.
func TestValidateCachedIdentityForBook_LiveAuthorForms(t *testing.T) {
	mfs := &Service{}
	book := &database.Book{ID: "valis", Title: "Valis"} // nil snapshot
	live := []string{"Philip K. Dick"}

	cases := []struct {
		name   string
		hash   string
		live   []string
		wantOK bool
	}{
		{"batch shape, snapshot author (nil)", hashSearchInputs("valis", "Valis", "", "", ""), live, true},
		{"batch shape, live author", hashSearchInputs("valis", "Valis", "Philip K. Dick", "", ""), live, true},
		{"UI shape, live author", hashSearchInputs("valis", "Valis", "Philip K. Dick", "", ""), live, true},
		{"live author row, no live authors given", hashSearchInputs("valis", "Valis", "Philip K. Dick", "", ""), nil, false},
		{"another author", hashSearchInputs("valis", "Valis", "Stephen King", "", ""), live, false},
		{"another title", hashSearchInputs("valis", "Ubik", "Philip K. Dick", "", ""), live, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mfs.ValidateCachedIdentityForBook(&MetadataCandidateCache{BookID: "valis", SourceHash: c.hash}, book, c.live)
			if c.wantOK && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !c.wantOK && !errors.Is(err, ErrStaleMetadataCache) {
				t.Fatalf("err = %v, want ErrStaleMetadataCache", err)
			}
		})
	}
}

// A co-authored book's author_name is every live author joined with " & ".
func TestValidateCachedIdentityForBook_JoinedLiveAuthors(t *testing.T) {
	mfs := &Service{}
	book := &database.Book{ID: "go", Title: "Good Omens", Author: &database.Author{Name: "Unknown Author"}}
	entry := &MetadataCandidateCache{BookID: "go", SourceHash: hashSearchInputs("go", "Good Omens", "Terry Pratchett & Neil Gaiman", "", "")}
	if err := mfs.ValidateCachedIdentityForBook(entry, book, []string{"Terry Pratchett", "Neil Gaiman"}); err != nil {
		t.Fatalf("joined live authors: %v", err)
	}
	// The stale snapshot still passes its own rows: nothing that passed
	// before this change fails now.
	entry.SourceHash = hashSearchInputs("go", "Good Omens", "Unknown Author", "", "")
	if err := mfs.ValidateCachedIdentityForBook(entry, book, []string{"Terry Pratchett", "Neil Gaiman"}); err != nil {
		t.Fatalf("snapshot row: %v", err)
	}
}
