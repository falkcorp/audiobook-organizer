// file: core/storage/derived_record.go
// version: 0.1.0
// guid: 5a1e9c34-72b8-4d0f-a6c3-8e4b1d7f2095
// last-edited: 2026-11-28

package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// The derived-index record (05-storage §5S.3.9 "When the hooks run"): what the
// kernel writes at Run start step (3a) after an index's Load, so a later boot
// can tell, without trusting an artifact, whether the index was last built to
// the version this build declares.
//
//	c/storage/derived/<module>/<name> = {version, state, built_at}
//
// It lives on storage's own keyspace c/storage/, beside the generation key
// c/storage/gen/<module>/<name> (derived.go), and exists only for indexes the
// module declared in Spec.Derived: the same check as DB.Derived. Nothing in
// this file runs at boot; the kernel's step (3a) is the one caller (F7-09).

// DerivedState is the record's state: what the kernel last decided about the
// index's content.
//
// Neither value is a health state. 05-health reads readiness from the record:
// a Blocking index whose record is not DerivedReady keeps readiness false.
type DerivedState string

const (
	// DerivedReady: the content in use was loaded from an artifact the owner
	// accepted, or a build finished, at the recorded version.
	DerivedReady DerivedState = "ready"
	// DerivedBuilding: a build is owed. The kernel writes it before it queues
	// the build run, so a crash between the two leaves a record that makes the
	// next boot queue the run again, whatever Load says then. Whoever finishes
	// the build writes DerivedReady (the kernel's markDerivedBuilt).
	DerivedBuilding DerivedState = "building"
)

// Valid reports a state the record may hold.
func (s DerivedState) Valid() bool { return s == DerivedReady || s == DerivedBuilding }

// DerivedRecord is c/storage/derived/<module>/<name>.
type DerivedRecord struct {
	// Version is the declared DerivedIndex.Version the record was written for.
	Version string       `json:"version"`
	State   DerivedState `json:"state"`
	// BuiltAt is when the index's content was last made ready: an accepted Load
	// or a finished build. A building record carries the previous value (zero
	// when the index never was ready).
	BuiltAt time.Time `json:"built_at"`
}

// ErrDerivedRecordUnreadable: the stored record does not decode. A record is a
// hint and the index is an accelerator, so the kernel treats it as absent and
// writes a fresh one; the error is for a caller that wants to say so.
var ErrDerivedRecordUnreadable = errors.New("storage: the derived-index record is unreadable")

func derivedRecordKey(module Owner, name string) []byte {
	return keys.Join(keys.Str("derived"), keys.Str(string(module)), keys.Str(name))
}

func codecDerivedRecord() codec.Codec[DerivedRecord] { return codec.JSON[DerivedRecord](1) }

// labelDerivedRecord is the crash-matrix label of the record's write: the one
// store write of derived-index step (3a) that storage makes (the enqueue is the
// kernel's, through the tasks store).
const labelDerivedRecord = "storage.derived.record"

// DerivedLabels are the crash-matrix labels of this file's durable operations,
// for the kit's writer table (coretest/crashmatrix).
func DerivedLabels() []string { return []string{labelDerivedRecord} }

// DerivedDeclaration is module's declaration of the derived index name, as its
// owner registered it in Spec.Derived. It carries the Version and Blocking the
// kernel's step (3a) compares and gates on, which the hooks and the build task
// do not (they are Registrar data). Undeclared, and a core owner, are
// ErrDerivedUndeclared, exactly as DB.Derived refuses them.
func (db *DB) DerivedDeclaration(module Owner, name string) (DerivedIndex, error) {
	if _, err := db.Derived(module, name); err != nil {
		return DerivedIndex{}, err
	}
	for _, d := range db.owners[module].spec.Derived {
		if d.Name == name {
			return d, nil
		}
	}
	// Unreachable: Derived found it.
	return DerivedIndex{}, fmt.Errorf("storage: derived index %q of module %q: %w", name, module, ErrDerivedUndeclared)
}

// DerivedRecord reads the record of module's index name. found is false when
// none was ever written; ErrDerivedRecordUnreadable when it does not decode.
func (db *DB) DerivedRecord(ctx context.Context, module Owner, name string) (rec DerivedRecord, found bool, err error) {
	if _, err := db.Derived(module, name); err != nil {
		return DerivedRecord{}, false, err
	}
	raw, err := db.storageKS().Get(ctx, derivedRecordKey(module, name))
	switch {
	case errors.Is(err, ErrNotFound):
		return DerivedRecord{}, false, nil
	case err != nil:
		return DerivedRecord{}, false, err
	}
	rec, err = codecDerivedRecord().Decode(raw)
	if err != nil {
		return DerivedRecord{}, false, fmt.Errorf("%w: %s/%s: %w", ErrDerivedRecordUnreadable, module, name, err)
	}
	return rec, true, nil
}

// PutDerivedRecord writes the record of module's index name (replacing any),
// committed with Sync inside the crash-matrix region DerivedLabels names. It
// refuses an index its module did not declare, an empty Version and a State
// that is neither DerivedReady nor DerivedBuilding.
func (db *DB) PutDerivedRecord(ctx context.Context, module Owner, name string, rec DerivedRecord) error {
	if _, err := db.Derived(module, name); err != nil {
		return err
	}
	if rec.Version == "" {
		return fmt.Errorf("storage: derived record of %s/%s has no Version", module, name)
	}
	if !rec.State.Valid() {
		return fmt.Errorf("storage: derived record of %s/%s has state %q, want %q or %q", module, name, rec.State, DerivedReady, DerivedBuilding)
	}
	raw, err := codecDerivedRecord().Encode(rec)
	if err != nil {
		return fmt.Errorf("storage: derived record of %s/%s: %w", module, name, err)
	}
	if err := db.labelled(labelDerivedRecord, func() error {
		return db.storageKS().Set(ctx, derivedRecordKey(module, name), raw)
	}); err != nil {
		return fmt.Errorf("storage: write the derived record of %s/%s: %w", module, name, err)
	}
	return nil
}

// DerivedRoot is the directory the warm-start artifacts live under, beside the
// store: <db parent>/derived (§5S.3.9). dbPath is the store's path.
func DerivedRoot(dbPath string) string { return filepath.Join(filepath.Dir(dbPath), "derived") }

// DerivedArtifactDir is where module's index name keeps its artifact,
// root/<module>/<name>/ (root is DerivedRoot's value). It only names the
// directory: nothing here creates it, and an absent directory is a rejected
// artifact, not an error (ArtifactAccepted).
func DerivedArtifactDir(root string, module Owner, name string) string {
	return filepath.Join(root, string(module), name)
}
