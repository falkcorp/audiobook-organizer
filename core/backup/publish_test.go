// file: core/backup/publish_test.go
// version: 1.0.0
// guid: 12ec280e-1941-42c2-966e-f694e2ff48ae
// last-edited: 2026-11-12

package backup_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
)

// rivals is a Disk whose Link lets another writer take the name first, as a
// concurrent writer does between this one's lookup and its link: each of the
// first lose calls plants bytes at the name and then makes the real link, which
// finds the name taken. With noLink set the link then fails with that error
// instead, as it does on a filesystem without hard links.
type rivals struct {
	mu     sync.Mutex
	asked  []string
	lose   int
	bytes  []byte
	noLink error
}

func (r *rivals) disk() backup.Disk {
	return backup.Disk{Link: func(oldname, newname string) error {
		r.mu.Lock()
		n := len(r.asked)
		r.asked = append(r.asked, newname)
		r.mu.Unlock()
		if n < r.lose {
			if err := os.WriteFile(newname, r.bytes, 0o600); err != nil {
				return err
			}
		}
		if r.noLink != nil {
			return r.noLink
		}
		return os.Link(oldname, newname)
	}}
}

// intact fails the test unless every name the rivals took still holds exactly
// what they wrote there.
func (r *rivals) intact(t *testing.T) {
	t.Helper()
	for i := 0; i < r.lose; i++ {
		got, err := os.ReadFile(r.asked[i])
		if err != nil || !bytes.Equal(got, r.bytes) {
			t.Fatalf("the archive another writer put at %s was replaced (%v): it holds %d bytes", filepath.Base(r.asked[i]), err, len(got))
		}
	}
}

// leftovers are the temporary files a finished run must not leave.
func leftovers(e *env) []string {
	var out []string
	for _, n := range e.names() {
		if strings.HasSuffix(n, ".partial") || strings.HasSuffix(n, ".tmp") {
			out = append(out, n)
		}
	}
	return out
}

// archivesFromElsewhere makes n archives in another install and returns their
// bytes.
func archivesFromElsewhere(t *testing.T, n int) [][]byte {
	t.Helper()
	other := newEnv(t)
	var out [][]byte
	for range n {
		a := other.create("manual")
		out = append(out, readFileT(t, other.path(a)))
	}
	return out
}

func sha(b []byte) [32]byte { return sha256.Sum256(b) }

// A finished archive is taken under its name by a link that fails if the name is
// there, so a rival that got the name between the lookup and the link keeps it.
// A lookup followed by a rename replaced the rival's archive, after the rival
// had been told it was made.
func TestCreateNeverReplacesAnArchiveAtItsName(t *testing.T) {
	e := newEnv(t)
	r := &rivals{lose: 2, bytes: []byte("an archive another writer finished first")}
	e.opts.Disk = r.disk()
	e.rebuild()
	a := e.create("manual")
	if len(r.asked) != 3 || r.asked[0] == r.asked[1] || r.asked[1] == r.asked[2] {
		t.Fatalf("names tried: %v", r.asked)
	}
	r.intact(t)
	if e.path(a) != r.asked[2] {
		t.Fatalf("the archive is at %s, want the third name %s", e.path(a), filepath.Base(r.asked[2]))
	}
	if a.Verified == nil || !a.Verified.OK {
		t.Fatalf("verified = %+v", a.Verified)
	}
	if l := leftovers(e); len(l) != 0 {
		t.Fatalf("leftover %v", l)
	}
}

func TestImportNeverReplacesAnArchiveAtItsName(t *testing.T) {
	e := newEnv(t)
	blob := archivesFromElsewhere(t, 1)[0]
	r := &rivals{lose: 1, bytes: []byte("an archive another writer finished first")}
	e.opts.Disk = r.disk()
	e.rebuild()
	imp, err := e.svc.Import(context.Background(), "", bytes.NewReader(blob))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.asked) != 2 {
		t.Fatalf("names tried: %v", r.asked)
	}
	r.intact(t)
	if e.path(imp) != r.asked[1] {
		t.Fatalf("the import is at %s, want the second name %s", e.path(imp), filepath.Base(r.asked[1]))
	}
	if got := readFileT(t, e.path(imp)); sha(got) != sha(blob) {
		t.Fatal("the imported archive is not the upload")
	}
	if l := leftovers(e); len(l) != 0 {
		t.Fatalf("leftover %v", l)
	}
}

func TestRewrapNeverReplacesAnArchiveAtItsName(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", "d2")
	e.opts.Keyring, e.opts.Inspector = e.keys, &backup.StorageInspector{Base: storageBase(e), Admin: e.st.DB().Admin(), Secrets: e.keys, SecretsOwner: gothamOwner}
	e.rebuild()
	old := e.create("manual")
	e.keys.active = "d2"
	r := &rivals{lose: 1, bytes: []byte("an archive another writer finished first")}
	e.opts.Disk = r.disk()
	e.rebuild()
	re, err := e.svc.Rewrap(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.intact(t)
	if len(r.asked) != 2 || e.path(re) != r.asked[1] {
		t.Fatalf("the rewrap is at %s after trying %v", re.Name, r.asked)
	}
	if re.Verified == nil || !re.Verified.OK {
		t.Fatalf("verified = %+v", re.Verified)
	}
	if l := leftovers(e); len(l) != 0 {
		t.Fatalf("leftover %v", l)
	}
}

// The same, through the operating system's own link and not a seam: the name the
// clock will choose is taken by a finished archive that has no sidecar, so no
// lookup of the sidecars finds it, and the writer must go on to the next second
// and leave the first archive alone.
func TestDefaultPublishNeverReplacesAnArchiveAtItsName(t *testing.T) {
	stamp := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	rivalName := func(e *env, at time.Time) string {
		return filepath.Join(e.backupDir, "gotham_"+at.Format("20060102_150405")+".tar.gz")
	}
	plant := func(t *testing.T, e *env) (path string, content []byte) {
		t.Helper()
		if err := os.MkdirAll(e.backupDir, 0o700); err != nil {
			t.Fatal(err)
		}
		content = []byte("an archive another writer finished first")
		path = rivalName(e, stamp)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		return path, content
	}
	held := func(t *testing.T, path string, want []byte) {
		t.Helper()
		if got := readFileT(t, path); !bytes.Equal(got, want) {
			t.Fatalf("the archive at %s was replaced: it holds %d bytes", filepath.Base(path), len(got))
		}
	}
	t.Run("create", func(t *testing.T) {
		e := newEnv(t)
		e.opts.Clock = func() time.Time { return stamp }
		e.rebuild()
		path, content := plant(t, e)
		a := e.create("manual")
		held(t, path, content)
		if want := filepath.Base(rivalName(e, stamp.Add(time.Second))); a.Name != want {
			t.Fatalf("the archive is %s, want %s", a.Name, want)
		}
	})
	t.Run("import", func(t *testing.T) {
		e := newEnv(t)
		e.opts.Clock = func() time.Time { return stamp }
		e.rebuild()
		path, content := plant(t, e)
		blob := archivesFromElsewhere(t, 1)[0]
		imp, err := e.svc.Import(context.Background(), "", bytes.NewReader(blob))
		if err != nil {
			t.Fatal(err)
		}
		held(t, path, content)
		if want := filepath.Base(rivalName(e, stamp.Add(time.Second))); imp.Name != want {
			t.Fatalf("the import is %s, want %s", imp.Name, want)
		}
		if got := readFileT(t, e.path(imp)); sha(got) != sha(blob) {
			t.Fatal("the imported archive is not the upload")
		}
	})
}

// A name that has a sidecar and no archive is not used either: the sidecars
// written next would replace those. The sidecar here appears after the lookup
// that precedes the first try (as another writer's does between its two steps).
func TestPublishSkipsANameWhoseSidecarIsThere(t *testing.T) {
	e := newEnv(t)
	var asked []string
	var orphan string
	e.opts.Disk = backup.Disk{Link: func(oldname, newname string) error {
		asked = append(asked, newname)
		if len(asked) == 1 {
			// Another writer takes this name, and has written the sidecar of the
			// name one second on, for an archive it has yet to link.
			if err := os.WriteFile(newname, []byte("an archive another writer finished first"), 0o600); err != nil {
				return err
			}
			next := filepath.Join(filepath.Dir(newname), "gotham_"+nextSecond(t, newname)+".tar.gz")
			orphan = next + backup.ChecksumSidecar
			if err := os.WriteFile(orphan, []byte("not an archive's checksum\n"), 0o600); err != nil {
				return err
			}
		}
		return os.Link(oldname, newname)
	}}
	e.rebuild()
	a := e.create("manual")
	if len(asked) != 2 {
		t.Fatalf("names tried: %v, want the first (taken) and the third (the second has a sidecar)", asked)
	}
	if e.path(a) != asked[1] {
		t.Fatalf("the archive is at %s, want %s", a.Name, filepath.Base(asked[1]))
	}
	if got := readFileT(t, orphan); string(got) != "not an archive's checksum\n" {
		t.Fatalf("the sidecar that was there was replaced: %q", got)
	}
}

// nextSecond is the YYYYMMDD_HHMMSS stamp one second after the one in an
// archive's file name.
func nextSecond(t *testing.T, name string) string {
	t.Helper()
	stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "gotham_"), ".tar.gz")
	at, err := time.Parse("20060102_150405", stamp)
	if err != nil {
		t.Fatal(err)
	}
	return at.Add(time.Second).Format("20060102_150405")
}

// Import asks only how much room the directory has, not which device it is on:
// a platform that cannot name one (disk_other.go) still takes an upload.
func TestImportNeedsNoDeviceLookup(t *testing.T) {
	e := newEnv(t)
	e.opts.Disk = backup.Disk{
		Device: func(string) (uint64, error) { return 0, errors.New("batcave sensor offline") },
		Avail:  func(string) (uint64, error) { return 1 << 40, nil },
	}
	e.rebuild()
	blob := archivesFromElsewhere(t, 1)[0]
	if _, err := e.svc.Import(context.Background(), "", bytes.NewReader(blob)); err != nil {
		t.Fatalf("Import = %v", err)
	}
}

// A volume that cannot link (FAT, exFAT, some network mounts) still takes a
// backup, and still never replaces one: the name is claimed by creating it
// exclusively and the finished file is renamed over the claim.
func TestPublishOnAFilesystemWithoutHardLinks(t *testing.T) {
	noLinks := errors.New("link: operation not supported")
	t.Run("create and import", func(t *testing.T) {
		e := newEnv(t)
		r := &rivals{noLink: noLinks}
		e.opts.Disk = r.disk()
		e.rebuild()
		a := e.create("manual")
		if a.Verified == nil || !a.Verified.OK {
			t.Fatalf("verified = %+v", a.Verified)
		}
		blob := archivesFromElsewhere(t, 1)[0]
		imp, err := e.svc.Import(context.Background(), "", bytes.NewReader(blob))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{a.Name, imp.Name} {
			fi, err := os.Stat(filepath.Join(e.backupDir, name))
			if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() == 0 {
				t.Fatalf("%s: %v, %v", name, fi, err)
			}
		}
		if got := readFileT(t, e.path(imp)); sha(got) != sha(blob) {
			t.Fatal("the imported archive is not the upload")
		}
		if l := leftovers(e); len(l) != 0 {
			t.Fatalf("leftover %v", l)
		}
	})
	t.Run("a rival's name is kept", func(t *testing.T) {
		e := newEnv(t)
		r := &rivals{noLink: noLinks, lose: 1, bytes: []byte("an archive another writer finished first")}
		e.opts.Disk = r.disk()
		e.rebuild()
		a := e.create("manual")
		r.intact(t)
		if len(r.asked) != 2 || e.path(a) != r.asked[1] {
			t.Fatalf("the archive is at %s after trying %v", a.Name, r.asked)
		}
		if l := leftovers(e); len(l) != 0 {
			t.Fatalf("leftover %v", l)
		}
	})
}

// Two uploads in the same second, with the clock held still, each keep their
// archive: every one is under a name of its own and holds its own bytes.
func TestImportsInTheSameSecondEachKeepTheirArchive(t *testing.T) {
	const n = 4
	e := newEnv(t)
	fixed := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	e.opts.Clock = func() time.Time { return fixed }
	e.rebuild()
	blobs := archivesFromElsewhere(t, n)
	var wg sync.WaitGroup
	got := make([]backup.Archive, n)
	errs := make([]error, n)
	for i := range blobs {
		wg.Go(func() { got[i], errs[i] = e.svc.Import(context.Background(), "", bytes.NewReader(blobs[i])) })
	}
	wg.Wait()
	seen := map[string]bool{}
	for i := range blobs {
		if errs[i] != nil {
			t.Fatalf("import %d: %v", i, errs[i])
		}
		if seen[got[i].Name] {
			t.Fatalf("two imports were given the name %s", got[i].Name)
		}
		seen[got[i].Name] = true
		if b := readFileT(t, e.path(got[i])); sha(b) != sha(blobs[i]) {
			t.Fatalf("import %d: %s holds another upload's bytes", i, got[i].Name)
		}
	}
	if l := leftovers(e); len(l) != 0 {
		t.Fatalf("leftover %v", l)
	}
}

// ---- free space during an upload ------------------------------------------

// mustNotRead fails the test if anything reads it.
type mustNotRead struct{ t *testing.T }

func (m mustNotRead) Read([]byte) (int, error) {
	m.t.Helper()
	m.t.Fatal("the upload was read although there was no room for it")
	return 0, io.EOF
}

// zeroSource is an endless reader of zeros that counts what it has handed out.
type zeroSource struct{ n *atomic.Int64 }

func (z zeroSource) Read(p []byte) (int, error) {
	clear(p)
	z.n.Add(int64(len(p)))
	return len(p), nil
}

// An upload onto a volume with no room is refused before a byte of it is read
// and before a file is made.
func TestImportRefusesBeforeReadingWhenThereIsNoRoom(t *testing.T) {
	e := newEnv(t)
	e.opts.Disk = fixedAvail(0)
	e.rebuild()
	_, err := e.svc.Import(context.Background(), "", mustNotRead{t})
	if !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("Import = %v, want ErrInsufficientSpace", err)
	}
	if names := e.names(); len(names) != 0 {
		t.Fatalf("a refused import left %v", names)
	}
}

// The upload's length is unknown until it ends. The free space is asked again as
// it is written, and a volume that fills stops it there, with the temporary file
// removed, not at a full disk.
func TestImportStopsWhenTheVolumeFillsMidUpload(t *testing.T) {
	const upload = 96 << 20 // far more than one check interval
	e := newEnv(t)
	var asked atomic.Int32
	e.opts.Disk = backup.Disk{Avail: func(string) (uint64, error) {
		if asked.Add(1) == 1 {
			return 1 << 40, nil // room for the start
		}
		return 0, nil // and then none
	}}
	e.rebuild()
	var read atomic.Int64
	_, err := e.svc.Import(context.Background(), "", io.LimitReader(zeroSource{&read}, upload))
	if !errors.Is(err, backup.ErrInsufficientSpace) {
		t.Fatalf("Import = %v, want ErrInsufficientSpace", err)
	}
	if got := asked.Load(); got != 2 {
		t.Errorf("free space was asked %d times, want 2 (before the upload and once during it)", got)
	}
	if n := read.Load(); n <= 0 || n >= upload {
		t.Errorf("read %d of %d bytes: the upload should have been stopped part-way", n, upload)
	}
	if names := e.names(); len(names) != 0 {
		t.Fatalf("a stopped import left %v", names)
	}
}
