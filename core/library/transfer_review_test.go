// file: core/library/transfer_review_test.go
// version: 1.0.0
// guid: af173944-fc53-437c-9cc7-c9e516a3410d
// last-edited: 2026-11-11

package library

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	jokeNew = "NEW edition bytes of the Joke"
	jokeOld = "OLD edition bytes of the Joke"
)

func jokePaths() (src, dst Path) {
	return Path{Root: "import-1", Rel: "Arkham/Joke.m4b"}, Path{Root: "books-library", Rel: "Moore/Joke.m4b"}
}

func replaceCopy() TransferOptions {
	o := copyOpts()
	o.Replace = &ReplaceSpec{ExpectSHA256: sum(jokeOld)}
	return o
}

// killedAt runs a Replace copy over a fresh rig, killing the process at the
// first step whose label matches (the step itself does not happen), and returns
// the rig as the dead process left it.
func killedAt(t *testing.T, opts TransferOptions, match func(label string) bool) *rig {
	t.Helper()
	src, dst := jokePaths()
	setup := func(r *rig) {
		r.put("import-1", src.Rel, jokeNew)
		r.put("books-library", dst.Rel, jokeOld)
	}
	dry := &faults{}
	dr, _ := transferRig(t, rigOpts{faults: dry})
	setup(dr)
	if _, err := dr.m.Transfer(ctxbg(), books, src, dst, opts); err != nil {
		t.Fatal(err)
	}
	k := 0
	for i, l := range dry.labels {
		if match(l) {
			k = i + 1
			break
		}
	}
	if k == 0 {
		t.Fatalf("no step matches in %v", dry.labels)
	}
	f := &faults{killAt: k}
	r, _ := transferRig(t, rigOpts{faults: f})
	setup(r)
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, opts); err == nil {
		t.Fatal("the kill did not stop the transfer")
	}
	return r
}

// A row that says "published" for a destination whose directory entry a power
// loss took back must not delete the temp or the replaced file: either may be
// the only copy. With the temp left, recovery publishes it again.
func TestRecoveryForwardPublishesAgainWhenTheDestinationEntryIsLost(t *testing.T) {
	_, dst := jokePaths()
	// Killed at the removal of the temp: the row says published, the temp is there.
	r := killedAt(t, replaceCopy(), func(l string) bool { return strings.Contains(l, ":remove:") && strings.Contains(l, tmpPrefix) })
	if err := os.Remove(r.abs("books-library", dst.Rel)); err != nil {
		t.Fatalf("setup: the destination should exist at the kill point: %v", err)
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Completed != 1 || rep.Conflicts != 0 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != jokeNew {
		t.Errorf("the destination holds %q, want the new bytes published again", got)
	}
	if l := r.strays(); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("strays %v, journal rows %d", l, r.journalRows())
	}
}

// With the temp gone too and the replaced file still set aside, the old file goes
// back: nothing is lost, the source still holds the new bytes, and the row is
// not reported as a completed transfer.
func TestRecoveryForwardRestoresTheReplacedFileWhenNothingElseIsLeft(t *testing.T) {
	src, dst := jokePaths()
	r := killedAt(t, replaceCopy(), func(l string) bool { return strings.Contains(l, ":remove:") && strings.Contains(l, asideInfix) })
	if err := os.Remove(r.abs("books-library", dst.Rel)); err != nil {
		t.Fatalf("setup: the destination should exist at the kill point: %v", err)
	}
	if len(r.leftovers("books-library")) != 1 {
		t.Fatalf("setup: want only the aside left, have %v", r.leftovers("books-library"))
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Completed != 0 || rep.RolledBack != 1 || rep.Conflicts != 0 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != jokeOld {
		t.Errorf("the destination holds %q, want the old file back", got)
	}
	if got, _ := r.read("import-1", src.Rel); got != jokeNew {
		t.Errorf("the source holds %q", got)
	}
	if l := r.strays(); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("strays %v, journal rows %d", l, r.journalRows())
	}
}

// A published row with the destination gone and nothing else left is dropped
// loudly: it is a conflict, and nothing is deleted.
func TestRecoveryForwardWithNothingLeftIsAConflict(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	src, dst := jokePaths()
	r.put("import-1", src.Rel, jokeNew)
	row := transferRow{ID: r.m.seam.newID(), State: stPublished, Mode: Copy, Did: Copy, Src: src, Dst: dst, SrcSHA: sum(jokeNew),
		Tmp: path2(dst.Dir().Rel, tmpPrefix+"x")}
	if err := r.m.jrn.put(ctxbg(), row.ID, row); err != nil {
		t.Fatal(err)
	}
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Conflicts != 1 || r.journalRows() != 0 {
		t.Fatalf("Recover = %v, %v, rows %d", rep, err, r.journalRows())
	}
	if !r.exists("import-1", src.Rel) {
		t.Error("the source was deleted")
	}
}

// A Replace whose destination is not the new bytes (a rival took the name after
// the publish was lost) keeps the replaced file and the temp.
func TestRecoveryForwardKeepsTheReplacedFileWhenTheDestinationIsNotOurs(t *testing.T) {
	_, dst := jokePaths()
	r := killedAt(t, replaceCopy(), func(l string) bool { return strings.Contains(l, ":remove:") && strings.Contains(l, tmpPrefix) })
	// The kill left the destination as a hard link to the temp: replace the link
	// itself, not the bytes both names share.
	if err := os.Remove(r.abs("books-library", dst.Rel)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.abs("books-library", dst.Rel), []byte("a rival's bytes"), 0o664); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if len(r.copies(jokeOld)) != 1 {
		t.Errorf("the replaced file's bytes are gone: %v", r.strays())
	}
}

// A cross-device Move removes its source only while it is still the file that
// was copied. The review probe appended to the source after the verify; the live
// path deleted it, recovery kept it.
func TestMoveKeepsASourceThatChangedAfterTheCopy(t *testing.T) {
	src, dst := jokePaths()
	for name, change := range map[string]struct {
		verify TransferOptions
		edit   func(t *testing.T, abs string)
	}{
		"appended": {move(), func(t *testing.T, abs string) {
			fh, err := os.OpenFile(abs, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fh.WriteString(" + appended by the writer still recording"); err != nil {
				t.Fatal(err)
			}
			if err := fh.Close(); err != nil {
				t.Fatal(err)
			}
		}},
		// Same size and bytes, another file with the old modification time: only
		// the inode tells it apart.
		"replaced by an identical file": {move(), func(t *testing.T, abs string) {
			st, err := os.Stat(abs)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(abs); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(abs, []byte(jokeNew), 0o664); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(abs, st.ModTime(), st.ModTime()); err != nil {
				t.Fatal(err)
			}
		}},
		// VerifySize does not hash: the modification time still catches it.
		"rewritten in place at the same size": {NewTransferOptions(Move, PermLibrary, VerifySize), func(t *testing.T, abs string) {
			if err := os.WriteFile(abs, []byte(strings.ToLower(jokeNew)), 0o664); err != nil {
				t.Fatal(err)
			}
			later := mustFutureTime(t, abs)
			if err := os.Chtimes(abs, later, later); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := &faults{}
			r, _ := transferRig(t, rigOpts{faults: f})
			r.put("import-1", src.Rel, jokeNew)
			opts := change.verify
			opts.MkdirAll = true
			edited := false
			f.inject = func(label string) error {
				if crossRoots(label) != nil {
					return ErrCrossDevice
				}
				// The last step before the source is examined for removal. A write
				// that lands after the examination, in the microseconds before the
				// unlink, cannot be told apart by any check (see the seams note).
				if strings.HasPrefix(label, "books-library:syncdir:") && !edited {
					edited = true
					change.edit(t, r.abs("import-1", src.Rel))
				}
				return nil
			}
			_, err := r.m.Transfer(ctxbg(), books, src, dst, opts)
			if !errors.Is(err, ErrSourceChanged) || errors.Is(err, ErrRecoveryPending) {
				t.Fatalf("Transfer = %v, want ErrSourceChanged and nothing pending", err)
			}
			if !r.exists("import-1", src.Rel) {
				t.Fatal("the live path deleted a source that changed after it was copied")
			}
			if got, _ := r.read("books-library", dst.Rel); got != jokeNew {
				t.Errorf("the destination holds %q", got)
			}
			if r.journalRows() != 0 || len(r.strays()) != 0 {
				t.Errorf("a settled transfer left %d rows and %v", r.journalRows(), r.strays())
			}
		})
	}
}

// Recovery asks the same question: a source swapped for an identical file (same
// bytes, same size, same time) is another inode and is kept.
func TestRecoveryKeepsASourceThatIsAnotherInode(t *testing.T) {
	r, src, _ := crossDeviceMoveKilledBeforeSourceRemoval(t)
	abs := r.abs("import-1", src.Rel)
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("the original bytes"), 0o664); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if !r.exists("import-1", src.Rel) {
		t.Error("recovery removed a source that is another file")
	}
}

func mustFutureTime(t *testing.T, abs string) time.Time {
	t.Helper()
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime().Add(7 * time.Second)
}

// A failed restore of the replaced file keeps the journal row: it is the only
// record of where the old file sits. The review probe had a rival create the
// destination between the aside and the publish; the restore failed, the row was
// deleted, and the old file was orphaned.
func TestFailedRestoreKeepsTheRowThatNamesTheReplacedFile(t *testing.T) {
	src, dst := jokePaths()
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	r.put("import-1", src.Rel, jokeNew)
	r.put("books-library", dst.Rel, jokeOld)
	f.inject = func(label string) error {
		if strings.Contains(label, ":link:") && strings.HasSuffix(label, "->"+dst.Rel) {
			if err := os.WriteFile(r.abs("books-library", dst.Rel), []byte("RIVAL"), 0o664); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, replaceCopy())
	if !errors.Is(err, fs.ErrExist) || !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Transfer = %v, want the rival's ErrExist marked for recovery", err)
	}
	if r.journalRows() != 1 {
		t.Fatalf("%d journal rows, want the one that names the replaced file", r.journalRows())
	}
	if got := r.copies(jokeOld); len(got) != 1 || !strings.Contains(got[0], asideInfix) {
		t.Fatalf("the replaced file is at %v", got)
	}
	// The rival still holds the name, so recovery cannot put the file back; it keeps
	// it, says so, and never deletes it.
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if len(r.copies(jokeOld)) != 1 {
		t.Error("recovery deleted the replaced file")
	}
}

// When the destination is free again, recovery puts the replaced file back.
func TestRecoveryRestoresTheFileAFailedRestoreLeft(t *testing.T) {
	src, dst := jokePaths()
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	r.put("import-1", src.Rel, jokeNew)
	r.put("books-library", dst.Rel, jokeOld)
	f.inject = func(label string) error {
		if strings.Contains(label, ":link:") && strings.HasSuffix(label, "->"+dst.Rel) {
			return syscall.EIO // the publish fails
		}
		if strings.Contains(label, ":rename:") && strings.Contains(label, asideInfix) && strings.HasSuffix(label, "->"+dst.Rel) {
			return syscall.EIO // and so does the restore
		}
		return nil
	}
	_, err := r.m.Transfer(ctxbg(), books, src, dst, replaceCopy())
	if !errors.Is(err, syscall.EIO) || !errors.Is(err, ErrRecoveryPending) || r.journalRows() != 1 {
		t.Fatalf("Transfer = %v, rows %d", err, r.journalRows())
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.RolledBack != 1 {
		t.Fatalf("Recover = %v, %v", rep, err)
	}
	if got, _ := r.read("books-library", dst.Rel); got != jokeOld {
		t.Errorf("the destination holds %q, want the old file back", got)
	}
	if l := r.strays(); len(l) != 0 || r.journalRows() != 0 {
		t.Errorf("strays %v, rows %d", l, r.journalRows())
	}
}

// After the final name exists a failure is ErrRecoveryPending, not a plain error:
// the transfer happened, the journal says what is left, and Recover finishes it.
func TestErrorAfterPublishIsPendingRecovery(t *testing.T) {
	src, dst := jokePaths()
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	r.put("import-1", src.Rel, jokeNew)
	linked := false
	f.inject = func(label string) error {
		if strings.Contains(label, ":link:") {
			linked = true
		}
		if linked && strings.Contains(label, ":syncdir:") {
			return syscall.EIO
		}
		return nil
	}
	res, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts())
	if !errors.Is(err, ErrRecoveryPending) || !errors.Is(err, syscall.EIO) {
		t.Fatalf("Transfer = %v", err)
	}
	if res.Did != Copy || res.Bytes != int64(len(jokeNew)) {
		t.Errorf("the partial result = %+v, want Did Copy and the bytes copied", res)
	}
	if got, _ := r.read("books-library", dst.Rel); got != jokeNew {
		t.Fatalf("the destination holds %q", got)
	}
	if r.journalRows() != 1 {
		t.Fatalf("%d rows left, want the one recovery finishes", r.journalRows())
	}
	f.inject = nil
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Completed != 1 || r.journalRows() != 0 || len(r.strays()) != 0 {
		t.Fatalf("Recover = %v, %v, rows %d, strays %v", rep, err, r.journalRows(), r.strays())
	}
}

// A refused transfer that changed nothing leaves nothing behind: no row, no
// temp. (Every failure now funnels through one decision, so the plain cases are
// pinned.)
func TestFailedTransfersSettleTheirRows(t *testing.T) {
	src, dst := jokePaths()
	for name, inject := range map[string]func(string) error{
		"the temp cannot be created": func(l string) error {
			if strings.Contains(l, ":create:") {
				return syscall.ENOSPC
			}
			return nil
		},
		"the publish loses a race": func(l string) error {
			if strings.Contains(l, ":link:") {
				return fs.ErrExist
			}
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &faults{inject: inject}
			r, _ := transferRig(t, rigOpts{faults: f})
			r.put("import-1", src.Rel, jokeNew)
			if _, err := r.m.Transfer(ctxbg(), books, src, dst, copyOpts()); err == nil || errors.Is(err, ErrRecoveryPending) {
				t.Fatalf("Transfer = %v", err)
			}
			if r.journalRows() != 0 || len(r.strays()) != 0 || r.exists("books-library", dst.Rel) {
				t.Errorf("rows %d, strays %v, dst %v", r.journalRows(), r.strays(), r.exists("books-library", dst.Rel))
			}
		})
	}
}

// A Replace may not touch a protected destination: files under protected paths
// are never moved or deleted, and a Replace sets the old one aside and removes
// it. A Move INTO a protected subtree stays allowed: the protection is of what
// is already there as a source, not of the places files may arrive.
func TestReplaceRefusesAProtectedDestination(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("books-library", "Locked/x.m4b", jokeOld)
	src := r.put("import-1", "new.m4b", jokeNew)
	setProtected(t, r, filepath.Join(r.dirs["books-library"], "Locked"))
	for _, mode := range []TransferMode{Copy, Move} {
		o := NewTransferOptions(mode, PermLibrary, VerifySHA256)
		o.Replace = &ReplaceSpec{ExpectSHA256: sum(jokeOld)}
		_, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "Locked/x.m4b"}, o)
		if !errors.Is(err, ErrSourceProtected) {
			t.Errorf("%s with Replace over a protected file = %v, want ErrSourceProtected", mode, err)
		}
		if got, _ := r.read("books-library", "Locked/x.m4b"); got != jokeOld || len(r.strays()) != 0 || r.journalRows() != 0 {
			t.Errorf("%s: the protected file holds %q, strays %v, rows %d", mode, got, r.strays(), r.journalRows())
		}
	}
	// Arriving in the protected directory is fine.
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "Locked/new.m4b"}, move()); err != nil {
		t.Errorf("Move into a protected subtree = %v, want it allowed", err)
	}
}

// A directory or a link cannot cross a device boundary: the copy path moves the
// bytes of a regular file. The error says so (it used to be ErrVerifyFailed), and
// a replaced file is put back.
func TestCrossDeviceMoveOfADirectoryIsNotSupported(t *testing.T) {
	f := &faults{inject: crossRoots}
	r, _ := transferRig(t, rigOpts{faults: f})
	r.put("import-1", "Arkham/Joke/01.m4b", jokeNew)
	mv := move()
	mv.MkdirAll = true
	_, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Arkham/Joke"}, Path{Root: "books-library", Rel: "Moore/Joke"}, mv)
	if !errors.Is(err, ErrNotSupported) || errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("Transfer = %v, want ErrNotSupported", err)
	}
	if !r.exists("import-1", "Arkham/Joke/01.m4b") || r.exists("books-library", "Moore/Joke") || r.journalRows() != 0 || len(r.strays()) != 0 {
		t.Errorf("the refused move left rows %d, strays %v", r.journalRows(), r.strays())
	}
}

// A journal row is read back from storage and names paths; one whose id is not a
// ULID, or whose aside is not a name beside its destination, is dropped without
// touching a file (an id with a slash would make the temp's name a path).
func TestRecoveryRefusesARowThatNamesAForeignFile(t *testing.T) {
	src, dst := jokePaths()
	for name, edit := range map[string]func(*transferRow){
		"an id that is a path":             func(r *transferRow) { r.ID = "../../Moore/Joke" },
		"an aside that is another file":    func(r *transferRow) { r.Aside = "Moore/Other.m4b" },
		"an aside that climbs out of root": func(r *transferRow) { r.Aside = dst.Rel + asideInfix + "../../x" },
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := transferRig(t, rigOpts{})
			r.put("import-1", src.Rel, jokeNew)
			r.put("books-library", dst.Rel, jokeOld)
			r.put("books-library", "Moore/Other.m4b", "someone else's file")
			row := transferRow{ID: r.m.seam.newID(), State: stPublished, Mode: Copy, Did: Copy, Src: src, Dst: dst}
			edit(&row)
			if rowProblem(row) == nil {
				t.Fatalf("rowProblem accepted %+v", row)
			}
			if err := r.m.jrn.put(ctxbg(), row.ID, row); err != nil {
				t.Fatal(err)
			}
			rep, err := r.m.Recover(ctxbg())
			if err != nil || rep.Conflicts != 1 {
				t.Fatalf("Recover = %v, %v", rep, err)
			}
			for rel, want := range map[string]string{"Moore/Joke.m4b": jokeOld, "Moore/Other.m4b": "someone else's file"} {
				if got, _ := r.read("books-library", rel); got != want {
					t.Errorf("%s holds %q", rel, got)
				}
			}
		})
	}
}
