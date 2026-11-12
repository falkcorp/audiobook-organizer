// file: core/backup/hardening_test.go
// version: 1.0.0
// guid: 2f6b8c14-9d37-4a05-b1e2-7c3a05d8e946
// last-edited: 2026-11-12

package backup_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// ---- fixtures ---------------------------------------------------------------

// sumSizes is the sum of the sizes the entries under prefix declare.
func sumSizes(ents []backup.Entry, prefix string) uint64 {
	var n uint64
	for _, en := range ents {
		if strings.HasPrefix(en.Path, prefix) {
			n += uint64(en.Size)
		}
	}
	return n
}

// fixedAvail is a Disk that says its filesystem has n bytes free wherever it is
// asked (the device question is the operating system's).
func fixedAvail(n uint64) backup.Disk {
	return backup.Disk{Avail: func(string) (uint64, error) { return n, nil }}
}

func readFileT(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// paxRec is one PAX extended-header record, "<length> <key>=<value>\n", the
// length counting its own digits.
func paxRec(k, v string) string {
	body := " " + k + "=" + v + "\n"
	n := len(body)
	total := n + len(strconv.Itoa(n))
	if len(strconv.Itoa(total)) != len(strconv.Itoa(n)) {
		total++
	}
	return strconv.Itoa(total) + body
}

// tarBlock is one 512-byte ustar header written by hand: archive/tar's Writer
// drops GNU.sparse records from a regular header, so a sparse entry cannot be
// made with it.
func tarBlock(name string, typ byte, size int64) []byte {
	b := make([]byte, 512)
	copy(b[0:100], name)
	copy(b[100:108], "0000644\x00")
	copy(b[108:116], "0000000\x00")
	copy(b[116:124], "0000000\x00")
	copy(b[124:136], fmt.Sprintf("%011o\x00", size))
	copy(b[136:148], "00000000000\x00")
	copy(b[148:156], "        ")
	b[156] = typ
	copy(b[257:265], "ustar\x0000")
	sum := 0
	for _, c := range b {
		sum += int(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

func padBlock(b []byte) []byte {
	if r := len(b) % 512; r != 0 {
		b = append(b, make([]byte, 512-r)...)
	}
	return b
}

// sparseEntryBytes is one PAX 0.1 sparse file: five bytes of data at offset zero
// and a logical size of logical, which a tar reader serves as logical bytes.
func sparseEntryBytes(name string, logical int64) []byte {
	recs := paxRec("GNU.sparse.major", "0") + paxRec("GNU.sparse.minor", "1") +
		paxRec("GNU.sparse.numblocks", "1") + paxRec("GNU.sparse.map", "0,5") +
		paxRec("GNU.sparse.name", name) + paxRec("GNU.sparse.realsize", strconv.FormatInt(logical, 10))
	var raw []byte
	raw = append(raw, tarBlock("PaxHeaders.0/sparse", 'x', int64(len(recs)))...)
	raw = append(raw, padBlock([]byte(recs))...)
	raw = append(raw, tarBlock(name, '0', 5)...)
	raw = append(raw, padBlock([]byte("wayne"))...)
	return raw
}

func writeTarFile(t *testing.T, name string, parts ...[]byte) string {
	t.Helper()
	raw := bytes.Join(parts, nil)
	raw = append(raw, make([]byte, 1024)...)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// coreSparseArchive is a mediacore-backup/1 archive whose manifest is well
// formed and lists db/000001.sst at logical bytes, and whose one entry is a
// sparse file of exactly that logical size: every check the manifest makes
// passes, and only the entry's type is wrong.
func coreSparseArchive(t *testing.T, logical int64) string {
	t.Helper()
	id, err := backup.NewArchiveID(time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	m := &backup.Manifest{
		Format: backup.FormatMediacore, ArchiveID: id, App: "gotham", AppVersion: "1.0.0", CoreVersion: "0.9.0",
		InstallID: "install-batcave", CreatedAt: time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC), Reason: "manual",
		Levels: map[string]int{"core": 1}, Keyspaces: map[string]backup.KSInfo{}, Modules: map[string]string{},
		Entries: []backup.Entry{{Path: "db/000001.sst", Size: logical, SHA256: strings.Repeat("0", 64), Class: backup.SystemOfRecord}},
	}
	mb, err := backup.MarshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	return writeTarFile(t, "core-sparse.tar",
		tarBlock(backup.ManifestName, '0', int64(len(mb))), padBlock(mb),
		sparseEntryBytes("db/000001.sst", logical))
}

func noTemporaryFiles(t *testing.T, e *env) {
	t.Helper()
	for _, n := range e.names() {
		if strings.Contains(n, ".partial") || strings.Contains(n, "import-") || strings.HasSuffix(n, ".tmp") {
			t.Fatalf("a temporary file was left behind: %s", n)
		}
	}
}

// ---- sparse files -------------------------------------------------------------

// A sparse entry arrives as a regular file of its logical size; the archive that
// carries it is a few kilobytes. It is refused, by Import, whatever its first
// entry, before a byte of it is written.
func TestImportRefusesSparseArchives(t *testing.T) {
	const logical = int64(32) << 20
	for name, p := range map[string]string{
		"a sparse first entry":                   writeTarFile(t, "first.tar", sparseEntryBytes("db/000001.sst", logical)),
		"a sparse file the manifest vouches for": coreSparseArchive(t, logical),
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			f, err := os.Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, err = e.svc.Import(context.Background(), "", f)
			if !errors.Is(err, backuparchive.ErrEntryType) {
				t.Fatalf("Import = %v, want ErrEntryType", err)
			}
			if l, _ := e.svc.List(context.Background()); len(l) != 0 {
				t.Fatalf("a sparse archive was catalogued: %+v", l)
			}
			noTemporaryFiles(t, e)
		})
	}
}

func TestExtractStoreRefusesASparseEntryTheManifestVouchesFor(t *testing.T) {
	const logical = int64(32) << 20
	p := coreSparseArchive(t, logical)
	dir := emptyDir(t)
	// The free-space check is out of the way (a disk with room for the logical
	// size): the entry's type is what is refused.
	_, err := backup.ExtractStoreWith(t.Context(), p, dir, fixedAvail(math.MaxUint64))
	if !errors.Is(err, backuparchive.ErrEntryType) {
		t.Fatalf("ExtractStore = %v, want ErrEntryType", err)
	}
	if got := extracted(t, dir); len(got) != 0 {
		t.Fatalf("a sparse entry left %v", got)
	}
}

// ---- free space ---------------------------------------------------------------

// The open level extracts the archive's store beside the database. When the
// declared bytes plus the margin do not fit it is refused before a byte is
// written, and nothing is recorded: a shortage of space is not a verdict on the
// archive.
func TestVerifyOpenRefusesWhenTheStoreDoesNotFit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	db := sumSizes(a.Manifest.Entries, "db/")
	if db == 0 {
		t.Fatal("fixture: the checkpoint holds no bytes")
	}
	sidecar := e.path(a) + backup.VerifySidecar
	before := readFileT(t, sidecar)

	e.opts.Disk = fixedAvail(db) // the margin is 1 byte: db+1 is needed
	e.rebuild()
	res, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen)
	if !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("Verify = %+v, %v; want ErrInsufficientSpace", res, err)
	}
	if after := readFileT(t, sidecar); !bytes.Equal(before, after) {
		t.Fatalf("a shortage of space was recorded as a verdict: %s", after)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".backup-verify")); len(ents) != 0 {
		t.Fatalf("a refused verification left %d scratch entries", len(ents))
	}
	// The entries level extracts nothing and needs no room.
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyEntries); err != nil {
		t.Fatalf("the entries level was refused for space: %v", err)
	}

	e.opts.Disk = fixedAvail(db + 1)
	e.rebuild()
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen); err != nil {
		t.Fatalf("Verify with exactly the room it needs: %v", err)
	}
}

// Staging writes every entry and the manifest: all of it must fit, margin
// included, before the first byte (the bound is exact: one byte short refuses,
// the exact amount stages).
func TestStageRefusesWhenTheDeclaredBytesDoNotFit(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	a := e.create("manual")
	total := sumSizes(a.Manifest.Entries, "") + uint64(len(readFileT(t, e.path(a)+backup.ManifestSidecar)))
	opts := backup.RestoreOptions{Verify: backup.VerifyEntries, AllowWeakVerify: true}

	e.opts.Disk = fixedAvail(total) // the margin is 1 byte
	e.rebuild()
	if _, err := e.svc.StageRestore(ctx, a.ID, opts); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("StageRestore with one byte too few = %v, want ErrInsufficientSpace", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("an intent was created for a restore that does not fit")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("the refused restore left %d staging entries", len(ents))
	}

	e.opts.Disk = fixedAvail(total + 1)
	e.rebuild()
	if _, err := e.svc.StageRestore(ctx, a.ID, opts); err != nil {
		t.Fatalf("StageRestore with exactly the room it needs: %v", err)
	}
}

func TestLegacyStageRefusesWhenTheScannedBytesDoNotFit(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	name := legacyArchive(t, e, "gzip", true)
	info, err := backuparchive.ScanLegacy(ctx, filepath.Join(e.backupDir, name), backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	opts := backup.RestoreOptions{Verify: backup.VerifyEntries, AllowWeakVerify: true, Foreign: true}

	e.opts.Disk = fixedAvail(uint64(info.Bytes)) // margin 1: one byte short
	e.rebuild()
	if _, err := e.svc.StageRestore(ctx, list[0].ID, opts); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("StageRestore of a legacy archive with one byte too few = %v, want ErrInsufficientSpace", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("an intent was created for a restore that does not fit")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("the refused restore left %d staging entries", len(ents))
	}
	e.opts.Disk = fixedAvail(uint64(info.Bytes) + 1)
	e.rebuild()
	if _, err := e.svc.StageRestore(ctx, list[0].ID, opts); err != nil {
		t.Fatalf("StageRestore with exactly the room it needs: %v", err)
	}
}

// The re-signing extracts every entry into the staging root and writes the new
// archive into the backup directory. Both are on the database's device here, so
// the two claims add up: twice the entries, not once.
func TestRewrapRefusesWhenBothCopiesDoNotFit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", "d2")
	e.rebuild()
	old := e.create("manual")
	e.keys.active = "d2"
	declared := sumSizes(old.Manifest.Entries, "")

	e.opts.Disk = fixedAvail(2 * declared) // margin 1: 2*declared+1 is needed
	e.rebuild()
	if _, err := e.svc.Rewrap(ctx, old.ID); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("Rewrap with one byte too few = %v, want ErrInsufficientSpace", err)
	}
	if list, _ := e.svc.List(ctx); len(list) != 1 || list[0].ID != old.ID {
		t.Fatalf("a refused rewrap changed the catalogue: %+v", list)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".backup-staging")); len(ents) != 0 {
		t.Fatalf("a refused rewrap left %d staging entries", len(ents))
	}
	e.opts.Disk = fixedAvail(2*declared + 1)
	e.rebuild()
	if _, err := e.svc.Rewrap(ctx, old.ID); err != nil {
		t.Fatalf("Rewrap with exactly the room it needs: %v", err)
	}
}

// The offline restore has no settings and keeps no margin: the archive's
// declared bytes are what must fit.
func TestExtractStoreRefusesWhenTheDeclaredBytesDoNotFit(t *testing.T) {
	e := newEnv(t)
	a := e.create("manual")
	db := sumSizes(a.Manifest.Entries, "db/")
	if db == 0 {
		t.Fatal("fixture: the checkpoint holds no bytes")
	}
	dir := emptyDir(t)
	if _, err := backup.ExtractStoreWith(t.Context(), e.path(a), dir, fixedAvail(db-1)); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("ExtractStore with one byte too few = %v, want ErrInsufficientSpace", err)
	}
	if got := extracted(t, dir); len(got) != 0 {
		t.Fatalf("a refused extraction left %v", got)
	}
	if _, err := backup.ExtractStoreWith(t.Context(), e.path(a), dir, fixedAvail(db)); err != nil {
		t.Fatalf("ExtractStore with exactly the room it needs: %v", err)
	}

	name := legacyArchive(t, e, "gzip", true)
	path := filepath.Join(e.backupDir, name)
	info, err := backuparchive.ScanLegacy(t.Context(), path, backuparchive.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	ldir := emptyDir(t)
	if _, err := backup.ExtractStoreWith(t.Context(), path, ldir, fixedAvail(uint64(info.Bytes)-1)); !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("legacy ExtractStore with one byte too few = %v, want ErrInsufficientSpace", err)
	}
	if got := extracted(t, ldir); len(got) != 0 {
		t.Fatalf("a refused legacy extraction left %v", got)
	}
	if _, err := backup.ExtractStoreWith(t.Context(), path, ldir, fixedAvail(uint64(info.Bytes))); err != nil {
		t.Fatalf("legacy ExtractStore with exactly the room it needs: %v", err)
	}
}

// ---- manifest text ------------------------------------------------------------

func TestParseManifestRefusesWhatNoArchiveOfOursCarries(t *testing.T) {
	keys := newBruceKeys("d1", "d1")
	parse := func(t *testing.T, edit func(*backup.Manifest)) error {
		t.Helper()
		m := manifestFixture(t, keys)
		edit(m)
		b, err := backup.MarshalManifest(m)
		if err != nil {
			t.Fatal(err)
		}
		_, err = backup.ParseManifest(b)
		return err
	}
	long := strings.Repeat("x", 257)
	tooMany := map[string]string{}
	for i := range 4097 {
		tooMany["m"+strconv.Itoa(i)] = "1"
	}
	refused := map[string]func(*backup.Manifest){
		"the ID of a legacy archive":         func(m *backup.Manifest) { m.ArchiveID = "legacy-gotham_20261112_020000" },
		"a lower-case ID":                    func(m *backup.Manifest) { m.ArchiveID = "01jbatman0000000000000000a" },
		"a 25-character ID":                  func(m *backup.Manifest) { m.ArchiveID = m.ArchiveID[:25] },
		"a letter Crockford leaves out":      func(m *backup.Manifest) { m.ArchiveID = "01JBATMAN0000000000000000U" },
		"a first character past 7":           func(m *backup.Manifest) { m.ArchiveID = "81JBATMAN0000000000000000A" },
		"an empty ID":                        func(m *backup.Manifest) { m.ArchiveID = "" },
		"a reason with a line break":         func(m *backup.Manifest) { m.Reason = "manual\nINFO forged" },
		"an app with a carriage return":      func(m *backup.Manifest) { m.App = "gotham\r" },
		"an over-long app version":           func(m *backup.Manifest) { m.AppVersion = long },
		"an over-long core version":          func(m *backup.Manifest) { m.CoreVersion = long },
		"a NUL in the install ID":            func(m *backup.Manifest) { m.InstallID = "install\x00batcave" },
		"a module name with a newline":       func(m *backup.Manifest) { m.Modules = map[string]string{"books\nx": "1"} },
		"a module version too long":          func(m *backup.Manifest) { m.Modules = map[string]string{"books": long} },
		"a levels key too long":              func(m *backup.Manifest) { m.Levels = map[string]int{long: 1} },
		"a keyspaces key with a control":     func(m *backup.Manifest) { m.Keyspaces = map[string]backup.KSInfo{"core\t": {}} },
		"more modules than any build has":    func(m *backup.Manifest) { m.Modules = tooMany },
		"a provenance key ID with a newline": func(m *backup.Manifest) { m.Provenance.KeyID = "d1\n" },
		"an over-long provenance algorithm":  func(m *backup.Manifest) { m.Provenance.Alg = long },
		"a served-boot nonce too long": func(m *backup.Manifest) {
			m.ServedBoot = &journal.ServedBoot{N: 1, Nonce: long}
		},
		"a derived generation too long": func(m *backup.Manifest) {
			m.Derived = map[string]backup.DerivedInfo{"x": {Generation: long}}
		},
		"an entry path with dot-dot":       func(m *backup.Manifest) { m.Entries[0].Path = "db/../x" },
		"an absolute entry path":           func(m *backup.Manifest) { m.Entries[0].Path = "/etc/x" },
		"a doubled slash in an entry path": func(m *backup.Manifest) { m.Entries[0].Path = "db//x" },
		"a backslash in an entry path":     func(m *backup.Manifest) { m.Entries[0].Path = `db\x` },
		"an entry path over 1024 bytes":    func(m *backup.Manifest) { m.Entries[0].Path = strings.Repeat("a", 1025) },
		"entry sizes that overflow an int64": func(m *backup.Manifest) {
			m.Entries[0].Size, m.Entries[1].Size = math.MaxInt64, 1
		},
	}
	for name, edit := range refused {
		t.Run(name, func(t *testing.T) {
			if err := parse(t, edit); !errors.Is(err, backup.ErrManifest) {
				t.Fatalf("ParseManifest = %v, want ErrManifest", err)
			}
		})
	}
	accepted := map[string]func(*backup.Manifest){
		"the fixture":                         func(*backup.Manifest) {},
		"an ID whose first character is 7":    func(m *backup.Manifest) { m.ArchiveID = "7ZZZZZZZZZZZZZZZZZZZZZZZZZ" },
		"text at the bound":                   func(m *backup.Manifest) { m.AppVersion = strings.Repeat("v", 256) },
		"a reason a module chose":             func(m *backup.Manifest) { m.Reason = "pre:books.organize" },
		"non-ASCII text without control":      func(m *backup.Manifest) { m.Reason = "pré-mise-à-jour" },
		"sizes that add up to the int64 edge": func(m *backup.Manifest) { m.Entries[0].Size, m.Entries[1].Size = math.MaxInt64-1, 1 },
	}
	for name, edit := range accepted {
		t.Run("accepts "+name, func(t *testing.T) {
			if err := parse(t, edit); err != nil {
				t.Fatalf("ParseManifest = %v", err)
			}
		})
	}
}

func TestNewRequiresMarkers(t *testing.T) {
	e := newEnv(t)
	o := e.opts
	o.Markers = nil
	if _, err := backup.New(o); err == nil || !strings.Contains(err.Error(), "Markers") {
		t.Fatalf("New without Markers = %v, want an error naming Markers", err)
	}
}

// ---- the catalogue ------------------------------------------------------------

// Two files with one ID would let find, Delete and Verify act on whichever came
// first: one is listed, the one whose name sorts first, and the other is left
// alone on disk.
func TestCatalogueListsOneFilePerArchiveID(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	body := readFileT(t, e.path(a))

	later := "gotham_20990101_000000.tar.gz"
	if err := os.WriteFile(filepath.Join(e.backupDir, later), body, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != a.Name {
		t.Fatalf("List = %+v, %v; want only %s", list, err, a.Name)
	}
	if !has(e.names(), later) {
		t.Fatal("the unlisted duplicate was deleted")
	}
	earlier := "gotham_20000101_000000.tar.gz"
	if err := os.WriteFile(filepath.Join(e.backupDir, earlier), body, 0o600); err != nil {
		t.Fatal(err)
	}
	list, err = e.svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != earlier {
		t.Fatalf("List = %+v, %v; want only %s, the name that sorts first", list, err, earlier)
	}
}

func TestCatalogueListsOneLegacyArchivePerStem(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	legacyArchive(t, e, "gzip", true)
	legacyArchive(t, e, "none", true)
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v; want one archive for the one stem", list, err)
	}
	if !strings.HasSuffix(list[0].Name, ".tar") {
		t.Fatalf("listed %s; the name that sorts first is the .tar", list[0].Name)
	}
}

// A run that owns a .partial or a .tmp keeps its time moving; one that nobody has
// written for hours is a crashed run's leftover, which the scan removes. Another
// app's files and a recent file are not touched.
func TestCatalogueSweepsStaleTemporaryFiles(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	old := e.now().Add(-7 * time.Hour)
	stale := []string{"gotham_import-1.partial", a.Name + ".sha256.0a1b2c3d4e5f.tmp", "gotham_20261112_000000.tar.gz.partial"}
	keep := []string{"gotham_import-2.partial", "arkham_import-1.partial", "gotham_notes.txt"}
	for _, n := range append(append([]string(nil), stale...), keep...) {
		p := filepath.Join(e.backupDir, n)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range stale {
		if err := os.Chtimes(filepath.Join(e.backupDir, n), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// An old file of another app's prefix is not ours to judge.
	if err := os.Chtimes(filepath.Join(e.backupDir, "arkham_import-1.partial"), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.List(ctx); err != nil {
		t.Fatal(err)
	}
	got := e.names()
	for _, n := range stale {
		if has(got, n) {
			t.Errorf("the stale file %s was kept: %v", n, got)
		}
	}
	for _, n := range append(keep, a.Name) {
		if !has(got, n) {
			t.Errorf("%s was removed: %v", n, got)
		}
	}
}

// ---- retention floors ---------------------------------------------------------

// The floor is the newest verified archive of this install. A foreign archive
// with a verification record nobody vouches for (the file is writable by anyone
// who can write the directory) must not take it: the install's own newest
// verified archive would then be free to go.
func TestForeignArchiveNeverTakesTheFloor(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	own := e.create("manual")
	foreign := madeElsewhere(t, e, func(o *env) { o.clock = o.clock.Add(24 * time.Hour) })
	if foreign.Provenance != backup.Foreign || !foreign.CreatedAt.After(own.CreatedAt) {
		t.Fatalf("fixture: foreign = %v at %v, own at %v", foreign.Provenance, foreign.CreatedAt, own.CreatedAt)
	}
	forged := fmt.Sprintf(`{"at":%q,"level":"open","ok":true,"detail":""}`, foreign.CreatedAt.Add(time.Hour).UTC().Format(time.RFC3339))
	if err := os.WriteFile(filepath.Join(e.backupDir, foreign.Name)+backup.VerifySidecar, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.List(ctx); len(list) != 2 || list[0].Verified == nil || !list[0].Verified.OK {
		t.Fatalf("fixture: the forged record is not read as a verification: %+v", list)
	}
	if err := e.svc.Delete(ctx, own.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the install's own newest verified archive = %v, want ErrProtectedByFloor", err)
	}
	if err := e.svc.Delete(ctx, foreign.ID); err != nil {
		t.Fatalf("the foreign archive holds a floor: %v", err)
	}
}

// Before the first backup under this build an install has only HEAD's archives,
// which carry no provenance. The newest verified one is the floor then; once a
// verified Own archive exists it is the floor instead.
func TestLegacyArchiveTakesTheFloorOnlyWhileNoOwnExists(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	name := legacyArchive(t, e, "gzip", true)
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != name || list[0].Provenance != backup.PreProvenance {
		t.Fatalf("List = %+v, %v", list, err)
	}
	legacy := list[0].ID
	if _, err := e.svc.Verify(ctx, legacy, backup.VerifyEntries); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, legacy); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the only verified archive = %v, want ErrProtectedByFloor", err)
	}
	// The legacy archive's name is newer than the fake clock's archives, and it
	// still gives way: an Own archive that verified is the floor.
	own := e.create("manual")
	if err := e.svc.Delete(ctx, own.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of the verified Own archive = %v, want ErrProtectedByFloor", err)
	}
	if err := e.svc.Delete(ctx, legacy); err != nil {
		t.Fatalf("the legacy archive still holds the floor beside a verified Own one: %v", err)
	}
}

// The scheduled verification opens the archive's store; it does so for an Own
// archive only. A foreign archive is checked at the entries level and left
// closed, however new it is.
func TestVerifyTaskOpensOnlyOwnArchives(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	own := e.create("manual")
	foreign := madeElsewhere(t, e, func(o *env) { o.clock = o.clock.Add(24 * time.Hour) })
	e.settings.VerifyKeep = 2
	rc := taskstest.NewFakeRun(ctx, backup.KindVerify).RC()
	res, err := backup.TaskVerify(e.svc, rc, backup.Empty{})
	if err != nil || res.Checked != 2 || res.Opened != 1 || res.Failed != 0 {
		t.Fatalf("result = %+v, %v; want two checked, the Own one opened", res, err)
	}
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	for _, a := range list {
		want := backup.VerifyOpen
		if a.ID == foreign.ID {
			want = backup.VerifyEntries
		}
		if a.Verified == nil || a.Verified.Level != want {
			t.Errorf("%s (own=%v) was verified at %+v, want the %s level", a.ID, a.ID == own.ID, a.Verified, want)
		}
	}
}

// ---- small things -------------------------------------------------------------

func TestRunReasonLabelIsAFixedSet(t *testing.T) {
	for in, want := range map[string]string{
		"manual": "manual", "schedule": "schedule", "": "other", "pre:books.organize": "other",
		"pre-update": "other", "SCHEDULE": "other", "manual ": "other",
	} {
		if got := backup.RunReasonLabel(in); got != want {
			t.Errorf("RunReasonLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// The runs counter carries the fixed label, never the caller's text.
func TestRunsCounterNeverCarriesAFreeTextReason(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("backup", backup.ScopeAttributes()...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := backup.NewMetrics(sc)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.opts.Metrics = m
	e.rebuild()
	e.create("pre:books.organize")
	e.create("pre:tv.import")
	e.create("schedule")
	if v, ok := seriesValue(t, p, "mediacore_backup_runs_total", map[string]string{"reason": "other", "outcome": "ok"}); !ok || v != 2 {
		t.Errorf("runs{other,ok} = %v, %v; want 2", v, ok)
	}
	if v, ok := seriesValue(t, p, "mediacore_backup_runs_total", map[string]string{"reason": "schedule", "outcome": "ok"}); !ok || v != 1 {
		t.Errorf("runs{schedule,ok} = %v, %v; want 1", v, ok)
	}
	if _, ok := seriesValue(t, p, "mediacore_backup_runs_total", map[string]string{"reason": "pre:books.organize"}); ok {
		t.Error("a series carries the caller's reason text")
	}
}

func TestTruncateUTF8CutsAtARuneBoundary(t *testing.T) {
	s := strings.Repeat("€", 300) // 3 bytes each: 900 bytes
	got := backup.TruncateUTF8(s, 500)
	if len(got) != 498 || !strings.HasPrefix(s, got) {
		t.Fatalf("len = %d, want 498 (166 whole runes)", len(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatal("the cut left a broken rune")
		}
	}
	if got := backup.TruncateUTF8("short", 500); got != "short" {
		t.Fatalf("a short string changed: %q", got)
	}
	if got := backup.TruncateUTF8("abc", 3); got != "abc" {
		t.Fatalf("a string of exactly n bytes changed: %q", got)
	}
	if got := backup.TruncateUTF8("€", 2); got != "" {
		t.Fatalf("a rune that does not fit is cut to %q, want empty", got)
	}
}

// A stale verification directory (a crashed run's) is removed by the next
// verification of the open level; a recent one, another run's, is not.
func TestVerifySweepsAStaleScratchDirectory(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	root := filepath.Join(e.dbParent, ".backup-verify")
	for _, d := range []string{"v-stale", "v-fresh", "keep-me"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "000001.sst"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := e.now().Add(-48 * time.Hour)
	for _, d := range []string{"v-stale", "keep-me"} {
		if err := os.Chtimes(filepath.Join(root, d), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyOpen); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "v-stale")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the stale scratch directory was kept: %v", err)
	}
	for _, d := range []string{"v-fresh", "keep-me"} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("%s was removed: %v", d, err)
		}
	}
}
