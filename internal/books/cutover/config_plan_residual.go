// file: internal/books/cutover/config_plan_residual.go
// version: 1.0.0
// guid: f88edc09-9c1d-47b6-95d8-894908b1ab26
// last-edited: 2026-11-19

package cutover

import (
	"fmt"

	"github.com/falkcorp/mediacore/secrets"
)

// BlobResidual is what M2a leaves of a settings blob, and the secrets it moves
// out of it into core's secrets store.
type BlobResidual struct {
	// Row is the blob's settings row as M2a writes it back: the class-i members
	// M6 reads, and nothing else.
	Row []byte
	// Secrets are the secrets M2a imports from the blob, by the name it gives
	// each (a credential key that is no valid name segment is quarantined, not
	// imported, so it is absent here).
	Secrets map[secrets.Name]string
	// Quarantined are the quarantine keys of the blob members M2a quarantines
	// (setting:config_blob#<member>; never their values).
	Quarantined []string
}

// PlanBlobResidual runs M2a's planner over one stored settings-blob row alone:
// no encrypted setting rows, no config.yaml, no key. It is the seam the step
// that follows M2 (M6, F7-08) tests its inputs through, so that what it is
// handed is what M2's own planner produces, with M2's secret names, and not a
// copy of them. It does no I/O and writes nothing.
func PlanBlobResidual(blobRow []byte) (BlobResidual, error) {
	p, err := buildPlan(&m2Input{Blob: blobRow, Rows: map[string]*rowState{}})
	if err != nil {
		return BlobResidual{}, fmt.Errorf("cutover: plan the settings blob: %w", err)
	}
	out := BlobResidual{Row: p.BlobNew, Secrets: map[secrets.Name]string{}}
	for _, s := range p.Secrets {
		out.Secrets[s.Name] = s.Value
	}
	for _, q := range p.Quarantines {
		out.Quarantined = append(out.Quarantined, string(q.Key))
	}
	return out, nil
}
