// file: core/secrets/metrics_test.go
// version: 1.0.0
// guid: 1c3cc8f4-2c71-4f9e-aa30-5599887f3f57
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
	"github.com/falkcorp/mediacore/storage"
)

// secretsScope builds a Provider and the core "secrets" scope the kernel
// gives the store (05-config §2.8).
func secretsScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: testAppID, Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("secrets", "op", "outcome", "use", "status")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// value is the sample of family's series whose labels include want.
func value(t *testing.T, p *metrics.Provider, family string, want map[string]string) (float64, bool) {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != family {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			for k, v := range want {
				if labels[k] != v {
					continue next
				}
			}
			return sampleOf(m), true
		}
	}
	return 0, false
}

func sampleOf(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	}
	return m.GetUntyped().GetValue()
}

// TestSecretsSeries_Golden (a test name coined by F3-03; 05-config §2.8): the
// secrets scope serves exactly the families of testdata/series.golden, every
// operation series exists at zero, and each family moves with what it
// counts.
func TestSecretsSeries_Golden(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	p, sc := secretsScope(t)
	s := e.open(t, func(o *Options) { o.Metrics = sc })
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")
	metricstest.AssertNoOverflow(t, p)

	ops := "mediacore_secrets_operations_total"
	for _, op := range []string{opGet, opPut, opDelete} {
		for _, oc := range []string{outcomeOK, outcomeNotSet, outcomeKeyUnavailable, outcomeCorrupt, outcomeOutOfScope, outcomeError} {
			if v, ok := value(t, p, ops, map[string]string{"op": op, "outcome": oc}); !ok || v != 0 {
				t.Errorf("%s{op=%s,outcome=%s} = %v (exported %v), want 0", ops, op, oc, v, ok)
			}
		}
	}
	if v, _ := value(t, p, "mediacore_secrets_keyring_keys", map[string]string{"use": "data", "status": "active"}); v != 1 {
		t.Errorf("keyring_keys{data,active} = %v, want 1", v)
	}
	if v, ok := value(t, p, "mediacore_secrets_keyring_keys", map[string]string{"use": "derive", "status": "verify"}); !ok || v != 0 {
		t.Errorf("keyring_keys{derive,verify} = %v (exported %v), want 0", v, ok)
	}

	mustPut(t, s, nameGate, valueVengeance)
	mustGet(t, s, nameGate, valueVengeance)
	if _, _, err := s.Get(ctx, nameModule); err == nil {
		t.Fatal("Get of nothing succeeded")
	}
	if _, _, err := s.Scope("modules.batcave.").Get(ctx, nameModule); err == nil {
		t.Fatal("an out-of-scope Get succeeded")
	}
	if err := s.Delete(ctx, nameGate, byAlfred); err != nil {
		t.Fatal(err)
	}
	b := e.ks.NewBatch()
	if err := s.StageUnreadable(ctx, b, nameOracle, reasonCorrupt, "cutover:m2"); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get(ctx, nameOracle); err == nil {
		t.Fatal("Get of an unreadable marker succeeded")
	}
	for _, c := range []struct {
		op, outcome string
		want        float64
	}{
		{opPut, outcomeOK, 1}, {opGet, outcomeOK, 1}, {opGet, outcomeNotSet, 1},
		{opGet, outcomeOutOfScope, 1}, {opDelete, outcomeOK, 1}, {opGet, outcomeCorrupt, 1},
	} {
		if v, _ := value(t, p, ops, map[string]string{"op": c.op, "outcome": c.outcome}); v != c.want {
			t.Errorf("%s{op=%s,outcome=%s} = %v, want %v", ops, c.op, c.outcome, v, c.want)
		}
	}
	if v, _ := value(t, p, "mediacore_secrets_undecryptable", nil); v != 1 {
		t.Errorf("undecryptable = %v, want 1", v)
	}
	if err := s.Instrument(sc); err == nil {
		t.Error("a second Instrument was accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Gatherer().Gather(); err != nil {
		t.Fatalf("gathering after Close: %v", err)
	}
}
