// file: core/backup/progress.go
// version: 1.0.0
// guid: 9e4b2a60-1c73-4d85-b7f2-6a0d8c3e5f19
// last-edited: 2026-11-12

package backup

import (
	"context"
	"time"
)

// Progress is where a long operation reports which phase it is in
// (checkpoint, checksum, archive, verify: 05-backup §5B.3.5 "Progress"). The
// tasks layer's *tasks.RC satisfies it through its Phase method; a Service run
// without one reports to the metrics only.
type Progress interface {
	Phase(name string) (end func())
}

type progressKey struct{}

// WithProgress returns a context that carries p, for Create and Verify to
// report their phases to.
func WithProgress(ctx context.Context, p Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

// phase starts a named phase: it tells the context's Progress and, when the
// returned function is called, records the phase's duration in the metrics.
func (s *Svc) phase(ctx context.Context, name string) (end func()) {
	var done func()
	if p, ok := ctx.Value(progressKey{}).(Progress); ok && p != nil {
		done = p.Phase(name)
	}
	t0 := time.Now()
	return func() {
		if done != nil {
			done()
		}
		s.o.Metrics.Phase(ctx, name, time.Since(t0))
	}
}
