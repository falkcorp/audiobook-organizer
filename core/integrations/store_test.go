// file: core/integrations/store_test.go
// version: 1.0.0
// guid: 84959f3a-97c8-461f-8840-9840bc66971a
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/internal/instancestore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// writeRaw stores raw at key in the integrations keyspace, as a damaged or
// foreign record would sit there.
func writeRaw(t *testing.T, ks storage.Keyspace, key, raw []byte) {
	t.Helper()
	ctx := context.Background()
	b := ks.NewBatch()
	defer func() { _ = b.Close() }()
	if _, err := b.GetForUpdate(ctx, ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if err := b.Set(key, raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// rowKey is ref's row key.
func rowKey(ref integrations.Ref) []byte {
	return instancestore.RowKey(string(ref.Kind), string(ref.Instance))
}

// TestNativeInstanceStore_RoundTrip (M1.4 "Native store"): one row per
// instance at c/integrations/instances/<kind>/<id>, read back field for
// field, plus the conformance suite.
func TestNativeInstanceStore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	st := integrations.NativeInstanceStore(e.ks)
	in := integrationstest.Batcave()
	b := e.ks.NewBatch()
	v, err := st.Put(ctx, b, in)
	if err != nil || v != 1 {
		t.Fatalf("Put = %d, %v", v, err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	raw := rawRows(t, e)
	if _, ok := raw[string(rowKey(in.Config.Ref))]; !ok || len(raw) != 1 {
		t.Fatalf("rows = %d, the row is not at instances/<kind>/<id>", len(raw))
	}
	rows, err := st.Load(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("Load = %d rows, %v", len(rows), err)
	}
	want := in
	want.Config.Version, want.Config.Stored = 1, true
	if !reflect.DeepEqual(rows[0], want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", rows[0], want)
	}
}

// TestNativeInstanceStore_Suite runs integrationstest.InstanceStoreSuite
// against NativeInstanceStore.
func TestNativeInstanceStore_Suite(t *testing.T) {
	integrationstest.InstanceStoreSuite(t, integrationstest.Harness{New: func(t *testing.T) integrationstest.Fixture {
		e := newEnv(t)
		return integrationstest.Fixture{
			Store:    integrations.NativeInstanceStore(e.ks),
			NewBatch: e.ks.NewBatch,
			Corrupt: func(t *testing.T, ref integrations.Ref) {
				writeRaw(t, e.ks, rowKey(ref), []byte("the Joker was here"))
			},
		}
	}})
}

// TestNativeInstanceStore_MalformedRows: every shape of a row that does not
// decode is loaded with Malformed naming what failed and the version it
// still shows; a key that is not <kind>/<id> fails the load.
func TestNativeInstanceStore_MalformedRows(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	enc := func(m map[string]any) []byte {
		raw, err := codec.JSON[map[string]any](1).Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	row := func(id string, extra map[string]any) map[string]any {
		m := map[string]any{"kind": "indexer", "instance": id, "adapter": "hello.oracle", "enabled": true, "origin": "ui", "version": 4}
		maps.Copy(m, extra)
		return m
	}
	ref := func(id string) integrations.Ref {
		return integrations.Ref{Kind: integrations.KindIndexer, Instance: integrations.InstanceID(id)}
	}
	cases := map[string]struct {
		raw     []byte
		field   string
		version uint64
	}{
		"garbage":   {[]byte("not a row"), "row", 0},
		"zone":      {enc(row("zone", map[string]any{"limits": map[string]any{"daily": map[string]any{"zone": "Gotham/Narrows"}}})), "limits", 4},
		"classkey":  {enc(row("classkey", map[string]any{"limits": map[string]any{"holds": map[string]any{"durations": map[string]any{"auth": 1}}}})), "limits", 4},
		"mismatch":  {enc(row("elsewhere", nil)), "ref", 4},
		"badmember": {enc(row("badmember", map[string]any{"enabled": "yes"})), "enabled", 4},
	}
	for id, c := range cases {
		writeRaw(t, e.ks, rowKey(ref(id)), c.raw)
	}
	rows, err := integrations.NativeInstanceStore(e.ks).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[integrations.InstanceID]integrations.StoredInstance{}
	for _, r := range rows {
		got[r.Config.Ref.Instance] = r
	}
	for id, c := range cases {
		r := got[integrations.InstanceID(id)]
		if r.Malformed != c.field || r.Config.Version != c.version || !r.Config.Stored {
			t.Errorf("%s: Malformed %q version %d stored %v; want %q, %d", id, r.Malformed, r.Config.Version, r.Config.Stored, c.field, c.version)
		}
	}
	writeRaw(t, e.ks, keys.Join(keys.Str("instances"), keys.Str("indexer")), []byte("x"))
	if _, err := integrations.NativeInstanceStore(e.ks).Load(ctx); err == nil {
		t.Error("a key that is not <kind>/<id> did not fail the load")
	}
}
