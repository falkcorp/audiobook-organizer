// file: internal/plugins/maintenance/write_back_backup_test.go
// version: 1.0.0
// guid: ad057a17-f426-4cea-852a-7bef7ec836ee
// last-edited: 2026-10-10

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// writeBackCtxDeps records the ctx maintenance.bulk-write-back hands the server.
type writeBackCtxDeps struct {
	fakeDeps
	got []context.Context
}

func (d *writeBackCtxDeps) RunBulkWriteBack(ctx context.Context, _ string, _ []string, _ bool, _ int, _ operations.ProgressReporter) error {
	d.got = append(d.got, ctx)
	return nil
}

// Owner decision D69: maintenance.bulk-write-back keeps no .bak-* sibling per
// file. With create_backups on, the ctx it hands RunBulkWriteBack must opt out.
func TestBulkWriteBack_ContextOptsOutOfBackups(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	if !tagger.BackupWanted(context.Background()) {
		t.Fatal("precondition: create_backups on")
	}

	deps := &writeBackCtxDeps{}
	p := &Plugin{deps: deps}
	raw, err := json.Marshal(BulkWriteBackParams{BookIDs: []string{"b1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.runBulkWriteBack(context.Background(), raw, &resultReporter{}); err != nil {
		t.Fatalf("runBulkWriteBack: %v", err)
	}
	if len(deps.got) != 1 {
		t.Fatalf("RunBulkWriteBack called %d times, want 1", len(deps.got))
	}
	if tagger.BackupWanted(deps.got[0]) {
		t.Error("the ctx handed to RunBulkWriteBack wants a backup; the bulk opt-out was dropped")
	}
}
