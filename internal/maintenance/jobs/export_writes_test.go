// file: internal/maintenance/jobs/export_writes_test.go
// version: 3.0.0
// guid: 1f6e2b94-7c03-4d58-a9e1-5b8d3c70f2a6
// last-edited: 2026-11-19

package jobs

import (
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
	"github.com/falkcorp/mediacore/tasks"
)

// TraceView is one job's jobTrace for the external tests.
type TraceView struct {
	Methods []string
	Files   bool
}

// JobTraces is every job's trace, copied, for the oracle tests that compare it
// with what the job's code reaches.
func JobTraces() map[string]TraceView {
	out := make(map[string]TraceView, len(jobTraces))
	for id, tr := range jobTraces {
		out[id] = TraceView{Methods: append([]string(nil), tr.Methods...), Files: tr.Files}
	}
	return out
}

// JobWritesDerivations is how many times the write-set derivation has run.
func JobWritesDerivations() int { return int(jobWritesDerivations.Load()) }

// DeriveWrites is deriveWrites over the given traces (method lists only) and
// table, for the tests that feed it a bad trace.
func DeriveWrites(traces map[string][]string, table map[string][]ops.Resource) (map[string][]ops.Resource, error) {
	in := make(map[string]jobTrace, len(traces))
	for id, methods := range traces {
		in[id] = jobTrace{Methods: methods}
	}
	return deriveWrites(in, table)
}

// KindsWithFailingWrites is Kinds over a derivation that fails with err. It
// replaces FailJobWritesWith, which swapped the package variable Kinds reads and
// so was unsafe under t.Parallel: a parallel test building the kinds beside it
// would have seen the failure, and the swap and the read raced. Nothing global
// changes here.
func KindsWithFailingWrites(err error) ([]tasks.Definition, error) {
	return kindsWith(func() (map[string][]ops.Resource, error) { return nil, err }, KindDeps{}, nil)
}
