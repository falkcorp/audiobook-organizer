// file: core/internal/pebble/files.go
// version: 1.0.0
// guid: 3a692be2-5dec-4747-8fb2-6bbba06b9e72
// last-edited: 2026-11-07

package pebble

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// The sidecar suffixes beside the DB path (05-storage §5S.3.7).
const (
	// LevelsSuffix is the level sidecar: one line per owner, "<owner>
	// <level>", sorted; readable without opening Pebble by make rollback's
	// guard.
	LevelsSuffix = ".storage-levels"
	// CheckpointSuffix names the latest upgrade attempt's checkpoint
	// (§5S.3.12).
	CheckpointSuffix = ".migration-checkpoint"
)

// WriteFileAtomic writes data to path through fs: a temp file in the same
// directory, fsynced, renamed over path, then the directory fsynced, so a
// crash leaves the old file or the new one, never a torn one (05-storage
// §5S.3.7 "Level sidecar", §5S.3.12 "How"). Every durable file core storage
// writes goes through it, so R1-21's crash matrix has one seam to label.
func WriteFileAtomic(fs vfs.FS, path string, data []byte) error {
	dir := filepath.Dir(path)
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Errorf("pebble: temp name for %s: %w", path, err)
	}
	tmp := filepath.Join(dir, "."+filepath.Base(path)+".tmp-"+hex.EncodeToString(suffix[:]))
	f, err := fs.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return fmt.Errorf("pebble: create %s: %w", tmp, err)
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("pebble: write %s: %w", tmp, err)
	}
	if err := fs.Rename(tmp, path); err != nil {
		_ = fs.Remove(tmp)
		return fmt.Errorf("pebble: rename %s: %w", path, err)
	}
	return SyncDir(fs, dir)
}

// SyncDir fsyncs a directory so a create, rename or remove in it is durable.
func SyncDir(fs vfs.FS, dir string) error {
	d, err := fs.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("pebble: open directory %s: %w", dir, err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if err := errors.Join(serr, cerr); err != nil {
		return fmt.Errorf("pebble: fsync directory %s: %w", dir, err)
	}
	return nil
}

// ReadFile reads a whole file through fs; ok is false when it does not exist.
func ReadFile(fs vfs.FS, path string) (data []byte, ok bool, err error) {
	f, err := fs.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("pebble: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, false, fmt.Errorf("pebble: read %s: %w", path, err)
	}
	return b, true, nil
}

// ReadLevels reads the level sidecar of the store at dbPath. ok is false when
// it does not exist; an unreadable one is an error (the caller rewrites it
// from the stamps, with an error log, §5S.3.7).
func ReadLevels(fs vfs.FS, dbPath string) (levels map[string]int, ok bool, err error) {
	path := dbPath + LevelsSuffix
	b, ok, err := ReadFile(fs, path)
	if err != nil || !ok {
		return nil, ok, err
	}
	levels = make(map[string]int)
	sc := bufio.NewScanner(bytes.NewReader(b))
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		owner, value, found := strings.Cut(text, " ")
		if !found || owner == "" {
			return nil, true, fmt.Errorf("pebble: %s line %d: want \"<owner> <level>\", got %q", path, line, text)
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return nil, true, fmt.Errorf("pebble: %s line %d: level %q is not a non-negative integer", path, line, value)
		}
		if _, dup := levels[owner]; dup {
			return nil, true, fmt.Errorf("pebble: %s line %d: owner %q listed twice", path, line, owner)
		}
		levels[owner] = n
	}
	if err := sc.Err(); err != nil {
		return nil, true, fmt.Errorf("pebble: read %s: %w", path, err)
	}
	return levels, true, nil
}

// FormatLevels renders the level sidecar's content: "<owner> <level>" per
// line, sorted by owner.
func FormatLevels(levels map[string]int) []byte {
	owners := make([]string, 0, len(levels))
	for o := range levels {
		owners = append(owners, o)
	}
	slices.Sort(owners)
	var b bytes.Buffer
	for _, o := range owners {
		fmt.Fprintf(&b, "%s %d\n", o, levels[o])
	}
	return b.Bytes()
}

// WriteLevels writes the level sidecar atomically.
func WriteLevels(fs vfs.FS, dbPath string, levels map[string]int) error {
	return WriteFileAtomic(fs, dbPath+LevelsSuffix, FormatLevels(levels))
}

// ReadInt reads a one-integer sidecar (books' legacy projection sidecar
// <db>.storage-format, HEAD's format: one line holding one integer). ok is
// false when it does not exist.
func ReadInt(fs vfs.FS, path string) (n int, ok bool, err error) {
	b, ok, err := ReadFile(fs, path)
	if err != nil || !ok {
		return 0, ok, err
	}
	n, err = strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, true, fmt.Errorf("pebble: %s: %q is not an integer", path, strings.TrimSpace(string(b)))
	}
	return n, true, nil
}

// WriteInt writes a one-integer sidecar atomically.
func WriteInt(fs vfs.FS, path string, n int) error {
	return WriteFileAtomic(fs, path, []byte(strconv.Itoa(n)+"\n"))
}
