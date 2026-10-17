// file: internal/server/write_op_modes_test.go
// version: 1.1.0
// guid: 96a36061-1106-45e7-8f9f-7bc530c6495e
// last-edited: 2026-10-17

package server

import (
	"bytes"
	"os"
	"sort"
	"testing"
)

const writeOpModesPath = "testdata/write_op_modes.golden"

// TestWriteOps_EveryWriteOpHasARecordedMode is the registry-level half of the
// owner's 2026-09-25 rule (every writing op previews unless told otherwise).
//
// The source scans in internal/operations/opmode catch the two shapes of a
// live default that have a params struct: a plain-bool dry_run field and a
// params value pre-filled live before decode. Neither can see an op that has
// no mode flag at all, and the registry cannot decode params generically. So
// this test makes the decision explicit instead: every registered op that can
// write must be listed in testdata/write_op_modes.golden as `preview` or
// `no-mode`. A new writing op fails here until its author records what `{}`
// does, and the ledger has no `live` class to record it under.
//
// Maintenance-job ops are skipped; TestMaintenanceJobs_PreviewByDefault in
// internal/maintenance/jobs checks each job's advertised default directly.
func TestWriteOps_EveryWriteOpHasARecordedMode(t *testing.T) {
	srv, _, _ := bootRegisteredOpIDs(t)
	ledger := readWriteOpModes(t)

	var unrecorded []string
	registered := map[string]bool{}
	for _, d := range srv.opRegistry.ActiveDefs() {
		if !canWrite(d) || isMaintenanceJobOp(d.ID) {
			continue
		}
		registered[d.ID] = true
		if _, ok := ledger[d.ID]; !ok {
			unrecorded = append(unrecorded, d.ID)
		}
	}
	sort.Strings(unrecorded)
	for _, id := range unrecorded {
		t.Errorf("%s can write but has no line in %s. Give it a preview mode (opmode.ResolveDryRun; omitted = preview) and record it as `preview`", id, writeOpModesPath)
	}
	var stale []string
	for id := range ledger {
		if !registered[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	for _, id := range stale {
		t.Errorf("%s lists %s, which is not a registered writing op; delete the line", writeOpModesPath, id)
	}
}

func readWriteOpModes(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(writeOpModesPath)
	if err != nil {
		t.Fatalf("open %s: %v", writeOpModesPath, err)
	}
	out, err := parseWriteOpModes(raw)
	if err != nil {
		t.Fatalf("%s: %v", writeOpModesPath, err)
	}
	return out
}

// TestWriteOpModes_EmbeddedLedgerIsTheFile: the copy /api/v3/ops reads at
// runtime is the file this test checks, and it parses.
func TestWriteOpModes_EmbeddedLedgerIsTheFile(t *testing.T) {
	raw, err := os.ReadFile(writeOpModesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, writeOpModesLedger) {
		t.Fatal("embedded ledger differs from the file")
	}
	if _, err := parseWriteOpModes(writeOpModesLedger); err != nil {
		t.Fatal(err)
	}
	if len(recordedWriteOpModes()) == 0 {
		t.Fatal("embedded ledger parsed to nothing")
	}
}
