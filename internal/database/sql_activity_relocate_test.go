// file: internal/database/sql_activity_relocate_test.go
// version: 1.0.0
// guid: 6a2f4c19-8e73-4b50-9d61-0c8b3a7e2f95
// last-edited: 2026-09-07

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedActivityDBAt creates a SQLite activity database at path holding n entries and
// returns it still OPEN, so the caller controls when (and whether) it is closed —
// which is what lets the WAL test leave frames uncheckpointed.
func seedActivityDBAt(t *testing.T, path string, n int) *SQLActivityStore {
	t.Helper()
	s, err := OpenSQLiteActivityStore(path)
	if err != nil {
		t.Fatalf("open source store at %s: %v", path, err)
	}
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	entries := make([]ActivityEntry, 0, n)
	for i := range n {
		entries = append(entries, ActivityEntry{
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Tier:      "change", Type: "t", Level: "info",
			Source:  "relocate-test",
			Summary: "entry-" + time.Duration(i).String(),
		})
	}
	if got, err := s.recordBatch(context.Background(), entries); err != nil || got != n {
		t.Fatalf("seed: wrote %d/%d err=%v", got, n, err)
	}
	return s
}

// countRowsAt opens path read-only-ish and returns the activity row count.
func countRowsAt(t *testing.T, path string) int64 {
	t.Helper()
	n, err := checkpointAndCount(path)
	if err != nil {
		t.Fatalf("count rows at %s: %v", path, err)
	}
	return n
}

// TestRelocateActivityDB_MovesEveryRowAndRemovesTheSource is the happy path: the
// destination holds every row and the source is gone once the move is verified.
func TestRelocateActivityDB_MovesEveryRowAndRemovesTheSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "old", "activity.sqlite")
	dst := filepath.Join(dir, "library", ".activity", "activity.sqlite")
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		t.Fatal(err)
	}

	s := seedActivityDBAt(t, src, 250)
	if err := s.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	st, err := RelocateActivityDB(context.Background(), src, dst)
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if st.Rows != 250 {
		t.Errorf("reported Rows = %d, want 250", st.Rows)
	}
	if st.Bytes <= 0 {
		t.Error("reported Bytes = 0, want the copied size")
	}

	if got := countRowsAt(t, dst); got != 250 {
		t.Errorf("destination holds %d rows, want 250", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source still present after a verified move")
	}
	// The destination directory did not exist beforehand; the move must create it.
	if _, err := os.Stat(filepath.Dir(dst)); err != nil {
		t.Errorf("destination directory was not created: %v", err)
	}
}

// copyRaw copies a single file byte-for-byte, or does nothing if it is absent.
func copyRaw(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// snapshotCrashedDB captures the on-disk state of a still-open SQLite database —
// `.sqlite`, `-wal` and `-shm` exactly as they are — reproducing what a process
// killed mid-flight leaves behind. It returns the path of the snapshot's database.
//
// This is not a contrived fixture: the SQLite activity store was OOM-killed on
// production on 2026-09-07, and a killed process never gets to checkpoint. A clean
// Close() folds the WAL back automatically, so a test that closes the store first
// cannot tell a correct implementation from one that ignores the WAL entirely.
func snapshotCrashedDB(t *testing.T, liveDB, intoDir string) string {
	t.Helper()
	if err := os.MkdirAll(intoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(intoDir, filepath.Base(liveDB))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		copyRaw(t, liveDB+suffix, snap+suffix)
	}
	return snap
}

// TestRelocateActivityDB_RecoversACrashedDatabaseIntact moves the on-disk state a
// killed process leaves behind — a `.sqlite` whose committed rows, and here even
// its schema, live only in the `-wal` — and requires every row on the far side.
// Production's SQLite activity store was OOM-killed on 2026-09-07, so this is the
// real starting state, not a contrived one.
//
// What this test does NOT prove: that the explicit CHECKPOINT(TRUNCATE) is what
// saves those frames. It is not. Closing the last connection checkpoints anyway,
// so this passes with the PRAGMA removed — measured, not assumed. The explicit
// checkpoint earns its place through the busy detection, which
// TestRelocateActivityDB_RefusesWhileAnotherConnectionHoldsTheDatabase covers.
// This one guards the end-to-end data-integrity claim.
func TestRelocateActivityDB_RecoversACrashedDatabaseIntact(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live", "activity.sqlite")
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}

	s := seedActivityDBAt(t, live, 500)
	src := snapshotCrashedDB(t, live, filepath.Join(dir, "crashed"))
	if err := s.Close(); err != nil { // only after the snapshot is taken
		t.Fatalf("close live store: %v", err)
	}

	// Prove the premise: the snapshot's main file must NOT already contain the
	// rows, or this test would pass against an implementation that never
	// checkpoints — which is exactly how its first version was worthless.
	walInfo, err := os.Stat(src + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("snapshot has no non-empty -wal (%v); fixture cannot prove the checkpoint matters", err)
	}
	bare := filepath.Join(dir, "bare.sqlite")
	copyRaw(t, src, bare) // the .sqlite WITHOUT its -wal
	// A count error here means the table itself is still only in the WAL, which
	// is the strongest form of the premise: the bare file carries nothing at all.
	n, cerr := checkpointAndCount(bare)
	if cerr != nil {
		n = 0
		t.Logf("premise verified: the bare .sqlite has no usable activity table yet (%v) — "+
			"schema and all 500 rows live only in the WAL", cerr)
	} else if n >= 500 {
		t.Fatalf("the snapshot's .sqlite already holds %d rows on its own; "+
			"the WAL carries nothing and this test proves nothing", n)
	} else {
		t.Logf("premise verified: .sqlite alone holds %d of 500 rows — %d live only in the WAL", n, 500-n)
	}

	dst := filepath.Join(dir, "moved", "activity.sqlite")
	if _, err := RelocateActivityDB(context.Background(), src, dst); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if got := countRowsAt(t, dst); got != 500 {
		t.Errorf("destination holds %d rows, want 500 — WAL frames were lost in the move", got)
	}

	// The sidecars must not have been carried across; SQLite recreates them.
	if _, err := os.Stat(dst + "-shm"); err == nil {
		t.Error("a -shm was carried to the destination; it is process-scoped scratch and must not be moved")
	}
}

// TestRelocateActivityDB_RefusesWhileAnotherConnectionHoldsTheDatabase is the test
// that actually discriminates the explicit checkpoint. With another connection
// holding the database, wal_checkpoint(TRUNCATE) reports busy=1 rather than
// failing — and a close-time checkpoint would skip in silence. Copying a database
// that something else is still writing is wrong however many frames this pass
// happened to catch, so the relocation must refuse and leave the source alone.
//
// Removing the `busy != 0` guard in checkpointAndCount makes this test fail.
func TestRelocateActivityDB_RefusesWhileAnotherConnectionHoldsTheDatabase(t *testing.T) {
	// The busy path is reached by waiting out the checkpoint's busy_timeout; the
	// production value would make this test a 30-second one.
	prev := relocateBusyTimeoutMS
	relocateBusyTimeoutMS = 250
	t.Cleanup(func() { relocateBusyTimeoutMS = prev })

	dir := t.TempDir()
	src := filepath.Join(dir, "activity.sqlite")
	dst := filepath.Join(dir, "moved", "activity.sqlite")

	// Deliberately left open for the duration: this is the "still in use" case.
	s := seedActivityDBAt(t, src, 200)
	defer func() { _ = s.Close() }()

	holder, err := sql.Open(sqliteDialect{}.driverName(),
		fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(1000)", src))
	if err != nil {
		t.Fatal(err)
	}
	holder.SetMaxOpenConns(1)
	defer func() { _ = holder.Close() }()

	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM activity").Scan(&n); err != nil {
		t.Fatalf("hold a read transaction open: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := RelocateActivityDB(context.Background(), src, dst); err == nil {
		t.Fatal("relocation succeeded while another connection held the database; " +
			"it must refuse rather than copy a database still in use")
	}

	// Nothing may have been left at the destination, and the source must be whole.
	for _, p := range []string{dst, dst + ".partial"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a refused relocation", p)
		}
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("source is missing after a refused relocation: %v", err)
	}
}

// TestRelocateActivityDB_RefusesToOverwriteAnExistingDestination guards against a
// misconfiguration silently destroying a database that is already in place.
func TestRelocateActivityDB_RefusesToOverwriteAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "activity.sqlite")
	dst := filepath.Join(dir, "existing.sqlite")

	s := seedActivityDBAt(t, src, 10)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	d := seedActivityDBAt(t, dst, 99)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := RelocateActivityDB(context.Background(), src, dst); err == nil {
		t.Fatal("expected a refusal when the destination already exists")
	}

	// Both databases must survive the refusal untouched.
	if got := countRowsAt(t, src); got != 10 {
		t.Errorf("source holds %d rows after the refusal, want 10", got)
	}
	if got := countRowsAt(t, dst); got != 99 {
		t.Errorf("destination holds %d rows after the refusal, want its original 99", got)
	}
}

// TestRelocateActivityDB_MissingSourceIsADistinctSignal — a first boot at a new
// path has nothing to move, which is normal and must be distinguishable from a
// failed move so the caller can just open the destination.
func TestRelocateActivityDB_MissingSourceIsADistinctSignal(t *testing.T) {
	dir := t.TempDir()
	_, err := RelocateActivityDB(context.Background(),
		filepath.Join(dir, "absent.sqlite"), filepath.Join(dir, "new.sqlite"))
	if !errors.Is(err, ErrRelocateSourceMissing) {
		t.Fatalf("err = %v, want ErrRelocateSourceMissing", err)
	}
}

// TestRelocateActivityDB_CancellationLeavesTheSourceIntact — a shutdown during a
// multi-minute copy must not cost the activity log. The partial destination is
// discarded and the source is left complete and openable.
func TestRelocateActivityDB_CancellationLeavesTheSourceIntact(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "activity.sqlite")
	dst := filepath.Join(dir, "moved.sqlite")

	s := seedActivityDBAt(t, src, 100)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: the copy must abort on its first chunk

	if _, err := RelocateActivityDB(ctx, src, dst); err == nil {
		t.Fatal("expected a cancelled relocation to fail")
	}
	if got := countRowsAt(t, src); got != 100 {
		t.Errorf("source holds %d rows after a cancelled move, want 100", got)
	}
	for _, p := range []string{dst, dst + ".partial"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a cancelled move; a partial copy must never be left behind", p)
		}
	}
}
