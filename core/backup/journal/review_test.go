// file: core/backup/journal/review_test.go
// version: 1.1.0
// guid: 3e8a1c75-9d24-4f60-8b13-a5c7e2d09f48
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// The tests the independent review of the first F4-11 round asked for in this
// package: the journal's file handling (MAJOR 1), the line it writes without
// committing the head (BLOCKER 2), the kinds it can tell it cannot apply
// (MAJOR 2), and the property that no edit of a journal file ever verifies a
// line that was not there.

func userRef(id string) entity.Ref { return entity.Ref{Module: "core", Kind: "user", ID: id} }

// TestFile_WriteLineLeavesTheHeadToTheCaller: the line is durable and chained,
// the store's head has not moved, and the caller moves it, in its own batch
// beside other writes, or alone.
func TestFile_WriteLineLeavesTheHeadToTheCaller(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker") // seq 1, head 1

	p, err := j.WriteLine(ctx, GapLine(time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC), GapOtherInstall))
	must(t, err)
	if p.Head().Seq != 2 || fx.head(t).Seq != 1 || fx.chain(t).IntactEnd() != 2 {
		t.Fatalf("pending head %v, committed head %v, journal end %d: want 2, 1 and 2", p.Head(), fx.head(t), fx.chain(t).IntactEnd())
	}

	// Staged into the caller's batch: the head moves with the caller's write, and only with its commit.
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/penguin"), []byte("disabled")))
	must(t, p.Stage(ctx, b))
	if fx.head(t).Seq != 1 {
		t.Fatalf("Stage moved the head before the batch committed")
	}
	must(t, b.Commit(ctx, storage.Sync))
	if got := fx.head(t); got != p.Head() {
		t.Fatalf("head = %v after the commit, want %v", got, p.Head())
	}

	// Alone, and the next Append continues after it.
	p2, err := j.WriteLine(ctx, GapLine(time.Date(2026, 11, 10, 5, 1, 0, 0, time.UTC), GapOtherInstall))
	must(t, err)
	must(t, p2.Commit(ctx))
	fx.commitRevoke(t, j, "bane")
	if got := fx.head(t); got.Seq != 4 || fx.chain(t).Break != nil {
		t.Errorf("head %v, chain break %v: the lines after a WriteLine continue the chain", got, fx.chain(t).Break)
	}
}

// TestFile_WriteLineClosesAnOpenGap: the closing gap line comes first and its
// journal_gap/ record is deleted with the head, whichever way the head is
// committed.
func TestFile_WriteLineClosesAnOpenGap(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	since := time.Date(2026, 11, 10, 3, 30, 0, 0, time.UTC)
	b := fx.auth.NewBatch()
	must(t, j.Gap(b, since))
	must(t, b.Commit(ctx, storage.Sync))
	if open, err := OpenGaps(ctx, fx.ks); err != nil || !open {
		t.Fatalf("OpenGaps = %v, %v before the line", open, err)
	}
	p, err := j.WriteLine(ctx, RestoreLineOf(RestoreInfo{Kind: RestoreExternal, Boundary: 0}, since))
	must(t, err)
	if p.Head().Seq != 2 {
		t.Fatalf("head %v: the closing gap line and the restore line are two lines", p.Head())
	}
	if open, _ := OpenGaps(ctx, fx.ks); !open {
		t.Fatal("the gap record was deleted before the head was committed")
	}
	must(t, p.Commit(ctx))
	if open, err := OpenGaps(ctx, fx.ks); err != nil || open {
		t.Errorf("OpenGaps = %v, %v after the commit: the closed gap's record should be gone", open, err)
	}
	if kinds := kindsOf(fx.chain(t)); len(kinds) != 2 || kinds[0] != Gap || kinds[1] != Restore {
		t.Errorf("kinds = %v, want [gap restore]", kinds)
	}
}

func kindsOf(c *Chain) []Kind {
	var out []Kind
	for _, l := range c.Lines {
		out = append(out, l.Kind)
	}
	return out
}

// TestFile_WriteLineOnABrokenFile: a journal that cannot take a line cannot
// take this one either, and nothing is left behind.
func TestFile_WriteLineOnABrokenFile(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	fx.write(t, "this is not a journal line")
	j, err := OpenFile(ctx, fx.opts())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenFile = %v, want ErrCorrupt", err)
	}
	defer func() { must(t, j.Close()) }()
	if _, err := j.WriteLine(ctx, GapLine(time.Now(), GapOtherInstall)); !errors.Is(err, ErrCorrupt) {
		t.Errorf("WriteLine on a broken file: %v, want ErrCorrupt", err)
	}
	c := fx.component(t)
	if _, err := c.WriteLine(ctx, GapLine(time.Now(), GapOtherInstall)); !errors.Is(err, ErrNotBound) {
		t.Errorf("Component.WriteLine before Bind: %v, want ErrNotBound", err)
	}
}

// TestFile_RefuseKeepsTheFirstFailure: Refuse stops later appends with its
// error, a second Refuse does not replace the first (the first is the cause an
// operator needs), and Refuse(nil) refuses nothing.
func TestFile_RefuseKeepsTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	j.Refuse(nil)
	if _, err := j.WriteLine(ctx, GapLine(time.Now(), GapAppendFailed)); err != nil {
		t.Fatalf("WriteLine after Refuse(nil): %v", err)
	}
	first, second := errors.New("a newer build wrote a line this one cannot apply"), errors.New("the keyring is gone")
	j.Refuse(first)
	j.Refuse(second)
	if _, err := j.WriteLine(ctx, GapLine(time.Now(), GapAppendFailed)); !errors.Is(err, first) || errors.Is(err, second) {
		t.Errorf("WriteLine after two Refuse calls: %v, want the first error only", err)
	}
	if st := j.Status(); !errors.Is(st.Broken, first) {
		t.Errorf("Status().Broken = %v, want the first error", st.Broken)
	}
}

// TestFile_HoldIsLiftedByResumeButNotABreakOrARefusal: a Hold is the refusal
// that ends (a replaced store's head waits for its hooks), so Resume lifts it
// and the journal takes lines again; a Refuse (a line this build cannot apply)
// and a broken open outlast it, and Hold(nil) holds nothing.
func TestFile_HoldIsLiftedByResumeButNotABreakOrARefusal(t *testing.T) {
	ctx := context.Background()
	line := func(j *File) error {
		_, err := j.WriteLine(ctx, GapLine(time.Now(), GapAppendFailed))
		return err
	}
	held, refused := errors.New("the hooks have not run"), errors.New("a newer build wrote a line this one cannot apply")

	fx := newFixture(t)
	j := fx.open(t)
	j.Hold(nil)
	if err := line(j); err != nil {
		t.Fatalf("after Hold(nil): %v", err)
	}
	j.Hold(held)
	j.Hold(errors.New("a second hold"))
	if err := line(j); !errors.Is(err, held) {
		t.Fatalf("held: %v, want the first hold's error", err)
	}
	if st := j.Status(); !errors.Is(st.Broken, held) {
		t.Errorf("Status().Broken = %v while held, want the hold's error (health says unwritable)", st.Broken)
	}
	b := fx.auth.NewBatch()
	if err := j.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, held) {
		t.Errorf("Append while held = %v, want the hold's error", err)
	}
	_ = b.Close()
	j.Resume()
	if err := line(j); err != nil {
		t.Errorf("after Resume: %v", err)
	}
	if st := j.Status(); st.Broken != nil {
		t.Errorf("Status().Broken = %v after Resume", st.Broken)
	}

	j.Hold(held)
	j.Refuse(refused)
	j.Resume()
	if err := line(j); !errors.Is(err, refused) {
		t.Errorf("after Resume of a hold that a Refuse outlasts: %v, want the refusal", err)
	}

	fx2 := newFixture(t)
	fx2.write(t, "this is not a journal line")
	broken, err := OpenFile(ctx, fx2.opts())
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("OpenFile = %v, want ErrCorrupt", err)
	}
	defer func() { must(t, broken.Close()) }()
	broken.Hold(held)
	broken.Resume()
	if err := line(broken); !errors.Is(err, ErrCorrupt) {
		t.Errorf("a broken File after Hold and Resume: %v, want ErrCorrupt", err)
	}

	c := fx.component(t)
	c.Hold(held) // before Bind: nothing to hold
	c.Resume()
}

// TestReadFile_OverTheSizeCapIsCorrupt: the journal is read into memory whole,
// so a file past maxJournalBytes is refused by its size, before any of it is
// read, for the reader and for the writer's open alike.
func TestReadFile_OverTheSizeCapIsCorrupt(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	f, err := os.Create(fx.path)
	must(t, err)
	must(t, f.Truncate(maxJournalBytes+1)) // sparse: nothing is written
	must(t, f.Close())
	if _, err := ReadFile(fx.path, fx.keys); !errors.Is(err, ErrCorrupt) {
		t.Errorf("ReadFile of a file over the cap = %v, want ErrCorrupt", err)
	}
	j, err := OpenFile(context.Background(), fx.opts())
	if !errors.Is(err, ErrCorrupt) || j == nil {
		t.Fatalf("OpenFile of a file over the cap = %v, %v, want a broken File and ErrCorrupt", j, err)
	}
	must(t, j.Close())
}

// failRead fails the test if it is read.
type failRead struct{ t *testing.T }

func (f failRead) Read([]byte) (int, error) {
	f.t.Error("a file measured over the limit was read")
	return 0, io.EOF
}

// TestReadLimited: the limit holds by the size alone and again while reading
// (a file that grew after it was measured).
func TestReadLimited(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        string
		size, limit int64
		ok          bool
	}{
		{"within", "0123456789", 10, 10, true},
		{"measured over", "0123456789a", 11, 10, false},
		{"grew while read", strings.Repeat("x", 20), 0, 10, false},
		{"empty", "", 0, 10, true},
	} {
		var r io.Reader = strings.NewReader(tc.body)
		if tc.size > tc.limit {
			r = failRead{t} // refused by its size alone: none of it is read
		}
		got, err := readLimited(r, "p", tc.size, tc.limit)
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, ErrCorrupt)) {
			t.Errorf("%s: err = %v, want ok %v (ErrCorrupt otherwise)", tc.name, err, tc.ok)
		}
		if tc.ok && string(got) != tc.body {
			t.Errorf("%s: read %q", tc.name, got)
		}
	}
}

// TestCommitHead_NeverLowers: a held head commits when the store's is below it
// and is a no-op when the store's has passed it.
func TestCommitHead_NeverLowers(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	fx.commitRevoke(t, j, "penguin")
	must(t, CommitHead(ctx, fx.ks, Head{Seq: 1, MAC: "old"}))
	if got := fx.head(t); got.Seq != 2 {
		t.Errorf("head = %v after committing a lower one", got)
	}
	must(t, CommitHead(ctx, fx.ks, Head{Seq: 3, MAC: "new"}))
	if got := fx.head(t); got.Seq != 3 || got.MAC != "new" {
		t.Errorf("head = %v, want {3 new}", got)
	}
}

// TestKnownKind_CoversEveryDeclaredKind: every Kind constant this package
// declares is one a build can name as known. A kind added to journal.go and not
// to KnownKind would be reported as one this build cannot apply, which stops
// every boot's re-apply at it; this reads the declarations so that cannot be
// forgotten.
func TestKnownKind_CoversEveryDeclaredKind(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "journal.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var declared []Kind
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Kind" {
			return true
		}
		for _, v := range vs.Values {
			if lit, ok := v.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				declared = append(declared, Kind(strings.Trim(lit.Value, `"`)))
			}
		}
		return true
	})
	if len(declared) < 14 {
		t.Fatalf("found only %d Kind constants in journal.go: %v", len(declared), declared)
	}
	for _, k := range declared {
		if !KnownKind(k) {
			t.Errorf("KnownKind(%q) = false for a declared Kind", k)
		}
	}
	for _, k := range []Kind{"user.frozen", "", "apikey.revoke"} {
		if KnownKind(k) {
			t.Errorf("KnownKind(%q) = true", k)
		}
		if !IsRevocation(k) {
			t.Errorf("IsRevocation(%q) = false: a kind this build does not know must count as a revocation", k)
		}
	}
}

// TestCover_RefusesALineItCannotApply: a restore's coverage fails closed when a
// line after the boundary is of a kind the hooks would skip.
func TestCover_RefusesALineItCannotApply(t *testing.T) {
	line := func(seq uint64, k Kind) Line {
		return Line{Seq: seq, Kind: k, MAC: fmt.Sprint("m", seq)}
	}
	c := &Chain{HasHeader: true, Header: Header{InstallID: installID}, Lines: []Line{line(1, UserDisabled), line(2, "user.frozen")}, ClaimedEnd: 2}
	if got := c.Cover(installID, 0, Head{Seq: 2, MAC: "m2"}, false); got.OK || !strings.Contains(got.Why, "cannot apply") {
		t.Errorf("Cover = %+v, want a refusal naming the kind", got)
	}
	if got := c.Cover(installID, 2, Head{Seq: 2, MAC: "m2"}, false); !got.OK {
		t.Errorf("Cover after the unknown line = %+v: it lies at or before the boundary, so it is not re-applied", got)
	}
}

// ---------------------------------------------------------------------------
// MAJOR 1: files are regular files.

// TestOpenFile_NeverFollowsASymlink: the share holds a link where the journal
// should be. The open refuses it, writes and cuts nothing at the target, and
// reports a broken File whose Gap still works.
func TestOpenFile_NeverFollowsASymlink(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	victim := filepath.Join(t.TempDir(), "victim.txt")
	const text = "someone else's file, with no newline at the end"
	must(t, os.WriteFile(victim, []byte(text), 0o600))
	must(t, os.Symlink(victim, fx.path))
	openRefused(t, fx, "a symlinked journal")
	if got, err := os.ReadFile(victim); err != nil || string(got) != text {
		t.Errorf("the target = %q, %v: it was written through the link", got, err)
	}

	// A dangling link must not be created through either.
	must(t, os.Remove(fx.path))
	missing := filepath.Join(t.TempDir(), "not-there")
	must(t, os.Symlink(missing, fx.path))
	openRefused(t, fx, "a dangling link")
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the open created the link's target: %v", err)
	}
}

// openRefused opens the journal expecting ErrNotRegular and a broken File that
// holds no lock (it never opened anything) and still lets a revocation commit
// with its gap record.
func openRefused(t *testing.T, fx *fixture, what string) {
	t.Helper()
	ctx := context.Background()
	j, err := OpenFile(ctx, fx.opts())
	if !errors.Is(err, ErrNotRegular) || j == nil {
		t.Fatalf("%s: OpenFile = %v, %v; want a broken File and ErrNotRegular", what, j, err)
	}
	defer func() { must(t, j.Close()) }()
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/bane"), []byte("disabled")))
	if err := j.Append(ctx, b, Revocation{Kind: UserDisabled}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("%s: Append = %v, want ErrNotRegular", what, err)
	}
	at := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
	must(t, j.Gap(b, at))
	must(t, b.Commit(ctx, storage.Sync))
	if _, err := fx.ks.Get(ctx, gapKey(at)); err != nil {
		t.Errorf("%s: the gap record: %v", what, err)
	}
	clear := fx.ks.NewBatch()
	must(t, clear.Delete(gapKey(at)))
	must(t, clear.Commit(ctx, storage.Sync))
}

// TestOpenFile_RefusesAnythingButARegularFile: a directory (or a device, or a
// pipe) at the journal path is refused the same way.
func TestOpenFile_RefusesAnythingButARegularFile(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(fx.path, 0o700))
	j, err := OpenFile(context.Background(), fx.opts())
	if !errors.Is(err, ErrNotRegular) && err == nil {
		t.Errorf("OpenFile of a directory = %v", err)
	}
	if j != nil {
		must(t, j.Close())
	}
	if _, err := ReadFile(fx.path, fx.keys); err == nil {
		t.Errorf("ReadFile of a directory = %v", err)
	}

	// A device opens read-write without complaint, so only the check on the
	// descriptor refuses it: the lines would go to the device.
	dev := newFixture(t)
	if fi, err := os.Stat(os.DevNull); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		dev.path = os.DevNull
		openRefused(t, dev, "a device")
		if _, err := ReadFile(os.DevNull, dev.keys); !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile of a device = %v, want ErrNotRegular", err)
		}
	}
}

// TestReadFile_RefusesALink: reading is no more willing to follow a planted
// link than writing is, and says so rather than reporting a missing file.
func TestReadFile_RefusesALink(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	must(t, j.Close())
	real := fx.path + ".real"
	must(t, os.Rename(fx.path, real))
	must(t, os.Symlink(real, fx.path))
	if _, err := ReadFile(fx.path, fx.keys); !errors.Is(err, ErrNotRegular) {
		t.Errorf("ReadFile of a link = %v, want ErrNotRegular", err)
	}
	if _, err := ReadFile(real, fx.keys); err != nil {
		t.Errorf("ReadFile of the file itself = %v", err)
	}
	if _, err := ReadFile(filepath.Join(filepath.Dir(fx.path), "nope"), fx.keys); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadFile of a missing file = %v, want os.ErrNotExist", err)
	}
}

// TestMoveFile_NeverFollowsALink: neither end of a move is a link, and the temp
// name is not written through.
func TestMoveFile_NeverFollowsALink(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	must(t, j.Close())
	elsewhere := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")
	must(t, os.WriteFile(victim, []byte("keep me"), 0o600))

	t.Run("a link as the source", func(t *testing.T) {
		link := filepath.Join(elsewhere, "src-link")
		must(t, os.Symlink(fx.path, link))
		if err := MoveFile(link, filepath.Join(elsewhere, "dst1"), fx.keys); !errors.Is(err, ErrMoveRefused) {
			t.Errorf("MoveFile from a link = %v, want ErrMoveRefused", err)
		}
	})
	t.Run("a link as the destination", func(t *testing.T) {
		dst := filepath.Join(elsewhere, "dst-link")
		must(t, os.Symlink(victim, dst))
		if err := MoveFile(fx.path, dst, fx.keys); !errors.Is(err, ErrMoveRefused) {
			t.Errorf("MoveFile onto a link = %v, want ErrMoveRefused", err)
		}
		if got, _ := os.ReadFile(victim); string(got) != "keep me" {
			t.Errorf("the target = %q", got)
		}
	})
	t.Run("a link planted at the temp name", func(t *testing.T) {
		dst := filepath.Join(elsewhere, "dst3")
		must(t, os.Symlink(victim, dst+".moving"))
		must(t, MoveFile(fx.path, dst, fx.keys))
		if got, _ := os.ReadFile(victim); string(got) != "keep me" {
			t.Errorf("the target = %q: the move wrote through the planted temp name", got)
		}
		if c, err := ReadFile(dst, fx.keys); err != nil || c.Break != nil || len(c.Lines) != 1 {
			t.Errorf("the moved journal = %+v, %v", c, err)
		}
	})
	t.Run("a crash's leftover temp file", func(t *testing.T) {
		dst := filepath.Join(elsewhere, "dst4")
		must(t, os.WriteFile(dst+".moving", []byte("half a line"), 0o600))
		must(t, MoveFile(fx.path, dst, fx.keys))
		if c, err := ReadFile(dst, fx.keys); err != nil || c.Break != nil || len(c.Lines) != 1 {
			t.Errorf("the moved journal = %+v, %v", c, err)
		}
		if _, err := os.Lstat(dst + ".moving"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the temp file is still there: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// The location file.

// TestLocation_DescribesComparesCleanedPaths: the same store spelled with a
// doubled or trailing separator is the same store, and an empty path on either
// side describes nothing.
func TestLocation_DescribesComparesCleanedPaths(t *testing.T) {
	l := Location{DBPath: "/var/lib/gotham/main.pebble", InstallID: "wayne"}
	for _, tc := range []struct {
		name   string
		db, id string
		want   bool
	}{
		{"the same", "/var/lib/gotham/main.pebble", "wayne", true},
		{"a doubled separator", "/var/lib//gotham/main.pebble", "wayne", true},
		{"a trailing separator", "/var/lib/gotham/main.pebble/", "wayne", true},
		{"a dot element", "/var/lib/./gotham/main.pebble", "wayne", true},
		{"a parent element", "/var/lib/other/../gotham/main.pebble", "wayne", true},
		{"another store", "/var/lib/arkham/main.pebble", "wayne", false},
		{"another install", "/var/lib/gotham/main.pebble", "crane", false},
		{"no path at hand", "", "wayne", false},
	} {
		if got := l.Describes(tc.db, tc.id); got != tc.want {
			t.Errorf("%s: Describes = %v, want %v", tc.name, got, tc.want)
		}
	}
	if (Location{}).Describes("", "") || (Location{DBPath: "/x"}).Describes("/x", "") {
		t.Error("an empty record describes a store")
	}
	// An empty path cleans to ".", so two empty paths (or a record of "." against
	// no path) would compare equal without the explicit check.
	for _, rec := range []Location{{InstallID: "wayne"}, {DBPath: ".", InstallID: "wayne"}} {
		if rec.Describes("", "wayne") {
			t.Errorf("the record %+v describes a store at no path", rec)
		}
	}
}

// TestReadLocation_EmptyRecordIsUnreadable: `{}` and `null` decode without an
// error and say nothing, so they would read as "no record describes this
// store" and hide a replacement; they are an error like a file that does not
// decode.
func TestReadLocation_EmptyRecordIsUnreadable(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"db_path":"/x"}`, `{"install_id":"wayne"}`} {
		dir := t.TempDir()
		must(t, os.WriteFile(filepath.Join(dir, LocationFile), []byte(body), 0o600))
		if _, ok, err := ReadLocation(dir); err == nil || ok {
			t.Errorf("ReadLocation(%s) = ok %v, err %v: want an error", body, ok, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Integrity.

// TestChain_NoEditEverVerifiesAForeignLine: whatever is done to the bytes of a
// journal (every byte flipped three ways, every truncation, every line deleted,
// duplicated or swapped with every other, a line spliced in, trailing garbage),
// the lines that verify are a prefix of the original lines and nothing else:
// an attacker without the key can drop the end of a journal, never write into
// it.
func TestChain_NoEditEverVerifiesAForeignLine(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	j := fx.open(t)
	evil := `","mac":"` + strings.Repeat("a", 64) + `"}`
	for _, r := range []Revocation{
		{Kind: APIKeyRevoked, Subject: entity.Ref{Module: "core", Kind: "apikey", ID: "k1"}},
		{Kind: APIKeyRevoked, Subject: entity.Ref{Module: "core", Kind: "apikey", ID: "k2"}},
		{Kind: IdentityUnlinked, Subject: userRef("selina"), Detail: []string{"oidc", evil, "line\nbreak"}},
		{Kind: UserDisabled, Subject: userRef("selina"), Epoch: 3},
		{Kind: PasswordChanged, Subject: userRef("harvey"), Epoch: 2},
	} {
		b := fx.auth.NewBatch()
		must(t, j.Append(ctx, b, r))
		must(t, b.Commit(ctx, storage.Sync))
	}
	must(t, j.Close())
	data, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	orig := ReadChain(data, fx.keys)
	if orig.Break != nil || len(orig.Lines) != 5 {
		t.Fatalf("setup: break %v, %d lines", orig.Break, len(orig.Lines))
	}
	lines := bytes.SplitAfter(data, []byte("\n"))
	lines = lines[:len(lines)-1]
	checked := 0
	check := func(name string, m []byte) {
		checked++
		c := ReadChain(m, fx.keys)
		if len(c.Lines) > len(orig.Lines) {
			t.Errorf("%s: %d lines verify, the original has %d", name, len(c.Lines), len(orig.Lines))
			return
		}
		for i, g := range c.Lines {
			w := orig.Lines[i]
			if g.Seq != w.Seq || g.Kind != w.Kind || g.MAC != w.MAC || g.Subject != w.Subject || g.Object != w.Object ||
				strings.Join(g.Detail, "\x00") != strings.Join(w.Detail, "\x00") || g.Epoch != w.Epoch || g.KID != w.KID {
				t.Errorf("%s: line %d is not the original's: %+v vs %+v", name, i+1, g, w)
				return
			}
		}
		if c.HasHeader && c.Header.MAC != orig.Header.MAC {
			t.Errorf("%s: another header verified", name)
		}
	}
	for i := range data {
		for _, x := range []byte{0x01, 0x20, 0xff} {
			m := bytes.Clone(data)
			m[i] ^= x
			check(fmt.Sprintf("flip byte %d ^%#x", i, x), m)
		}
	}
	for i := 0; i <= len(data); i++ {
		check(fmt.Sprintf("truncate at %d", i), data[:i])
	}
	for i := range lines {
		check(fmt.Sprintf("delete line %d", i), bytes.Join(append(slices.Clone(lines[:i]), lines[i+1:]...), nil))
		check(fmt.Sprintf("duplicate line %d", i), bytes.Join(append(slices.Clone(lines[:i+1]), lines[i:]...), nil))
		for k := range lines {
			if i == k {
				continue
			}
			m := append([][]byte(nil), lines...)
			m[i], m[k] = m[k], m[i]
			check(fmt.Sprintf("swap lines %d and %d", i, k), bytes.Join(m, nil))
		}
	}
	check("no final newline", bytes.TrimSuffix(data, []byte("\n")))
	check("trailing garbage", append(bytes.Clone(data), []byte("{\"seq\":6}\n")...))
	t.Logf("%d edits checked, no foreign line verified", checked)
}
