// file: internal/organizer/copyfile_race_test.go
// version: 1.2.0
// guid: 3f9c2a7e-6b41-4d58-9e02-7c1a5d8f4b36
// last-edited: 2026-09-02

package organizer

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Two organize workers targeting the SAME destination (two same-titled books,
// or one book planned twice) must never share a temp file. Before this test
// copyFile wrote every destination through `dst+".tmp"` with O_TRUNC and
// os.Remove'd it first: in 30 of 30 probe iterations one worker returned
// success while dst held the other worker's bytes, or an interleaving of both,
// at full length — so verifyRenamed's size check passed over corrupt audio.
//
// The contract: exactly one writer wins, dst is byte-identical to the WINNER's
// source, the loser gets an error os.IsExist recognises (the callers' race
// recovery branches on it), and no temp file is left behind.
func TestCopyFile_ConcurrentWritersNeverShareATemp(t *testing.T) {
	o := &Organizer{config: &config.Config{}}
	const size = 1 << 20
	a := bytes.Repeat([]byte{'A'}, size)
	b := bytes.Repeat([]byte{'B'}, size)

	for iter := range 40 {
		dir := t.TempDir()
		srcA := filepath.Join(dir, "a.src")
		srcB := filepath.Join(dir, "b.src")
		dst := filepath.Join(dir, "Book.m4b")
		if err := os.WriteFile(srcA, a, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(srcB, b, 0o644); err != nil {
			t.Fatal(err)
		}

		var start sync.WaitGroup
		start.Add(1)
		var done sync.WaitGroup
		errs := make([]error, 2)
		for i, src := range []string{srcA, srcB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = o.copyFile(src, dst)
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for i, err := range errs {
			switch {
			case err == nil:
				wins++
			case os.IsExist(err) || errors.Is(err, fs.ErrExist):
			default:
				t.Fatalf("iter %d writer %d: unexpected error %v", iter, i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("iter %d: %d writers reported success, want exactly 1 (errs=%v)", iter, wins, errs)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		var want []byte
		if errs[0] == nil {
			want = a
		} else {
			want = b
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("iter %d: dst is not the winner's bytes (len=%d first=%q last=%q)", iter, len(got), got[0], got[len(got)-1])
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), tempFileSuffix) {
				t.Fatalf("iter %d: temp file left behind: %s", iter, e.Name())
			}
		}
	}
}

// The temp name must still be something cleanupTempFiles recognises, or a
// crash between copy and finalize leaves an unswept multi-GB file forever.
func TestCopyFile_TempNameIsSweptByCleanup(t *testing.T) {
	dir := t.TempDir()
	o := &Organizer{config: &config.Config{RootDir: dir}}
	tmp := o.tempPathFor(filepath.Join(dir, "Book.m4b"))
	if !strings.HasPrefix(tmp, filepath.Join(dir, "Book.m4b")) || !strings.HasSuffix(tmp, tempFileSuffix) {
		t.Fatalf("temp name %q must sit beside dst and end in %q", tmp, tempFileSuffix)
	}
	if o.tempPathFor(filepath.Join(dir, "Book.m4b")) == tmp {
		t.Fatalf("two temp names for the same dst must differ (got %q twice)", tmp)
	}
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := o.cleanupTempFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("cleanupTempFiles did not sweep %s: %v", tmp, err)
	}
}

// A destination that already exists is refused, never replaced — and the
// refusal is the os.IsExist shape the organize loop's recovery branch expects.
func TestCopyFile_RefusesToReplaceAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	o := &Organizer{config: &config.Config{}}
	src := filepath.Join(dir, "src.m4b")
	dst := filepath.Join(dir, "Book.m4b")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("other book"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := o.copyFile(src, dst)
	if !os.IsExist(err) {
		t.Fatalf("want an os.IsExist error, got %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "other book" {
		t.Fatalf("existing destination was replaced: %q", got)
	}
}

// The second defence, isolated. With the nonce pinned, two writers ARE handed
// the same temp name — the pre-fix situation exactly — and only O_EXCL stands
// between them. One must fail at open, the winner's bytes must reach dst
// intact, and the loser must NOT remove the temp it did not create (that would
// abort the winner's in-flight copy).
//
// The loser's error is deliberately NOT an exists-error. An fs.ErrExist from
// copyFile means "the DESTINATION is taken" and the organize loop's recovery
// branch answers it by checking whether the file there is this book's and
// adopting it. A temp-name collision says nothing about the destination — the
// winner may still be mid-copy — so it must not trigger that adoption.
func TestCopyFile_PinnedNonce_OnlyOneWriterOpensTheTemp(t *testing.T) {
	prev := tempNonce
	tempNonce = func() string { return "pinned" }
	t.Cleanup(func() { tempNonce = prev })

	o := &Organizer{config: &config.Config{}}
	const size = 1 << 20
	a := bytes.Repeat([]byte{'A'}, size)
	b := bytes.Repeat([]byte{'B'}, size)

	for iter := range 20 {
		dir := t.TempDir()
		srcA := filepath.Join(dir, "a.src")
		srcB := filepath.Join(dir, "b.src")
		dst := filepath.Join(dir, "Book.m4b")
		if err := os.WriteFile(srcA, a, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(srcB, b, 0644); err != nil {
			t.Fatal(err)
		}

		var start, done sync.WaitGroup
		start.Add(1)
		errs := make([]error, 2)
		for i, src := range []string{srcA, srcB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = o.copyFile(src, dst)
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		var winner []byte
		for i, err := range errs {
			if err == nil {
				wins++
				winner = [][]byte{a, b}[i]
				continue
			}
			if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
				t.Fatalf("iter %d: a temp-name collision must not look like a taken destination, got %v", iter, err)
			}
			if !strings.Contains(err.Error(), "nonce collision") {
				t.Fatalf("iter %d: loser error must name the nonce collision, got %v", iter, err)
			}
		}
		if wins != 1 {
			t.Fatalf("iter %d: expected exactly one winner, got %d (%v)", iter, wins, errs)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("iter %d: %v", iter, err)
		}
		if !bytes.Equal(got, winner) {
			t.Fatalf("iter %d: destination does not hold the winner's bytes intact (len=%d)", iter, len(got))
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), tempFileSuffix) {
				t.Fatalf("iter %d: temp left behind: %s", iter, e.Name())
			}
		}
	}
}
