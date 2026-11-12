// file: core/backup/metrics_test.go
// version: 1.0.0
// guid: 7b1e4d93-2c60-4a85-9f3e-8d0a5c7b2e14
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// TestBackupSeriesGolden: the backup scope serves exactly the families of
// testdata/series.golden (05-backup §5B.8), each fed by the operation it counts.
func TestBackupSeriesGolden(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("backup", backup.ScopeAttributes...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := backup.NewMetrics(sc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.NewMetrics(nil); err == nil {
		t.Fatal("NewMetrics without a scope succeeded")
	}
	e := newEnv(t)
	e.opts.Metrics = m
	e.rebuild()
	a := e.create("manual")
	if _, err := e.svc.Verify(ctx, a.ID, backup.VerifyEntries); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.List(ctx); err != nil {
		t.Fatal(err)
	}
	// The journal component and the boot re-apply feed these two.
	m.JournalGaps(ctx, 1)
	m.Reapplied(ctx, "apikey.revoked", "restore", 2)
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")

	// Each family moves with what it counts.
	if v, ok := seriesValue(t, p, "mediacore_backup_runs_total", map[string]string{"reason": "manual", "outcome": "ok"}); !ok || v != 1 {
		t.Errorf("runs{manual,ok} = %v, %v", v, ok)
	}
	if v, ok := seriesValue(t, p, "mediacore_backup_restore_total", map[string]string{"stage": "staged", "foreign": "false"}); !ok || v != 1 {
		t.Errorf("restore{staged,false} = %v, %v", v, ok)
	}
	if v, _ := seriesValue(t, p, "mediacore_backup_archives", map[string]string{"format": "mediacore-backup/1", "provenance": "own"}); v != 1 {
		t.Errorf("archives{mediacore-backup/1,own} = %v, want 1", v)
	}
	if v, _ := seriesValue(t, p, "mediacore_backup_bytes", nil); v != float64(a.Size) {
		t.Errorf("bytes = %v, want %d", v, a.Size)
	}
	if v, _ := seriesValue(t, p, "mediacore_backup_revocation_journal_gaps", nil); v != 1 {
		t.Errorf("gaps = %v", v)
	}
	if v, _ := seriesValue(t, p, "mediacore_backup_verify_total", map[string]string{"level": "entries", "outcome": "ok"}); v != 1 {
		t.Errorf("verify{entries,ok} = %v", v)
	}
	// A failed Create counts as a failed run, not a success.
	e.settings.Compression = "lz4"
	if _, err := e.svc.Create(ctx, backup.CreateOptions{}); err == nil {
		t.Fatal("Create with a bad codec succeeded")
	}
	if v, ok := seriesValue(t, p, "mediacore_backup_runs_total", map[string]string{"reason": "manual", "outcome": "error"}); !ok || v != 1 {
		t.Errorf("runs{manual,error} = %v, %v", v, ok)
	}
	// A nil *Metrics is a Service without a scope: nothing panics.
	var none *backup.Metrics
	none.Run(ctx, "manual", "ok")
	none.Phase(ctx, "archive", 0)
	none.Restore(ctx, "staged", false)
	none.JournalGaps(ctx, 1)
	none.Reapplied(ctx, "k", "boot", 1)
}
