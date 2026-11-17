// file: internal/maintenance/jobs/export_writes_test.go
// version: 1.0.0
// guid: 0f7c3a52-8d14-4be6-a1c9-52e6d0b94f37
// last-edited: 2026-11-16

package jobs

// TraceView is one job's jobTrace for the external tests.
type TraceView struct {
	Methods []string
	Outside []OutsideView
}

// OutsideView is one outsideWrite for the external tests.
type OutsideView struct {
	Via string
	Res string
}

// ViaDisk is the Via of a write to files on disk.
const ViaDisk = viaDisk

// JobTraces is every job's trace, copied, for the oracle tests that compare it
// with what the job's code reaches.
func JobTraces() map[string]TraceView {
	out := make(map[string]TraceView, len(jobTraces))
	for id, tr := range jobTraces {
		v := TraceView{Methods: append([]string(nil), tr.Methods...)}
		for _, o := range tr.Outside {
			v.Outside = append(v.Outside, OutsideView{Via: o.Via, Res: string(o.Res)})
		}
		out[id] = v
	}
	return out
}
