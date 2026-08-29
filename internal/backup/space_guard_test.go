// file: internal/backup/space_guard_test.go
// version: 1.2.0
// guid: 3b6d0f27-58c1-49ea-a704-1f8e2d95c6b3
// last-edited: 2026-08-29

package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withDiskStats swaps the free-space probe for the duration of a test.
func withDiskStats(t *testing.T, total, free uint64, err error) {
	t.Helper()
	prev := diskStatsFn
	t.Cleanup(func() { diskStatsFn = prev })
	diskStatsFn = func(string) (uint64, uint64, error) { return total, free, err }
}

// seedDB writes a fake database directory of roughly size bytes.
func seedDB(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001.sst"), make([]byte, size), 0o600); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return dir
}

func TestDirSizeBytes_SumsRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := dirSizeBytes(dir)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if got != 350 {
		t.Fatalf("got %d, want 350 (directories must not contribute)", got)
	}
}

// THE test for this change. A guard that refuses only after os.Create has
// already consumed space on a full filesystem has not prevented anything --
// that is precisely the production failure. Assert the directory stays empty.
func TestCreateBackup_RefusesAndWritesNothingWhenSpaceShort(t *testing.T) {
	db := seedDB(t, 4096)
	backupDir := filepath.Join(t.TempDir(), "backups")
	withDiskStats(t, 1<<40, 1024, nil) // 1 KiB free, far under the archive + margin

	cfg := DefaultBackupConfig()
	cfg.BackupDir = backupDir

	info, err := CreateBackup(db, "test", cfg)
	if err == nil {
		t.Fatal("expected refusal, got a successful backup")
	}
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("wrong error: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info on refusal, got %+v", info)
	}

	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read backup dir: %v", readErr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			t.Fatalf("guard refused but still created %s -- it consumed space anyway", e.Name())
		}
	}
}

// The error must name the actual numbers; an operator reading it at 3am needs
// to know how short they are, not merely that something failed.
func TestCreateBackup_RefusalErrorReportsSizes(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 1<<40, 1024, nil)
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	_, err := CreateBackup(db, "test", cfg)
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"free", "margin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// Fail-closed: an unmeasurable filesystem must not be written to blind.
func TestCreateBackup_RefusesWhenFreeSpaceUnknown(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 0, 0, errors.New("statfs exploded"))
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	_, err := CreateBackup(db, "test", cfg)
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("expected fail-closed refusal, got %v", err)
	}
}

// Ample space must still work -- a guard that refuses everything is useless.
func TestCreateBackup_ProceedsWhenSpaceAmple(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 1<<40, 1<<40, nil)
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	info, err := CreateBackup(db, "test", cfg)
	if err != nil {
		t.Fatalf("backup refused despite ample space: %v", err)
	}
	if info == nil || info.Size == 0 {
		t.Fatalf("expected a real archive, got %+v", info)
	}
}

// --- retention -------------------------------------------------------------

// writeArchives lays down n fake .tar.gz files of the given size, oldest first.
func writeArchives(t *testing.T, dir string, n, size int) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, "audiobooks_test_"+string(rune('a'+i))+".tar.gz")
		if err := os.WriteFile(name, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func remaining(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			n++
		}
	}
	return n
}

// The production bug: a COUNT bound cannot express "do not fill the disk" once
// the counted files grow. Ten 15 GB archives satisfy MaxBackups=10 and consume
// 150 GB on a 141 GB filesystem.
func TestEnforceRetention_TotalBytesBoundEvictsBeyondCountBound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 5, 1000) // 5 files, 5000 bytes, count bound satisfied

	if err := enforceRetention(dir, 10, 2500, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}

	if got := remaining(t, dir); got != 2 {
		t.Fatalf("expected 2 archives under a 2500-byte cap, got %d", got)
	}
}

func TestEnforceRetention_EvictsOldestFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	names := writeArchives(t, dir, 4, 1000)

	if err := enforceRetention(dir, 2, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}

	for _, gone := range names[:2] {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected oldest %s to be evicted", filepath.Base(gone))
		}
	}
	for _, kept := range names[2:] {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected newest %s to survive: %v", filepath.Base(kept), err)
		}
	}
}

// incomingBytes reserves room for the archive about to be written, which is why
// pruning before the write can make a backup fit that would otherwise fail.
func TestEnforceRetention_ReservesRoomForIncoming(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 2, 1000) // 2000 bytes present

	// Cap 2500, incoming 1000: 2000+1000 > 2500, so one must go.
	if err := enforceRetention(dir, 10, 2500, 1000); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 1 {
		t.Fatalf("expected 1 archive after reserving for incoming, got %d", got)
	}
}

// MaxTotalBytes uses the ordinary Go zero convention (unlimited). Making it mean
// "keep nothing" would turn every pre-existing BackupConfig literal into a
// silent delete-everything.
func TestEnforceRetention_ZeroTotalBytesMeansUnlimited(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 3, 1_000_000)

	if err := enforceRetention(dir, -1, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 3 {
		t.Fatalf("zero MaxTotalBytes must not evict; got %d of 3", got)
	}
}

// A negative count bound means unlimited. The old implementation used
// len(backups)-maxBackups as a loop bound, so this indexed past the slice.
func TestEnforceRetention_NegativeCountIsUnlimitedNotPanic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 3, 100)

	if err := enforceRetention(dir, -1, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 3 {
		t.Fatalf("negative count must keep everything; got %d of 3", got)
	}
}

// The margin is the load-bearing half of the guard and needs its own test:
// every other refusal case fails on the archive size alone, so zeroing the
// margin would pass all of them.
//
// Why it matters: the database stays LIVE for the 20-25 minutes the archive
// takes on production. Sizing the check at exactly the archive size lets the
// backup succeed and still leave Pebble with no room for the WAL writes and
// compactions it performs meanwhile -- which is the fatal commit error that
// killed the process on 2026-08-29.
func TestCreateBackup_RefusesWhenOnlyTheMarginIsMissing(t *testing.T) {
	db := seedDB(t, 4096)
	// Comfortably larger than the archive, but inside the margin.
	withDiskStats(t, 1<<40, 1<<30, nil)

	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	_, err := CreateBackup(db, "test", cfg)
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("free space exceeded the archive but not the margin; expected refusal, got %v", err)
	}
}

// ...and the margin must not be so eager that it refuses a backup with real
// room, which is the failure mode of over-correcting the test above.
func TestCreateBackup_ProceedsJustAboveTheMargin(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 1<<40, backupSpaceMargin+(1<<20), nil)

	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	if _, err := CreateBackup(db, "test", cfg); err != nil {
		t.Fatalf("refused despite clearing archive + margin: %v", err)
	}
}

// stubCheckpointer records whether Checkpoint was reached. It never needs to
// run for the test below -- the point is that retention happens BEFORE the
// space check, which sits before the checkpoint.
type stubCheckpointer struct{ called bool }

func (s *stubCheckpointer) Checkpoint(destDir string) error {
	s.called = true
	return os.MkdirAll(destDir, 0o755)
}

// seedArchive writes a fake .tar.gz of the given size and backdates it, so
// retention's oldest-first ordering is deterministic.
func seedArchive(t *testing.T, dir, name string, size int, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return p
}

// THE regression test for the 2026-08-29 production finding.
//
// The checkpoint path is the production path, and it ran its space check before
// any retention. So when the disk was genuinely full the guard refused -- which
// correctly kept the database alive -- but the old archives that CAUSED the
// shortage were never pruned. Every later attempt refused identically. The
// system was stable and could never dig itself out; only a human with rm could.
//
// Retention must therefore run even on the path that then refuses. Asserting
// only "it returned ErrInsufficientDiskSpace" would pass in both worlds, which
// is exactly how this shipped: assert the PRUNE happened.
func TestCreateBackupWithCheckpoint_PrunesBeforeRefusing(t *testing.T) {
	db := seedDB(t, 4096)
	backupDir := t.TempDir()

	oldest := seedArchive(t, backupDir, "audiobooks_pebble_20260101_000000.tar.gz", 8000, 72*time.Hour)
	middle := seedArchive(t, backupDir, "audiobooks_pebble_20260102_000000.tar.gz", 8000, 48*time.Hour)
	newest := seedArchive(t, backupDir, "audiobooks_pebble_20260103_000000.tar.gz", 8000, 24*time.Hour)

	// Far too little free space: the guard must refuse after pruning.
	withDiskStats(t, 1<<30, 1024, nil)

	cp := &stubCheckpointer{}
	_, err := CreateBackupWithCheckpoint(cp, db, "pebble", BackupConfig{
		BackupDir: backupDir,
		// Negative == unlimited, so the COUNT bound cannot be what prunes here
		// and the assertion below is unambiguously about the byte bound.
		MaxBackups:    -1,
		MaxTotalBytes: 10000,
	})

	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("want ErrInsufficientDiskSpace, got %v", err)
	}
	if cp.called {
		t.Error("checkpoint ran despite insufficient space; the guard must precede it")
	}

	// The prune must have happened anyway -- that is the whole point.
	if _, statErr := os.Stat(oldest); !os.IsNotExist(statErr) {
		t.Error("oldest archive survived: retention did not run before the refusal, " +
			"so a full disk can never be recovered from")
	}
	if _, statErr := os.Stat(middle); !os.IsNotExist(statErr) {
		t.Error("middle archive survived: retention did not prune far enough to fit the incoming archive")
	}
	// The floor: even an unsatisfiable budget must leave one archive behind.
	if _, statErr := os.Stat(newest); statErr != nil {
		t.Errorf("newest archive was deleted (%v); retention must never leave zero backups", statErr)
	}
}

// Retention must never leave the database with no backup at all.
//
// The byte bound can be unsatisfiable: production on 2026-08-29 had a 30.3 GiB
// incoming archive, a 40 GiB budget and ~15 GB archives, so no existing archive
// could be retained and the loop would have deleted every one. Deleting the
// whole history to make room for a write that may itself fail is a worse
// outcome than being over budget.
func TestEnforceRetention_KeepsTheLastArchiveWhenTheBudgetIsUnsatisfiable(t *testing.T) {
	dir := t.TempDir()
	a := seedArchive(t, dir, "audiobooks_pebble_20260101_000000.tar.gz", 8000, 72*time.Hour)
	b := seedArchive(t, dir, "audiobooks_pebble_20260102_000000.tar.gz", 8000, 48*time.Hour)
	c := seedArchive(t, dir, "audiobooks_pebble_20260103_000000.tar.gz", 8000, 24*time.Hour)

	// Budget smaller than one archive plus the incoming one: unsatisfiable.
	if err := enforceRetention(dir, -1, 10000, 4096); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}

	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived; retention should have pruned it", filepath.Base(p))
		}
	}
	if _, err := os.Stat(c); err != nil {
		t.Fatalf("the newest archive was deleted: retention must never leave zero backups (%v)", err)
	}
}

// The explicit "keep none" case still means none. maxBackups == 0 is
// pre-existing behaviour and the floor must not quietly resurrect an archive.
func TestEnforceRetention_MaxBackupsZeroStillDeletesEverything(t *testing.T) {
	dir := t.TempDir()
	a := seedArchive(t, dir, "audiobooks_pebble_20260101_000000.tar.gz", 100, 48*time.Hour)
	b := seedArchive(t, dir, "audiobooks_pebble_20260102_000000.tar.gz", 100, 24*time.Hour)

	if err := enforceRetention(dir, 0, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived a maxBackups=0 sweep", filepath.Base(p))
		}
	}
}

// ResolveDir is the single answer to "where do backups live". Five call sites
// used to each carry their own copy of this rule; a disagreement between the
// create path and the list path would surface as "the backup succeeded but the
// list is empty", which is a confusing way to discover you have no backups.
func TestResolveDir(t *testing.T) {
	dbPath := "/var/lib/audiobook-organizer/audiobooks.pebble"
	for _, tc := range []struct {
		name       string
		configured string
		dbPath     string
		want       string
	}{
		{
			name:       "unset keeps the historical behaviour",
			configured: "",
			dbPath:     dbPath,
			want:       "/var/lib/audiobook-organizer/backups",
		},
		{
			name:       "an absolute configured path wins outright",
			configured: "/mnt/bigdata/books/audiobook-organizer/.backups",
			dbPath:     dbPath,
			want:       "/mnt/bigdata/books/audiobook-organizer/.backups",
		},
		{
			// The whole point of the change: the configured path must be able
			// to leave the database's filesystem entirely.
			name:       "the configured path need not be near the database",
			configured: "/somewhere/else",
			dbPath:     dbPath,
			want:       "/somewhere/else",
		},
		{
			// A relative path is resolved against the DATABASE, never the
			// process working directory -- for a service that is wherever
			// systemd started it, which nobody meant to fill with archives.
			name:       "a relative configured path hangs off the database dir",
			configured: "snapshots",
			dbPath:     dbPath,
			want:       "/var/lib/audiobook-organizer/snapshots",
		},
		{
			name:       "no database path falls back to the bare name",
			configured: "",
			dbPath:     "",
			want:       "backups",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveDir(tc.configured, tc.dbPath); got != tc.want {
				t.Errorf("ResolveDir(%q, %q) = %q, want %q", tc.configured, tc.dbPath, got, tc.want)
			}
		})
	}
}
