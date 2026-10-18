// file: internal/server/write_op_modes_test.go
// version: 1.2.0
// guid: 96a36061-1106-45e7-8f9f-7bc530c6495e
// last-edited: 2026-10-18

package server

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
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

// isMaintenanceJobOp: maintenance.<job id> for a registered job. Those ops
// are not in the ledger; TestMaintenanceJobs_PreviewByDefault checks that
// every one previews by default.
func isMaintenanceJobOp(id string) bool {
	jobID, ok := strings.CutPrefix(id, "maintenance.")
	if !ok {
		return false
	}
	j, err := maintenance.Get(jobID)
	return err == nil && j != nil
}

// parseWriteOpModes reads the ledger format: `<op id><TAB><class>` lines,
// `#` comments and blank lines. A malformed line, an unknown class or a
// duplicate id is an error, never skipped.
func parseWriteOpModes(b []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, class, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, fmt.Errorf("malformed line %q (want <op id><TAB><class>)", line)
		}
		switch class {
		case "preview", "no-mode":
		default:
			return nil, fmt.Errorf("%s has class %q; allowed: preview, no-mode", id, class)
		}
		if _, dup := out[id]; dup {
			return nil, fmt.Errorf("%s listed twice", id)
		}
		out[id] = class
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
