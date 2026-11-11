// file: core/integrations/registry_extra_test.go
// version: 1.0.0
// guid: 0001123d-da59-42c5-aa6c-5ea8c12c21e2
// last-edited: 2026-11-11

package integrations_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
)

// TestRegistry_NoOutboundRefuses: without Options.Outbound an instance's
// client and gate refuse before sending.
func TestRegistry_NoOutboundRefuses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) { o.Outbound = nil })
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	inst, release := lease(t, r, signalRef)
	defer release()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://192.0.2.20/", nil)
	if _, err := inst.Env.HTTP.Do(req); integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("Do without an outbound policy: %v", err)
	}
	if resp, err := inst.Env.HTTP.Client().Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("the SDK client sent without an outbound policy")
	}
	if _, err := inst.Env.HTTP.Gate().Acquire(ctx, "requests"); integrations.ClassOf(ctx, err) != integrations.ClassRefusedLocal {
		t.Errorf("Acquire without an outbound policy: %v", err)
	}
	if st := inst.Env.Gate.State(); st.Hold != nil {
		t.Errorf("State = %+v", st)
	}
	if h, _ := r.Health(ctx, signalRef); h.Status != integrations.HealthDown {
		t.Errorf("Health without an outbound policy = %+v", h)
	}
}

// racingStore commits a competing write of the same row between the
// Registry's stage and its commit.
type racingStore struct {
	integrations.InstanceStore
	ks storage.Keyspace
}

func (s racingStore) Put(ctx context.Context, b *storage.Batch, inst integrations.StoredInstance) (uint64, error) {
	v, err := s.InstanceStore.Put(ctx, b, inst)
	if err != nil {
		return v, err
	}
	other := s.ks.NewBatch()
	defer func() { _ = other.Close() }()
	rival := inst
	rival.Config.Name = "Two-Face"
	if _, err := s.InstanceStore.Put(ctx, other, rival); err != nil {
		return 0, err
	}
	return v, other.Commit(ctx, storage.Sync)
}

// failingStore fails every write.
type failingStore struct{ integrations.InstanceStore }

func (failingStore) Put(context.Context, *storage.Batch, integrations.StoredInstance) (uint64, error) {
	return 0, errors.New("the disk under the Batcave is full")
}

func (failingStore) Delete(context.Context, *storage.Batch, integrations.Ref) error {
	return errors.New("the disk under the Batcave is full")
}

// TestPut_StoreFailures: a store write that fails is ErrUnavailable; a
// writer that commits first makes the Put ErrVersionConflict; neither is
// served.
func TestPut_StoreFailures(t *testing.T) {
	e := newEnv(t)
	r := e.registry(t, nil, func(o *integrations.Options) { o.Store = failingStore{o.Store} })
	if _, err := r.Put(admin(), signal("http://192.0.2.20/"), cred("alfred-1")); !errors.Is(err, integrations.ErrUnavailable) {
		t.Errorf("a failing store write: %v", err)
	}
	if _, ok := r.Config(signalRef); ok {
		t.Error("a failed create is served")
	}

	e2 := newEnv(t)
	plain := e2.registry(t, nil, nil)
	mustPut(t, admin(), plain, signal("http://192.0.2.20/"), cred("alfred-1"))
	racing := e2.registry(t, nil, func(o *integrations.Options) { o.Store = racingStore{InstanceStore: o.Store, ks: e2.ks} })
	if _, err := racing.Put(admin(), current(t, racing, signalRef), cred("alfred-2")); !errors.Is(err, integrations.ErrVersionConflict) {
		t.Errorf("a Put losing the race: %v", err)
	}
	if row, _ := e2.stored(t, signalRef); row.Config.Name != "Two-Face" || e2.secret(t, signalRef, "password") != "alfred-1" {
		t.Errorf("after the lost race: row %q, secret %q", row.Config.Name, e2.secret(t, signalRef, "password"))
	}
	broken := e2.registry(t, nil, func(o *integrations.Options) { o.Store = failingStore{o.Store} })
	if err := broken.Delete(admin(), signalRef); !errors.Is(err, integrations.ErrUnavailable) {
		t.Errorf("a failing store delete: %v", err)
	}
}

// TestRegistry_OperatorValuesCoerced: env values are typed by the field
// they set; a value that does not fit makes the instance misconfigured; an
// operator value for no field, or a secret that is not a string, is
// ignored.
func TestRegistry_OperatorValuesCoerced(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	r := e.registry(t, nil, nil)
	mustPut(t, admin(), r, integrations.InstanceConfig{
		Ref: oracleRef, Adapter: oracle, Enabled: true, Settings: map[string]any{"host": "192.0.2.60", "port": 9000},
		Limits: &integrations.Limits{RPS: 1, Timeout: 2 * time.Second},
	}, nil)
	if err := r.SetOverlay(ctx, overlay(
		"integrations.metadata.clocktower.port", "9443",
		"integrations.metadata.clocktower.utility_belt", "on",
	)); err != nil {
		t.Fatal(err)
	}
	cfg := current(t, r, oracleRef)
	if cfg.Settings["port"] != float64(9443) || cfg.Locked["utility_belt"] != "" || cfg.Limits == nil || cfg.Limits.RPS != 1 {
		t.Errorf("coerced view = %+v", cfg)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthOK {
		t.Errorf("Health = %+v", h)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.metadata.clocktower.port", "nine thousand")); err != nil {
		t.Fatal(err)
	}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthMisconfigured || h.Message != integrations.ReasonInvalidSettings {
		t.Errorf("Health with a port that is no integer = %+v", h)
	}
	if err := r.SetOverlay(ctx, overlay("integrations.metadata.clocktower.password", "")); err != nil {
		t.Fatal(err)
	}
	if cfg := current(t, r, oracleRef); cfg.Locked["password"] != "" {
		t.Errorf("an empty operator secret locked the field: %v", cfg.Locked)
	}
	e.out.State = integrations.GateState{Hold: &integrations.Hold{Scope: "requests", Class: integrations.ClassRateLimited}}
	if h, _ := r.Health(ctx, oracleRef); h.Status != integrations.HealthDegraded || h.Hold == nil {
		t.Errorf("Health under a hold = %+v", h)
	}
}
