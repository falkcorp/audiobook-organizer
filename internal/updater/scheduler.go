// file: internal/updater/scheduler.go
// version: 1.1.0
// guid: 3b4c5d6e-7f8a-9b0c-1d2e-3f4a5b6c7d8e
// last-edited: 2026-11-05

package updater

import (
	"log/slog"
	"time"
)

// SchedulerConfig holds the runtime config for the update scheduler.
type SchedulerConfig struct {
	Enabled   bool
	Channel   string
	CheckMins int
	// WindowStart and WindowEnd (hours 0-23) gated the unattended apply, which
	// R0 removed; the check-only scheduler reads neither (R2 deletes it).
	WindowStart int // hour 0-23
	WindowEnd   int // hour 0-23
}

// Scheduler periodically checks for updates. It never applies one: unattended
// apply is paused (the R0 hotfix's check-only rule), because the updater has
// no automatic way back from a bad swap. An update it finds is recorded as the
// last check, logged, and applied only from Settings.
type Scheduler struct {
	updater *Updater
	ticker  *time.Ticker
	stopCh  chan struct{}
	config  func() SchedulerConfig
}

// NewScheduler creates a scheduler that reads config dynamically via the getter.
func NewScheduler(u *Updater, configGetter func() SchedulerConfig) *Scheduler {
	return &Scheduler{
		updater: u,
		stopCh:  make(chan struct{}),
		config:  configGetter,
	}
}

// Start begins the periodic check loop in a goroutine.
func (s *Scheduler) Start() {
	cfg := s.config()
	if !cfg.Enabled {
		slog.Info("Auto-update scheduler disabled")
		return
	}

	interval := max(time.Duration(cfg.CheckMins)*time.Minute, time.Minute)
	s.ticker = time.NewTicker(interval)

	slog.Info("Auto-update scheduler started checking every minutes", "checkMins", cfg.CheckMins)

	go s.loop()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	if s.ticker != nil {
		s.ticker.Stop()
	}
	close(s.stopCh)
	slog.Info("Auto-update scheduler stopped")
}

func (s *Scheduler) loop() {
	for {
		select {
		case <-s.stopCh:
			return
		case <-s.ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) tick() {
	cfg := s.config()
	if !cfg.Enabled {
		return
	}

	// Check only. CheckUnattended records the result (with AutoApplyPaused)
	// as the last check and logs a newer release at info; nothing is
	// downloaded, swapped or restarted here.
	info, err := s.updater.CheckUnattended(cfg.Channel)
	if err != nil {
		updateLog.Warn("Auto-update check failed: %v", err)
		return
	}

	if !info.UpdateAvailable {
		updateLog.Debug("Auto-update check: no update available (running %s, latest %s, local build %t)", info.CurrentVersion, info.LatestVersion, info.LocalBuild)
	}
}
