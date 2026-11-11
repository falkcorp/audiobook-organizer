// file: core/internal/instancestore/instancestore.go
// version: 1.0.0
// guid: bb2cac5e-071e-444e-8fbe-a71d564345bf
// last-edited: 2026-11-11

// Package instancestore is the key layout and the raw, versioned staging
// under integrations.NativeInstanceStore and integrations.NativeImporter
// (05-metadata M1.4 "Native store"): one row per instance at
// c/integrations/instances/<kind>/<id>, no index rows (a kind is one prefix
// scan). It never decodes a row beyond the version its caller's func reads:
// the row's format, codec.JSON[integrations.StoredInstance](1), belongs to
// package integrations, which cannot be imported from here (it imports this
// package). It holds no cache and no lock: every write stages into the
// caller's batch with an Expect on the row's committed value, so a concurrent
// writer fails the commit with storage.ErrConflict.
//
// It implements integrations (layer L4, 03 R4) and is imported only by
// integrations.
package instancestore

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the storage owner of the c/integrations/ keyspace.
const Owner storage.Owner = "core/integrations"

// instancesSeg is the first key segment of every instance row.
const instancesSeg = "instances"

// StorageSpec is the c/integrations/ spec the kernel registers for Owner: a
// core keyspace whose instance rows are Serialized, so every write to one
// carries an Expect (05-storage R6-1) and two writers of one row conflict
// instead of losing a write.
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{string(instancesPrefix())}}
}

// instancesPrefix is the relative prefix of every instance row.
func instancesPrefix() []byte { return keys.Prefix(keys.Str(instancesSeg)) }

// RowKey is the relative key of the row of instance id of kind.
func RowKey(kind, id string) []byte {
	return keys.Join(keys.Str(instancesSeg), keys.Str(kind), keys.Str(id))
}

// ErrVersionMismatch: a Put's expected version is not the stored row's (0
// expects no row, or a row whose version cannot be read: the repair of a
// malformed row).
var ErrVersionMismatch = errors.New("instancestore: the expected version is not the stored row's")

// Row is one stored row, raw.
type Row struct {
	Kind, ID string
	Value    []byte
}

// Store is the raw store over one c/integrations/ keyspace.
type Store struct {
	ks      storage.Keyspace
	version func(raw []byte) (uint64, error)
}

// New returns the store over ks, the c/integrations/ keyspace. version reads
// a stored row's version from its value; an error makes the row's version
// unreadable (a malformed row).
func New(ks storage.Keyspace, version func(raw []byte) (uint64, error)) *Store {
	return &Store{ks: ks, version: version}
}

// Keyspace is the store's keyspace (the origin of the Registry's batches).
func (s *Store) Keyspace() storage.Keyspace { return s.ks }

// Load returns every instance row, sorted by key, read-only. A key under
// instances/ that is not <kind>/<id> fails the load: a store whose rows
// cannot be told apart must not be served as a partial list.
func (s *Store) Load(ctx context.Context) ([]Row, error) {
	var out []Row
	for kv, err := range s.ks.ScanPrefix(ctx, instancesPrefix()) {
		if err != nil {
			return nil, fmt.Errorf("instancestore: read c/integrations/: %w", err)
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 3 || string(segs[0]) != instancesSeg || len(segs[1]) == 0 || len(segs[2]) == 0 {
			return nil, fmt.Errorf("instancestore: c/integrations/ key %q is not an instance row", kv.Key)
		}
		out = append(out, Row{Kind: string(segs[1]), ID: string(segs[2]), Value: bytes.Clone(kv.Value)})
	}
	return out, nil
}

// read binds the keyspace to b and reads the row with an Expect on its
// committed value. A nil value is an absent row.
func (s *Store) read(ctx context.Context, b *storage.Batch, kind, id string) ([]byte, error) {
	if b == nil {
		return nil, errors.New("instancestore: a write needs the caller's batch")
	}
	if kind == "" || id == "" {
		return nil, errors.New("instancestore: a row needs a kind and an ID")
	}
	b.Bind(s.ks)
	raw, err := b.GetForUpdate(ctx, s.ks, RowKey(kind, id))
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("instancestore: read %s/%s: %w", kind, id, err)
	}
	return raw, nil
}

// Put stages the row of kind/id into b when its stored version is expect,
// and returns the new version, expect+1. expect 0 expects no row, or a row
// whose version cannot be read (the repair of a malformed row, which then
// becomes version 1). encode renders the row at the new version. A
// mismatch is ErrVersionMismatch and stages nothing.
func (s *Store) Put(ctx context.Context, b *storage.Batch, kind, id string, expect uint64, encode func(version uint64) ([]byte, error)) (uint64, error) {
	raw, err := s.read(ctx, b, kind, id)
	if err != nil {
		return 0, err
	}
	var cur uint64
	if raw != nil {
		v, verr := s.version(raw)
		if verr == nil {
			cur = v
		}
	}
	if expect != cur {
		return 0, fmt.Errorf("%w: %s/%s is at version %d, the write expected %d", ErrVersionMismatch, kind, id, cur, expect)
	}
	next := cur + 1
	val, err := encode(next)
	if err != nil {
		return 0, err
	}
	if err := b.In(s.ks).Set(RowKey(kind, id), val); err != nil {
		return 0, fmt.Errorf("instancestore: stage %s/%s: %w", kind, id, err)
	}
	return next, nil
}

// Delete stages the deletion of kind/id's row into b, with an Expect on its
// committed value. An absent row is storage.ErrNotFound and stages nothing.
func (s *Store) Delete(ctx context.Context, b *storage.Batch, kind, id string) error {
	raw, err := s.read(ctx, b, kind, id)
	if err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("instancestore: delete %s/%s: %w", kind, id, storage.ErrNotFound)
	}
	if err := b.In(s.ks).Delete(RowKey(kind, id)); err != nil {
		return fmt.Errorf("instancestore: stage the delete of %s/%s: %w", kind, id, err)
	}
	return nil
}

// Import stages val as kind/id's row into b, create-only (05-storage
// §5S.3.13's importer rules), and reports whether it staged it: an absent
// row is written (true); a row holding exactly val is already current and
// stages nothing (false), so a re-run of a step that committed is a no-op;
// any other row fails with storage.ErrImportConflict.
func (s *Store) Import(ctx context.Context, b *storage.Batch, kind, id string, val []byte) (bool, error) {
	raw, err := s.read(ctx, b, kind, id)
	if err != nil {
		return false, err
	}
	if raw != nil {
		if bytes.Equal(raw, val) {
			return false, nil
		}
		return false, fmt.Errorf("%w: integration instance %s/%s already has a row with other content", storage.ErrImportConflict, kind, id)
	}
	if err := b.In(s.ks).Set(RowKey(kind, id), val); err != nil {
		return false, fmt.Errorf("instancestore: stage the import of %s/%s: %w", kind, id, err)
	}
	return true, nil
}
