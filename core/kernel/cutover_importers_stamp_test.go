// file: core/kernel/cutover_importers_stamp_test.go
// version: 1.0.0
// guid: 8a4e1d63-2f95-4b07-9c38-6d1b7e0a5f24
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

type plainTasksImporter struct{ tasks.Importer }

type stampRecorder struct {
	tasks.Importer
	kind tasks.Kind
	view tasks.KindView
	n    int
}

func (s *stampRecorder) StampView(_ context.Context, _ *storage.Batch, k tasks.Kind, v tasks.KindView) error {
	s.kind, s.view, s.n = k, v, s.n+1
	return nil
}

// TestImporters_TasksStampViewForwardsOnlyOnceBoundAndToAStamper: an app finds
// tasks.ViewStamper by type assertion on Importers.Tasks (m4 records the read
// gate of the kinds it imports). Unbound it refuses with ErrImporterNotBound;
// bound to an importer that cannot stamp, with its own error (never silence,
// which would leave the imported kind hidden from everyone); bound to a
// stamper, it forwards the kind and the rule untouched.
func TestImporters_TasksStampViewForwardsOnlyOnceBoundAndToAStamper(t *testing.T) {
	ctx := context.Background()
	l := &lateImporters{}
	st, ok := l.values().Tasks.(tasks.ViewStamper)
	if !ok {
		t.Fatal("Importers.Tasks cannot record a kind's View rule")
	}
	view := tasks.KindView{Requires: rbac.AllOf("gotham:settings")}

	if err := st.StampView(ctx, nil, "books.retired.batcave", view); !errors.Is(err, module.ErrImporterNotBound) {
		t.Errorf("StampView unbound = %v; want ErrImporterNotBound", err)
	}
	l.bindNative(plainTasksImporter{}, nil, nil)
	if err := st.StampView(ctx, nil, "books.retired.batcave", view); err == nil || errors.Is(err, module.ErrImporterNotBound) {
		t.Errorf("StampView over an importer that cannot stamp = %v; want its own refusal", err)
	}
	rec := &stampRecorder{}
	l.bindNative(rec, nil, nil)
	if err := st.StampView(ctx, nil, "books.retired.batcave", view); err != nil {
		t.Fatalf("StampView bound: %v", err)
	}
	if rec.n != 1 || rec.kind != "books.retired.batcave" || rec.view.Requires.String() != view.Requires.String() {
		t.Errorf("forwarded %d call(s) as %q %v; want one for books.retired.batcave %v", rec.n, rec.kind, rec.view.Requires, view.Requires)
	}
}
