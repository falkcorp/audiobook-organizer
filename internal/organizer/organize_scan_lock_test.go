// file: internal/organizer/organize_scan_lock_test.go
// version: 1.0.0
// guid: 7c3a9e15-2f84-4d6b-a0c1-58e2b94d7f36
// last-edited: 2026-09-30

package organizer

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/scanlock"
)

func holdBook(t *testing.T, id string) *scanlock.Hold {
	t.Helper()
	h, err := scanlock.Books.LockSet(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Release)
	return h
}

func movedFrom(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// library.organize (lockBooks) on a book the scanner is reading: pass 1 sets
// it aside, the other book organizes at once, and the held book is organized
// the moment the scanner releases it -- not before, and not skipped.
func TestOrganizeBooks_WaitsForAHeldBookAndOrganizesAFreeOneAtOnce(t *testing.T) {
	svc, store, root := setupInPlace(t)
	heldSrc := filepath.Join(root, "incoming", "held.m4b")
	freeSrc := filepath.Join(root, "incoming", "free.m4b")
	held := addInPlaceBook(t, store, "held", "Held", heldSrc, filled(150, 1), nil, 0)
	free := addInPlaceBook(t, store, "free", "Free", freeSrc, filled(160, 2), nil, 0)

	scan := holdBook(t, held.ID)
	done := make(chan *Stats, 1)
	go func() {
		done <- svc.organizeBooksOpts(context.Background(), []database.Book{*held, *free}, nil, &noopLogger{}, "", true)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !movedFrom(freeSrc) {
		if time.Now().After(deadline) {
			t.Fatal("the free book was not organized while another book was held")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if movedFrom(heldSrc) {
		t.Fatal("the held book was moved while the scanner held it")
	}
	select {
	case s := <-done:
		t.Fatalf("organize returned (%+v) while a book was still held", s)
	case <-time.After(100 * time.Millisecond):
	}

	scan.Release()
	var stats *Stats
	select {
	case stats = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("organize never finished after the scanner released the book")
	}
	if !movedFrom(heldSrc) {
		t.Fatal("the held book was not organized after the scanner released it")
	}
	if stats.Failed != 0 || stats.Collisions[OutcomeScanBusy] != 0 {
		t.Fatalf("want both organized, got %+v", stats)
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("organize left %d scan lock(s) held", n)
	}
}

// A book the scanner holds past the shared bound is skipped and counted
// scan_busy -- not failed -- so the next organize picks it up.
func TestOrganizeBooks_BookHeldPastTheBoundIsSkippedBusyNotFailed(t *testing.T) {
	svc, store, root := setupInPlace(t)
	old := organizeBookLockWait
	organizeBookLockWait = 50 * time.Millisecond
	t.Cleanup(func() { organizeBookLockWait = old })
	src := filepath.Join(root, "incoming", "busy.m4b")
	b := addInPlaceBook(t, store, "busy", "Busy", src, filled(150, 3), nil, 0)
	holdBook(t, b.ID)

	stats := svc.organizeBooksOpts(context.Background(), []database.Book{*b}, nil, &noopLogger{}, "", true)
	if stats.Skipped != 1 || stats.Failed != 0 || stats.Collisions[OutcomeScanBusy] != 1 {
		t.Fatalf("want one scan_busy skip, got %+v", stats)
	}
	mustContent(t, src, filled(150, 3))
}

// The post-scan auto-organize runs INSIDE the scan and must never take the
// scan lock (R3, T9): organizeBooks without lockBooks organizes a book whose
// key is held, without waiting.
func TestOrganizeBooks_AutoOrganizeTakesNoScanLock(t *testing.T) {
	svc, store, root := setupInPlace(t)
	src := filepath.Join(root, "incoming", "auto.m4b")
	b := addInPlaceBook(t, store, "auto", "Auto", src, filled(150, 4), nil, 0)
	holdBook(t, b.ID)

	done := make(chan *Stats, 1)
	go func() { done <- svc.organizeBooks(context.Background(), []database.Book{*b}, nil, &noopLogger{}, "") }()
	select {
	case stats := <-done:
		if stats.Failed != 0 || stats.Collisions[OutcomeScanBusy] != 0 {
			t.Fatalf("auto-organize: %+v", stats)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("auto-organize waited on the scan lock")
	}
	if !movedFrom(src) {
		t.Fatal("auto-organize did not organize the book")
	}
}

// Move case (b) for organize copies, asserted. library.organize copies an
// out-of-root book X into the library: the copy's file exists before its row,
// and a scan finding the copy in that window must not import it or repoint X
// at it. The scanner side (it waits for L0 {X}, then merges onto the copy's
// row) is TestScanBookLock_LibraryCopyBeforeItsRowIsNotPromotedOntoTheOriginal
// in internal/scanner. This is the organize side: L0 {X} is held across the
// whole window. The probe runs where CreateOrganizedVersion takes its
// version-group key (L1) -- after the copy is on disk, before its row -- and
// finds X's scan lock held, i.e. L0 was taken first and is still held.
//
// Until this PR the organize took no L0 and this was the documented gap.
func TestOrganizeBooks_CopyLandsUnderTheBooksScanLock(t *testing.T) {
	svc, store, _ := setupInPlace(t)
	config.AppConfig.OrganizationStrategy = "copy"
	src := filepath.Join(t.TempDir(), "import", "x.m4b")
	x := addInPlaceBook(t, store, "x", "Copied", src, filled(170, 7), nil, 0)

	var probes, heldAtProbe atomic.Int32
	svc.VersionGroupLocker = func(string) func() {
		probes.Add(1)
		if h, ok := scanlock.Books.TryLockSet([]string{x.ID}); ok {
			h.Release()
		} else {
			heldAtProbe.Add(1)
		}
		return func() {}
	}

	stats := svc.organizeBooksOpts(context.Background(), []database.Book{*x}, nil, &noopLogger{}, "", true)
	if stats.Failed != 0 || stats.Organized != 1 {
		t.Fatalf("want one organized copy, got %+v", stats)
	}
	if probes.Load() == 0 {
		t.Fatal("CreateOrganizedVersion never took its version-group key; the probe proves nothing")
	}
	if heldAtProbe.Load() != probes.Load() {
		t.Fatalf("X's scan lock was free at %d of %d version-group probes: a scan could import the copy before its row",
			probes.Load()-heldAtProbe.Load(), probes.Load())
	}
	if n := scanlock.Books.Held(); n != 0 {
		t.Fatalf("organize left %d scan lock(s) held", n)
	}
}
