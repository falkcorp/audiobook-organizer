// file: core/storage/derived.go
// version: 1.0.1
// guid: 6162657c-5114-44d6-a4bc-75bc350008ed
// last-edited: 2026-11-11

package storage

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Derived indexes and storage generations (05-storage §5S.3.9).
//
// A derived index is anything rebuilt from storage (in-memory tables, ANN
// graphs, a full-text index, caches with an on-disk warm-start snapshot). What
// is generic about them is the check that a warm-start artifact still
// describes storage, and core owns that check.
//
// # Why a generation and not a count
//
// A truth count passes when an add and a delete cancel, and an artifact taken
// after a checkpoint can hold documents the checkpoint lacks. The generation
// key c/storage/gen/<module>/<name> is blind-written with a fresh, unique
// Sequence value by every batch that touches the index's source, so after an
// artifact records value X any later committed touch leaves a value other than
// X: an artifact can be falsely rejected, never falsely accepted, provided
// every write to the source touched the key.
//
// # The store generation rides inside the token
//
// A Sequence is per store. A restore, a relocation or a swap brings back an
// older copy of the store whose sequence high-water mark, and so the next
// Touch value, can equal a value an artifact already recorded: the same
// sequence value then names two different states. The store generation
// (c/storage/epoch, §5S.3.7) is the one thing that differs across those
// copies, so Current returns epoch || sequence in the 16 bytes of a
// Generation. An artifact from another store generation therefore never equals
// the current token, whatever Load or the caller compares, and the kernel's own
// store-generation comparison (§5S.3.9 "When the hooks run") is a second
// check, not the only one.

// Generation is an opaque token that changes in every batch that touches a
// derived index's source: the store generation (8 bytes, big endian) followed
// by the last Sequence value a Touch wrote (8 bytes, big endian; 0 = never
// touched). Compare it with ==; treat the bytes as opaque.
type Generation [16]byte

// IsZero reports the zero token. No store has generation 0 (the first is 1),
// so a zero token is an unstamped or corrupt artifact and is never accepted.
func (g Generation) IsZero() bool { return g == Generation{} }

// String is the token as 32 lower-case hex digits, the form an artifact file
// records.
func (g Generation) String() string { return hex.EncodeToString(g[:]) }

// ParseGeneration is String's inverse.
func ParseGeneration(s string) (Generation, error) {
	var g Generation
	raw, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return g, fmt.Errorf("storage: generation %q: %w", s, err)
	}
	if len(raw) != len(g) {
		return g, fmt.Errorf("storage: generation %q is %d bytes, want %d", s, len(raw), len(g))
	}
	copy(g[:], raw)
	return g, nil
}

// ErrDerivedUndeclared is DB.Derived's refusal of an index its module did not
// declare in Spec.Derived.
var ErrDerivedUndeclared = errors.New("storage: derived index not declared in its owner's Spec.Derived")

// ErrNoStoreGeneration is Derived.Current's refusal when the store has no
// store-generation record (c/storage/epoch), which a prepared store always
// has: a generation that cannot name its store is not served.
var ErrNoStoreGeneration = errors.New("storage: the store has no store generation record")

// genSequence is the name of the one Sequence every Touch draws from, on
// storage's own keyspace c/storage/. One shared sequence makes every touch
// value unique across all derived indexes of the store.
const genSequence = "gen"

// Derived is the per-index handle (module.StorageHandles.Derived[name]); it
// carries write access to exactly one core key, c/storage/gen/<module>/<name>.
// The zero Derived is invalid: Touch and Current return ErrInvalidKeyspace.
type Derived struct {
	ks  Keyspace // storage's own keyspace; never exposed
	key []byte   // relative: gen/<module>/<name>
	seq *Sequence
}

// Derived issues module's handle for the derived index name, for the kernel.
// The index must be declared in the module's Spec.Derived, so the handle that
// writes c/storage/gen/<module>/<name> exists exactly for the indexes the
// claim ledger and readiness gating know (§5S.3.9).
func (db *DB) Derived(module Owner, name string) (Derived, error) {
	info := db.owners[module]
	if info == nil || info.core {
		return Derived{}, fmt.Errorf("storage: derived index %q: owner %q is not a registered module: %w", name, module, ErrDerivedUndeclared)
	}
	declared := false
	for _, d := range info.spec.Derived {
		if d.Name == name {
			declared = true
			break
		}
	}
	if !declared {
		return Derived{}, fmt.Errorf("storage: derived index %q of module %q: %w", name, module, ErrDerivedUndeclared)
	}
	if err := keys.ValidName(name); err != nil {
		return Derived{}, fmt.Errorf("storage: derived index name: %w", err)
	}
	ks := db.storageKS()
	return Derived{
		ks:  ks,
		key: genKey(module, name),
		seq: ks.Sequence(genSequence),
	}, nil
}

// genKey is gen/<module>/<name> relative to c/storage/.
func genKey(module Owner, name string) []byte {
	return keys.Join(keys.Str("gen"), keys.Str(string(module)), keys.Str(name))
}

// Touch blind-writes a fresh Sequence value into the generation key in b. Every
// batch that changes the index's source must Touch (index.MarkDirty does it for
// full-text indexes). The value is allocated from a durable lease before
// anything is staged, so a refill commits on its own first and a crash can only
// skip values, never reissue one.
func (d Derived) Touch(ctx context.Context, b *Batch) error {
	if d.seq == nil {
		return ErrInvalidKeyspace
	}
	if b == nil {
		return errors.New("storage: Derived.Touch with a nil batch")
	}
	n, err := d.seq.NextIn(ctx, b)
	if err != nil {
		return fmt.Errorf("storage: derived generation: %w", err)
	}
	b.Bind(d.ks)
	return b.In(d.ks).Set(d.key, binary.BigEndian.AppendUint64(nil, n))
}

// Current is the index's token now: the store generation and the last touch
// value committed (0 when the index was never touched). It reads committed
// state only, so a Touch in a batch that has not committed is not visible.
func (d Derived) Current(ctx context.Context) (Generation, error) {
	var g Generation
	if d.seq == nil {
		return g, ErrInvalidKeyspace
	}
	raw, err := d.ks.Get(ctx, epochKey())
	switch {
	case errors.Is(err, ErrNotFound):
		return g, ErrNoStoreGeneration
	case err != nil:
		return g, err
	}
	rec, err := codec.JSON[epochRecord](1).Decode(raw)
	if err != nil {
		return g, fmt.Errorf("storage: store generation unreadable: %w", err)
	}
	if rec.Epoch == 0 {
		return g, ErrNoStoreGeneration
	}
	var last uint64
	switch raw, err := d.ks.Get(ctx, d.key); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return g, err
	case len(raw) != 8:
		return g, fmt.Errorf("storage: derived generation key holds %d bytes, want 8", len(raw))
	default:
		last = binary.BigEndian.Uint64(raw)
	}
	binary.BigEndian.PutUint64(g[:8], rec.Epoch)
	binary.BigEndian.PutUint64(g[8:], last)
	return g, nil
}

// DerivedIndex is the DECLARATIVE part of anything rebuilt from storage
// (in-memory tables, ANN graphs, caches with an on-disk warm-start snapshot).
// It is manifest data (Spec.Derived), so the claim ledger and readiness gating
// know the index while its module is disabled.
type DerivedIndex struct {
	Name     string // module-relative: "memdb", "hnsw" (COR3-7)
	Version  string // a change forces a rebuild
	Blocking bool   // true: readiness waits for it (05-health)
}

// Validate reports a declaration no index can be built from: a Name that is
// not a key name, or an empty Version (which could never differ from itself, so
// a change could never be told).
func (d DerivedIndex) Validate() error {
	if err := keys.ValidName(d.Name); err != nil {
		return fmt.Errorf("storage: derived index name: %w", err)
	}
	if strings.TrimSpace(d.Version) == "" {
		return fmt.Errorf("storage: derived index %q has no Version", d.Name)
	}
	return nil
}

// DerivedHooks are the closures, supplied through
// Registrar.DerivedIndex(name, hooks, build) (06) because they close over
// services built in Register (COR2-15). The rebuild is not a hook: build is an
// ordinary tasks.Definition of the MODULE kind "<module>.derived.<name>.build"
// (datamigrate.DerivedBuildTask builds one, §5S.3.6), which P7 validates like
// every definition (Title, Effects, Perms.Start, Resume, a Timeout sized for a
// library-scale rebuild). storage (L1) cannot name a tasks type (COR3-7).
type DerivedHooks struct {
	Snapshot func(ctx context.Context, dir string, gen Generation) error // optional warm-start export; records gen in the artifact
	// Load must refuse unless the artifact's recorded generation equals current
	// (the kernel also compares the recorded store generation, §5S.3.7) AND
	// the owner's own check passes (hnsw truth count, server.go:845-866).
	Load func(ctx context.Context, dir string, current Generation) (accepted bool, err error)
}

// ArtifactGenerationFile is the file StampArtifact writes in an artifact
// directory: the generation the artifact was taken at.
const ArtifactGenerationFile = "generation"

// StampArtifact records gen in dir/generation, the one rule a Snapshot hook
// follows: read Derived.Current BEFORE exporting, export, then stamp that
// token. A touch that commits during the export leaves a later value, so the
// artifact is rejected rather than accepted with a missing document. The file
// is written by WriteFileDurable, so a crash leaves the old stamp or the new
// one and never a torn one; an artifact without a stamp is rejected.
//
// The token only names the SOURCE's state. An index whose content can lag its
// source (a full-text index with a dirty set: the reconciler changes the index
// without touching the generation) must also be quiescent when it reads the
// token, or the stamp vouches for a content the artifact does not hold:
// internal/bleveindex refuses to copy while any mark is pending.
func StampArtifact(dir string, gen Generation) error {
	if gen.IsZero() {
		return errors.New("storage: refusing to stamp an artifact with the zero generation")
	}
	if err := WriteFileDurable(filepath.Join(dir, ArtifactGenerationFile), []byte(gen.String()+"\n")); err != nil {
		return fmt.Errorf("storage: stamp artifact: %w", err)
	}
	return nil
}

// WriteFileDurable replaces the file at path with data so that a crash leaves
// the old content or the new, never a torn or empty file, and so that a
// success means the new content survives one: it writes a sibling temp file,
// fsyncs it, renames it over path and fsyncs the directory. It is the one
// marker and stamp writer of the derived-index machinery.
func WriteFileDurable(path string, data []byte) error {
	return writeFileDurable(vfs.Default, path, data)
}

func writeFileDurable(fsys vfs.FS, path string, data []byte) (err error) {
	tmp := path + ".tmp"
	f, err := fsys.Create(tmp, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, fsys.Remove(tmp))
		}
	}()
	if _, err := f.Write(data); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, path); err != nil {
		return err
	}
	d, err := fsys.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	// The directory sync makes the rename durable; without it a crash may
	// bring back the previous file or none.
	return errors.Join(d.Sync(), d.Close())
}

// ArtifactAccepted is the generation half of a Load hook's rule: it reports
// true only when dir/generation names exactly current. An absent, unreadable,
// malformed or zero stamp, and a zero current, are all (false, nil): rejection
// means rebuild, never an error, because the artifact is an accelerator only.
// An I/O error other than absence is returned.
func ArtifactAccepted(dir string, current Generation) (bool, error) {
	if current.IsZero() {
		return false, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, ArtifactGenerationFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: read artifact stamp: %w", err)
	}
	recorded, err := ParseGeneration(string(raw))
	if err != nil || recorded.IsZero() {
		return false, nil
	}
	return recorded == current, nil
}
