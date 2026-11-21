// file: internal/books/roots/reader_internal_test.go
// version: 0.1.0
// guid: 7b7d4823-f95e-4a1c-8c17-8694ee72505a
// last-edited: 2026-11-20

package roots

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"
)

var testNow = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func fixtureRoots() []Root {
	return []Root{
		{ID: BooksLibraryRoot, Name: "Wayne Manor Library", Path: "/srv/gotham/library", Role: library.RoleLibrary, Watch: library.WatchOff},
		{ID: "import-2", Name: "batcave drop", Path: "/srv/gotham/in/batcave", Role: library.RoleImport, Watch: library.WatchAuto},
		{ID: "import-1", Name: "Arkham intake", Path: "/srv/gotham/in/arkham", Role: library.RoleImport, Watch: library.WatchAuto},
		{ID: "import-3", Name: "Crime Alley", Path: "/srv/gotham/in/alley", Role: library.RoleImport, Watch: library.WatchOff},
		{ID: "gcpd-share", Name: "GCPD share", Path: "/srv/gotham/in/gcpd", Role: library.RoleImport, Watch: library.WatchAuto, Disabled: errors.New("not mounted")},
		{ID: "dl-1", Name: "downloads", Path: "/srv/gotham/dl", Role: library.RoleDownload, Watch: library.WatchAuto},
	}
}

func fixtureReader(t *testing.T, list []Root) (*Reader, *ScanStore) {
	t.Helper()
	rows := &mapRows{rows: map[string][]byte{}}
	scans := NewScanStore(rows)
	return newReader(func() []Root { return list }, scans), scans
}

func TestReader_ImportRootsJoinScanStateAndSortLikeHEAD(t *testing.T) {
	ctx := context.Background()
	r, scans := fixtureReader(t, fixtureRoots())
	if err := scans.RecordScan(ctx, "import-1", 12, testNow); err != nil {
		t.Fatal(err)
	}
	if err := scans.SetEnabled(ctx, "import-2", false); err != nil {
		t.Fatal(err)
	}
	got, err := r.ImportRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ir := range got {
		names = append(names, ir.Name)
	}
	want := []string{"Arkham intake", "batcave drop", "Crime Alley", "GCPD share"} // lower-cased name order
	if len(names) != len(want) {
		t.Fatalf("names = %v; want %v (import roots only)", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v; want %v", names, want)
		}
	}
	ark := got[0]
	if ark.ID != 1 || !ark.HasScan || !ark.Enabled || ark.BookCount != 12 || ark.LastScan == nil || !ark.LastScan.Equal(testNow) {
		t.Errorf("import-1 = %+v", ark)
	}
	if bat := got[1]; bat.ID != 2 || bat.Enabled || !bat.HasScan {
		t.Errorf("import-2 (row says disabled) = %+v", bat)
	}
	// No row: Enabled is whether library watches, nothing else is invented.
	if alley := got[2]; alley.HasScan || alley.Enabled || !alley.CreatedAt.IsZero() || alley.BookCount != 0 {
		t.Errorf("import-3 (no row, watch off) = %+v", alley)
	}
	if gcpd := got[3]; gcpd.ID != 0 || gcpd.RootID != "gcpd-share" || !gcpd.Enabled || gcpd.Disabled == nil {
		t.Errorf("gcpd-share (not an import-<n> ID, disabled) = %+v", gcpd)
	}
}

func TestReader_BadScanRowIsReportedPerRootAndHidesNothing(t *testing.T) {
	ctx := context.Background()
	r, scans := fixtureReader(t, fixtureRoots())
	_ = scans.rows.Set(ctx, []byte("rootscan:import-1"), []byte(`nope`))
	got, err := r.ImportRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || !errors.Is(got[0].ScanErr, ErrBadScanRow) || got[0].HasScan {
		t.Errorf("import-1 with a bad row = %+v (all %d)", got[0], len(got))
	}
}

func TestReader_LibraryRootAndRootDir(t *testing.T) {
	r, _ := fixtureReader(t, fixtureRoots())
	rt, err := r.LibraryRoot()
	if err != nil || rt.ID != BooksLibraryRoot {
		t.Fatalf("LibraryRoot = %+v, %v", rt, err)
	}
	if got := r.RootDir(); got != "/srv/gotham/library" {
		t.Errorf("RootDir = %q", got)
	}
	empty, _ := fixtureReader(t, nil)
	if _, err := empty.LibraryRoot(); !errors.Is(err, ErrNoLibraryRoot) {
		t.Errorf("LibraryRoot with no roots = %v; want ErrNoLibraryRoot", err)
	}
	if got := empty.RootDir(); got != "" {
		t.Errorf("RootDir with no roots = %q; want the empty HEAD meaning", got)
	}
	// An import root named books-library is not the library root.
	wrongRole, _ := fixtureReader(t, []Root{{ID: BooksLibraryRoot, Path: "/x", Role: library.RoleImport}})
	if got := wrongRole.RootDir(); got != "" {
		t.Errorf("RootDir of a books-library import root = %q; want empty", got)
	}
}

func TestReader_Lookups(t *testing.T) {
	ctx := context.Background()
	r, _ := fixtureReader(t, fixtureRoots())
	if ir, err := r.ImportRootByLegacyID(ctx, 2); err != nil || ir.Name != "batcave drop" {
		t.Errorf("ImportRootByLegacyID(2) = %+v, %v", ir, err)
	}
	if ir, err := r.ImportRootByPath(ctx, "/srv/gotham/in/arkham/"); err != nil || ir.RootID != "import-1" {
		t.Errorf("ImportRootByPath with a trailing slash = %+v, %v", ir, err)
	}
	for _, f := range []func() error{
		func() error { _, err := r.ImportRootByLegacyID(ctx, 99); return err },
		func() error { _, err := r.ImportRootByPath(ctx, "/srv/gotham/library"); return err }, // the library root is not an import root
		func() error { _, err := r.ImportRoot(ctx, "dl-1"); return err },
	} {
		if err := f(); !errors.Is(err, ErrNoSuchRoot) {
			t.Errorf("lookup of a missing import root = %v; want ErrNoSuchRoot", err)
		}
	}
}

func TestReader_WatchRootsAreEnabledWatchedAndMounted(t *testing.T) {
	ctx := context.Background()
	r, scans := fixtureReader(t, fixtureRoots())
	if err := scans.SetEnabled(ctx, "import-2", false); err != nil {
		t.Fatal(err)
	}
	got, err := r.WatchRoots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// import-1: enabled+auto. import-2: row disabled. import-3: watch off.
	// gcpd-share: disabled by library.
	if len(got) != 1 || got[0] != "import-1" {
		t.Errorf("WatchRoots = %v; want [import-1]", got)
	}
}

func TestReader_NextImportNCountsRootsAndRows(t *testing.T) {
	ctx := context.Background()
	r, scans := fixtureReader(t, fixtureRoots())
	if n, err := r.NextImportN(ctx); err != nil || n != 4 {
		t.Fatalf("NextImportN = %d, %v; want 4", n, err)
	}
	_ = scans.RecordScan(ctx, "import-9", 1, testNow) // an orphan row
	if n, _ := r.NextImportN(ctx); n != 10 {
		t.Errorf("NextImportN with an orphan row = %d; want 10", n)
	}
}
