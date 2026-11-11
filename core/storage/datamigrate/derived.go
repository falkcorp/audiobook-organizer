// file: core/storage/datamigrate/derived.go
// version: 1.0.1
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
//
//tasklint:constructor modules call it and pass the result to Registrar.DerivedIndex (05-storage §5S.3.9); core builds no derived index
func DerivedBuildTask(module, name string, s DerivedBuildSpec) tasks.Definition {
	timeout := s.Timeout
	if timeout == 0 {
		timeout = DefaultDerivedBuildTimeout
	}
	var start rbac.Requirement
	if s.PermStart != "" {
		start = rbac.Need(rbac.Permission(s.PermStart))
	}
	var run func(rc *tasks.RC, _ struct{}) (struct{}, error)
	if r := s.Run; r != nil {
		run = func(rc *tasks.RC, _ struct{}) (struct{}, error) { return struct{}{}, r(rc) }
	}
	// The spec is a literal inside the tasks.Task call so that archtest's
	// guards can read its Resume policy (05-tasks §5T.3.6).
	return tasks.Task(tasks.Kind(module+".derived."+name+".build"), tasks.TaskSpec[struct{}, struct{}]{
		Title:   s.Title,
		Effects: tasks.ReadOnly(s.Reads...),
		Perms:   tasks.Permissions{Start: start},
		Resume:  tasks.ResumeFromZero,
		Timeout: timeout,
		Run:     run,
	})
}
