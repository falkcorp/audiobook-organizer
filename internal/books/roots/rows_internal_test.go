// file: internal/books/roots/rows_internal_test.go
// version: 0.1.0
// guid: 92bd5818-9af8-401f-9959-f3ccc27dc0f5
// last-edited: 2026-11-20

package roots

import (
	"bytes"
	"context"
	"iter"
	"sort"
	"sync"

	"github.com/falkcorp/mediacore/storage"
)

// mapRows is an in-memory Rows for the package's own tests.
type mapRows struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *mapRows) Get(_ context.Context, key []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.rows[string(key)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return bytes.Clone(v), nil
}

func (m *mapRows) Set(_ context.Context, key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[string(key)] = bytes.Clone(value)
	return nil
}

func (m *mapRows) Delete(_ context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, string(key))
	return nil
}

func (m *mapRows) Scan(_ context.Context, opts storage.ScanOptions) iter.Seq2[storage.KV, error] {
	m.mu.Lock()
	var keys []string
	for k := range m.rows {
		if bytes.Compare([]byte(k), opts.Start) >= 0 && (opts.End == nil || bytes.Compare([]byte(k), opts.End) < 0) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var kvs []storage.KV
	for _, k := range keys {
		kvs = append(kvs, storage.KV{Key: []byte(k), Value: bytes.Clone(m.rows[k])})
	}
	m.mu.Unlock()
	return func(yield func(storage.KV, error) bool) {
		for _, kv := range kvs {
			if !yield(kv, nil) {
				return
			}
		}
	}
}
