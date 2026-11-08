// file: internal/updater/register.go
// version: 2.1.2
// guid: 8c9d0a1b-2c3d-4e5f-6a7b-8c9d0a1b2c3d
// last-edited: 2026-11-07
//
// Constructors for the auto-updater and its scheduler, which the server's
// service container holds as Overrides (R1-15 replaced the init()
// registration with explicit construction):
//   - "updater":         *Updater, NewService(the build's version).
//   - "updatescheduler": *SchedulerStarterAdapter wrapping *Scheduler,
//                        NewSchedulerService(updater). Implements
//                        Starter/Stopper for Container.Start / Container.Stop.

package updater

import (
	"context"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// SchedulerStarterAdapter wraps *Scheduler to implement the Starter/Stopper interfaces.
// Scheduler.Start() and Stop() take no context parameter, so we adapt them here.
type SchedulerStarterAdapter struct {
	scheduler *Scheduler
}

// Scheduler returns the wrapped *Scheduler. Nil-safe.
func (a *SchedulerStarterAdapter) Scheduler() *Scheduler {
	if a == nil {
		return nil
	}
	return a.scheduler
}

// Start implements the serviceregistry.Starter interface.
func (a *SchedulerStarterAdapter) Start(_ context.Context) error {
	if a == nil || a.scheduler == nil {
		return nil
	}
	a.scheduler.Start()
	return nil
}

// Stop implements the serviceregistry.Stopper interface.
func (a *SchedulerStarterAdapter) Stop(_ context.Context) error {
	if a == nil || a.scheduler == nil {
		return nil
	}
	a.scheduler.Stop()
	return nil
}

// NewService builds the auto-updater the server's container holds under
// serviceregistry.KeyUpdater, for the build's version; an empty version is
// "dev" (the historical inline default, and what a container built in
// isolation used). It replaces this file's init() registration (R1-15).
func NewService(version string) *Updater {
	if version == "" {
		version = "dev"
	}
	return NewUpdater(version)
}

// NewSchedulerService builds the "updatescheduler" service around upd: the
// update scheduler reading the auto-update settings from config.AppConfig on
// every check (AppConfigSchedulerConfig), wrapped for the container's
// Start/Stop. It replaces this file's init() registration (R1-15).
func NewSchedulerService(upd *Updater) *SchedulerStarterAdapter {
	return &SchedulerStarterAdapter{scheduler: NewScheduler(upd, AppConfigSchedulerConfig)}
}

// AppConfigSchedulerConfig is the scheduler's settings read from
// config.AppConfig at call time, so a UI change applies without a restart.
func AppConfigSchedulerConfig() SchedulerConfig {
	return SchedulerConfig{
		Enabled:     config.AppConfig.AutoUpdate.Enabled,
		Channel:     config.AppConfig.AutoUpdate.Channel,
		CheckMins:   config.AppConfig.AutoUpdate.CheckMinutes,
		WindowStart: config.AppConfig.AutoUpdate.WindowStart,
		WindowEnd:   config.AppConfig.AutoUpdate.WindowEnd,
	}
}
