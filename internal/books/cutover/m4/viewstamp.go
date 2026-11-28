// file: internal/books/cutover/m4/viewstamp.go
// version: 1.0.0
// guid: 6d2b8f41-9e07-4c35-a1b8-3f5e0c7d9a62
// last-edited: 2026-11-28

package m4

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// OutcomeViewStamped counts the kinds whose read rule the step recorded: one
// per kind, however many of its runs were imported.
const OutcomeViewStamped = "kind-view-stamped"

// ViewOfFunc is HEAD's read gate for the runs of kind k, as the rule core's
// View takes: the zero KindView for a kind HEAD showed to every reader of the
// task routes, a Requires for one it restricted (a Repairs fixer's
// permissions). stamp false says the kind's snapshot is not this step's to
// write (core's own kinds, which core registers and upserts). An error means
// HEAD's gate for k cannot be told, and fails the step rather than leaving the
// kind to the executor's closed default (R3): the owner is told which kind.
type ViewOfFunc func(k tasks.Kind) (v tasks.KindView, stamp bool, err error)

// viewStamper stages each imported run's kind View snapshot beside the run. The
// executor reads a run of a kind with no live definition under the snapshot's
// rule and hides it from every caller when there is none (F5-18 reach R3), and
// the kinds an import carries over are largely ones no definition registers
// (books.retired.<id>, a retired fixer's books.repairs.<id>), so without a
// stamp every imported run of such a kind would vanish from the task routes,
// admin included, where HEAD showed it to its readers.
//
// The stamp is staged for every run, not once per kind: a chunk's batch can
// fail and the step resumes, and a kind remembered as done would then never be
// stamped. The bytes are identical for a kind, so the repeat is a rewrite of
// the same value. Only ViewOf's answer is remembered.
type viewStamper struct {
	st     tasks.ViewStamper
	viewOf ViewOfFunc
	logger *slog.Logger

	kinds sync.Map // tasks.Kind -> *viewVerdict
}

type viewVerdict struct {
	once  sync.Once
	view  tasks.KindView
	stamp bool
	err   error
}

func newViewStamper(imp tasks.Importer, viewOf ViewOfFunc, logger *slog.Logger) (*viewStamper, error) {
	if viewOf == nil {
		return nil, errors.New("m4: Config.ViewOf is HEAD's read gate per kind (F5-18 reach R3: a kind with no snapshot is hidden from everyone)")
	}
	st, ok := imp.(tasks.ViewStamper)
	if !ok {
		return nil, fmt.Errorf("m4: Config.Importer (%T) cannot record a kind's View rule (tasks.ViewStamper)", imp)
	}
	return &viewStamper{st: st, viewOf: viewOf, logger: logger}, nil
}

// stage writes kind's snapshot into b. The kind's first stage counts it.
func (s *viewStamper) stage(ctx context.Context, b *storage.Batch, w *datamigrate.StepWriter, kind tasks.Kind) error {
	vv, _ := s.kinds.LoadOrStore(kind, &viewVerdict{})
	v := vv.(*viewVerdict)
	first := false
	v.once.Do(func() {
		first = true
		v.view, v.stamp, v.err = s.viewOf(kind)
	})
	if v.err != nil {
		return fmt.Errorf("m4: HEAD's read gate for kind %q cannot be told: %w", kind, v.err)
	}
	if !v.stamp {
		return nil
	}
	if err := s.st.StampView(ctx, b, kind, v.view); err != nil {
		return fmt.Errorf("m4: record the read rule of kind %q: %w", kind, err)
	}
	if first {
		w.Count(OutcomeViewStamped)
		s.logger.Info("m4: a kind's read rule is recorded", "kind", string(kind))
	}
	return nil
}
