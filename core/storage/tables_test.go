// file: core/storage/tables_test.go
// version: 1.0.0
// guid: 3d8b6f20-7a41-4c95-b2e8-6f1c0a9d5e37
// last-edited: 2026-11-08

package storage

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

// tablesStore is a gotham store with n flushed joker keys in one sstable.
func tablesStore(t *testing.T, n int) (*DB, *Admin) {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t, OwnerSpec{Owner: "gotham", Enabled: true, Spec: Spec{Claims: gothamClaims()}})
	db := e.open()
	g, _ := db.Legacy("gotham")
	for i := range n {
		if err := g.Set(ctx, []byte{'j', 'o', 'k', 'e', 'r', ':', byte('a' + i)}, []byte("ha")); err != nil {
			t.Fatal(err)
		}
	}
	a := db.Admin()
	if err := a.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	return db, a
}

// TestAdminTablesListsFlushedTables pins what ABO's census reads: after a
// flush the joker keys are in a table whose bounds hold them, with their
// entry count and raw sizes when properties are asked for, and without
// properties when they are not.
func TestAdminTablesListsFlushedTables(t *testing.T) {
	ctx := context.Background()
	_, a := tablesStore(t, 5)

	tables, err := a.Tables(ctx, TablesOptions{Properties: true})
	if err != nil {
		t.Fatal(err)
	}
	var joker *TableInfo
	for i := range tables {
		ti := &tables[i]
		if bytes.Compare(ti.Smallest, []byte("joker:a")) <= 0 && bytes.Compare(ti.Largest, []byte("joker:e")) >= 0 {
			joker = ti
		}
	}
	if joker == nil {
		t.Fatalf("no table spans joker:a..joker:e: %+v", tables)
	}
	if !joker.HasProperties || joker.Entries < 5 {
		t.Errorf("joker table properties = %+v, want HasProperties and at least 5 entries", joker)
	}
	if joker.RawKeyBytes == 0 || joker.RawValueBytes == 0 || joker.Size == 0 {
		t.Errorf("joker table sizes = %+v, want raw key, raw value and on-disk bytes", joker)
	}

	bare, err := a.Tables(ctx, TablesOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) != len(tables) {
		t.Fatalf("Tables without properties = %d tables, with = %d", len(bare), len(tables))
	}
	for _, ti := range bare {
		if ti.HasProperties || ti.Entries != 0 {
			t.Errorf("properties reported without TablesOptions.Properties: %+v", ti)
		}
	}
}

// TestAdminTablesRangeAndSpanBytes: a range keeps only the tables that
// overlap it, and SpanBytes reports the bytes inside it; a range no table
// overlaps lists nothing.
func TestAdminTablesRangeAndSpanBytes(t *testing.T) {
	ctx := context.Background()
	_, a := tablesStore(t, 5)

	in, err := a.Tables(ctx, TablesOptions{Start: []byte("joker:"), End: []byte("joker;"), SpanBytes: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(in) == 0 {
		t.Fatal("no table overlaps the joker range")
	}
	var span uint64
	for _, ti := range in {
		span += ti.SpanBytes
	}
	if span == 0 {
		t.Errorf("SpanBytes = 0 over the joker range: %+v", in)
	}

	out, err := a.Tables(ctx, TablesOptions{Start: []byte("zsasz:"), End: []byte("zsasz;")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("a range past every key lists %d tables: %+v", len(out), out)
	}
}

// TestAdminTablesRefusals: a half-open range or SpanBytes without a range is
// refused before the lease, a cancelled context is its own error, and a
// closed store is ErrClosed, never an empty list a census would read as an
// empty store.
func TestAdminTablesRefusals(t *testing.T) {
	db, a := tablesStore(t, 1)
	ctx := context.Background()
	for name, o := range map[string]TablesOptions{
		"start only":     {Start: []byte("joker:")},
		"end only":       {End: []byte("joker;")},
		"span, no range": {SpanBytes: true},
	} {
		if _, err := a.Tables(ctx, o); !errors.Is(err, errTablesSpanNeedsRange) {
			t.Errorf("%s: Tables = %v, want errTablesSpanNeedsRange", name, err)
		}
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.Tables(cancelled, TablesOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Tables with a cancelled context = %v, want context.Canceled", err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Tables(ctx, TablesOptions{}); !errors.Is(err, ErrClosed) {
		t.Errorf("Tables on a closed store = %v, want ErrClosed", err)
	}
}
