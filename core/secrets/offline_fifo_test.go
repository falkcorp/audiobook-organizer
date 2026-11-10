// file: core/secrets/offline_fifo_test.go
// version: 1.0.0
// guid: 06c1c515-64cd-413b-af1b-e3a85acae94f
// last-edited: 2026-11-10

//go:build unix

package secrets

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestExportKeyring_RefusesFIFOTarget (a test name coined by F3-03): an --out
// that is a FIFO with mode 0600 passes the mode check, so only the
// regular-file check refuses it. Without that check the atomic rename would
// replace the FIFO with the export. (A symbolic link is refused by the mode
// check too, since Lstat reports the link's own mode, so the FIFO is the
// case that isolates the regular-file rule.)
func TestExportKeyring_RefusesFIFOTarget(t *testing.T) {
	e := newEnv(t)
	e.open(t, nil)
	fifo := filepath.Join(t.TempDir(), "batsignal")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := ExportKeyring(e.root, "", fifo, e.clock.now()); err == nil {
		t.Fatal("export-keyring replaced a FIFO")
	}
	fi, err := os.Lstat(fifo)
	if err != nil || fi.Mode()&fs.ModeNamedPipe == 0 {
		t.Fatalf("the FIFO is gone or replaced (%v, %v)", fi, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "secrets.keyring.exported")); !os.IsNotExist(err) {
		t.Error("a refused export wrote the record")
	}
}
