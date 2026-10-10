// file: internal/versions/live_torrent_holder_test.go
// version: 1.0.0
// guid: 98d6eb3e-9cc5-4ca2-8c9e-b947eb184761
// last-edited: 2026-10-10

package versions

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const holderTestHash = "aabbccddeeff00112233445566778899aabbccdd"

func newHolderStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestLiveTorrentHolder(t *testing.T) {
	marked := true
	cases := []struct {
		name     string
		status   string
		book     *database.Book // nil: the version's book does not exist
		wantLive bool
	}{
		{name: "active version of a live book", status: database.BookVersionStatusActive, book: &database.Book{Title: "Example Book"}, wantLive: true},
		{name: "alt version of a live book", status: database.BookVersionStatusAlt, book: &database.Book{Title: "Example Book"}, wantLive: true},
		{name: "purged version", status: database.BookVersionStatusInactivePurged, book: &database.Book{Title: "Example Book"}},
		{name: "blocked version", status: database.BookVersionStatusBlockedForRedownload, book: &database.Book{Title: "Example Book"}},
		{name: "trashed version", status: database.BookVersionStatusTrash, book: &database.Book{Title: "Example Book"}},
		{name: "stale index entry: book deleted", status: database.BookVersionStatusActive},
		{name: "book marked for deletion", status: database.BookVersionStatusActive, book: &database.Book{Title: "Example Book", MarkedForDeletion: &marked}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newHolderStore(t)
			bookID := "01JMISSINGBOOK000000000000"
			if tc.book != nil {
				b, err := store.CreateBook(tc.book)
				if err != nil {
					t.Fatalf("create book: %v", err)
				}
				bookID = b.ID
			}
			v, err := store.CreateBookVersion(&database.BookVersion{
				BookID: bookID, Status: tc.status, Format: "m4b", Source: "deluge", TorrentHash: holderTestHash,
			})
			if err != nil {
				t.Fatalf("create version: %v", err)
			}

			got, err := LiveTorrentHolder(store, holderTestHash)
			if err != nil {
				t.Fatalf("LiveTorrentHolder: %v", err)
			}
			if tc.wantLive {
				if got == nil || got.ID != v.ID {
					t.Fatalf("want holder %s, got %+v", v.ID, got)
				}
				return
			}
			if got != nil {
				t.Fatalf("want no live holder, got version %s (%s)", got.ID, got.Status)
			}
		})
	}
}

func TestLiveTorrentHolder_UnknownAndEmptyHash(t *testing.T) {
	store := newHolderStore(t)
	for _, h := range []string{"", holderTestHash} {
		got, err := LiveTorrentHolder(store, h)
		if err != nil || got != nil {
			t.Errorf("hash %q: want (nil, nil), got (%+v, %v)", h, got, err)
		}
	}
}

func TestLiveTorrentHolder_ReadErrorIsReturned(t *testing.T) {
	st := &database.MockStore{
		GetBookVersionByTorrentHashFunc: func(string) (*database.BookVersion, error) {
			return nil, errors.New("synthetic index failure")
		},
	}
	if _, err := LiveTorrentHolder(st, holderTestHash); err == nil {
		t.Fatal("an index read error must be returned, not read as 'no holder'")
	}
}
