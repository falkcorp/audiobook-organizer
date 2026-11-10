// file: core/internal/configstore/configstore.go
// version: 1.0.0
// guid: 3ecdf108-c136-4a1d-aaff-deaea66fcd45
// last-edited: 2026-11-10

// Package configstore is core's one config.OverrideStore, over the keyspace
// c/config/, and the kernel's config.Committer (05-config §1.3.3, R4-1): one
// row per explicitly set key, Load read-only, every write a read-modify-write
// through GetForUpdate, and Commit as one storage batch holding the rows, the
// staged secret writes and the core.settings.update audit record, retried
// with storage.RetryOnConflict on a fresh batch per attempt. config never
// imports audit (L4); this package is the kernel's and joins them.
package configstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is c/config/'s owner in the storage claim ledger.
const Owner storage.Owner = "core/config"

// rowSeg is the first key segment of every override row: the row of path p
// is keys.Join("v", p).
const rowSeg = "v"

// OwnerSpec is the storage claim of core config: the c/config/ keyspace,
// Serialized whole, so every row write carries an Expect (R6-1) and two
// writers of one row conflict instead of losing a write.
func OwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: Owner, Spec: storage.Spec{Keyspace: true, Serialized: []string{""}}, Enabled: true}
}

// envelope is one row's value.
type envelope struct {
	Value json.RawMessage `json:"value"`
	Gen   uint64          `json:"gen"`
	At    time.Time       `json:"at"`
	By    string          `json:"by"`
}

// Store is the native override store over one c/config/ keyspace.
type Store struct{ ks storage.Keyspace }

// New returns the store over ks, the c/config/ keyspace of OwnerSpec.
func New(ks storage.Keyspace) *Store { return &Store{ks: ks} }

// Keyspace is the store's keyspace (the Committer's batch origin).
func (s *Store) Keyspace() storage.Keyspace { return s.ks }

// RowKey is the key of p's row.
func RowKey(p config.Path) []byte { return keys.Join(keys.Str(rowSeg), keys.Str(string(p))) }

// decodeEnvelope decodes a row strictly: unknown fields, trailing data and
// an empty value are refused.
func decodeEnvelope(raw []byte) (envelope, error) {
	var e envelope
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return e, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return e, errors.New("unexpected data after the row")
	}
	if len(e.Value) == 0 {
		return e, errors.New("the row has no value")
	}
	return e, nil
}

// Load yields every row, read-only. A row that is not a valid envelope is
// yielded with a nil Value, never rewritten: config cannot decode it, so it
// quarantines the row's section at Attach (05-config §1.4 step 6) and the
// row stays as it was. A read error, or a key that is not a row key, is
// yielded once and ends the sequence (fail-closed, §1.5).
func (s *Store) Load(ctx context.Context) iter.Seq2[config.Override, error] {
	return func(yield func(config.Override, error) bool) {
		for kv, err := range s.ks.ScanPrefix(ctx, keys.Prefix(keys.Str(rowSeg))) {
			if err != nil {
				yield(config.Override{}, fmt.Errorf("configstore: read c/config/: %w", err))
				return
			}
			segs, err := keys.Split(kv.Key)
			if err != nil || len(segs) != 2 || string(segs[0]) != rowSeg {
				yield(config.Override{}, fmt.Errorf("configstore: c/config/ key %q is not an override row", kv.Key))
				return
			}
			ov := config.Override{Path: config.Path(segs[1])}
			if e, derr := decodeEnvelope(kv.Value); derr == nil {
				ov.Value, ov.Gen, ov.At, ov.By = slices.Clone(e.Value), e.Gen, e.At, e.By
			}
			if !yield(ov, nil) {
				return
			}
		}
	}
}

// Stage joins b with the writes for set and unset, binding c/config/ to b.
// Each key is read with GetForUpdate first, so a concurrent writer of the same
// row fails the commit with storage.ErrConflict and the Committer retries.
// Only the staged keys change.
func (s *Store) Stage(ctx context.Context, b *storage.Batch, set []config.Override, unset []config.Path) error {
	if b == nil {
		return errors.New("configstore: Stage needs a batch")
	}
	b.Bind(s.ks)
	w := b.In(s.ks)
	read := func(p config.Path) ([]byte, error) {
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("configstore: %w", err)
		}
		k := RowKey(p)
		if _, err := b.GetForUpdate(ctx, s.ks, k); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return nil, fmt.Errorf("configstore: read %s: %w", p, err)
		}
		return k, nil
	}
	for _, ov := range set {
		k, err := read(ov.Path)
		if err != nil {
			return err
		}
		val, err := json.Marshal(envelope{Value: ov.Value, Gen: ov.Gen, At: ov.At.UTC(), By: ov.By})
		if err != nil {
			return fmt.Errorf("configstore: encode %s: %w", ov.Path, err)
		}
		if err := w.Set(k, val); err != nil {
			return fmt.Errorf("configstore: stage %s: %w", ov.Path, err)
		}
	}
	for _, p := range unset {
		k, err := read(p)
		if err != nil {
			return err
		}
		if err := w.Delete(k); err != nil {
			return fmt.Errorf("configstore: stage the unset of %s: %w", p, err)
		}
	}
	return nil
}
