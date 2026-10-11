// file: internal/versions/ingest_link_cas_test.go
// version: 1.1.0
// guid: 91d19c19-e81f-4b9b-9706-59e4789058f3
// last-edited: 2026-10-11

package versions

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// CreateIngestVersion's link write is a compare-and-set on the current row
// (ModifyBookFile). Most book_file writers take neither the merge lock nor the
// torrent lock, so these tests land a concurrent write inside the call's
// read-to-write window and check that it survives: a change to a field the
// link does not depend on is kept and the link lands; a change to one it does
// depend on (VersionID, FilePath, DelugeHash, the adopted version) refuses the
// link with nothing written.

// interleaveIngestStore runs a hook inside CreateIngestVersion, after it read
// the row (GetBookFiles) and before its link write: inside CreateBookVersion
// (the create path, which the importer takes) or right after
// GetBookVersionsByBookID returns (the adopt path's stranded-version read).
type interleaveIngestStore struct {
	*database.PebbleStore
	beforeCreate   func()
	afterVersionsR func()
}

func (s *interleaveIngestStore) CreateBookVersion(v *database.BookVersion) (*database.BookVersion, error) {
	if s.beforeCreate != nil {
		s.beforeCreate()
	}
	return s.PebbleStore.CreateBookVersion(v)
}

func (s *interleaveIngestStore) GetBookVersionsByBookID(id string) ([]database.BookVersion, error) {
	vers, err := s.PebbleStore.GetBookVersionsByBookID(id)
	if s.afterVersionsR != nil {
		s.afterVersionsR()
	}
	return vers, err
}

func modifyRow(t *testing.T, store *database.PebbleStore, bookID string, fn func(*database.BookFile)) {
	t.Helper()
	if _, err := store.ModifyBookFile(bookID, "f1", func(r *database.BookFile) error { fn(r); return nil }); err != nil {
		t.Fatalf("concurrent ModifyBookFile: %v", err)
	}
}

// The importer's shape (NewBook, a torrent hash): a writer that changes
// fields the link does not depend on, between the row read and the link
// write, keeps its change; the link still lands. The whole-row write-back this
// replaced reverted TrackNumber, Missing and Duration here.
func TestCreateIngestVersion_LinkKeepsConcurrentRowEdit(t *testing.T) {
	const torrent = "2123456789abcdef0123456789abcdef01234567"
	pebble, book, filePath := ingestFixture(t, database.BookFile{TrackNumber: 1, Duration: 100})
	store := &interleaveIngestStore{PebbleStore: pebble, beforeCreate: func() {
		modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.TrackNumber, r.Missing, r.Duration = 7, true, 4242 })
	}}

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "deluge",
		TorrentHash: torrent, NewBook: true,
	})
	if err != nil || !linked {
		t.Fatalf("linked=%v err=%v, want the link written", linked, err)
	}
	row := storedRow(t, pebble, book.ID, "f1")
	if row.VersionID != ver.ID || row.DelugeHash != torrent {
		t.Errorf("link: VersionID=%q DelugeHash=%q, want %q and %q", row.VersionID, row.DelugeHash, ver.ID, torrent)
	}
	if row.TrackNumber != 7 || !row.Missing || row.Duration != 4242 {
		t.Errorf("concurrent write reverted: TrackNumber=%d Missing=%v Duration=%d, want 7 true 4242",
			row.TrackNumber, row.Missing, row.Duration)
	}
	want, err := filehash.BookFileHash(filePath)
	if err != nil {
		t.Fatalf("control hash: %v", err)
	}
	if row.FileHash != want {
		t.Errorf("FileHash = %q, want %q", row.FileHash, want)
	}
}

// Another writer links the row inside the window: the link is refused, not
// overwritten. Outside adopt-only mode that is linked=false with a nil error
// (the version this call created is returned, unlinked, as for any link write
// that did not land).
func TestCreateIngestVersion_LinkRefusesRowLinkedConcurrently(t *testing.T) {
	pebble, book, filePath := ingestFixture(t, database.BookFile{})
	other := strandVersion(t, pebble, book.ID, "")
	store := &interleaveIngestStore{PebbleStore: pebble, beforeCreate: func() {
		modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.VersionID = other.ID })
	}}

	ver, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported", NewBook: true,
	})
	if err != nil || linked || ver == nil {
		t.Fatalf("want (created version, linked=false, nil), got ver=%v linked=%v err=%v", ver, linked, err)
	}
	if row := storedRow(t, pebble, book.ID, "f1"); row.VersionID != other.ID {
		t.Errorf("row VersionID = %q, want the concurrent link %q kept", row.VersionID, other.ID)
	}
}

// The row is repointed inside the window: the link is refused, and the new
// path survives.
func TestCreateIngestVersion_LinkRefusesRowWhosePathMoved(t *testing.T) {
	pebble, book, filePath := ingestFixture(t, database.BookFile{})
	moved := filePath + ".moved"
	store := &interleaveIngestStore{PebbleStore: pebble, beforeCreate: func() {
		modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.FilePath = moved })
	}}

	_, linked, err := CreateIngestVersion(store, IngestVersionParams{
		BookID: book.ID, BookFileID: "f1", FilePath: filePath, Format: "m4b", Source: "imported", NewBook: true,
	})
	if err != nil || linked {
		t.Fatalf("want linked=false with a nil error, got linked=%v err=%v", linked, err)
	}
	row := storedRow(t, pebble, book.ID, "f1")
	if row.VersionID != "" || row.FilePath != moved {
		t.Errorf("row: VersionID=%q FilePath=%q, want unlinked at %q", row.VersionID, row.FilePath, moved)
	}
}

// adoptParams adopts v for the fixture's row f1, as the relink pass does.
func adoptParams(bookID, filePath, torrent, versionID string) IngestVersionParams {
	return IngestVersionParams{BookID: bookID, BookFileID: "f1", FilePath: filePath, TorrentHash: torrent,
		AdoptVersionID: versionID, NoFileRead: true}
}

// Adopt-only refusals: each returns its sentinel with nothing written and no
// version created.
func TestCreateIngestVersion_AdoptOnlyRefusesMovedRowOrVersion(t *testing.T) {
	const torrent = "3123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name string
		want error
		// hook runs after the stranded-version read.
		hook func(t *testing.T, pebble *database.PebbleStore, book *database.Book, v *database.BookVersion)
	}{
		{"version trashed after the stranded read", ErrVersionNotAdoptable,
			func(t *testing.T, pebble *database.PebbleStore, _ *database.Book, v *database.BookVersion) {
				cp := *v
				cp.Status = database.BookVersionStatusTrash
				if err := pebble.UpdateBookVersion(&cp); err != nil {
					t.Fatalf("trash: %v", err)
				}
			}},
		{"torrent hash changed on the row", ErrBookFileChanged,
			func(t *testing.T, pebble *database.PebbleStore, book *database.Book, _ *database.BookVersion) {
				modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.DelugeHash = "ff" + torrent[2:] })
			}},
		{"row moved to another book", ErrBookFileChanged,
			func(t *testing.T, pebble *database.PebbleStore, book *database.Book, _ *database.BookVersion) {
				other, err := pebble.CreateBook(&database.Book{Title: "Other", FilePath: book.FilePath, Format: "m4b"})
				if err != nil {
					t.Fatalf("CreateBook: %v", err)
				}
				if err := pebble.MoveBookFilesToBook([]string{"f1"}, book.ID, other.ID); err != nil {
					t.Fatalf("move: %v", err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pebble, book, filePath := ingestFixture(t, database.BookFile{DelugeHash: torrent})
			v := strandVersion(t, pebble, book.ID, torrent)
			store := &interleaveIngestStore{PebbleStore: pebble}
			store.afterVersionsR = func() {
				store.afterVersionsR = nil
				tc.hook(t, pebble, book, v)
			}
			got, linked, err := CreateIngestVersion(store, adoptParams(book.ID, filePath, torrent, v.ID))
			if !errors.Is(err, tc.want) || linked || got != nil {
				t.Fatalf("want %v with nothing returned, got ver=%v linked=%v err=%v", tc.want, got, linked, err)
			}
			if vers, _ := pebble.GetBookVersionsByBookID(book.ID); len(vers) != 1 {
				t.Errorf("want the 1 stranded version only, got %d", len(vers))
			}
			files, _ := pebble.GetAllBookFilesCoreComplete()
			for _, f := range files {
				if f.ID == "f1" && f.VersionID != "" {
					t.Errorf("row linked to %q", f.VersionID)
				}
			}
		})
	}
}

// NoFileRead: the file is never read, so a row without a hash is linked
// without one when the caller has none; a caller's FileHash is written only
// onto a row that still has none at the write.
func TestCreateIngestVersion_AdoptOnlyNoFileReadUsesCallerHash(t *testing.T) {
	t.Run("no caller hash: linked without one", func(t *testing.T) {
		pebble, book, filePath := ingestFixture(t, database.BookFile{})
		v := strandVersion(t, pebble, book.ID, "")
		if _, linked, err := CreateIngestVersion(pebble, adoptParams(book.ID, filePath, "", v.ID)); err != nil || !linked {
			t.Fatalf("linked=%v err=%v", linked, err)
		}
		if row := storedRow(t, pebble, book.ID, "f1"); row.VersionID != v.ID || row.FileHash != "" {
			t.Errorf("row: VersionID=%q FileHash=%q, want linked with no hash (the file must not be read)", row.VersionID, row.FileHash)
		}
	})
	t.Run("caller hash fills an empty one", func(t *testing.T) {
		pebble, book, filePath := ingestFixture(t, database.BookFile{})
		v := strandVersion(t, pebble, book.ID, "")
		params := adoptParams(book.ID, filePath, "", v.ID)
		params.FileHash = "caller-hash"
		if _, linked, err := CreateIngestVersion(pebble, params); err != nil || !linked {
			t.Fatalf("linked=%v err=%v", linked, err)
		}
		if row := storedRow(t, pebble, book.ID, "f1"); row.FileHash != "caller-hash" {
			t.Errorf("FileHash = %q, want the caller's", row.FileHash)
		}
	})
	t.Run("a hash written concurrently is kept", func(t *testing.T) {
		pebble, book, filePath := ingestFixture(t, database.BookFile{})
		v := strandVersion(t, pebble, book.ID, "")
		store := &interleaveIngestStore{PebbleStore: pebble}
		store.afterVersionsR = func() {
			store.afterVersionsR = nil
			modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.FileHash = "backfill-hash" })
		}
		params := adoptParams(book.ID, filePath, "", v.ID)
		params.FileHash = "caller-hash"
		if _, linked, err := CreateIngestVersion(store, params); err != nil || !linked {
			t.Fatalf("linked=%v err=%v", linked, err)
		}
		if row := storedRow(t, pebble, book.ID, "f1"); row.VersionID != v.ID || row.FileHash != "backfill-hash" {
			t.Errorf("row: VersionID=%q FileHash=%q, want linked keeping the backfill's hash", row.VersionID, row.FileHash)
		}
	})
}

// Adopt-only holds the row to the CALLER'S plan, not only to this call's own
// read: a row whose path, torrent hash or version link changed after the
// caller planned it and before CreateIngestVersion read it is refused with
// ErrBookFileChanged and nothing written -- including a row another writer
// already linked to the very version the caller wanted, because the plan the
// owner reviewed had it unlinked.
func TestCreateIngestVersion_AdoptOnlyHoldsRowToThePlan(t *testing.T) {
	const torrent = "4123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name string
		// drift changes the stored row before the call; v is the stranded
		// version the caller plans to adopt.
		drift func(r *database.BookFile, v *database.BookVersion)
	}{
		{"path changed", func(r *database.BookFile, _ *database.BookVersion) { r.FilePath += ".moved" }},
		{"torrent hash gained", func(r *database.BookFile, _ *database.BookVersion) { r.DelugeHash = torrent }},
		{"linked to the planned version", func(r *database.BookFile, v *database.BookVersion) { r.VersionID = v.ID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pebble, book, filePath := ingestFixture(t, database.BookFile{})
			v := strandVersion(t, pebble, book.ID, "")
			modifyRow(t, pebble, book.ID, func(r *database.BookFile) { tc.drift(r, v) })
			before := storedRow(t, pebble, book.ID, "f1")

			got, linked, err := CreateIngestVersion(pebble, adoptParams(book.ID, filePath, "", v.ID))
			if !errors.Is(err, ErrBookFileChanged) || linked || got != nil {
				t.Fatalf("want ErrBookFileChanged with nothing returned, got ver=%v linked=%v err=%v", got, linked, err)
			}
			if after := storedRow(t, pebble, book.ID, "f1"); !reflect.DeepEqual(after, before) {
				t.Errorf("row written:\n before %+v\n after  %+v", before, after)
			}
			if vers, _ := pebble.GetBookVersionsByBookID(book.ID); len(vers) != 1 {
				t.Errorf("want the 1 stranded version only, got %d", len(vers))
			}
		})
	}
}

// A writer that links the row to the SAME version inside the read-to-write
// window (after the plan check passed) leaves the row exactly as the link
// would: the compare-and-set reports it linked, not refused.
func TestCreateIngestVersion_AdoptOnlyConcurrentSameLinkIsLinked(t *testing.T) {
	pebble, book, filePath := ingestFixture(t, database.BookFile{})
	v := strandVersion(t, pebble, book.ID, "")
	store := &interleaveIngestStore{PebbleStore: pebble}
	store.afterVersionsR = func() {
		store.afterVersionsR = nil
		modifyRow(t, pebble, book.ID, func(r *database.BookFile) { r.VersionID = v.ID })
	}
	got, linked, err := CreateIngestVersion(store, adoptParams(book.ID, filePath, "", v.ID))
	if err != nil || !linked || got == nil || got.ID != v.ID {
		t.Fatalf("want %s linked, got ver=%v linked=%v err=%v", v.ID, got, linked, err)
	}
	if row := storedRow(t, pebble, book.ID, "f1"); row.VersionID != v.ID {
		t.Errorf("row VersionID = %q, want %q", row.VersionID, v.ID)
	}
}
