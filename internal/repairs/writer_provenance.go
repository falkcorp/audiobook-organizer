// file: internal/repairs/writer_provenance.go
// version: 1.0.0
// guid: 037e6aad-9346-4c8e-8645-c04ac0954b1f
// last-edited: 2026-10-29

package repairs

// ApplyProvenance is who and what a repairs.apply run is acting for: the plan
// op it applies and the user whose request enqueued the apply op. A fixer that
// writes a decision record for a row it applied (the retired-versions fixer)
// reads it from its Writer, as it reads OpID, so the record names the plan and
// the person instead of reading as an automatic one.
//
// The zero value is a run with no plan op and no actor: a system-enqueued
// apply, or a Writer used outside RunApply. A record written under it is
// automatic.
type ApplyProvenance struct {
	// PlanOpID is the repairs.plan op the apply's rows came from.
	PlanOpID string
	// ActorUserID is the user the apply op was enqueued for ("" when it was
	// enqueued by no user), ActorName their display name when known.
	ActorUserID, ActorName string
}

// Provenance is the plan and actor of the apply run this Writer is in. RunApply
// sets it for the run and clears it after, so it is the zero ApplyProvenance
// outside one. Safe for concurrent use: the partition workers read it.
func (w *Writer) Provenance() ApplyProvenance {
	if p := w.provenance.Load(); p != nil {
		return *p
	}
	return ApplyProvenance{}
}

func (w *Writer) setProvenance(p ApplyProvenance) { w.provenance.Store(&p) }
