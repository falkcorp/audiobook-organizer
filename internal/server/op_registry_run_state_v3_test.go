// file: internal/server/op_registry_run_state_v3_test.go
// version: 1.0.1
// guid: 7be5d482-a71c-4572-86c3-af4eaa876d95
// last-edited: 2026-10-12

package server

// NewServer builds the operations registry over the search-index decorator,
// and the registry resolves its ops v3 run state through that decorator's
// Unwrap. These tests pin the end-to-end resolution on a real Pebble server,
// the lenient path a mock-store server keeps, and the gate NewServer uses to
// refuse a Pebble server whose registry missed it.

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

func TestNewServer_PebbleOpRegistryHasRunStateV3(t *testing.T) {
	srv, cleanup := setupTestServer(t)
	defer cleanup()

	reg := srv.OpRegistry()
	if reg == nil {
		t.Fatal("NewServer on a Pebble store left the operations registry nil")
	}
	if !reg.HasRunStateV3() {
		t.Fatal("NewServer's operations registry did not resolve the ops v3 run state through the search-index decorator; " +
			"every v3 path (stopping runs, boot sweep, requeue successors) would act as if no run had a record")
	}
}

func TestNewServer_MockStoreOpRegistryHasNoRunStateV3(t *testing.T) {
	// A mock store carries no v3 surface and no *PebbleStore, so NewServer
	// must still construct (no exit) and the registry runs v2-only.
	srv, cleanup := setupTestServerWithStore(t, &database.MockStore{})
	defer cleanup()

	reg := srv.OpRegistry()
	if reg == nil {
		t.Fatal("NewServer on a mock store left the operations registry nil")
	}
	if reg.HasRunStateV3() {
		t.Fatal("a mock store has no ops v3 run state, yet the registry reports one")
	}
}

// opaqueStore embeds database.Store and offers no Unwrap, so it hides both
// the v3 surface and the *PebbleStore beneath it.
type opaqueStore struct {
	database.Store
}

func TestOpRegistryRunStateGate(t *testing.T) {
	pebble, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "gate.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pebble.Close() })

	withV3 := opsregistry.New(&indexedStore{Store: pebble}, slog.Default(), 1, nil)
	if !withV3.HasRunStateV3() {
		t.Fatal("setup: a registry over indexedStore{Pebble} did not resolve the v3 run state")
	}
	// Negative control: the opaque decorator hides the run state, so this
	// registry is what a Pebble server with a broken wrapper would get.
	withoutV3 := opsregistry.New(opaqueStore{Store: pebble}, slog.Default(), 1, nil)
	if withoutV3.HasRunStateV3() {
		t.Fatal("setup: a registry over an opaque decorator resolved the v3 run state; the negative case proves nothing")
	}

	// The gate's inputs as NewServer computes them: the *PebbleStore resolved
	// from the bare store, and the registry. A registry built over the opaque
	// decorator while the bare store is Pebble is the broken-wrapper case.
	ps := database.AsPebbleStore(pebble)
	if ps == nil {
		t.Fatal("setup: AsPebbleStore did not resolve the bare Pebble store")
	}
	if err := opRegistryRunStateGate(ps, withoutV3); err == nil {
		t.Error("gate passed a Pebble-backed server whose registry has no ops v3 run state; it must refuse")
	}
	if err := opRegistryRunStateGate(ps, withV3); err != nil {
		t.Errorf("gate refused a Pebble-backed server whose registry has the run state: %v", err)
	}
	// A mock store resolves to no *PebbleStore, which is the lenient path.
	mockPS := database.AsPebbleStore(&database.MockStore{})
	if mockPS != nil {
		t.Fatal("setup: AsPebbleStore resolved a *PebbleStore from a MockStore")
	}
	if err := opRegistryRunStateGate(mockPS, withoutV3); err != nil {
		t.Errorf("gate refused a mock-store server; mock stores keep the lenient v2-only path: %v", err)
	}
	if err := opRegistryRunStateGate(ps, nil); err != nil {
		t.Errorf("gate refused a server with no operations registry: %v", err)
	}
}
