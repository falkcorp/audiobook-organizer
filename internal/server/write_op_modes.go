// file: internal/server/write_op_modes.go
// version: 1.0.0
// guid: 46bd373a-4c1e-47b0-9864-2839b2bd44b7
// last-edited: 2026-10-17

package server

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// writeOpModesLedger is testdata/write_op_modes.golden: what `{}` does for
// every registered v2 op that can write, `preview` or `no-mode`. Nothing on
// OperationDef says whether an op reads dry_run, so this hand-kept ledger is
// the only record of it. TestWriteOps_EveryWriteOpHasARecordedMode keeps it
// complete; /api/v3/ops reads it to refuse a "preview" of an op that would
// ignore dry_run and write (wire_ops_v3_routes.go).
//
//go:embed testdata/write_op_modes.golden
var writeOpModesLedger []byte

// Ledger classes.
const (
	writeOpModePreview = "preview"
	writeOpModeNoMode  = "no-mode"
)

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
		case writeOpModePreview, writeOpModeNoMode:
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

var (
	writeOpModesOnce sync.Once
	writeOpModesMap  map[string]string
)

// recordedWriteOpModes is the embedded ledger, parsed once. A ledger that does
// not parse yields an empty map, which makes every adapted writing op read as
// having no preview: the API then demands an explicit live mode, the safe
// side. The ledger's own test fails on the same input, so this never ships.
func recordedWriteOpModes() map[string]string {
	writeOpModesOnce.Do(func() {
		m, err := parseWriteOpModes(writeOpModesLedger)
		if err != nil {
			m = map[string]string{}
		}
		writeOpModesMap = m
	})
	return writeOpModesMap
}

// canWrite: declares a write capability, or declares none (undeclared is not
// evidence of read-only).
func canWrite(d opsregistry.OperationDef) bool {
	if len(d.Capabilities) == 0 {
		return true
	}
	for _, c := range d.Capabilities {
		switch c {
		case opsregistry.CapLibraryWrite, opsregistry.CapFilesWrite, opsregistry.CapDBMigrate:
			return true
		}
	}
	return false
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

// v2PreviewMode classifies an unchanged v2 op: whether it can write at all,
// and if so whether it honours dry_run. A writing op that is neither a
// maintenance job nor recorded `preview` has no preview: `no-mode`, and an op
// missing from the ledger (which its test forbids) are treated alike, so
// nothing unproven is ever called a preview.
func v2PreviewMode(d opsregistry.OperationDef) (writer, hasPreview bool) {
	if !canWrite(d) {
		return false, false
	}
	if isMaintenanceJobOp(d.ID) {
		return true, true
	}
	return true, recordedWriteOpModes()[d.ID] == writeOpModePreview
}
