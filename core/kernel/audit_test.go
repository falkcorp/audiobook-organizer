// file: core/kernel/audit_test.go
// version: 1.0.0
// guid: 94d1b7e3-6a50-4c28-8f3e-0b5a2d7c9e41
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/storage"
)

// bootAuditDB boots a store with owners the way P2, P4a and P4b do. coretest
// cannot be imported here (it reaches the kernel), so the boot is spelled out.
func bootAuditDB(t *testing.T, owners ...storage.OwnerSpec) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "main.pebble"), Owners: owners,
		Build: "coretest+fixture", FreeSpaceMargin: 1, FS: storage.MemFS(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// keysOf lists every key of owner's keyspace.
func keysOf(t *testing.T, db *storage.DB, owner storage.Owner) []string {
	t.Helper()
	ks, ok := db.Keyspace(owner)
	if !ok {
		t.Fatalf("no keyspace for %s", owner)
	}
	var out []string
	for kv, err := range ks.ScanPrefix(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(kv.Key))
	}
	return out
}

func TestAuditOwnerSpec(t *testing.T) {
	spec := auditOwnerSpec()
	if spec.Owner != "core/audit" || !spec.Spec.Keyspace || !spec.Enabled {
		t.Errorf("auditOwnerSpec = %+v, want an enabled core/audit keyspace", spec)
	}
	if len(spec.Spec.Claims)+len(spec.Spec.Migrations)+len(spec.Spec.Serialized) != 0 {
		t.Errorf("auditOwnerSpec carries claims, migrations or serialized prefixes: %+v", spec.Spec)
	}
	db := bootAuditDB(t, spec)
	if _, ok := db.Keyspace("core/audit"); !ok {
		t.Error("the spec gave no c/audit/ keyspace")
	}
}

// TestAudit_KernelBuildStartStop: the build writes nothing, a record written
// through the store round-trips, and Stop drains the async queue.
func TestAudit_KernelBuildStartStop(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, auditOwnerSpec())
	before := keysOf(t, db, "core/audit")
	s, err := buildAudit(ctx, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after := keysOf(t, db, "core/audit"); !slices.Equal(before, after) {
		t.Errorf("buildAudit wrote rows: before %q, after %q", before, after)
	}
	if err := startAudit(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := startAudit(ctx, s); err == nil {
		t.Error("a second startAudit succeeded")
	}
	actor := audit.Actor{UserID: "bruce_wayne", Username: "Bruce Wayne"}
	if err := s.Record(ctx, audit.Event{Action: "core.auth.setup", Outcome: audit.Success, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	s.Emit(ctx, audit.Event{Action: "core.auth.login", Outcome: audit.Failure, Actor: actor, Reason: "bad_password"})
	if err := stopAudit(ctx, s); err != nil {
		t.Fatal(err)
	}
	var got []string
	for rec, err := range s.Query(ctx, audit.Filter{ActorUserID: "bruce_wayne"}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, rec.Action)
	}
	if want := []string{"core.auth.login", "core.auth.setup"}; !slices.Equal(got, want) {
		t.Errorf("records = %v, want %v (newest first)", got, want)
	}
}

// TestAudit_KernelBuildNeedsTheOwner: a store opened without auditOwnerSpec
// fails the build with a message that names the missing wiring.
func TestAudit_KernelBuildNeedsTheOwner(t *testing.T) {
	db := bootAuditDB(t)
	_, err := buildAudit(context.Background(), db, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "auditOwnerSpec") {
		t.Errorf("buildAudit without the owner = %v, want an error naming auditOwnerSpec", err)
	}
	if _, err := buildAudit(context.Background(), nil, nil, nil); err == nil {
		t.Error("buildAudit(nil db) succeeded")
	}
}

// TestAudit_KernelMetrics: the Provider is built after the store (P5 after
// P4b), auditMetrics gives it the core audit scope, and what was recorded
// before it is still served.
func TestAudit_KernelMetrics(t *testing.T) {
	ctx := context.Background()
	db := bootAuditDB(t, auditOwnerSpec())
	s, err := buildAudit(ctx, db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, audit.Event{Action: "core.auth.setup", Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	sc, err := auditMetrics(p, s)
	if err != nil {
		t.Fatal(err)
	}
	metricstest.AssertSeries(t, p, sc, "../audit/testdata/series.golden")
	if _, err := auditMetrics(p, s); err == nil {
		t.Error("a second auditMetrics succeeded")
	}
}
