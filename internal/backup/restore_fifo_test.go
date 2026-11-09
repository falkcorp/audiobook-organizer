// file: internal/backup/restore_fifo_test.go
// version: 1.0.0
// guid: 56e84662-0266-4b84-ac74-9675d55cc28d
// last-edited: 2026-11-08

//go:build !windows

package backup

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fifoBody is larger than any pipe buffer, so the extraction's write to a
// FIFO blocks until the reader drains it.
var fifoBody = strings.Repeat("joker", 1<<18)

// pauseAtFIFO makes path a named pipe and returns a wait function. When the
// extraction opens the pipe to write an entry into it, during runs while the
// extraction is held inside that entry: it cannot write past the pipe buffer
// until during returns and the pipe is drained. wait reports whether the
// extraction reached the pipe at all.
func pauseAtFIFO(t *testing.T, path string, during func()) (wait func() bool) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		f, err := os.Open(path) // blocks until a writer opens the pipe
		if err != nil {
			done <- false
			return
		}
		during()
		_, _ = io.Copy(io.Discard, f)
		_ = f.Close()
		done <- true
	}()
	return func() bool {
		select {
		case ok := <-done:
			return ok
		case <-time.After(10 * time.Second):
			// The extraction never opened the pipe; release the reader.
			if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
			return false
		}
	}
}

// TestExtractArchive_ReadsTheSnapshotDirsAtEachEntry: the snapshot
// directories are looked up again for every entry. A directory that did not
// exist when the extraction began (the server's periodic HNSW export can
// make one) has no identity until it is made, and a symlink planted to it
// resolves into it from then on.
func TestExtractArchive_ReadsTheSnapshotDirsAtEachEntry(t *testing.T) {
	base := t.TempDir()
	target, live := filepath.Join(base, "gotham"), filepath.Join(base, "wayne-manor")
	snap := filepath.Join(live, "hnsw")
	mustMkdir(t, target, live)
	mustSymlink(t, snap, filepath.Join(target, "lnk"))
	wait := pauseAtFIFO(t, filepath.Join(target, "pipe"), func() { mustMkdir(t, snap) })

	archive := filepath.Join(base, "knightfall.tar")
	writePlainTar(t, archive, [][2]string{
		{"pipe", fifoBody},
		{"lnk/hnsw.clean-stop", "a marker"},
	})
	err := extractArchive(archive, target, []string{snap})
	if !wait() {
		t.Fatal("the extraction never reached the pipe")
	}
	if !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
		t.Fatalf("extractArchive = %v; want ErrArchiveCarriesHNSWSnapshot", err)
	}
	if _, err := os.Lstat(filepath.Join(snap, "hnsw.clean-stop")); !os.IsNotExist(err) {
		t.Errorf("the marker was written into the snapshot directory made during the extraction (%v)", err)
	}
}
