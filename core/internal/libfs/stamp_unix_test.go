// file: core/internal/libfs/stamp_unix_test.go
// version: 1.0.0
// guid: e5a8e95b-7e37-469a-ac7a-892474dd1cb1
// last-edited: 2026-11-12

//go:build unix

package libfs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// tick is longer than the timestamp granularity of the filesystems these tests
// run on: a change made one tick later has a later change time.
const tick = 25 * time.Millisecond

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func factsOfPath(t *testing.T, path string) FileFacts {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	facts, err := FactsOf(f)
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

// The facts of a regular file are the ones the kernel reports for its inode.
func TestFactsOfARegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Joker.m4b")
	writeFile(t, path, "ha ha ha")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)

	got := factsOfPath(t, path)
	if !got.Regular {
		t.Fatal("a regular file reported as another kind")
	}
	if got.Size != 8 || got.MTime != st.ModTime().UnixNano() {
		t.Errorf("size %d, mtime %d; want 8 and %d", got.Size, got.MTime, st.ModTime().UnixNano())
	}
	if got.Dev != uint64(sys.Dev) || got.Ino != uint64(sys.Ino) {
		t.Errorf("device %d inode %d; want %d and %d", got.Dev, got.Ino, uint64(sys.Dev), uint64(sys.Ino))
	}
	if got.CTime == 0 {
		t.Error("no inode change time on a unix platform")
	}
	if got != factsOfPath(t, path) {
		t.Error("two readings of an untouched file differ")
	}
}

// The inode change time moves on a change that touches neither the bytes nor
// the modification time, and no system call can put it back.
func TestFactsOfChangeTimeMovesWithoutAWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Joker.m4b")
	writeFile(t, path, "ha ha ha")
	before := factsOfPath(t, path)

	for name, touch := range map[string]func() error{
		"chmod":     func() error { return os.Chmod(path, 0o600) },
		"hard link": func() error { return os.Link(path, filepath.Join(dir, "Joker.also")) },
	} {
		time.Sleep(tick)
		if err := touch(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after := factsOfPath(t, path)
		if after.MTime != before.MTime || after.Ino != before.Ino || after.Size != before.Size {
			t.Errorf("%s moved a fact it should not: %+v -> %+v", name, before, after)
		}
		if after.CTime <= before.CTime {
			t.Errorf("%s left the change time at %d (was %d)", name, after.CTime, before.CTime)
		}
		before = after
	}
}

// Only a regular file has facts to compare: a FIFO opened without blocking, a
// directory and the like come back as not regular and without an error, and a
// FIFO is never asked for a generation (an ioctl on a device can act).
func TestFactsOfOtherKindsAreNotRegular(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}
	if got := factsOfPath(t, fifo); got.Regular || got.HasGen || got.Birth != 0 {
		t.Errorf("a FIFO has facts %+v", got)
	}
	if got := factsOfPath(t, dir); got.Regular || got.HasGen {
		t.Errorf("a directory has facts %+v", got)
	}
}

// A filesystem that frees an inode number and hands it to the next file makes
// two files with the same device and inode number. Whatever the platform
// reports must separate them: the generation inside one clock tick, the change
// time and creation time a tick later.
func TestFactsOfTellARecycledInodeApart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Joker.m4b")
	var first, second FileFacts
	recycled := false
	for range 50 {
		writeFile(t, path, "ha ha ha")
		first = factsOfPath(t, path)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "ha ha ha")
		second = factsOfPath(t, path)
		if second.Ino == first.Ino {
			recycled = true
			break
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if !recycled {
		t.Skip("this filesystem did not reuse an inode number")
	}
	if first.HasGen && second.HasGen && first.Gen == second.Gen {
		t.Errorf("a recycled inode kept its generation %d", first.Gen)
	}
	// A tick later every platform that reports a change time tells them apart,
	// with or without a generation.
	time.Sleep(tick)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "ha ha ha")
	third := factsOfPath(t, path)
	if third.Ino == second.Ino && third.CTime == second.CTime && third.Birth == second.Birth && third.Gen == second.Gen {
		t.Errorf("an inode recreated a tick later is indistinguishable: %+v and %+v", second, third)
	}
}
