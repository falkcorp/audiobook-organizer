// file: core/backup/journal/review_unix_test.go
// version: 1.0.0
// guid: 3c8e1f5a-92d7-4b06-8a14-e7b25d09c63f
// last-edited: 2026-11-11

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// unblock opens the write end of the pipe at path so a reader stuck in open(2)
// returns and its goroutine ends, whatever the test found.
func unblock(path string) {
	if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		_ = f.Close()
	}
}

// TestReadFile_NamedPipeIsRefusedWithoutBlocking: open(2) of a FIFO for reading
// waits for a writer, so a pipe planted at the journal's name would hang the
// boot before the regular-file check could refuse it. The read opens without
// blocking and refuses the pipe.
func TestReadFile_NamedPipeIsRefusedWithoutBlocking(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	if err := syscall.Mkfifo(fx.path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(fx.path, fx.keys)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile of a named pipe = %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		unblock(fx.path)
		t.Fatal("ReadFile of a named pipe blocks: the boot would hang on it")
	}
}

// TestOpenFile_NamedPipeIsRefusedWithoutBlocking: the same for the writer's
// open: a broken File that still takes a gap, and ErrNotRegular.
func TestOpenFile_NamedPipeIsRefusedWithoutBlocking(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	if err := syscall.Mkfifo(fx.path, 0o600); err != nil {
		t.Fatal(err)
	}
	type result struct {
		j   *File
		err error
	}
	done := make(chan result, 1)
	go func() {
		j, err := OpenFile(context.Background(), fx.opts())
		done <- result{j, err}
	}()
	select {
	case r := <-done:
		if !errors.Is(r.err, ErrNotRegular) || r.j == nil {
			t.Errorf("OpenFile of a named pipe = %v, %v, want a broken File and ErrNotRegular", r.j, r.err)
		}
		if r.j != nil {
			must(t, r.j.Close())
		}
	case <-time.After(5 * time.Second):
		unblock(fx.path)
		t.Fatal("OpenFile of a named pipe blocks")
	}
}
