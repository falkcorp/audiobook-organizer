// file: internal/maintenance/jobs/op_writes.go
// version: 1.0.0
// guid: 3f7a1c58-9e24-4b6d-a0c3-5d8e2b7f1a94
// last-edited: 2026-11-18

package jobs

import (
	"fmt"

	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// OpWrites is the write set a maintenance job's OperationDef declares in
// OperationDef.Writes, so the dispatcher's write-set gate (Gate 3b) can see the
// job. It is the same set the job's tasks.Effects carries (effectsFor): derived
// from the store write methods the job's code reaches (jobWritesFor), never typed
// per job.
//
// The names are the ops.Resource names, which the registry's own v3 adapter casts
// to opsregistry.Resource one for one (a name only conflicts with another
// operation that declares the same name). An empty result is a job that writes
// nothing; a job missing from the traces is an error, not an empty set, so a
// caller cannot register a writer with no Writes by asking for a job this table
// does not know.
func OpWrites(jobID string) ([]opsregistry.Resource, error) {
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
