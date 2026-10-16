// file: internal/server/op_catalog_gate_test.go
// version: 1.0.0
// guid: 8968804a-b144-44ee-8b87-f21d26b22b6b
// last-edited: 2026-10-16

package server

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/opscatalog"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// A ledger line that resolves to no def, alias or tombstone refuses startup,
// and the error names the ID.
func TestCatalogGate_FailsOnUnknownLedgerID(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	const bogus = "synthetic.never-registered-op"
	ledger := append(readOpIDLedger(t), bogus)

	err := srv.checkOpCatalog(ledger)
	if err == nil {
		t.Fatal("checkOpCatalog accepted a ledger ID that resolves to nothing")
	}
	var ce *ops.CatalogError
	if !errors.As(err, &ce) || ce.Refusal != opscatalog.RefuseLedgerIDUnresolved || ce.DefID != bogus {
		t.Fatalf("want a %s refusal for %q, got: %v", opscatalog.RefuseLedgerIDUnresolved, bogus, err)
	}
	if strings.Count(err.Error(), string(opscatalog.RefuseLedgerIDUnresolved)) != 1 {
		t.Fatalf("only the injected ID should be unresolved, got: %v", err)
	}
}

// The real boot (the helper TestOpIDs_NoRenameWithoutAlias uses) passes the
// gate with the embedded ledger.
func TestCatalogGate_PassesOnRealBoot(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	if err := srv.opCatalogGate(); err != nil {
		t.Fatalf("opCatalogGate refused a real boot: %v", err)
	}
}

// A server that registered none of the service-registry plugins' ops (no
// Deluge client, no dedup engine, iTunes off, ...) still passes: their IDs
// resolve through the static OperationDefs() lists.
func TestCatalogGate_PassesWithoutPluginPostInit(t *testing.T) {
	srv := bootServerForOpIDs(t)
	if err := srv.opCatalogGate(); err != nil {
		t.Fatalf("opCatalogGate refused a server without plugin ops: %v", err)
	}
}

// A Server with no registry cannot resolve anything and must not start.
func TestCatalogGate_NoRegistryRefuses(t *testing.T) {
	if err := (&Server{}).opCatalogGate(); err == nil {
		t.Fatal("opCatalogGate accepted a server with no operations registry")
	}
}
