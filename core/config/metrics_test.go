// file: core/config/metrics_test.go
// version: 1.0.0
// guid: 0e7a4c19-6b2d-4f85-a3c1-9d5e2b8f4a67
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"errors"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/metrics/metricstest"
)

// configScope builds a Provider and the core "config" scope the kernel
// hands the store (05-config §1.8).
func configScope(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := p.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	sc, err := p.Core("config", "outcome", "section")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// series is the value of family's series whose labels include want.
func series(t *testing.T, p *metrics.Provider, family string, want map[string]string) (float64, bool) {
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
			return sample(m), true
		}
	}
	return 0, false
}

func sample(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return m.GetGauge().GetValue()
}

// TestConfigSeries_Golden (name coined by F3-01): the config scope serves
// exactly the families of testdata/series.golden, every outcome and every
// registered section from zero, and each family moves with what it counts.
func TestConfigSeries_Golden(t *testing.T) {
	p, sc := configScope(t)
	r := newRig(t, rigOpts{
		o:    config.Options{Metrics: sc, Flags: map[config.Path]string{kLevel: "warn"}},
		rows: map[config.Path]string{kCount: `"three"`},
	})
	if err := r.st.Instrument(sc); err == nil {
		t.Error("a second Instrument succeeded")
	}
	for _, oc := range []string{"applied", "unknown_key", "locked", "forbidden", "protected", "invalid", "storage_error"} {
		if v, ok := series(t, p, "mediacore_config_applies_total", map[string]string{"outcome": oc}); !ok || v != 0 {
			t.Errorf("before Load: applies{outcome=%s} = %v, %v; want 0 exported", oc, v, ok)
		}
	}
	r.start()
	metricstest.AssertSeries(t, p, sc, "testdata/series.golden")

	if v, _ := series(t, p, "mediacore_config_quarantined_overrides", nil); v != 1 {
		t.Errorf("quarantined_overrides = %v, want 1", v)
	}
	for _, sec := range []config.Path{configtest.BatcavePath, configtest.WatchPath, configtest.ModulesPath} {
		if _, ok := series(t, p, "mediacore_config_post_commit_failures_total", map[string]string{"section": string(sec)}); !ok {
			t.Errorf("post_commit_failures{section=%s} not exported from zero", sec)
		}
	}

	// One Apply per outcome.
	noModule := manager()
	noModule.Can = func(p string) bool { return p == "core:settings.manage" }
	steps := []struct {
		a   config.Actor
		set map[config.Path]string
	}{
		{admin(), map[config.Path]string{kWorkers: `8`}},            // applied (restart key: pending)
		{admin(), map[config.Path]string{"core.batcave.nope": `1`}}, // unknown_key
		{admin(), map[config.Path]string{kLevel: `"debug"`}},        // locked
		{noModule, map[config.Path]string{kMax: `20`}},              // forbidden
		{manager(), map[config.Path]string{kPath: `"/x"`}},          // protected
		{admin(), map[config.Path]string{kCount: `-1`}},             // invalid
	}
	for i, s := range steps {
		if _, err := r.apply(s.a, s.set); (err == nil) != (i == 0) {
			t.Fatalf("step %d: err = %v", i, err)
		}
	}
	r.cm.Fail(errors.New("the batcomputer is down"))
	if _, err := r.apply(admin(), map[config.Path]string{kSignal: `"spotlight"`}); err == nil {
		t.Fatal("a failing commit applied")
	}
	r.cm.Fail(nil)
	for _, oc := range []string{"applied", "unknown_key", "locked", "forbidden", "protected", "invalid", "storage_error"} {
		if v, _ := series(t, p, "mediacore_config_applies_total", map[string]string{"outcome": oc}); v != 1 {
			t.Errorf("applies{outcome=%s} = %v, want 1", oc, v)
		}
	}
	if v, _ := series(t, p, "mediacore_config_generation", nil); v != 1 {
		t.Errorf("generation = %v, want 1", v)
	}
	if v, _ := series(t, p, "mediacore_config_restart_pending_keys", nil); v != 1 {
		t.Errorf("restart_pending_keys = %v, want 1", v)
	}

	// A failing hook and a panicking subscriber, each counted on its section.
	stop := r.cave.AfterCommit(func(context.Context, *configtest.Batcave, *configtest.Batcave) error {
		return errors.New("the bat-signal is broken")
	})
	defer stop()
	panicked := make(chan struct{})
	stopSub := r.watch.OnChange(func(context.Context, *configtest.Watch, *configtest.Watch) {
		defer close(panicked)
		panic("joker")
	})
	defer stopSub()
	if _, err := r.apply(admin(), map[config.Path]string{kSignal: `"spotlight"`, kMax: `20`}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-panicked:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber never ran")
	}
	if v, _ := series(t, p, "mediacore_config_post_commit_failures_total", map[string]string{"section": string(configtest.BatcavePath)}); v != 1 {
		t.Errorf("post_commit_failures{%s} = %v, want 1", configtest.BatcavePath, v)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, _ := series(t, p, "mediacore_config_subscriber_panics_total", map[string]string{"section": string(configtest.WatchPath)})
		if v == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber_panics{%s} = %v, want 1", configtest.WatchPath, v)
		}
		time.Sleep(10 * time.Millisecond)
	}
	metricstest.AssertNoOverflow(t, p)
}
