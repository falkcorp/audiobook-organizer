// file: core/storage/datamigrate/derived.go
// version: 1.0.0
// guid: f7abceb7-e1c5-4049-9dd5-fbac58e1b8c1
// last-edited: 2026-11-10

package datamigrate

import (
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// DerivedBuildSpec and DerivedBuildTask build the rebuild task of a derived
// index (§5S.3.9). They live here, not in storage, because storage (L1)
// cannot name a tasks type (COR3-7); a module may equally build the same
// tasks.Definition with tasks.Task.
type DerivedBuildSpec struct {
	Title     string
	Reads     []entity.Ref             // module resources the rebuild reads (Effects: ReadOnly over them)
	PermStart string                   // the module permission that may start a rebuild by hand
	Timeout   time.Duration            // 0 = 24h (library-scale, as Batch)
	Run       func(rc *tasks.RC) error // its library-scale loops declare their tasks.Parallelism (R7)
}

// DefaultDerivedBuildTimeout is DerivedBuildSpec.Timeout when it is zero.
// Name coined by F2-06.
const DefaultDerivedBuildTimeout = 24 * time.Hour

// DerivedBuildTask returns the tasks.Definition of kind
// "<module>.derived.<name>.build" (name module-relative, e.g. "hnsw"), with
// Resume set to restart from scratch; Registrar.DerivedIndex takes it (06).
//
// Like every tasks constructor it never panics: a bad module or name, a
// missing Title, Run or PermStart is one of tasks.Problems(def), which
// registration reports.
func DerivedBuildTask(module, name string, s DerivedBuildSpec) tasks.Definition {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = DefaultDerivedBuildTimeout
	}
	var start rbac.Requirement
	if s.PermStart != "" {
		start = rbac.Need(rbac.Permission(s.PermStart))
	}
	spec := tasks.TaskSpec[struct{}, struct{}]{
		Title:   s.Title,
		Effects: tasks.ReadOnly(s.Reads...),
		Perms:   tasks.Permissions{Start: start},
		Resume:  tasks.ResumeFromZero,
		Timeout: timeout,
	}
	if run := s.Run; run != nil {
		spec.Run = func(rc *tasks.RC, _ struct{}) (struct{}, error) { return struct{}{}, run(rc) }
	}
	return tasks.Task(tasks.Kind(module+".derived."+name+".build"), spec)
}
