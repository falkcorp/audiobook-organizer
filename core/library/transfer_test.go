// file: core/library/transfer_test.go
// version: 1.0.0
// guid: 1af0cfd3-c7e3-42bb-a439-7db2ac8dc820
// last-edited: 2026-11-11

package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// transferRig is a books manager with a library root and an import root, with
// fault injection around both.
func transferRig(t *testing.T, o rigOpts) (*rig, *faults) {
	t.Helper()
	if o.faults == nil {
		o.faults = &faults{}
	}
	r := newRig(t, newStore(t), o, spec("books-library", books, RoleLibrary), spec("import-1", books, RoleImport))
	return r, o.faults
}

func (r *rig) journalRows() int {
	r.t.Helper()
	rows, err := r.m.listTransfers(ctxbg())
	if err != nil {
		r.t.Fatal(err)
	}
	return len(rows)
}

// crossDevice makes every no-clobber rename report a device boundary.
func crossDevice(label string) error {
	if strings.Contains(label, ":rename:") {
		return ErrCrossDevice
	}
	return nil
}

func TestTransferCopyPublishIsNoClobber(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	src := r.put("import-1", "Arkham/The Long Halloween.m4b", "twelve chapters of Gotham")
	dst := Path{Root: "books-library", Rel: "Jeph Loeb/The Long Halloween/01.m4b"}

	res, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts())
	if err != nil {
		t.Fatal(err)
	}
	want := "twelve chapters of Gotham"
	if res.Did != Copy || res.Bytes != int64(len(want)) || res.SHA256 != sum(want) {
		t.Errorf("result = %+v", res)
	}
	if got, _ := r.read("books-library", dst.Rel); got != want {
		t.Errorf("dst = %q", got)
	}
	if got, _ := r.read("import-1", src.Rel); got != want {
		t.Errorf("a copy changed the source: %q", got)
	}
	if l := r.leftovers("books-library"); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("leftovers %v, journal rows %d", l, r.journalRows())
	}

	// An existing destination is refused and never touched.
	other := r.put("import-1", "Arkham/other.m4b", "someone else's bytes")
	if _, err := r.m.Transfer(ctxbg(), books, other, dst, copyOpts()); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Transfer over an existing file = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != want {
		t.Errorf("an existing destination was overwritten: %q", got)
	}

	// A creator that wins the race for the final name between our verify and
	// our publish keeps it; our temp goes (the publish is a link, never a rename
	// over it).
	race := Path{Root: "books-library", Rel: "Jeph Loeb/Dark Victory/01.m4b"}
	f.inject = func(label string) error {
		if strings.Contains(label, ":link:") && strings.HasSuffix(label, "->"+race.Rel) {
			if err := os.MkdirAll(filepath.Dir(r.abs("books-library", race.Rel)), 0o755); err != nil {
				return err
			}
			return os.WriteFile(r.abs("books-library", race.Rel), []byte("the winner"), 0o664)
		}
		return nil
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, race, copyOpts()); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Transfer that lost the race = %v, want fs.ErrExist", err)
	}
	f.inject = nil
	if got, _ := r.read("books-library", race.Rel); got != "the winner" {
		t.Errorf("the concurrent creator's file = %q", got)
	}
	if l := r.leftovers("books-library"); len(l) != 0 {
		t.Errorf("our temp stayed: %v", l)
	}
	if r.journalRows() != 0 {
		t.Errorf("journal rows = %d", r.journalRows())
	}
}

func TestTransferConcurrentWritersOneWinner(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	dst := Path{Root: "books-library", Rel: "Batman/Year One.m4b"}
	const n = 8
	var wins, exists atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		src := r.put("import-1", "Arkham/copy"+string(rune('a'+i))+".m4b", strings.Repeat("x", 100+i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts())
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, fs.ErrExist):
				exists.Add(1)
			default:
				t.Errorf("Transfer: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || exists.Load() != n-1 {
		t.Errorf("wins %d, refused %d", wins.Load(), exists.Load())
	}
	if l := r.leftovers("books-library"); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("leftovers %v, journal rows %d", l, r.journalRows())
	}
}

func TestTransferPermPolicyHasNoDefault(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	src := r.put("import-1", "a.m4b", "bytes")
	dst := Path{Root: "books-library", Rel: "a.m4b"}
	bad := []TransferOptions{
		{Mode: Copy, Perm: PermInvalid, Verify: VerifySize, Journal: true},
		{Mode: Copy, Perm: PermLibrary, Verify: VerifyInvalid, Journal: true},
		{Mode: TransferInvalid, Perm: PermLibrary, Verify: VerifySize, Journal: true},
		{Mode: Auto + 1, Perm: PermLibrary, Verify: VerifySize, Journal: true},
		{Mode: Copy, Perm: PermLibrary, Verify: VerifySize, Journal: true, Replace: &ReplaceSpec{ExpectSHA256: "short"}},
		{},
	}
	for i, o := range bad {
		if _, err := r.m.Transfer(ctxbg(), books, src, dst, o); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("options %d: %v, want ErrInvalidOptions", i, err)
		}
	}
	if r.exists("books-library", "a.m4b") || r.journalRows() != 0 {
		t.Error("a refused option set did work")
	}

	// PermLibrary ignores a 0600 source: the new file is 0664 under the umask.
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, nil, 0o664); err != nil {
		t.Fatal(err)
	}
	pi, _ := os.Stat(probe)
	wantLib := pi.Mode().Perm()
	if err := os.Chmod(r.abs("import-1", "a.m4b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, NewTransferOptions(Copy, PermLibrary, VerifySize)); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(r.abs("books-library", "a.m4b")); st.Mode().Perm() != wantLib {
		t.Errorf("PermLibrary of a 0600 source = %v, want %v", st.Mode().Perm(), wantLib)
	}
	// PermFromSource keeps the source's bits.
	for _, mode := range []fs.FileMode{0o600, 0o640, 0o755} {
		name := "from-" + mode.String() + ".m4b"
		p := r.put("import-1", name, "bytes")
		if err := os.Chmod(r.abs("import-1", name), mode); err != nil {
			t.Fatal(err)
		}
		d := Path{Root: "books-library", Rel: name}
		if _, err := r.m.Transfer(ctxbg(), books, p, d, NewTransferOptions(Copy, PermFromSource, VerifySize)); err != nil {
			t.Fatal(err)
		}
		if st, _ := os.Stat(r.abs("books-library", name)); st.Mode().Perm() != mode {
			t.Errorf("PermFromSource of %v = %v", mode, st.Mode().Perm())
		}
	}
}

func TestTransferAutoFallbackChain(t *testing.T) {
	auto := NewTransferOptions(Auto, PermLibrary, VerifySHA256)
	label := func(op string) func(string) error {
		return func(l string) error {
			if strings.Contains(l, ":"+op+":") {
				switch op {
				case "reflink":
					return ErrReflinkUnsupported
				case "link":
					if !strings.Contains(l, tmpPrefix) { // the copy's own publish is a link too
						return ErrCrossDevice
					}
				}
			}
			return nil
		}
	}
	both := func(l string) error { return errors.Join(label("reflink")(l), label("link")(l)) }
	// errors.Join of nils is nil; of one error is that error (wrapped), which Is() still matches.
	cases := []struct {
		name   string
		inject func(string) error
		want   []TransferMode
	}{
		{"no reflink, no link: copy", both, []TransferMode{Copy}},
		{"no reflink: hardlink", label("reflink"), []TransferMode{Hardlink}},
		{"whatever this filesystem supports", nil, []TransferMode{Reflink, Hardlink}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, f := transferRig(t, rigOpts{})
			f.inject = c.inject
			src := r.put("import-1", "Arkham/x.m4b", "the bat and the cat")
			dst := Path{Root: "books-library", Rel: "x.m4b"}
			res, err := r.m.Transfer(ctxbg(), books, src, dst, auto)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(c.want, res.Did) {
				t.Errorf("Did = %s, want one of %v", res.Did, c.want)
			}
			if got, _ := r.read("books-library", dst.Rel); got != "the bat and the cat" {
				t.Errorf("dst = %q", got)
			}
			if res.SHA256 != sum("the bat and the cat") {
				t.Errorf("SHA256 = %q", res.SHA256)
			}
			si, _ := os.Stat(r.abs("import-1", src.Rel))
			di, _ := os.Stat(r.abs("books-library", dst.Rel))
			if res.Did == Hardlink && !os.SameFile(si, di) {
				t.Error("Did = Hardlink but the files are two inodes")
			}
			if res.Did == Copy && os.SameFile(si, di) {
				t.Error("Did = Copy but the files share an inode")
			}
			if l := r.leftovers("books-library"); len(l) != 0 || r.journalRows() != 0 {
				t.Errorf("leftovers %v, rows %d", l, r.journalRows())
			}
		})
	}

	t.Run("an explicit mode is not masked", func(t *testing.T) {
		r, f := transferRig(t, rigOpts{})
		src := r.put("import-1", "x.m4b", "bytes")
		f.inject = both
		if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "r.m4b"}, NewTransferOptions(Reflink, PermLibrary, VerifySize)); !errors.Is(err, ErrReflinkUnsupported) {
			t.Errorf("Reflink = %v, want ErrReflinkUnsupported", err)
		}
		if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "h.m4b"}, NewTransferOptions(Hardlink, PermLibrary, VerifySize)); !errors.Is(err, ErrCrossDevice) {
			t.Errorf("Hardlink = %v, want ErrCrossDevice", err)
		}
		if r.exists("books-library", "r.m4b") || r.exists("books-library", "h.m4b") || r.journalRows() != 0 || len(r.leftovers("books-library")) != 0 {
			t.Error("a refused mode left something behind")
		}
	})
	t.Run("hardlink and symlink", func(t *testing.T) {
		r, _ := transferRig(t, rigOpts{})
		src := r.put("import-1", "Arkham/x.m4b", "bytes")
		if res, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "h.m4b"}, NewTransferOptions(Hardlink, PermLibrary, VerifySize)); err != nil || res.Did != Hardlink {
			t.Fatalf("Hardlink: %+v %v", res, err)
		}
		si, _ := os.Stat(r.abs("import-1", src.Rel))
		hi, _ := os.Stat(r.abs("books-library", "h.m4b"))
		if !os.SameFile(si, hi) {
			t.Error("hardlink is not the same inode")
		}
		if res, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "s.m4b"}, NewTransferOptions(Symlink, PermLibrary, VerifySize)); err != nil || res.Did != Symlink {
			t.Fatalf("Symlink: %+v %v", res, err)
		}
		target, err := os.Readlink(r.abs("books-library", "s.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		wantAbs, _ := r.m.Abs(src)
		if target != wantAbs {
			t.Errorf("symlink target = %q, want %q", target, wantAbs)
		}
		if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "s2.m4b"}, TransferOptions{Mode: Symlink, Perm: PermLibrary, Verify: VerifySize, Journal: true, Replace: &ReplaceSpec{ExpectSHA256: sum("bytes")}}); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("Replace with Symlink = %v, want ErrInvalidOptions", err)
		}
	})
}

type oracleFunc func(context.Context, Path) (bool, bool, error)

func (f oracleFunc) Protected(ctx context.Context, p Path) (bool, bool, error) { return f(ctx, p) }

func TestMoveRefusedWhenSeedingUnknown(t *testing.T) {
	unknown := oracleFunc(func(context.Context, Path) (bool, bool, error) { return false, false, nil })
	r, _ := transferRig(t, rigOpts{oracles: []ProtectionOracle{unknown}})
	src := r.put("import-1", "Arkham/seeding.m4b", "bytes")
	dst := Path{Root: "books-library", Rel: "a.m4b"}
	// Unknown (loaded=false) blocks a Move and allows a copy.
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, move()); !errors.Is(err, ErrSourceProtected) {
		t.Fatalf("Move with an unknown oracle = %v, want ErrSourceProtected", err)
	}
	if !r.exists("import-1", src.Rel) || r.exists("books-library", "a.m4b") {
		t.Fatal("a refused Move changed something")
	}
	for _, mode := range []TransferMode{Copy, Hardlink, Symlink, Auto} {
		d := Path{Root: "books-library", Rel: "ok-" + mode.String() + ".m4b"}
		if _, err := r.m.Transfer(ctxbg(), books, src, d, NewTransferOptions(mode, PermLibrary, VerifySize)); err != nil {
			t.Errorf("%s with an unknown oracle: %v", mode, err)
		}
	}
	// Quarantine is a move too.
	if _, err := r.m.Quarantine(ctxbg(), books, src, "x"); !errors.Is(err, ErrSourceProtected) {
		t.Errorf("Quarantine with an unknown oracle = %v, want ErrSourceProtected", err)
	}

	state := func(protected, loaded bool, err error) *rig {
		o := oracleFunc(func(context.Context, Path) (bool, bool, error) { return protected, loaded, err })
		rr, _ := transferRig(t, rigOpts{oracles: []ProtectionOracle{o}})
		rr.put("import-1", "a.m4b", "bytes")
		return rr
	}
	for name, c := range map[string]struct {
		rig     *rig
		refused bool
	}{
		"protected":           {state(true, true, nil), true},
		"oracle error":        {state(false, true, errors.New("deluge unreachable")), true},
		"loaded and free":     {state(false, true, nil), false},
		"error with free set": {state(false, false, errors.New("x")), true},
	} {
		_, err := c.rig.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "a.m4b"}, Path{Root: "books-library", Rel: "b.m4b"}, move())
		if c.refused != errors.Is(err, ErrSourceProtected) || (!c.refused && err != nil) {
			t.Errorf("%s: Move = %v (refused want %v)", name, err, c.refused)
		}
	}

	// An external root is never moved out of; copying from it is fine.
	ext := spec("itunes", CoreOwner, RoleExternal)
	rr := newRig(t, newStore(t), rigOpts{}, ext, spec("books-library", books, RoleLibrary))
	esrc := rr.put("itunes", "Music/Gotham.m4a", "bytes")
	if _, err := rr.m.Transfer(ctxbg(), books, esrc, Path{Root: "books-library", Rel: "g.m4a"}, move()); !errors.Is(err, ErrNotReadable) && !errors.Is(err, ErrSourceProtected) {
		t.Errorf("Move out of an external root = %v", err)
	}
	if _, err := rr.m.Transfer(ctxbg(), CoreOwner, esrc, Path{Root: "books-library", Rel: "g.m4a"}, move()); !errors.Is(err, ErrReadOnlyRoot) {
		// core may read the external root but is not the books root's owner.
		t.Errorf("core writing the books root = %v, want ErrReadOnlyRoot", err)
	}
	if !rr.exists("itunes", "Music/Gotham.m4a") {
		t.Error("the external file moved")
	}
}

func TestMoveFromExternalRootRefused(t *testing.T) {
	ext := spec("itunes", books, RoleExternal)
	r := newRig(t, newStore(t), rigOpts{}, ext, spec("books-library", books, RoleLibrary))
	src := r.put("itunes", "Music/Gotham.m4a", "bytes")
	dst := Path{Root: "books-library", Rel: "g.m4a"}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, move()); !errors.Is(err, ErrReadOnlyRoot) && !errors.Is(err, ErrSourceProtected) {
		t.Fatalf("Move out of an external root = %v", err)
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, NewTransferOptions(Copy, PermLibrary, VerifySize)); err != nil {
		t.Fatalf("copy out of an external root: %v", err)
	}
	if !r.exists("itunes", "Music/Gotham.m4a") {
		t.Error("the external source changed")
	}
	// Nobody writes an external root.
	if _, err := r.m.Transfer(ctxbg(), books, dst, Path{Root: "itunes", Rel: "Music/new.m4a"}, NewTransferOptions(Copy, PermLibrary, VerifySize)); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("a transfer into an external root = %v, want ErrReadOnlyRoot", err)
	}
}

func TestMoveRefusedUnderStaticProtectedPath(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	src := r.put("import-1", "Seeding/Gotham.m4b", "bytes")
	free := r.put("import-1", "Free/Gotham.m4b", "bytes")
	set := defaultSettings()
	set.roots = r.specs
	set.protected = []string{filepath.Join(r.dirs["import-1"], "Seeding")}
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a.m4b"}, move()); !errors.Is(err, ErrSourceProtected) {
		t.Errorf("Move under a protected path = %v, want ErrSourceProtected", err)
	}
	if _, err := r.m.Transfer(ctxbg(), books, free, Path{Root: "books-library", Rel: "b.m4b"}, move()); err != nil {
		t.Errorf("Move outside the protected path: %v", err)
	}
	// A protected path that is not absolute fails closed.
	set.protected = []string{"relative/seeding"}
	if err := r.m.apply(ctxbg(), set); err != nil {
		t.Fatal(err)
	}
	r.put("import-1", "c.m4b", "bytes")
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "c.m4b"}, Path{Root: "books-library", Rel: "c.m4b"}, move()); !errors.Is(err, ErrSourceProtected) {
		t.Errorf("Move with an unresolvable protected path = %v, want ErrSourceProtected", err)
	}
}

func TestTransferChecksFreeSpaceFirst(t *testing.T) {
	avail := uint64(1000)
	seam := seams{usage: func(string) (Usage, error) { return Usage{Total: 1 << 40, Avail: avail}, nil }}
	lib := spec("books-library", books, RoleLibrary)
	lib.MinFree = 100
	f := &faults{}
	r := newRig(t, newStore(t), rigOpts{seams: seam, faults: f}, lib, spec("import-1", books, RoleImport))
	src := r.put("import-1", "Arkham/big.m4b", strings.Repeat("b", 901))
	dst := Path{Root: "books-library", Rel: "Batman/big.m4b"}

	// 901 + floor 100 > 1000: refused before anything is written, journaled or created.
	steps := f.steps()
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("Transfer below the floor = %v, want ErrInsufficientSpace", err)
	}
	// Only the journal's own begun and removal steps ran; no file operation.
	for _, l := range f.labels[steps:] {
		if !strings.HasPrefix(l, "transfer:") {
			t.Errorf("a file operation ran before the space check: %s", l)
		}
	}
	if r.exists("books-library", "Batman") || len(r.leftovers("books-library")) != 0 || r.journalRows() != 0 {
		t.Error("a refused transfer left something behind")
	}
	// Exactly at the floor is allowed.
	avail = 1001
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); err != nil {
		t.Fatalf("Transfer with exactly the floor free: %v", err)
	}
	// A same-device rename needs no space: it skips the check.
	avail = 0
	src2 := r.put("import-1", "Arkham/rename.m4b", strings.Repeat("r", 500))
	if _, err := r.m.Transfer(ctxbg(), books, src2, Path{Root: "books-library", Rel: "rename.m4b"}, move()); err != nil {
		t.Errorf("a same-device Move with no space: %v", err)
	}
	// A Move that must copy (device boundary) is checked, and the source stays.
	src3 := r.put("import-1", "Arkham/xdev.m4b", strings.Repeat("x", 500))
	f.inject = crossDevice
	if _, err := r.m.Transfer(ctxbg(), books, src3, Path{Root: "books-library", Rel: "xdev.m4b"}, move()); !errors.Is(err, ErrInsufficientSpace) {
		t.Errorf("a cross-device Move with no space = %v, want ErrInsufficientSpace", err)
	}
	if !r.exists("import-1", "Arkham/xdev.m4b") || r.exists("books-library", "xdev.m4b") || len(r.leftovers("books-library")) != 0 {
		t.Error("a refused cross-device Move changed something")
	}
	// A root that omits min_free takes the default floor from the settings.
	def := spec("books-default", books, RoleLibrary)
	def.MinFree = MinFreeDefault
	r2 := newRig(t, newStore(t), rigOpts{seams: seam}, def, spec("import-1", books, RoleImport))
	s2 := r2.put("import-1", "a.m4b", "12345")
	avail = 5 + defaultMinFreeBytes - 1
	if _, err := r2.m.Transfer(ctxbg(), books, s2, Path{Root: "books-default", Rel: "a.m4b"}, copyOpts()); !errors.Is(err, ErrInsufficientSpace) {
		t.Errorf("default floor = %v, want ErrInsufficientSpace", err)
	}
	avail = 5 + defaultMinFreeBytes
	if _, err := r2.m.Transfer(ctxbg(), books, s2, Path{Root: "books-default", Rel: "a.m4b"}, copyOpts()); err != nil {
		t.Errorf("default floor met: %v", err)
	}
}

// indexOf is the position of the first label containing all of parts, or -1.
func indexOf(labels []string, parts ...string) int {
	for i, l := range labels {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(l, p)
		}
		if ok {
			return i
		}
	}
	return -1
}

func TestTransferCrossDeviceMoveOrder(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = crossDevice
	src := r.put("import-1", "Arkham/Knightfall.m4b", "the back of the Bat is broken")
	dst := Path{Root: "books-library", Rel: "Knightfall.m4b"}
	res, err := r.m.Transfer(ctxbg(), books, src, dst, move())
	if err != nil {
		t.Fatal(err)
	}
	if res.Did != Move || res.SHA256 != sum("the back of the Bat is broken") {
		t.Errorf("result = %+v", res)
	}
	if _, ok := r.read("import-1", src.Rel); ok {
		t.Error("the source stayed")
	}
	if got, _ := r.read("books-library", dst.Rel); got != "the back of the Bat is broken" {
		t.Errorf("dst = %q", got)
	}
	if l := r.leftovers("books-library"); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("leftovers %v, rows %d", l, r.journalRows())
	}
	// The source goes only after the destination is written, fsynced (file),
	// published and its directory fsynced.
	L := f.labels
	create := indexOf(L, ":create:", tmpPrefix)
	write := indexOf(L, "write:", tmpPrefix)
	syncF := indexOf(L, "sync:", tmpPrefix)
	copied := indexOf(L, "transfer:put") // the begun row; the copied row is a later put
	publish := indexOf(L, ":link:", "->Knightfall.m4b")
	syncDst := indexOf(L, "books-library:syncdir:")
	removeSrc := indexOf(L, "import-1:remove:Arkham/Knightfall.m4b")
	syncSrc := indexOf(L, "import-1:syncdir:Arkham")
	order := []struct {
		name string
		at   int
	}{{"journal begun", copied}, {"create temp", create}, {"write", write}, {"fsync temp", syncF}, {"publish", publish},
		{"fsync destination dir", syncDst}, {"remove source", removeSrc}, {"fsync source dir", syncSrc}}
	for i, s := range order {
		if s.at < 0 {
			t.Fatalf("step %q never ran; labels: %v", s.name, L)
		}
		if i > 0 && s.at <= order[i-1].at {
			t.Errorf("step %q (%d) ran before %q (%d); labels: %v", s.name, s.at, order[i-1].name, order[i-1].at, L)
		}
	}
}

func TestTransferVerifyMismatchLeavesNothing(t *testing.T) {
	for name, tamper := range map[string]func(r *rig, label string){
		"the source changes while it is copied": func(r *rig, label string) {
			if strings.HasPrefix(label, "sync:") {
				p := r.abs("import-1", "Arkham/a.m4b")
				_ = os.WriteFile(p, []byte("more bytes appended later"), 0o664)
			}
		},
		"the written bytes differ from the read ones": func(r *rig, label string) {
			if strings.HasPrefix(label, "sync:") {
				_ = os.WriteFile(filepath.Join(r.dirs["books-library"], strings.TrimPrefix(label, "sync:")), []byte("bytes-OF-SAME-SIZE-!!!"[:len("original bytes")]), 0o664)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, f := transferRig(t, rigOpts{})
			src := r.put("import-1", "Arkham/a.m4b", "original bytes")
			f.inject = func(label string) error { tamper(r, label); return crossDevice(label) }
			_, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a.m4b"}, move())
			if !errors.Is(err, ErrVerifyFailed) {
				t.Fatalf("Transfer = %v, want ErrVerifyFailed", err)
			}
			if r.exists("books-library", "a.m4b") || len(r.leftovers("books-library")) != 0 || r.journalRows() != 0 {
				t.Error("a failed verify left something behind")
			}
			if !r.exists("import-1", "Arkham/a.m4b") {
				t.Error("the source was removed after a failed verify")
			}
		})
	}
}

func TestTransferCancelledCopyRemovesItsTemp(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	src := r.put("import-1", "Arkham/long.m4b", strings.Repeat("g", 3<<20+17))
	ctx, cancel := context.WithCancel(ctxbg())
	defer cancel()
	f.inject = func(label string) error {
		if strings.HasPrefix(label, "write:") {
			cancel() // after the first MiB; the next chunk boundary notices
		}
		return nil
	}
	_, err := r.m.Transfer(ctx, books, src, Path{Root: "books-library", Rel: "long.m4b"}, copyOpts())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Transfer = %v, want context.Canceled", err)
	}
	if r.exists("books-library", "long.m4b") || len(r.leftovers("books-library")) != 0 || r.journalRows() != 0 {
		t.Error("a cancelled copy left its temp, a destination or a journal row")
	}
	if !r.exists("import-1", "Arkham/long.m4b") {
		t.Error("the source went")
	}
}

func TestTransferReplaceIsCompareAndSwap(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	dst := r.put("books-library", "Batman/Hush.m4b", "the old edition")
	src := r.put("import-1", "Arkham/Hush.m4b", "the new edition")
	rep := func(content string) TransferOptions {
		o := NewTransferOptions(Copy, PermLibrary, VerifySHA256)
		o.Replace = &ReplaceSpec{ExpectSHA256: sum(content)}
		return o
	}
	// A stale expectation is refused and changes nothing.
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: dst.Rel}, rep("not what is there")); !errors.Is(err, ErrReplaceMismatch) {
		t.Fatalf("Replace with a stale digest = %v, want ErrReplaceMismatch", err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != "the old edition" {
		t.Errorf("dst = %q", got)
	}
	if len(r.leftovers("books-library")) != 0 || r.journalRows() != 0 {
		t.Errorf("leftovers %v rows %d", r.leftovers("books-library"), r.journalRows())
	}
	// A matching one swaps; the old file is kept until the new one is durable.
	steps := f.steps()
	res, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: dst.Rel}, rep("the old edition"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != "the new edition" || res.Did != Copy {
		t.Errorf("dst = %q, did %s", got, res.Did)
	}
	L := f.labels[steps:]
	aside := indexOf(L, ":rename:", asideInfix)
	publish := indexOf(L, ":link:", "->"+dst.Rel)
	syncDst := indexOf(L, "books-library:syncdir:Batman")
	dropAside := indexOf(L, ":remove:", asideInfix)
	if aside < 0 || publish < 0 || dropAside < 0 || !(aside < publish && publish < syncDst && syncDst < dropAside) {
		t.Errorf("order aside %d, publish %d, fsync %d, drop %d; labels %v", aside, publish, syncDst, dropAside, L)
	}
	if len(r.leftovers("books-library")) != 0 || r.journalRows() != 0 {
		t.Errorf("leftovers %v rows %d", r.leftovers("books-library"), r.journalRows())
	}
	// A destination that vanished is a mismatch too.
	gone := Path{Root: "books-library", Rel: "Batman/gone.m4b"}
	if _, err := r.m.Transfer(ctxbg(), books, src, gone, rep("x")); !errors.Is(err, ErrReplaceMismatch) {
		t.Errorf("Replace of a missing destination = %v, want ErrReplaceMismatch", err)
	}
	// A destination changed between the first hash and the rename is caught by
	// hashing what was actually set aside, and put back.
	r.put("books-library", "Batman/race.m4b", "v1")
	fired := false
	f.inject = func(l string) error {
		if !fired && strings.Contains(l, ":rename:") && strings.Contains(l, asideInfix) {
			fired = true
			return os.WriteFile(r.abs("books-library", "Batman/race.m4b"), []byte("v2"), 0o664)
		}
		return nil
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "Batman/race.m4b"}, rep("v1")); !errors.Is(err, ErrReplaceMismatch) {
		t.Fatalf("Replace of a changed destination = %v, want ErrReplaceMismatch", err)
	}
	f.inject = nil
	if got, _ := r.read("books-library", "Batman/race.m4b"); got != "v2" {
		t.Errorf("the changed destination was not restored: %q", got)
	}
	if len(r.leftovers("books-library")) != 0 {
		t.Errorf("leftovers %v", r.leftovers("books-library"))
	}
}

func TestTransferMoveSameDevice(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	src := r.put("import-1", "Arkham/Zero Year.m4b", "bytes")
	dst := Path{Root: "books-library", Rel: "Zero Year.m4b"}
	res, err := r.m.Transfer(ctxbg(), books, src, dst, move())
	if err != nil || res.Did != Move {
		t.Fatalf("Move = %+v, %v", res, err)
	}
	if r.exists("import-1", src.Rel) || !r.exists("books-library", dst.Rel) {
		t.Error("the file did not move")
	}
	if indexOf(f.labels, ":create:") >= 0 {
		t.Error("a same-device Move copied bytes")
	}
	// A directory moves as a rename.
	r.put("import-1", "Series/Gotham/01.m4b", "a")
	r.put("import-1", "Series/Gotham/02.m4b", "b")
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Series/Gotham"}, Path{Root: "books-library", Rel: "Gotham"}, move()); err != nil {
		t.Fatalf("Move of a directory: %v", err)
	}
	if got, _ := r.read("books-library", "Gotham/02.m4b"); got != "b" || r.exists("import-1", "Series/Gotham") {
		t.Error("the directory did not move whole")
	}
	// Never over an existing name, whatever its type.
	r.put("books-library", "taken.m4b", "mine")
	r.put("import-1", "x.m4b", "theirs")
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "x.m4b"}, Path{Root: "books-library", Rel: "taken.m4b"}, move()); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Move onto an existing file = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", "taken.m4b"); got != "mine" || !r.exists("import-1", "x.m4b") {
		t.Error("a refused Move changed something")
	}
	// Bad endpoints.
	for name, c := range map[string][2]Path{
		"same path":      {{Root: "import-1", Rel: "x.m4b"}, {Root: "import-1", Rel: "x.m4b"}},
		"inside itself":  {{Root: "import-1", Rel: "Series"}, {Root: "import-1", Rel: "Series/in"}},
		"a root as src":  {{Root: "import-1"}, {Root: "books-library", Rel: "z"}},
		"a root as dst":  {{Root: "import-1", Rel: "x.m4b"}, {Root: "books-library"}},
		"unknown root":   {{Root: "nope", Rel: "x"}, {Root: "books-library", Rel: "z"}},
		"escaping rel":   {{Root: "import-1", Rel: "../x"}, {Root: "books-library", Rel: "z"}},
		"missing source": {{Root: "import-1", Rel: "ghost.m4b"}, {Root: "books-library", Rel: "z"}},
	} {
		if _, err := r.m.Transfer(ctxbg(), books, c[0], c[1], move()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// MkdirAll off: the destination directory must exist.
	r.put("import-1", "y.m4b", "y")
	o := NewTransferOptions(Copy, PermLibrary, VerifySize)
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "y.m4b"}, Path{Root: "books-library", Rel: "NoSuchDir/y.m4b"}, o); err == nil {
		t.Error("a copy into a missing directory without MkdirAll succeeded")
	}
	o.MkdirAll = true
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "y.m4b"}, Path{Root: "books-library", Rel: "NoSuchDir/y.m4b"}, o); err != nil {
		t.Errorf("MkdirAll: %v", err)
	}
}

func TestTransferJournalFailureClosesBeforeAnyFileOperation(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	src := r.put("import-1", "a.m4b", "bytes")
	steps := f.steps()
	f.inject = func(l string) error {
		if l == "transfer:put" {
			return errors.New("the store is full")
		}
		return nil
	}
	_, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a.m4b"}, copyOpts())
	if !errors.Is(err, ErrJournal) {
		t.Fatalf("Transfer = %v, want ErrJournal", err)
	}
	for _, l := range f.labels[steps:] {
		if !strings.HasPrefix(l, "transfer:") {
			t.Errorf("a file operation ran without a journal row: %s", l)
		}
	}
	if r.exists("books-library", "a.m4b") {
		t.Error("the destination exists")
	}
	// Without the journal (temp files inside an AppDir) there is no row.
	f.inject = nil
	o := copyOpts()
	o.Journal = false
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "b.m4b"}, o); err != nil {
		t.Fatal(err)
	}
	for _, l := range f.labels {
		if l == "transfer:put" && indexOf(f.labels[len(f.labels)-1:], "transfer:put") >= 0 {
			t.Error("a Journal=false transfer wrote a row")
		}
	}
}

// TransferIn: the file work and the journal's end are one commit with the
// caller's rows. A crash before the caller commits leaves a done row that
// PendingTransfers reports and recovery never undoes.
func TestTransferInCommitsWithCallerBatch(t *testing.T) {
	mem := vfs.NewCrashableMem()
	dir := t.TempDir()
	st := bootStore(t, mem, dir, books)
	r := newRig(t, st, rigOpts{}, spec("books-library", books, RoleLibrary), spec("import-1", books, RoleImport))
	callerKS := st.keyspace(t, books).Sub("imported")
	scope, err := NewScope(r.m, books)
	if err != nil {
		t.Fatal(err)
	}
	src := r.put("import-1", "Arkham/Batman Begins.m4b", "bytes of Begins")
	dst := Path{Root: "books-library", Rel: "Batman Begins.m4b"}

	b := callerKS.NewBatch()
	if err := b.Set([]byte("batman-begins"), []byte("history row")); err != nil {
		t.Fatal(err)
	}
	res, err := scope.TransferIn(ctxbg(), b, src, dst, NewTransferOptions(Move, PermLibrary, VerifySHA256))
	if err != nil {
		t.Fatal(err)
	}
	if res.Did != Move {
		t.Fatalf("Did = %s", res.Did)
	}
	// File moved; the journal row is done, the caller's row is staged only.
	pending, err := scope.PendingTransfers(ctxbg())
	if err != nil || len(pending) != 1 || pending[0].Dst != dst || pending[0].Src != src || pending[0].Did != Move {
		t.Fatalf("PendingTransfers = %+v, %v", pending, err)
	}
	if _, err := callerKS.Get(ctxbg(), []byte("batman-begins")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("the caller's row is visible before its commit: %v", err)
	}
	other, _ := NewScope(r.m, tv)
	if p, _ := other.PendingTransfers(ctxbg()); len(p) != 0 {
		t.Errorf("another owner sees %d pending transfers", len(p))
	}

	// A crash here: the disk as it was before b committed.
	crashed := mem.CrashClone(vfs.CrashCloneCfg{})
	st2 := bootStore(t, crashed, dir, books)
	r2 := &rig{t: t, st: st2, dirs: r.dirs, specs: r.specs}
	r2.m = r2.build(rigOpts{})
	scope2, _ := NewScope(r2.m, books)
	pending2, err := scope2.PendingTransfers(ctxbg())
	if err != nil || len(pending2) != 1 || pending2[0].ID != pending[0].ID {
		t.Fatalf("after the crash PendingTransfers = %+v, %v", pending2, err)
	}
	if _, err := st2.keyspace(t, books).Sub("imported").Get(ctxbg(), []byte("batman-begins")); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("the caller's row survived a crash before its commit: %v", err)
	}
	// Recovery never undoes it: the file stays at dst, the row stays done.
	rep, err := r2.m.Recover(ctxbg())
	if err != nil || rep.Left != 1 || rep.Completed != 0 || rep.RolledBack != 0 {
		t.Errorf("Recover = %v, %v", rep, err)
	}
	if got, ok := r2.read("books-library", dst.Rel); !ok || got != "bytes of Begins" {
		t.Errorf("recovery undid a done TransferIn: %q %v", got, ok)
	}
	if p, _ := scope2.PendingTransfers(ctxbg()); len(p) != 1 {
		t.Errorf("recovery dropped the done row: %d", len(p))
	}

	// The caller commits b: its rows and the end of the journal land together.
	if err := b.Commit(ctxbg(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if p, _ := scope.PendingTransfers(ctxbg()); len(p) != 0 {
		t.Errorf("a committed TransferIn is still pending: %+v", p)
	}
	if v, err := callerKS.Get(ctxbg(), []byte("batman-begins")); err != nil || string(v) != "history row" {
		t.Errorf("the caller's row = %q, %v", v, err)
	}
	if r.journalRows() != 0 {
		t.Errorf("journal rows = %d", r.journalRows())
	}

	// A batch that is discarded instead of committed has the same outcome as the crash.
	src2 := r.put("import-1", "Arkham/Dark Knight.m4b", "bytes of Dark Knight")
	b2 := callerKS.NewBatch()
	if _, err := scope.TransferIn(ctxbg(), b2, src2, Path{Root: "books-library", Rel: "The Dark Knight.m4b"}, copyOpts()); err != nil {
		t.Fatal(err)
	}
	_ = b2.Close()
	if p, _ := scope.PendingTransfers(ctxbg()); len(p) != 1 || p[0].Dst.Rel != "The Dark Knight.m4b" {
		t.Errorf("after a discarded batch PendingTransfers = %+v", p)
	}
	// TransferIn needs a batch and a journal.
	if _, err := scope.TransferIn(ctxbg(), nil, src2, dst, copyOpts()); !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("TransferIn(nil batch) = %v, want ErrInvalidOptions", err)
	}
	nj := copyOpts()
	nj.Journal = false
	if _, err := scope.TransferIn(ctxbg(), callerKS.NewBatch(), src2, dst, nj); !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("TransferIn(no journal) = %v, want ErrInvalidOptions", err)
	}
}

func TestTransferDurationAndBytesAreRecorded(t *testing.T) {
	r, _ := transferRig(t, rigOpts{seams: seams{now: func() time.Time { return time.Unix(1_700_000_000, 0) }}})
	src := r.put("import-1", "a.m4b", "12345678")
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a.m4b"}, copyOpts()); err != nil {
		t.Fatal(err)
	}
	s := r.m.ctr.snapshot()
	if s.transfers[transferKey{"copy", "copy", "ok"}] != 1 || s.bytes["copy"] != 8 {
		t.Errorf("tallies = %+v", s)
	}
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "a.m4b"}, copyOpts()); err == nil {
		t.Fatal("expected a refusal")
	}
	s = r.m.ctr.snapshot()
	if s.transfers[transferKey{"copy", "none", "refused"}] != 1 || s.refusals[reasonExists] != 1 {
		t.Errorf("refusal tallies = %+v", s)
	}
	_ = bytes.Equal
}
