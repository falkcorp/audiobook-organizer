// file: internal/books/roots/scan_test.go
// version: 0.1.0
// guid: 44165e43-3e03-4330-be9b-08363ce6c33c
// last-edited: 2026-11-20

package roots_test

import (
	"bytes"
	"context"
	"errors"
	"iter"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"

	"github.com/falkcorp/audiobook-organizer/internal/books/roots"
)

// memRows is an in-memory roots.Rows. Get yields the processor after reading, so
// a read-modify-write without a lock loses an update in this fake.
type memRows struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func newMemRows() *memRows { return &memRows{rows: map[string][]byte{}} }

func (m *memRows) Get(_ context.Context, key []byte) ([]byte, error) {
	m.mu.Lock()
	v, ok := m.rows[string(key)]
	m.mu.Unlock()
	runtime.Gosched()
	if !ok {
		return nil, storage.ErrNotFound
	}
	return bytes.Clone(v), nil
}

func (m *memRows) Set(_ context.Context, key, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[string(key)] = bytes.Clone(value)
	return nil
}

func (m *memRows) Delete(_ context.Context, key []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, string(key))
	return nil
}

func (m *memRows) Scan(_ context.Context, opts storage.ScanOptions) iter.Seq2[storage.KV, error] {
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

var t0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func TestScanStore_RecordScanKeepsEveryOtherField(t *testing.T) {
	rows := newMemRows()
	s := roots.NewScanStore(rows)
	ctx := context.Background()
	id := roots.RootIDFor(3)
	created := t0.Add(-48 * time.Hour)
	if err := rows.Set(ctx, []byte("rootscan:import-3"), []byte(`{"enabled":false,"created_at":"`+created.Format(time.RFC3339)+`","book_count":2}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScan(ctx, id, 41, t0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
	if got.Enabled || !got.CreatedAt.Equal(created) || got.BookCount != 41 || got.LastScan == nil || !got.LastScan.Equal(t0) {
		t.Errorf("after RecordScan: %+v (Enabled and CreatedAt must be untouched, count and time set)", got)
	}
}

func TestScanStore_AbsentRowIsCreatedByAWriter(t *testing.T) {
	ctx := context.Background()
	s := roots.NewScanStore(newMemRows())
	id := roots.RootIDFor(8)
	if _, ok, err := s.Get(ctx, id); ok || err != nil {
		t.Fatalf("Get on an absent row = %v, %v", ok, err)
	}
	if err := s.RecordScan(ctx, id, 5, t0); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(ctx, id)
	if !got.Enabled || !got.CreatedAt.Equal(t0) || got.BookCount != 5 {
		t.Errorf("a scan of a root with no row made %+v; want enabled, created at the scan, count 5", got)
	}
}

func TestScanStore_CreateIsIdempotentAndKeepsAScan(t *testing.T) {
	ctx := context.Background()
	s := roots.NewScanStore(newMemRows())
	id := roots.RootIDFor(2)
	if err := s.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordScan(ctx, id, 9, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Create(ctx, id); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Get(ctx, id)
	if got.BookCount != 9 || got.LastScan == nil {
		t.Errorf("Create after a scan reset the row: %+v", got)
	}
}

// The lost update HEAD's whole-row writers had: a scan recording its count and
// the UI switching the root off, finishing together. Without the per-root lock
// one of them overwrites the other (memRows.Get yields between read and write).
func TestScanStore_ConcurrentWritersOfDifferentFieldsBothLand(t *testing.T) {
	ctx := context.Background()
	for range 50 {
		s := roots.NewScanStore(newMemRows())
		id := roots.RootIDFor(1)
		if err := s.Create(ctx, id); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.RecordScan(ctx, id, 77, t0) }()
		go func() { defer wg.Done(); _ = s.SetEnabled(ctx, id, false) }()
		wg.Wait()
		got, _, _ := s.Get(ctx, id)
		if got.BookCount != 77 || got.Enabled {
			t.Fatalf("a concurrent update was lost: %+v (want count 77 and disabled)", got)
		}
	}
}

func TestScanStore_UndecodableRowIsNeverOverwritten(t *testing.T) {
	ctx := context.Background()
	rows := newMemRows()
	s := roots.NewScanStore(rows)
	_ = rows.Set(ctx, []byte("rootscan:import-4"), []byte(`{not json`))
	if err := s.RecordScan(ctx, roots.RootIDFor(4), 1, t0); !errors.Is(err, roots.ErrBadScanRow) {
		t.Fatalf("RecordScan over a bad row = %v; want ErrBadScanRow", err)
	}
	if err := s.SetEnabled(ctx, roots.RootIDFor(4), true); !errors.Is(err, roots.ErrBadScanRow) {
		t.Fatalf("SetEnabled over a bad row = %v; want ErrBadScanRow", err)
	}
	if got, _ := rows.Get(ctx, []byte("rootscan:import-4")); string(got) != `{not json` {
		t.Errorf("the bad row was overwritten with %q", got)
	}
}

func TestScanStore_AllReportsABadRowBesideTheGoodOnes(t *testing.T) {
	ctx := context.Background()
	rows := newMemRows()
	s := roots.NewScanStore(rows)
	_ = s.RecordScan(ctx, roots.RootIDFor(1), 3, t0)
	_ = rows.Set(ctx, []byte("rootscan:import-2"), []byte(`[]`))
	_ = rows.Set(ctx, []byte("other:import-9"), []byte(`{}`)) // outside the family
	all, err := s.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("All = %d entries; want 2 (the family only): %+v", len(all), all)
	}
	if all["import-1"].Err != nil || all["import-1"].State.BookCount != 3 {
		t.Errorf("good row: %+v", all["import-1"])
	}
	if !errors.Is(all["import-2"].Err, roots.ErrBadScanRow) {
		t.Errorf("bad row: %+v; want ErrBadScanRow", all["import-2"])
	}
}

func TestScanStore_DeleteAndNextImportN(t *testing.T) {
	ctx := context.Background()
	s := roots.NewScanStore(newMemRows())
	_ = s.RecordScan(ctx, roots.RootIDFor(6), 1, t0)
	registered := []library.RootID{"books-library", roots.RootIDFor(2)}
	// An orphan row (its root removed) still counts: a new root must not inherit it.
	if n, err := s.NextImportN(ctx, registered); err != nil || n != 7 {
		t.Fatalf("NextImportN with an orphan row = %d, %v; want 7", n, err)
	}
	if err := s.Delete(ctx, roots.RootIDFor(6)); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, roots.RootIDFor(6)); err != nil {
		t.Fatalf("deleting an absent row: %v", err)
	}
	if n, _ := s.NextImportN(ctx, registered); n != 3 {
		t.Errorf("NextImportN after Delete = %d; want 3", n)
	}
}

func TestScanStore_RefusesNonRootIDsAndNegativeCounts(t *testing.T) {
	ctx := context.Background()
	s := roots.NewScanStore(newMemRows())
	if err := s.RecordScan(ctx, "Import 1", 1, t0); err == nil {
		t.Error("RecordScan accepted an invalid root ID")
	}
	if err := s.RecordScan(ctx, roots.RootIDFor(1), -1, t0); err == nil {
		t.Error("RecordScan accepted a negative count")
	}
}
