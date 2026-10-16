// file: internal/opscatalog/validate.go
// version: 1.0.0
// guid: cd6d1761-51b5-4917-971b-5ec67bb94bce
// last-edited: 2026-10-16

package opscatalog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The refusals this package adds to pkg/ops's (ops.ValidateCatalog).
const (
	// RefuseScheduledWriterNotLive is a native def with a Schedule and a
	// write set whose schedule does not say .Live() (owner decision D8).
	RefuseScheduledWriterNotLive ops.Refusal = "scheduled_writer_not_live"
	// RefuseLedgerIDUnresolved is an op-ID ledger line that resolves to no
	// def, alias or tombstone.
	RefuseLedgerIDUnresolved ops.Refusal = "ledger_id_unresolved"
)

// ParseLedger returns the IDs in an op_ids.golden body: one ID per line,
// surrounding space trimmed, blank lines and lines starting with "#" or
// "<!--" skipped.
func ParseLedger(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<!--") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// Universe is every op ID the process can resolve: live registry defs and
// aliases, the static plugin OperationDefs lists, native bundle defs, every
// FormerIDs entry of those, and Tombstones.
type Universe struct {
	ids map[string]bool
}

// BuildUniverse collects the IDs of live (the registry's defs), aliases (the
// registry's alias table, alias -> canonical), static (StaticPluginDefs) and
// bundles (Bundles with a zero Deps), with their FormerIDs, plus every
// tombstone.
func BuildUniverse(live []registry.OperationDef, aliases map[string]string,
	static [][]registry.OperationDef, bundles []ops.Bundle) Universe {
	u := Universe{ids: map[string]bool{}}
	addV2 := func(d registry.OperationDef) {
		u.ids[d.ID] = true
		for _, f := range d.FormerIDs {
			u.ids[f] = true
		}
	}
	for _, d := range live {
		addV2(d)
	}
	for old, canon := range aliases {
		u.ids[old] = true
		u.ids[canon] = true
	}
	for _, list := range static {
		for _, d := range list {
			addV2(d)
		}
	}
	for _, b := range bundles {
		for _, d := range b.Defs {
			u.addDef(d)
		}
	}
	for id := range Tombstones {
		u.ids[id] = true
	}
	return u
}

// addDef adds a native def, its FormerIDs and its pipeline stages' defs.
func (u Universe) addDef(d ops.Definition) {
	if d == nil {
		return
	}
	c := ops.Compile(d)
	u.ids[c.ID] = true
	for _, f := range c.Common.FormerIDs {
		u.ids[f] = true
	}
	for _, st := range c.Stages {
		u.addDef(st.Def)
	}
}

// Resolves reports whether id is in the universe.
func (u Universe) Resolves(id string) bool { return u.ids[id] }

// ResolveLedger returns the ledger IDs that resolve to nothing in u, sorted
// and without duplicates.
func ResolveLedger(u Universe, ledger []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ledger {
		if u.Resolves(id) || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ValidateCatalog refuses to start on any of:
//
//   - every ops.ValidateCatalog refusal: a constructor's programmer error
//     (empty ID or Title, a Task with no Run, ...), a duplicate ID (a def or
//     a FormerIDs entry claimed twice), an invalid schedule, a native def
//     with no declared Effects, a Fixer without Writes, a native def with an
//     empty Permission (adapted v2 defs are exempt until ported, D1),
//     Sequential with an empty reason, and a pipeline stage whose def is not
//     in the catalog;
//   - a scheduled writer: a native def with a Schedule and a write set whose
//     schedule does not say .Live() (D8: scheduled writers must declare
//     .Live(); pkg/ops has no explicit preview-note API, so .Live() is the
//     only way to pass);
//   - a ledger ID that resolves to no def, alias or tombstone in u.
//
// Two checks named in sdk-api.md §11 are not made here: an Unordered source
// with ResumeContinue and PartitionBy on a non-Snapshot source. A Batch's
// source order is only known once its Source has run, so the runner refuses
// both before the first item (pkg/ops/validate.go ValidateCatalog's doc).
//
// It returns every problem, joined (each an *ops.CatalogError), not just the
// first; nil when there is none.
func ValidateCatalog(defs []ops.Definition, u Universe, ledger []string) error {
	var errs []error
	if err := ops.ValidateCatalog(defs, nil); err != nil {
		errs = append(errs, err)
	}
	for _, d := range defs {
		c := ops.Compile(d)
		if c.Kind == ops.KindAdapted {
			continue
		}
		cm := c.Common
		if cm.Schedule == nil || cm.Effects.IsReadOnly() || len(cm.Effects.WriteSet()) == 0 {
			continue
		}
		if !cm.Schedule.IsLive() {
			errs = append(errs, &ops.CatalogError{DefID: c.ID, Refusal: RefuseScheduledWriterNotLive,
				Detail: fmt.Sprintf("schedule %q writes %v but does not say .Live() (D8)", cm.Schedule.String(), cm.Effects.WriteSet())})
		}
	}
	for _, id := range ResolveLedger(u, ledger) {
		errs = append(errs, &ops.CatalogError{DefID: id, Refusal: RefuseLedgerIDUnresolved,
			Detail: "the op-ID ledger (internal/server/testdata/op_ids.golden) lists it, and it resolves to no def, " +
				"FormerIDs alias or tombstone. Renamed it? Add it to the new def's FormerIDs. Retired it on purpose? " +
				"Add it to opscatalog.Tombstones with the reason"})
	}
	return errors.Join(errs...)
}
