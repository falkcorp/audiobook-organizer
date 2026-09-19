// file: internal/fingerprint/workerclient/probe_unix.go
// version: 1.0.0
// guid: 6fa959be-aea1-4221-842d-83a5bc2d5a72
// last-edited: 2026-09-19

//go:build unix

package workerclient

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// writeProbe is startup check 3 (design (d2)): creating a fresh dot-file in
// dir must fail with EROFS, EACCES or EPERM. It runs only after statfs has
// already reported the mount read-only, so on a correctly mounted root it
// never creates anything. If the create DOES succeed the file is removed and
// the root is refused as writable.
func writeProbe(dir string) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("write probe: %w", err)
	}
	p := filepath.Join(dir, ".fp-worker-probe-"+hex.EncodeToString(b[:]))
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_ = f.Close()
		_ = os.Remove(p)
		return fmt.Errorf("%w: %s accepted a write", ErrWritableMount, dir)
	}
	if errors.Is(err, syscall.EROFS) || errors.Is(err, fs.ErrPermission) {
		return nil
	}
	return fmt.Errorf("write probe on %s: unexpected error (want EROFS or EACCES): %w", dir, err)
}
