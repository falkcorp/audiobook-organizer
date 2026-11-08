// file: core/internal/restoreswap/modes_unix_test.go
// version: 1.0.2
// guid: 9fc3abb8-4b6f-4fba-ac15-81185f297514
// last-edited: 2026-11-08

//go:build unix

package restoreswap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// wantMode fails t unless path's permission bits are want.
func wantMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("%s has mode %o, want %o", filepath.Base(path), got, want)
	}
}

// TestSwapFilesArePrivate (SEC-9): on the real filesystem, with a umask
// that would leave them world-readable, every file the swap creates is 0600
// (the intent, the journal, step x's marks), and the location file's copy
// keeps the original's mode, so step x puts it back as it was. The intent
// and the journal are checked as Stage leaves them, before any boot: P0a
// rewrites the journal (durableJournal), so a mode checked only after it
// would hide one appendRec's create left (R1-21 verification review).
func TestSwapFilesArePrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	h := newOSHost(t.TempDir())
	id := "r-private"
	makeStore(t, vfs.Default, h.db, 0, "live")
	staged := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
	makeStore(t, vfs.Default, staged, 1, "staged")
	location := filepath.Join(h.state, locationFile)
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(location, []byte(`{"generation":"robin"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: h.db,
		Targets: []restorejournal.Target{{From: staged, To: h.db}}, StagedByBuild: testBuild,
		ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	s := h.swapper(t)
	ctx := context.Background()
	if err := s.Stage(ctx, in); err != nil {
		t.Fatal(err)
	}
	dir := restorejournal.JournalDir(h.state, id)
	wantMode(t, restorejournal.PendingPath(h.state), 0o600)
	wantMode(t, filepath.Join(dir, restorejournal.JournalName), 0o600)
	out, err := s.RunP0a(ctx)
	if err != nil || out.Boot != BootRestore {
		t.Fatalf("P0a: %+v %v", out, err)
	}
	wantMode(t, filepath.Join(dir, restorejournal.InflightName), 0o600)
	wantMode(t, filepath.Join(dir, restorejournal.JournalName), 0o600)
	wantMode(t, filepath.Join(dir, locationFile), 0o640)
	// The directories the swap creates are 0700 (R1-21 review n5): restore/,
	// the journal's, and previous/, which holds the original store.
	wantMode(t, restorejournal.Dir(h.state), 0o700)
	wantMode(t, dir, 0o700)
	wantMode(t, filepath.Dir(restorejournal.SetAsideDest(h.state, h.db, id, h.db)), 0o700)
	if err := s.Opened(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.SwapBack(ctx, id, errors.New("P4b failed on the restored store")); !errors.Is(err, ErrSwappedBack) {
		t.Fatalf("SwapBack: %v", err)
	}
	wantMode(t, filepath.Join(dir, backStarted), 0o600)
	wantMode(t, filepath.Join(dir, backMoved), 0o600)
	wantMode(t, location, 0o640)
	wantMode(t, filepath.Dir(restorejournal.FailedDest(h.state, h.db, id, h.db)), 0o700)
}

// TestEnsureStagedJournalIsPrivate (SEC-9; R1-21 verification review): a
// stager whose create succeeded and whose journal write did not leaves the
// intent with no journal; P0a's ensureStaged creates it, and that create is
// 0600 in a 0700 directory as it leaves it, before anything rewrites it.
func TestEnsureStagedJournalIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	h := newOSHost(t.TempDir())
	id := "r-ensure"
	makeStore(t, vfs.Default, h.db, 0, "live")
	staged := filepath.Join(restorejournal.StagingDir(h.db, id), "main.pebble")
	makeStore(t, vfs.Default, staged, 1, "staged")
	in := restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive, DBPath: h.db,
		Targets: []restorejournal.Target{{From: staged, To: h.db}}, StagedByBuild: testBuild,
		ExpiresAt: testNow().Add(time.Hour), RequestedBy: "alfred", RequestedAt: testNow()}
	if err := restorejournal.CreateIntent(h.state, in); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(restorejournal.JournalDir(h.state, id), restorejournal.JournalName)
	if _, err := os.Stat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal before ensureStaged: %v, want none", err)
	}
	recs, err := h.swapper(t).ensureStaged(context.Background(), in)
	if err != nil || len(recs) != 1 || recs[0].Step != StepStaged {
		t.Fatalf("ensureStaged = %+v, %v; want the one staged record", recs, err)
	}
	wantMode(t, journal, 0o600)
	wantMode(t, restorejournal.JournalDir(h.state, id), 0o700)
}
