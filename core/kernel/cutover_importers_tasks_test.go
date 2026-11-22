// file: core/kernel/cutover_importers_tasks_test.go
// version: 0.1.0
// guid: e19a6c43-2b78-4d05-9f31-c6a8b5d0e742
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// TestImporters_TasksAndLogRefuseUntilBound: an unbound Importers.Tasks and
// Importers.Log refuse every call, and the run read-back the activity rewrite
// asks for, with ErrImporterNotBound, so a step that runs before P4b writes and
// reads nowhere.
func TestImporters_TasksAndLogRefuseUntilBound(t *testing.T) {
	ctx := context.Background()
	imp := (&lateImporters{}).values()
	errs := []error{
		imp.Tasks.Run(ctx, nil, tasks.RunRecord{}),
		imp.Tasks.RunData(ctx, nil, "", tasks.RunData{}),
		imp.Tasks.Clock(ctx, nil, "", tasks.Clock{}),
		imp.Tasks.Pause(ctx, nil, tasks.Pause{}),
		imp.Tasks.StandDown(ctx, nil, tasks.StandDown{}),
		imp.Tasks.Debounce(ctx, nil, tasks.DebounceBucket{}),
		imp.Tasks.Stamp(ctx, nil, entity.Ref{}, "", time.Time{}),
		imp.Log.Import(ctx, []log.ImportedRecord{{}}),
	}
	_, err := imp.Tasks.RenameKind(ctx, nil, "", "", "", 1)
	errs = append(errs, err)
	r, ok := imp.Tasks.(interface {
		Get(context.Context, tasks.RunID) (tasks.RunRecord, error)
	})
	if !ok {
		t.Fatal("Importers.Tasks cannot read a run back")
	}
	_, err = r.Get(ctx, "01JBATCAVE0000000000000041")
	errs = append(errs, err)
	for i, err := range errs {
		if !errors.Is(err, module.ErrImporterNotBound) {
			t.Errorf("unbound call %d = %v, want ErrImporterNotBound", i, err)
		}
	}
}

// TestBoot_BindsTasksAndLogImportersOverTheirKeyspaces: a booted kernel's
// Importers.Log writes through to c/log/ (the native journal reads the record
// back), and Importers.Tasks reads from c/tasks/: an unknown run is
// tasks.ErrRunNotFound, not an unbound importer.
func TestBoot_BindsTasksAndLogImportersOverTheirKeyspaces(t *testing.T) {
	ctx := context.Background()
	h := newSwapHost(t)
	k := h.mustBoot(h.envConfig(nil))
	defer h.close(k)
	imp := k.importers().values()

	at := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	rec := log.ImportedRecord{Record: log.Record{Time: at, Level: slog.LevelInfo, Module: "books", Message: "The Long Halloween"}, SourceID: "syslog:2026-11-22T03:00:00Z:batcave"}
	for range 2 { // idempotent: the second import rewrites the same key
		if err := imp.Log.Import(ctx, []log.ImportedRecord{rec}); err != nil {
			t.Fatalf("Import: %v", err)
		}
	}
	ks, ok := k.store.db.Keyspace(logjournal.Owner)
	if !ok {
		t.Fatal("no core/log keyspace")
	}
	page, err := logjournal.New(ks).Query(ctx, log.JournalQuery{MinLevel: slog.LevelInfo})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 || page.Records[0].Message != "The Long Halloween" {
		t.Errorf("journal holds %+v, want the one imported record", page.Records)
	}

	r := imp.Tasks.(interface {
		Get(context.Context, tasks.RunID) (tasks.RunRecord, error)
	})
	if _, err := r.Get(ctx, "01JBATCAVE0000000000000041"); !errors.Is(err, tasks.ErrRunNotFound) {
		t.Errorf("Get of an unknown run = %v, want ErrRunNotFound", err)
	}
}
