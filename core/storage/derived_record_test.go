// file: core/storage/derived_record_test.go
// version: 0.1.0
// guid: 0c6f3a18-d94b-4e57-b2a1-7f8e5d3c6b40
// last-edited: 2026-11-28

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage/keys"
)

// recordEnv boots a store whose module "gotham" declares two derived indexes
// (one Blocking), plus "wayne", which declares none, and a core owner.
func recordEnv(t *testing.T) *DB {
	t.Helper()
	gotham := Spec{Keyspace: true, Derived: []DerivedIndex{
		{Name: "signal", Version: "3"},
		{Name: "cave", Version: "7", Blocking: true},
	}}
	return newEnv(t,
		OwnerSpec{Owner: "gotham", Enabled: true, Spec: gotham},
		OwnerSpec{Owner: "wayne", Enabled: true, Spec: wayneSpec()},
		OwnerSpec{Owner: "core/tasks", Enabled: true, Spec: Spec{Keyspace: true}},
	).open()
}

func derivedPrefix() []byte { return keys.Join(keys.Str("derived")) }

func TestDerivedRecord_RoundTrip(t *testing.T) {
	ctx := context.Background()
	db := recordEnv(t)

	if _, found, err := db.DerivedRecord(ctx, "gotham", "signal"); err != nil || found {
		t.Fatalf("a never-written record = found %t, err %v; want absent", found, err)
	}
	want := DerivedRecord{Version: "3", State: DerivedBuilding, BuiltAt: time.Date(2026, 11, 22, 3, 4, 5, 0, time.UTC)}
	if err := db.PutDerivedRecord(ctx, "gotham", "signal", want); err != nil {
		t.Fatal(err)
	}
	got, found, err := db.DerivedRecord(ctx, "gotham", "signal")
	if err != nil || !found || got != want {
		t.Fatalf("read back = %+v found %t err %v, want %+v", got, found, err, want)
	}
	// The sibling index has its own record.
	if _, found, _ := db.DerivedRecord(ctx, "gotham", "cave"); found {
		t.Error("writing signal's record created cave's")
	}
	// A put replaces.
	want = DerivedRecord{Version: "3", State: DerivedReady, BuiltAt: want.BuiltAt.Add(time.Hour)}
	if err := db.PutDerivedRecord(ctx, "gotham", "signal", want); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := db.DerivedRecord(ctx, "gotham", "signal"); got != want {
		t.Errorf("after a second put = %+v, want %+v", got, want)
	}
}

// TestDerivedRecord_Key: the record is c/storage/derived/<module>/<name>, on
// storage's own keyspace and nowhere else.
func TestDerivedRecord_Key(t *testing.T) {
	ctx := context.Background()
	db := recordEnv(t)
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", DerivedRecord{Version: "7", State: DerivedReady}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for kv, err := range db.storageKS().ScanPrefix(ctx, derivedPrefix()) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(kv.Key))
	}
	if want := []string{string(derivedRecordKey("gotham", "cave"))}; !slices.Equal(got, want) {
		t.Errorf("c/storage/derived/ holds %q, want %q", got, want)
	}
	// The generation key is a different row: writing a record never touches it.
	h, err := db.Derived("gotham", "cave")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.Current(ctx)
	if err := db.PutDerivedRecord(ctx, "gotham", "cave", DerivedRecord{Version: "7", State: DerivedBuilding}); err != nil {
		t.Fatal(err)
	}
	if after, _ := h.Current(ctx); after != before {
		t.Errorf("a record write moved the generation: %s -> %s", before, after)
	}
}

func TestDerivedRecord_UndeclaredRefused(t *testing.T) {
	ctx := context.Background()
	db := recordEnv(t)
	rec := DerivedRecord{Version: "1", State: DerivedReady}
	for _, c := range []struct {
		name      string
		module    Owner
		indexName string
	}{
		{"an undeclared index of a declaring module", "gotham", "batmobile"},
		{"a module that declares none", "wayne", "signal"},
		{"a core owner", "core/tasks", "signal"},
		{"an unknown owner", "joker", "signal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := db.PutDerivedRecord(ctx, c.module, c.indexName, rec); !errors.Is(err, ErrDerivedUndeclared) {
				t.Errorf("Put = %v, want ErrDerivedUndeclared", err)
			}
			if _, _, err := db.DerivedRecord(ctx, c.module, c.indexName); !errors.Is(err, ErrDerivedUndeclared) {
				t.Errorf("Get = %v, want ErrDerivedUndeclared", err)
			}
			if _, err := db.DerivedDeclaration(c.module, c.indexName); !errors.Is(err, ErrDerivedUndeclared) {
				t.Errorf("DerivedDeclaration = %v, want ErrDerivedUndeclared", err)
			}
		})
	}
	// Nothing was written by any refusal.
	for kv, err := range db.storageKS().ScanPrefix(ctx, derivedPrefix()) {
		if err != nil {
			t.Fatal(err)
		}
		t.Errorf("a refused write left %q", kv.Key)
	}
}

func TestDerivedRecord_PutRefusesAnInvalidRecord(t *testing.T) {
	ctx := context.Background()
	db := recordEnv(t)
	for name, rec := range map[string]DerivedRecord{
		"no version":    {State: DerivedReady},
		"no state":      {Version: "3"},
		"unknown state": {Version: "3", State: "stale"},
	} {
		if err := db.PutDerivedRecord(ctx, "gotham", "signal", rec); err == nil {
			t.Errorf("%s: Put accepted %+v", name, rec)
		}
	}
	if _, found, _ := db.DerivedRecord(ctx, "gotham", "signal"); found {
		t.Error("a refused record was stored")
	}
}

func TestDerivedRecord_UnreadableIsNamed(t *testing.T) {
	ctx := context.Background()
	db := recordEnv(t)
	if err := db.storageKS().Set(ctx, derivedRecordKey("gotham", "signal"), []byte("not a record")); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.DerivedRecord(ctx, "gotham", "signal"); !errors.Is(err, ErrDerivedRecordUnreadable) || found {
		t.Errorf("a corrupt record = found %t, err %v; want ErrDerivedRecordUnreadable", found, err)
	}
	// A put repairs it.
	if err := db.PutDerivedRecord(ctx, "gotham", "signal", DerivedRecord{Version: "3", State: DerivedBuilding}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := db.DerivedRecord(ctx, "gotham", "signal"); err != nil || !found {
		t.Errorf("after a put = found %t, err %v", found, err)
	}
}

func TestDerivedDeclaration(t *testing.T) {
	db := recordEnv(t)
	got, err := db.DerivedDeclaration("gotham", "cave")
	if err != nil || got != (DerivedIndex{Name: "cave", Version: "7", Blocking: true}) {
		t.Errorf("DerivedDeclaration(cave) = %+v, %v", got, err)
	}
	got, err = db.DerivedDeclaration("gotham", "signal")
	if err != nil || got.Version != "3" || got.Blocking {
		t.Errorf("DerivedDeclaration(signal) = %+v, %v", got, err)
	}
}

func TestDerivedArtifactDir(t *testing.T) {
	root := DerivedRoot("/batcave/state/storage/main.pebble")
	if want := filepath.FromSlash("/batcave/state/storage/derived"); root != want {
		t.Errorf("DerivedRoot = %q, want %q", root, want)
	}
	if got, want := DerivedArtifactDir(root, "gotham", "cave"), filepath.FromSlash("/batcave/state/storage/derived/gotham/cave"); got != want {
		t.Errorf("DerivedArtifactDir = %q, want %q", got, want)
	}
}

func TestDerivedLabels(t *testing.T) {
	if got := DerivedLabels(); !slices.Equal(got, []string{"storage.derived.record"}) {
		t.Errorf("DerivedLabels = %q", got)
	}
}
