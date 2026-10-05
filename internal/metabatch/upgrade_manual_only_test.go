// file: internal/metabatch/upgrade_manual_only_test.go
// version: 1.0.0
// guid: 3f6b1d8e-27c4-4a95-b0e3-8d5c2f7a1e46
// last-edited: 2026-10-05

package metabatch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The metadata upgrade's owner-manual check is the full bulk guard: a
// franchise tag, an author credit or a narrator album holds a book whose
// path and title name nothing; a tag read failure fails closed.
func TestUpgradeIsOwnerManualOnly_FullGuard(t *testing.T) {
	neutral := func() *database.Book { return &database.Book{ID: "b", Title: "Gift of the Gods", FilePath: "/lib/x"} }
	svc := func(m *database.MockStore) *MetadataUpgradeService { return &MetadataUpgradeService{DB: m} }

	held, err := svc(&database.MockStore{GetBookTagsDetailedFunc: func(string) ([]database.BookTag, error) {
		return []database.BookTag{{Tag: "franchise:doctor-who", Source: "franchise-matcher:op"}}, nil
	}}).isOwnerManualOnly(neutral())
	if err != nil || !held {
		t.Errorf("tagged book: held=%v err=%v", held, err)
	}

	id := 3
	b := neutral()
	b.AuthorID = &id
	held, err = svc(&database.MockStore{GetAuthorByIDFunc: func(int) (*database.Author, error) {
		return &database.Author{ID: 3, Name: "Big Finish Productions"}, nil
	}}).isOwnerManualOnly(b)
	if err != nil || !held {
		t.Errorf("author credit: held=%v err=%v", held, err)
	}

	b = neutral()
	n := "The War Master - Series 12"
	b.Narrator = &n
	if held, err = svc(&database.MockStore{}).isOwnerManualOnly(b); err != nil || !held {
		t.Errorf("narrator album: held=%v err=%v", held, err)
	}

	if held, err = svc(&database.MockStore{}).isOwnerManualOnly(neutral()); err != nil || held {
		t.Errorf("neutral book: held=%v err=%v", held, err)
	}

	_, err = svc(&database.MockStore{GetBookTagsDetailedFunc: func(string) ([]database.BookTag, error) {
		return nil, errors.New("down")
	}}).isOwnerManualOnly(neutral())
	if err == nil {
		t.Error("tag read failure must fail closed")
	}
}
