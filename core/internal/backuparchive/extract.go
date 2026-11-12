// file: core/internal/backuparchive/extract.go
// version: 1.0.0
// guid: 9772cca4-e04e-4fb0-b01e-8201936fa4f0
// last-edited: 2026-11-12

package backuparchive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Expect is what the manifest recorded for one entry.
type Expect struct {
	Size   int64
	SHA256 string // lower-case hex
}

// Written is one file an Extractor wrote.
type Written struct {
	Name   string
	Size   int64
	SHA256 string
}

// Extractor writes archive entries into one directory the caller made, and
// nowhere else. The directory is opened as an os.Root, so no name, however
// built, can leave it through a `..` or a symlink; every file is created with
// O_CREATE|O_EXCL and mode 0600 and every directory 0700, from the start (never
// widened and narrowed after); each file is fsynced when it is written and
// every directory by Sync. A name that already exists is an error, never an
// overwrite.
type Extractor struct {
	dst  string
	root *os.Root
	dirs map[string]struct{}
}

// NewExtractor opens dst, which must exist and be a directory (not a symlink to
// one).
func NewExtractor(dst string) (*Extractor, error) {
	if !filepath.IsAbs(dst) {
		return nil, fmt.Errorf("backuparchive: extraction directory %q is not absolute", dst)
	}
	lst, err := os.Lstat(dst)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() {
		return nil, fmt.Errorf("backuparchive: extraction directory %s is %s, not a directory", dst, lst.Mode().Type())
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return nil, err
	}
	return &Extractor{dst: dst, root: root, dirs: map[string]struct{}{}}, nil
}

// WriteFile writes the entry name (already validated by Reader or CleanName)
// from r. With want set, the bytes read must be exactly want.Size and hash to
// want.SHA256, else the file is removed and ErrEntryMismatch returned: an
// entry the manifest does not describe is refused by the caller passing nil
// only for entries it has decided not to check.
func (e *Extractor) WriteFile(name string, r io.Reader, want *Expect) (Written, error) {
	if _, err := CleanName(name, false); err != nil {
		return Written{}, err
	}
	if dir := path.Dir(name); dir != "." {
		if err := e.root.MkdirAll(dir, 0o700); err != nil {
			return Written{}, fmt.Errorf("backuparchive: create %s: %w", dir, err)
		}
		for d := dir; d != "."; d = path.Dir(d) {
			e.dirs[d] = struct{}{}
		}
	}
	f, err := e.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Written{}, fmt.Errorf("backuparchive: create %s: %w", name, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r)
	w := Written{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil && want != nil && (n != want.Size || !strings.EqualFold(w.SHA256, want.SHA256)) {
		err = refuse(ErrEntryMismatch, name, "size %d or digest differs from the manifest's", n)
	}
	if err != nil {
		return w, errors.Join(err, e.root.Remove(name))
	}
	return w, nil
}

// Mkdir creates the directory name (0700), as a legacy archive's directory
// entry asks, so an empty directory of the source exists in the copy.
func (e *Extractor) Mkdir(name string) error {
	if _, err := CleanName(name, true); err != nil {
		return err
	}
	if err := e.root.MkdirAll(name, 0o700); err != nil {
		return fmt.Errorf("backuparchive: create %s: %w", name, err)
	}
	for d := name; d != "."; d = path.Dir(d) {
		e.dirs[d] = struct{}{}
	}
	return nil
}

// Sync fsyncs every directory the extractor made, deepest first, then the
// extraction directory itself, so the names written survive a crash (the swap
// renames this tree into place).
func (e *Extractor) Sync() error {
	names := make([]string, 0, len(e.dirs))
	for d := range e.dirs {
		names = append(names, d)
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, d := range names {
		f, err := e.root.Open(d)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return fmt.Errorf("backuparchive: sync %s: %w", d, err)
		}
	}
	f, err := os.Open(e.dst)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// Close releases the root.
func (e *Extractor) Close() error { return e.root.Close() }
