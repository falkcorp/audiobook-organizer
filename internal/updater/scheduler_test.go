// file: internal/updater/scheduler_test.go
// version: 1.0.0
// guid: 0d5e1f2a-7b3c-4d8e-9f10-a2b3c4d5e6f7
// last-edited: 2026-11-18

package updater

import "testing"

func TestSchedulerConfig(t *testing.T) {
	u := NewUpdater("1.0.0")
	s := NewScheduler(u, func() SchedulerConfig {
		return SchedulerConfig{
			Enabled:     false,
			Channel:     "stable",
			CheckMins:   60,
			WindowStart: 2,
			WindowEnd:   4,
		}
	})

	// Start should be a no-op when disabled
	s.Start()
	// No ticker should be created
	if s.ticker != nil {
		t.Error("ticker should be nil when disabled")
	}
}

// --- Scheduler coverage tests ---

func TestCoverage_NewScheduler(t *testing.T) {
	u := NewUpdater("1.0.0")
	configGetter := func() SchedulerConfig {
		return SchedulerConfig{
			Enabled:     true,
			Channel:     "stable",
			CheckMins:   60,
			WindowStart: 2,
			WindowEnd:   4,
		}
	}
	s := NewScheduler(u, configGetter)
	if s == nil {
		t.Fatal("NewScheduler returned nil")
	}
	if s.updater != u {
		t.Error("updater not set")
	}
}

func TestCoverage_Scheduler_StartStop(t *testing.T) {
	u := NewUpdater("1.0.0")
	s := NewScheduler(u, func() SchedulerConfig {
		return SchedulerConfig{
			Enabled:   true,
			Channel:   "stable",
			CheckMins: 1,
		}
	})

	s.Start()
	if s.ticker == nil {
		t.Error("ticker should be created when enabled")
	}

	s.Stop()
}

func TestCoverage_Scheduler_Disabled(t *testing.T) {
	u := NewUpdater("1.0.0")
	s := NewScheduler(u, func() SchedulerConfig {
		return SchedulerConfig{Enabled: false}
	})

	s.Start()
	if s.ticker != nil {
		t.Error("ticker should be nil when disabled")
	}
}

func TestCoverage_Scheduler_MinInterval(t *testing.T) {
	u := NewUpdater("1.0.0")
	s := NewScheduler(u, func() SchedulerConfig {
		return SchedulerConfig{
			Enabled:   true,
			Channel:   "stable",
			CheckMins: 0, // should default to 1 minute
		}
	})

	s.Start()
	if s.ticker == nil {
		t.Error("ticker should be created even with 0 CheckMins")
	}
	s.Stop()
}

func TestCoverage_SchedulerConfig_Struct(t *testing.T) {
	cfg := SchedulerConfig{
		Enabled:     true,
		Channel:     "develop",
		CheckMins:   30,
		WindowStart: 22,
		WindowEnd:   6,
	}
	if !cfg.Enabled {
		t.Error("Enabled not set")
	}
	if cfg.Channel != "develop" {
		t.Error("Channel not set")
	}
}
