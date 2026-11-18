// file: internal/books/cutover/kindrename/rules.go
// version: 1.0.0
// guid: 7c41e8a2-5d0b-4936-a8f3-2e9d6b1c0f74
// last-edited: 2026-11-18

// Package kindrename is the kind-rename map of the R2 tasks cutover
// (05-tasks §5T.8.2): HEAD's stored vocabulary (operation definition IDs,
// scheduler task names) to the task kinds the native task engine registers.
// M4 (internal/books/cutover/m4) applies it once to every stored reference,
// and so does the activity-feed rewrite beside it. It is books code; core
// holds none of it (03 R1(c)).
//
// The map is generated (map_gen.go) from HEAD's op-ID ledger, the booted
// registry's alias table, opscatalog.Tombstones and the scheduler's
// taskV2DefIDs, by the program in ./gen (go generate); this file holds
// everything the generator and the step share by hand: the rules'
// constants, the one table the generator cannot derive (the T4 core kinds),
// the run-level rules, and the lookups. The package imports core and the
// standard library only, so it is a layer-0 leaf.
//
//go:generate go run ./gen/cmd
package kindrename

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/tasks"
)

// Rule is the rule of 05-tasks §5T.8.2 that decided a target (first match
// wins).
type Rule uint8

// The rules, numbered as the spec numbers them.
const (
	// RuleCore: a HEAD def ID in the T4 table maps to a core kind.
	RuleCore Rule = 1 + iota
	// RuleRepairs: repairs.plan and repairs.apply map to
	// books.repairs.<fixer_id>, a run-level rule (MarkerRepairsPlan,
	// MarkerRepairsApply).
	RuleRepairs
	// RuleAlias: a FormerIDs alias maps where its canonical ID maps.
	RuleAlias
	// RuleBooks: every other registered HEAD def ID <id> maps to books.<id>.
	RuleBooks
	// RuleRetired: a tombstoned ID, the IDs the flip deletes and any other
	// well-formed ID found in a store map to books.retired.<id>: history
	// only, never registered.
	RuleRetired
	// RuleInvalid: an ID that fails the kind grammar. Its run is quarantined.
	RuleInvalid
)

func (r Rule) String() string {
	switch r {
	case RuleCore:
		return "1 (core kind)"
	case RuleRepairs:
		return "2 (repairs)"
	case RuleAlias:
		return "3 (alias)"
	case RuleBooks:
		return "4 (books kind)"
	case RuleRetired:
		return "5 (retired)"
	case RuleInvalid:
		return "6 (invalid)"
	default:
		return fmt.Sprintf("rule(%d)", uint8(r))
	}
}

// Marker names a run-level rule: the map cannot decide the kind from the ID
// alone, so the entry carries a marker and Target.ForRun applies it to the
// run's own params.
type Marker uint8

const (
	// MarkerNone: the entry's Kind is the answer.
	MarkerNone Marker = iota
	// MarkerRepairsPlan: books.repairs.<fixer_id> from the run's fixer_id,
	// run as ModePreview.
	MarkerRepairsPlan
	// MarkerRepairsApply: books.repairs.<fixer_id> from the run's fixer_id,
	// run as ModeLive with PlanRef the plan run its plan_op_id names.
	MarkerRepairsApply
	// MarkerLibraryScan: books.library.scan-full for a run whose params are
	// the full sweep's, books.library.scan otherwise.
	MarkerLibraryScan
)

// Target is what a HEAD def ID maps to.
type Target struct {
	// Kind is the kind the ID maps to; empty when Marker is a repairs marker
	// (the fixer ID names it per run) and the scan's non-full kind for
	// MarkerLibraryScan (ForRun picks the full sweep's).
	Kind tasks.Kind
	// Rule is the rule that produced the entry. An alias entry is RuleAlias
	// with the Kind and Marker of the ID it aliases.
	Rule   Rule
	Marker Marker
}

// The kinds and IDs the rules and the run-level rules name.
const (
	// RepairsPlanID and RepairsApplyID are HEAD's two repairs definitions,
	// replaced by one books.repairs.<fixer_id> kind per fixer (rule 2).
	RepairsPlanID  = "repairs.plan"
	RepairsApplyID = "repairs.apply"

	// RepairsKindPrefix is the kind namespace of the fixers; the fixer ID
	// follows it.
	RepairsKindPrefix = "books.repairs."

	// LibraryScanID is the one HEAD def ID whose runs split (rule 4).
	LibraryScanID = "library.scan"
	// ScanKind and FullSweepKind are the two kinds a library.scan run maps
	// to; one schedule stands per kind (05-tasks §5T.8.2).
	ScanKind      tasks.Kind = "books.library.scan"
	FullSweepKind tasks.Kind = "books.library.scan-full"

	// RetiredKindPrefix is the namespace of history-only kinds (rule 5).
	RetiredKindPrefix = "books.retired."

	// FullSweepTask is the scheduler task whose def ID is library.scan but
	// whose clock belongs to the full sweep's kind.
	FullSweepTask = "library_scan_full"
	// BatchPollerTask is the inline scheduler task with no def ID; its clock
	// is core.ai.batch-poll's.
	BatchPollerTask = "batch_poller"
	// TranscodeTask fires nothing; its clock is dropped.
	TranscodeTask = "transcode"

	// BatchPollKind is core.ai.batch-poll, the kind that takes
	// batch_poller's clock (05-tasks §5T.8.2).
	BatchPollKind tasks.Kind = "core.ai.batch-poll"
)

// CoreKinds is the T4 table (05-tasks §5T.8.2, "HEAD's generic ops: which
// become core kinds"): the HEAD def IDs that become core kinds, rule 1. It is
// the one input the generator cannot derive, so it is written here, and the
// generator reads it from here.
//
// The returned map is a copy.
func CoreKinds() map[string]tasks.Kind {
	return map[string]tasks.Kind{
		"maintenance.db-optimize":     "core.storage.compact",
		"maintenance.db-census-exact": "core.storage.census",
		"maintenance.purge-old-logs":  "core.log.journal.prune",
		"maintenance.throttle-reset":  "core.integrations.release-holds",
		"diagnostics.export":          "core.diagnostics.export",
		"maintenance.window":          "core.window.nightly",
	}
}

// FlipRetired lists the registered HEAD def IDs the tasks flip deletes
// outright: they map by rule 5 although a registry still reports them
// (05-tasks §5T.8.2 rule 5).
func FlipRetired() []string {
	return []string{"operations.backfill-legacy-status"}
}

// ErrInvalidKind is wrapped by every refusal of rule 6: the ID or the param
// that names the kind fails the kind grammar, so the run is quarantined.
var ErrInvalidKind = errors.New("kindrename: not a valid kind")

// Lookup returns the generated entry for a HEAD def ID. ok is false for an ID
// the ledger and the registry never knew; Resolve applies rules 5 and 6 to it.
func Lookup(defID string) (Target, bool) {
	t, ok := defTargets[defID]
	return t, ok
}

// Resolve applies rules 1-6 to a HEAD def ID: the generated entry when there
// is one, else rule 5 (a well-formed ID is kept as books.retired.<id>), else
// rule 6 (an error wrapping ErrInvalidKind: quarantine the run).
func Resolve(defID string) (Target, error) {
	if t, ok := defTargets[defID]; ok {
		return t, nil
	}
	k, err := retiredKind(defID)
	if err != nil {
		return Target{Rule: RuleInvalid}, err
	}
	return Target{Kind: k, Rule: RuleRetired}, nil
}

// RetiredKind is rule 5's kind for a HEAD def ID: books.retired.<id>, or an
// error wrapping ErrInvalidKind when the result fails the kind grammar.
func RetiredKind(defID string) (tasks.Kind, error) { return retiredKind(defID) }

func retiredKind(defID string) (tasks.Kind, error) {
	k, err := tasks.ParseKind(RetiredKindPrefix + defID)
	if err != nil {
		return "", fmt.Errorf("%w: def ID %q: %v", ErrInvalidKind, defID, err)
	}
	return k, nil
}

// RunTarget is a run's kind, mode and plan reference once a Target's
// run-level rule has been applied to the run's params.
type RunTarget struct {
	Kind tasks.Kind
	// Mode is ModePreview for a repairs plan and for a run whose params ask
	// for a dry run (HEAD writes dry_run back resolved), ModeLive otherwise
	// (a repairs apply is always Live).
	Mode tasks.Mode
	// PlanRef is the plan run a repairs apply names; empty otherwise.
	PlanRef tasks.RunID
}

// runParams are the params keys the run-level rules read.
type runParams struct {
	FixerID        *string `json:"fixer_id"`
	PlanOpID       *string `json:"plan_op_id"`
	DryRun         *bool   `json:"dry_run"`
	DryRunCamel    *bool   `json:"dryRun"`
	ForceUpdate    *bool   `json:"force_update"`
	IncludeRootDir *bool   `json:"include_root_dir"`
}

// ForRun applies the target's run-level rule, if it has one, to the run's
// stored params. A run whose params cannot be decoded keeps the entry's own
// answer where there is one (the full-sweep test then reads false and the
// dry-run test reads live) and is refused where the rule needs a param (a
// repairs run with no usable fixer_id), with an error wrapping
// ErrInvalidKind.
func (t Target) ForRun(params json.RawMessage) (RunTarget, error) {
	var p runParams
	decoded := len(params) == 0 || json.Unmarshal(params, &p) == nil
	rt := RunTarget{Kind: t.Kind, Mode: tasks.ModeLive}
	if p.dryRun() {
		rt.Mode = tasks.ModePreview
	}
	switch t.Marker {
	case MarkerRepairsPlan, MarkerRepairsApply:
		if !decoded || p.FixerID == nil {
			return RunTarget{}, fmt.Errorf("%w: a %s run without a readable fixer_id", ErrInvalidKind, repairsName(t.Marker))
		}
		k, err := tasks.ParseKind(RepairsKindPrefix + *p.FixerID)
		if err != nil {
			return RunTarget{}, fmt.Errorf("%w: fixer_id %q: %v", ErrInvalidKind, *p.FixerID, err)
		}
		rt.Kind = k
		if t.Marker == MarkerRepairsPlan {
			rt.Mode = tasks.ModePreview
		} else {
			rt.Mode = tasks.ModeLive
			if p.PlanOpID != nil {
				rt.PlanRef = tasks.RunID(*p.PlanOpID)
			}
		}
	case MarkerLibraryScan:
		rt.Kind = ScanKind
		if decoded && p.fullSweep() {
			rt.Kind = FullSweepKind
		}
	}
	return rt, nil
}

func repairsName(m Marker) string {
	if m == MarkerRepairsPlan {
		return RepairsPlanID
	}
	return RepairsApplyID
}

func (p runParams) dryRun() bool {
	return (p.DryRun != nil && *p.DryRun) || (p.DryRunCamel != nil && *p.DryRunCamel)
}

// fullSweep is the params HEAD's library_scan_full task enqueues: force_update
// and include_root_dir both true, whoever started the run.
func (p runParams) fullSweep() bool {
	return p.ForceUpdate != nil && *p.ForceUpdate && p.IncludeRootDir != nil && *p.IncludeRootDir
}

// SchedulerTarget is where a scheduler task name's clock goes.
type SchedulerTarget struct {
	// Kind is the kind the task fires; empty when Dropped.
	Kind tasks.Kind
	// Dropped: the task fires nothing (transcode), so its clock is dropped.
	Dropped bool
}

// SchedulerKind maps a scheduler task name through taskV2DefIDs, then rule 1
// or 4, with the exceptions of 05-tasks §5T.8.2: batch_poller to
// core.ai.batch-poll, transcode dropped, library_scan_full to
// books.library.scan-full. ok is false for a name the scheduler never had.
func SchedulerKind(task string) (SchedulerTarget, bool) {
	t, ok := schedulerTargets[task]
	return t, ok
}
