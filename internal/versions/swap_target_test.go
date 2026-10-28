// file: internal/versions/swap_target_test.go
// version: 1.1.0
// guid: 3c9e1b7d-52a4-4e68-b0f3-8d6a2c4e9175
// last-edited: 2026-10-28

package versions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// swapFixture is a book ("The Long Halloween") whose active version plays one
// file at the book's folder, plus whatever other versions a test adds under
// .versions/{id}/.
type swapFixture struct {
	t       *testing.T
	store   *database.PebbleStore
	book    *database.Book
	bookDir string
	active  *database.BookVersion
	rootAud string // the active version's file
}

func newSwapFixture(t *testing.T) *swapFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup()

	bookDir := t.TempDir()
	rootAud := filepath.Join(bookDir, "The Long Halloween.m4b")
	writeTestFile(t, rootAud, "active")
	book, err := store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: rootAud, Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
		IngestDate: time.Now().Add(-100 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		BookID: book.ID, VersionID: active.ID, FilePath: rootAud, Format: "m4b",
	}); err != nil {
		t.Fatal(err)
	}
	return &swapFixture{t: t, store: store, book: book, bookDir: bookDir, active: active, rootAud: rootAud}
}

// add makes a version of the book in the given status, ingested ago ago. With
// a name it also gets one file of that name in its slot.
func (f *swapFixture) add(status string, ago time.Duration, name string) *database.BookVersion {
	f.t.Helper()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: status, Format: "mp3", Source: "deluge",
		IngestDate: time.Now().Add(-ago),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if name != "" {
		path := filepath.Join(VersionSlotDir(f.bookDir, v.ID), name)
		writeTestFile(f.t, path, "alt-"+v.ID)
		if err := f.store.CreateBookFile(&database.BookFile{
			BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "mp3",
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	return v
}

func (f *swapFixture) pick() (*database.BookVersion, error) {
	return PickSwapTarget(f.store, f.book.ID, f.active.ID)
}

func requireNoTarget(t *testing.T, err error, wantInReasons ...string) {
	t.Helper()
	if !errors.Is(err, ErrNoSwapTarget) {
		t.Fatalf("err = %v, want ErrNoSwapTarget", err)
	}
	for _, want := range wantInReasons {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// The newest alt that owns files wins, whatever order the store lists them in.
func TestPickSwapTarget_NewestAltWithFiles(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, 48*time.Hour, "Knightfall.mp3")
	newest := f.add(database.BookVersionStatusAlt, time.Hour, "No Mans Land.mp3")
	f.add(database.BookVersionStatusAlt, 24*time.Hour, "Zero Year.mp3")

	got, err := f.pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != newest.ID {
		t.Errorf("picked %s, want the newest alt %s", got.ID, newest.ID)
	}
}

// Equal IngestDates are broken by the smaller version ID, so the pick does not
// depend on listing order.
func TestPickSwapTarget_TieGoesToSmallerID(t *testing.T) {
	f := newSwapFixture(t)
	at := time.Now().Add(-time.Hour)
	var ids []string
	for _, name := range []string{"Arkham Asylum.mp3", "Death of the Family.mp3", "Under the Red Hood.mp3"} {
		v, err := f.store.CreateBookVersion(&database.BookVersion{
			BookID: f.book.ID, Status: database.BookVersionStatusAlt, Format: "mp3", Source: "deluge", IngestDate: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(VersionSlotDir(f.bookDir, v.ID), name)
		writeTestFile(t, path, "x")
		if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "mp3"}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	smallest := ids[0]
	for _, id := range ids {
		if id < smallest {
			smallest = id
		}
	}
	got, err := f.pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != smallest {
		t.Errorf("picked %s, want the smallest id %s", got.ID, smallest)
	}
}

// Only an alt is a candidate: a newer trashed, purging, purged or retired
// version is never chosen, and is named in the refusal when nothing else is.
func TestPickSwapTarget_IgnoresEveryStatusButAlt(t *testing.T) {
	f := newSwapFixture(t)
	titles := []string{"Gotham Central", "Bane", "Ego", "Officer Down", "Broken City", "Prey"}
	for i, st := range []string{
		database.BookVersionStatusTrash, database.BookVersionStatusPurging, database.BookVersionStatusPurgeFailed,
		database.BookVersionStatusInactivePurged, database.BookVersionStatusPending, database.BookVersionStatusBlockedForRedownload,
	} {
		f.add(st, time.Minute, titles[i]+".mp3")
	}
	_, err := f.pick()
	requireNoTarget(t, err, "is trash", "is purging", "is inactive_purged")

	alt := f.add(database.BookVersionStatusAlt, 72*time.Hour, "Court of Owls.mp3")
	got, err := f.pick()
	if err != nil {
		t.Fatalf("with an alt present: %v", err)
	}
	if got.ID != alt.ID {
		t.Errorf("picked %s, want the alt %s over the newer non-alt versions", got.ID, alt.ID)
	}
}

// A retired version is never chosen, even one newer than every alt: promoting it
// would put the retired files of another book into this book's active slot.
func TestPickSwapTarget_IgnoresRetired(t *testing.T) {
	f := newRetiredFixture(t, true)
	active, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported",
		IngestDate: time.Now().Add(-200 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	retiredBefore := f.reread(t)

	// Only a retired version besides the active one: nothing to swap to.
	_, err = PickSwapTarget(f.store, f.holderID, active.ID)
	requireNoTarget(t, err, f.ver.ID, "retired")

	// An alt older than the retired version wins, and the retired version is
	// not rewritten.
	alt, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
		IngestDate: retiredBefore.IngestDate.Add(-48 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	altFile := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Mad Love.m4b")
	writeTestFile(t, altFile, "alt")
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.holderID, VersionID: alt.ID, FilePath: altFile, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	got, err := PickSwapTarget(f.store, f.holderID, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != alt.ID {
		t.Errorf("picked %s, want the alt %s (the retired version %s is newer)", got.ID, alt.ID, f.ver.ID)
	}
	if after := f.reread(t); after.Status != database.BookVersionStatusRetired || after.Version != retiredBefore.Version {
		t.Errorf("retired version: status=%s row version %d -> %d, want untouched", after.Status, retiredBefore.Version, after.Version)
	}
}

// A book with no other version at all is refused, with a reason a person can read.
func TestPickSwapTarget_OnlyVersion(t *testing.T) {
	f := newSwapFixture(t)
	_, err := f.pick()
	requireNoTarget(t, err, "no other version")
}

// An alt with no files cannot take over: a swap that moves nothing leaves the
// book serving the files of the version that is leaving.
func TestPickSwapTarget_AltWithoutFilesIsSkipped(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Minute, "") // newest, no files
	withFiles := f.add(database.BookVersionStatusAlt, 10*time.Hour, "Year Two.mp3")
	got, err := f.pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != withFiles.ID {
		t.Errorf("picked %s, want %s (the newer alt owns no files)", got.ID, withFiles.ID)
	}

	f2 := newSwapFixture(t)
	f2.add(database.BookVersionStatusAlt, time.Minute, "")
	_, err = f2.pick()
	requireNoTarget(t, err, "no files")
}

// A file the disk no longer has makes the version ineligible, and the next one
// is picked instead; the swap would otherwise fail half way through its moves.
func TestPickSwapTarget_MissingOrBlockedFilesFallThrough(t *testing.T) {
	f := newSwapFixture(t)
	gone := f.add(database.BookVersionStatusAlt, time.Minute, "Face the Face.mp3")
	fine := f.add(database.BookVersionStatusAlt, time.Hour, "Dark Victory.mp3")
	// blocked's file would land on a file at the book folder that no version owns.
	blocked := f.add(database.BookVersionStatusAlt, 30*time.Minute, "Venom.mp3")
	writeTestFile(t, filepath.Join(f.bookDir, "Venom.mp3"), "not in any version")
	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, gone.ID), "Face the Face.mp3")); err != nil {
		t.Fatal(err)
	}

	got, err := f.pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != fine.ID {
		t.Errorf("picked %s, want %s (%s has a missing file, %s a file that would land on a stranger's)", got.ID, fine.ID, gone.ID, blocked.ID)
	}

	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, fine.ID), "Dark Victory.mp3")); err != nil {
		t.Fatal(err)
	}
	_, err = f.pick()
	requireNoTarget(t, err, gone.ID, blocked.ID, fine.ID, "is missing", "already there")
}

// The active version's own file being gone makes every candidate ineligible: the
// swap would fail at its first move.
func TestPickSwapTarget_ActiveVersionFileMissing(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	if err := os.Remove(f.rootAud); err != nil {
		t.Fatal(err)
	}
	_, err := f.pick()
	requireNoTarget(t, err, "the active version's")
}

// A file row that names no version is refused outright: the swap folds such
// rows into both versions' file sets.
func TestPickSwapTarget_UnmigratedFileRowRefuses(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	orphan := filepath.Join(f.bookDir, "The Killing Joke.mp3")
	writeTestFile(t, orphan, "x")
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, FilePath: orphan, Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.pick()
	requireNoTarget(t, err, "belongs to no version")
}

// A swap that did not finish refuses another one.
func TestPickSwapTarget_UnfinishedSwapRefuses(t *testing.T) {
	for _, st := range []string{database.BookVersionStatusSwappingIn, database.BookVersionStatusSwappingOut} {
		f := newSwapFixture(t)
		f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
		f.add(st, time.Minute, "Contagion.mp3")
		_, err := f.pick()
		requireNoTarget(t, err, "did not finish", st)
	}
}

// The picked version swaps in through RunVersionSwap with nothing refused by the
// preflight on the way: the files move, the statuses flip, file_path follows.
func TestPickSwapTarget_ThenRunVersionSwap(t *testing.T) {
	f := newSwapFixture(t)
	target := f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	got, err := f.pick()
	if err != nil {
		t.Fatal(err)
	}
	if err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: got.ID,
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.store.GetBookVersion(target.ID); v.Status != database.BookVersionStatusActive {
		t.Errorf("target status = %s, want active", v.Status)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusAlt {
		t.Errorf("old active status = %s, want alt", v.Status)
	}
	if _, err := os.Stat(filepath.Join(f.bookDir, "Court of Owls.mp3")); err != nil {
		t.Errorf("the target's file is not at the book folder: %v", err)
	}
	if b, _ := f.store.GetBookByID(f.book.ID); b.FilePath != filepath.Join(f.bookDir, "Court of Owls.mp3") {
		t.Errorf("book file_path = %s, want the target's file", b.FilePath)
	}
}

// RunVersionSwap refuses a retired version, on either side, before it writes
// anything: both versions keep their status and row version and no file moves.
func TestRunVersionSwap_RefusesRetiredVersion(t *testing.T) {
	for _, retiredSide := range []string{"to", "from"} {
		t.Run(retiredSide, func(t *testing.T) {
			f := newRetiredFixture(t, true)
			ordinary, err := f.store.CreateBookVersion(&database.BookVersion{
				BookID: f.holderID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
			})
			if err != nil {
				t.Fatal(err)
			}
			ordFile := filepath.Join(VersionSlotDir(f.bookDir, ordinary.ID), "Dark Knight Returns.m4b")
			writeTestFile(t, ordFile, "ordinary")
			if err := f.store.CreateBookFile(&database.BookFile{BookID: f.holderID, VersionID: ordinary.ID, FilePath: ordFile, Format: "m4b"}); err != nil {
				t.Fatal(err)
			}

			params := VersionSwapParams{BookID: f.holderID, FromVersionID: ordinary.ID, ToVersionID: f.ver.ID}
			if retiredSide == "from" {
				params.FromVersionID, params.ToVersionID = f.ver.ID, ordinary.ID
			}
			retiredBefore := f.reread(t)
			ordBefore, _ := f.store.GetBookVersion(ordinary.ID)

			err = RunVersionSwap(context.Background(), f.store, params, nil, nil, nil)
			if !errors.Is(err, ErrRetiredVersion) {
				t.Fatalf("err = %v, want ErrRetiredVersion", err)
			}
			retiredAfter := f.reread(t)
			ordAfter, _ := f.store.GetBookVersion(ordinary.ID)
			if retiredAfter.Status != database.BookVersionStatusRetired || retiredAfter.Version != retiredBefore.Version {
				t.Errorf("retired version: status=%s row version %d -> %d, want untouched", retiredAfter.Status, retiredBefore.Version, retiredAfter.Version)
			}
			if ordAfter.Status != ordBefore.Status || ordAfter.Version != ordBefore.Version {
				t.Errorf("ordinary version: status=%s row version %d -> %d, want untouched", ordAfter.Status, ordBefore.Version, ordAfter.Version)
			}
			for _, p := range []string{f.slotFile, ordFile} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("a file moved: %v", err)
				}
			}
		})
	}

	// Control: the same two versions, neither retired, swap.
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	if err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil); err != nil {
		t.Fatalf("control swap: %v", err)
	}
}

// A swap that fails before any file has moved puts both versions back as they
// were: otherwise the book is left with no active version (the active index is
// dropped when the version leaves 'active').
func TestRunVersionSwap_NothingMovedRestoresStatuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	// A .versions folder the process may not write to: the leaving version's
	// slot cannot be created (step 2) before anything has moved, while the
	// preflight, which only looks, still passes.
	versionsDir := VersionsDir(f.bookDir)
	if err := os.Chmod(versionsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(versionsDir, 0o755) })

	err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "move-to-versions") {
		t.Fatalf("err = %v, want the move-to-versions failure (the slot cannot be created)", err)
	}
	if errors.Is(err, ErrSwapRefused) || errors.Is(err, ErrSwapIncomplete) {
		t.Errorf("err = %v: a failure that put everything back is neither a refusal nor an incomplete swap", err)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusActive {
		t.Errorf("leaving version status = %s, want active again", v.Status)
	}
	if v, _ := f.store.GetBookVersion(alt.ID); v.Status != database.BookVersionStatusAlt {
		t.Errorf("entering version status = %s, want alt again", v.Status)
	}
	if got, _ := f.store.GetActiveVersionForBook(f.book.ID); got == nil || got.ID != f.active.ID {
		t.Errorf("active version = %v, want %s", got, f.active.ID)
	}
	if _, err := os.Stat(f.rootAud); err != nil {
		t.Errorf("the active file moved: %v", err)
	}
}

// A multi-file book keeps its folder in FilePath. The swap takes the book's
// folder to be the parent of FilePath, so for such a book it would move the
// entering version's files into the author's folder and rewrite FilePath to one
// file: PickSwapTarget refuses it, and RunVersionSwap, called directly, refuses
// it before it writes anything.
func TestSwap_RefusesBookWhosePathIsAFolder(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Court of Owls.mp3")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
		b.FilePath = f.bookDir
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	_, err := f.pick()
	requireNoTarget(t, err, "is a folder", "multi-file")

	before, _ := f.store.GetBookVersion(f.active.ID)
	err = RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "is a folder") {
		t.Fatalf("RunVersionSwap err = %v, want a refusal naming the folder", err)
	}
	after, _ := f.store.GetBookVersion(f.active.ID)
	if after.Status != database.BookVersionStatusActive || after.Version != before.Version {
		t.Errorf("active version: status=%s row version %d -> %d, want untouched", after.Status, before.Version, after.Version)
	}
	if _, err := os.Stat(f.rootAud); err != nil {
		t.Errorf("the active file moved: %v", err)
	}
}
