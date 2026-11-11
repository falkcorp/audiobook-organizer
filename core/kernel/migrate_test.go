// file: core/kernel/migrate_test.go
// version: 1.0.0
// guid: 8372b531-fb94-4d44-b792-8947198d08ac
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// dmOwners: the module gotham with a keyspace and one versioned step.
func dmOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{{Owner: "gotham", Enabled: true, Spec: storage.Spec{Keyspace: true, Migrations: []storage.Migration{
		{Version: 1, Name: "bat-signal", Compat: storage.Additive, Up: func(context.Context, *storage.MigrationTx) error { return nil }},
	}}}}
}

// dmRecount steps every case file under m/gotham/case/ once.
func dmRecount() datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gotham.recount", Owner: "gotham", Source: []storage.Range{{Start: []byte("m/gotham/case/"), End: []byte("m/gotham/case0")}},
		Parallelism: tasks.Fixed(2, "test: two shards"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
		Step: func(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
			w.Count("converted")
			return w.Set(kv.Key, []byte("filed"))
		},
	}
}

// dmBoot opens the store on fs with steps, prepares and migrates it.
func dmBoot(t *testing.T, fs vfs.FS, steps []storage.DataStep) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{Path: "/batcave/store", FS: storage.FSOf(fs), Owners: dmOwners(),
		Build: "batcomputer-1", FreeSpaceMargin: 1, DataSteps: steps})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestDataMigrationsKernelSide: NewDataMigrations validates against the
// build's owners; Steps run the migration at P4b inside storage's DAG;
// RecordMetrics records the items per outcome, the one-hot state and the
// step's P4b boot phase.
func TestDataMigrationsKernelSide(t *testing.T) {
	bad := dmRecount()
	bad.Parallelism = tasks.Parallelism{}
	if _, err := NewDataMigrations(DataMigrationsConfig{}, dmOwners(), bad); !errors.Is(err, tasks.ErrNoParallelism) {
		t.Fatalf("NewDataMigrations = %v, want ErrNoParallelism", err)
	}
	mem := vfs.NewMem()
	mem.TestingSetDiskUsage(vfs.DiskUsage{AvailBytes: 1 << 40, TotalBytes: 1 << 41, UsedBytes: 1 << 40})
	db := dmBoot(t, mem, nil)
	ks, _ := db.Keyspace("gotham")
	for _, k := range []string{"case/arkham", "case/blackgate", "case/iceberg"} {
		if err := ks.Set(context.Background(), []byte(k), []byte("open")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var extended []string
	dms, err := NewDataMigrations(DataMigrationsConfig{ChunkItems: 2, ExtendStart: func(s string) { extended = append(extended, s) }}, dmOwners(), dmRecount())
	if err != nil {
		t.Fatal(err)
	}
	if steps := dms.Steps(); len(steps) != 1 || steps[0].ID != "gotham.recount" || steps[0].Owner != "gotham" {
		t.Fatalf("Steps = %+v", steps)
	}
	db = dmBoot(t, mem, dms.Steps())
	if len(extended) == 0 || !strings.Contains(extended[0], "gotham.recount") {
		t.Fatalf("ExtendStart calls = %q, want the step's start", extended)
	}
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	scope, err := p.Core("storage", "owner", "kind", "state", "op", "mode", "migration", "outcome")
	if err != nil {
		t.Fatal(err)
	}
	ks2, err := p.Core("kernel", "phase", "step")
	if err != nil {
		t.Fatal(err)
	}
	phase, err := ks2.Meter().Float64Gauge(ks2.Prefix()+".boot_phase", metric.WithUnit("s"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dms.RecordMetrics(ctx, db, scope, phase); err != nil {
		t.Fatal(err)
	}
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	series := func(name string, labels map[string]string) (float64, bool) {
		for _, mf := range mfs {
			if mf.GetName() != name {
				continue
			}
			for _, m := range mf.GetMetric() {
				if matches(m, labels) {
					switch {
					case m.GetCounter() != nil:
						return m.GetCounter().GetValue(), true
					case m.GetGauge() != nil:
						return m.GetGauge().GetValue(), true
					}
				}
			}
		}
		return 0, false
	}
	if v, ok := series("mediacore_storage_data_migration_items_total", map[string]string{"migration": "gotham.recount", "outcome": "converted"}); !ok || v != 3 {
		t.Fatalf("items_total{converted} = %v (%v), want 3", v, ok)
	}
	if v, ok := series("mediacore_storage_data_migration_state", map[string]string{"migration": "gotham.recount", "state": "done"}); !ok || v != 1 {
		t.Fatalf("state{done} = %v (%v), want 1", v, ok)
	}
	if v, ok := series("mediacore_storage_data_migration_state", map[string]string{"migration": "gotham.recount", "state": "failed"}); !ok || v != 0 {
		t.Fatalf("state{failed} = %v (%v), want 0", v, ok)
	}
	if _, ok := series("mediacore_kernel_boot_phase_seconds", map[string]string{"phase": "P4b", "step": "gotham.recount"}); !ok {
		t.Fatal("no boot_phase{phase=P4b,step=gotham.recount}")
	}
}

func matches(m *dto.Metric, labels map[string]string) bool {
	n := 0
	for _, l := range m.GetLabel() {
		if want, ok := labels[l.GetName()]; ok {
			if want != l.GetValue() {
				return false
			}
			n++
		}
	}
	return n == len(labels)
}

// TestDataMigrationsPreflightSeam: Preflight (F3-04's P4a seam) does nothing
// without its hook or without a cutover step, and otherwise hands the hook
// every cutover step and wraps its refusal.
func TestDataMigrationsPreflightSeam(t *testing.T) {
	owners := []storage.OwnerSpec{{Owner: "gotham", Enabled: true, Spec: storage.Spec{Keyspace: true, Claims: []storage.Claim{
		{Prefix: "case:", Mode: storage.ClaimRetired},
	}}}}
	cut := datamigrate.DataMigration{ID: "gotham.cutover", Owner: "gotham", Source: []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: tasks.Sequential("test"), Mode: datamigrate.BootBlocking, Compat: storage.Breaking, Cutover: true,
		Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil }}
	ctx := context.Background()
	dms, err := NewDataMigrations(DataMigrationsConfig{}, owners, cut)
	if err != nil {
		t.Fatal(err)
	}
	if err := dms.Preflight(ctx, nil); err != nil {
		t.Fatalf("Preflight without a hook = %v", err)
	}
	var got []string
	hook := func(_ context.Context, _ *storage.Opened, steps []datamigrate.DataMigration) error {
		for _, s := range steps {
			got = append(got, s.ID)
		}
		return errors.New("Catwoman's account merge is in progress")
	}
	dms, err = NewDataMigrations(DataMigrationsConfig{Preflight: hook}, owners, cut)
	if err != nil {
		t.Fatal(err)
	}
	if err := dms.Preflight(ctx, nil); err == nil || !strings.Contains(err.Error(), "P4a") || len(got) != 1 || got[0] != "gotham.cutover" {
		t.Fatalf("Preflight = %v with %v, want the hook's refusal for gotham.cutover", err, got)
	}
	plain := datamigrate.DataMigration{ID: "gotham.plain", Owner: "gotham", Source: []storage.Range{{Start: []byte("m/gotham/a"), End: []byte("m/gotham/b")}},
		Parallelism: tasks.Sequential("test"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
		Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil }}
	dms, err = NewDataMigrations(DataMigrationsConfig{Preflight: hook}, owners, plain)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := dms.Preflight(ctx, nil); err != nil || got != nil {
		t.Fatalf("Preflight with no cutover step = %v, called with %v", err, got)
	}
}
