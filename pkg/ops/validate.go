// file: pkg/ops/validate.go
// version: 1.0.0
// guid: bf143a3a-27aa-4c76-b9e2-aefc761d798d
// last-edited: 2026-10-13

package ops

import (
	"errors"
	"fmt"
	"strings"
)

// Refusal names one ValidateCatalog check.
type Refusal string

// The refusals ValidateCatalog reports.
const (
	RefuseInvalid            Refusal = "invalid"           // a constructor recorded a programmer error
	RefuseDuplicateID        Refusal = "duplicate_id"      // two defs (or a def and a former id) share an id
	RefuseSchedule           Refusal = "invalid_schedule"  // a Schedule that does not validate
	RefuseNoEffects          Refusal = "no_effects"        // neither ReadOnly nor Writes was declared
	RefuseFixerReadOnly      Refusal = "fixer_read_only"   // a Fixer without Writes
	RefuseNoPermission       Refusal = "no_permission"     // a native def with an empty Permission
	RefuseSequentialNoReason Refusal = "sequential_no_why" // Concurrency: Sequential("")
	RefuseStageNotInCatalog  Refusal = "stage_not_in_catalog"
)

// CatalogError is one refusal for one def.
type CatalogError struct {
	DefID   string
	Refusal Refusal
	Detail  string
}

func (e *CatalogError) Error() string {
	return fmt.Sprintf("ops catalog: %s: %s: %s", e.DefID, e.Refusal, e.Detail)
}

// ErrLedgerCheckPending is returned when ValidateCatalog is given op-id
// ledger entries: checking them against the catalog (every id resolves to a
// def, an alias or a tombstone) is 05-PR7's, which embeds op_ids.golden.
var ErrLedgerCheckPending = errors.New("ops catalog: the op-id ledger check is not wired yet (05-PR7)")

// ValidateCatalog checks a whole catalog at startup and returns every
// refusal joined (each a *CatalogError), or nil.
//
// Checks that need a run's Source -- an Unordered source with
// ResumeContinue, PartitionBy on a non-Snapshot source -- cannot be made
// here, because Order is only known once Source has run; the runner refuses
// both before the first item. The D8 rule (a scheduled writer states .Live()
// or a preview note) is 05-PR8's, and refusing a native def that has
// non-terminal migrated runs without a LegacyResume needs the store, so it is
// the catalog wiring's (05-PR7).
func ValidateCatalog(defs []Definition, ledgerIDs []string) error {
	var errs []error
	refuse := func(id string, r Refusal, detail string) {
		errs = append(errs, &CatalogError{DefID: id, Refusal: r, Detail: detail})
	}
	ids := map[string]string{} // id or former id -> owning def id
	claim := func(id, owner string) {
		if prev, ok := ids[id]; ok {
			refuse(owner, RefuseDuplicateID, fmt.Sprintf("%q is already claimed by %s", id, prev))
			return
		}
		ids[id] = owner
	}
	for _, d := range defs {
		s := d.spec()
		claim(s.id, s.id)
		for _, f := range s.common.FormerIDs {
			claim(f, s.id)
		}
	}
	for _, d := range defs {
		s := d.spec()
		c := s.common
		if len(s.problems) > 0 {
			refuse(s.id, RefuseInvalid, strings.Join(s.problems, "; "))
		}
		if c.Schedule != nil && c.Schedule.err != nil {
			refuse(s.id, RefuseSchedule, c.Schedule.err.Error())
		}
		if s.kind == KindAdapted {
			// An unchanged v2 def: an empty permission list means
			// settings.manage on the trigger route (08 X2) until it is ported.
			continue
		}
		if !c.Effects.Declared() {
			refuse(s.id, RefuseNoEffects, "declare ReadOnly(...) or Writes(...)")
		}
		if s.kind == KindFixer && (c.Effects.IsReadOnly() || len(c.Effects.WriteSet()) == 0) {
			refuse(s.id, RefuseFixerReadOnly, "a Fixer must declare Writes(...)")
		}
		if c.Permission == "" {
			refuse(s.id, RefuseNoPermission, "a native def must name its Permission")
		}
		if s.conc.IsSequential() && strings.TrimSpace(s.conc.Reason()) == "" {
			refuse(s.id, RefuseSequentialNoReason, `Sequential("") must say why`)
		}
		for _, st := range s.stages {
			if st.Def == nil {
				continue
			}
			if _, ok := ids[st.Def.ID()]; !ok {
				refuse(s.id, RefuseStageNotInCatalog, fmt.Sprintf("stage %s runs %s, which is not in the catalog", st.Name, st.Def.ID()))
			}
		}
	}
	if len(ledgerIDs) > 0 {
		errs = append(errs, ErrLedgerCheckPending)
	}
	return errors.Join(errs...)
}
