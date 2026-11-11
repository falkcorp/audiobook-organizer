// file: internal/books/cutover/config_strip.go
// version: 1.0.0
// guid: c7391f62-422d-4a79-8fa6-f679c490c72a
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// M2b (05-config §1.12.2): after M2a committed, the fifteen keys HEAD's
// SaveConfigToFile managed in <db parent>/config.yaml are stripped from it,
// so the file no longer holds a stale copy of a secret M2a moved or a value
// R2 no longer reads. It runs on every boot until the file is clean, and is
// idempotent: a file with none of the keys is left alone, and one whose keys
// are all managed is deleted. Every other key stays; the new file is written
// beside the old one, synced, renamed over it and the directory synced, mode
// 0600, so a crash leaves the old file or the new one, never a partial one.
//
// It is not a data step: it touches a file, not the store, so the
// migration runner never sees it. F3-07 calls StripGapFill once
// ConfigCutoverDone says M2a committed (the gap-fill values it reads live
// in the file M2a is still reading until then), at every boot, and lists it
// in the boot-writer table.

// FileOps is the filesystem calls StripGapFill and the config.yaml read
// make, so a test can fail each one in turn.
type FileOps interface {
	ReadFile(path string) ([]byte, error)
	// WriteTempFile creates a new file in dir named by pattern (os.CreateTemp's
	// rules), writes data, syncs it, closes it with mode, and returns its path.
	WriteTempFile(dir, pattern string, data []byte, mode fs.FileMode) (string, error)
	Rename(oldpath, newpath string) error
	Remove(path string) error
	SyncDir(dir string) error
}

// osFiles is the OS filesystem.
type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) WriteTempFile(dir, pattern string, data []byte, mode fs.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		return "", errors.Join(err, f.Close(), os.Remove(name))
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(err, os.Remove(name))
	}
	return name, nil
}

func (osFiles) Rename(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

func (osFiles) Remove(path string) error { return os.Remove(path) }

func (osFiles) SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// StripResult is what StripGapFill did.
type StripResult string

const (
	StripAbsent     StripResult = "absent"     // there is no file
	StripClean      StripResult = "clean"      // the file holds none of the keys
	StripStripped   StripResult = "stripped"   // the keys were removed, the file rewritten
	StripDeleted    StripResult = "deleted"    // the keys were all it held, the file was removed
	StripUnparsable StripResult = "unparsable" // not YAML: HEAD ignored it too, and it is left alone
)

// StripGapFill strips the fifteen managed keys from the file at path. An
// error means the strip did not finish and the file is as it was (or, after
// the rename, already stripped): run it again. It never writes the file
// when nothing is to be stripped, and never reads or reports a value.
func StripGapFill(files FileOps, path string) (StripResult, error) {
	if files == nil {
		files = osFiles{}
	}
	data, err := files.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return StripAbsent, nil
	case err != nil:
		return "", fmt.Errorf("%w: read config.yaml: %v", errConfigStep, errClassFile(err))
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return StripUnparsable, nil
	}
	before := len(flatKeys(m))
	for _, k := range headManagedFileKeys {
		headDeleteConfigKeyFold(m, k)
	}
	if len(flatKeys(m)) == before {
		return StripClean, nil
	}
	dir := filepath.Dir(path)
	if len(m) == 0 {
		if err := files.Remove(path); err != nil {
			return "", fmt.Errorf("%w: remove config.yaml: %v", errConfigStep, errClassFile(err))
		}
		if err := files.SyncDir(dir); err != nil {
			return "", fmt.Errorf("%w: sync the config.yaml directory: %v", errConfigStep, errClassFile(err))
		}
		return StripDeleted, nil
	}
	out, err := yaml.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("%w: marshal config.yaml", errConfigStep)
	}
	tmp, err := files.WriteTempFile(dir, ".config.yaml.strip-*", out, 0o600)
	if err != nil {
		return "", fmt.Errorf("%w: write the stripped config.yaml: %v", errConfigStep, errClassFile(err))
	}
	if err := files.Rename(tmp, path); err != nil {
		return "", errors.Join(fmt.Errorf("%w: rename the stripped config.yaml: %v", errConfigStep, errClassFile(err)), files.Remove(tmp))
	}
	if err := files.SyncDir(dir); err != nil {
		return "", fmt.Errorf("%w: sync the config.yaml directory: %v", errConfigStep, errClassFile(err))
	}
	return StripStripped, nil
}

// flatKeys counts the keys of a nested map (every key at every depth), so a
// removal inside a nested map shows.
func flatKeys(m map[string]any) []string {
	var out []string
	var walk func(prefix string, cur map[string]any)
	walk = func(prefix string, cur map[string]any) {
		for k, v := range cur {
			out = append(out, prefix+k)
			if child, ok := v.(map[string]any); ok {
				walk(prefix+k+".", child)
			}
		}
	}
	walk("", m)
	return out
}

// errClassFile names a file error's class without its text (a path can carry
// a directory name).
func errClassFile(err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrExist):
		return "already exists"
	}
	return "file operation failed"
}

// ConfigCutoverDone reports whether M2a committed: the data migration's
// record in storage's keyspace is done. sks is the c/storage keyspace
// (storage.DB.Keyspace("core/storage")).
func ConfigCutoverDone(ctx context.Context, sks storage.Reader) (bool, error) {
	raw, err := sks.Get(ctx, storage.DataMigrationKey(ConfigCutoverID))
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rec, err := storage.DataMigrationCodec().Decode(raw)
	if err != nil {
		return false, err
	}
	return rec.State == datamigrate.StateDone, nil
}
