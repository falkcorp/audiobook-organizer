// file: internal/maintenance/jobs/op_writes_test.go
// version: 1.2.0
// guid: 8e3b5f21-6d94-4c07-b1a8-2f7c9e0d4a53
// last-edited: 2026-11-18

package jobs

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// readOnlyJobReasons is every maintenance job that may declare no Writes, with
// why. A job is added here only after reading its Run: a writer left off the
// gate is how MAINTENANCE-JOB-OPS-REGISTER-WITHOUT-WRITES happened. The test
// below fails when a job outside this list has no Writes, and when a job in it
// has gained some, so the list cannot go stale in either direction.
var readOnlyJobReasons = map[string]string{
	"relink-report":           "reports title/author relink candidates and writes no row",
	"scan-chapter-groups":     "reports chapter-group candidates as its result and writes no row",
	"scan-duplicate-files":    "reports duplicate file groups as its result and writes no row",
	"scan-duration-mismatch":  "reports books whose stored duration disagrees with their files and writes no row",
	"scan-metadata-hash-dups": "reports books sharing a metadata hash and writes no row",
}

// opWrites is the write set a maintenance job's OperationDef would declare in
// OperationDef.Writes, so the dispatcher's write-set gate (Gate 3b) could see
// the job. It is the same set the job's tasks.Effects carries (effectsFor):
// derived from the store write methods the job's code reaches (jobWritesFor),
// never typed per job. The names are the ops.Resource names, which the
// registry's own v3 adapter casts to opsregistry.Resource one for one.
//
// It lives in this test file, not in production code, on purpose: the one
// production caller would be registerMaintenanceJobOp (internal/server/
// maintenance_job_op.go), and until that is wired an exported accessor is a
// function nothing reaches, which the deadcode ratchet rejects. The change that
// wires Writes exports it (see the TODO item). An unknown job is an error, not an
// empty set, so a writer cannot pass for a read-only job by being missing here.
func opWrites(jobID string) ([]opsregistry.Resource, error) {
	all, err := jobWritesFor()
	if err != nil {
		return nil, fmt.Errorf("maintenance job %s: derive write set: %w", jobID, err)
	}
	ws, ok := all[jobID]
	if !ok {
		return nil, fmt.Errorf("maintenance job %s has no write-set trace (kinds_writes.go jobTraces)", jobID)
	}
	out := make([]opsregistry.Resource, len(ws))
	for i, w := range ws {
		out[i] = opsregistry.Resource(w)
	}
	return out, nil
}

// opWritesFitOperationDef is a compile-time proof that opWrites returns exactly
// the type OperationDef.Writes holds, so wiring it into registerMaintenanceJobOp
// is a one-field change with no conversion.
var _ = func() opsregistry.OperationDef {
	writes, _ := opWrites("cleanup-series")
	return opsregistry.OperationDef{Writes: writes}
}

// TestOpWrites_EveryRegisteredJobDeclaresWritesOrIsReadOnly: no registered
// maintenance job can reach Gate 3b with an empty Writes unless it is a listed,
// reasoned read-only job.
func TestOpWrites_EveryRegisteredJobDeclaresWritesOrIsReadOnly(t *testing.T) {
	registered := map[string]bool{}
	for _, job := range maintenance.All() {
		id := job.ID()
		registered[id] = true
		ws, err := opWrites(id)
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		reason, readOnly := readOnlyJobReasons[id]
		switch {
		case len(ws) == 0 && !readOnly:
			t.Errorf("%s declares no Writes and is not a listed read-only job: the write-set gate would never see it", id)
		case len(ws) > 0 && readOnly:
			t.Errorf("%s is listed read-only (%s) but writes %v: remove it from readOnlyJobReasons", id, reason, ws)
		}
		if readOnly && strings.TrimSpace(reason) == "" {
			t.Errorf("%s is listed read-only with no reason", id)
		}
	}
	for id := range readOnlyJobReasons {
		if !registered[id] {
			t.Errorf("readOnlyJobReasons lists %q, which is no registered maintenance job", id)
		}
	}
}

// TestOpWrites_IsTheDerivedSetAndSorted: the OperationDef set is exactly the
// Effects set, name for name, in a stable order, and a job the table does not
// know is an error rather than an empty set.
func TestOpWrites_IsTheDerivedSetAndSorted(t *testing.T) {
	derived, err := jobWritesFor()
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range maintenance.All() {
		got, err := opWrites(job.ID())
		if err != nil {
			t.Fatal(err)
		}
		want := derived[job.ID()]
		if len(got) != len(want) {
			t.Errorf("%s: opWrites has %d names, derived set has %d", job.ID(), len(got), len(want))
			continue
		}
		for i := range want {
			if string(got[i]) != string(want[i]) {
				t.Errorf("%s: OpWrites[%d] = %q, derived %q", job.ID(), i, got[i], want[i])
			}
		}
		if !slices.IsSorted(got) {
			t.Errorf("%s: opWrites is not sorted: %v", job.ID(), got)
		}
	}
	if ws, err := opWrites("not-a-job"); err == nil {
		t.Errorf("an unknown job got a write set %v instead of an error", ws)
	}
}

// TestOpWrites_SeriesAndBooksWritersSeeTheSeriesOps: the jobs the TODO names as
// able to run beside the series ops declare the resources those ops declare.
func TestOpWrites_SeriesAndBooksWritersSeeTheSeriesOps(t *testing.T) {
	for _, tc := range []struct {
		job  string
		want []opsregistry.Resource
	}{
		{"cleanup-series", []opsregistry.Resource{opsregistry.ResSeries, opsregistry.ResBooks}},
		{"fix-read-by-narrator", []opsregistry.Resource{opsregistry.ResBooks}},
	} {
		got, err := opWrites(tc.job)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range tc.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: opWrites %v does not contain %q", tc.job, got, w)
			}
		}
	}
}
