// file: core/storage/index/dirty.go
// version: 1.0.0
// guid: b4876c35-5657-4642-8921-0f2ac23abf8d
// last-edited: 2026-11-11

package index

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Owner is the core owner of c/index/, the keyspace the native dirty sets
// live in (05-storage §5S.3.8: c/index/<module>/<name>/dirty/<id>).
const Owner storage.Owner = "core/index"

// StorageSpec is the c/index/ spec the kernel registers for Owner. The whole
// keyspace is Serialized: every row in it is a dirty mark, and a mark is
// written and cleared under a precondition, so a re-mark that commits while the
// reconciler is indexing the previous state is never lost (see DirtySet.Clear).
func StorageSpec() storage.Spec {
	return storage.Spec{Keyspace: true, Serialized: []string{""}}
}

// dirtySequence names the Sequence every mark value is drawn from, on c/index/.
const dirtySequence = "dirty"

// DirtySet is one index's durable set of ids whose document must be
// (re-)indexed. A mark commits in the same batch as the entity write that made
// it (G4), so a crash after the commit leaves the mark for the reconciler to
// drain on the next boot; there is no in-memory event the index depends on.
type DirtySet struct {
	ks      storage.Keyspace // c/index/<module>/<name>/dirty/
	seq     *storage.Sequence
	derived storage.Derived
}

// Mark is one pending id and the token of the mark that put it there. Clear
// removes the row only while it still holds Token.
type Mark struct {
	ID    string
	Token []byte
}

// NewDirtySet builds the dirty set of module's index name over root, the
// c/index/ keyspace (Owner), touching derived, the index's generation handle,
// on every mark.
func NewDirtySet(root storage.Keyspace, derived storage.Derived, module, name string) (*DirtySet, error) {
	if root.Owner() != Owner {
		return nil, fmt.Errorf("index: dirty set over keyspace of %q, want %q", root.Owner(), Owner)
	}
	ks := root.Sub(module).Sub(name).Sub("dirty")
	if ks.Prefix() == "" {
		return nil, fmt.Errorf("index: dirty set for %q/%q: module and index names must be key names", module, name)
	}
	return &DirtySet{ks: ks, seq: root.Sequence(dirtySequence), derived: derived}, nil
}

// idKey is the row key of id: entity IDs are arbitrary bytes, so the key is a
// key-encoded segment, never a Sub name.
func idKey(id string) []byte { return keys.Join(keys.Str(id)) }

// Mark stages id into b, the entity's batch, and touches the index's storage
// generation in it (§5S.3.9). The row holds a fresh sequence value, so two
// marks of one id never carry the same token.
//
// The write is a read-modify-write under a precondition (the dirty keyspace is
// Serialized): a concurrent mark of the same id, or a reconciler clear that
// commits between this read and b's commit, fails b's Commit with
// storage.ErrConflict, and the caller retries (storage.RetryOnConflict). That
// is the price of never losing a mark to a clear.
func (d *DirtySet) Mark(ctx context.Context, b *storage.Batch, id string) error {
	if id == "" {
		return errors.New("index: MarkDirty of an empty id")
	}
	if b == nil {
		return errors.New("index: MarkDirty with a nil batch")
	}
	b.Bind(d.ks)
	key := idKey(id)
	if _, err := b.GetForUpdate(ctx, d.ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("index: read dirty mark: %w", err)
	}
	n, err := d.seq.NextIn(ctx, b)
	if err != nil {
		return fmt.Errorf("index: dirty mark token: %w", err)
	}
	if err := b.In(d.ks).Set(key, binary.BigEndian.AppendUint64(nil, n)); err != nil {
		return fmt.Errorf("index: stage dirty mark: %w", err)
	}
	if err := d.derived.Touch(ctx, b); err != nil {
		return fmt.Errorf("index: touch generation: %w", err)
	}
	return nil
}

// Pending returns up to limit marks in key order (limit <= 0: all). Marks added
// while it runs may or may not appear.
func (d *DirtySet) Pending(ctx context.Context, limit int) ([]Mark, error) {
	return d.PendingAfter(ctx, "", limit)
}

// PendingAfter is Pending starting after the mark of afterID in key order
// ("" = from the first), so a pass that cannot clear every mark (one whose
// document fails to load stays pending) still walks the whole set instead of
// reading the same failing page forever.
func (d *DirtySet) PendingAfter(ctx context.Context, afterID string, limit int) ([]Mark, error) {
	opts := storage.ScanOptions{}
	if afterID != "" {
		opts.Start = keys.PrefixEnd(idKey(afterID))
	}
	var out []Mark
	for kv, err := range d.ks.Scan(ctx, opts) {
		if err != nil {
			return nil, fmt.Errorf("index: scan dirty set: %w", err)
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 1 {
			return nil, fmt.Errorf("index: dirty set holds a key that is not one segment: %v", err)
		}
		out = append(out, Mark{ID: string(segs[0]), Token: append([]byte(nil), kv.Value...)})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Count is the number of pending marks.
func (d *DirtySet) Count(ctx context.Context) (int, error) {
	n := 0
	for _, err := range d.ks.ScanPrefix(ctx, nil) {
		if err != nil {
			return 0, fmt.Errorf("index: count dirty set: %w", err)
		}
		n++
	}
	return n, nil
}

// Clear removes m's row if, and only if, it still holds m.Token, and reports
// whether it did. A row that was marked again since m was read keeps its new
// token and the id stays pending: the document the reconciler just indexed is
// older than the write that re-marked it. Call Clear only after the document
// was indexed.
func (d *DirtySet) Clear(ctx context.Context, m Mark) (cleared bool, err error) {
	b := d.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	key := idKey(m.ID)
	if err := b.Expect(d.ks, key, m.Token); err != nil {
		return false, fmt.Errorf("index: clear dirty mark: %w", err)
	}
	if err := b.Delete(key); err != nil {
		return false, fmt.Errorf("index: clear dirty mark: %w", err)
	}
	// NoSync: a clear lost to a crash only re-indexes the document.
	switch err := b.Commit(ctx, storage.NoSync); {
	case errors.Is(err, storage.ErrConflict):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("index: clear dirty mark: %w", err)
	}
	return true, nil
}
