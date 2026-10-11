// file: internal/importer/torrent_link_race_test.go
// version: 1.1.0
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
// while the first is still running) must produce exactly ONE book, linked to
// the torrent; the second is refused with ErrTorrentAlreadyLinked before it
// creates anything. Before the per-torrent lock, both passed the already-linked
// guard while the first was still hashing and creating, both versions carried
// the hash, and the index ended up on the second book while the first book's
// row still claimed the torrent. With the lock but without the refusal the
// link was safe but the second request still minted a duplicate, unlinked book
// and reported success.
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

	var linked *ImportFileResponse
	var refusals []error
	for i := range resps {
		switch {
		case errs[i] == nil && resps[i].TorrentLinked:
			if linked != nil {
				t.Fatalf("both imports linked the torrent: %+v and %+v", linked, resps[i])
			}
			linked = resps[i]
		case errors.Is(errs[i], ErrTorrentAlreadyLinked):
			refusals = append(refusals, errs[i])
		default:
			t.Fatalf("import %d: resp=%+v err=%v, want linked or ErrTorrentAlreadyLinked", i, resps[i], errs[i])
		}
	}
	if linked == nil || len(refusals) != 1 {
		t.Fatalf("want exactly 1 linked import and 1 refusal, got linked=%v refusals=%d (errs %v, %v)", linked, len(refusals), errs[0], errs[1])
	}

	holder, err := store.PebbleStore.GetBookVersionByTorrentHash(testTorrentHash)
	if err != nil || holder == nil {
		t.Fatalf("torrent index: %v (err %v)", holder, err)
	}
	if holder.BookID != linked.ID {
		t.Errorf("torrent index names book %q, want the linked book %q", holder.BookID, linked.ID)
	}
	var linkedErr *TorrentAlreadyLinkedError
	if !errors.As(refusals[0], &linkedErr) || linkedErr.BookID != linked.ID || linkedErr.VersionID != holder.ID {
		t.Errorf("the refusal must name the holder (book %q, version %q), got %+v", linked.ID, holder.ID, refusals[0])
	}
	// The point of refusing rather than importing unlinked: no second book.
	books, err := store.PebbleStore.GetAllBooksCore(0, 0)
	if err != nil {
		t.Fatalf("GetAllBooksCore: %v", err)
	}
	if len(books) != 1 || books[0].ID != linked.ID {
		t.Errorf("want exactly the linked book, got %d books: %+v", len(books), books)
	}
	if row := storedImportRow(t, store.PebbleStore, linked.ID); row.DelugeHash != testTorrentHash || row.VersionID != holder.ID {
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

// lateHolderStore plants a live holder for the torrent the moment CreateBook
// returns, i.e. AFTER the importer's already-linked guard passed and BEFORE
// CreateIngestVersion runs. That is the one interleaving the per-torrent lock
// cannot prevent: a writer that does not take it (UpdateBookVersion rewrites
// the torrent index too) makes the torrent held mid-import.
type lateHolderStore struct {
	*database.PebbleStore
	t        *testing.T
	holderID string
	bookID   string
}

func (s *lateHolderStore) CreateBook(b *database.Book) (*database.Book, error) {
	created, err := s.PebbleStore.CreateBook(b)
	if err != nil {
		return nil, err
	}
	other, err := s.PebbleStore.CreateBook(&database.Book{Title: "Other Book", FilePath: b.FilePath + ".other", Format: "m4b"})
	if err != nil {
		s.t.Fatalf("plant holder book: %v", err)
	}
	v, err := s.PebbleStore.CreateBookVersion(&database.BookVersion{
		BookID: other.ID, Status: database.BookVersionStatusActive, Format: "m4b", Source: "deluge", TorrentHash: testTorrentHash,
	})
	if err != nil {
		s.t.Fatalf("plant holder version: %v", err)
	}
	s.holderID, s.bookID = v.ID, other.ID
	return created, nil
}

// When a holder appears after the guard passed, the book already exists and can
// no longer be refused: the importer falls back to an ordinary unlinked version
// (Source "imported", no hash) and reports TorrentLinkNoteAlreadyLinked. The
// version matters: with the fallback gone the import returned a book with no
// version at all, and the row unlinked. The other book's link must be left
// alone.
func TestImportFile_HolderAppearsAfterGuardFallsBackToUnlinkedVersion(t *testing.T) {
	withSupportedExt(t)
	dir, path := stageFixture(t)
	store := &lateHolderStore{PebbleStore: pebbleImportStore(t, dir), t: t}
	is := NewImportService(store)

	resp, err := is.ImportFile(&ImportFileRequest{FilePath: path, TorrentHash: testTorrentHash})
	if err != nil {
		t.Fatalf("a holder that appears mid-import must not fail the import: %v", err)
	}
	if resp.TorrentLinked || resp.TorrentLinkNote != TorrentLinkNoteAlreadyLinked {
		t.Fatalf("want unlinked with note %q, got linked=%v note=%q", TorrentLinkNoteAlreadyLinked, resp.TorrentLinked, resp.TorrentLinkNote)
	}

	vers, err := store.PebbleStore.GetBookVersionsByBookID(resp.ID)
	if err != nil || len(vers) != 1 {
		t.Fatalf("want exactly one fallback version for the imported book, got %d (err %v)", len(vers), err)
	}
	if vers[0].TorrentHash != "" || vers[0].Source != "imported" {
		t.Errorf("fallback version: TorrentHash=%q Source=%q, want empty and imported", vers[0].TorrentHash, vers[0].Source)
	}
	row := storedImportRow(t, store.PebbleStore, resp.ID)
	if row.VersionID != vers[0].ID || row.DelugeHash != "" {
		t.Errorf("row: VersionID=%q DelugeHash=%q, want it linked to %q without the torrent", row.VersionID, row.DelugeHash, vers[0].ID)
	}
	idx, err := store.PebbleStore.GetBookVersionByTorrentHash(testTorrentHash)
	if err != nil || idx == nil || idx.ID != store.holderID || idx.BookID != store.bookID {
		t.Errorf("the torrent index must still name the other book's version %q, got %+v (err %v)", store.holderID, idx, err)
	}
}
