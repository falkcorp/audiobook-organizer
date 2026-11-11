// file: internal/books/cutover/cutoverlist.go
// version: 1.0.0
// guid: 40719d00-78c0-4106-a0ca-975c1aa4405a
// last-edited: 2026-11-11

package cutover

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// The cutover-list check (05-storage §5S.3.13 "The list"; F3-04's "Flip
// mechanism"; the order M1, M7, M2, M3, M4, M5, M6, M8 of 03 §3.8). The list
// is registered only in F3-07's steps.go, which builds one CutoverList from
// books' manifest list and the kernel's core half and runs ValidateCutoverList
// over it; this file is the check, tested over lists built from this
// package's constructors.
//
// A flip is one release's group of cutover steps: the level steps of every
// owner whose level it raises, then the data steps they guard. The list is
// valid when
//
//   - every owner the build raises (Target above 0) has level steps whose
//     levels strictly increase and end at Target, and no owner has one above
//     Target;
//   - each owner's level steps have contiguous versions (and the first is
//     FirstVersion's, when the caller knows it);
//   - every data step is a cutover step (Cutover, Breaking, RaisesLevel above
//     0) whose owner's level step for its RaisesLevel comes before it in the
//     list and is named in its After, and every run of level steps is
//     followed by a data step it guards;
//   - exactly one M8, RewriteID, owned by books, is registered, and it is the
//     last entry.

// ErrInvalidCutoverList is what every CutoverListError matches
// (errors.Is).
var ErrInvalidCutoverList = errors.New("cutover: invalid cutover list")

// CutoverListError lists every problem ValidateCutoverList found, so one run
// names them all.
type CutoverListError struct {
	Problems []string
}

func (e *CutoverListError) Error() string {
	return fmt.Sprintf("%v: %s", ErrInvalidCutoverList, strings.Join(e.Problems, "; "))
}

// Is matches ErrInvalidCutoverList.
func (e *CutoverListError) Is(target error) bool { return target == ErrInvalidCutoverList }

type entryKind uint8

const (
	entryLevel entryKind = iota + 1
	entryData
	entryRewrite
)

// ListEntry is one step of a registered cutover list, in registration order.
// Build one with LevelEntry, DataEntry or RewriteEntry.
type ListEntry struct {
	kind    entryKind
	owner   storage.Owner // the storage owner the step is registered on
	level   storage.Migration
	data    datamigrate.DataMigration
	rewrite storage.RewriteStep
}

// LevelEntry is a level step (BooksLevelStep, storage.CoreLevelStep)
// registered on owner's Spec.Migrations: books for books' half, core/storage
// for core's.
func LevelEntry(owner storage.Owner, m storage.Migration) ListEntry {
	return ListEntry{kind: entryLevel, owner: owner, level: m}
}

// DataEntry is a cutover data step (ConfigStep, and later M3 to M6).
func DataEntry(m datamigrate.DataMigration) ListEntry {
	return ListEntry{kind: entryData, owner: m.Owner, data: m}
}

// RewriteEntry is M8 (RewriteStep).
func RewriteEntry(r storage.RewriteStep) ListEntry {
	return ListEntry{kind: entryRewrite, owner: r.Owner, rewrite: r}
}

// CutoverList is a registered cutover list and what it is checked against.
type CutoverList struct {
	// Target is the build's declared level per level owner: each owner's
	// Spec.Level, "core" for every core subsystem (storage.Supported).
	Target storage.Levels
	// FirstVersion, when set for a level owner, is the version its first
	// level step must have: its next contiguous one. The list alone cannot
	// know it (books' versioned steps start at LegacyCap+1 and an Additive
	// one may sit before the level step), so the registrar passes it.
	FirstVersion storage.Levels
	// Entries are the steps in registration order.
	Entries []ListEntry
}

// levelOwnerOfStep is the level owner and target level a level step's name
// encodes ("<levelOwner>.level-<n>", storage.LevelStepName), and whether m is
// one: Breaking, named for the level it raises.
func levelOwnerOfStep(m storage.Migration) (storage.Owner, bool) {
	if m.Compat != storage.Breaking || m.RaisesLevel <= 0 {
		return "", false
	}
	suffix := ".level-" + strconv.Itoa(m.RaisesLevel)
	owner, ok := strings.CutSuffix(m.Name, suffix)
	if !ok || owner == "" || m.Name != storage.LevelStepName(storage.Owner(owner), m.RaisesLevel) {
		return "", false
	}
	return storage.Owner(owner), true
}

// levelOwnerOfStorageOwner is the level owner of a storage owner: "core" for
// every core subsystem, the module ID otherwise (05-storage §5S.3.7).
func levelOwnerOfStorageOwner(o storage.Owner) storage.Owner {
	if o == "core" || strings.HasPrefix(string(o), "core/") {
		return "core"
	}
	return o
}

// ValidateCutoverList checks l (see the package comment above). The error is
// a *CutoverListError listing every problem; nil means the list is a valid
// R2 cutover list.
func ValidateCutoverList(l CutoverList) error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	type levelRef struct {
		pos     int
		version int
		level   int
	}
	levels := map[storage.Owner][]levelRef{} // by level owner, in list order
	rewrites := 0
	var pendingRun []string // names of the level steps not yet guarded by a data step
	for i, e := range l.Entries {
		switch e.kind {
		case entryLevel:
			lo, ok := levelOwnerOfStep(e.level)
			if !ok {
				add("entry %d (version %d of %s, %q) is not a level step: a level step is Breaking, raises a level and is named <owner>.level-<n>", i, e.level.Version, e.owner, e.level.Name)
				continue
			}
			if want := levelOwnerOfStorageOwner(e.owner); want != lo {
				add("level step %s is registered on %s, whose level owner is %s", e.level.Name, e.owner, want)
			}
			levels[lo] = append(levels[lo], levelRef{pos: i, version: e.level.Version, level: e.level.RaisesLevel})
			pendingRun = append(pendingRun, e.level.Name)
		case entryData:
			m := e.data
			switch {
			case !m.Cutover || m.Compat != storage.Breaking || m.RaisesLevel <= 0:
				add("data step %s is not a cutover step: it needs Cutover, Compat Breaking and a RaisesLevel above 0", m.ID)
			default:
				lo := levelOwnerOfStorageOwner(m.Owner)
				idx := slices.IndexFunc(levels[lo], func(r levelRef) bool { return r.level == m.RaisesLevel })
				if idx < 0 {
					add("data step %s raises %s to %d but the level step %s is not ordered before it", m.ID, lo, m.RaisesLevel, storage.LevelStepName(lo, m.RaisesLevel))
					break
				}
				if lo == m.Owner { // books' own level step is a DAG edge of its data step; core's is ordered by the list
					ref := storage.StepRef{Owner: m.Owner, Version: levels[lo][idx].version}
					if !slices.Contains(m.After, ref) {
						add("data step %s does not name %s (version %d) in its After", m.ID, storage.LevelStepName(lo, m.RaisesLevel), ref.Version)
					}
				}
			}
			pendingRun = nil
		case entryRewrite:
			rewrites++
			if e.rewrite.ID != RewriteID || e.rewrite.Owner != BooksOwner {
				add("M8 is %s of %s, want %s of %s (the one fixed ID, D15)", e.rewrite.ID, e.rewrite.Owner, RewriteID, BooksOwner)
			}
			if i != len(l.Entries)-1 {
				add("M8 is entry %d of %d: it must be the last step, after every data step", i, len(l.Entries))
			}
			if len(pendingRun) > 0 {
				add("level step(s) %s guard no data step before M8", strings.Join(pendingRun, ", "))
				pendingRun = nil
			}
		default:
			add("entry %d is the zero ListEntry", i)
		}
	}
	switch {
	case rewrites == 0:
		add("no M8 (%s) is registered: exactly one must be, last", RewriteID)
	case rewrites > 1:
		add("%d M8 steps are registered: exactly one must be, last", rewrites)
	}

	for _, lo := range slices.Sorted(maps.Keys(l.Target)) {
		target := l.Target[lo]
		refs := levels[lo]
		switch {
		case target > 0 && len(refs) == 0:
			add("%s is raised to %d by this build but the list has no level step for it (%s)", lo, target, storage.LevelStepName(lo, target))
		case len(refs) > 0 && refs[len(refs)-1].level != target:
			add("%s's last level step raises it to %d but the build declares level %d", lo, refs[len(refs)-1].level, target)
		}
	}
	for _, lo := range slices.Sorted(maps.Keys(levels)) {
		refs := levels[lo]
		if _, ok := l.Target[lo]; !ok {
			add("level step %s names %s, which declares no level in Target", storage.LevelStepName(lo, refs[0].level), lo)
		}
		if first, ok := l.FirstVersion[lo]; ok && refs[0].version != first {
			add("%s's first level step is version %d, want its next contiguous version %d", lo, refs[0].version, first)
		}
		for j := 1; j < len(refs); j++ {
			if refs[j].version != refs[j-1].version+1 {
				add("%s's level steps are not contiguous: version %d follows version %d", lo, refs[j].version, refs[j-1].version)
			}
			if refs[j].level <= refs[j-1].level {
				add("%s's level steps do not rise: level %d follows level %d", lo, refs[j].level, refs[j-1].level)
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return &CutoverListError{Problems: problems}
}
