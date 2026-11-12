// file: core/library/review_p2_test.go
// version: 1.2.1
// guid: a9d0b21f-80d1-4624-8e39-94f7e4871e5d
// last-edited: 2026-11-12

package library

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// clockTick is longer than the timestamp granularity of the filesystems the
// tests run on (a kernel tick of 4 ms on a coarse-clock Linux, a few ms on
// others). A test that makes "another file" waits one before it does, so the
// replacement's inode change time cannot tie with the original's: a tie within
// a tick is the documented residual of the check, not something to assert on.
const clockTick = 25 * time.Millisecond

// stampOf is the stamp a transfer takes of src: the Lstat the transfer was
// checked from, plus the descriptor facts, through the same row the live path
// writes to the journal.
func stampOf(t *testing.T, root *Root, abs string, src Path) srcStamp {
	t.Helper()
	st, err := os.Lstat(abs)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := root.rw.local().factsOf(src.Rel)
	if err != nil {
		t.Fatal(err)
	}
	dev, ino, _ := libfs.InodeOf(st)
	row := transferRow{Size: st.Size(), SrcDev: dev, SrcIno: ino, SrcMTime: st.ModTime().UnixNano()}
	row.recordFacts(facts)
	return row.stamp()
}

// Every fact sourceUnchanged checks has a case in which it is the only one that
// can tell, so none of them can be dropped without a test noticing.
func TestSourceUnchangedChecksEachFact(t *testing.T) {
	const content = "the bytes of the Joke"
	build := func(t *testing.T) (r *rig, root *Root, abs string, src Path, stamp srcStamp) {
		t.Helper()
		r, _ = transferRig(t, rigOpts{})
		src = r.put("import-1", "Arkham/a.m4b", content)
		abs = r.abs("import-1", src.Rel)
		var err error
		if root, err = r.m.Root("import-1"); err != nil {
			t.Fatal(err)
		}
		return r, root, abs, src, stampOf(t, root, abs, src)
	}
	sha := sum(content)
	check := func(t *testing.T, r *rig, root *Root, src Path, stamp srcStamp, strong bool) (bool, error) {
		t.Helper()
		return r.m.sourceUnchanged(ctxbg(), root.rw, src, stamp, sha, strong)
	}
	// replaceKeepingTime makes another file of the same bytes, size and
	// modification time at abs: all the Lstat facts but the inode number match.
	replaceKeepingTime := func(t *testing.T, abs string, mtime int64) {
		t.Helper()
		time.Sleep(clockTick)
		if err := os.Remove(abs); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o664); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(0, mtime)
		if err := os.Chtimes(abs, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unchanged", func(t *testing.T) {
		r, root, _, src, stamp := build(t)
		if present, err := check(t, r, root, src, stamp, true); !present || err != nil {
			t.Errorf("= %v, %v", present, err)
		}
	})
	t.Run("gone", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if err := os.Remove(abs); err != nil {
			t.Fatal(err)
		}
		if present, err := check(t, r, root, src, stamp, true); present || err != nil {
			t.Errorf("= %v, %v; want absent without an error", present, err)
		}
	})
	t.Run("a link to a file with the same bytes", func(t *testing.T) {
		// Nothing recorded but the size, which a link's own length can match; the
		// digest, read through the link, matches too. Only "is it a regular file"
		// can say no.
		r, root, abs, src, _ := build(t)
		if err := os.Rename(abs, abs+".real"); err != nil {
			t.Fatal(err)
		}
		target := filepath.Base(abs) + ".real"
		if err := os.Symlink(target, abs); err != nil {
			t.Fatal(err)
		}
		_, err := check(t, r, root, src, srcStamp{size: int64(len(target))}, true)
		if !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged", err)
		}
	})
	t.Run("another file of the same size and time", func(t *testing.T) {
		// Every fact the Lstat gave is the same except the inode number, and
		// filesystems that free an inode number hand it to the next file made
		// (ext4 does; this is the case the inode number alone cannot
		// tell). What tells it is the inode change time, the creation time or the
		// inode generation, whichever the platform reports.
		r, root, abs, src, stamp := build(t)
		replaceKeepingTime(t, abs, stamp.mtime)
		if _, err := check(t, r, root, src, stamp, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (another inode)", err)
		}
	})
	t.Run("another file, told by the inode alone", func(t *testing.T) {
		// The stamp records the device and inode but no time or generation: a
		// platform that reports neither, or an old journal row. Hold the old inode
		// open so its number cannot be reused: the replacement is another number.
		r, root, abs, src, stamp := build(t)
		held, err := os.Open(abs)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := held.Close(); err != nil {
				t.Error(err)
			}
		}()
		replaceKeepingTime(t, abs, stamp.mtime)
		basic := srcStamp{dev: stamp.dev, ino: stamp.ino, size: stamp.size, mtime: stamp.mtime}
		if _, err := check(t, r, root, src, basic, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (inode number)", err)
		}
	})
	t.Run("a different size, nothing else recorded", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if err := os.WriteFile(abs, []byte(content+", and a new chapter"), 0o664); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, srcStamp{size: stamp.size}, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (size)", err)
		}
	})
	t.Run("a different modification time", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		later := time.Unix(0, stamp.mtime).Add(time.Hour)
		if err := os.Chtimes(abs, later, later); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, srcStamp{size: stamp.size, mtime: stamp.mtime}, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (mtime)", err)
		}
	})
	t.Run("other bytes of the same size, inode and time", func(t *testing.T) {
		// Written in place and the time put back: with only the Lstat facts
		// recorded, only the digest can tell.
		r, root, abs, src, stamp := build(t)
		basic := srcStamp{dev: stamp.dev, ino: stamp.ino, size: stamp.size, mtime: stamp.mtime}
		if err := os.WriteFile(abs, []byte(strings.ToUpper(content)), 0o664); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(0, stamp.mtime)
		if err := os.Chtimes(abs, mt, mt); err != nil {
			t.Fatal(err)
		}
		if present, err := check(t, r, root, src, basic, false); !present || err != nil {
			t.Fatalf("without the digest the file looks unchanged: %v, %v", present, err)
		}
		if _, err := check(t, r, root, src, basic, true); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("with the digest = %v, want ErrSourceChanged", err)
		}
	})
	t.Run("rewritten in place with the time put back, told by the change time", func(t *testing.T) {
		// The same rewrite, but the inode change time was recorded: the kernel
		// moved it and no one can put it back, so no digest is needed.
		r, root, abs, src, stamp := build(t)
		if stamp.ctime == 0 {
			t.Skip("this platform reports no inode change time")
		}
		time.Sleep(clockTick)
		if err := os.WriteFile(abs, []byte(strings.ToUpper(content)), 0o664); err != nil {
			t.Fatal(err)
		}
		mt := time.Unix(0, stamp.mtime)
		if err := os.Chtimes(abs, mt, mt); err != nil {
			t.Fatal(err)
		}
		if _, err := check(t, r, root, src, stamp, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (change time)", err)
		}
	})
	t.Run("a change to the inode only", func(t *testing.T) {
		// Nothing about the bytes, size or modification time moves: a permission
		// change, a new hard link, an extended attribute. The inode change time
		// does, and the source is kept (a cross-device Move removes the source
		// only if it is exactly what was copied).
		for name, touch := range map[string]func(t *testing.T, abs string){
			"chmod": func(t *testing.T, abs string) {
				if err := os.Chmod(abs, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			"hard link": func(t *testing.T, abs string) {
				if err := os.Link(abs, abs+".also"); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name, func(t *testing.T) {
				r, root, abs, src, stamp := build(t)
				if stamp.ctime == 0 {
					t.Skip("this platform reports no inode change time")
				}
				time.Sleep(clockTick)
				touch(t, abs)
				if _, err := check(t, r, root, src, stamp, true); !errors.Is(err, ErrSourceChanged) {
					t.Errorf("= %v, want ErrSourceChanged (change time)", err)
				}
			})
		}
	})
	t.Run("another file, told by the creation time alone", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if stamp.birth == 0 {
			t.Skip("this platform reports no creation time")
		}
		replaceKeepingTime(t, abs, stamp.mtime)
		only := srcStamp{size: stamp.size, birth: stamp.birth}
		if _, err := check(t, r, root, src, only, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (creation time)", err)
		}
	})
	t.Run("another file, told by the generation alone", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		if !stamp.hasGen {
			t.Skip("this filesystem reports no inode generation")
		}
		replaceKeepingTime(t, abs, stamp.mtime)
		only := srcStamp{size: stamp.size, gen: stamp.gen, hasGen: true}
		if _, err := check(t, r, root, src, only, false); !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (generation)", err)
		}
	})
	t.Run("replaced while it is read", func(t *testing.T) {
		// The facts agree before the digest and the digest agrees with the copy:
		// the file is swapped for another with the same bytes while it is being
		// read. Only reading the facts again after the digest sees it. The old
		// descriptor stays open through the swap, so the old inode number is
		// not reused: the second reading is another number.
		r, root, abs, src, stamp := build(t)
		swapped := false
		hooked := openHook{fsOps: root.rw, after: func() {
			if swapped {
				return
			}
			swapped = true
			replaceKeepingTime(t, abs, stamp.mtime)
		}}
		_, err := r.m.sourceUnchanged(ctxbg(), hooked, src, stamp, sha, true)
		if !swapped {
			t.Fatal("the digest never opened the file")
		}
		if !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, want ErrSourceChanged (replaced while it was read)", err)
		}
	})
	t.Run("removed while it is read", func(t *testing.T) {
		r, root, abs, src, stamp := build(t)
		hooked := openHook{fsOps: root.rw, after: func() {
			if err := os.Remove(abs); err != nil {
				t.Error(err)
			}
		}}
		present, err := r.m.sourceUnchanged(ctxbg(), hooked, src, stamp, sha, true)
		if !present || !errors.Is(err, ErrSourceChanged) {
			t.Errorf("= %v, %v; want the source reported present and ErrSourceChanged", present, err)
		}
	})
}

// openHook runs after once the file has been opened for reading, with the
// descriptor still held: the moment a concurrent writer replaces the file under
// a digest in progress.
type openHook struct {
	fsOps
	after func()
}

func (h openHook) Open(name string) (fs.File, error) {
	f, err := h.fsOps.Open(name)
	if err == nil {
		h.after()
	}
	return f, err
}

// The row a Move journals carries the source's facts, so recovery can tell the
// source from a later file with the same inode number. A Copy never removes its
// source, so it does not open it for them.
func TestTransferJournalsTheSourceFactsOfAMoveOnly(t *testing.T) {
	t.Run("Move", func(t *testing.T) {
		r, _, _ := crossDeviceMoveKilledBeforeSourceRemoval(t)
		rows, err := r.m.listTransfers(ctxbg())
		if err != nil || len(rows) != 1 {
			t.Fatalf("listTransfers = %v, %v; want the one row the kill left", rows, err)
		}
		st, err := os.Lstat(r.abs("import-1", "Arkham/a.m4b"))
		if err != nil {
			t.Fatal(err)
		}
		if got := rows[0]; got.SrcCTime == 0 || got.SrcMTime != st.ModTime().UnixNano() || got.SrcIno == 0 {
			t.Errorf("the row has ctime %d, mtime %d, inode %d; want the source's facts", got.SrcCTime, got.SrcMTime, got.SrcIno)
		}
	})
	t.Run("Copy", func(t *testing.T) {
		f := &faults{killAt: 3}
		r, _ := transferRig(t, rigOpts{faults: f})
		src := r.put("import-1", "Arkham/a.m4b", "the original bytes")
		dst := Path{Root: "books-library", Rel: "a.m4b"}
		if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); err == nil {
			t.Fatal("the kill did not stop the copy")
		}
		rows, err := r.m.listTransfers(ctxbg())
		if err != nil || len(rows) != 1 {
			t.Fatalf("listTransfers = %v, %v; want the one row the kill left", rows, err)
		}
		if got := rows[0]; got.SrcCTime != 0 || got.SrcBirth != 0 || got.SrcGen != nil {
			t.Errorf("a Copy row holds facts it never uses: %+v", got)
		}
	})
}

// Each line of srcStamp.differs is one fact, so each has a case no other fact
// can cover, whatever the platform reports.
func TestSrcStampDifferences(t *testing.T) {
	base := libfs.FileFacts{Dev: 7, Ino: 42, Size: 100, MTime: 1000, CTime: 2000, Birth: 3000, Gen: 5, HasGen: true, Regular: true}
	full := srcStamp{dev: 7, ino: 42, size: 100, mtime: 1000, ctime: 2000, birth: 3000, gen: 5, hasGen: true}
	for name, tc := range map[string]struct {
		stamp  srcStamp
		change func(f *libfs.FileFacts)
		want   string // a part of the reason; "" is unchanged
	}{
		"the same file":                 {full, func(*libfs.FileFacts) {}, ""},
		"not a regular file":            {full, func(f *libfs.FileFacts) { f.Regular = false }, "regular file"},
		"another device":                {full, func(f *libfs.FileFacts) { f.Dev = 8 }, "another file"},
		"another inode number":          {full, func(f *libfs.FileFacts) { f.Ino = 43 }, "another file"},
		"another size":                  {full, func(f *libfs.FileFacts) { f.Size = 101 }, "bytes"},
		"another modification time":     {full, func(f *libfs.FileFacts) { f.MTime = 1001 }, "modified"},
		"another change time":           {full, func(f *libfs.FileFacts) { f.CTime = 2001 }, "change time"},
		"no change time any more":       {full, func(f *libfs.FileFacts) { f.CTime = 0 }, "change time"},
		"another creation time":         {full, func(f *libfs.FileFacts) { f.Birth = 3001 }, "created again"},
		"another generation":            {full, func(f *libfs.FileFacts) { f.Gen = 6 }, "generation"},
		"no generation any more":        {full, func(f *libfs.FileFacts) { f.HasGen = false; f.Gen = 0 }, "generation"},
		"unrecorded facts are skipped":  {srcStamp{size: 100}, func(f *libfs.FileFacts) { f.Dev, f.Ino, f.MTime, f.CTime, f.Birth, f.HasGen = 9, 9, 9, 9, 9, false }, ""},
		"generation zero is a recorded": {srcStamp{size: 100, hasGen: true}, func(f *libfs.FileFacts) { f.Gen = 1 }, "generation"},
	} {
		t.Run(name, func(t *testing.T) {
			got := base
			tc.change(&got)
			why := tc.stamp.differs(got)
			if tc.want == "" {
				if why != "" {
					t.Errorf("differs = %q, want the same file", why)
				}
				return
			}
			if !strings.Contains(why, tc.want) {
				t.Errorf("differs = %q, want it to say %q", why, tc.want)
			}
		})
	}
}

// The facts survive the journal: a row written by one process names the same
// stamp when the next one reads it, and a row from before the facts existed reads
// as a stamp that skips them. A generation of zero is a generation, not an absent
// one.
func TestJournalRowKeepsTheSourceFacts(t *testing.T) {
	zero := uint32(0)
	row := transferRow{Size: 100, SrcDev: 7, SrcIno: 42, SrcMTime: 1000, SrcCTime: 2000, SrcBirth: 3000, SrcGen: &zero}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var back transferRow
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	want := srcStamp{dev: 7, ino: 42, size: 100, mtime: 1000, ctime: 2000, birth: 3000, hasGen: true}
	if got := back.stamp(); got != want {
		t.Errorf("stamp = %+v, want %+v", got, want)
	}
	old := `{"id":"x","size":100,"src_dev":7,"src_ino":42,"src_mtime_ns":1000}`
	var legacy transferRow
	if err := json.Unmarshal([]byte(old), &legacy); err != nil {
		t.Fatal(err)
	}
	if got, want := legacy.stamp(), (srcStamp{dev: 7, ino: 42, size: 100, mtime: 1000}); got != want {
		t.Errorf("a row without the new facts: stamp = %+v, want %+v", got, want)
	}
}

// Recovery removes a cross-device Move's source only after reading it again: a
// file rewritten in place, with its size and time put back, is not the file that
// was copied.
func TestRecoveryKeepsASourceRewrittenInPlace(t *testing.T) {
	r, _, _ := crossDeviceMoveKilledBeforeSourceRemoval(t)
	abs := r.abs("import-1", "Arkham/a.m4b")
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	const rewritten = "THE ORIGINAL BYTES" // the same 18 bytes
	if int64(len(rewritten)) != st.Size() {
		t.Fatalf("fixture: %d bytes, want %d", len(rewritten), st.Size())
	}
	if err := os.WriteFile(abs, []byte(rewritten), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	rep, rerr := r.m.Recover(ctxbg())
	if rerr != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v; want one conflict", rep, rerr)
	}
	if got, _ := r.read("import-1", "Arkham/a.m4b"); got != rewritten {
		t.Errorf("the rewritten source was removed or changed: %q", got)
	}
	if got, _ := r.read("books-library", "a.m4b"); got != "the original bytes" {
		t.Errorf("destination = %q", got)
	}
}

// A temp the cleanup could not remove is still owed: the row stays, says so, and
// recovery removes it.
func TestTempThatCannotBeRemovedKeepsTheRow(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		switch {
		case strings.Contains(label, ":link:"):
			return errors.New("the Batcave link failed")
		case strings.Contains(label, ":remove:") && strings.Contains(label, tmpPrefix):
			return errors.New("the Batcave is on fire")
		}
		return nil
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(copyOpts()))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want ErrRecoveryPending", err)
	}
	if r.journalRows() != 1 || len(r.strays()) != 1 {
		t.Fatalf("rows %d, strays %v: the row must stay while a temp does", r.journalRows(), r.strays())
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.RolledBack != 1 || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 || r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) {
		t.Errorf("after recovery: strays %v, rows %d, dst %v, src %v", r.strays(), r.journalRows(), r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel))
	}
}

// A Move has no temp, so a placeholder it cannot take away is the only thing the
// row is kept for.
func TestMoveWhosePlaceholderCannotBeRemovedKeepsTheRow(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") || label == "books-library:remove:Gordon/joke.m4b" {
			return errors.New("the Batcave is on fire")
		}
		return noAtomicRename(label)
	}
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, withMkdir(move()))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want ErrRecoveryPending", err)
	}
	if got := r.placingRows(); len(got) != 1 || got[0] == nil || !r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) {
		t.Fatalf("placing %v, placeholder %v, source %v", got, r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel))
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.RolledBack != 1 || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if r.exists(dst.Root, dst.Rel) || !r.exists(src.Root, src.Rel) || r.journalRows() != 0 {
		t.Errorf("after recovery: placeholder %v, source %v, rows %d", r.exists(dst.Root, dst.Rel), r.exists(src.Root, src.Rel), r.journalRows())
	}
}

// A Replace whose publish fails puts the old file back, on a filesystem where even
// that restore has to reserve the name first.
func TestReplaceRestoresThroughAPlaceholder(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") && strings.Contains(label, tmpPrefix) {
			return errors.New("the Batcave is on fire") // the new bytes cannot be put in place
		}
		return neither(label)
	}
	const old = "the old edition of the Joke"
	r.put("books-library", "Gordon/joke.m4b", old)
	src := r.put("import-1", "Arkham/joke.m4b", gas)
	dst := Path{Root: "books-library", Rel: "Gordon/joke.m4b"}
	ro := withMkdir(copyOpts())
	ro.Replace = &ReplaceSpec{ExpectSHA256: sum(old)}
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, ro); err == nil {
		t.Fatal("Transfer succeeded")
	}
	if got, _ := r.read(dst.Root, dst.Rel); got != old {
		t.Errorf("the old file was not put back: %q", got)
	}
	if len(r.strays()) != 0 || r.journalRows() != 0 || !r.exists(src.Root, src.Rel) {
		t.Errorf("strays %v, rows %d, source %v", r.strays(), r.journalRows(), r.exists(src.Root, src.Rel))
	}
}

// A Release that fails and cannot remove its placeholder leaves the record
// "releasing" with the placeholder named, so recovery removes it; a record put
// back to "held" would leave the empty file at the target for good.
func TestReleaseWhosePlaceholderCannotBeRemovedStaysReleasing(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noAtomicRename
	id, err := r.m.Quarantine(ctxbg(), books, r.put("books-library", "Gotham/held.m4b", gas), "damaged")
	if err != nil {
		t.Fatal(err)
	}
	to := Path{Root: "books-library", Rel: "Gotham/back.m4b"}
	f.inject = func(label string) error {
		if strings.Contains(label, ":renameover:") || label == "books-library:remove:Gotham/back.m4b" {
			return errors.New("the Batcave is on fire")
		}
		return noAtomicRename(label)
	}
	if _, err := r.m.Release(ctxbg(), books, id, &to); err == nil {
		t.Fatal("Release succeeded")
	}
	row, err := r.m.loadQuarantine(ctxbg(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != qReleasing || row.Placing == nil || !r.exists(to.Root, to.Rel) {
		t.Fatalf("record %s placing %v, placeholder %v: want releasing with the placeholder named", row.State, row.Placing, r.exists(to.Root, to.Rel))
	}
	r.m = r.build(rigOpts{})
	if rep, rerr := r.m.Recover(ctxbg()); rerr != nil || rep.Failed != 0 {
		t.Fatalf("Recover = %v, %v", rep, rerr)
	}
	if r.exists(to.Root, to.Rel) {
		t.Error("recovery left the placeholder at the release target")
	}
	row, err = r.m.loadQuarantine(ctxbg(), id)
	if err != nil || row.State != qHeld || row.Placing != nil {
		t.Errorf("after recovery: %v, %v; want held", row, err)
	}
}

// The source leaf of a rename is judged by identity from its directory's
// descriptor, after the name check: a directory swapped for a remembered area
// between the two is refused. The swap happens when the destination is checked,
// which is after the source's name check and before the act.
func TestRenameSourceSwappedAfterItsNameCheckIsRefusedByDescriptor(t *testing.T) {
	var hook func(stage, name string)
	s := spec("books-library", books, RoleLibrary)
	s.Quarantine = ".failed"
	r := newRig(t, newStore(t), rigOpts{seams: seams{fsRace: func(stage, name string) {
		if hook != nil {
			hook(stage, name)
		}
	}}}, s)
	r.put("books-library", ".failed/held.m4b", "held bytes")
	if err := os.Mkdir(r.abs("books-library", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("x", "y"); err != nil { // remembers the area's identity
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("y", "x"); err != nil {
		t.Fatal(err)
	}
	swapped := false
	hook = func(stage, name string) {
		if stage == "checked" && !swapped && name == "elsewhere" {
			swapped = true
			if err := os.Remove(r.abs("books-library", "x")); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(r.abs("books-library", ".failed"), r.abs("books-library", "x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := fsys.RenameNoReplace("x", "elsewhere"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("rename of a source swapped for the quarantine area = %v, want ErrReservedPath", err)
	}
	if !swapped {
		t.Fatal("the hook never ran")
	}
	if !r.exists("books-library", "x/held.m4b") || r.exists("books-library", "elsewhere") {
		t.Error("the area moved")
	}
}

// A directory that HOLDS an area is carried along by a rename, so the leaf of a
// rename is refused by identity at the act as well as by name before it: a name
// swapped for the area-holding directory after the name check. The identity is
// faked (a second name for the directory a case-insensitive volume or a bind
// mount would give) only once the name check has passed, so only the act's own
// check can see it.
func TestRenameLeafHoldingAnAreaIsRefusedByIdentityAtTheAct(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	var armed atomic.Bool
	leaf := filepath.Join(dir, "x")
	seam := func(i fs.FileInfo) (uint64, uint64, bool) {
		if armed.Load() && i.Name() == "state" {
			// "state" now answers with the identity of x: x is a second name for it.
			st, err := os.Lstat(leaf)
			if err != nil {
				return 0, 0, false
			}
			return libfs.InodeOf(st)
		}
		return libfs.InodeOf(i)
	}
	s := spec("books-library", books, RoleLibrary)
	s.Path, s.AppDir = dir, "state/app"
	r := newRig(t, newStore(t), rigOpts{seams: seams{inodeOf: seam, fsRace: func(stage, name string) {
		if stage == "checked" && name == "elsewhere" {
			armed.Store(true)
		}
	}}}, s)
	for _, d := range []string{"x", "y"} {
		if err := os.Mkdir(r.abs("books-library", d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.RenameNoReplace("y", "z"); err != nil { // an ordinary directory moves
		t.Fatalf("rename of an ordinary directory = %v", err)
	}
	if err := fsys.RenameNoReplace("x", "elsewhere"); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("rename of a leaf that is a second name for the area's holder = %v, want ErrReservedPath", err)
	}
	if !armed.Load() {
		t.Fatal("the hook never ran")
	}
	if !r.exists("books-library", "x") || r.exists("books-library", "elsewhere") {
		t.Error("the directory moved")
	}
}

// renameFresh is for a name nobody else can be making (a ULID names it), and
// where the filesystem has no atomic no-replace rename it checks the name is
// absent: it never replaces a name that is there.
func TestRenameFreshNeverReplacesAnExistingName(t *testing.T) {
	r, f := transferRig(t, rigOpts{})
	f.inject = noAtomicRename
	r.put("books-library", "Gotham/held.m4b", "the one being moved")
	r.put("books-library", "Gotham/taken.m4b", "a name that must keep its bytes")
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	err = r.m.renameFresh(root.rw, "Gotham/held.m4b", root.rw, "Gotham/taken.m4b")
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("renameFresh onto an existing name = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", "Gotham/taken.m4b"); got != "a name that must keep its bytes" {
		t.Errorf("the existing name was replaced: %q", got)
	}
	if !r.exists("books-library", "Gotham/held.m4b") {
		t.Error("the source was moved")
	}
	// Where the name is free it is a plain rename, with no placeholder made.
	if err := r.m.renameFresh(root.rw, "Gotham/held.m4b", root.rw, "Gotham/free.m4b"); err != nil {
		t.Fatalf("renameFresh onto a free name = %v", err)
	}
	if got, _ := r.read("books-library", "Gotham/free.m4b"); got != "the one being moved" || r.exists("books-library", "Gotham/held.m4b") {
		t.Errorf("free name holds %q, source still there %v", got, r.exists("books-library", "Gotham/held.m4b"))
	}
}
