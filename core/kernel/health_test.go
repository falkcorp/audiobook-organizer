// file: core/kernel/health_test.go
// version: 1.0.0
// guid: 8c842fd1-600f-443e-a1eb-c30bde81c922
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/storage"
)

// memStore boots a store on an in-memory filesystem the way coretest.Store
// does (Open, Prepare, Migrate). coretest imports kernel, so a kernel test
// cannot import it (see the R1-37 seams file).
func memStore(t *testing.T) *storage.DB {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path: "/batcave/main.pebble", Build: "kernel-health-test", FreeSpaceMargin: 1, FS: storage.MemFS(),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		t.Fatalf("boot: %v", errors.Join(err, o.Close()))
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func states(reg *health.Registry) map[health.ComponentID]health.ComponentState {
	m := map[health.ComponentID]health.ComponentState{}
	for _, c := range reg.Snapshot().Components {
		m[c.ID] = c
	}
	return m
}

// TestKernel_RegistersStorageAndSecretsComponents: over an in-memory store the
// registry holds exactly core.storage (Up after the ping) and core.secrets
// (Up). F2-16 adds the boot-path assertion.
func TestKernel_RegistersStorageAndSecretsComponents(t *testing.T) {
	db := memStore(t)
	reg, rep, err := buildHealth(db.Admin())
	if err != nil {
		t.Fatal(err)
	}
	got := states(reg)
	var ids []health.ComponentID
	for id := range got {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []health.ComponentID{"core.secrets", "core.storage"}) {
		t.Fatalf("components = %v, want exactly core.secrets and core.storage", ids)
	}
	st, se := got["core.storage"], got["core.secrets"]
	if st.Result.Status != health.StatusUp || st.Result.Reason != "" || st.Result.At.IsZero() {
		t.Errorf("core.storage = %+v, want Up with the ping's time", st.Result)
	}
	if !st.Critical {
		t.Error("core.storage is not Critical")
	}
	if se.Result.Status != health.StatusUp || se.Result.At.IsZero() {
		t.Errorf("core.secrets = %+v, want Up", se.Result)
	}
	if se.Critical {
		t.Error("core.secrets is Critical")
	}
	if rep.Storage == nil || rep.Secrets == nil {
		t.Fatalf("reporters = %+v, want both", rep)
	}
	if ok, tok := reg.Ready(); !ok {
		t.Errorf("Ready = false %v on a healthy store", tok)
	}
	// The executor's default Ready signal is {core.storage}.
	if err := reg.Wait(t.Context(), "core.storage"); err != nil {
		t.Errorf("Wait(core.storage) = %v", err)
	}
}

// The reporters are the components' own: F3's tokens ride them.
func TestKernel_ReportersRaiseTheF3Tokens(t *testing.T) {
	reg, rep, err := buildHealth(memStore(t).Admin())
	if err != nil {
		t.Fatal(err)
	}
	rep.Storage.Report(health.Result{Status: health.StatusDegraded, Reason: "storage_quarantine"})
	rep.Secrets.Report(health.Result{Status: health.StatusDegraded, Reason: "secrets_undecryptable"})
	got := states(reg)
	if r := got["core.storage"].Result; r.Status != health.StatusDegraded || r.Reason != "storage_quarantine" {
		t.Errorf("core.storage = %+v", r)
	}
	if r := got["core.secrets"].Result; r.Status != health.StatusDegraded || r.Reason != "secrets_undecryptable" {
		t.Errorf("core.secrets = %+v", r)
	}
	if ok, tok := reg.Ready(); !ok {
		t.Errorf("a Degraded core.storage must stay ready: %v", tok)
	}
}

// A store that cannot answer leaves core.storage Down and the process not
// ready, with the fixed token.
func TestKernel_ClosedStoreIsDownAtRegister(t *testing.T) {
	db := memStore(t)
	admin := db.Admin()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reg, _, err := buildHealth(admin)
	if err != nil {
		t.Fatal(err)
	}
	if r := states(reg)["core.storage"].Result; r.Status != health.StatusDown || r.Reason != "engine_ping_failed" {
		t.Errorf("core.storage = %+v, want Down engine_ping_failed", r)
	}
	ok, tok := reg.Ready()
	if ok || !slices.Equal(tok, []string{"core.storage:engine_ping_failed"}) {
		t.Errorf("Ready = %v %v", ok, tok)
	}
}

func TestKernel_BuildHealthNeedsAStore(t *testing.T) {
	if _, _, err := buildHealth(nil); err == nil {
		t.Fatal("buildHealth(nil) = nil error")
	}
}
