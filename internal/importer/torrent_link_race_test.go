// file: internal/importer/torrent_link_race_test.go
// version: 1.0.0
// guid: 6e2a91c4-0b7d-4f53-a8e6-3d15c9b7f402
// last-edited: 2026-10-10

package importer

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/versions"
)

// raceStore is a real PebbleStore that parks the FIRST CreateBookVersion
// until a second CreateBookVersion call arrives (or a short grace period
// passes), forcing the doubled-import interleaving deterministically: the
// first import has passed every already-linked check and is about to write
// the torrent index when the second import runs.
//
// Without a lock spanning guard-to-version the second import passes its own
// checks, reaches CreateBookVersion, and releases the first. With the lock it
// cannot get that far until the first has finished, so the parked call waits
// out the grace period instead.
type raceStore struct {
	*database.PebbleStore
	creates     atomic.Int32
	secondEntry chan struct{}
	firstParked chan struct{}
}

func (s *raceStore) CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error) {
	switch s.creates.Add(1) {
	case 1:
		close(s.firstParked)
		select {
		case <-s.secondEntry:
		case <-time.After(300 * time.Millisecond):
		}
	case 2:
		close(s.secondEntry)
	}
	return s.PebbleStore.CreateBookVersion(v)
}

// Two concurrent imports of the same torrent (a double click, or a proxy retry
// while the first is still running) must link it to exactly one book. The
// second is imported unlinked with TorrentLinkNoteAlreadyLinked, the outcome
// the sequential retry already had. Before the per-torrent lock, both passed
// the already-linked guard while the first was still hashing and creating,
// both versions carried the hash, and the index ended up on the second book
// while the first book's row still claimed the torrent.
func TestImportFile_ConcurrentSameTorrentLinksOnce(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := &raceStore{
		PebbleStore: pebbleImportStore(t, dir),
		secondEntry: make(chan struct{}),
		firstParked: make(chan struct{}),
	}
	is := NewImportService(store)

	var (
		wg    sync.WaitGroup
		resps [2]*ImportFileResponse
		errs  [2]error
	)
	run := func(i int) {
		defer wg.Done()
		resps[i], errs[i] = is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	}
	wg.Add(1)
	go run(0)
	<-store.firstParked
	wg.Add(1)
	go run(1)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("import %d failed: %v", i, err)
		}
	}
	var linked, refused []*ImportFileResponse
	for _, r := range resps {
		switch {
		case r.TorrentLinked:
			linked = append(linked, r)
		case r.TorrentLinkNote == TorrentLinkNoteAlreadyLinked:
			refused = append(refused, r)
		}
	}
	if len(linked) != 1 || len(refused) != 1 {
		t.Fatalf("want exactly 1 linked and 1 already-linked import, got linked=%d refused=%d (%+v, %+v)",
			len(linked), len(refused), resps[0], resps[1])
	}

	holder, err := store.PebbleStore.GetBookVersionByTorrentHash(testTorrentHash)
	if err != nil || holder == nil {
		t.Fatalf("torrent index: %v (err %v)", holder, err)
	}
	if holder.BookID != linked[0].ID {
		t.Errorf("torrent index names book %q, want the linked book %q", holder.BookID, linked[0].ID)
	}
	if row := storedImportRow(t, store.PebbleStore, refused[0].ID); row.DelugeHash != "" {
		t.Errorf("the unlinked book's row must not claim the torrent: DelugeHash = %q", row.DelugeHash)
	}
	if row := storedImportRow(t, store.PebbleStore, linked[0].ID); row.DelugeHash != testTorrentHash || row.VersionID != holder.ID {
		t.Errorf("linked row: DelugeHash=%q VersionID=%q, want %q and %q", row.DelugeHash, row.VersionID, testTorrentHash, holder.ID)
	}
}

// failingFileStore is a real PebbleStore whose CreateBookFile always fails.
type failingFileStore struct{ *database.PebbleStore }

func (s *failingFileStore) CreateBookFile(*database.BookFile) error {
	return errors.New("synthetic write failure")
}

// A failed book_file write keeps the version, and with it the torrent index
// entry. That entry is the only thing that turns a retried discover import
// into a 409 instead of a second book (every ImportFile mints a new book), so
// it is the reason the version is not rolled back with the row.
func TestImportFile_FileWriteFailureKeepsTorrentIndexForRetry(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := &failingFileStore{PebbleStore: pebbleImportStore(t, dir)}
	is := NewImportService(store)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("a book_file failure must not fail the import: %v", err)
	}
	if resp.TorrentLinked || resp.TorrentLinkNote != TorrentLinkNoteFileFailed {
		t.Fatalf("want unlinked with note %q, got linked=%v note=%q", TorrentLinkNoteFileFailed, resp.TorrentLinked, resp.TorrentLinkNote)
	}
	holder, err := versions.LiveTorrentHolder(store.PebbleStore, testTorrentHash)
	if err != nil {
		t.Fatalf("LiveTorrentHolder: %v", err)
	}
	if holder == nil || holder.BookID != resp.ID {
		t.Fatalf("the torrent index must still name the imported book %q so a retry is refused, got %+v", resp.ID, holder)
	}
}
