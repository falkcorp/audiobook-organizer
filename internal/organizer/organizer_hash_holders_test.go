// file: internal/organizer/organizer_hash_holders_test.go
// version: 1.0.0
// guid: 5b2e8d94-1c37-4a60-9e8f-d03a7c61b425
// last-edited: 2026-10-22

package organizer

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The organizer's same-hash check lists EVERY holder of the hash and acts on
// the first one that is another live book inside the library root. These pin
// the parts of that rule the library-copy tests do not: which holders are
// skipped, and that a store which cannot list the holders fails the organize.

// A trashed holder is not a library book to collide with.
func TestOrganizeBook_TrashedHashTwinIsNotACollision(t *testing.T) {
	eachIDOrder(t, func(t *testing.T, order idOrder) {
		f := newLibraryCopyFixture(t, "vg-unrelated", order)
		trashed := true
		now := time.Now()
		if _, err := f.store.ModifyBook(f.libCopy.ID, func(b *database.Book) error {
			b.MarkedForDeletion, b.MarkedForDeletionAt = &trashed, &now
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		spy := &collisionSpy{}

		_, _, err := f.organizer(spy).OrganizeBook(f.original)

		if err != nil && strings.Contains(err.Error(), "duplicate file already organized") {
			t.Fatalf("a trashed same-hash book was reported as a duplicate: %v", err)
		}
		if n := spy.count(); n != 0 {
			t.Errorf("OnCollision fired %d time(s) for a trashed holder; want 0", n)
		}
	})
}

// A holder outside the library root is not an organized copy; it is skipped, and
// the check goes on to the holder that is, whatever the order of their ids.
func TestOrganizeBook_HolderOutsideRootIsSkippedForTheOneInside(t *testing.T) {
	eachIDOrder(t, func(t *testing.T, order idOrder) {
		f := newLibraryCopyFixture(t, "vg-unrelated", order)
		// Older than both, outside the root: the first holder in id order.
		outsideID := ulid.MustNew(ulid.Timestamp(time.Date(2026, 9, 30, 11, 0, 0, 0, time.UTC)), zeroEntropy{}).String()
		outside := addInPlaceBook(t, f.store, outsideID, "Title", filepath.Join(t.TempDir(), "elsewhere", "Title.mp3"), filled(512, 3), nil, 0)
		outside.FileHash = &f.hash
		if _, err := f.store.UpdateBook(outside.ID, outside); err != nil {
			t.Fatal(err)
		}
		spy := &collisionSpy{}

		_, _, err := f.organizer(spy).OrganizeBook(f.original)

		want := "duplicate file already organized at: " + f.libCopy.FilePath
		if err == nil || err.Error() != want {
			t.Fatalf("OrganizeBook(original) = %v; want %q", err, want)
		}
		if got := spy.calls; len(got) != 1 || got[0][1] != f.libCopy.FilePath {
			t.Errorf("OnCollision calls = %v; want one for the holder inside the root", got)
		}
	})
}

// opaqueStore wraps a store without exposing its capabilities or an Unwrap, the
// shape of a decorator that hides them.
type opaqueStore struct{ OrganizerStore }

// failingHolderStore can list no holders.
type failingHolderStore struct{ OrganizerStore }

func (failingHolderStore) BooksByFileHash(string) ([]*database.Book, error) {
	return nil, errors.New("a candidate row cannot be read")
}

// A store that cannot list the holders (no capability, or a list that could not
// be completed) fails the organize: "could not look" is not "no duplicate", so
// no file is written.
func TestOrganizeBook_UnlistableHashHoldersFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(OrganizerStore) OrganizerStore
		want  string
	}{
		{"no capability", func(s OrganizerStore) OrganizerStore { return opaqueStore{s} }, database.ErrNoFileHashHolderLookup.Error()},
		{"incomplete list", func(s OrganizerStore) OrganizerStore { return failingHolderStore{s} }, "a candidate row cannot be read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLibraryCopyFixture(t, "vg-unrelated", bothIDOrders[0])
			org := f.organizer(&collisionSpy{})
			org.SetStore(tc.store(f.store))

			target, method, err := org.OrganizeBook(f.original)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("OrganizeBook = (%q, %q, %v); want an error containing %q", target, method, err, tc.want)
			}
			if target != "" || method != "" {
				t.Errorf("a failed check still returned a target (%q, %q)", target, method)
			}
		})
	}
}
