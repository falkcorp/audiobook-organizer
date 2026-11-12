// file: internal/books/cutover/config_strip.go
// version: 1.3.0
// guid: c7391f62-422d-4a79-8fa6-f679c490c72a
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// M2b (05-config §1.12.2): after M2a committed, fourteen of the fifteen keys
// HEAD's SaveConfigToFile managed in <db parent>/config.yaml are stripped from
// it, so the file no longer holds a stale copy of a secret M2a moved or a value
// R2 no longer reads. The fifteenth, database_path, stays (below). It runs on
// every boot until the file is clean, and is idempotent: a file with none of
// the keys is left alone, and one whose only keys are managed and strippable
// is deleted. Every other key stays.
//
// A strip is one-way, so it deletes a copy only after the original is shown
// to be somewhere else. Ten of the fifteen keys are settings HEAD wrote on a
// save and, through viper (cmd/root.go reads the file --config names, which
// docs/configuration.md points at the file beside the database), can read back
// at startup. For nine of them, on every install M2 runs on, the settings blob
// shadows the file, so stripping them loses nothing; an install with no blob is
// the one in which a file value is HEAD's effective setting, and M2a's
// Preflight refuses that upgrade (the census) instead of letting this strip
// delete it. database_path is the tenth and the exception: the blob cannot be
// where HEAD finds it (the database must be open before the blob can be read),
// and M2 never stores it, so nothing shadows the file's line and no destination
// can be shown to hold the value. The strip therefore always keeps it
// (headNeverStrippedKeys) and lists it in StripReport.Kept, until the operator
// layer reads core.storage.path from that source; F3-07 may then drop it from
// the list (seams.d/F3-06.md, F3-07 item 6). The census leaves it out for the
// same reason: M2 never stores it, so a file value is no reason to refuse. The
// other five
// are the ones HEAD's gap-fill reads (the three secrets, root_dir and
// enable_ai_parsing) and so the only ones whose value can exist nowhere but
// the file even with a blob. Each of those five that carries a usable value is
// stripped only when its destination already holds a value: M2a's import, or a
// value the name held before (a secret's unreadable marker is not a value). One
// whose destination holds nothing stays in the file and is reported by name
// (StripReport.Kept); the strip never imports, because it has no batch to
// import into and cannot quarantine, which only a migration chunk can. M2a is
// what imports a gap-fill value, and its Preflight refuses an upgrade in which
// no chunk would run.
//
// The write is HEAD's writeFileAtomic: a symlink is followed and its target
// replaced, the file keeps its mode and (where the process may) its owner, the
// new file is written beside the old one, synced, renamed over it and the
// directory synced, and a directory the process cannot write falls back to an
// in-place write (not atomic, and reported). A temp file an interrupted strip
// left is removed by the next run.
//
// It is not a data step: it touches a file, not the store, so the
// migration runner never sees it. F3-07 calls StripGapFill once
// ConfigCutoverDone says M2a committed (the gap-fill values it reads live
// in the file M2a is still reading until then), at every boot, and lists it
// in the boot-writer table. A strip that fails or refuses is not fatal to the
// boot; F3-07 raises a health warning from the error or the report's Kept and
// InPlace.

// FileOps is the filesystem calls StripGapFill and the config.yaml read
// make, so a test can fail each one in turn.
type FileOps interface {
	ReadFile(path string) ([]byte, error)
	// Resolve returns the file a write to path replaces: path itself, or the
	// target of a symlink there. A symlink whose target cannot be resolved is
	// an error.
	Resolve(path string) (string, error)
	// Stat returns the mode and owner of the file at path; ok is false when
	// there is none.
	Stat(path string) (meta FileMeta, ok bool, err error)
	// WriteTempFile creates a new file in dir named by pattern (os.CreateTemp's
	// rules), writes data, syncs it, gives it meta's mode and, where the
	// process may, owner, closes it, and returns its path.
	WriteTempFile(dir, pattern string, data []byte, meta FileMeta) (string, error)
	// WriteInPlace truncates and rewrites the existing file at path, keeping
	// its inode, mode and owner. It is not atomic.
	WriteInPlace(path string, data []byte) error
	Rename(oldpath, newpath string) error
	Remove(path string) error
	SyncDir(dir string) error
	// ReadDir lists the names in dir.
	ReadDir(dir string) ([]string, error)
}

// FileMeta is an existing file's mode and owner.
type FileMeta struct {
	Mode     fs.FileMode
	UID, GID int
	HasOwner bool
}

// osFiles is the OS filesystem.
type osFiles struct{}

func (osFiles) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (osFiles) Resolve(path string) (string, error) {
	li, err := os.Lstat(path)
	if err != nil || li.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	return filepath.EvalSymlinks(path)
}

func (osFiles) Stat(path string) (FileMeta, bool, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return FileMeta{}, false, nil
	case err != nil:
		return FileMeta{}, false, err
	}
	m := FileMeta{Mode: info.Mode().Perm()}
	m.UID, m.GID, m.HasOwner = fileOwner(info)
	return m, true, nil
}

func (osFiles) WriteTempFile(dir, pattern string, data []byte, meta FileMeta) (string, error) {
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
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Chmod(meta.Mode); err != nil {
		return fail(err)
	}
	if meta.HasOwner {
		keepOwner(f, meta.UID, meta.GID)
	}
	if err := f.Close(); err != nil {
		return "", errors.Join(err, os.Remove(name))
	}
	return name, nil
}

func (osFiles) WriteInPlace(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
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

func (osFiles) ReadDir(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names, nil
}

// keepOwner gives the temp file the original's owner and group so the rename
// does not change who owns config.yaml. It is skipped when they already match
// and when the process is not permitted (a non-root process cannot chown to
// another user), which is expected (HEAD: keepOwner).
func keepOwner(f *os.File, uid, gid int) {
	info, err := f.Stat()
	if err != nil {
		return
	}
	if curUID, curGID, ok := fileOwner(info); ok && curUID == uid && curGID == gid {
		return
	}
	_ = f.Chown(uid, gid)
}

// StripResult is what StripGapFill did.
type StripResult string

const (
	StripAbsent     StripResult = "absent"     // there is no file
	StripClean      StripResult = "clean"      // the file holds none of the keys it may strip
	StripStripped   StripResult = "stripped"   // keys were removed, the file rewritten
	StripDeleted    StripResult = "deleted"    // the keys were all it held, the file was removed
	StripUnparsable StripResult = "unparsable" // not YAML: HEAD ignored it too, and it is left alone
)

// StripReport is what one StripGapFill did and left. It carries names, never
// values.
type StripReport struct {
	Result StripResult
	// Kept names the keys left in the file because the file's copy is the only
	// one: the gap-fill keys whose destination holds no value, and database_path
	// whenever the file holds a value for it (M2 never stores it, so nothing can
	// be shown to hold it; headNeverStrippedKeys). Sorted.
	Kept []string
	// InPlace is true when the directory could not be written and the file
	// was rewritten in place (not atomic).
	InPlace bool
	// TempsRemoved counts temp files an interrupted strip had left.
	TempsRemoved int
}

// GapFillHolder reports whether the destination of one gap-fill key (a name
// from headGapFillKeys) already holds what the file would have supplied: a
// secret's name holds a readable value (an unreadable marker, or a record
// that does not decrypt, holds none), root_dir is in books' settings blob or
// core.library.roots lists a root, enable_ai_parsing's key is effectively
// true. NewGapFillHolder builds it from core's stores.
type GapFillHolder func(ctx context.Context, key string) (held bool, err error)

// headGapFillKeys are the five managed keys HEAD's gap-fill reads, in the
// order headParseGapFill reads them: the keys whose value can be only in the
// file.
var headGapFillKeys = []string{"openai_api_key", "google_books_api_key", "hardcover_api_token", "root_dir", "enable_ai_parsing"}

// headNeverStrippedKeys are managed keys the strip keeps whatever the holder
// says: M2 never stores them (the rename table's convert=never-stored), and no
// shadow of theirs exists, so no destination can be shown to hold the value.
// database_path is HEAD's database location: the database has to be open
// before the settings blob can be read, so the file (with the environment and
// the flag) is where HEAD finds it, and core.storage.path is read from the
// operator layer, not from this file. The list may shrink once F3-07 has wired
// core.storage.path from the same source.
var headNeverStrippedKeys = []string{"database_path"}

// fileHoldsValueFold reports whether m has a top-level key matching name
// case-insensitively (as viper reads it) with a value that is neither null nor
// an empty string: a null or empty line says nothing, so deleting it loses
// nothing.
func fileHoldsValueFold(m map[string]any, name string) bool {
	for k, v := range m {
		if !strings.EqualFold(k, name) {
			continue
		}
		if v == nil {
			continue
		}
		if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
			continue
		}
		return true
	}
	return false
}

// SecretMeta is the part of core's secrets store the holder reads
// (*secrets.Store).
type SecretMeta interface {
	Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error)
}

// NewGapFillHolder is the holder over core's stores: sec for the three
// secrets, legacy (books' legacy range) for root_dir while the settings blob
// still holds it, and effective for the config keys: the key's effective JSON
// value, its stored override or else its default. enable_ai_parsing is held
// when that is true and core.library.roots when it lists a root. It is the
// effective value, not "an override is stored", because the importer skips a
// value equal to its default (C4): HEAD's default for enable_ai_parsing is
// true, so a file's "true" is imported as no row at all, and testing for a row
// would keep that line in the file, and its retired-name warning, forever.
func NewGapFillHolder(sec SecretMeta, legacy storage.Reader, effective func(ctx context.Context, p config.Path) (json.RawMessage, error)) GapFillHolder {
	return func(ctx context.Context, key string) (bool, error) {
		row, ok := Table().Row(key)
		if !ok || len(row.Targets) == 0 {
			return false, fmt.Errorf("%w: %s is not a gap-fill key", errConfigStep, key)
		}
		path := row.Targets[0].Path
		switch key {
		case "openai_api_key", "google_books_api_key", "hardcover_api_token":
			m, err := sec.Meta(ctx, secrets.Name(path))
			if err != nil {
				return false, fmt.Errorf("%w: read %s: %v", errConfigStep, path, errClass(err))
			}
			// Set is true for an unreadable marker and for a record that fails to
			// decrypt (Err is ErrKeyUnavailable or ErrCorrupt): neither holds a
			// value, so a file value added later is kept and listed, not stripped.
			return m.Set && m.Err == nil, nil
		case "root_dir":
			if legacy != nil {
				raw, err := get(ctx, legacy, blobKey())
				if err != nil {
					return false, fmt.Errorf("%w: read the settings blob: %v", errConfigStep, errClass(err))
				}
				if root := blobRootDir(raw); root != "" {
					return true, nil
				}
			}
			// root_dir's target is an entry of a list-valued key: Path describes the
			// entry, List is the key.
			list := row.Targets[0].List
			v, err := effective(ctx, config.Path(list))
			if err != nil {
				return false, fmt.Errorf("%w: read %s: %v", errConfigStep, list, errClass(err))
			}
			var roots []json.RawMessage
			return json.Unmarshal(v, &roots) == nil && len(roots) > 0, nil
		default:
			v, err := effective(ctx, config.Path(path))
			if err != nil {
				return false, fmt.Errorf("%w: read %s: %v", errConfigStep, path, errClass(err))
			}
			var on bool
			return json.Unmarshal(v, &on) == nil && on, nil
		}
	}
}

// blobRootDir reads root_dir from a stored blob row without HEAD's transforms.
func blobRootDir(blob []byte) string {
	if blob == nil {
		return ""
	}
	return peekRootDir(blob, "")
}

// errMultiDocument is a config.yaml with more than one YAML document.
var errMultiDocument = fmt.Errorf("%w: config.yaml holds more than one YAML document; HEAD reads only the first, and a rewrite would drop the rest, so its managed keys were not stripped: merge the documents into one", errConfigStep)

// StripGapFill strips the managed keys from the file at path, except a
// gap-fill key whose destination holds nothing (holder; a nil holder keeps
// every gap-fill key). An error means the strip did not finish and the file is
// as it was (or, after the rename, already stripped): run it again. It never
// writes the file when nothing is to be stripped, and never reads or reports a
// value.
func StripGapFill(ctx context.Context, files FileOps, path string, holder GapFillHolder) (StripReport, error) {
	if files == nil {
		files = osFiles{}
	}
	data, err := files.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return StripReport{Result: StripAbsent}, nil
	case err != nil:
		return StripReport{}, fmt.Errorf("%w: read config.yaml: %v", errConfigStep, errClassFile(err))
	}
	// A symlink whose target does not exist reads as absent above (ReadFile
	// follows the link), so this branch is reached only when the link or its
	// target changes between the read and here: a race, not a state.
	target, err := files.Resolve(path)
	if err != nil {
		return StripReport{}, fmt.Errorf("%w: config.yaml is a symlink whose target cannot be resolved; left unchanged: %v", errConfigStep, errClassFile(err))
	}
	rep := StripReport{}
	dir := filepath.Dir(target)
	tempPrefix := "." + filepath.Base(target) + ".strip-"
	// A temp file an interrupted strip left holds what was about to replace
	// the file (the file less its managed keys): never a secret, but clutter.
	names, err := files.ReadDir(dir)
	if err != nil {
		return StripReport{}, fmt.Errorf("%w: list the config.yaml directory: %v", errConfigStep, errClassFile(err))
	}
	for _, n := range names {
		if strings.HasPrefix(n, tempPrefix) {
			if err := files.Remove(filepath.Join(dir, n)); err != nil {
				return StripReport{}, fmt.Errorf("%w: remove a temp file a strip left: %v", errConfigStep, errClassFile(err))
			}
			rep.TempsRemoved++
		}
	}

	var m map[string]any
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil && !errors.Is(err, io.EOF) {
		rep.Result = StripUnparsable
		return rep, nil
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return rep, errMultiDocument
	}

	// A gap-fill key HEAD would read a value from is kept unless its
	// destination holds one.
	_, present, _ := headParseGapFill(data)
	var keep []string
	for _, k := range headGapFillKeys {
		if !present[k] {
			continue
		}
		held := false
		if holder != nil {
			if held, err = holder(ctx, k); err != nil {
				return StripReport{}, err
			}
		}
		if !held {
			keep = append(keep, k)
		}
	}
	for _, k := range headNeverStrippedKeys {
		if fileHoldsValueFold(m, k) {
			keep = append(keep, k)
		}
	}
	slices.Sort(keep)
	rep.Kept = keep

	before := len(flatKeys(m))
	for _, k := range headManagedFileKeys {
		if slices.Contains(keep, k) {
			continue
		}
		headDeleteConfigKeyFold(m, k)
	}
	if len(flatKeys(m)) == before {
		rep.Result = StripClean
		return rep, nil
	}

	symlinked := target != path
	if len(m) == 0 && !symlinked {
		if err := files.Remove(path); err != nil {
			return StripReport{}, fmt.Errorf("%w: remove config.yaml: %v", errConfigStep, errClassFile(err))
		}
		if err := files.SyncDir(dir); err != nil {
			return StripReport{}, fmt.Errorf("%w: sync the config.yaml directory: %v", errConfigStep, errClassFile(err))
		}
		rep.Result = StripDeleted
		return rep, nil
	}
	// A symlink that would end up empty is rewritten as an empty document, not
	// removed: removing its target would leave the link dangling.
	out, err := yaml.Marshal(m)
	if err != nil {
		return StripReport{}, fmt.Errorf("%w: marshal config.yaml", errConfigStep)
	}
	meta, exists, err := files.Stat(target)
	if err != nil {
		return StripReport{}, fmt.Errorf("%w: stat config.yaml: %v", errConfigStep, errClassFile(err))
	}
	if !exists {
		meta = FileMeta{Mode: 0o600}
	}
	tmp, err := files.WriteTempFile(dir, tempPrefix+"*", out, meta)
	switch {
	case err == nil:
	case exists && errors.Is(err, fs.ErrPermission):
		if err := files.WriteInPlace(target, out); err != nil {
			return StripReport{}, fmt.Errorf("%w: write the stripped config.yaml in place: %v", errConfigStep, errClassFile(err))
		}
		rep.Result, rep.InPlace = StripStripped, true
		return rep, nil
	default:
		return StripReport{}, fmt.Errorf("%w: write the stripped config.yaml: %v", errConfigStep, errClassFile(err))
	}
	if err := files.Rename(tmp, target); err != nil {
		return StripReport{}, errors.Join(fmt.Errorf("%w: rename the stripped config.yaml: %v", errConfigStep, errClassFile(err)), files.Remove(tmp))
	}
	if err := files.SyncDir(dir); err != nil {
		return StripReport{}, fmt.Errorf("%w: sync the config.yaml directory: %v", errConfigStep, errClassFile(err))
	}
	rep.Result = StripStripped
	return rep, nil
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
	case errors.Is(err, fs.ErrNotExist):
		return "does not exist"
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
