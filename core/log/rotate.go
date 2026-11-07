// file: core/log/rotate.go
// version: 1.0.0
// guid: 69ffdce2-9713-4cde-a0a7-376e9bc42449
// last-edited: 2026-11-07

package log

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rotateRetry is how long a RotatingFile waits after a failed rotation before
// it tries again, and how often it reports a rotation failure on stderr.
const rotateRetry = time.Minute

// RotatingFile is the built-in size-based rotator for output=file (05-logging
// §3; no third-party dependency). The live file is name; rotated files are
// name.1 (newest) to name.<maxFiles-1> (oldest), so at most maxFiles files
// exist, the live one included. Every open, rename and remove goes through the
// *os.Root, so nothing outside it is touched. Write and rotation are
// serialised by one mutex (05-logging §7). Safe for concurrent use.
type RotatingFile struct {
	mu       sync.Mutex
	root     *os.Root
	name     string // slash-separated, local to root
	maxBytes int64
	maxFiles int
	f        *os.File // nil after a failed reopen; Write retries
	size     int64
	closed   bool
	retryAt  time.Time // no rotation attempt before this, after a failure
	lastWarn time.Time
	now      func() time.Time
	stderr   io.Writer
}

// rotatingFileFlags opens the live file for appending, creating it.
const rotatingFileFlags = os.O_WRONLY | os.O_CREATE | os.O_APPEND

// OpenRotatingFile opens (creating it and its parent directories when
// missing) the live file name under root, appending. name must be a local,
// slash-separated path: an absolute path or a ".." element is refused, and
// os.Root refuses any symlink that leaves root. maxBytes and maxFiles must be
// at least 1. A failure is returned; the kernel turns it into a boot error
// (05-logging §6).
func OpenRotatingFile(root *os.Root, name string, maxBytes int64, maxFiles int) (*RotatingFile, error) {
	if root == nil {
		return nil, errors.New("log: OpenRotatingFile: nil *os.Root")
	}
	if maxBytes < 1 || maxFiles < 1 {
		return nil, fmt.Errorf("log: OpenRotatingFile: maxBytes (%d) and maxFiles (%d) must be at least 1", maxBytes, maxFiles)
	}
	if err := checkLocalName(name); err != nil {
		return nil, err
	}
	f := &RotatingFile{
		root:     root,
		name:     name,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		now:      time.Now,
		stderr:   os.Stderr,
	}
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("log: OpenRotatingFile: create %q: %w", dir, err)
		}
	}
	if err := f.open(); err != nil {
		return nil, fmt.Errorf("log: OpenRotatingFile: %w", err)
	}
	return f, nil
}

// checkLocalName refuses an empty, absolute or ".."-bearing name.
func checkLocalName(name string) error {
	if name == "" || path.IsAbs(name) || filepath.IsAbs(name) {
		return fmt.Errorf("log: OpenRotatingFile: %q is not a local path", name)
	}
	if slices.Contains(strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }), "..") {
		return fmt.Errorf("log: OpenRotatingFile: %q has a \"..\" element", name)
	}
	return nil
}

// open opens the live file and reads its size. f.mu must be held (or f not
// yet shared).
func (f *RotatingFile) open() error {
	fh, err := f.root.OpenFile(f.name, rotatingFileFlags, 0o640)
	if err != nil {
		return err
	}
	st, err := fh.Stat()
	if err != nil {
		return errors.Join(err, fh.Close())
	}
	f.f, f.size = fh, st.Size()
	return nil
}

// Write appends p to the live file, rotating first when p would take a
// non-empty file past maxBytes. A rotation failure keeps writing to the
// current file and warns on stderr at most once a minute (05-logging §6); a
// failed attempt is retried after a minute. p is never split across files.
func (f *RotatingFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.f == nil {
		if err := f.open(); err != nil {
			return 0, err
		}
	}
	if f.size > 0 && f.size+int64(len(p)) > f.maxBytes && !f.now().Before(f.retryAt) {
		if err := f.rotate(); err != nil {
			f.retryAt = f.now().Add(rotateRetry)
			f.warn(err)
			if f.f == nil {
				return 0, err
			}
		}
	}
	n, err := f.f.Write(p)
	f.size += int64(n)
	return n, err
}

// rotate closes the live file, shifts name.i to name.i+1 (removing the
// oldest and any file beyond maxFiles-1 left by a larger earlier setting),
// renames the live file to name.1 and opens a new live file. It closes before
// renaming so that it works where an open file cannot be renamed. Whatever
// step fails, the live name is reopened for appending, so writing goes on to
// the current file when the shift did not happen.
func (f *RotatingFile) rotate() error {
	var errs []error
	if err := f.f.Close(); err != nil {
		errs = append(errs, err)
	}
	f.f = nil
	if err := f.shift(); err != nil {
		errs = append(errs, err)
	}
	if err := f.open(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// shift makes room for the live file at name.1.
func (f *RotatingFile) shift() error {
	if f.maxFiles == 1 {
		if err := f.root.Remove(f.name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := f.removeBeyond(f.maxFiles - 1); err != nil {
		return err
	}
	for i := f.maxFiles - 2; i >= 1; i-- {
		from := f.name + "." + strconv.Itoa(i)
		if err := f.root.Rename(from, f.name+"."+strconv.Itoa(i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return f.root.Rename(f.name, f.name+".1")
}

// removeBeyond removes every rotated file name.N with N >= from.
func (f *RotatingFile) removeBeyond(from int) error {
	dir, base := path.Split(f.name)
	if dir == "" {
		dir = "."
	}
	entries, err := fs.ReadDir(f.root.FS(), path.Clean(dir))
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		suffix, ok := strings.CutPrefix(e.Name(), base+".")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(suffix)
		if err != nil || n < from || strconv.Itoa(n) != suffix {
			continue
		}
		if err := f.root.Remove(path.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// warn reports a rotation failure on stderr at most once per rotateRetry.
func (f *RotatingFile) warn(err error) {
	now := f.now()
	if !f.lastWarn.IsZero() && now.Sub(f.lastWarn) < rotateRetry {
		return
	}
	// The minute's budget is spent only when the line reached stderr, so a
	// failed report is retried on the next failure.
	if _, werr := fmt.Fprintf(f.stderr, "mediacore/log: rotating %q failed, still writing the current file: %q\n", f.name, err.Error()); werr == nil {
		f.lastWarn = now
	}
}

// Close closes the live file. Later writes return os.ErrClosed; a second
// Close returns nil.
func (f *RotatingFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if f.f == nil {
		return nil
	}
	err := f.f.Close()
	f.f = nil
	return err
}
