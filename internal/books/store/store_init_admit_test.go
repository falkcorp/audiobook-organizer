// file: internal/books/store/store_init_admit_test.go
// version: 1.0.0
// guid: 6f2a9c18-3d54-4b71-8e0a-c5d7b19e4a36
// last-edited: 2026-11-28

package store

import (
	"errors"
	"testing"
	"time"
)

// TestInitializeStoreWith_AdmitRefusalComesBeforeTheOpsV3Reconcile: the opener
// writes to the store it opens (ReconcileOpsV3WithV2 rebuilds the opv3 record of
// any run a previous binary changed), and a process that must not run over the
// store (the operation child over a store whose identity cutover is done) has to
// be refused before that write, not after it. A run an old binary wrote with no
// opv3 record is the probe: a refused open leaves it without one, the store is
// closed (it opens again at once) and is not left as the global store; the same
// open admitted creates the record, so the probe does see the reconcile.
func TestInitializeStoreWith_AdmitRefusalComesBeforeTheOpsV3Reconcile(t *testing.T) {
	dir := t.TempDir() + "/pebble"
	t.Cleanup(func() {
		if s := GetGlobalStore(); s != nil {
			_ = s.Close()
		}
		SetGlobalStore(nil)
	})

	first, err := InitializeStoreWith("pebble", dir, StandaloneOptions{})
	if err != nil {
		t.Fatalf("InitializeStoreWith: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	SetGlobalStore(nil)

	// The previous binary: a run written to opv2 with no opv3 record.
	now := time.Now().UTC().Truncate(time.Second)
	probe := newRunID()
	raw, err := NewPebbleStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rawSetOp(t, raw, OperationV2Row{ID: probe, DefID: "test.admit", Plugin: "test", Status: "failed", QueuedAt: now, CompletedAt: &now, Params: `{}`})
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	hasRecord := func() bool {
		t.Helper()
		p, err := NewPebbleStore(dir)
		if err != nil {
			t.Fatalf("the refused store was not closed, or cannot be opened: %v", err)
		}
		defer p.Close()
		r, err := p.GetRunV3(probe)
		if err != nil {
			t.Fatal(err)
		}
		return r != nil
	}
	if hasRecord() {
		t.Fatal("fixture: the probe run already has an opv3 record")
	}

	refusal := errors.New("this process may not run over this store")
	var asked Store
	_, err = InitializeStoreWith("pebble", dir, StandaloneOptions{Admit: func(s Store) error { asked = s; return refusal }})
	if !errors.Is(err, refusal) {
		t.Fatalf("a refused open returned %v, want the refusal", err)
	}
	if asked == nil {
		t.Error("Admit was not given the opened store")
	}
	if GetGlobalStore() != nil {
		t.Error("a refused store became the global store")
	}
	if hasRecord() {
		t.Error("the opener reconciled opv3 before the refusal: the store was written to by a process that was refused")
	}

	admitted, err := InitializeStoreWith("pebble", dir, StandaloneOptions{Admit: func(Store) error { return nil }})
	if err != nil {
		t.Fatalf("an admitted open: %v", err)
	}
	if r, err := admitted.(*PebbleStore).GetRunV3(probe); err != nil || r == nil {
		t.Errorf("an admitted open did not reconcile the probe run (record %v, %v): the probe cannot see the reconcile", r, err)
	}
}
