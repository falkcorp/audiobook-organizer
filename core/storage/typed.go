// file: core/storage/typed.go
// version: 1.0.0
// guid: 4111de47-5477-4b19-bfe4-27ff4d9cca19
// last-edited: 2026-11-07

package storage

import (
	"context"
	"iter"

	"github.com/falkcorp/mediacore/storage/codec"
)

// Typed binds a codec to a keyspace for the common Get/Set/Scan shapes. It
// lives in storage, not codec, so codec imports nothing from core (MOD3-9).
type Typed[T any] struct {
	KS Keyspace
	C  codec.Codec[T]
}

// Get reads and decodes one key; ErrNotFound when absent.
func (t Typed[T]) Get(ctx context.Context, key []byte) (T, error) {
	var zero T
	raw, err := t.KS.Get(ctx, key)
	if err != nil {
		return zero, err
	}
	return t.C.Decode(raw)
}

// Put encodes v and stages it into b through t.KS, which must be bound to b.
func (t Typed[T]) Put(b *Batch, key []byte, v T) error {
	raw, err := t.C.Encode(v)
	if err != nil {
		return err
	}
	return b.In(t.KS).Set(key, raw)
}

// All decodes every value in the scan; a decode error is yielded and ends it.
func (t Typed[T]) All(ctx context.Context, opts ScanOptions) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		for kv, err := range t.KS.Scan(ctx, opts) {
			if err != nil {
				yield(zero, err)
				return
			}
			v, err := t.C.Decode(kv.Value)
			if err != nil {
				yield(zero, err)
				return
			}
			if !yield(v, nil) {
				return
			}
		}
	}
}
