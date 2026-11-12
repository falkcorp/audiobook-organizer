// file: core/library/special_files_test.go
// version: 1.1.0
// guid: 30e22335-fdf1-410b-96fb-a7d369ea79d5
// last-edited: 2026-11-11

//go:build unix

package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fifoRig is a transfer rig with a FIFO in the import root. A regression would
// block forever on it, so every call runs under a watchdog that opens the FIFO's
// write end to let the blocked goroutine go, and fails the test.
func fifoRig(t *testing.T, names ...string) (*rig, *faults) {
	t.Helper()
	f := &faults{}
	r, _ := transferRig(t, rigOpts{faults: f})
	for _, n := range names {
		if err := syscall.Mkfifo(filepath.Join(r.dirs["import-1"], n), 0o644); err != nil {
			t.Skipf("no FIFOs here: %v", err)
		}
	}
	return r, f
}

func within(t *testing.T, r *rig, what string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Errorf("%s is still blocked on a FIFO three seconds on", what)
		_ = filepath.WalkDir(r.dirs["import-1"], func(p string, d os.DirEntry, err error) error {
			if err == nil && d.Type()&os.ModeNamedPipe != 0 {
				if fh, e := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); e == nil {
					_ = fh.Close()
				}
			}
			return nil
		})
		return <-done
	}
}

// Hash, a transfer in any mode that reads or moves the source, and a view's
// Open all refuse a FIFO at once: opening one for reading waits for a writer,
// and neither the context nor the path locks can reach a goroutine in open(2).
func TestFIFOIsRefusedNotWaitedOn(t *testing.T) {
	r, f := fifoRig(t, "x.m4b")
	src := Path{Root: "import-1", Rel: "x.m4b"}
	ctx, cancel := context.WithTimeout(ctxbg(), 300*time.Millisecond)
	defer cancel()
	if err := within(t, r, "Hash", func() error {
		_, err := r.m.Hash(ctx, src)
		return err
	}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("Hash of a FIFO = %v, want ErrNotSupported", err)
	}
	for _, mode := range []TransferMode{Copy, Move, Hardlink, Reflink, Auto} {
		for _, cross := range []bool{false, true} {
			if cross && mode != Move {
				continue
			}
			t.Run(fmt.Sprintf("%s cross=%v", mode, cross), func(t *testing.T) {
				f.inject = nil
				if cross {
					f.inject = crossRoots
				}
				o := NewTransferOptions(mode, PermLibrary, VerifySHA256)
				o.MkdirAll = true
				err := within(t, r, mode.String(), func() error {
					_, err := r.m.Transfer(ctx, books, src, Path{Root: "books-library", Rel: "y.m4b"}, o)
					return err
				})
				if !errors.Is(err, ErrNotSupported) {
					t.Errorf("%s of a FIFO = %v, want ErrNotSupported", mode, err)
				}
				if r.exists("books-library", "y.m4b") || r.journalRows() != 0 || len(r.strays()) != 0 {
					t.Errorf("rows %d, strays %v", r.journalRows(), r.strays())
				}
			})
		}
	}
	root, err := r.m.Root("import-1")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(CoreOwner)
	if err != nil {
		t.Fatal(err)
	}
	g, err := root.GuardedFS()
	if err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]func() error{
		"FS.Open":         func() error { _, err := fsys.Open("x.m4b"); return err },
		"FS.OpenFile":     func() error { _, err := fsys.OpenFile("x.m4b", os.O_RDONLY, 0); return err },
		"FS.ReadDir":      func() error { _, err := fsys.ReadDir("x.m4b"); return err },
		"Guarded.Open":    func() error { _, err := g.Open("x.m4b"); return err },
		"Guarded.ReadDir": func() error { _, err := g.ReadDir("x.m4b"); return err },
	} {
		if err := within(t, r, name, open); !errors.Is(err, ErrNotSupported) {
			t.Errorf("%s of a FIFO = %v, want ErrNotSupported", name, err)
		}
	}
	// Seeing that it exists is still possible: a listing shows the name.
	if ents, err := fsys.ReadDir("."); err != nil || len(ents) == 0 {
		t.Errorf("ReadDir of the root = %v, %v", ents, err)
	}
	// A link to it names it without opening it.
	if _, err := r.m.Transfer(ctxbg(), books, src, Path{Root: "books-library", Rel: "link.m4b"}, NewTransferOptions(Symlink, PermLibrary, VerifySize)); err != nil {
		t.Errorf("Symlink to a FIFO = %v", err)
	}
}

// As many FIFOs as there are transfer workers must not hold the pool: an
// ordinary copy queued behind them still runs.
func TestFIFOsDoNotWedgeTheTransferPool(t *testing.T) {
	names := make([]string, defaultWorkers)
	for i := range names {
		names[i] = fmt.Sprintf("f%d.m4b", i)
	}
	r, f := fifoRig(t, names...)
	f.inject = crossRoots
	mv := move()
	mv.MkdirAll = true
	for i, n := range names {
		go func() {
			_, _ = r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: n}, Path{Root: "books-library", Rel: fmt.Sprintf("y%d.m4b", i)}, mv)
		}()
	}
	time.Sleep(300 * time.Millisecond)
	ok := r.put("import-1", "ordinary.m4b", "bytes")
	ctx, cancel := context.WithTimeout(ctxbg(), 2*time.Second)
	defer cancel()
	if err := within(t, r, "an ordinary copy", func() error {
		_, err := r.m.Transfer(ctx, books, ok, Path{Root: "books-library", Rel: "ordinary.m4b"}, copyOpts())
		return err
	}); err != nil {
		t.Errorf("the transfer pool is wedged by %d FIFOs: %v", defaultWorkers, err)
	}
}

// A regular file that becomes a FIFO between the check and the open is not
// waited on: the open does not block (O_NONBLOCK), and the descriptor, once open,
// is what refuses it.
func TestAFileSwappedForAFifoJustBeforeTheOpenDoesNotBlock(t *testing.T) {
	var hook func(stage, name string)
	r, _ := transferRig(t, rigOpts{seams: seams{fsRace: func(stage, name string) {
		if hook != nil {
			hook(stage, name)
		}
	}}})
	r.put("import-1", "Arkham/clue.m4b", "a riddle")
	root, err := r.m.Root("import-1")
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := root.FS(books)
	if err != nil {
		t.Fatal(err)
	}
	abs := r.abs("import-1", "Arkham/clue.m4b")
	swapped := false
	hook = func(stage, name string) {
		if stage == "preopen" && name == "Arkham/clue.m4b" && !swapped {
			swapped = true
			if err := os.Remove(abs); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(abs, 0o644); err != nil {
				t.Error(err)
			}
		}
	}
	err = within(t, r, "Open", func() error {
		f, oerr := fsys.Open("Arkham/clue.m4b")
		if oerr == nil {
			return errors.Join(errors.New("Open of a FIFO succeeded"), f.Close())
		}
		return oerr
	})
	if !swapped {
		t.Fatal("the hook never ran")
	}
	if !errors.Is(err, ErrNotSupported) {
		t.Errorf("Open = %v, want ErrNotSupported", err)
	}
}
