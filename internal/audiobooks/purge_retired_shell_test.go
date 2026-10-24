// file: internal/audiobooks/purge_retired_shell_test.go
// version: 1.0.1
// guid: e2a71d58-3c94-4f06-8b1d-5a07c9f4d132
// last-edited: 2026-10-28

package audiobooks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// retireShellInto retires shell's one live book_file row onto holder, the way
// the retired-versions repair does (a retired version, then the re-key). The
// ids are invented; nothing here is a real library row.
func retireShellInto(t *testing.T, store *database.PebbleStore, shell, holder string) {
	t.Helper()
	rows, err := store.GetBookFiles(shell)
	if err != nil || len(rows) != 1 {
		t.Fatalf("precondition: shell has %d live rows, err %v", len(rows), err)
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	if _, err := store.CreateBookVersion(&database.BookVersion{ID: "rv-" + shell, BookID: holder,
		Status: database.BookVersionStatusRetired, Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-test", RetiredAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: shell}}, holder, "rv-"+shell); err != nil {
		t.Fatal(err)
	}
}

// failingRetiredReads makes the retired-version lookup fail.
type failingRetiredReads struct{ *database.PebbleStore }

func (failingRetiredReads) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, errors.New("injected: retired versions unreadable")
}

func retiredShellFixture(t *testing.T) (svc *AudiobookService, store *database.PebbleStore, shell, plain string) {
	t.Helper()
	svc, store, _ = setupPurgeBoundary(t)
	if _, err := store.CreateBook(&database.Book{ID: "holder", Title: "holder", FilePath: "/lib/holder"}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{ID: "holder-f", BookID: "holder", FilePath: "/lib/holder/a.mp3"}); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "shell", "/lib/shell")
	if err := store.CreateBookFile(&database.BookFile{ID: "shell-f", BookID: "shell", FilePath: "/lib/shell/a.mp3"}); err != nil {
		t.Fatal(err)
	}
	softDeleted(t, store, "plain", "/lib/plain")
	retireShellInto(t, store, "shell", "holder")
	return svc, store, "shell", "plain"
}

// The nightly purge leaves a retire shell in the trash (it owns no live row, so
// the owns-files check alone would pass it) and still purges an ordinary
// trashed book in the same run. Without the retired-shell skip the shell is
// purged: the assertions on the shell are what fail.
func TestPurge_RetiredShellIsSkipped(t *testing.T) {
	svc, store, shell, plain := retiredShellFixture(t)
	days := 0
	res, err := svc.RunPurge(context.Background(), PurgeOptions{OlderThanDays: &days, Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := store.GetBookByID(shell); b == nil {
		t.Fatalf("the retire shell was purged: %+v", res)
	}
	if res.SkippedRetiredShell != 1 || !containsSubstr(res.SkippedRetiredShellIDs, shell) {
		t.Fatalf("skip not reported: %+v", res)
	}
	if b, _ := store.GetBookByID(plain); b != nil {
		t.Errorf("control: the ordinary trashed book should have been purged: %+v", res)
	}
	if res.Purged != 1 {
		t.Errorf("Purged = %d, want 1 (the control)", res.Purged)
	}
	if rows, _ := store.GetRetiredBookFiles("holder"); len(rows) != 1 {
		t.Errorf("the holder lost its retired row: %d", len(rows))
	}
}

// An unreadable retired-version lookup fails closed: nothing is purged.
func TestPurge_RetiredShellLookupErrorFailsClosed(t *testing.T) {
	_, store, shell, _ := retiredShellFixture(t)
	svc := NewAudiobookService(failingRetiredReads{store})
	days := 0
	res, err := svc.RunPurge(context.Background(), PurgeOptions{OlderThanDays: &days, Unattended: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Purged != 0 {
		t.Fatalf("purged %d books with the retired lookup failing: %+v", res.Purged, res)
	}
	if b, _ := store.GetBookByID(shell); b == nil {
		t.Fatal("the shell was purged on a lookup error")
	}
	if len(res.Errors) == 0 {
		t.Fatalf("the lookup failure was not reported: %+v", res)
	}
}

// "Purge now" and "Discard progress and purge" refuse a retire shell too.
func TestPurge_RetiredShellRefusedOnTheOwnerPaths(t *testing.T) {
	svc, store, shell, _ := retiredShellFixture(t)
	if _, err := svc.DeleteAudiobook(context.Background(), shell, &DeleteAudiobookOptions{}); !errors.Is(err, database.ErrBookIsRetiredShell) {
		t.Fatalf("DeleteAudiobook (Purge now) err = %v, want ErrBookIsRetiredShell", err)
	}
	if _, err := svc.DiscardProgressAndPurge(context.Background(), shell, "owner"); !errors.Is(err, database.ErrBookIsRetiredShell) {
		t.Fatalf("DiscardProgressAndPurge err = %v, want ErrBookIsRetiredShell", err)
	}
	if b, _ := store.GetBookByID(shell); b == nil {
		t.Fatal("the shell was deleted")
	}
}

// The purge preview classifies the shell the way the run does, so the counts a
// dialog shows cannot disagree with the pass that follows it.
func TestPurge_RetiredShellPreviewMatchesTheRun(t *testing.T) {
	svc, store, shell, _ := retiredShellFixture(t)
	days := 0
	res, err := svc.RunPurge(context.Background(), PurgeOptions{OlderThanDays: &days, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedRetiredShell != 1 || !containsSubstr(res.SkippedRetiredShellIDs, shell) {
		t.Fatalf("preview did not report the shell as kept: %+v", res)
	}
	if res.Purgeable != 1 {
		t.Errorf("Purgeable = %d, want 1 (the ordinary trashed book)", res.Purgeable)
	}
	if b, _ := store.GetBookByID(shell); b == nil {
		t.Fatal("a preview deleted the shell")
	}
}

func mustBook(t *testing.T, store *database.PebbleStore, id string) *database.Book {
	t.Helper()
	b, err := store.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("book %s: %v", id, err)
	}
	return b
}
