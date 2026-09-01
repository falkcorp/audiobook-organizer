// file: internal/itunes/backupname_test.go
// version: 1.0.0
// guid: 9a2e7c04-1b58-4f6d-b3c9-58e0d17a4b26
// last-edited: 2026-09-01

package itunes

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch writes a backup file named for t in the given historical layout and
// returns its base name. loc says which zone that layout was written in — the
// compact dashed form was produced with time.Now(), i.e. LOCAL.
func touch(t *testing.T, dir, base, layout string, when time.Time, loc *time.Location) string {
	t.Helper()
	name := base + BackupPrefix + when.In(loc).Format(layout)
	if err := os.WriteFile(filepath.Join(dir, name), []byte("itl"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return name
}

// TestRotateBackupsKeepsTheActuallyNewest is the regression test for the bug
// this file was written to fix. Three writers used three timestamp layouts in
// one directory, and both rotators sorted with sort.Strings. Lexically, '-'
// (0x2D) precedes every digit, so a dashed-RFC3339 name sorts before every
// compact name whatever its date — and the rotator deleted the newest backup
// while keeping one two months older.
//
// The fixture below is EXACTLY that arrangement: the newest backup wears the
// layout that sorts first as a string. A rotator that got this right by
// accident (because its fixture happened to be lexically ordered) could not
// fail this test.
func TestRotateBackupsKeepsTheActuallyNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "iTunes Library.itl")
	base := filepath.Base(path)
	if err := os.WriteFile(path, []byte("live"), 0o644); err != nil {
		t.Fatalf("write library: %v", err)
	}

	sept := time.Date(2026, 9, 1, 7, 14, 49, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	newest := touch(t, dir, base, BackupTimeLayout, sept, time.UTC)
	middle := touch(t, dir, base, "20060102-150405", aug, time.Local)
	oldest := touch(t, dir, base, "20060102T150405Z", july, time.UTC)

	if err := RotateBackups(path, 1); err != nil {
		t.Fatalf("RotateBackups: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, newest)); err != nil {
		t.Errorf("the NEWEST backup (%s) was deleted: %v", newest, err)
	}
	for _, gone := range []string{middle, oldest} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("older backup %s survived a keep=1 rotation", gone)
		}
	}
}

// TestListBackupsOrdersByParsedTimeNotByName pins the ordering directly, so a
// rotation change cannot quietly reintroduce lexical order while still
// satisfying a keep=1 assertion.
func TestListBackupsOrdersByParsedTimeNotByName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.itl")
	base := filepath.Base(path)

	sept := time.Date(2026, 9, 1, 7, 14, 49, 0, time.UTC)
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	legacyHardened := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	want := []string{
		touch(t, dir, base, BackupTimeLayout, sept, time.UTC),
		touch(t, dir, base, "20060102-150405", aug, time.Local),
		touch(t, dir, base, "20060102T150405Z", july, time.UTC),
		// The safe-write path's historical "...Z-00" spelling.
		touch(t, dir, base, "2006-01-02T15-04-05.000000000Z07-00", legacyHardened, time.UTC),
	}

	got, err := ListBackups(path)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d backups, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Dated {
			t.Errorf("backup %d (%s) did not parse under any known layout", i, got[i].Name)
			continue
		}
		if got[i].Name != want[i] {
			t.Errorf("position %d = %s, want %s (newest first, by parsed time)", i, got[i].Name, want[i])
		}
	}
}

// TestBackupNameRoundTrips guards the layout constant itself: the previous
// spelling emitted a stray literal "-00" because Z07-00 is not a zone unit Go
// recognises. Nothing caught it for the life of that constant because nothing
// ever parsed the names back.
func TestBackupNameRoundTrips(t *testing.T) {
	path := "/tmp/lib.itl"
	when := time.Date(2026, 9, 1, 7, 45, 19, 990625000, time.UTC)

	full := BackupName(path, when)
	name := filepath.Base(full)
	got, ok := ParseBackupTime(name, filepath.Base(path))
	if !ok {
		t.Fatalf("BackupName produced %q, which ParseBackupTime cannot read", name)
	}
	if !got.Equal(when) {
		t.Errorf("round-trip = %v, want %v", got, when)
	}
	if suffix := name[len(filepath.Base(path))+len(BackupPrefix):]; suffix[len(suffix)-1] != 'Z' {
		t.Errorf("UTC stamp %q does not end in Z — the zone specifier is emitting literal text again", suffix)
	}
}

func TestRotateBackupsNeverTouchesThePinnedAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.itl")
	base := filepath.Base(path)
	lkg := filepath.Join(dir, base+PinnedBackupName)
	if err := os.WriteFile(lkg, []byte("last known good"), 0o644); err != nil {
		t.Fatalf("write lkg: %v", err)
	}
	for i := range 5 {
		touch(t, dir, base, BackupTimeLayout, time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), time.UTC)
	}

	if err := RotateBackups(path, 1); err != nil {
		t.Fatalf("RotateBackups: %v", err)
	}
	if _, err := os.Stat(lkg); err != nil {
		t.Errorf("the pinned last-known-good anchor was rotated away: %v", err)
	}
}

// TestRotateBackupsKeepsUndatableBackups: a name matching no known layout has
// no establishable age, and deleting it is how a rotation bug becomes data
// loss. Keeping it is the fail-safe direction.
func TestRotateBackupsKeepsUndatableBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.itl")
	base := filepath.Base(path)

	mystery := base + BackupPrefix + "handwritten-by-an-operator"
	if err := os.WriteFile(filepath.Join(dir, mystery), []byte("?"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := range 4 {
		touch(t, dir, base, BackupTimeLayout, time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), time.UTC)
	}

	if err := RotateBackups(path, 1); err != nil {
		t.Fatalf("RotateBackups: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, mystery)); err != nil {
		t.Errorf("an undatable backup was deleted: %v", err)
	}
}

// TestRotateBackupsZeroKeepIsANoOp: keep=0 must not mean "delete everything".
// A zero arriving from an unset config value has erased data in this codebase
// before (chapter_consolidation_threshold_min).
func TestRotateBackupsZeroKeepIsANoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lib.itl")
	base := filepath.Base(path)
	var names []string
	for i := range 3 {
		names = append(names, touch(t, dir, base, BackupTimeLayout,
			time.Date(2026, 1, i+1, 0, 0, 0, 0, time.UTC), time.UTC))
	}

	if err := RotateBackups(path, 0); err != nil {
		t.Fatalf("RotateBackups: %v", err)
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("keep=0 deleted %s; it must be a no-op: %v", n, err)
		}
	}
}

// TestParseBackupTimeReadsCompactStampAsLocal: writeback_batcher wrote its
// stamp with time.Now(), so it is LOCAL. Parsing it as UTC shifts it by the
// machine's offset and can reorder backups taken hours apart.
func TestParseBackupTimeReadsCompactStampAsLocal(t *testing.T) {
	base := "lib.itl"
	when := time.Date(2026, 8, 1, 13, 30, 0, 0, time.Local)
	name := base + BackupPrefix + when.Format("20060102-150405")

	got, ok := ParseBackupTime(name, base)
	if !ok {
		t.Fatalf("ParseBackupTime(%q) failed", name)
	}
	if !got.Equal(when) {
		t.Errorf("parsed %v, want %v — the compact layout must be read in the local zone", got, when)
	}
}

func TestParseBackupTimeRejectsNonBackups(t *testing.T) {
	base := "lib.itl"
	for _, name := range []string{
		base,                    // the library itself
		base + PinnedBackupName, // the pinned anchor carries no date
		"other.itl" + BackupPrefix + "2026-09-01T07-14-49.000000000Z", // another library
		base + BackupPrefix, // empty stamp
	} {
		if _, ok := ParseBackupTime(name, base); ok {
			t.Errorf("ParseBackupTime(%q) reported a timestamp; it must not", name)
		}
	}
}
